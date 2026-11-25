package main

// Lark usage reports of the 号池 portal logins: 账号上号 (account 1, the
// local rs_supplier_account list, below) and 账号 2 (supplier2_report.go).
// Each lane has its own report_config keys, builder and admin endpoints; the
// message format, usage fetching, push loop and settings handlers are shared.
//
// A report lists every account grouped by owner, with today's cost and
// requests and its cumulative cost, pushed to an admin-configured webhook at
// admin-configured hours (UTC+8). The leader checks every minute and pushes
// each configured hour once (<lane>_last_slot); 立即推送 sends at once from
// whichever node takes the request. Every outcome is kept in <lane>_last for
// the settings card.
//
// Cost is the portal bill (RMB, ~2h delayed) ÷ supplier_fx_rate. "Today" runs
// from 00:00 UTC+8; cumulative is the bill's 90-day lookback. Accounts whose
// usage could not be fetched are listed apart instead of counting as 0.

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
	cfgSupplierReportWebhook  = "supplier_report_webhook"
	cfgSupplierReportHours    = "supplier_report_hours"
	cfgSupplierReportLast     = "supplier_report_last"
	cfgSupplierReportLastSlot = "supplier_report_last_slot"
	cfgSupplierReportMinUSD   = "supplier_report_min_usd"

	// supplierReportDefaultMinUSD applies until an admin saves a threshold.
	supplierReportDefaultMinUSD = 10.0
	// supplierReportSmallNames caps the group names on the 小额 line.
	supplierReportSmallNames = 10

	supplierReportLabel      = "账号上号"
	supplierReportUnassigned = "未分配"
)

// supplierUsageReportAccount is one account's line in a report.
type supplierUsageReportAccount struct {
	ID            int64
	Name          string // alias or remark
	Owner         string // group name; "" = unassigned
	TodayRequests int64
	TodayCostRMB  float64
	TotalCostRMB  float64
	Failed        bool // usage could not be fetched; left out of every sum
}

type supplierUsageReport struct {
	Label  string
	At     time.Time // UTC+8
	FxRate float64
	// MinUSD is the today's-cost threshold (USD): groups below it fold into
	// one 小额 line, accounts below it into one line per group. 0 = list
	// every account that ran today.
	MinUSD   float64
	Accounts []supplierUsageReportAccount
}

// supplierUsageReportGroup is one owner's accounts in a report.
type supplierUsageReportGroup struct {
	Owner    string
	Accounts []supplierUsageReportAccount
	Requests int64
	TodayRMB float64
}

// supplierUsageReportText renders a report: a title, the overall line, one
// block per owner (most cost today first, 未分配 last) with a line per account
// at or above MinUSD today and one line for the rest, one line for the groups
// below MinUSD, then failed accounts and the data notes. With MinUSD 0 every
// group shows and every account that ran today gets its own line.
func supplierUsageReportText(r supplierUsageReport) string {
	fx := r.FxRate
	if fx <= 0 {
		fx = supplierDefaultFxRate
	}
	usd := func(rmb float64) string { return fmtUSD(rmb / fx) }

	groups := map[string]*supplierUsageReportGroup{}
	var order []*supplierUsageReportGroup
	var failed []supplierUsageReportAccount
	var total supplierUsageReportGroup
	var totalRMB float64
	for _, a := range r.Accounts {
		if a.Failed {
			failed = append(failed, a)
			continue
		}
		owner := cmp.Or(a.Owner, supplierReportUnassigned)
		g := groups[owner]
		if g == nil {
			g = &supplierUsageReportGroup{Owner: owner}
			groups[owner] = g
			order = append(order, g)
		}
		g.Accounts = append(g.Accounts, a)
		g.Requests += a.TodayRequests
		g.TodayRMB += a.TodayCostRMB
		total.Accounts = append(total.Accounts, a)
		total.Requests += a.TodayRequests
		total.TodayRMB += a.TodayCostRMB
		totalRMB += a.TotalCostRMB
	}
	slices.SortStableFunc(order, func(a, b *supplierUsageReportGroup) int {
		if (a.Owner == supplierReportUnassigned) != (b.Owner == supplierReportUnassigned) {
			if a.Owner == supplierReportUnassigned {
				return 1
			}
			return -1
		}
		if c := cmp.Compare(b.TodayRMB, a.TodayRMB); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Requests, a.Requests); c != 0 {
			return c
		}
		return strings.Compare(a.Owner, b.Owner)
	})

	var b strings.Builder
	fmt.Fprintf(&b, "[%s] 用量播报 %s (UTC+8)\n", r.Label, r.At.In(supplierPortalLoc).Format("2006-01-02 15:00"))
	fmt.Fprintf(&b, "合计：%d 个账号 · 今日 %s · 请求 %d · 累计 %s\n",
		len(total.Accounts), usd(total.TodayRMB), total.Requests, usd(totalRMB))
	var small supplierUsageReportGroup
	var smallNames []string
	for _, g := range order {
		if r.MinUSD > 0 && g.TodayRMB/fx < r.MinUSD {
			small.Accounts = append(small.Accounts, g.Accounts...)
			small.Requests += g.Requests
			small.TodayRMB += g.TodayRMB
			smallNames = append(smallNames, g.Owner)
			continue
		}
		fmt.Fprintf(&b, "\n【%s】%d 个账号 · 今日 %s · 请求 %d\n", g.Owner, len(g.Accounts), usd(g.TodayRMB), g.Requests)
		slices.SortStableFunc(g.Accounts, func(x, y supplierUsageReportAccount) int {
			if c := cmp.Compare(y.TodayCostRMB, x.TodayCostRMB); c != 0 {
				return c
			}
			if c := cmp.Compare(y.TodayRequests, x.TodayRequests); c != 0 {
				return c
			}
			return cmp.Compare(x.ID, y.ID)
		})
		var rest supplierUsageReportGroup
		for _, a := range g.Accounts {
			ran := a.TodayRequests != 0 || a.TodayCostRMB != 0
			if (r.MinUSD > 0 && a.TodayCostRMB/fx >= r.MinUSD) || (r.MinUSD <= 0 && ran) {
				fmt.Fprintf(&b, "· #%d %s：今日 %s · 请求 %d · 累计 %s\n", a.ID, a.Name, usd(a.TodayCostRMB), a.TodayRequests, usd(a.TotalCostRMB))
				continue
			}
			rest.Accounts = append(rest.Accounts, a)
			rest.Requests += a.TodayRequests
			rest.TodayRMB += a.TodayCostRMB
		}
		switch {
		case len(rest.Accounts) == 0:
		case rest.Requests == 0 && rest.TodayRMB == 0:
			fmt.Fprintf(&b, "· 另 %d 个账号今日无用量\n", len(rest.Accounts))
		default:
			fmt.Fprintf(&b, "· 另 %d 个账号合计 %s · 请求 %d\n", len(rest.Accounts), usd(rest.TodayRMB), rest.Requests)
		}
	}
	if len(smallNames) > 0 {
		names := strings.Join(smallNames[:min(len(smallNames), supplierReportSmallNames)], "、")
		if len(smallNames) > supplierReportSmallNames {
			names += "等"
		}
		fmt.Fprintf(&b, "\n小额/无用量：%d 组 %d 个账号，合计 %s · 请求 %d（%s）\n",
			len(smallNames), len(small.Accounts), usd(small.TodayRMB), small.Requests, names)
	}
	if len(failed) > 0 {
		ids := make([]string, len(failed))
		for i, a := range failed {
			ids[i] = "#" + strconv.FormatInt(a.ID, 10)
		}
		fmt.Fprintf(&b, "\n⚠ %d 个账号用量查询失败，未计入合计：%s\n", len(failed), strings.Join(ids, " "))
	}
	fmt.Fprintf(&b, "\n注：费用来自号池账单，约有 2 小时延迟；今日从 00:00 (UTC+8) 起，累计为近 %d 天；汇率 %g", supplierBillMaxLookbackDays, fx)
	return b.String()
}

// supplierFillReportUsage sets today's requests (realtime metrics from 00:00
// UTC+8) and today's / cumulative cost (bill) on each account, in batches of
// the portal's 100-id cap. Metrics rows are matched back through the account
// alias. A failed batch marks its accounts Failed instead of counting them as
// 0.
func supplierFillReportUsage(tag, token string, accounts []supplierUsageReportAccount, aliasToID map[string]int64, now time.Time) {
	now = now.In(supplierPortalLoc)
	index := make(map[int64]int, len(accounts))
	ids := make([]int64, 0, len(accounts))
	for i, a := range accounts {
		index[a.ID] = i
		ids = append(ids, a.ID)
	}
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, supplierPortalLoc)
	today := now.Format("2006-01-02")
	cumulativeStart := supplierBillStartDate(now.AddDate(0, 0, -supplierBillMaxLookbackDays).Unix())
	for start := 0; start < len(ids); start += 100 {
		chunk := ids[start:min(start+100, len(ids))]
		markFailed := func(err error) {
			log.Printf("[%s] usage of %d accounts failed: %v", tag, len(chunk), err)
			for _, id := range chunk {
				accounts[index[id]].Failed = true
			}
		}
		requests := map[int64]int64{}
		if now.After(dayStart) {
			rows, err := supplierFetchRealtimeMetrics(token, chunk, dayStart.Format(supplier2TimeLayout), now.Format(supplier2TimeLayout))
			if err != nil {
				markFailed(err)
				continue
			}
			for _, m := range rows {
				if id, ok := aliasToID[m.AccountAlias]; ok && m.Requests != nil {
					requests[id] += *m.Requests
				}
			}
		}
		todayCost, err := supplierBillCostRMB(token, chunk, today, today)
		if err != nil {
			markFailed(err)
			continue
		}
		totalCost, err := supplierBillCostRMB(token, chunk, cumulativeStart, today)
		if err != nil {
			markFailed(err)
			continue
		}
		for _, id := range chunk {
			a := &accounts[index[id]]
			a.TodayRequests = requests[id]
			a.TodayCostRMB = todayCost[id]
			a.TotalCostRMB = totalCost[id]
		}
	}
}

// supplierBuildReport is the 账号上号 report: every account of the local
// rs_supplier_account list, grouped by its uploader's username, else its
// studio, else 未分配.
func supplierBuildReport(now time.Time) (supplierUsageReport, error) {
	now = now.In(supplierPortalLoc)
	r := supplierUsageReport{Label: supplierReportLabel, At: now, FxRate: supplierFxRate()}
	if !supplierOpenAPIConfigured() {
		return r, errors.New("账号上号未配置号池地址或 OpenAPI token")
	}
	rows, err := db.Query(`SELECT a.remote_account_id, a.alias, a.remark, COALESCE(u.username, ''), a.studio
	                         FROM rs_supplier_account a
	                         LEFT JOIN rs_auth_user u ON u.id = a.uploaded_by
	                        ORDER BY a.id`)
	if err != nil {
		return r, fmt.Errorf("读取账号列表失败: %w", err)
	}
	defer rows.Close()
	aliasToID := map[string]int64{}
	for rows.Next() {
		var (
			id                              int64
			alias, remark, username, studio string
		)
		if err := rows.Scan(&id, &alias, &remark, &username, &studio); err != nil {
			return r, fmt.Errorf("读取账号列表失败: %w", err)
		}
		if alias != "" {
			aliasToID[alias] = id
		}
		r.Accounts = append(r.Accounts, supplierUsageReportAccount{
			ID:    id,
			Name:  cmp.Or(strings.TrimSpace(alias), strings.TrimSpace(remark), "-"),
			Owner: cmp.Or(strings.TrimSpace(username), strings.TrimSpace(studio)),
		})
	}
	if err := rows.Err(); err != nil {
		return r, fmt.Errorf("读取账号列表失败: %w", err)
	}
	supplierFillReportUsage("supplier-report", effectiveOpenAPIToken(), r.Accounts, aliasToID, now)
	return r, nil
}

// supplierReportLane is one login's report: its report_config keys and how
// to build it.
type supplierReportLane struct {
	tag                                                   string // log prefix
	cfgWebhook, cfgHours, cfgLast, cfgLastSlot, cfgMinUSD string
	build                                                 func(now time.Time) (supplierUsageReport, error)
}

var supplierReportLaneMain = &supplierReportLane{
	tag:         "supplier-report",
	cfgWebhook:  cfgSupplierReportWebhook,
	cfgHours:    cfgSupplierReportHours,
	cfgLast:     cfgSupplierReportLast,
	cfgLastSlot: cfgSupplierReportLastSlot,
	cfgMinUSD:   cfgSupplierReportMinUSD,
	build:       supplierBuildReport,
}

// supplierReportResult is <lane>_last.
type supplierReportResult struct {
	At      int64  `json:"at"`
	OK      bool   `json:"ok"`
	Trigger string `json:"trigger"` // auto | manual
	Text    string `json:"text,omitempty"`
	Error   string `json:"error,omitempty"`
}

// push builds and sends the report, records the outcome as the lane's last
// result and returns it.
func (l *supplierReportLane) push(now time.Time, trigger string) supplierReportResult {
	res := supplierReportResult{At: now.Unix(), Trigger: trigger}
	webhook := strings.TrimSpace(supplierConfigGet(l.cfgWebhook))
	if webhook == "" {
		res.Error = "未配置 Lark Webhook"
	} else if r, err := l.build(now); err != nil {
		res.Error = err.Error()
	} else {
		r.MinUSD = l.minUSD()
		res.Text = supplierUsageReportText(r)
		if err := postLark(webhook, res.Text); err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
		}
	}
	if !res.OK {
		log.Printf("[%s] %s push failed: %s", l.tag, trigger, res.Error)
	}
	blob, _ := json.Marshal(res)
	if err := supplierConfigSet(l.cfgLast, string(blob)); err != nil {
		log.Printf("[%s] save last result: %v", l.tag, err)
	}
	return res
}

// minUSD is the configured threshold (USD, ≥ 0); the default when unset or
// unreadable.
func (l *supplierReportLane) minUSD() float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(supplierConfigGet(l.cfgMinUSD)), 64)
	if err != nil || v < 0 || math.IsInf(v, 0) {
		return supplierReportDefaultMinUSD
	}
	return v
}

// hours is the configured push hours (UTC+8); empty = manual only.
func (l *supplierReportLane) hours() []int {
	hours, err := ccmaxParseReportHours(supplierConfigGet(l.cfgHours))
	if err != nil {
		return nil
	}
	return hours
}

// tick pushes when now's hour is a configured push hour that has not been
// pushed yet. The slot is claimed before pushing, so a failed or slow push is
// not retried within the same hour. Reports whether it pushed.
func (l *supplierReportLane) tick(now time.Time, push func(time.Time)) bool {
	now = now.In(supplierPortalLoc)
	if strings.TrimSpace(supplierConfigGet(l.cfgWebhook)) == "" || !slices.Contains(l.hours(), now.Hour()) {
		return false
	}
	slot := now.Format("2006-01-02 15")
	if supplierConfigGet(l.cfgLastSlot) == slot {
		return false
	}
	if err := supplierConfigSet(l.cfgLastSlot, slot); err != nil {
		log.Printf("[%s] claim slot %s: %v", l.tag, slot, err)
		return false
	}
	push(now)
	return true
}

// startSupplierReportLoops checks every lane once a minute on the leader.
func startSupplierReportLoops() {
	go func() {
		for {
			time.Sleep(time.Minute)
			if !IsLeader() {
				continue
			}
			for _, l := range []*supplierReportLane{supplierReportLaneMain, supplier2ReportLane} {
				l.tick(time.Now(), func(at time.Time) { l.push(at, "auto") })
			}
		}
	}()
}

// requireReportAdmin rejects non-admin callers. The routes are gated with
// requireRole too; the 账号上号 group itself also admits suppliers.
func requireReportAdmin(c *gin.Context) bool {
	if callerIsSupplierAdmin(c) {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	return false
}

func (l *supplierReportLane) handleSettingsGet(c *gin.Context) {
	if !requireReportAdmin(c) {
		return
	}
	var last any
	if raw := supplierConfigGet(l.cfgLast); raw != "" {
		_ = json.Unmarshal([]byte(raw), &last)
	}
	webhook := strings.TrimSpace(supplierConfigGet(l.cfgWebhook))
	last4 := ""
	if len(webhook) > 12 {
		last4 = webhook[len(webhook)-4:]
	}
	c.JSON(http.StatusOK, gin.H{
		"webhook_set":   webhook != "",
		"webhook_last4": last4,
		"hours":         supplierConfigGet(l.cfgHours),
		"min_usd":       l.minUSD(),
		"last":          last,
	})
}

// handleSettingsSet saves the webhook (blank keeps the stored one,
// clear_webhook turns the push off) and the UTC+8 push hours (empty = manual
// only) and min_usd (≥ 0, 0 = no folding). Omitted fields stay unchanged.
func (l *supplierReportLane) handleSettingsSet(c *gin.Context) {
	if !requireReportAdmin(c) {
		return
	}
	var body struct {
		Webhook      *string  `json:"webhook"`
		ClearWebhook bool     `json:"clear_webhook"`
		Hours        *string  `json:"hours"`
		MinUSD       *float64 `json:"min_usd"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	webhook := ""
	if body.Webhook != nil {
		webhook = strings.TrimSpace(*body.Webhook)
	}
	if webhook != "" && !strings.HasPrefix(webhook, "https://") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Webhook 须以 https:// 开头"})
		return
	}
	if body.MinUSD != nil && (*body.MinUSD < 0 || math.IsInf(*body.MinUSD, 0)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "最小播报金额须为非负数"})
		return
	}
	var hours []string
	if body.Hours != nil {
		parsed, err := ccmaxParseReportHours(*body.Hours)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		hours = make([]string, len(parsed))
		for i, h := range parsed {
			hours[i] = strconv.Itoa(h)
		}
	}
	if webhook != "" || body.ClearWebhook {
		if err := supplierConfigSet(l.cfgWebhook, webhook); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save webhook"})
			return
		}
	}
	if hours != nil {
		if err := supplierConfigSet(l.cfgHours, strings.Join(hours, ",")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.MinUSD != nil {
		if err := supplierConfigSet(l.cfgMinUSD, strconv.FormatFloat(*body.MinUSD, 'f', -1, 64)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	l.handleSettingsGet(c)
}

// handlePush builds and sends the report now and returns the outcome (also
// kept as the last result).
func (l *supplierReportLane) handlePush(c *gin.Context) {
	if !requireReportAdmin(c) {
		return
	}
	if strings.TrimSpace(supplierConfigGet(l.cfgWebhook)) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先配置 Lark Webhook"})
		return
	}
	res := l.push(time.Now(), "manual")
	if !res.OK {
		c.JSON(http.StatusFailedDependency, gin.H{"error": res.Error, "last": res})
		return
	}
	c.JSON(http.StatusOK, gin.H{"last": res})
}
