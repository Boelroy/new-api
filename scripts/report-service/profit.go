package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// defaultFXRate is the cold fallback when neither report_fx_rate nor
// report_config['default_fx_rate'] has been configured.
const defaultFXRate = 6.79

// Profit is scoped by *downstream pricing configuration*, not by channel
// type. Any (channel, group) row whose group has an entry in
// report_downstream_pricing (or a per-day override in
// report_downstream_daily) contributes to profit; groups with no
// configured discount are silently skipped in the step1/step2 loops. This
// used to be a hardcoded channel-type filter for Claude only; expanding
// it to cover OpenAI Official, Gemini, etc. is handled purely by
// configuring downstream pricing for the relevant token groups.

// getDefaultFXRate reads the configurable default from report_config and
// returns the hardcoded fallback if unset or malformed.
func getDefaultFXRate() float64 {
	var v string
	err := db.QueryRow(`SELECT value FROM report_config WHERE key='default_fx_rate'`).Scan(&v)
	if err != nil {
		return defaultFXRate
	}
	r, err := strconv.ParseFloat(v, 64)
	if err != nil || r <= 0 {
		return defaultFXRate
	}
	return r
}

// ---- Upstream per-key pricing ----

type keyPricingPayload struct {
	ChannelID    int      `json:"channel_id"`
	QuotaUSD     *float64 `json:"quota_usd"`
	UnitPriceCNY *float64 `json:"unit_price_cny"`
	Note         *string  `json:"note"`
}

// handleSaveKeyPricing upserts upstream pricing for one or more channels.
// Each field is optional — only provided fields overwrite existing values.
func handleSaveKeyPricing(c *gin.Context) {
	var payload []keyPricingPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(payload) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty payload"})
		return
	}
	now := time.Now().Unix()
	saved := 0
	for _, p := range payload {
		if p.ChannelID <= 0 {
			continue
		}
		// Use COALESCE so partial updates don't clobber other fields.
		// On insert, NULL inputs become NULL; on conflict, NULL inputs keep the existing value.
		_, err := db.Exec(`
			INSERT INTO report_key_quotas (channel_id, quota_usd, unit_price_cny, note, updated_at)
			VALUES ($1, COALESCE($2, 0), $3, COALESCE($4,''), $5)
			ON CONFLICT (channel_id) DO UPDATE SET
			  quota_usd = COALESCE($2, report_key_quotas.quota_usd),
			  unit_price_cny = COALESCE($3, report_key_quotas.unit_price_cny),
			  note = COALESCE($4, report_key_quotas.note),
			  updated_at = $5`,
			p.ChannelID, p.QuotaUSD, p.UnitPriceCNY, p.Note, now)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		saved++
	}
	c.JSON(http.StatusOK, gin.H{"saved": saved})
}

// handleBulkSaveKeyPricing accepts raw text "<key><whitespace><price>" per line
// and upserts unit_price_cny by matching channels.key. Empty / comment (#) lines ignored.
func handleBulkSaveKeyPricing(c *gin.Context) {
	var body struct {
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	type lineErr struct {
		Line   int    `json:"line"`
		Reason string `json:"reason"`
	}
	var (
		notFound = make([]string, 0)
		parseErr = make([]lineErr, 0)
		saved    = 0
	)
	now := time.Now().Unix()

	for i, raw := range strings.Split(body.Text, "\n") {
		lineNum := i + 1
		s := strings.TrimSpace(raw)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		// Split on any whitespace (tab, spaces, multiple spaces).
		fields := strings.Fields(s)
		if len(fields) < 2 {
			parseErr = append(parseErr, lineErr{Line: lineNum, Reason: "需要 key 和 price 两列"})
			continue
		}
		key := fields[0]
		price, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			parseErr = append(parseErr, lineErr{Line: lineNum, Reason: "无法解析价格: " + fields[1]})
			continue
		}
		var channelID int
		err = db.QueryRow(`SELECT id FROM channels WHERE key = $1 LIMIT 1`, key).Scan(&channelID)
		if err == sql.ErrNoRows {
			notFound = append(notFound, key)
			continue
		}
		if err != nil {
			parseErr = append(parseErr, lineErr{Line: lineNum, Reason: "查询失败: " + err.Error()})
			continue
		}
		// Upsert only unit_price_cny + updated_at; keep quota_usd/note intact.
		_, err = db.Exec(`
			INSERT INTO report_key_quotas (channel_id, quota_usd, unit_price_cny, note, updated_at)
			VALUES ($1, 0, $2, '', $3)
			ON CONFLICT (channel_id) DO UPDATE SET
			  unit_price_cny = $2,
			  updated_at = $3`,
			channelID, price, now)
		if err != nil {
			parseErr = append(parseErr, lineErr{Line: lineNum, Reason: "写入失败: " + err.Error()})
			continue
		}
		saved++
	}
	c.JSON(http.StatusOK, gin.H{
		"saved":     saved,
		"not_found": notFound,
		"errors":    parseErr,
	})
}

// ---- Downstream group pricing (discount = direct USD multiplier) ----

type DownstreamPricing struct {
	Group     string  `json:"group"`
	Discount  float64 `json:"discount"`
	Note      string  `json:"note"`
	UpdatedAt int64   `json:"updated_at"`
}

func handleListDownstreamPricing(c *gin.Context) {
	rows, err := db.Query(`SELECT "group", discount, COALESCE(note,''), updated_at FROM report_downstream_pricing ORDER BY "group"`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := make([]DownstreamPricing, 0)
	for rows.Next() {
		var d DownstreamPricing
		if err := rows.Scan(&d.Group, &d.Discount, &d.Note, &d.UpdatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out = append(out, d)
	}
	c.JSON(http.StatusOK, out)
}

func handleSaveDownstreamPricing(c *gin.Context) {
	var payload []struct {
		Group    string  `json:"group"`
		Discount float64 `json:"discount"`
		Note     string  `json:"note"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(payload) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty payload"})
		return
	}
	now := time.Now().Unix()
	today := time.Now().UTC().Format("2006-01-02")
	saved := 0
	for _, p := range payload {
		g := strings.TrimSpace(p.Group)
		if g == "" {
			continue
		}
		_, err := db.Exec(`
			INSERT INTO report_downstream_pricing ("group", discount, note, updated_at)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT ("group") DO UPDATE SET discount=$2, note=$3, updated_at=$4`,
			g, p.Discount, p.Note, now)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// Mirror the new price into today's daily row so profit calc picks
		// it up immediately without waiting on the carry-forward loop. Any
		// existing explicit override for today gets replaced (this endpoint
		// is the "set the current price" one).
		if _, err := db.Exec(`
			INSERT INTO report_downstream_daily ("group", date, discount, note, updated_at)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT ("group", date) DO UPDATE SET discount=$3, note=$4, updated_at=$5`,
			g, today, p.Discount, p.Note, now); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		saved++
	}
	c.JSON(http.StatusOK, gin.H{"saved": saved})
}

// ---- FX rate (CNY per USD), per date ----

type FXRate struct {
	Date      string  `json:"date"`
	Rate      float64 `json:"rate"`
	UpdatedAt int64   `json:"updated_at"`
}

func handleListFXRate(c *gin.Context) {
	rows, err := db.Query(`SELECT date, rate, updated_at FROM report_fx_rate ORDER BY date DESC`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := make([]FXRate, 0)
	for rows.Next() {
		var f FXRate
		if err := rows.Scan(&f.Date, &f.Rate, &f.UpdatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out = append(out, f)
	}
	c.JSON(http.StatusOK, gin.H{"rates": out, "default_rate": getDefaultFXRate()})
}

func handleSaveDefaultFXRate(c *gin.Context) {
	var body struct {
		Rate float64 `json:"rate"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.Rate <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rate must be > 0"})
		return
	}
	v := strconv.FormatFloat(body.Rate, 'f', 4, 64)
	_, err := db.Exec(`
		INSERT INTO report_config (key, value, updated_at) VALUES ('default_fx_rate', $1, $2)
		ON CONFLICT (key) DO UPDATE SET value=$1, updated_at=$2`,
		v, time.Now().Unix())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "rate": body.Rate})
}

func handleSaveFXRate(c *gin.Context) {
	var payload []struct {
		Date string  `json:"date"`
		Rate float64 `json:"rate"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(payload) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty payload"})
		return
	}
	now := time.Now().Unix()
	saved := 0
	for _, p := range payload {
		d := strings.TrimSpace(p.Date)
		if d == "" || p.Rate <= 0 {
			continue
		}
		_, err := db.Exec(`
			INSERT INTO report_fx_rate (date, rate, updated_at) VALUES ($1,$2,$3)
			ON CONFLICT (date) DO UPDATE SET rate=$2, updated_at=$3`,
			d, p.Rate, now)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		saved++
	}
	c.JSON(http.StatusOK, gin.H{"saved": saved})
}

func handleDeleteFXRate(c *gin.Context) {
	date := c.Param("date")
	if date == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "date required"})
		return
	}
	if _, err := db.Exec(`DELETE FROM report_fx_rate WHERE date=$1`, date); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func handleDeleteDownstreamPricing(c *gin.Context) {
	group := c.Param("group")
	if group == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group required"})
		return
	}
	if _, err := db.Exec(`DELETE FROM report_downstream_pricing WHERE "group"=$1`, group); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- Daily profit calculation ----

type ProfitDaily struct {
	Date       string  `json:"date"`
	FXRate     float64 `json:"fx_rate"`
	UsedUSD    float64 `json:"used_usd"`
	RevenueUSD float64 `json:"revenue_usd"`
	CostUSD    float64 `json:"cost_usd"`
	ProfitUSD  float64 `json:"profit_usd"`
	ProfitRate float64 `json:"profit_rate"` // (revenue - cost) / revenue
}

type ProfitByKey struct {
	ChannelID    int     `json:"channel_id"`
	ChannelName  string  `json:"channel_name"`
	Tag          string  `json:"tag"`
	Source       string  `json:"source"` // 'main' or 'pipi'
	UsedUSD      float64 `json:"used_usd"`
	UnitPriceCNY float64 `json:"unit_price_cny"`
	CostUSD      float64 `json:"cost_usd"`
}

type ProfitByGroup struct {
	Group      string  `json:"group"`
	UsedUSD    float64 `json:"used_usd"`
	Discount   float64 `json:"discount"`
	RevenueUSD float64 `json:"revenue_usd"`
}

type ProfitByTag struct {
	Tag        string  `json:"tag"`
	Source     string  `json:"source"` // 'main' or 'pipi'
	UsedUSD    float64 `json:"used_usd"`
	CostUSD    float64 `json:"cost_usd"`
	RevenueUSD float64 `json:"revenue_usd"`
	ProfitUSD  float64 `json:"profit_usd"`
	ProfitRate float64 `json:"profit_rate"`
	KeyCount   int     `json:"key_count"`
}

type MissingPricing struct {
	ChannelIDs []int    `json:"channel_ids"`
	Groups     []string `json:"groups"`
}

// ProfitByRemoteChannel is one row of the "Remote Channels" section in
// the profit report. Each row corresponds to a single external channel
// on a saved Remote profile; used_usd is the sum of daily deltas from
// remote_channel_snapshot, cost is unit_price_cny × used_usd / fx, and
// revenue is downstream_cny × used_usd / fx (looked up per date, with
// yesterday's price carrying forward).
type ProfitByRemoteChannel struct {
	ProfileID    int64   `json:"profile_id"`
	ProfileName  string  `json:"profile_name"`
	ChannelID    int64   `json:"channel_id"`
	ChannelName  string  `json:"channel_name"`
	UsedUSD      float64 `json:"used_usd"`
	CostUSD      float64 `json:"cost_usd"`
	RevenueUSD   float64 `json:"revenue_usd"`
	ProfitUSD    float64 `json:"profit_usd"`
	ProfitRate   float64 `json:"profit_rate"`
	// Fields carried through for display / debugging. All optional.
	UnitPriceCNY       *float64 `json:"unit_price_cny,omitempty"`
	DownstreamDiscount *float64 `json:"downstream_discount,omitempty"` // USD → USD multiplier
}

type ProfitSummary struct {
	Start           string                  `json:"start"`
	End             string                  `json:"end"`
	UsedUSD         float64                 `json:"used_usd"`
	RevenueUSD      float64                 `json:"revenue_usd"`
	CostUSD         float64                 `json:"cost_usd"`
	ProfitUSD       float64                 `json:"profit_usd"`
	ProfitRate      float64                 `json:"profit_rate"`
	Daily           []ProfitDaily           `json:"daily"`
	ByKey           []ProfitByKey           `json:"by_key"`
	ByTag           []ProfitByTag           `json:"by_tag"`
	ByGroup         []ProfitByGroup         `json:"by_group"`
	ByRemoteChannel []ProfitByRemoteChannel `json:"by_remote_channel"`
	// Aggregate totals for the remote-channels section so the frontend
	// can render summary numbers without re-summing.
	RemoteUsedUSD    float64 `json:"remote_used_usd"`
	RemoteCostUSD    float64 `json:"remote_cost_usd"`
	RemoteRevenueUSD float64 `json:"remote_revenue_usd"`
	RemoteProfitUSD  float64 `json:"remote_profit_usd"`
	MissingPricing   MissingPricing         `json:"missing_pricing"`
}

// step1Row holds a non-pipi (date, channel_id, group) aggregation row.
type step1Row struct {
	date        string
	channelID   int
	channelName string
	channelTag  string
	tokenGroup  string
	usedUSD     float64
	upPrice     sql.NullFloat64
}

// step2Row holds pipi revenue-side aggregation (System 1's logs for tag=pipi channels).
type step2Row struct {
	date       string
	tokenGroup string
	revenueUSD float64
}

// step3Row holds pipi cost-side aggregation (synced from System 2).
type step3Row struct {
	date         string
	channelID    int
	channelName  string
	channelTag   string
	costUSD      float64
	unitPriceCNY sql.NullFloat64
}

func handleProfitDaily(c *gin.Context) {
	startDate := c.DefaultQuery("start", time.Now().UTC().AddDate(0, 0, -6).Format("2006-01-02"))
	endDate := c.DefaultQuery("end", time.Now().UTC().Format("2006-01-02"))

	// --- Downstream discount lookup (group + date -> multiplier) ---
	// Per-day table is the authoritative source; the legacy single-value
	// pricing table only provides a last-resort fallback for groups that
	// never got a daily row (e.g. a channel that was priced but never had
	// its bootstrap seed run yet).
	dailyPrices, err := loadDownstreamDailyPricing()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load downstream daily: " + err.Error()})
		return
	}
	fallbackDiscount := map[string]float64{}
	dpRows, err := db.Query(`SELECT "group", discount FROM report_downstream_pricing`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load downstream pricing: " + err.Error()})
		return
	}
	for dpRows.Next() {
		var g string
		var p float64
		if err := dpRows.Scan(&g, &p); err == nil {
			fallbackDiscount[g] = p
		}
	}
	dpRows.Close()

	// getDiscount walks the ascending list of (date, discount) rows for
	// the group and returns the last entry whose date <= day. Falls back
	// to the single-value pricing table when no daily row is <= day for
	// that group. Returns ok=false only when neither is available; the
	// caller flags that in MissingPricing.
	getDiscount := func(group, day string) (float64, bool) {
		if rows, ok := dailyPrices[group]; ok {
			best, hit := 0.0, false
			for _, r := range rows {
				if r.date > day {
					break
				}
				best, hit = r.discount, true
			}
			if hit {
				return best, true
			}
		}
		if v, ok := fallbackDiscount[group]; ok {
			return v, true
		}
		return 0, false
	}

	// --- FX rate lookup (date -> CNY/USD) ---
	fx := map[string]float64{}
	fxRows, err := db.Query(`SELECT date, rate FROM report_fx_rate WHERE date BETWEEN $1 AND $2`, startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load fx: " + err.Error()})
		return
	}
	for fxRows.Next() {
		var d string
		var r float64
		if err := fxRows.Scan(&d, &r); err == nil {
			fx[d] = r
		}
	}
	fxRows.Close()
	def := getDefaultFXRate()
	getFX := func(d string) float64 {
		if r, ok := fx[d]; ok && r > 0 {
			return r
		}
		return def
	}

	step1, err := loadStep1(startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "step1: " + err.Error()})
		return
	}
	step2, err := loadStep2(startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "step2: " + err.Error()})
		return
	}
	step3, err := loadStep3(startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "step3: " + err.Error()})
		return
	}

	missingChIDs := map[int]bool{}

	daily := map[string]*ProfitDaily{}
	byKey := map[int]*ProfitByKey{}
	byKeyPipi := map[int]*ProfitByKey{}
	byGroup := map[string]*ProfitByGroup{}
	// by_tag: bucketed independently of by_key so revenue can be attributed
	// per-tag (main side) and pipi gets a single explicit "pipi" tag bucket.
	type tagBucket struct {
		usedUSD, costUSD, revenueUSD float64
		channels                     map[int]struct{}
	}
	mainTagAgg := map[string]*tagBucket{} // keyed by channel.tag
	pipiTagAgg := &tagBucket{channels: map[int]struct{}{}}
	bumpTagBucket := func(b *tagBucket, channelID int) {
		if b.channels == nil {
			b.channels = map[int]struct{}{}
		}
		if channelID > 0 {
			b.channels[channelID] = struct{}{}
		}
	}

	getDay := func(d string) *ProfitDaily {
		if v, ok := daily[d]; ok {
			return v
		}
		v := &ProfitDaily{Date: d, FXRate: getFX(d)}
		daily[d] = v
		return v
	}
	getKey := func(m map[int]*ProfitByKey, id int, name, tag, source string, price float64) *ProfitByKey {
		if v, ok := m[id]; ok {
			return v
		}
		v := &ProfitByKey{ChannelID: id, ChannelName: name, Tag: tag, Source: source, UnitPriceCNY: price}
		m[id] = v
		return v
	}
	getGroup := func(g string, dis float64) *ProfitByGroup {
		if v, ok := byGroup[g]; ok {
			return v
		}
		v := &ProfitByGroup{Group: g, Discount: dis}
		byGroup[g] = v
		return v
	}

	// Step 1 — non-pipi: revenue + cost both anchored on System 1's used_usd.
	// Groups without a configured downstream price are silently dropped: the
	// profit report is scoped to what has been priced, not to what has
	// traffic. Missing per-channel upstream prices (unit_price_cny) still
	// surface via MissingPricing.ChannelIDs so operators can fix them.
	for _, r := range step1 {
		dis, dok := getDiscount(r.tokenGroup, r.date)
		if !dok {
			continue
		}
		var upP float64
		if r.upPrice.Valid {
			upP = r.upPrice.Float64
		} else {
			missingChIDs[r.channelID] = true
		}
		rate := getFX(r.date)

		costUSD := r.usedUSD * upP / rate
		revUSD := r.usedUSD * dis

		d := getDay(r.date)
		d.UsedUSD += r.usedUSD
		d.CostUSD += costUSD
		d.RevenueUSD += revUSD

		k := getKey(byKey, r.channelID, r.channelName, r.channelTag, "main", upP)
		k.UsedUSD += r.usedUSD
		k.CostUSD += costUSD

		g := getGroup(r.tokenGroup, dis)
		g.UsedUSD += r.usedUSD
		g.RevenueUSD += revUSD

		tb, ok := mainTagAgg[r.channelTag]
		if !ok {
			tb = &tagBucket{}
			mainTagAgg[r.channelTag] = tb
		}
		tb.usedUSD += r.usedUSD
		tb.costUSD += costUSD
		tb.revenueUSD += revUSD
		bumpTagBucket(tb, r.channelID)
	}

	// Step 2 — pipi revenue side (downstream group lives in System 1's logs).
	// Same scoping rule as step 1: unpriced groups are dropped.
	for _, r := range step2 {
		dis, dok := getDiscount(r.tokenGroup, r.date)
		if !dok {
			continue
		}
		revUSD := r.revenueUSD * dis

		d := getDay(r.date)
		d.UsedUSD += r.revenueUSD
		d.RevenueUSD += revUSD

		g := getGroup(r.tokenGroup, dis)
		g.UsedUSD += r.revenueUSD
		g.RevenueUSD += revUSD

		// pipi tag bucket: used + revenue from System 1's perspective
		pipiTagAgg.usedUSD += r.revenueUSD
		pipiTagAgg.revenueUSD += revUSD
	}

	// Step 3 — pipi cost side (per-sub-key with its own CNY price, converted by daily FX)
	for _, r := range step3 {
		var upP float64
		if r.unitPriceCNY.Valid {
			upP = r.unitPriceCNY.Float64
		} else {
			missingChIDs[r.channelID] = true
		}
		rate := getFX(r.date)
		costUSD := r.costUSD * upP / rate

		d := getDay(r.date)
		d.CostUSD += costUSD

		k := getKey(byKeyPipi, r.channelID, r.channelName, r.channelTag, "pipi", upP)
		k.UsedUSD += r.costUSD
		k.CostUSD += costUSD

		// pipi tag bucket: cost comes from System 2 sync, distinct channels
		// counted for key_count
		pipiTagAgg.costUSD += costUSD
		bumpTagBucket(pipiTagAgg, r.channelID)
	}

	summary := ProfitSummary{Start: startDate, End: endDate}

	// --- Remote Channels layer ---
	// Load first so per-day totals can be folded into `daily` before the
	// summary is finalized. remoteRows drive the standalone Remote table
	// on the frontend; remotePerDay is what makes the top summary and the
	// daily bar chart reflect main + pipi + remote as a single blended
	// total. RemoteUsedUSD / RemoteCostUSD / RemoteRevenueUSD /
	// RemoteProfitUSD stay populated so the UI can still break out Remote
	// as its own section.
	remoteRows, remotePerDay, err := loadRemoteDaily(startDate, endDate, getFX)
	if err != nil {
		log.Printf("[profit] loadRemoteDaily: %v", err)
	}
	for date, tot := range remotePerDay {
		d := getDay(date)
		d.UsedUSD += tot.UsedUSD
		d.CostUSD += tot.CostUSD
		d.RevenueUSD += tot.RevenueUSD
	}

	for _, d := range daily {
		d.ProfitUSD = d.RevenueUSD - d.CostUSD
		if d.UsedUSD > 0 {
			d.ProfitRate = d.ProfitUSD / d.UsedUSD
		}
		d.UsedUSD = roundTo(d.UsedUSD, 4)
		d.CostUSD = roundTo(d.CostUSD, 4)
		d.RevenueUSD = roundTo(d.RevenueUSD, 4)
		d.ProfitUSD = roundTo(d.ProfitUSD, 4)
		d.ProfitRate = roundTo(d.ProfitRate, 4)
		summary.UsedUSD += d.UsedUSD
		summary.CostUSD += d.CostUSD
		summary.RevenueUSD += d.RevenueUSD
	}

	dailyList := make([]ProfitDaily, 0, len(daily))
	for _, d := range daily {
		dailyList = append(dailyList, *d)
	}
	for i := 1; i < len(dailyList); i++ {
		for j := i; j > 0 && dailyList[j-1].Date > dailyList[j].Date; j-- {
			dailyList[j-1], dailyList[j] = dailyList[j], dailyList[j-1]
		}
	}
	summary.Daily = dailyList

	for _, v := range byKey {
		v.UsedUSD = roundTo(v.UsedUSD, 4)
		v.CostUSD = roundTo(v.CostUSD, 4)
		summary.ByKey = append(summary.ByKey, *v)
	}
	for _, v := range byKeyPipi {
		v.UsedUSD = roundTo(v.UsedUSD, 4)
		v.CostUSD = roundTo(v.CostUSD, 4)
		summary.ByKey = append(summary.ByKey, *v)
	}
	for _, v := range byGroup {
		v.UsedUSD = roundTo(v.UsedUSD, 4)
		v.RevenueUSD = roundTo(v.RevenueUSD, 4)
		summary.ByGroup = append(summary.ByGroup, *v)
	}

	// Emit by_tag from the dedicated buckets (independent of by_key so we
	// can attribute revenue per main-side tag and force "pipi" as the tag
	// label for the System 2 sync bucket).
	emit := func(tag, source string, b *tagBucket) {
		out := ProfitByTag{
			Tag:        tag,
			Source:     source,
			UsedUSD:    roundTo(b.usedUSD, 4),
			CostUSD:    roundTo(b.costUSD, 4),
			RevenueUSD: roundTo(b.revenueUSD, 4),
			KeyCount:   len(b.channels),
		}
		out.ProfitUSD = roundTo(out.RevenueUSD-out.CostUSD, 4)
		if out.UsedUSD > 0 {
			out.ProfitRate = roundTo(out.ProfitUSD/out.UsedUSD, 4)
		}
		summary.ByTag = append(summary.ByTag, out)
	}
	for tag, b := range mainTagAgg {
		emit(tag, "main", b)
	}
	if pipiTagAgg.usedUSD > 0 || pipiTagAgg.costUSD > 0 || pipiTagAgg.revenueUSD > 0 {
		emit("pipi", "pipi", pipiTagAgg)
	}

	// --- Remote-side by_tag buckets ---
	// Aggregate remote channels per profile so the ByTag breakdown adds
	// up to the (now blended) top total. Tag label is the profile name;
	// source is "remote" to distinguish from the main / pipi buckets.
	type remoteProfileAgg struct {
		name     string
		buckets  tagBucket
	}
	remoteByProfile := map[int64]*remoteProfileAgg{}
	for _, r := range remoteRows {
		a, ok := remoteByProfile[r.ProfileID]
		if !ok {
			a = &remoteProfileAgg{name: r.ProfileName, buckets: tagBucket{channels: map[int]struct{}{}}}
			remoteByProfile[r.ProfileID] = a
		}
		a.buckets.usedUSD += r.UsedUSD
		a.buckets.costUSD += r.CostUSD
		a.buckets.revenueUSD += r.RevenueUSD
		if r.ChannelID > 0 {
			a.buckets.channels[int(r.ChannelID)] = struct{}{}
		}
	}
	for pid, agg := range remoteByProfile {
		label := agg.name
		if label == "" {
			label = fmt.Sprintf("profile#%d", pid)
		}
		emit(label, "remote", &agg.buckets)
	}

	// Populate the Remote breakdown section. Totals mirror what got folded
	// into `daily` above so the standalone Remote row on the UI still adds
	// up the same way. summary.UsedUSD / CostUSD / RevenueUSD already
	// include these numbers via the fold; the Remote* fields are for the
	// dedicated breakdown card only.
	summary.ByRemoteChannel = remoteRows
	for _, r := range remoteRows {
		summary.RemoteUsedUSD += r.UsedUSD
		summary.RemoteCostUSD += r.CostUSD
		summary.RemoteRevenueUSD += r.RevenueUSD
	}
	summary.RemoteUsedUSD = roundTo(summary.RemoteUsedUSD, 4)
	summary.RemoteCostUSD = roundTo(summary.RemoteCostUSD, 4)
	summary.RemoteRevenueUSD = roundTo(summary.RemoteRevenueUSD, 4)
	summary.RemoteProfitUSD = roundTo(summary.RemoteRevenueUSD-summary.RemoteCostUSD, 4)

	summary.UsedUSD = roundTo(summary.UsedUSD, 4)
	summary.CostUSD = roundTo(summary.CostUSD, 4)
	summary.RevenueUSD = roundTo(summary.RevenueUSD, 4)
	summary.ProfitUSD = roundTo(summary.RevenueUSD-summary.CostUSD, 4)
	if summary.UsedUSD > 0 {
		summary.ProfitRate = roundTo((summary.RevenueUSD-summary.CostUSD)/summary.UsedUSD, 4)
	}

	for id := range missingChIDs {
		summary.MissingPricing.ChannelIDs = append(summary.MissingPricing.ChannelIDs, id)
	}
	// MissingPricing.Groups is retained on the response struct for wire
	// compatibility but is no longer populated: unpriced groups are dropped
	// upstream in the step1/step2 loops rather than surfaced as "misconfigured".

	c.JSON(http.StatusOK, summary)
}

// remoteDailyDelta is one (channel, date) row derived from snapshots.
type remoteDailyDelta struct {
	profileID   int64
	profileName string
	channelID   int64
	channelName string
	date        string
	usedUSD     float64
}

// remoteDailyPerDate holds the aggregate remote used/cost/revenue for one
// date across every profile+channel that had activity. Used by
// handleProfitDaily to fold Remote Channels into the daily+summary
// totals so the top summary reflects the true blended profit.
type remoteDailyPerDate struct {
	UsedUSD    float64
	CostUSD    float64
	RevenueUSD float64
}

// loadRemoteDaily walks remote_channel_snapshot, computes per-day
// used-quota deltas (last snapshot of day D − last of D−1), joins with
// remote_channel_meta.unit_price_cny and per-date downstream_cny, and
// aggregates per (profile, channel) into a profit row for the window.
// Also returns a per-date rollup so callers can merge remote activity
// into the main daily aggregation without a second query.
// Returns an empty slice when no snapshots or no downstream configured
// intersect the window.
func loadRemoteDaily(startDate, endDate string, getFX func(string) float64) ([]ProfitByRemoteChannel, map[string]*remoteDailyPerDate, error) {
	// Grow the snapshot window by 1 day on each side so the "last of D−1"
	// lookup for the start-of-window date still finds a baseline, and
	// end-of-window carryover isn't lost when the tick lands after midnight.
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return nil, nil, fmt.Errorf("parse startDate: %v", err)
	}
	end, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return nil, nil, fmt.Errorf("parse endDate: %v", err)
	}
	startExpanded := start.AddDate(0, 0, -1).Unix()
	endExpanded := end.AddDate(0, 0, 2).Unix() // end + 1 day margin, exclusive upper

	// Pull the last snapshot per (profile, channel, UTC date) — one row per
	// channel-day. DISTINCT ON is Postgres-only which is fine here.
	rows, err := db.Query(
		`WITH per_day AS (
			SELECT profile_id, remote_channel_id,
			       to_char(to_timestamp(captured_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS date,
			       MAX(captured_at) AS last_ts
			  FROM remote_channel_snapshot
			 WHERE captured_at >= $1 AND captured_at < $2
			 GROUP BY profile_id, remote_channel_id, date
		)
		SELECT s.profile_id, s.remote_channel_id, p.date, s.used_quota
		  FROM per_day p
		  JOIN remote_channel_snapshot s
		    ON s.profile_id = p.profile_id
		   AND s.remote_channel_id = p.remote_channel_id
		   AND s.captured_at = p.last_ts
		 ORDER BY s.profile_id, s.remote_channel_id, p.date`,
		startExpanded, endExpanded,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("query snapshots: %v", err)
	}
	defer rows.Close()

	type lastSnap struct {
		date  string
		quota int64
	}
	// (profile, channel) → chronological list of (date, quota)
	series := make(map[[2]int64][]lastSnap)
	for rows.Next() {
		var pid, chID, quota int64
		var date string
		if err := rows.Scan(&pid, &chID, &date, &quota); err != nil {
			return nil, nil, err
		}
		k := [2]int64{pid, chID}
		series[k] = append(series[k], lastSnap{date: date, quota: quota})
	}

	// Pre-window baseline: the last snapshot strictly before startDate,
	// per (profile, channel). If a channel first appeared inside the
	// window, no baseline row exists and the first day's delta will be
	// (first-snapshot-in-window − 0). This keeps day 1 from silently
	// dropping the entire "starting used_quota" onto the floor.
	baselines := make(map[[2]int64]int64)
	{
		bRows, err := db.Query(
			`SELECT DISTINCT ON (profile_id, remote_channel_id)
			        profile_id, remote_channel_id, used_quota
			   FROM remote_channel_snapshot
			  WHERE captured_at < $1
			  ORDER BY profile_id, remote_channel_id, captured_at DESC`,
			start.Unix(),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("query baseline: %v", err)
		}
		for bRows.Next() {
			var pid, chID, q int64
			if err := bRows.Scan(&pid, &chID, &q); err != nil {
				bRows.Close()
				return nil, nil, err
			}
			baselines[[2]int64{pid, chID}] = q
		}
		bRows.Close()
	}

	// Deltas per (profile, channel, date). Each day's delta is the last
	// snapshot of that day minus the previous "known" value (last of the
	// day before, or the pre-window baseline for day 1). Cumulative
	// counters can only decrease across a wipe — negative diffs are
	// dropped so the row isn't credited with fake negative revenue.
	deltas := make(map[[2]int64]map[string]int64) // key → date → raw quota units
	for k, snaps := range series {
		prev := baselines[k] // zero when the channel is new inside the window
		for _, s := range snaps {
			d := s.quota - prev
			prev = s.quota
			if d < 0 {
				continue
			}
			if deltas[k] == nil {
				deltas[k] = make(map[string]int64)
			}
			deltas[k][s.date] += d
		}
	}

	// Look up per-channel unit_price_cny (single value) and per-date
	// downstream_cny (walked with "latest ≤ day" semantics below).
	metaRows, err := db.Query(`SELECT profile_id, remote_channel_id, unit_price_cny
	                             FROM remote_channel_meta`)
	if err != nil {
		return nil, nil, fmt.Errorf("query meta: %v", err)
	}
	defer metaRows.Close()
	metaPrice := make(map[[2]int64]float64)
	for metaRows.Next() {
		var pid, chID int64
		var price sql.NullFloat64
		if err := metaRows.Scan(&pid, &chID, &price); err != nil {
			return nil, nil, err
		}
		if price.Valid {
			metaPrice[[2]int64{pid, chID}] = price.Float64
		}
	}

	// Per-profile per-day downstream discount. One multiplier applies to
	// the whole profile on a given day; missing days fall back to the
	// latest configured date ≤ that day (so setting a rate on 2026-07-01
	// carries all the way to today until a new row overrides).
	dsRows, err := db.Query(
		`SELECT profile_id, date, discount
		   FROM remote_downstream_daily
		  WHERE date <= $1
		  ORDER BY profile_id, date`,
		endDate,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("query downstream_daily: %v", err)
	}
	defer dsRows.Close()
	type dsRow struct {
		date  string
		value float64
	}
	// profile_id → ascending list of (date, discount)
	dsByProfile := make(map[int64][]dsRow)
	for dsRows.Next() {
		var pid int64
		var date string
		var value float64
		if err := dsRows.Scan(&pid, &date, &value); err != nil {
			return nil, nil, err
		}
		dsByProfile[pid] = append(dsByProfile[pid], dsRow{date: date, value: value})
	}
	getProfileDiscount := func(pid int64, day string) (float64, bool) {
		best, ok := 0.0, false
		for _, r := range dsByProfile[pid] {
			if r.date > day {
				break
			}
			best = r.value
			ok = true
		}
		return best, ok
	}

	// Profile name lookup for a friendlier UI.
	profNames := make(map[int64]string)
	pRows, err := db.Query(`SELECT id, name FROM remote_newapi_profile`)
	if err == nil {
		for pRows.Next() {
			var id int64
			var name string
			if err := pRows.Scan(&id, &name); err == nil {
				profNames[id] = name
			}
		}
		pRows.Close()
	}

	// Latest snapshot's channel name for the row label (uses the mirror so
	// we don't have to touch the remote here).
	chNames := make(map[[2]int64]string)
	nRows, err := db.Query(`SELECT profile_id, remote_channel_id, name FROM remote_channel_current`)
	if err == nil {
		for nRows.Next() {
			var pid, chID int64
			var name string
			if err := nRows.Scan(&pid, &chID, &name); err == nil {
				chNames[[2]int64{pid, chID}] = name
			}
		}
		nRows.Close()
	}

	// perDate accumulates the per-day rollup used by handleProfitDaily to
	// fold Remote into the main daily aggregation. Grown lazily so dates
	// with no remote activity stay absent from the map.
	perDate := make(map[string]*remoteDailyPerDate)
	bumpPerDate := func(date string, used, cost, rev float64) {
		if used == 0 && cost == 0 && rev == 0 {
			return
		}
		d, ok := perDate[date]
		if !ok {
			d = &remoteDailyPerDate{}
			perDate[date] = d
		}
		d.UsedUSD += used
		d.CostUSD += cost
		d.RevenueUSD += rev
	}

	out := make([]ProfitByRemoteChannel, 0, len(deltas))
	for k, dates := range deltas {
		var usedUSD, costUSD, revenueUSD float64
		hasPrice := false
		hasDiscount := false
		var priceValue float64
		var discountMax float64 // largest multiplier seen in window (for display)
		if p, ok := metaPrice[k]; ok {
			hasPrice = true
			priceValue = p
		}
		for date, quota := range dates {
			if date < startDate || date > endDate {
				continue
			}
			day := usdFromRawQuota(quota)
			usedUSD += day
			fx := getFX(date)
			if fx <= 0 {
				fx = defaultFXRate
			}
			var dayCost, dayRev float64
			if hasPrice {
				dayCost = day * priceValue / fx
				costUSD += dayCost
			}
			// Per-profile per-day downstream discount is a USD-to-USD
			// multiplier — matches the existing report_downstream_pricing
			// convention on the main side.
			if d, ok := getProfileDiscount(k[0], date); ok {
				hasDiscount = true
				if d > discountMax {
					discountMax = d
				}
				dayRev = day * d
				revenueUSD += dayRev
			}
			bumpPerDate(date, day, dayCost, dayRev)
		}
		if usedUSD == 0 && costUSD == 0 && revenueUSD == 0 {
			continue
		}
		row := ProfitByRemoteChannel{
			ProfileID:   k[0],
			ProfileName: profNames[k[0]],
			ChannelID:   k[1],
			ChannelName: chNames[k],
			UsedUSD:     roundTo(usedUSD, 4),
			CostUSD:     roundTo(costUSD, 4),
			RevenueUSD:  roundTo(revenueUSD, 4),
		}
		if hasPrice {
			v := priceValue
			row.UnitPriceCNY = &v
		}
		if hasDiscount {
			v := discountMax
			row.DownstreamDiscount = &v
		}
		row.ProfitUSD = roundTo(row.RevenueUSD-row.CostUSD, 4)
		if row.UsedUSD > 0 {
			row.ProfitRate = roundTo((row.RevenueUSD-row.CostUSD)/row.UsedUSD, 4)
		}
		out = append(out, row)
	}
	// Descending profit so the biggest movers show up first.
	sort.Slice(out, func(i, j int) bool { return out[i].ProfitUSD > out[j].ProfitUSD })
	return out, perDate, nil
}

// usdFromRawQuota converts new-api's raw quota-unit counter into USD.
// Kept local to profit.go so the profit math doesn't depend on the wider
// service's helpers.
func usdFromRawQuota(q int64) float64 {
	return float64(q) / 500000.0
}

func loadStep1(startDate, endDate string) ([]step1Row, error) {
	q := `
		SELECT
		    LEFT(r.hour,10) AS date,
		    r.channel_id,
		    COALESCE(r.channel_name,'') AS channel_name,
		    COALESCE(c.tag,'') AS channel_tag,
		    COALESCE(r."group",'') AS token_group,
		    SUM(r.total_cost) AS used_usd,
		    q.unit_price_cny
		FROM report_daily_agg r
		JOIN channels c ON c.id = r.channel_id
		LEFT JOIN report_key_quotas q ON q.channel_id = r.channel_id
		WHERE LEFT(r.hour,10) BETWEEN $1 AND $2
		  AND COALESCE(c.tag,'') <> 'pipi'
		GROUP BY LEFT(r.hour,10), r.channel_id, COALESCE(r.channel_name,''),
		         COALESCE(c.tag,''), COALESCE(r."group",''), q.unit_price_cny
	`
	rows, err := db.Query(q, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []step1Row{}
	for rows.Next() {
		var r step1Row
		if err := rows.Scan(&r.date, &r.channelID, &r.channelName, &r.channelTag, &r.tokenGroup, &r.usedUSD, &r.upPrice); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func loadStep2(startDate, endDate string) ([]step2Row, error) {
	q := `
		SELECT LEFT(r.hour,10) AS date, COALESCE(r."group",'') AS token_group, SUM(r.total_cost) AS used_usd
		FROM report_daily_agg r
		JOIN channels c ON c.id = r.channel_id
		WHERE LEFT(r.hour,10) BETWEEN $1 AND $2
		  AND COALESCE(c.tag,'') = 'pipi'
		GROUP BY LEFT(r.hour,10), COALESCE(r."group",'')
	`
	rows, err := db.Query(q, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []step2Row{}
	for rows.Next() {
		var r step2Row
		if err := rows.Scan(&r.date, &r.tokenGroup, &r.revenueUSD); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- Per-day downstream pricing (group + date -> multiplier) ----

// downstreamDailyRow is one (date, discount) tuple within a group's
// ordered history. Only used internally by getDiscount().
type downstreamDailyRow struct {
	date     string
	discount float64
}

// DownstreamDailyItem is the JSON shape returned by the list handler
// and accepted by the upsert handler.
type DownstreamDailyItem struct {
	Group     string  `json:"group"`
	Date      string  `json:"date"`
	Discount  float64 `json:"discount"`
	Note      string  `json:"note"`
	UpdatedAt int64   `json:"updated_at"`
}

// loadDownstreamDailyPricing returns every configured (group, date) row
// grouped by group and sorted ascending by date so callers can walk the
// list once and pick the latest row ≤ target date.
func loadDownstreamDailyPricing() (map[string][]downstreamDailyRow, error) {
	rows, err := db.Query(
		`SELECT "group", date, discount FROM report_downstream_daily ORDER BY "group", date ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]downstreamDailyRow)
	for rows.Next() {
		var g, d string
		var v float64
		if err := rows.Scan(&g, &d, &v); err != nil {
			return nil, err
		}
		out[g] = append(out[g], downstreamDailyRow{date: d, discount: v})
	}
	return out, rows.Err()
}

// handleListDownstreamDaily returns configured rows, optionally filtered
// by group and date range. Frontend uses it to render the editor grid.
func handleListDownstreamDaily(c *gin.Context) {
	group := strings.TrimSpace(c.Query("group"))
	start := strings.TrimSpace(c.Query("start"))
	end := strings.TrimSpace(c.Query("end"))
	q := `SELECT "group", date, discount, note, updated_at FROM report_downstream_daily`
	args := []any{}
	conds := []string{}
	if group != "" {
		args = append(args, group)
		conds = append(conds, fmt.Sprintf(`"group" = $%d`, len(args)))
	}
	if start != "" {
		args = append(args, start)
		conds = append(conds, fmt.Sprintf(`date >= $%d`, len(args)))
	}
	if end != "" {
		args = append(args, end)
		conds = append(conds, fmt.Sprintf(`date <= $%d`, len(args)))
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += ` ORDER BY "group", date DESC`

	rows, err := db.Query(q, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := make([]DownstreamDailyItem, 0)
	for rows.Next() {
		var r DownstreamDailyItem
		if err := rows.Scan(&r.Group, &r.Date, &r.Discount, &r.Note, &r.UpdatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out = append(out, r)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// handleSaveDownstreamDaily upserts one or more (group, date) rows. The
// bootstrap sentinel date '1970-01-01' is rejected — operators should
// edit real business dates, not the historical baseline.
func handleSaveDownstreamDaily(c *gin.Context) {
	var payload []struct {
		Group    string  `json:"group"`
		Date     string  `json:"date"`
		Discount float64 `json:"discount"`
		Note     string  `json:"note"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(payload) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty payload"})
		return
	}
	now := time.Now().Unix()
	saved := 0
	for _, p := range payload {
		g := strings.TrimSpace(p.Group)
		if g == "" {
			continue
		}
		date := strings.TrimSpace(p.Date)
		if date == "" {
			date = time.Now().UTC().Format("2006-01-02")
		}
		if _, err := time.Parse("2006-01-02", date); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "date must be YYYY-MM-DD: " + date})
			return
		}
		if p.Discount < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "discount must be ≥ 0"})
			return
		}
		if _, err := db.Exec(`
			INSERT INTO report_downstream_daily ("group", date, discount, note, updated_at)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT ("group", date) DO UPDATE SET
			  discount=EXCLUDED.discount,
			  note=EXCLUDED.note,
			  updated_at=EXCLUDED.updated_at`,
			g, date, p.Discount, strings.TrimSpace(p.Note), now); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		saved++
	}
	c.JSON(http.StatusOK, gin.H{"saved": saved})
}

func handleDeleteDownstreamDaily(c *gin.Context) {
	group := strings.TrimSpace(c.Query("group"))
	date := strings.TrimSpace(c.Query("date"))
	if group == "" || date == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group and date are required"})
		return
	}
	if _, err := db.Exec(
		`DELETE FROM report_downstream_daily WHERE "group"=$1 AND date=$2`,
		group, date,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// startDownstreamDailyCarryForward keeps report_downstream_daily current
// by copying each group's most recent past discount into today's row when
// today has no explicit entry. Marks the auto-created row with note
// 'auto-carry' so the operator can spot it in the UI. Runs on startup
// (after a short delay) and every 6h thereafter — DDL-only fills are
// idempotent thanks to ON CONFLICT DO NOTHING.
func startDownstreamDailyCarryForward() {
	go func() {
		time.Sleep(15 * time.Second)
		if err := runDownstreamDailyCarryForward(); err != nil {
			log.Printf("[profit] downstream carry-forward initial: %v", err)
		}
		t := time.NewTicker(6 * time.Hour)
		for range t.C {
			if err := runDownstreamDailyCarryForward(); err != nil {
				log.Printf("[profit] downstream carry-forward: %v", err)
			}
		}
	}()
}

func runDownstreamDailyCarryForward() error {
	today := time.Now().UTC().Format("2006-01-02")
	now := time.Now().Unix()
	_, err := db.Exec(`
		INSERT INTO report_downstream_daily ("group", date, discount, note, updated_at)
		SELECT DISTINCT ON (d."group")
		       d."group", $1, d.discount, 'auto-carry', $2
		  FROM report_downstream_daily d
		 WHERE d.date < $1
		 ORDER BY d."group", d.date DESC
		ON CONFLICT ("group", date) DO NOTHING`,
		today, now)
	return err
}

func loadStep3(startDate, endDate string) ([]step3Row, error) {
	q := `
		SELECT date, channel_id, channel_name, channel_tag, total_cost_usd, unit_price_cny
		FROM report_pipi_daily
		WHERE date BETWEEN $1 AND $2
	`
	rows, err := db.Query(q, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []step3Row{}
	for rows.Next() {
		var r step3Row
		if err := rows.Scan(&r.date, &r.channelID, &r.channelName, &r.channelTag, &r.costUSD, &r.unitPriceCNY); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
