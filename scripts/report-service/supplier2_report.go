package main

// 账号上号「账号 2」Lark usage report: every account of the second portal
// login, grouped by its owner (rs_supplier2_owner). Format, push loop and
// settings handlers are shared with the 账号上号 report
// (supplier_usage_report.go); only the keys and the account list differ.

import (
	"cmp"
	"fmt"
	"strings"
	"time"
)

const (
	cfgSupplier2ReportWebhook  = "supplier2_report_webhook"
	cfgSupplier2ReportHours    = "supplier2_report_hours"
	cfgSupplier2ReportLast     = "supplier2_report_last"
	cfgSupplier2ReportLastSlot = "supplier2_report_last_slot"
	cfgSupplier2ReportMinUSD   = "supplier2_report_min_usd"
)

var supplier2ReportLane = &supplierReportLane{
	tag:         "supplier2-report",
	cfgWebhook:  cfgSupplier2ReportWebhook,
	cfgHours:    cfgSupplier2ReportHours,
	cfgLast:     cfgSupplier2ReportLast,
	cfgLastSlot: cfgSupplier2ReportLastSlot,
	cfgMinUSD:   cfgSupplier2ReportMinUSD,
	build:       supplier2BuildReport,
}

// supplier2BuildReport reads the live roster and owners. Only a missing login
// or roster/owner failures fail the report; usage failures mark accounts.
func supplier2BuildReport(now time.Time) (supplierUsageReport, error) {
	now = now.In(supplierPortalLoc)
	r := supplierUsageReport{
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
	aliasToID := map[string]int64{}
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
		r.Accounts = append(r.Accounts, supplierUsageReportAccount{
			ID: id, Name: cmp.Or(strings.TrimSpace(remark), alias, "-"), Owner: owners[id].Username,
		})
	}
	supplierFillReportUsage("supplier2-report", token, r.Accounts, aliasToID, now)
	return r, nil
}
