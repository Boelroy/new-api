package main

// 腾讯一站 · 月对账: the monthly statement in the layout of the customer
// template's 「API账单」 (columns A–N; one row per 3P account, per CC Max
// supplier, per proxy channel) and 「汇总表」
// sheets, summed from three sources:
//
//   - third_party (账号上号 / 老台子): the portal WEB bill/query of the month
//     for every non-claude account on the portal roster (/supplier/accounts),
//     cost in RMB; the local rs_supplier_account only names the uploader.
//   - ccmax: rs_ccmax_daily_bill (the same portal bill, pulled daily for the
//     CC Max accounts), RMB, summed per supplier.
//   - proxy: each proxy_channel_ownership channel's consume logs of the month
//     on newapi-proxy's upstream New API (/api/log/stat), queried through the
//     remote profile picked as that upstream, USD.
//
// G is USD: RMB costs are divided by the month's rate (default the system
// supplier_fx_rate). Months are Beijing calendar months. The month's rate,
// reference rate (K), discount (I) and tax rate (H) are the only inputs, set
// per month under /api/tencent/params/months, super admin only.

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

const (
	tencentPlatformThirdParty = "third_party"
	tencentPlatformCCMax      = "ccmax"
	tencentPlatformProxy      = "proxy"

	// tencentCCMaxFallbackModel is column D for CC Max accounts whose bill
	// carried no model (days pulled before per-model storage).
	tencentCCMaxFallbackModel = "Claude（CC Max 订阅）"
	// tencentDefaultTaxRate is column H unless the month sets another.
	tencentDefaultTaxRate = "含票"
	// tencentPortalBillDays is how far back the portal keeps bills.
	tencentPortalBillDays = 90

	// cfgTencentProxyProfileID picks the remote_newapi_profile that is
	// newapi-proxy's upstream New API; proxy rows are queried through it.
	cfgTencentProxyProfileID = "tencent_proxy_profile_id"
	// tencentProxyConcurrency bounds parallel upstream channel queries; the
	// dev upstream answers 429 to 8 at a time.
	tencentProxyConcurrency = 2

	// tencentProxyQuotaPerUSD is newapi's default quota unit (500,000 quota
	// = 1 USD), used when the upstream /api/status does not say.
	tencentProxyQuotaPerUSD = 500000
)

// tencentNow is the clock that bounds the portal bill query (today, and the
// portal's retention); tests pin it.
var tencentNow = time.Now

// tencentProxyRetryWait is the first back-off after an upstream 429; tests
// shorten it.
var tencentProxyRetryWait = 2 * time.Second

var tencentPlatforms = []string{tencentPlatformThirdParty, tencentPlatformCCMax, tencentPlatformProxy}

// tencentSourcePrefix is column N per platform; the summary sheet's SUMIFS
// match it as a prefix ("third_party*").
var tencentSourcePrefix = map[string]string{
	tencentPlatformThirdParty: "third_party",
	tencentPlatformCCMax:      "ccmax",
	tencentPlatformProxy:      "newapi proxy",
}

// tencentStatementHeaders and tencentSummaryHeaders are the template's header
// rows, A1 onwards.
var tencentStatementHeaders = []string{
	"供应商名称", "账单月份", "模型厂商名称", "模型名称", "账号ID", "key",
	"付给官方的费用（不区分币种）", "税率", "对应折扣", "腾讯支付金额（不区分币种）",
	"参考汇率\nhttps://www.kylc.com/huilv/d-boc-usd.html", "开票金额（RMB/元）",
	"官方导出的账单名称", "来源系统 / 上号人",
}

var tencentSummaryHeaders = []string{
	"供应商名称", "账单月份", "模型厂商名称", "付给官方的费用（不区分币种）", "开票金额（RMB/元）", "备注",
}

var tencentSchemaStatements = []string{
	// Per-month statement inputs. fx_rate is the RMB÷rate divisor for column G
	// (NULL = the system supplier_fx_rate at statement time); ref_fx_rate is
	// column K, the BOC USD rate entered by hand (NULL = the column G rate);
	// discount is column I as a fraction (NULL = the 上号 default discount);
	// tax_rate is column H (NULL = 含票).
	`CREATE TABLE IF NOT EXISTS rs_tencent_month_param (
		month       TEXT PRIMARY KEY,
		fx_rate     DOUBLE PRECISION,
		ref_fx_rate DOUBLE PRECISION,
		note        TEXT   NOT NULL DEFAULT '',
		updated_at  BIGINT NOT NULL,
		updated_by  BIGINT NOT NULL DEFAULT 0
	)`,
	`ALTER TABLE rs_tencent_month_param ADD COLUMN IF NOT EXISTS discount DOUBLE PRECISION`,
	`ALTER TABLE rs_tencent_month_param ADD COLUMN IF NOT EXISTS tax_rate TEXT`,
	// Month-end snapshots (tencent_statement_export.go). A month is a draft
	// until confirmed; a confirmed month no longer follows the source data.
	// params holds the discount and tax rate the lines were built with (JSON);
	// the rates are columns.
	`CREATE TABLE IF NOT EXISTS rs_tencent_statement (
		month        TEXT PRIMARY KEY,
		status       TEXT   NOT NULL,
		fx_rate      DOUBLE PRECISION NOT NULL,
		ref_fx_rate  DOUBLE PRECISION NOT NULL,
		params       TEXT   NOT NULL DEFAULT '{}',
		warnings     TEXT   NOT NULL DEFAULT '[]',
		generated_at BIGINT NOT NULL,
		generated_by BIGINT NOT NULL DEFAULT 0,
		confirmed_at BIGINT NOT NULL DEFAULT 0,
		confirmed_by BIGINT NOT NULL DEFAULT 0
	)`,
	// One row per statement line (A–N plus the source amounts). rmb_priced
	// and discount_from are left over from the first version and unused.
	`CREATE TABLE IF NOT EXISTS rs_tencent_statement_line (
		month         TEXT    NOT NULL,
		seq           INT     NOT NULL,
		line_key      TEXT    NOT NULL,
		platform      TEXT    NOT NULL,
		user_id       BIGINT  NOT NULL,
		supplier      TEXT    NOT NULL,
		bill_month    INT     NOT NULL,
		vendor        TEXT    NOT NULL,
		model         TEXT    NOT NULL,
		account_id    TEXT    NOT NULL,
		key_col       TEXT    NOT NULL DEFAULT '',
		official_usd  DOUBLE PRECISION NOT NULL,
		tax_rate      TEXT    NOT NULL DEFAULT '',
		discount      DOUBLE PRECISION NOT NULL,
		tencent_usd   DOUBLE PRECISION NOT NULL,
		ref_fx_rate   DOUBLE PRECISION NOT NULL,
		invoice_rmb   DOUBLE PRECISION NOT NULL,
		bill_name     TEXT    NOT NULL DEFAULT '',
		source        TEXT    NOT NULL,
		cost_cny      DOUBLE PRECISION NOT NULL DEFAULT 0,
		cost_usd      DOUBLE PRECISION NOT NULL DEFAULT 0,
		rmb_priced    BOOLEAN NOT NULL DEFAULT FALSE,
		discount_from TEXT    NOT NULL DEFAULT '',
		PRIMARY KEY (month, line_key)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_tencent_statement_line_seq ON rs_tencent_statement_line(month, seq)`,
	// Push attempts (tencent_push.go): kind statement for now; trigger is
	// manual or schedule; detail is the pushed totals (JSON).
	`CREATE TABLE IF NOT EXISTS rs_tencent_push_log (
		id         BIGSERIAL PRIMARY KEY,
		kind       TEXT    NOT NULL,
		trigger    TEXT    NOT NULL,
		month      TEXT    NOT NULL DEFAULT '',
		ok         BOOLEAN NOT NULL,
		error      TEXT    NOT NULL DEFAULT '',
		detail     TEXT    NOT NULL DEFAULT '{}',
		created_at BIGINT  NOT NULL,
		created_by BIGINT  NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS idx_tencent_push_log_kind ON rs_tencent_push_log(kind, id DESC)`,
}

// initTencentSchema applies the 腾讯一站 DDLs. Idempotent; fatal on failure
// like initV2Schema.
func initTencentSchema() {
	for _, ddl := range tencentSchemaStatements {
		if _, err := db.Exec(ddl); err != nil {
			log.Fatalf("tencent schema init failed on %.80q: %v", ddl, err)
		}
	}
}

// tencentSourceRow is one account's (proxy: one channel's) month cost on one
// platform, before the month's parameters are applied.
type tencentSourceRow struct {
	Platform  string
	UserID    int64
	Supplier  string // column A
	AccountID int64  // column E
	Vendor    string // column C
	Models    []string
	Label     string // column F
	CostCNY   float64
	CostUSD   float64
}

// tencentStatementParams are a month's statement inputs.
type tencentStatementParams struct {
	Month     time.Time `json:"-"`
	FxRate    float64   `json:"fx_rate"`
	RefFxRate float64   `json:"ref_fx_rate"`
	Discount  float64   `json:"discount"`
	TaxRate   string    `json:"tax_rate"`
}

// tencentLine is one 「API账单」 row; the json names of A–N follow the
// template column order.
type tencentLine struct {
	LineKey     string  `json:"line_key"`
	Platform    string  `json:"platform"`
	UserID      int64   `json:"user_id"`
	Supplier    string  `json:"supplier"`     // A
	BillMonth   int     `json:"bill_month"`   // B
	Vendor      string  `json:"vendor"`       // C
	Model       string  `json:"model"`        // D
	AccountID   string  `json:"account_id"`   // E
	Key         string  `json:"key"`          // F
	OfficialUSD float64 `json:"official_usd"` // G
	TaxRate     string  `json:"tax_rate"`     // H
	Discount    float64 `json:"discount"`     // I, a fraction (0.88)
	TencentUSD  float64 `json:"tencent_usd"`  // J = ROUND(G×I, 2)
	RefFxRate   float64 `json:"ref_fx_rate"`  // K
	InvoiceRMB  float64 `json:"invoice_rmb"`  // L = J×K
	BillName    string  `json:"bill_name"`    // M
	Source      string  `json:"source"`       // N
	CostCNY     float64 `json:"cost_cny"`
	CostUSD     float64 `json:"cost_usd"`
}

// tencentSummaryRow is one 「汇总表」 row: G and L summed per supplier ×
// month × vendor × source.
type tencentSummaryRow struct {
	Supplier    string  `json:"supplier"`
	BillMonth   int     `json:"bill_month"`
	Vendor      string  `json:"vendor"`
	Source      string  `json:"source"`
	OfficialUSD float64 `json:"official_usd"`
	InvoiceRMB  float64 `json:"invoice_rmb"`
	Lines       int     `json:"lines"`
}

// tencentRound rounds half away from zero to n decimals like Excel ROUND,
// absorbing the binary error of values such as 1.005.
func tencentRound(x float64, n int) float64 {
	p := math.Pow10(n)
	return math.Round(x*p+math.Copysign(1e-7, x)) / p
}

// tencentMonthRange parses a YYYY-MM month into its Beijing [start, end).
func tencentMonthRange(month string) (time.Time, time.Time, error) {
	start, err := time.ParseInLocation("2006-01", strings.TrimSpace(month), billingExportZone)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("month 须为 YYYY-MM")
	}
	return start, start.AddDate(0, 1, 0), nil
}

// buildTencentStatement applies the month parameters to the source rows (one
// per platform account) and returns the 「API账单」 and 「汇总表」 rows.
func buildTencentStatement(rows []tencentSourceRow, p tencentStatementParams) ([]tencentLine, []tencentSummaryRow) {
	lines := make([]tencentLine, 0, len(rows))
	for _, r := range rows {
		// CC Max rows are per supplier: no account id (E), keyed and named
		// (M) by the supplier instead.
		account, key := "", r.Supplier
		billName := r.Vendor + "+" + r.Supplier
		if r.AccountID > 0 {
			account = strconv.FormatInt(r.AccountID, 10)
			key = account
			billName = r.Vendor + "+" + account + "+" + r.Supplier
		}
		g := tencentRound(r.CostUSD+r.CostCNY/p.FxRate, 2)
		j := tencentRound(g*p.Discount, 2)
		lines = append(lines, tencentLine{
			LineKey:     fmt.Sprintf("%s|%s|%s", p.Month.Format("2006-01"), r.Platform, key),
			Platform:    r.Platform,
			UserID:      r.UserID,
			Supplier:    r.Supplier,
			BillMonth:   int(p.Month.Month()),
			Vendor:      r.Vendor,
			Model:       strings.Join(r.Models, ", "),
			AccountID:   account,
			Key:         r.Label,
			OfficialUSD: g,
			TaxRate:     p.TaxRate,
			Discount:    p.Discount,
			TencentUSD:  j,
			RefFxRate:   p.RefFxRate,
			// J has 2 decimals and K at most 4, so 6 keep J×K exact.
			InvoiceRMB: tencentRound(j*p.RefFxRate, 6),
			BillName:   billName,
			Source:     tencentSourcePrefix[r.Platform],
			CostCNY:    r.CostCNY,
			CostUSD:    r.CostUSD,
		})
	}
	slices.SortStableFunc(lines, func(a, b tencentLine) int {
		return cmp.Or(
			cmp.Compare(a.Supplier, b.Supplier),
			cmp.Compare(slices.Index(tencentPlatforms, a.Platform), slices.Index(tencentPlatforms, b.Platform)),
			cmp.Compare(a.Vendor, b.Vendor),
			cmp.Compare(len(a.AccountID), len(b.AccountID)),
			cmp.Compare(a.AccountID, b.AccountID),
		)
	})
	return lines, tencentSummarize(lines)
}

// tencentSummarize builds the 「汇总表」 rows of statement lines: G and L
// summed per supplier × month × vendor × source, the grouping the exported
// SUMIFS formulas reproduce.
func tencentSummarize(lines []tencentLine) []tencentSummaryRow {
	type summaryKey struct {
		supplier string
		month    int
		vendor   string
		source   string
	}
	sums := map[summaryKey]*tencentSummaryRow{}
	for _, l := range lines {
		k := summaryKey{l.Supplier, l.BillMonth, l.Vendor, l.Source}
		s, ok := sums[k]
		if !ok {
			s = &tencentSummaryRow{Supplier: k.supplier, BillMonth: k.month, Vendor: k.vendor, Source: k.source}
			sums[k] = s
		}
		s.OfficialUSD += l.OfficialUSD
		s.InvoiceRMB += l.InvoiceRMB
		s.Lines++
	}
	summary := make([]tencentSummaryRow, 0, len(sums))
	for _, s := range sums {
		s.OfficialUSD = tencentRound(s.OfficialUSD, 2)
		s.InvoiceRMB = tencentRound(s.InvoiceRMB, 2)
		summary = append(summary, *s)
	}
	slices.SortFunc(summary, func(a, b tencentSummaryRow) int {
		return cmp.Or(cmp.Compare(a.Supplier, b.Supplier), cmp.Compare(a.BillMonth, b.BillMonth),
			cmp.Compare(slices.Index(tencentPlatforms, tencentPlatformOfSource(a.Source)), slices.Index(tencentPlatforms, tencentPlatformOfSource(b.Source))),
			cmp.Compare(a.Vendor, b.Vendor))
	})
	return summary
}

func tencentPlatformOfSource(source string) string {
	for p, s := range tencentSourcePrefix {
		if s == source {
			return p
		}
	}
	return ""
}

// tencentAccountLabel is column F of a portal account: its alias, then *** and
// the last five characters of its key, as the template shows them.
func tencentAccountLabel(alias, apiKey string) string {
	alias, apiKey = strings.TrimSpace(alias), strings.TrimSpace(apiKey)
	if apiKey == "" {
		return alias
	}
	tail := []rune(apiKey)
	return strings.TrimSpace(alias + " ***" + string(tail[max(0, len(tail)-5):]))
}

// loadTencentSources reads every platform's cost in the Beijing month
// [start, end). Warnings name cost that could not be billed or may be partial.
func loadTencentSources(start, end time.Time) ([]tencentSourceRow, []string, error) {
	out, warnings, err := loadTencentThirdPartyRows(start, end, tencentNow())
	if err != nil {
		return nil, nil, fmt.Errorf("third_party bill: %w", err)
	}
	ccmaxRows, err := loadTencentCCMaxRows(start, end)
	if err != nil {
		return nil, nil, fmt.Errorf("ccmax bill: %w", err)
	}
	out = append(out, ccmaxRows...)

	// proxy: the owned channels' consume logs on the upstream New API.
	var hasOwnership bool
	if err := db.QueryRow(`SELECT to_regclass('proxy_channel_ownership') IS NOT NULL`).Scan(&hasOwnership); err != nil {
		return nil, nil, err
	}
	profileID, _ := strconv.ParseInt(supplierConfigGet(cfgTencentProxyProfileID), 10, 64)
	switch {
	case profileID <= 0:
		warnings = append(warnings, "proxy: 未选择上游 New API 实例，New API Proxy 未出账")
	case !hasOwnership:
		warnings = append(warnings, "proxy: 数据库中没有 proxy_channel_ownership 表，New API Proxy 未出账")
	default:
		proxyRows, proxyWarnings, err := loadTencentProxyRows(profileID, start, end, tencentNow())
		if err != nil {
			return nil, nil, fmt.Errorf("proxy usage: %w", err)
		}
		out = append(out, proxyRows...)
		warnings = append(warnings, proxyWarnings...)
	}
	return out, warnings, nil
}

// loadTencentThirdPartyRows bills every non-claude account on the 账号上号
// portal roster with the portal's own bill of the month (WEB bill/query, RMB),
// up to today; the portal only keeps tencentPortalBillDays of bills.
func loadTencentThirdPartyRows(start, end, now time.Time) ([]tencentSourceRow, []string, error) {
	if !supplierWeb.configured() {
		return nil, []string{"third_party: 号池门户 WEB 登录未配置（SUPPLIER_ACCOUNT_*），账号上号未出账"}, nil
	}
	now = now.In(supplierPortalLoc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, supplierPortalLoc)
	from, to := start.In(supplierPortalLoc), end.AddDate(0, 0, -1).In(supplierPortalLoc)
	if to.After(today) {
		to = today
	}
	if to.Before(from) {
		return nil, nil, nil
	}
	warnings := []string{}
	if earliest := today.AddDate(0, 0, -tencentPortalBillDays); from.Before(earliest) {
		warnings = append(warnings, fmt.Sprintf("third_party: 门户只保留近 %d 天账单，%s 之前的日期没有数据", tencentPortalBillDays, earliest.Format("2006-01-02")))
	}

	roster, err := fetchSupplierPortalAccounts()
	if err != nil {
		return nil, nil, err
	}
	accounts := map[int64]portalAccount{}
	ids := []int64{}
	for _, a := range roster {
		if a.ID <= 0 || strings.EqualFold(strings.TrimSpace(a.Provider), "claude") {
			continue
		}
		if _, ok := accounts[a.ID]; !ok {
			ids = append(ids, a.ID)
		}
		accounts[a.ID] = a
	}
	if len(ids) == 0 {
		return nil, warnings, nil
	}
	bill, err := portalBillQuery(supplierWeb, ids, from, to)
	if err != nil {
		return nil, nil, err
	}
	costs := map[int64]float64{}
	models := map[int64][]string{}
	for _, b := range bill {
		if _, ok := accounts[b.AccountID]; !ok {
			continue
		}
		costs[b.AccountID] += b.Cost
		if b.Model != "" && !slices.Contains(models[b.AccountID], b.Model) {
			models[b.AccountID] = append(models[b.AccountID], b.Model)
		}
	}

	// Name the uploader from the local 上号 table where the account is there.
	type owner struct {
		userID           int64
		username, studio string
	}
	owners := map[int64]owner{}
	rows, err := db.Query(`
		SELECT sa.remote_account_id, sa.uploaded_by, COALESCE(u.username, ''), COALESCE(u.studio, '')
		  FROM rs_supplier_account sa
		  LEFT JOIN rs_auth_user u ON u.id = sa.uploaded_by
		 WHERE sa.remote_account_id = ANY($1)`, pq.Array(ids))
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id int64
		var o owner
		if err := rows.Scan(&id, &o.userID, &o.username, &o.studio); err != nil {
			rows.Close()
			return nil, nil, err
		}
		owners[id] = o
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	out := []tencentSourceRow{}
	unlinked := 0
	for _, id := range ids {
		cost := costs[id]
		if cost <= 0 {
			continue
		}
		a := accounts[id]
		o, linked := owners[id]
		if !linked {
			unlinked++
		}
		ms := models[id]
		if len(ms) == 0 {
			for m := range strings.SplitSeq(a.Model, ",") {
				if m = strings.TrimSpace(m); m != "" {
					ms = append(ms, m)
				}
			}
		}
		slices.Sort(ms)
		out = append(out, tencentSourceRow{
			Platform:  tencentPlatformThirdParty,
			UserID:    o.userID,
			Supplier:  cmp.Or(strings.TrimSpace(o.studio), o.username, strings.TrimSpace(a.SupplierName), "未关联上号人"),
			AccountID: id,
			Vendor:    strings.TrimSpace(a.Provider),
			Models:    ms,
			Label:     tencentAccountLabel(a.AccountAlias, a.APIKey),
			CostCNY:   cost,
		})
	}
	if unlinked > 0 {
		warnings = append(warnings, fmt.Sprintf("third_party: %d 个有账单的门户账号不在本地上号表，供应商名称取门户的供应商名", unlinked))
	}
	return out, warnings, nil
}

// loadTencentCCMaxRows bills each CC Max supplier (column A: the uploader's
// studio, else username) once with the stored daily bills of all its
// accounts in the month (RMB); F says how many accounts ran.
func loadTencentCCMaxRows(start, end time.Time) ([]tencentSourceRow, error) {
	from, to := start.Format("2006-01-02"), end.Format("2006-01-02")
	models := map[int64][]string{}
	rows, err := db.Query(`
		SELECT remote_account_id, model FROM rs_ccmax_daily_bill_model
		 WHERE ds >= $1::date AND ds < $2::date AND model <> '' AND cost_micro > 0
		 GROUP BY 1, 2`, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var model string
		if err := rows.Scan(&id, &model); err != nil {
			rows.Close()
			return nil, err
		}
		models[id] = append(models[id], model)
	}
	rows.Close()

	rows, err = db.Query(`
		SELECT a.remote_account_id, a.uploaded_by, COALESCE(u.username, ''), COALESCE(u.studio, ''), SUM(b.cost_micro)
		  FROM rs_ccmax_daily_bill b
		  JOIN rs_ccmax_account a ON a.remote_account_id = b.remote_account_id
		  LEFT JOIN rs_auth_user u ON u.id = a.uploaded_by
		 WHERE b.ds >= $1::date AND b.ds < $2::date
		 GROUP BY 1, 2, 3, 4
		HAVING SUM(b.cost_micro) > 0
		 ORDER BY 1`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bySupplier := map[string]*tencentSourceRow{}
	accounts := map[string]int{}
	order := []string{}
	for rows.Next() {
		var accountID, userID, micro int64
		var username, studio string
		if err := rows.Scan(&accountID, &userID, &username, &studio, &micro); err != nil {
			return nil, err
		}
		supplier := cmp.Or(strings.TrimSpace(studio), username, fmt.Sprintf("#%d", userID))
		r, ok := bySupplier[supplier]
		if !ok {
			r = &tencentSourceRow{Platform: tencentPlatformCCMax, Vendor: "claude", Supplier: supplier, UserID: userID}
			bySupplier[supplier] = r
			order = append(order, supplier)
		}
		r.CostCNY += float64(micro) / 1e6
		accounts[supplier]++
		for _, m := range models[accountID] {
			if !slices.Contains(r.Models, m) {
				r.Models = append(r.Models, m)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]tencentSourceRow, 0, len(order))
	for _, supplier := range order {
		r := bySupplier[supplier]
		slices.Sort(r.Models)
		if len(r.Models) == 0 {
			r.Models = []string{tencentCCMaxFallbackModel}
		}
		r.Label = fmt.Sprintf("%d 个账号", accounts[supplier])
		out = append(out, *r)
	}
	return out, nil
}

// tencentProxyVendor names column C of a proxy channel from its newapi channel
// type, telling Azure / Vertex channels serving Claude from the others.
func tencentProxyVendor(channelType int, models string) string {
	claude := strings.Contains(strings.ToLower(models), "claude")
	switch channelType {
	case 1:
		return "openai"
	case 3:
		if claude {
			return "azure-claude"
		}
		return "azure-openai"
	case 14:
		return "anthropic"
	case 24:
		return "gemini"
	case 16, 26:
		return "zhipu"
	case 33:
		return "aws-claude"
	case 41:
		if claude {
			return "vertex-claude"
		}
		return "vertex-gemini"
	}
	return fmt.Sprintf("newapi-type-%d", channelType)
}

// loadTencentProxyRows bills each channel in proxy_channel_ownership with the
// consume-log total of the Beijing month (/api/log/stat type=2, up to now) on
// the remote profile of newapi-proxy's upstream, quota ÷ its quota_per_unit
// = USD; name, type and models come from /api/channel/:id. Requests go
// through remoteDoJSON, so the profile's token, user and proxy apply. A
// channel whose request fails is left out with a warning.
func loadTencentProxyRows(profileID int64, start, end, now time.Time) ([]tencentSourceRow, []string, error) {
	host, userID, token, err := loadRemoteProfileByID(profileID)
	if err != nil {
		return nil, []string{fmt.Sprintf("proxy: 远端实例 #%d 不可用（%v），New API Proxy 未出账", profileID, err)}, nil
	}
	type owned struct {
		channelID, userID int64
		supplier          string
	}
	rows, err := db.Query(`
		SELECT o.remote_channel_id, o.user_id, COALESCE(NULLIF(TRIM(u.studio), ''), u.username, '#' || o.user_id::text)
		  FROM proxy_channel_ownership o
		  LEFT JOIN rs_auth_user u ON u.id = o.user_id
		 ORDER BY 1`)
	if err != nil {
		return nil, nil, err
	}
	channels := []owned{}
	for rows.Next() {
		var o owned
		if err := rows.Scan(&o.channelID, &o.userID, &o.supplier); err != nil {
			rows.Close()
			return nil, nil, err
		}
		channels = append(channels, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(channels) == 0 {
		return nil, nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var status struct {
		Data struct {
			QuotaPerUnit float64 `json:"quota_per_unit"`
		} `json:"data"`
	}
	quotaPerUnit := float64(tencentProxyQuotaPerUSD)
	if raw, err := remoteDoRaw(ctx, http.MethodGet, host, "/api/status", token, userID, nil, nil); err == nil &&
		json.Unmarshal(raw, &status) == nil && status.Data.QuotaPerUnit > 0 {
		quotaPerUnit = status.Data.QuotaPerUnit
	}
	from, to := start.Unix(), min(end.Unix(), now.Unix())

	type result struct {
		row tencentSourceRow
		err error
	}
	results := make([]result, len(channels))
	sem := make(chan struct{}, tencentProxyConcurrency)
	var wg sync.WaitGroup
	for i, o := range channels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := tencentSourceRow{Platform: tencentPlatformProxy, UserID: o.userID, Supplier: o.supplier, AccountID: o.channelID}
			var stat struct {
				Quota int64 `json:"quota"`
			}
			q := url.Values{"type": {"2"}, "channel": {strconv.FormatInt(o.channelID, 10)},
				"start_timestamp": {strconv.FormatInt(from, 10)}, "end_timestamp": {strconv.FormatInt(to, 10)}}
			raw, err := remoteDoJSON(ctx, http.MethodGet, host, "/api/log/stat", token, userID, q, nil)
			// Back off and retry when the upstream rate-limits.
			for wait := tencentProxyRetryWait; isRemoteRateLimited(err) && wait <= 8*tencentProxyRetryWait; wait *= 2 {
				select {
				case <-ctx.Done():
				case <-time.After(wait):
				}
				raw, err = remoteDoJSON(ctx, http.MethodGet, host, "/api/log/stat", token, userID, q, nil)
			}
			if err == nil {
				err = json.Unmarshal(raw, &stat)
			}
			if err != nil {
				results[i] = result{err: err}
				return
			}
			r.CostUSD = float64(stat.Quota) / quotaPerUnit
			if stat.Quota <= 0 {
				results[i] = result{row: r}
				return
			}
			var ch struct {
				Name   string `json:"name"`
				Type   int    `json:"type"`
				Models string `json:"models"`
			}
			// A channel deleted upstream keeps its logs; bill it by id.
			if raw, err := remoteDoJSON(ctx, http.MethodGet, host, fmt.Sprintf("/api/channel/%d", o.channelID), token, userID, nil, nil); err == nil {
				_ = json.Unmarshal(raw, &ch)
			}
			r.Vendor = tencentProxyVendor(ch.Type, ch.Models)
			for m := range strings.SplitSeq(ch.Models, ",") {
				if m = strings.TrimSpace(m); m != "" {
					r.Models = append(r.Models, m)
				}
			}
			r.Label = strings.TrimSpace(fmt.Sprintf("%s #%d", ch.Name, o.channelID))
			results[i] = result{row: r}
		}()
	}
	wg.Wait()

	out := []tencentSourceRow{}
	warnings := []string{}
	for i, res := range results {
		switch {
		case res.err != nil:
			warnings = append(warnings, fmt.Sprintf("proxy: 渠道 #%d 取数失败（%v），未出账", channels[i].channelID, res.err))
		case res.row.CostUSD > 0:
			out = append(out, res.row)
		}
	}
	return out, warnings, nil
}

// loadTencentStatementParams reads the month's inputs and fills the defaults:
// the system rate, K = the G rate, I = the 上号 default discount, H = 含票.
func loadTencentStatementParams(month time.Time) (tencentStatementParams, gin.H, error) {
	p := tencentStatementParams{Month: month}
	var fx, ref, discount sql.NullFloat64
	var tax sql.NullString
	err := db.QueryRow(`SELECT fx_rate, ref_fx_rate, discount, tax_rate FROM rs_tencent_month_param WHERE month = $1`,
		month.Format("2006-01")).Scan(&fx, &ref, &discount, &tax)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, nil, err
	}
	from := gin.H{"fx_rate_from": "month", "ref_fx_rate_from": "month", "discount_from": "month", "tax_rate_from": "month"}
	p.FxRate = fx.Float64
	if !fx.Valid || fx.Float64 <= 0 {
		p.FxRate, from["fx_rate_from"] = supplierFxRate(), "system"
	}
	p.RefFxRate = ref.Float64
	if !ref.Valid || ref.Float64 <= 0 {
		p.RefFxRate, from["ref_fx_rate_from"] = p.FxRate, "fx_rate"
	}
	p.Discount = discount.Float64
	if !discount.Valid || discount.Float64 <= 0 {
		pct, _ := strconv.ParseFloat(supplierAccountDiscount(), 64)
		p.Discount, from["discount_from"] = cmp.Or(pct, 100)/100, "upload"
	}
	p.TaxRate = tax.String
	if !tax.Valid {
		p.TaxRate, from["tax_rate_from"] = tencentDefaultTaxRate, "default"
	}
	return p, from, nil
}

func tencentCallerID(c *gin.Context) int64 {
	uidAny, _ := c.Get("user_id")
	uid, _ := uidAny.(int64)
	return uid
}

// tencentLiveStatement builds the Beijing month [start, end) statement from
// the current source data and parameters.
func tencentLiveStatement(start, end time.Time) (tencentStatementParams, gin.H, []tencentLine, []string, error) {
	params, paramFrom, err := loadTencentStatementParams(start)
	if err != nil {
		return params, nil, nil, nil, err
	}
	sources, warnings, err := loadTencentSources(start, end)
	if err != nil {
		return params, nil, nil, nil, err
	}
	lines, _ := buildTencentStatement(sources, params)
	return params, paramFrom, lines, warnings, nil
}

// handleTencentStatement previews a month's statement (default: last month)
// from live source data, optionally for one supplier user or platform.
func handleTencentStatement(c *gin.Context) {
	month := c.Query("month")
	if month == "" {
		now := time.Now().In(billingExportZone)
		month = time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, billingExportZone).Format("2006-01")
	}
	start, end, err := tencentMonthRange(month)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	userID, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	platform := c.Query("platform")
	if platform != "" && !slices.Contains(tencentPlatforms, platform) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown platform"})
		return
	}
	params, paramFrom, lines, warnings, err := tencentLiveStatement(start, end)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	lines = slices.DeleteFunc(lines, func(l tencentLine) bool {
		return (userID > 0 && l.UserID != userID) || (platform != "" && l.Platform != platform)
	})
	c.JSON(http.StatusOK, gin.H{
		"month":            start.Format("2006-01"),
		"bill_month":       int(start.Month()),
		"fx_rate":          params.FxRate,
		"fx_rate_from":     paramFrom["fx_rate_from"],
		"ref_fx_rate":      params.RefFxRate,
		"ref_fx_rate_from": paramFrom["ref_fx_rate_from"],
		"discount":         params.Discount,
		"discount_from":    paramFrom["discount_from"],
		"tax_rate":         params.TaxRate,
		"tax_rate_from":    paramFrom["tax_rate_from"],
		"headers":          tencentStatementHeaders,
		"summary_headers":  tencentSummaryHeaders,
		"lines":            lines,
		"summary":          tencentSummarize(lines),
		"warnings":         warnings,
	})
}

// ---- Month parameters ----

func handleTencentMonthParamList(c *gin.Context) {
	rows, err := db.Query(`SELECT month, fx_rate, ref_fx_rate, discount, tax_rate, note, updated_at FROM rs_tencent_month_param ORDER BY month DESC`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	type item struct {
		Month     string   `json:"month"`
		FxRate    *float64 `json:"fx_rate"`
		RefFxRate *float64 `json:"ref_fx_rate"`
		Discount  *float64 `json:"discount"`
		TaxRate   *string  `json:"tax_rate"`
		Note      string   `json:"note"`
		UpdatedAt int64    `json:"updated_at"`
	}
	list := []item{}
	for rows.Next() {
		var it item
		var fx, ref, discount sql.NullFloat64
		var tax sql.NullString
		if err := rows.Scan(&it.Month, &fx, &ref, &discount, &tax, &it.Note, &it.UpdatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if fx.Valid {
			it.FxRate = &fx.Float64
		}
		if ref.Valid {
			it.RefFxRate = &ref.Float64
		}
		if discount.Valid {
			it.Discount = &discount.Float64
		}
		if tax.Valid {
			it.TaxRate = &tax.String
		}
		list = append(list, it)
	}
	upload, _ := strconv.ParseFloat(supplierAccountDiscount(), 64)
	c.JSON(http.StatusOK, gin.H{
		"list":             list,
		"system_fx_rate":   supplierFxRate(),
		"default_discount": cmp.Or(upload, 100) / 100,
		"default_tax_rate": tencentDefaultTaxRate,
	})
}

// handleTencentMonthParamSet upserts a month's inputs; null clears one back to
// its default.
func handleTencentMonthParamSet(c *gin.Context) {
	var body struct {
		Month     string   `json:"month"`
		FxRate    *float64 `json:"fx_rate"`
		RefFxRate *float64 `json:"ref_fx_rate"`
		Discount  *float64 `json:"discount"`
		TaxRate   *string  `json:"tax_rate"`
		Note      string   `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	start, _, err := tencentMonthRange(body.Month)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	for _, rate := range []*float64{body.FxRate, body.RefFxRate} {
		if rate != nil && (*rate <= 0 || *rate > 100) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "汇率须在 0–100 之间"})
			return
		}
	}
	if body.Discount != nil && (*body.Discount <= 0 || *body.Discount > 1) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "折扣须在 0–1 之间（如 0.88）"})
		return
	}
	if body.TaxRate != nil {
		tax := strings.TrimSpace(*body.TaxRate)
		body.TaxRate = &tax
	}
	if _, err := db.Exec(`
		INSERT INTO rs_tencent_month_param (month, fx_rate, ref_fx_rate, discount, tax_rate, note, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (month) DO UPDATE SET fx_rate=EXCLUDED.fx_rate, ref_fx_rate=EXCLUDED.ref_fx_rate,
		  discount=EXCLUDED.discount, tax_rate=EXCLUDED.tax_rate,
		  note=EXCLUDED.note, updated_at=EXCLUDED.updated_at, updated_by=EXCLUDED.updated_by`,
		start.Format("2006-01"), body.FxRate, body.RefFxRate, body.Discount, body.TaxRate,
		strings.TrimSpace(body.Note), time.Now().Unix(), tencentCallerID(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- Settings ----

func handleTencentSettingsGet(c *gin.Context) {
	profileID, _ := strconv.ParseInt(supplierConfigGet(cfgTencentProxyProfileID), 10, 64)
	upload, _ := strconv.ParseFloat(supplierAccountDiscount(), 64)
	c.JSON(http.StatusOK, gin.H{
		"proxy_profile_id":    profileID,
		"system_fx_rate":      supplierFxRate(),
		"upload_discount_pct": upload,
	})
}

// handleTencentSettingsSet picks the remote profile that is newapi-proxy's
// upstream New API (0 = none, proxy usage is not billed). The profile must
// use the admin API: the seller API has no /api/log.
func handleTencentSettingsSet(c *gin.Context) {
	var body struct {
		ProxyProfileID *int64 `json:"proxy_profile_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if body.ProxyProfileID != nil {
		id := max(*body.ProxyProfileID, 0)
		if id > 0 {
			var apiMode string
			err := db.QueryRow(`SELECT api_mode FROM remote_newapi_profile WHERE id = $1`, id).Scan(&apiMode)
			if errors.Is(err, sql.ErrNoRows) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "remote profile not found"})
				return
			}
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if apiMode == remoteAPIModeSeller {
				c.JSON(http.StatusBadRequest, gin.H{"error": "该实例是 seller 模式，没有日志接口，不能用于 proxy 出账"})
				return
			}
		}
		if err := supplierConfigSet(cfgTencentProxyProfileID, strconv.FormatInt(id, 10)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	handleTencentSettingsGet(c)
}

// registerTencentRoutes mounts /tencent/* on the authenticated api group,
// super admin only (the X-API-Key service caller counts as super admin).
func registerTencentRoutes(api *gin.RouterGroup) {
	g := api.Group("/tencent", requireRole(minSuperAdminRole))
	g.GET("/statement", handleTencentStatement)
	g.GET("/params/months", handleTencentMonthParamList)
	g.PUT("/params/months", handleTencentMonthParamSet)
	g.GET("/settings", handleTencentSettingsGet)
	g.PUT("/settings", handleTencentSettingsSet)
	g.POST("/statement-push", handleTencentStatementPush)
	g.GET("/push-logs", handleTencentPushLogs)
	g.GET("/push-settings", handleTencentPushSettingsGet)
	g.PUT("/push-settings", handleTencentPushSettingsSet)
	g.GET("/users", handleTencentUserList)
	g.POST("/users", handleTencentUserCreate)
	g.GET("/statements", handleTencentStatementList)
	g.GET("/statements/:month", handleTencentStatementGet)
	g.POST("/statements/:month/generate", handleTencentStatementWrite(false))
	g.POST("/statements/:month/regenerate", handleTencentStatementWrite(true))
	g.POST("/statements/:month/confirm", handleTencentStatementConfirm)
	g.GET("/statements/:month/export", handleTencentStatementExport)
}
