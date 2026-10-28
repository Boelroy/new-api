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

	current := map[int64]int64{}
	var fiveSum, weekSum float64
	for _, raw := range raws {
		row, err := parseCCMaxPortalRow(raw)
		if err != nil {
			continue
		}
		r.Total++
		current[row.ID] = int64(math.Round(row.CostRMB * 1e6))
		if slices.Contains(ccmaxServingStatuses, row.Status) {
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
	for _, cost := range monthCosts {
		monthMicro += cost
	}
	r.MonthCostRMB = float64(monthMicro) / 1e6
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
// records the cost snapshot every hour, so month baselines stay current even
// with no push configured, and pushes at the configured hours when a webhook
// is set.
func startCCMaxReportLoop() {
	go func() {
		for {
			now := time.Now().In(supplierPortalLoc)
			time.Sleep(time.Until(now.Truncate(time.Hour).Add(time.Hour)))
			if !IsLeader() || !ccmaxWeb.configured() {
				continue
			}
			now = time.Now().In(supplierPortalLoc)
			r, err := ccmaxBuildReport(now)
			if err != nil {
				log.Printf("[ccmax-report] build: %v", err)
				continue
			}
			webhook := strings.TrimSpace(supplierConfigGet(cfgCCMaxReportWebhook))
			if webhook == "" || !slices.Contains(ccmaxReportHours(), now.Hour()) {
				continue
			}
			if err := postLark(webhook, ccmaxReportText(r)); err != nil {
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
	if err := postLark(webhook, text); err != nil {
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
