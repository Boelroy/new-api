package main

// CC Max Lark report: account totals, running accounts, average 5h / 7d
// window and month-to-date cost, pushed to an admin-configured Lark webhook
// at admin-configured hours (UTC+8).
//
// The portal only reports a cumulative cost per claude account
// (extra.cpa_usage.cost) and rejects claude ids on its date-ranged
// bill/metrics endpoints, so month-to-date cost comes from
// rs_ccmax_cost_snapshot: the hourly loop records each account's cumulative
// cost for the day, and the month figure is latest − the last snapshot before
// the 1st (0 for accounts first seen this month). Accounts that dropped off the
// roster keep counting through their last snapshot.

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	cfgCCMaxReportWebhook = "ccmax_report_webhook"
	cfgCCMaxReportHours   = "ccmax_report_hours"
	// ccmaxReportDefaultHours applies until an admin saves push hours.
	ccmaxReportDefaultHours = "10,22"
)

// ccmaxServingStatuses are the portal statuses counted as running — the same
// green ("serving") set the /ccmax page averages its windows over.
var ccmaxServingStatuses = []string{"online", "using", "bound"}

// ccmaxReport is one computed push.
type ccmaxReport struct {
	Total        int
	Serving      int
	FiveHourAvg  float64 // fraction, averaged over serving accounts
	WeekAvg      float64 // fraction, averaged over serving accounts
	MonthCostRMB float64
	FxRate       float64
	From, To     time.Time
	// Per uploader (rs_ccmax_account.uploaded_by), most month cost first;
	// accounts not uploaded through report-service are "(未归属)".
	ByUploader []ccmaxUploaderStat
}

type ccmaxUploaderStat struct {
	Name         string
	Total        int
	Serving      int
	MonthCostRMB float64
}

// ccmaxParseReportHours parses "10,22" into sorted, de-duplicated hours 0..23.
func ccmaxParseReportHours(raw string) ([]int, error) {
	hours := []int{}
	for part := range strings.SplitSeq(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		h, err := strconv.Atoi(part)
		if err != nil || h < 0 || h > 23 {
			return nil, fmt.Errorf("推送时间须为 0-23 的整数，逗号分隔：%q", part)
		}
		hours = append(hours, h)
	}
	slices.Sort(hours)
	return slices.Compact(hours), nil
}

func ccmaxReportHours() []int {
	raw := supplierConfigGet(cfgCCMaxReportHours)
	if strings.TrimSpace(raw) == "" {
		raw = ccmaxReportDefaultHours
	}
	hours, err := ccmaxParseReportHours(raw)
	if err != nil {
		return nil
	}
	return hours
}

// ccmaxMonthCostByAccount returns each account's month-to-date cost: its
// latest cumulative cost (current roster value, else its latest snapshot this
// month) minus the last snapshot before the month (0 when there is none). A
// counter that went backwards counts 0.
func ccmaxMonthCostByAccount(current, inMonthLatest, baseline map[int64]int64) map[int64]int64 {
	out := map[int64]int64{}
	for _, latest := range []map[int64]int64{current, inMonthLatest} {
		for id, cost := range latest {
			if _, seen := out[id]; seen {
				continue
			}
			out[id] = max(0, cost-baseline[id])
		}
	}
	return out
}

// ccmaxMonthStart is the first instant of now's month in UTC+8.
func ccmaxMonthStart(now time.Time) time.Time {
	now = now.In(supplierPortalLoc)
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, supplierPortalLoc)
}

// ccmaxSnapshotMonthCosts records today's cumulative cost snapshot for the
// current roster (account id → micro-yuan) and returns every account's
// month-to-date cost in micro-yuan, including accounts no longer on the roster.
func ccmaxSnapshotMonthCosts(current map[int64]int64, now time.Time) (map[int64]int64, error) {
	now = now.In(supplierPortalLoc)
	today := now.Format("2006-01-02")
	for id, cost := range current {
		if _, err := db.Exec(
			`INSERT INTO rs_ccmax_cost_snapshot (remote_account_id, ds, cost_micro, updated_at) VALUES ($1, $2, $3, $4)
			 ON CONFLICT (remote_account_id, ds) DO UPDATE SET cost_micro=EXCLUDED.cost_micro, updated_at=EXCLUDED.updated_at`,
			id, today, cost, now.Unix(),
		); err != nil {
			return nil, fmt.Errorf("record cost snapshot: %w", err)
		}
	}
	monthDS := ccmaxMonthStart(now).Format("2006-01-02")
	baseline, err := ccmaxLatestSnapshots(`ds < $1`, monthDS)
	if err != nil {
		return nil, err
	}
	inMonth, err := ccmaxLatestSnapshots(`ds >= $1`, monthDS)
	if err != nil {
		return nil, err
	}
	return ccmaxMonthCostByAccount(current, inMonth, baseline), nil
}

// ccmaxReportText renders the Lark message.
func ccmaxReportText(r ccmaxReport) string {
	const layout = "2006-01-02 15:04"
	fx := r.FxRate
	if fx <= 0 {
		fx = supplierDefaultFxRate
	}
	var b strings.Builder
	fmt.Fprintf(&b, "【CC Max 账号统计】%s（UTC+8）\n", r.To.Format(layout))
	fmt.Fprintf(&b, "账号总数：%d\n", r.Total)
	fmt.Fprintf(&b, "正在跑：%d（线上 / 使用中 / 绑定完成）\n", r.Serving)
	fmt.Fprintf(&b, "5 小时窗口平均：%.1f%%\n", r.FiveHourAvg*100)
	fmt.Fprintf(&b, "7 天窗口平均：%.1f%%（按正在跑的账号平均）\n", r.WeekAvg*100)
	fmt.Fprintf(&b, "本月消耗：$%.2f（¥%.2f，汇率 %g）\n", r.MonthCostRMB/fx, r.MonthCostRMB, fx)
	fmt.Fprintf(&b, "统计区间：%s ~ %s（UTC+8）", r.From.Format(layout), r.To.Format(layout))
	return b.String()
}

// ccmaxReportCard renders the report as a Lark card: the headline numbers in
// two columns, then a per-uploader table.
func ccmaxReportCard(r ccmaxReport) map[string]any {
	const layout = "01-02 15:04"
	fx := r.FxRate
	if fx <= 0 {
		fx = supplierDefaultFxRate
	}
	field := func(label, value string) map[string]any {
		return map[string]any{"is_short": true, "text": map[string]any{"tag": "lark_md", "content": "**" + label + "**\n" + value}}
	}
	elements := []any{
		map[string]any{"tag": "div", "fields": []any{
			field("本月消耗", fmt.Sprintf("%s（¥%.2f）", fmtUSD(r.MonthCostRMB/fx), r.MonthCostRMB)),
			field("账号", fmt.Sprintf("%d 个，正在跑 %d", r.Total, r.Serving)),
			field("5 小时窗口平均", fmt.Sprintf("%.1f%%", r.FiveHourAvg*100)),
			field("7 天窗口平均", fmt.Sprintf("%.1f%%", r.WeekAvg*100)),
		}},
	}
	if len(r.ByUploader) > 0 {
		rows := make([]any, len(r.ByUploader))
		for i, u := range r.ByUploader {
			rows[i] = map[string]any{"name": u.Name, "total": u.Total, "serving": u.Serving,
				"usd": math.Round(u.MonthCostRMB/fx*100) / 100, "rmb": math.Round(u.MonthCostRMB*100) / 100}
		}
		num := func(name, title, symbol string, precision int) map[string]any {
			col := map[string]any{"name": name, "display_name": title, "data_type": "number", "horizontal_align": "right"}
			if symbol != "" {
				col["format"] = map[string]any{"symbol": symbol, "precision": precision}
			}
			return col
		}
		elements = append(elements, map[string]any{"tag": "hr"}, map[string]any{
			"tag": "table", "page_size": min(len(rows), 10), "row_height": "low",
			"header_style": map[string]any{"bold": true, "background_style": "grey"},
			"columns": []any{
				map[string]any{"name": "name", "display_name": "上号人", "data_type": "text"},
				num("total", "账号", "", 0), num("serving", "正在跑", "", 0),
				num("usd", "本月 USD", "$", 2), num("rmb", "本月 RMB", "¥", 2),
			},
			"rows": rows,
		})
	}
	elements = append(elements, map[string]any{"tag": "note", "elements": []any{map[string]any{"tag": "plain_text",
		"content": fmt.Sprintf("统计区间 %s ~ %s（UTC+8）· 汇率 %g · 正在跑 = 线上 / 使用中 / 绑定完成", r.From.Format(layout), r.To.Format(layout), fx)}}})
	return map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"config":   map[string]any{"wide_screen_mode": true},
			"header":   map[string]any{"template": "orange", "title": map[string]any{"tag": "plain_text", "content": "🤖 CC Max 账号统计 · " + r.To.Format(layout)}},
			"elements": elements,
		},
	}
}

// ccmaxBuildReport loads the whole claude roster, records today's cost
// snapshot and computes the report as of now.
func ccmaxBuildReport(now time.Time) (ccmaxReport, error) {
	if !ccmaxWeb.configured() {
		return ccmaxReport{}, errors.New("CC Max portal login not configured")
	}
	raws, err := fetchPortalAccountsRaw(ccmaxWeb, "claude")
	if err != nil {
		return ccmaxReport{}, err
	}
	now = now.In(supplierPortalLoc)
	r := ccmaxReport{From: ccmaxMonthStart(now), To: now, FxRate: supplierFxRate()}

	owner := map[int64]string{}
	if rows, err := db.Query(`SELECT a.remote_account_id, COALESCE(u.username, '#' || a.uploaded_by::text)
		FROM rs_ccmax_account a LEFT JOIN rs_auth_user u ON u.id = a.uploaded_by`); err == nil {
		for rows.Next() {
			var id int64
			var name string
			if rows.Scan(&id, &name) == nil {
				owner[id] = name
			}
		}
		rows.Close()
	}
	byUploader := map[string]*ccmaxUploaderStat{}
	accountUploader := map[int64]*ccmaxUploaderStat{}

	current := map[int64]int64{}
	var fiveSum, weekSum float64
	for _, raw := range raws {
		row, err := parseCCMaxPortalRow(raw)
		if err != nil {
			continue
		}
		r.Total++
		current[row.ID] = int64(math.Round(row.CostRMB * 1e6))
		name := cmp.Or(owner[row.ID], "(未归属)")
		st := byUploader[name]
		if st == nil {
			st = &ccmaxUploaderStat{Name: name}
			byUploader[name] = st
		}
		accountUploader[row.ID] = st
		st.Total++
		if slices.Contains(ccmaxServingStatuses, row.Status) {
			st.Serving++
			r.Serving++
			fiveSum += row.FiveHourUsedPercent
			weekSum += row.WeekUsedPercent
		}
	}
	if r.Serving > 0 {
		r.FiveHourAvg = fiveSum / float64(r.Serving)
		r.WeekAvg = weekSum / float64(r.Serving)
	}

	monthCosts, err := ccmaxSnapshotMonthCosts(current, now)
	if err != nil {
		return ccmaxReport{}, err
	}
	var monthMicro int64
	for id, cost := range monthCosts {
		monthMicro += cost
		if st := accountUploader[id]; st != nil {
			st.MonthCostRMB += float64(cost) / 1e6
		}
	}
	r.MonthCostRMB = float64(monthMicro) / 1e6
	for _, st := range byUploader {
		r.ByUploader = append(r.ByUploader, *st)
	}
	slices.SortFunc(r.ByUploader, func(a, b ccmaxUploaderStat) int {
		if c := cmp.Compare(b.MonthCostRMB, a.MonthCostRMB); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return r, nil
}

// ccmaxLatestSnapshots returns each account's latest snapshot cost among the
// rows matching cond (a ds predicate on $1).
func ccmaxLatestSnapshots(cond, ds string) (map[int64]int64, error) {
	rows, err := db.Query(
		`SELECT DISTINCT ON (remote_account_id) remote_account_id, cost_micro
		   FROM rs_ccmax_cost_snapshot WHERE `+cond+`
		  ORDER BY remote_account_id, ds DESC`, ds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, cost int64
		if err := rows.Scan(&id, &cost); err != nil {
			return nil, err
		}
		out[id] = cost
	}
	return out, rows.Err()
}

// startCCMaxReportLoop wakes at the top of every hour (UTC+8). The leader
// pulls any daily bills not yet stored (from ccmaxBillHour on, so yesterday's
// lands at 08:00), records the cost snapshot every hour, so month baselines
// stay current even with no push configured, and pushes at the configured
// hours when a webhook is set.
func startCCMaxReportLoop() {
	go func() {
		for {
			now := time.Now().In(supplierPortalLoc)
			time.Sleep(time.Until(now.Truncate(time.Hour).Add(time.Hour)))
			if !IsLeader() || !ccmaxWeb.configured() {
				continue
			}
			now = time.Now().In(supplierPortalLoc)
			if now.Hour() >= ccmaxBillHour {
				if err := ccmaxCatchUpDailyBills(now); err != nil {
					log.Printf("[ccmax-bill] sync: %v", err)
				}
			}
			r, err := ccmaxBuildReport(now)
			if err != nil {
				log.Printf("[ccmax-report] build: %v", err)
				continue
			}
			webhook := strings.TrimSpace(supplierConfigGet(cfgCCMaxReportWebhook))
			if webhook == "" || !slices.Contains(ccmaxReportHours(), now.Hour()) {
				continue
			}
			if err := postLarkPayload(webhook, ccmaxReportCard(r)); err != nil {
				log.Printf("[ccmax-report] push: %v", err)
			}
		}
	}()
}

func handleCCMaxReportSettingsGet(c *gin.Context) {
	hours := supplierConfigGet(cfgCCMaxReportHours)
	if strings.TrimSpace(hours) == "" {
		hours = ccmaxReportDefaultHours
	}
	c.JSON(http.StatusOK, gin.H{
		"webhook": supplierConfigGet(cfgCCMaxReportWebhook),
		"hours":   hours,
	})
}

// handleCCMaxReportSettingsSet saves the webhook (empty = push off) and the
// push hours. Omitted fields stay unchanged.
func handleCCMaxReportSettingsSet(c *gin.Context) {
	var body struct {
		Webhook *string `json:"webhook"`
		Hours   *string `json:"hours"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if body.Webhook != nil {
		webhook := strings.TrimSpace(*body.Webhook)
		if webhook != "" && !strings.HasPrefix(webhook, "https://") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Webhook 须以 https:// 开头"})
			return
		}
		if err := supplierConfigSet(cfgCCMaxReportWebhook, webhook); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.Hours != nil {
		hours, err := ccmaxParseReportHours(*body.Hours)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		parts := make([]string, len(hours))
		for i, h := range hours {
			parts[i] = strconv.Itoa(h)
		}
		if err := supplierConfigSet(cfgCCMaxReportHours, strings.Join(parts, ",")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	handleCCMaxReportSettingsGet(c)
}

// handleCCMaxReportPush builds the report now and sends it to the configured
// webhook, returning the text that was sent.
func handleCCMaxReportPush(c *gin.Context) {
	webhook := strings.TrimSpace(supplierConfigGet(cfgCCMaxReportWebhook))
	if webhook == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先配置 Lark Webhook"})
		return
	}
	r, err := ccmaxBuildReport(time.Now())
	if err != nil {
		c.JSON(http.StatusFailedDependency, gin.H{"error": err.Error()})
		return
	}
	text := ccmaxReportText(r)
	if err := postLarkPayload(webhook, ccmaxReportCard(r)); err != nil {
		c.JSON(http.StatusFailedDependency, gin.H{"error": err.Error(), "text": text})
		return
	}
	c.JSON(http.StatusOK, gin.H{"text": text})
}

// ccmaxUserStats is one supplier_ccmax user's row on the CC Max users page:
// totals over the accounts they uploaded through report-service.
type ccmaxUserStats struct {
	ID           int64          `json:"id"`
	Username     string         `json:"username"`
	Status       int            `json:"status"`
	CreatedAt    int64          `json:"created_at"`
	Managers     []ccmaxUserRef `json:"managers"`
	Accounts     int            `json:"accounts"`
	Serving      int            `json:"serving"`
	InputTokens  int64          `json:"input_tokens"`
	OutputTokens int64          `json:"output_tokens"`
	CostRMB      float64        `json:"cost_rmb"`
	MonthCostRMB float64        `json:"month_cost_rmb"`
}

// handleCCMaxUsersStats lists every supplier_ccmax user with their managers
// and how much their accounts have run: account / running counts, tokens,
// cumulative cost and month-to-date cost (UTC+8 month, via the cost snapshots;
// accounts gone from the roster still count toward the month).
func handleCCMaxUsersStats(c *gin.Context) {
	if !ccmaxWeb.configured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "CC Max portal login not configured"})
		return
	}
	raws, err := fetchPortalAccountsRaw(ccmaxWeb, "claude")
	if err != nil {
		c.JSON(http.StatusFailedDependency, gin.H{"error": err.Error()})
		return
	}
	roster := map[int64]ccmaxAccountRow{}
	current := map[int64]int64{}
	for _, raw := range raws {
		row, err := parseCCMaxPortalRow(raw)
		if err != nil {
			continue
		}
		roster[row.ID] = row
		current[row.ID] = int64(math.Round(row.CostRMB * 1e6))
	}
	now := time.Now().In(supplierPortalLoc)
	monthCosts, err := ccmaxSnapshotMonthCosts(current, now)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	rows, err := db.Query(`SELECT id, username, status, created_at FROM rs_auth_user WHERE role=$1 ORDER BY username`, minSupplierCCMaxRole)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	users := []*ccmaxUserStats{}
	byID := map[int64]*ccmaxUserStats{}
	for rows.Next() {
		u := &ccmaxUserStats{Managers: []ccmaxUserRef{}}
		if err := rows.Scan(&u.ID, &u.Username, &u.Status, &u.CreatedAt); err != nil {
			rows.Close()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		users = append(users, u)
		byID[u.ID] = u
	}
	rows.Close()

	rows, err = db.Query(
		`SELECT m.member_id, u.id, u.username FROM rs_ccmax_manager_member m
		   JOIN rs_auth_user u ON u.id = m.manager_id ORDER BY u.username`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for rows.Next() {
		var memberID int64
		var m ccmaxUserRef
		if err := rows.Scan(&memberID, &m.ID, &m.Username); err != nil {
			rows.Close()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if u := byID[memberID]; u != nil {
			u.Managers = append(u.Managers, m)
		}
	}
	rows.Close()

	rows, err = db.Query(`SELECT remote_account_id, uploaded_by FROM rs_ccmax_account`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, uploadedBy int64
		if err := rows.Scan(&accountID, &uploadedBy); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		u := byID[uploadedBy]
		if u == nil {
			continue
		}
		u.MonthCostRMB += float64(monthCosts[accountID]) / 1e6
		row, onRoster := roster[accountID]
		if !onRoster {
			continue
		}
		u.Accounts++
		if slices.Contains(ccmaxServingStatuses, row.Status) {
			u.Serving++
		}
		u.InputTokens += row.InputTokens
		u.OutputTokens += row.OutputTokens
		u.CostRMB += row.CostRMB
	}

	c.JSON(http.StatusOK, gin.H{
		"users":      users,
		"fx_rate":    supplierFxRate(),
		"month_from": ccmaxMonthStart(now).Format("2006-01-02 15:04"),
		"month_to":   now.Format("2006-01-02 15:04"),
	})
}

// ---- Daily bill (日结算) ----
//
// The portal WEB bill page (/bill/query, the same call its 账单查询 page makes)
// returns each account's cost per day and model, at original price for claude
// accounts. Bills settle with a delay, so a day is pulled once, the next
// morning at ccmaxBillHour, into rs_ccmax_daily_bill; the daily page only
// reads that table.

const (
	// ccmaxBillHour (UTC+8) is when yesterday's bill is pulled.
	ccmaxBillHour = 8
	// cfgCCMaxBillSyncedThrough is the last UTC+8 day (YYYY-MM-DD) pulled.
	cfgCCMaxBillSyncedThrough = "ccmax_bill_synced_through"
	// ccmaxBillBatch is the portal's per-query account limit.
	ccmaxBillBatch = 100
	// ccmaxBillMaxDays bounds one pull and one /daily query; the portal keeps
	// 90 days of bills.
	ccmaxBillMaxDays = 90
)

// portalBillRow is one row of the portal WEB bill/query: an account's cost
// (RMB) on one UTC+8 day for one model (empty when the row names none).
type portalBillRow struct {
	AccountID int64
	DS        string
	Model     string
	Cost      float64
}

// portalBillQuery reads the portal WEB bill (/bill/query, the call behind its
// 账单查询 page) of the accounts for the UTC+8 days [from, to] through a login
// session, ccmaxBillBatch ids per call.
func portalBillQuery(s *portalWebSession, ids []int64, from, to time.Time) ([]portalBillRow, error) {
	out := []portalBillRow{}
	for batch := range slices.Chunk(ids, ccmaxBillBatch) {
		parts := make([]string, len(batch))
		for i, id := range batch {
			parts[i] = strconv.FormatInt(id, 10)
		}
		path := fmt.Sprintf("/supplier-account/api/bill/query?account_ids=%s&start_ds=%s&end_ds=%s",
			strings.Join(parts, ","), from.Format("20060102"), to.Format("20060102"))
		status, body, err := s.proxy(http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("bill query: %s", supplierErr(body))
		}
		var parsed struct {
			List []struct {
				AccountID int64   `json:"account_id"`
				DS        string  `json:"ds_text"`
				Cost      float64 `json:"cost"`
				Model     string  `json:"model"`
				ModelName string  `json:"model_name"`
			} `json:"list"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("unexpected bill response: %w", err)
		}
		for _, l := range parsed.List {
			out = append(out, portalBillRow{l.AccountID, l.DS, strings.TrimSpace(cmp.Or(l.Model, l.ModelName)), l.Cost})
		}
	}
	return out, nil
}

// ccmaxSyncDailyBills pulls the bill of every locally recorded CC Max account
// for the UTC+8 days [from, to] and replaces those days in
// rs_ccmax_daily_bill, then advances the synced-through day.
func ccmaxSyncDailyBills(from, to time.Time) error {
	rows, err := db.Query(`SELECT remote_account_id FROM rs_ccmax_account ORDER BY remote_account_id`)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()

	bill, err := portalBillQuery(ccmaxWeb, ids, from, to)
	if err != nil {
		return err
	}
	type key struct {
		id int64
		ds string
	}
	type modelKey struct {
		key
		model string
	}
	costs := map[key]float64{}
	modelCosts := map[modelKey]float64{}
	for _, l := range bill {
		costs[key{l.AccountID, l.DS}] += l.Cost
		modelCosts[modelKey{key{l.AccountID, l.DS}, l.Model}] += l.Cost
	}

	fromDS, toDS := from.Format("2006-01-02"), to.Format("2006-01-02")
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM rs_ccmax_daily_bill WHERE ds BETWEEN $1 AND $2`, fromDS, toDS); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM rs_ccmax_daily_bill_model WHERE ds BETWEEN $1 AND $2`, fromDS, toDS); err != nil {
		return err
	}
	now := time.Now().Unix()
	for k, cost := range costs {
		if _, err := tx.Exec(
			`INSERT INTO rs_ccmax_daily_bill (remote_account_id, ds, cost_micro, updated_at) VALUES ($1, $2, $3, $4)`,
			k.id, k.ds, int64(math.Round(cost*1e6)), now,
		); err != nil {
			return err
		}
	}
	for k, cost := range modelCosts {
		if _, err := tx.Exec(
			`INSERT INTO rs_ccmax_daily_bill_model (remote_account_id, ds, model, cost_micro, updated_at) VALUES ($1, $2, $3, $4, $5)`,
			k.id, k.ds, k.model, int64(math.Round(cost*1e6)), now,
		); err != nil {
			return err
		}
	}
	if toDS > supplierConfigGet(cfgCCMaxBillSyncedThrough) {
		if _, err := tx.Exec(
			`INSERT INTO report_config (key, value, updated_at) VALUES ($1, $2, $3)
			 ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=EXCLUDED.updated_at`,
			cfgCCMaxBillSyncedThrough, toDS, now,
		); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("[ccmax-bill] pulled %s ~ %s: %d account-days", fromDS, toDS, len(costs))
	return nil
}

// ccmaxCatchUpDailyBills pulls every day after the synced-through day up to
// yesterday (first run: from the earliest CC Max upload), within the portal's
// 90-day window. A no-op once yesterday is stored.
func ccmaxCatchUpDailyBills(now time.Time) error {
	now = now.In(supplierPortalLoc)
	yesterday := time.Date(now.Year(), now.Month(), now.Day()-1, 0, 0, 0, 0, supplierPortalLoc)
	from := yesterday.AddDate(0, 0, 1-ccmaxBillMaxDays)
	if through, err := time.ParseInLocation("2006-01-02", supplierConfigGet(cfgCCMaxBillSyncedThrough), supplierPortalLoc); err == nil {
		from = through.AddDate(0, 0, 1)
	} else {
		var first int64
		if err := db.QueryRow(`SELECT COALESCE(MIN(created_at), 0) FROM rs_ccmax_account`).Scan(&first); err != nil {
			return err
		}
		if first == 0 {
			return nil
		}
		t := time.Unix(first, 0).In(supplierPortalLoc)
		from = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, supplierPortalLoc)
	}
	if oldest := yesterday.AddDate(0, 0, 1-ccmaxBillMaxDays); from.Before(oldest) {
		from = oldest
	}
	if from.After(yesterday) {
		return nil
	}
	return ccmaxSyncDailyBills(from, yesterday)
}

// ccmaxParseDayRange parses a from/to UTC+8 day range (YYYY-MM-DD), at most
// ccmaxBillMaxDays long.
func ccmaxParseDayRange(fromDS, toDS string) (from, to time.Time, err error) {
	from, errFrom := time.ParseInLocation("2006-01-02", fromDS, supplierPortalLoc)
	to, errTo := time.ParseInLocation("2006-01-02", toDS, supplierPortalLoc)
	if errFrom != nil || errTo != nil || to.Before(from) {
		return from, to, errors.New("日期格式须为 YYYY-MM-DD，且开始不晚于结束")
	}
	if to.Sub(from) >= ccmaxBillMaxDays*24*time.Hour {
		return from, to, fmt.Errorf("一次最多 %d 天", ccmaxBillMaxDays)
	}
	return from, to, nil
}

// handleCCMaxDailySync re-pulls the bill for [from, to] (admin): backfill or
// correct days after the portal revises a bill. Days must be before today.
func handleCCMaxDailySync(c *gin.Context) {
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	from, to, err := ccmaxParseDayRange(body.From, body.To)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	now := time.Now().In(supplierPortalLoc)
	if !to.Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, supplierPortalLoc)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "只能拉取今天之前的账单"})
		return
	}
	if from.Before(now.AddDate(0, 0, -ccmaxBillMaxDays)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("portal 只保留近 %d 天的账单", ccmaxBillMaxDays)})
		return
	}
	if !ccmaxWeb.configured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "CC Max portal login not configured"})
		return
	}
	if err := ccmaxSyncDailyBills(from, to); err != nil {
		c.JSON(http.StatusFailedDependency, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"synced_through": supplierConfigGet(cfgCCMaxBillSyncedThrough)})
}
