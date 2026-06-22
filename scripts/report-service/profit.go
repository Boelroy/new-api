package main

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// fxRate is the CNY/USD rate used to convert (revenue_cny - cost_cny) -> USD profit.
// Hardcoded for v1 per design doc; can be lifted to env/config later.
const fxRate = 7.0

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

// ---- Downstream group pricing ----

type DownstreamPricing struct {
	Group        string  `json:"group"`
	UnitPriceCNY float64 `json:"unit_price_cny"`
	Note         string  `json:"note"`
	UpdatedAt    int64   `json:"updated_at"`
}

func handleListDownstreamPricing(c *gin.Context) {
	rows, err := db.Query(`SELECT "group", unit_price_cny, COALESCE(note,''), updated_at FROM report_downstream_pricing ORDER BY "group"`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := make([]DownstreamPricing, 0)
	for rows.Next() {
		var d DownstreamPricing
		if err := rows.Scan(&d.Group, &d.UnitPriceCNY, &d.Note, &d.UpdatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out = append(out, d)
	}
	c.JSON(http.StatusOK, out)
}

func handleSaveDownstreamPricing(c *gin.Context) {
	var payload []struct {
		Group        string  `json:"group"`
		UnitPriceCNY float64 `json:"unit_price_cny"`
		Note         string  `json:"note"`
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
			INSERT INTO report_downstream_pricing ("group", unit_price_cny, note, updated_at)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT ("group") DO UPDATE SET unit_price_cny=$2, note=$3, updated_at=$4`,
			g, p.UnitPriceCNY, p.Note, now)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		saved++
	}
	c.JSON(http.StatusOK, gin.H{"saved": saved})
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
	Date        string  `json:"date"`
	UsedUSD     float64 `json:"used_usd"`
	RevenueCNY  float64 `json:"revenue_cny"`
	CostCNY     float64 `json:"cost_cny"`
	ProfitUSD   float64 `json:"profit_usd"`
	ProfitRate  float64 `json:"profit_rate"` // (revenue - cost) / revenue
}

type ProfitByKey struct {
	ChannelID    int     `json:"channel_id"`
	ChannelName  string  `json:"channel_name"`
	Tag          string  `json:"tag"`
	Source       string  `json:"source"` // 'system1' or 'pipi'
	UsedUSD      float64 `json:"used_usd"`
	UnitPriceCNY float64 `json:"unit_price_cny"`
	CostCNY      float64 `json:"cost_cny"`
}

type ProfitByGroup struct {
	Group        string  `json:"group"`
	UsedUSD      float64 `json:"used_usd"`
	UnitPriceCNY float64 `json:"unit_price_cny"`
	RevenueCNY   float64 `json:"revenue_cny"`
}

type MissingPricing struct {
	ChannelIDs []int    `json:"channel_ids"`
	Groups     []string `json:"groups"`
}

type ProfitSummary struct {
	Start          string           `json:"start"`
	End            string           `json:"end"`
	UsedUSD        float64          `json:"used_usd"`
	RevenueCNY     float64          `json:"revenue_cny"`
	CostCNY        float64          `json:"cost_cny"`
	ProfitUSD      float64          `json:"profit_usd"`
	ProfitRate     float64          `json:"profit_rate"`
	Daily          []ProfitDaily    `json:"daily"`
	ByKey          []ProfitByKey    `json:"by_key"`
	ByGroup        []ProfitByGroup  `json:"by_group"`
	MissingPricing MissingPricing   `json:"missing_pricing"`
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

	// Load downstream pricing into memory for O(1) lookup.
	downPrice := map[string]float64{}
	dpRows, err := db.Query(`SELECT "group", unit_price_cny FROM report_downstream_pricing`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load downstream pricing: " + err.Error()})
		return
	}
	for dpRows.Next() {
		var g string
		var p float64
		if err := dpRows.Scan(&g, &p); err == nil {
			downPrice[g] = p
		}
	}
	dpRows.Close()

	// --- Step 1: non-pipi rows from System 1 ---
	step1, err := loadStep1(startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "step1: " + err.Error()})
		return
	}

	// --- Step 2: pipi revenue side (System 1 logs for tag=pipi) ---
	step2, err := loadStep2(startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "step2: " + err.Error()})
		return
	}

	// --- Step 3: pipi cost side (synced from System 2) ---
	step3, err := loadStep3(startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "step3: " + err.Error()})
		return
	}

	// Track missing pricing for UI warning.
	missingChIDs := map[int]bool{}
	missingGroups := map[string]bool{}

	// Per-day accumulators.
	daily := map[string]*ProfitDaily{}
	byKey := map[int]*ProfitByKey{}    // channel_id -> agg (system1 + pipi share same channel_id space? no — use composite key)
	byKeyPipi := map[int]*ProfitByKey{} // separate map for pipi to avoid collision
	byGroup := map[string]*ProfitByGroup{}

	getDay := func(d string) *ProfitDaily {
		if v, ok := daily[d]; ok {
			return v
		}
		v := &ProfitDaily{Date: d}
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
	getGroup := func(g string, price float64) *ProfitByGroup {
		if v, ok := byGroup[g]; ok {
			return v
		}
		v := &ProfitByGroup{Group: g, UnitPriceCNY: price}
		byGroup[g] = v
		return v
	}

	// Step 1 — non-pipi: revenue + cost both from System 1
	for _, r := range step1 {
		dp, dok := downPrice[r.tokenGroup]
		if !dok {
			missingGroups[r.tokenGroup] = true
		}
		var upP float64
		if r.upPrice.Valid {
			upP = r.upPrice.Float64
		} else {
			missingChIDs[r.channelID] = true
		}

		costCNY := r.usedUSD * upP
		revCNY := r.usedUSD * dp

		d := getDay(r.date)
		d.UsedUSD += r.usedUSD
		d.CostCNY += costCNY
		d.RevenueCNY += revCNY

		k := getKey(byKey, r.channelID, r.channelName, r.channelTag, "system1", upP)
		k.UsedUSD += r.usedUSD
		k.CostCNY += costCNY

		g := getGroup(r.tokenGroup, dp)
		g.UsedUSD += r.usedUSD
		g.RevenueCNY += revCNY
	}

	// Step 2 — pipi revenue side
	for _, r := range step2 {
		dp, dok := downPrice[r.tokenGroup]
		if !dok {
			missingGroups[r.tokenGroup] = true
		}
		revCNY := r.revenueUSD * dp

		d := getDay(r.date)
		d.UsedUSD += r.revenueUSD
		d.RevenueCNY += revCNY

		g := getGroup(r.tokenGroup, dp)
		g.UsedUSD += r.revenueUSD
		g.RevenueCNY += revCNY
	}

	// Step 3 — pipi cost side
	for _, r := range step3 {
		var upP float64
		if r.unitPriceCNY.Valid {
			upP = r.unitPriceCNY.Float64
		} else {
			missingChIDs[r.channelID] = true
		}
		costCNY := r.costUSD * upP

		d := getDay(r.date)
		d.CostCNY += costCNY

		k := getKey(byKeyPipi, r.channelID, r.channelName, r.channelTag, "pipi", upP)
		k.UsedUSD += r.costUSD
		k.CostCNY += costCNY
	}

	// Finalize per-day numbers.
	summary := ProfitSummary{Start: startDate, End: endDate}
	for _, d := range daily {
		d.ProfitUSD = (d.RevenueCNY - d.CostCNY) / fxRate
		if d.RevenueCNY > 0 {
			d.ProfitRate = (d.RevenueCNY - d.CostCNY) / d.RevenueCNY
		}
		// Round for presentation
		d.UsedUSD = roundTo(d.UsedUSD, 4)
		d.CostCNY = roundTo(d.CostCNY, 2)
		d.RevenueCNY = roundTo(d.RevenueCNY, 2)
		d.ProfitUSD = roundTo(d.ProfitUSD, 4)
		d.ProfitRate = roundTo(d.ProfitRate, 4)
		summary.UsedUSD += d.UsedUSD
		summary.CostCNY += d.CostCNY
		summary.RevenueCNY += d.RevenueCNY
	}

	// Sorted daily list
	dailyList := make([]ProfitDaily, 0, len(daily))
	for _, d := range daily {
		dailyList = append(dailyList, *d)
	}
	// Sort by date asc
	for i := 1; i < len(dailyList); i++ {
		for j := i; j > 0 && dailyList[j-1].Date > dailyList[j].Date; j-- {
			dailyList[j-1], dailyList[j] = dailyList[j], dailyList[j-1]
		}
	}
	summary.Daily = dailyList

	// Flatten byKey (system1 first, then pipi)
	for _, v := range byKey {
		v.UsedUSD = roundTo(v.UsedUSD, 4)
		v.CostCNY = roundTo(v.CostCNY, 2)
		summary.ByKey = append(summary.ByKey, *v)
	}
	for _, v := range byKeyPipi {
		v.UsedUSD = roundTo(v.UsedUSD, 4)
		v.CostCNY = roundTo(v.CostCNY, 2)
		summary.ByKey = append(summary.ByKey, *v)
	}

	for _, v := range byGroup {
		v.UsedUSD = roundTo(v.UsedUSD, 4)
		v.RevenueCNY = roundTo(v.RevenueCNY, 2)
		summary.ByGroup = append(summary.ByGroup, *v)
	}

	// Roll up summary totals
	summary.UsedUSD = roundTo(summary.UsedUSD, 4)
	summary.CostCNY = roundTo(summary.CostCNY, 2)
	summary.RevenueCNY = roundTo(summary.RevenueCNY, 2)
	summary.ProfitUSD = roundTo((summary.RevenueCNY-summary.CostCNY)/fxRate, 4)
	if summary.RevenueCNY > 0 {
		summary.ProfitRate = roundTo((summary.RevenueCNY-summary.CostCNY)/summary.RevenueCNY, 4)
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
		LEFT JOIN channels c ON c.id = r.channel_id
		LEFT JOIN report_key_quotas q ON q.channel_id = r.channel_id
		WHERE LEFT(r.hour,10) BETWEEN $1 AND $2
		  AND COALESCE(c.tag,'') <> 'pipi'
		GROUP BY date, r.channel_id, r.channel_name, channel_tag, token_group, q.unit_price_cny
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
		LEFT JOIN channels c ON c.id = r.channel_id
		WHERE LEFT(r.hour,10) BETWEEN $1 AND $2
		  AND COALESCE(c.tag,'') = 'pipi'
		GROUP BY date, token_group
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
