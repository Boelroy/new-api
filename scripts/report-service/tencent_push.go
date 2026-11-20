package main

// 腾讯一站 · 对账推送 + 推送配置. The month's statement snapshot goes to its own
// Lark webhook as a card of G / L totals per supplier × platform with a link
// to the xlsx export, pushed by hand or automatically for last month on a
// fixed day and hour (Beijing time). Every attempt lands in
// rs_tencent_push_log. /push-settings reads and writes the existing push
// settings (quota alerts, CC Max report, proxy report, ops daily spend) in the
// same report_config keys their own pages use, so both stay in step.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	cfgTencentPushWebhook = "tencent_statement_webhook"
	cfgTencentPushEnabled = "tencent_statement_push_enabled"
	cfgTencentPushDay     = "tencent_statement_push_day"
	cfgTencentPushHour    = "tencent_statement_push_hour"
	cfgTencentPublicURL   = "tencent_public_base_url"
	// cfgTencentPushMonth is the last month (YYYY-MM) the schedule claimed;
	// claiming before sending makes it go out once across nodes and restarts.
	cfgTencentPushMonth = "tencent_statement_push_month"

	tencentPushDefaultDay  = 2
	tencentPushDefaultHour = 10

	tencentPushKindStatement = "statement"
)

// tencentPushSchedule is the automatic push of last month's statement.
type tencentPushSchedule struct {
	Webhook string `json:"webhook"`
	Enabled bool   `json:"enabled"`
	Day     int    `json:"day"`
	Hour    int    `json:"hour"`
	BaseURL string `json:"base_url"`
}

func loadTencentPushSchedule() tencentPushSchedule {
	s := tencentPushSchedule{
		Webhook: strings.TrimSpace(supplierConfigGet(cfgTencentPushWebhook)),
		Enabled: supplierConfigGet(cfgTencentPushEnabled) != "0",
		Day:     tencentPushDefaultDay,
		Hour:    tencentPushDefaultHour,
		BaseURL: strings.TrimRight(strings.TrimSpace(supplierConfigGet(cfgTencentPublicURL)), "/"),
	}
	if d, err := strconv.Atoi(supplierConfigGet(cfgTencentPushDay)); err == nil && d >= 1 && d <= 28 {
		s.Day = d
	}
	if h, err := strconv.Atoi(supplierConfigGet(cfgTencentPushHour)); err == nil && h >= 0 && h <= 23 {
		s.Hour = h
	}
	return s
}

// tencentPushRow is one card table row: a vendor's G and L on the
// statement, as on 汇总表.
type tencentPushRow struct {
	Vendor     string  `json:"vendor"`
	Pricing    string  `json:"pricing"`
	Lines      int     `json:"lines"`
	Official   float64 `json:"official"`
	InvoiceRMB float64 `json:"invoice_rmb"`
}

// tencentPushRows totals snapshot lines per vendor.
func tencentPushRows(lines []tencentLine, cfg tencentMonthConfig) []tencentPushRow {
	summary := tencentSummarize(lines, cfg)
	out := make([]tencentPushRow, len(summary))
	for i, s := range summary {
		out[i] = tencentPushRow{Vendor: s.Vendor, Pricing: s.Pricing, Lines: s.Lines, Official: tencentRound(s.Official, 2), InvoiceRMB: s.InvoiceRMB}
	}
	return out
}

// tencentStatementCard renders a month's snapshot as a Lark card: each
// vendor's G (in its currency) and L, and the invoice total.
func tencentStatementCard(h tencentStatementHeader, rows []tencentPushRow, downloadURL string) map[string]any {
	status := map[string]string{tencentStatementDraft: "草稿", tencentStatementConfirmed: "已确认"}[h.Status]
	var totalRMB float64
	lines := 0
	tableRows := make([]any, len(rows))
	for i, r := range rows {
		totalRMB += r.InvoiceRMB
		lines += r.Lines
		currency := "RMB"
		if r.Pricing == tencentPricingUSD {
			currency = "USD"
		}
		tableRows[i] = map[string]any{"vendor": r.Vendor, "lines": r.Lines,
			"official": fmt.Sprintf("%.2f %s", r.Official, currency), "rmb": r.InvoiceRMB}
	}
	cfg := h.Meta.Config
	elements := []any{
		map[string]any{"tag": "markdown", "content": fmt.Sprintf("**%s %s公司对账单**（%s）\n开票金额合计 **¥%.2f** · %d 行\n中行汇率 %g（%s）· CCMax 除数 %g",
			h.Month, cfg.Company, status, tencentRound(totalRMB, 2), lines, cfg.BOCRate, cfg.BOCRateDate, cfg.CCMaxDivisor)},
	}
	if len(tableRows) > 0 {
		elements = append(elements, map[string]any{
			"tag": "table", "page_size": min(len(tableRows), 10), "row_height": "low",
			"header_style": map[string]any{"bold": true, "background_style": "grey"},
			"columns": []any{
				map[string]any{"name": "vendor", "display_name": "模型厂商", "data_type": "text"},
				map[string]any{"name": "lines", "display_name": "行数", "data_type": "number", "horizontal_align": "right"},
				map[string]any{"name": "official", "display_name": "付给官方 G", "data_type": "text", "horizontal_align": "right"},
				map[string]any{"name": "rmb", "display_name": "开票 L", "data_type": "number", "horizontal_align": "right",
					"format": map[string]any{"symbol": "¥", "precision": 2}},
			},
			"rows": tableRows,
		})
	}
	if downloadURL != "" {
		elements = append(elements, map[string]any{"tag": "action", "actions": []any{map[string]any{
			"tag": "button", "type": "primary", "url": downloadURL,
			"text": map[string]any{"tag": "plain_text", "content": "下载对账单 xlsx"},
		}}})
	} else {
		elements = append(elements, map[string]any{"tag": "note", "elements": []any{map[string]any{"tag": "plain_text",
			"content": "未配置站点地址（tencent_public_base_url），请到腾讯一站下载对账单"}}})
	}
	template := "orange"
	if h.Status == tencentStatementConfirmed {
		template = "green"
	}
	return map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"config":   map[string]any{"wide_screen_mode": true},
			"header":   map[string]any{"template": template, "title": map[string]any{"tag": "plain_text", "content": "📑 腾讯对账单 · " + h.Month}},
			"elements": elements,
		},
	}
}

// tencentSendStatement sends a month's statement to the statement webhook,
// generating a draft first when the month has no snapshot (the download link
// reads the snapshot).
func tencentSendStatement(month time.Time, by int64) (tencentStatementHeader, []tencentPushRow, error) {
	s := loadTencentPushSchedule()
	if s.Webhook == "" {
		return tencentStatementHeader{}, nil, errors.New("未配置对账推送 Webhook")
	}
	key := month.Format("2006-01")
	h, lines, err := tencentLoadSnapshot(key, "")
	if errors.Is(err, sql.ErrNoRows) {
		if err := tencentWriteSnapshot(month, false, by); err != nil {
			return h, nil, fmt.Errorf("生成草稿: %w", err)
		}
		h, lines, err = tencentLoadSnapshot(key, "")
	}
	if err != nil {
		return h, nil, err
	}
	rows := tencentPushRows(lines, h.Meta.Config)
	download := ""
	if s.BaseURL != "" {
		download = s.BaseURL + "/api/tencent/statements/" + key + "/export"
	}
	return h, rows, postLarkPayload(s.Webhook, tencentStatementCard(h, rows, download))
}

// tencentPushStatement sends a month's statement and records the attempt in
// rs_tencent_push_log.
func tencentPushStatement(month time.Time, trigger string, by int64) (tencentStatementHeader, []tencentPushRow, error) {
	h, rows, err := tencentSendStatement(month, by)
	detail, _ := json.Marshal(gin.H{"status": h.Status, "rows": rows})
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	key := month.Format("2006-01")
	if _, logErr := db.Exec(`
		INSERT INTO rs_tencent_push_log (kind, trigger, month, ok, error, detail, created_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		tencentPushKindStatement, trigger, key, err == nil, errText, string(detail), time.Now().Unix(), by); logErr != nil {
		log.Printf("[tencent-push] record %s %s: %v", trigger, key, logErr)
	}
	return h, rows, err
}

// tencentStatementScheduleTick pushes last month's statement once the
// configured day and hour (UTC+8) have come. Returns whether this call
// claimed the month; a failed send is not retried automatically.
func tencentStatementScheduleTick(now time.Time) (bool, error) {
	s := loadTencentPushSchedule()
	if !s.Enabled || s.Webhook == "" {
		return false, nil
	}
	now = now.In(billingExportZone)
	due := time.Date(now.Year(), now.Month(), s.Day, s.Hour, 0, 0, 0, billingExportZone)
	if now.Before(due) {
		return false, nil
	}
	month := time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, billingExportZone)
	key := month.Format("2006-01")
	res, err := db.Exec(`
		INSERT INTO report_config (key, value, updated_at) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = $3 WHERE report_config.value < $2`,
		cfgTencentPushMonth, key, now.Unix())
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	_, _, err = tencentPushStatement(month, "schedule", 0)
	return true, err
}

// startTencentStatementPushLoop checks the schedule at the top of every hour
// on the leader.
func startTencentStatementPushLoop() {
	go func() {
		for {
			time.Sleep(time.Until(time.Now().Truncate(time.Hour).Add(time.Hour)))
			if !IsLeader() {
				continue
			}
			if claimed, err := tencentStatementScheduleTick(time.Now()); err != nil {
				log.Printf("[tencent-push] schedule (claimed=%v): %v", claimed, err)
			}
		}
	}()
}

// ---- Handlers ----

// handleTencentStatementPush pushes a month (default last month) now.
func handleTencentStatementPush(c *gin.Context) {
	var body struct {
		Month string `json:"month"`
	}
	_ = c.ShouldBindJSON(&body)
	if body.Month == "" {
		now := time.Now().In(billingExportZone)
		body.Month = time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, billingExportZone).Format("2006-01")
	}
	month, _, err := tencentMonthRange(body.Month)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h, rows, err := tencentPushStatement(month, "manual", tencentCallerID(c))
	if err != nil {
		c.JSON(http.StatusFailedDependency, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"month": h.Month, "status": h.Status, "rows": rows})
}

func handleTencentPushLogs(c *gin.Context) {
	limit, err := strconv.Atoi(c.Query("limit"))
	if err != nil {
		limit = 50
	}
	rows, err := db.Query(`
		SELECT id, kind, trigger, month, ok, error, detail, created_at, created_by
		  FROM rs_tencent_push_log WHERE ($1 = '' OR kind = $1)
		 ORDER BY id DESC LIMIT $2`, c.Query("kind"), clampInt(limit, 1, 500))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	type item struct {
		ID        int64  `json:"id"`
		Kind      string `json:"kind"`
		Trigger   string `json:"trigger"`
		Month     string `json:"month"`
		OK        bool   `json:"ok"`
		Error     string `json:"error"`
		Detail    any    `json:"detail"`
		CreatedAt int64  `json:"created_at"`
		CreatedBy int64  `json:"created_by"`
	}
	list := []item{}
	for rows.Next() {
		var it item
		var detail string
		if err := rows.Scan(&it.ID, &it.Kind, &it.Trigger, &it.Month, &it.OK, &it.Error, &detail, &it.CreatedAt, &it.CreatedBy); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		_ = json.Unmarshal([]byte(detail), &it.Detail)
		list = append(list, it)
	}
	c.JSON(http.StatusOK, gin.H{"list": list})
}

// handleTencentPushSettingsGet gathers every push setting the 腾讯一站 page
// shows, read from the keys the owning pages use.
func handleTencentPushSettingsGet(c *gin.Context) {
	ops := loadOpsNotifyConfig()
	ccmaxHours := supplierConfigGet(cfgCCMaxReportHours)
	if strings.TrimSpace(ccmaxHours) == "" {
		ccmaxHours = ccmaxReportDefaultHours
	}
	// The quota webhook is masked like on the 账号上号 settings page, which
	// also falls back to the SUPPLIER_QUOTA_WEBHOOK env.
	quotaWebhook := effectiveQuotaWebhook()
	quotaLast4 := ""
	if len(quotaWebhook) > 4 {
		quotaLast4 = quotaWebhook[len(quotaWebhook)-4:]
	}
	c.JSON(http.StatusOK, gin.H{
		"statement":            loadTencentPushSchedule(),
		"statement_last_month": supplierConfigGet(cfgTencentPushMonth),
		"supplier_quota":       gin.H{"webhook_set": quotaWebhook != "", "webhook_last4": quotaLast4},
		"ccmax_report":         gin.H{"webhook": supplierConfigGet(cfgCCMaxReportWebhook), "hours": ccmaxHours},
		"proxy_report":         gin.H{"webhook": supplierConfigGet(cfgProxyReportWebhook), "hours": supplierConfigGet(cfgProxyReportHours)},
		"ops_daily_spend": gin.H{"enabled": ops.Enabled, "webhook": ops.Webhook,
			"daily_spend": ops.DailySpend, "daily_spend_hour": ops.DailySpendHour},
	})
}

type tencentReportPushSetting struct {
	Webhook *string `json:"webhook"`
	Hours   *string `json:"hours"`
}

// handleTencentPushSettingsSet updates the given fields only, with the same
// validation and storage as each owning page. Webhooks must be https.
func handleTencentPushSettingsSet(c *gin.Context) {
	var body struct {
		Statement *struct {
			Webhook *string `json:"webhook"`
			Enabled *bool   `json:"enabled"`
			Day     *int    `json:"day"`
			Hour    *int    `json:"hour"`
			BaseURL *string `json:"base_url"`
		} `json:"statement"`
		SupplierQuotaWebhook *string                   `json:"supplier_quota_webhook"`
		CCMaxReport          *tencentReportPushSetting `json:"ccmax_report"`
		ProxyReport          *tencentReportPushSetting `json:"proxy_report"`
		OpsDailySpend        *struct {
			Enabled        *bool   `json:"enabled"`
			Webhook        *string `json:"webhook"`
			DailySpend     *bool   `json:"daily_spend"`
			DailySpendHour *int    `json:"daily_spend_hour"`
		} `json:"ops_daily_spend"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	// Validate everything before writing anything.
	webhooks := []*string{body.SupplierQuotaWebhook}
	hours := map[string]*string{}
	if st := body.Statement; st != nil {
		webhooks = append(webhooks, st.Webhook)
		if st.Day != nil && (*st.Day < 1 || *st.Day > 28) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "推送日须在 1–28 之间"})
			return
		}
		if st.Hour != nil && (*st.Hour < 0 || *st.Hour > 23) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "推送小时须在 0–23 之间"})
			return
		}
		if st.BaseURL != nil {
			u := strings.TrimSpace(*st.BaseURL)
			if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "站点地址须以 http(s):// 开头"})
				return
			}
		}
	}
	if r := body.CCMaxReport; r != nil {
		webhooks = append(webhooks, r.Webhook)
		hours[cfgCCMaxReportHours] = r.Hours
	}
	if r := body.ProxyReport; r != nil {
		webhooks = append(webhooks, r.Webhook)
		hours[cfgProxyReportHours] = r.Hours
	}
	if o := body.OpsDailySpend; o != nil {
		webhooks = append(webhooks, o.Webhook)
	}
	for _, w := range webhooks {
		if w != nil && strings.TrimSpace(*w) != "" && !strings.HasPrefix(strings.TrimSpace(*w), "https://") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Webhook 须以 https:// 开头"})
			return
		}
	}
	writes := map[string]string{}
	for key, raw := range hours {
		if raw == nil {
			continue
		}
		parsed, err := ccmaxParseReportHours(*raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		parts := make([]string, len(parsed))
		for i, h := range parsed {
			parts[i] = strconv.Itoa(h)
		}
		writes[key] = strings.Join(parts, ",")
	}

	if st := body.Statement; st != nil {
		if st.Webhook != nil {
			writes[cfgTencentPushWebhook] = strings.TrimSpace(*st.Webhook)
		}
		if st.Enabled != nil {
			writes[cfgTencentPushEnabled] = map[bool]string{true: "1", false: "0"}[*st.Enabled]
		}
		if st.Day != nil {
			writes[cfgTencentPushDay] = strconv.Itoa(*st.Day)
		}
		if st.Hour != nil {
			writes[cfgTencentPushHour] = strconv.Itoa(*st.Hour)
		}
		if st.BaseURL != nil {
			writes[cfgTencentPublicURL] = strings.TrimRight(strings.TrimSpace(*st.BaseURL), "/")
		}
	}
	if body.SupplierQuotaWebhook != nil {
		writes[cfgSupplierQuotaWebhook] = strings.TrimSpace(*body.SupplierQuotaWebhook)
	}
	if r := body.CCMaxReport; r != nil && r.Webhook != nil {
		writes[cfgCCMaxReportWebhook] = strings.TrimSpace(*r.Webhook)
	}
	if r := body.ProxyReport; r != nil && r.Webhook != nil {
		writes[cfgProxyReportWebhook] = strings.TrimSpace(*r.Webhook)
	}
	if o := body.OpsDailySpend; o != nil {
		cfg := loadOpsNotifyConfig()
		if o.Enabled != nil {
			cfg.Enabled = *o.Enabled
		}
		if o.Webhook != nil {
			cfg.Webhook = strings.TrimSpace(*o.Webhook)
		}
		if o.DailySpend != nil {
			cfg.DailySpend = *o.DailySpend
		}
		if o.DailySpendHour != nil {
			cfg.DailySpendHour = *o.DailySpendHour
		}
		raw, _ := json.Marshal(cfg.clamp())
		writes[cfgOpsNotify] = string(raw)
	}
	for key, value := range writes {
		if err := supplierConfigSet(key, value); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	handleTencentPushSettingsGet(c)
}
