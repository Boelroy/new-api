package main

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// defaultFXRate is the cold fallback when neither report_fx_rate nor
// report_config['default_fx_rate'] has been configured.
const defaultFXRate = 6.79

// anthropicChannelTypesSQL filters the profit report to channels that serve
// Claude in newapi's constants:
//   14 = Anthropic direct
//   33 = AWS Bedrock (serving Claude)
const anthropicChannelTypesSQL = "(14, 33)"

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

type ProfitSummary struct {
	Start          string          `json:"start"`
	End            string          `json:"end"`
	UsedUSD        float64         `json:"used_usd"`
	RevenueUSD     float64         `json:"revenue_usd"`
	CostUSD        float64         `json:"cost_usd"`
	ProfitUSD      float64         `json:"profit_usd"`
	ProfitRate     float64         `json:"profit_rate"`
	Daily          []ProfitDaily   `json:"daily"`
	ByKey          []ProfitByKey   `json:"by_key"`
	ByTag          []ProfitByTag   `json:"by_tag"`
	ByGroup        []ProfitByGroup `json:"by_group"`
	MissingPricing MissingPricing  `json:"missing_pricing"`
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

	// --- Downstream discount lookup (group -> multiplier) ---
	discount := map[string]float64{}
	dpRows, err := db.Query(`SELECT "group", discount FROM report_downstream_pricing`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load downstream pricing: " + err.Error()})
		return
	}
	for dpRows.Next() {
		var g string
		var p float64
		if err := dpRows.Scan(&g, &p); err == nil {
			discount[g] = p
		}
	}
	dpRows.Close()

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
	missingGroups := map[string]bool{}

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

	// Step 1 — non-pipi: revenue + cost both anchored on System 1's used_usd
	for _, r := range step1 {
		dis, dok := discount[r.tokenGroup]
		if !dok {
			missingGroups[r.tokenGroup] = true
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

	// Step 2 — pipi revenue side (downstream group lives in System 1's logs)
	for _, r := range step2 {
		dis, dok := discount[r.tokenGroup]
		if !dok {
			missingGroups[r.tokenGroup] = true
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
	for _, d := range daily {
		d.ProfitUSD = d.RevenueUSD - d.CostUSD
		if d.RevenueUSD > 0 {
			d.ProfitRate = d.ProfitUSD / d.RevenueUSD
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
		if out.RevenueUSD > 0 {
			out.ProfitRate = roundTo(out.ProfitUSD/out.RevenueUSD, 4)
		}
		summary.ByTag = append(summary.ByTag, out)
	}
	for tag, b := range mainTagAgg {
		emit(tag, "main", b)
	}
	if pipiTagAgg.usedUSD > 0 || pipiTagAgg.costUSD > 0 || pipiTagAgg.revenueUSD > 0 {
		emit("pipi", "pipi", pipiTagAgg)
	}

	summary.UsedUSD = roundTo(summary.UsedUSD, 4)
	summary.CostUSD = roundTo(summary.CostUSD, 4)
	summary.RevenueUSD = roundTo(summary.RevenueUSD, 4)
	summary.ProfitUSD = roundTo(summary.RevenueUSD-summary.CostUSD, 4)
	if summary.RevenueUSD > 0 {
		summary.ProfitRate = roundTo((summary.RevenueUSD-summary.CostUSD)/summary.RevenueUSD, 4)
	}

	for id := range missingChIDs {
		summary.MissingPricing.ChannelIDs = append(summary.MissingPricing.ChannelIDs, id)
	}
	for g := range missingGroups {
		summary.MissingPricing.Groups = append(summary.MissingPricing.Groups, g)
	}

	c.JSON(http.StatusOK, summary)
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
		  AND c.type IN ` + anthropicChannelTypesSQL + `
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
		  AND c.type IN ` + anthropicChannelTypesSQL + `
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
