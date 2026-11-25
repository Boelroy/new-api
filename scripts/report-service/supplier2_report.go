package main

// 账号上号「账号 2」Lark usage report: every account of the second portal
// login, grouped by its owner (rs_supplier2_owner), with today's cost and
// requests and its cumulative cost, pushed to an admin-configured webhook at
// admin-configured hours (UTC+8). The leader checks every minute and pushes
// each configured hour once (supplier2_report_last_slot); 立即推送 sends at
// once from whichever node takes the request. Every outcome is kept in
// supplier2_report_last for the settings card.
//
// Cost is the portal bill (RMB, ~2h delayed) ÷ supplier_fx_rate. "Today" runs
// from 00:00 UTC+8; cumulative is the bill's 90-day lookback. Accounts whose
// usage could not be fetched are listed apart instead of counting as 0.

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	cfgSupplier2ReportWebhook  = "supplier2_report_webhook"
	cfgSupplier2ReportHours    = "supplier2_report_hours"
	cfgSupplier2ReportLast     = "supplier2_report_last"
	cfgSupplier2ReportLastSlot = "supplier2_report_last_slot"

	supplier2Unassigned = "未分配"
)

// supplier2ReportAccount is one account's line in the report.
type supplier2ReportAccount struct {
	ID            int64
	Name          string // portal remark, else alias
	Owner         string // owner username; "" = unassigned
	TodayRequests int64
	TodayCostRMB  float64
	TotalCostRMB  float64
	Failed        bool // usage could not be fetched; left out of every sum
}

type supplier2Report struct {
	Label    string
	At       time.Time // UTC+8
	FxRate   float64
	Accounts []supplier2ReportAccount
}

// supplier2ReportGroup is one owner's accounts in a report.
type supplier2ReportGroup struct {
	Owner    string
	Accounts []supplier2ReportAccount
	Requests int64
	TodayRMB float64
}

// supplier2ReportText renders the report: a title, the overall line, one block
// per owner (most cost today first, 未分配 last) with a line per account that
// ran today and one count line for the idle rest, then failed accounts and the
// data notes.
func supplier2ReportText(r supplier2Report) string {
	fx := r.FxRate
	if fx <= 0 {
		fx = supplierDefaultFxRate
	}
	usd := func(rmb float64) string { return fmtUSD(rmb / fx) }

	groups := map[string]*supplier2ReportGroup{}
	var order []*supplier2ReportGroup
	var failed []supplier2ReportAccount
	var total supplier2ReportGroup
	var totalRMB float64
	for _, a := range r.Accounts {
		if a.Failed {
			failed = append(failed, a)
			continue
		}
		owner := cmp.Or(a.Owner, supplier2Unassigned)
		g := groups[owner]
		if g == nil {
			g = &supplier2ReportGroup{Owner: owner}
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
	slices.SortStableFunc(order, func(a, b *supplier2ReportGroup) int {
		if (a.Owner == supplier2Unassigned) != (b.Owner == supplier2Unassigned) {
			if a.Owner == supplier2Unassigned {
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
	fmt.Fprintf(&b, "[%s] 用量播报 %s (UTC+8)\n", cmp.Or(r.Label, supplier2DefaultLabel), r.At.In(supplierPortalLoc).Format("2006-01-02 15:00"))
	fmt.Fprintf(&b, "合计：%d 个账号 · 今日 %s · 请求 %d · 累计 %s\n",
		len(total.Accounts), usd(total.TodayRMB), total.Requests, usd(totalRMB))
	for _, g := range order {
		fmt.Fprintf(&b, "\n【%s】%d 个账号 · 今日 %s · 请求 %d\n", g.Owner, len(g.Accounts), usd(g.TodayRMB), g.Requests)
		slices.SortStableFunc(g.Accounts, func(x, y supplier2ReportAccount) int {
			if c := cmp.Compare(y.TodayCostRMB, x.TodayCostRMB); c != 0 {
				return c
			}
			if c := cmp.Compare(y.TodayRequests, x.TodayRequests); c != 0 {
				return c
			}
			return cmp.Compare(x.ID, y.ID)
		})
		idle := 0
		for _, a := range g.Accounts {
			if a.TodayRequests == 0 && a.TodayCostRMB == 0 {
				idle++
				continue
			}
			fmt.Fprintf(&b, "· #%d %s：今日 %s · 请求 %d · 累计 %s\n", a.ID, a.Name, usd(a.TodayCostRMB), a.TodayRequests, usd(a.TotalCostRMB))
		}
		if idle > 0 {
			fmt.Fprintf(&b, "· 另 %d 个账号今日无用量\n", idle)
		}
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

// supplier2BuildReport reads the roster, owners and usage as of now. A failed
// portal batch marks its accounts Failed rather than failing the report; only
// a missing login or roster/owner failures do.
func supplier2BuildReport(now time.Time) (supplier2Report, error) {
	now = now.In(supplierPortalLoc)
	r := supplier2Report{
		Label:  cmp.Or(strings.TrimSpace(supplierConfigGet(cfgSupplier2Label)), supplier2DefaultLabel),
		At:     now,
		FxRate: supplierFxRate(),
	}
	token := supplier2Token()
	if !supplier2Web.configured() || token == "" {
		return r, fmt.Errorf("%s 未配置登录或 OpenAPI token", r.Label)
	}
	roster, err := supplier2Roster()
	if err != nil {
		return r, fmt.Errorf("读取账号列表失败: %w", err)
	}
	owners, err := supplier2Owners(0)
	if err != nil {
		return r, fmt.Errorf("读取账号归属失败: %w", err)
	}

	ids := make([]int64, 0, len(roster))
	aliasToID := map[string]int64{}
	index := map[int64]int{}
	for _, row := range roster {
		id := supplier2RowID(row)
		if id == 0 {
			continue
		}
		remark, _ := row["remark"].(string)
		alias, _ := row["account_alias"].(string)
		if alias != "" {
			aliasToID[alias] = id
		}
		index[id] = len(r.Accounts)
		r.Accounts = append(r.Accounts, supplier2ReportAccount{
			ID: id, Name: cmp.Or(strings.TrimSpace(remark), alias, "-"), Owner: owners[id].Username,
		})
		ids = append(ids, id)
	}

	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, supplierPortalLoc)
	today := now.Format("2006-01-02")
	cumulativeStart := supplierBillStartDate(now.AddDate(0, 0, -supplierBillMaxLookbackDays).Unix())
	for start := 0; start < len(ids); start += 100 {
		chunk := ids[start:min(start+100, len(ids))]
		markFailed := func(err error) {
			log.Printf("[supplier2-report] usage of %d accounts failed: %v", len(chunk), err)
			for _, id := range chunk {
				r.Accounts[index[id]].Failed = true
			}
		}
		if now.After(dayStart) {
			rows, err := supplierFetchRealtimeMetrics(token, chunk, dayStart.Format(supplier2TimeLayout), now.Format(supplier2TimeLayout))
			if err != nil {
				markFailed(err)
				continue
			}
			for _, m := range rows {
				id, ok := aliasToID[m.AccountAlias]
				if !ok || m.Requests == nil {
					continue
				}
				if i, ok := index[id]; ok {
					r.Accounts[i].TodayRequests += *m.Requests
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
			r.Accounts[index[id]].TodayCostRMB = todayCost[id]
			r.Accounts[index[id]].TotalCostRMB = totalCost[id]
		}
	}
	return r, nil
}

// supplier2ReportResult is supplier2_report_last.
type supplier2ReportResult struct {
	At      int64  `json:"at"`
	OK      bool   `json:"ok"`
	Trigger string `json:"trigger"` // auto | manual
	Text    string `json:"text,omitempty"`
	Error   string `json:"error,omitempty"`
}

// supplier2PushReport builds and sends the report, records the outcome as
// supplier2_report_last and returns it.
func supplier2PushReport(now time.Time, trigger string) supplier2ReportResult {
	res := supplier2ReportResult{At: now.Unix(), Trigger: trigger}
	webhook := strings.TrimSpace(supplierConfigGet(cfgSupplier2ReportWebhook))
	if webhook == "" {
		res.Error = "未配置 Lark Webhook"
	} else if r, err := supplier2BuildReport(now); err != nil {
		res.Error = err.Error()
	} else {
		res.Text = supplier2ReportText(r)
		if err := postLark(webhook, res.Text); err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
		}
	}
	if !res.OK {
		log.Printf("[supplier2-report] %s push failed: %s", trigger, res.Error)
	}
	blob, _ := json.Marshal(res)
	if err := supplierConfigSet(cfgSupplier2ReportLast, string(blob)); err != nil {
		log.Printf("[supplier2-report] save last result: %v", err)
	}
	return res
}

// supplier2ReportHours is the configured push hours (UTC+8); empty = manual
// only.
func supplier2ReportHours() []int {
	hours, err := ccmaxParseReportHours(supplierConfigGet(cfgSupplier2ReportHours))
	if err != nil {
		return nil
	}
	return hours
}

// supplier2ReportTick pushes when now's hour is a configured push hour that
// has not been pushed yet. The slot is claimed before pushing, so a failed or
// slow push is not retried within the same hour. Reports whether it pushed.
func supplier2ReportTick(now time.Time, push func(time.Time)) bool {
	now = now.In(supplierPortalLoc)
	if strings.TrimSpace(supplierConfigGet(cfgSupplier2ReportWebhook)) == "" ||
		!slices.Contains(supplier2ReportHours(), now.Hour()) {
		return false
	}
	slot := now.Format("2006-01-02 15")
	if supplierConfigGet(cfgSupplier2ReportLastSlot) == slot {
		return false
	}
	if err := supplierConfigSet(cfgSupplier2ReportLastSlot, slot); err != nil {
		log.Printf("[supplier2-report] claim slot %s: %v", slot, err)
		return false
	}
	push(now)
	return true
}

// startSupplier2ReportLoop checks once a minute on the leader.
func startSupplier2ReportLoop() {
	go func() {
		for {
			time.Sleep(time.Minute)
			if !IsLeader() {
				continue
			}
			supplier2ReportTick(time.Now(), func(at time.Time) { supplier2PushReport(at, "auto") })
		}
	}()
}

func handleSupplier2ReportSettingsGet(c *gin.Context) {
	if !requireSupplier2Admin(c) {
		return
	}
	var last any
	if raw := supplierConfigGet(cfgSupplier2ReportLast); raw != "" {
		_ = json.Unmarshal([]byte(raw), &last)
	}
	webhook := strings.TrimSpace(supplierConfigGet(cfgSupplier2ReportWebhook))
	last4 := ""
	if len(webhook) > 12 {
		last4 = webhook[len(webhook)-4:]
	}
	c.JSON(http.StatusOK, gin.H{
		"webhook_set":   webhook != "",
		"webhook_last4": last4,
		"hours":         supplierConfigGet(cfgSupplier2ReportHours),
		"last":          last,
	})
}

// handleSupplier2ReportSettingsSet saves the webhook (blank keeps the stored
// one, clear_webhook turns the push off) and the UTC+8 push hours (empty =
// manual only). Omitted fields stay unchanged.
func handleSupplier2ReportSettingsSet(c *gin.Context) {
	if !requireSupplier2Admin(c) {
		return
	}
	var body struct {
		Webhook      *string `json:"webhook"`
		ClearWebhook bool    `json:"clear_webhook"`
		Hours        *string `json:"hours"`
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
		if err := supplierConfigSet(cfgSupplier2ReportWebhook, webhook); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save webhook"})
			return
		}
	}
	if hours != nil {
		if err := supplierConfigSet(cfgSupplier2ReportHours, strings.Join(hours, ",")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	handleSupplier2ReportSettingsGet(c)
}

// handleSupplier2ReportPush builds and sends the report now and returns the
// outcome (also kept as the last result).
func handleSupplier2ReportPush(c *gin.Context) {
	if !requireSupplier2Admin(c) {
		return
	}
	if strings.TrimSpace(supplierConfigGet(cfgSupplier2ReportWebhook)) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先配置 Lark Webhook"})
		return
	}
	res := supplier2PushReport(time.Now(), "manual")
	if !res.OK {
		c.JSON(http.StatusFailedDependency, gin.H{"error": res.Error, "last": res})
		return
	}
	c.JSON(http.StatusOK, gin.H{"last": res})
}
