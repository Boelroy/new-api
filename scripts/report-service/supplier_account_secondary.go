package main

// 账号上号「账号 2」: a second login on the same 号池 portal
// (SUPPLIER_ACCOUNT_BASE_URL), configured from the admin page and stored in
// report_config so it applies without a restart. Read-only: its roster is read
// live from the portal's /supplier/accounts and its usage from
// /openapi/metrics/query + /openapi/bill/query with its own OpenAPI token.
// Nothing is written to rs_supplier_account, so account 1's list, sync and
// quota alerts never see these accounts.
//
// Admins see and configure everything and assign each account to a
// report-service user (rs_supplier2_owner). Any signed-in user can read the
// accounts assigned to them through /secondary/mine, without cost unless they
// are an admin. supplier2_report.go pushes a Lark usage report.

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
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

// supplier2RowID is a roster row's portal account id (0 when missing).
func supplier2RowID(row map[string]any) int64 {
	id, _ := row["id"].(float64)
	return int64(id)
}

// supplier2Owner is the report-service user an account 2 account belongs to.
type supplier2Owner struct {
	UserID   int64
	Username string
}

// supplier2Owners returns the account → owner map, or only userID's accounts
// when userID is non-zero.
func supplier2Owners(userID int64) (map[int64]supplier2Owner, error) {
	query := `SELECT o.remote_account_id, o.user_id, COALESCE(u.username, '')
	            FROM rs_supplier2_owner o LEFT JOIN rs_auth_user u ON u.id = o.user_id`
	var args []any
	if userID != 0 {
		query += ` WHERE o.user_id=$1`
		args = append(args, userID)
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]supplier2Owner{}
	for rows.Next() {
		var id int64
		var o supplier2Owner
		if err := rows.Scan(&id, &o.UserID, &o.Username); err != nil {
			return nil, err
		}
		out[id] = o
	}
	return out, rows.Err()
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
	owners, err := supplier2Owners(0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, row := range rows {
		row["owner_user_id"], row["owner_username"] = nil, nil
		if o, ok := owners[supplier2RowID(row)]; ok {
			row["owner_user_id"], row["owner_username"] = o.UserID, o.Username
		}
	}
	c.JSON(http.StatusOK, gin.H{"configured": true, "accounts": rows})
}

// handleSupplier2OwnerSet assigns account :id to user_id, or clears its owner
// when user_id is null or 0. Admin-only.
func handleSupplier2OwnerSet(c *gin.Context) {
	if !requireSupplier2Admin(c) {
		return
	}
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid account id"})
		return
	}
	var body struct {
		UserID *int64 `json:"user_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if body.UserID == nil || *body.UserID == 0 {
		if _, err := db.Exec(`DELETE FROM rs_supplier2_owner WHERE remote_account_id=$1`, accountID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "owner_user_id": nil, "owner_username": nil})
		return
	}
	var username string
	err = db.QueryRow(`SELECT username FROM rs_auth_user WHERE id=$1`, *body.UserID).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户不存在"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if _, err := db.Exec(
		`INSERT INTO rs_supplier2_owner (remote_account_id, user_id, updated_at) VALUES ($1, $2, $3)
		 ON CONFLICT (remote_account_id) DO UPDATE SET user_id=EXCLUDED.user_id, updated_at=EXCLUDED.updated_at`,
		accountID, *body.UserID, time.Now().Unix(),
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "owner_user_id": *body.UserID, "owner_username": username})
}

// supplier2MyRoster returns the roster rows assigned to the caller; nil (and
// no portal call) when nothing is assigned or account 2 is not configured.
// Owner rows for accounts no longer on the portal roster are skipped.
func supplier2MyRoster(c *gin.Context) ([]map[string]any, error) {
	uidAny, _ := c.Get("user_id")
	uid, _ := uidAny.(int64)
	if uid == 0 {
		return nil, nil
	}
	owned, err := supplier2Owners(uid)
	if err != nil || len(owned) == 0 || !supplier2Web.configured() {
		return nil, err
	}
	roster, err := supplier2Roster()
	if err != nil {
		return nil, err
	}
	mine := make([]map[string]any, 0, len(owned))
	for _, row := range roster {
		if _, ok := owned[supplier2RowID(row)]; ok {
			mine = append(mine, row)
		}
	}
	return mine, nil
}

// handleSupplier2MyAccounts lists the account 2 accounts assigned to the
// caller (any signed-in user). The server decides the set; there is no way to
// ask for other account ids.
func handleSupplier2MyAccounts(c *gin.Context) {
	rows, err := supplier2MyRoster(c)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	c.JSON(http.StatusOK, gin.H{"accounts": rows, "can_view_cost": callerIsSupplierAdmin(c)})
}

// handleSupplier2MyMetrics is handleSupplier2Metrics over the caller's own
// accounts; cost is left out for non-admin callers.
func handleSupplier2MyMetrics(c *gin.Context) {
	begin, end, ok := supplier2BindRange(c)
	if !ok {
		return
	}
	rows, err := supplier2MyRoster(c)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	token := supplier2Token()
	if len(rows) > 0 && token == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "账号 2 用量暂不可用，请联系管理员"})
		return
	}
	resp, err := supplier2Usage(token, rows, begin, end)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if !callerIsSupplierAdmin(c) {
		resp = supplier2WithoutCost(resp)
	}
	c.JSON(http.StatusOK, resp)
}

// supplier2WithoutCost drops every cost figure (cost, cost_usd,
// total_cost_usd, fx_rate) from a usage response, at any depth.
func supplier2WithoutCost(resp gin.H) gin.H {
	blob, _ := json.Marshal(resp)
	var out map[string]any
	_ = json.Unmarshal(blob, &out)
	var strip func(v any)
	strip = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for _, k := range []string{"cost", "cost_usd", "total_cost_usd", "fx_rate"} {
				delete(t, k)
			}
			for _, child := range t {
				strip(child)
			}
		case []any:
			for _, child := range t {
				strip(child)
			}
		}
	}
	strip(out)
	return out
}

// handleSupplier2Metrics returns 账号 2 usage per account plus totals over
// [begin_time, end_time] for every account (see supplier2BindRange and
// supplier2Usage).
func handleSupplier2Metrics(c *gin.Context) {
	if !requireSupplier2Admin(c) || supplier2NotConfigured(c) {
		return
	}
	token := supplier2Token()
	if token == "" {
		c.JSON(http.StatusOK, gin.H{"configured": false, "error": "账号 2 未配置 OpenAPI token，无法查询用量"})
		return
	}
	begin, end, ok := supplier2BindRange(c)
	if !ok {
		return
	}
	roster, err := supplier2Roster()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	resp, err := supplier2Usage(token, roster, begin, end)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// supplier2BindRange reads {begin_time, end_time} (portal time, "2006-01-02
// 15:04:05"; default the last 7 days; end is capped at now), answering 400
// itself when they are invalid.
func supplier2BindRange(c *gin.Context) (time.Time, time.Time, bool) {
	var body struct {
		BeginTime string `json:"begin_time"`
		EndTime   string `json:"end_time"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return time.Time{}, time.Time{}, false
	}
	now := time.Now().In(supplierPortalLoc)
	end, err := parseSupplier2Time(body.EndTime, now)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "end_time 格式应为 YYYY-MM-DD HH:MM:SS"})
		return time.Time{}, time.Time{}, false
	}
	if end.After(now) {
		end = now
	}
	begin, err := parseSupplier2Time(body.BeginTime, end.Add(-supplier2MetricsWindow))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "begin_time 格式应为 YYYY-MM-DD HH:MM:SS"})
		return time.Time{}, time.Time{}, false
	}
	if !begin.Before(end) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "begin_time 必须早于 end_time"})
		return time.Time{}, time.Time{}, false
	}
	return begin, end, true
}

// supplier2Usage computes the usage response for the given roster rows over
// [begin, end]: longer ranges are fetched in ≤7-day windows and summed; cost
// (RMB) comes from the bill once over the whole range, with cost_usd at
// supplier_fx_rate.
func supplier2Usage(token string, roster []map[string]any, begin, end time.Time) (gin.H, error) {
	accounts := make([]supplierMetricAccount, 0, len(roster))
	ids := make([]int64, 0, len(roster))
	aliasToID := map[string]int64{}
	for _, row := range roster {
		id := supplier2RowID(row)
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
		var err error
		if rows, err = supplier2QueryMetrics(token, ids, aliasToID, begin, end); err != nil {
			return nil, err
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
	return gin.H{
		"configured":     true,
		"begin_time":     begin.Format(supplier2TimeLayout),
		"end_time":       end.Format(supplier2TimeLayout),
		"accounts":       out,
		"summary":        summary,
		"total_cost_usd": rmbToUSD(summary.Total.Cost, fx),
		"fx_rate":        fx,
	}, nil
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
