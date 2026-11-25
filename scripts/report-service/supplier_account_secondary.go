package main

// 账号上号「账号 2」: a second login on the same 号池 portal
// (SUPPLIER_ACCOUNT_BASE_URL), configured from the admin page and stored in
// report_config so it applies without a restart. Read-only and admin-only: its
// roster is read live from the portal's /supplier/accounts and its usage from
// /openapi/metrics/query + /openapi/bill/query with its own OpenAPI token.
// Nothing is written to rs_supplier_account, so account 1's list, sync and
// quota alerts never see these accounts.

import (
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	cfgSupplier2Label    = "supplier2_label"
	cfgSupplier2Username = "supplier2_portal_username"
	cfgSupplier2Password = "supplier2_portal_password"
	cfgSupplier2Token    = "supplier2_openapi_token"

	supplier2DefaultLabel = "账号 2"
	// supplier2MetricsWindow is the portal's longest metrics query span.
	supplier2MetricsWindow = 7 * 24 * time.Hour
	supplier2TimeLayout    = "2006-01-02 15:04:05"
)

// supplier2Web is the 账号 2 WEB login; credentials are read on every login so
// settings edits apply at once (the settings handler drops the cached token).
var supplier2Web = &portalWebSession{
	name: "supplier2",
	creds: func() (string, string) {
		return strings.TrimSpace(supplierConfigGet(cfgSupplier2Username)), supplierConfigGet(cfgSupplier2Password)
	},
}

func supplier2Token() string {
	return strings.TrimSpace(supplierConfigGet(cfgSupplier2Token))
}

// requireSupplier2Admin rejects non-admin callers. The routes are also gated
// with requireRole; the handlers check again so they are never exposed to the
// 账号上号 group's supplier role by a routing slip.
func requireSupplier2Admin(c *gin.Context) bool {
	if callerIsSupplierAdmin(c) {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	return false
}

func handleSupplier2SettingsGet(c *gin.Context) {
	if !requireSupplier2Admin(c) {
		return
	}
	username, password := supplier2Web.creds()
	token := supplier2Token()
	tokenLast4 := ""
	if len(token) > 8 {
		tokenLast4 = token[len(token)-4:]
	}
	c.JSON(http.StatusOK, gin.H{
		"label":               cmp.Or(strings.TrimSpace(supplierConfigGet(cfgSupplier2Label)), supplier2DefaultLabel),
		"username":            username,
		"password_set":        password != "",
		"openapi_token_set":   token != "",
		"openapi_token_last4": tokenLast4,
		"portal_configured":   supplierAccountBaseURL != "",
		"web_configured":      supplier2Web.configured(),
		"openapi_configured":  supplierAccountBaseURL != "" && token != "",
		"fx_rate":             supplierFxRate(),
	})
}

// handleSupplier2SettingsSet saves the 账号 2 login. An omitted or blank
// password / token keeps the stored one; clear: true wipes all three
// credentials.
func handleSupplier2SettingsSet(c *gin.Context) {
	if !requireSupplier2Admin(c) {
		return
	}
	var body struct {
		Label        *string `json:"label"`
		Username     *string `json:"username"`
		Password     *string `json:"password"`
		OpenAPIToken *string `json:"openapi_token"`
		Clear        bool    `json:"clear"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	updates := map[string]string{}
	if body.Label != nil {
		updates[cfgSupplier2Label] = strings.TrimSpace(*body.Label)
	}
	if body.Username != nil {
		updates[cfgSupplier2Username] = strings.TrimSpace(*body.Username)
	}
	if body.Password != nil && *body.Password != "" {
		updates[cfgSupplier2Password] = *body.Password
	}
	if body.OpenAPIToken != nil && strings.TrimSpace(*body.OpenAPIToken) != "" {
		updates[cfgSupplier2Token] = strings.TrimSpace(*body.OpenAPIToken)
	}
	if body.Clear {
		updates[cfgSupplier2Username] = ""
		updates[cfgSupplier2Password] = ""
		updates[cfgSupplier2Token] = ""
	}
	for key, value := range updates {
		if err := supplierConfigSet(key, value); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save settings"})
			return
		}
	}
	// A new username/password must log in afresh.
	supplier2Web.invalidate()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// supplier2Roster is the 账号 2 roster with every secret-bearing field (the
// portal returns full api keys) replaced by key_last8.
func supplier2Roster() ([]map[string]any, error) {
	raws, err := fetchPortalAccountsRaw(supplier2Web, "")
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(raws))
	for _, raw := range raws {
		var row map[string]any
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, errors.New("unexpected accounts response")
		}
		if key, ok := row["api_key"].(string); ok {
			row["key_last8"] = key[max(0, len(key)-8):]
		}
		for field := range row {
			lower := strings.ToLower(field)
			if field != "key_last8" && (strings.Contains(lower, "key") || strings.Contains(lower, "secret") ||
				strings.Contains(lower, "password") || strings.Contains(lower, "token") || strings.HasPrefix(lower, "enc_")) {
				delete(row, field)
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// supplier2NotConfigured answers when the 账号 2 WEB login is missing and
// reports whether it did.
func supplier2NotConfigured(c *gin.Context) bool {
	if supplier2Web.configured() {
		return false
	}
	c.JSON(http.StatusOK, gin.H{"configured": false, "error": "账号 2 未配置：请先填写用户名和密码"})
	return true
}

func handleSupplier2Accounts(c *gin.Context) {
	if !requireSupplier2Admin(c) || supplier2NotConfigured(c) {
		return
	}
	rows, err := supplier2Roster()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"configured": true, "accounts": rows})
}

// handleSupplier2Metrics returns 账号 2 usage per account plus totals over
// [begin_time, end_time] (portal time, "2006-01-02 15:04:05"; default the last
// 7 days; end is capped at now). Longer ranges are fetched in ≤7-day windows
// and summed; cost (RMB) comes from the bill once over the whole range, with
// cost_usd at supplier_fx_rate.
func handleSupplier2Metrics(c *gin.Context) {
	if !requireSupplier2Admin(c) || supplier2NotConfigured(c) {
		return
	}
	token := supplier2Token()
	if token == "" {
		c.JSON(http.StatusOK, gin.H{"configured": false, "error": "账号 2 未配置 OpenAPI token，无法查询用量"})
		return
	}
	var body struct {
		BeginTime string `json:"begin_time"`
		EndTime   string `json:"end_time"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	now := time.Now().In(supplierPortalLoc)
	end, err := parseSupplier2Time(body.EndTime, now)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "end_time 格式应为 YYYY-MM-DD HH:MM:SS"})
		return
	}
	if end.After(now) {
		end = now
	}
	begin, err := parseSupplier2Time(body.BeginTime, end.Add(-supplier2MetricsWindow))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "begin_time 格式应为 YYYY-MM-DD HH:MM:SS"})
		return
	}
	if !begin.Before(end) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "begin_time 必须早于 end_time"})
		return
	}

	roster, err := supplier2Roster()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	accounts := make([]supplierMetricAccount, 0, len(roster))
	ids := make([]int64, 0, len(roster))
	aliasToID := map[string]int64{}
	for _, row := range roster {
		idNum, _ := row["id"].(float64)
		id := int64(idNum)
		if id == 0 {
			continue
		}
		provider, _ := row["provider"].(string)
		accounts = append(accounts, supplierMetricAccount{RemoteAccountID: id, Provider: provider})
		ids = append(ids, id)
		if alias, _ := row["account_alias"].(string); alias != "" {
			aliasToID[alias] = id
		}
	}

	fx := supplierFxRate()
	rows := []supplierMetric{}
	if len(ids) > 0 {
		rows, err = supplier2QueryMetrics(token, ids, aliasToID, begin, end)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
	}
	summary := summarizeSupplierMetrics(accounts, rows, false)
	type metricOut struct {
		supplierMetric
		CostUSD *float64 `json:"cost_usd"`
	}
	out := make([]metricOut, 0, len(rows))
	for _, m := range rows {
		out = append(out, metricOut{supplierMetric: m, CostUSD: rmbToUSD(m.Cost, fx)})
	}
	c.JSON(http.StatusOK, gin.H{
		"configured":     true,
		"begin_time":     begin.Format(supplier2TimeLayout),
		"end_time":       end.Format(supplier2TimeLayout),
		"accounts":       out,
		"summary":        summary,
		"total_cost_usd": rmbToUSD(summary.Total.Cost, fx),
		"fx_rate":        fx,
	})
}

// supplier2QueryMetrics sums realtime metrics over ≤7-day windows of
// [begin, end], keyed by portal account id, then fills cost from the bill.
func supplier2QueryMetrics(token string, ids []int64, aliasToID map[string]int64, begin, end time.Time) ([]supplierMetric, error) {
	byID := map[int64]*supplierMetric{}
	order := []int64{}
	for from := begin; from.Before(end); from = from.Add(supplier2MetricsWindow) {
		to := from.Add(supplier2MetricsWindow)
		if to.After(end) {
			to = end
		}
		window, err := supplierFetchRealtimeMetrics(token, ids, from.Format(supplier2TimeLayout), to.Format(supplier2TimeLayout))
		if err != nil {
			return nil, err
		}
		for _, m := range window {
			if id, ok := aliasToID[m.AccountAlias]; ok {
				m.AID = id
			}
			acc, ok := byID[m.AID]
			if !ok {
				row := m
				byID[m.AID] = &row
				order = append(order, m.AID)
				continue
			}
			acc.merge(m)
		}
	}
	rows := make([]supplierMetric, 0, len(order))
	for _, id := range order {
		rows = append(rows, *byID[id])
	}
	supplierFillBillCost(token, ids, rows, begin.Unix(), end.Unix())
	return rows, nil
}

// merge adds a later window's metrics for the same account: counts add up,
// success_rate is weighted by requests, status is the later one.
func (m *supplierMetric) merge(next supplierMetric) {
	prevReq, nextReq := int64(0), int64(0)
	if m.Requests != nil {
		prevReq = *m.Requests
	}
	if next.Requests != nil {
		nextReq = *next.Requests
	}
	if m.SuccessRate == nil || prevReq == 0 {
		m.SuccessRate = next.SuccessRate
	} else if next.SuccessRate != nil && nextReq > 0 {
		rate := (*m.SuccessRate*float64(prevReq) + *next.SuccessRate*float64(nextReq)) / float64(prevReq+nextReq)
		m.SuccessRate = &rate
	}
	m.Requests = addInt64Ptr(m.Requests, next.Requests)
	m.PromptTokens = addInt64Ptr(m.PromptTokens, next.PromptTokens)
	m.CompletionTokens = addInt64Ptr(m.CompletionTokens, next.CompletionTokens)
	if next.Status != "" {
		m.Status = next.Status
	}
}

func addInt64Ptr(a, b *int64) *int64 {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	sum := *a + *b
	return &sum
}

func rmbToUSD(rmb *float64, fx float64) *float64 {
	if rmb == nil || fx <= 0 {
		return nil
	}
	usd := *rmb / fx
	return &usd
}

// parseSupplier2Time parses a portal-time string, returning fallback when s is
// blank.
func parseSupplier2Time(s string, fallback time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback, nil
	}
	for _, layout := range []string{supplier2TimeLayout, "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, supplierPortalLoc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("invalid time")
}
