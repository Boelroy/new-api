package main

// 腾讯一站 · 月对账: the monthly statement in the layout of the customer
// template's 「API账单」 (columns A–N) and 「汇总表」 sheets, built from the
// three platforms report-service already mirrors:
//
//   - third_party: rs_supplier_record_log_hourly (portal quota, CNY) per
//     account × model, joined to rs_supplier_account by platform account id;
//   - ccmax: rs_ccmax_daily_bill (CNY), split per model through
//     rs_ccmax_daily_bill_model where the portal bill carried the model;
//   - proxy: proxy_channel_ownership × the upstream newapi's channel
//     used_quota growth (remote_channel_snapshot of the configured remote
//     profile, the same source as /api/billing/remote-tag-usage). Snapshots
//     carry no model, so a proxy channel is billed as one row; its quota is
//     USD already.
//
// Months are Beijing calendar months. Every business rule that is not settled
// yet (FX rates, discount, tax rate, RMB-priced rows, bill names, vendor
// naming, proxy upstream) is a setting under /api/tencent/*, super admin only.

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	tencentPlatformThirdParty = "third_party"
	tencentPlatformCCMax      = "ccmax"
	tencentPlatformProxy      = "proxy"
	// tencentPlatformAll keys a supplier parameter that applies to every
	// platform of the supplier unless a platform row overrides it.
	tencentPlatformAll = "*"

	// tencentCCMaxFallbackModel names CC Max cost whose portal bill carried no
	// model (days pulled before per-model storage, or a portal without it).
	tencentCCMaxFallbackModel = "Claude（CC Max 订阅）"

	cfgTencentProxyProfileID = "tencent_proxy_profile_id"
	cfgTencentVendorMap      = "tencent_vendor_map"

	// tencentProxyQuotaPerUSD is newapi's quota unit (500,000 quota = 1 USD).
	tencentProxyQuotaPerUSD = 500000
)

var tencentPlatforms = []string{tencentPlatformThirdParty, tencentPlatformCCMax, tencentPlatformProxy}

// tencentSourcePrefix is the template's column N prefix per platform; the
// summary sheet's SUMIFS match on it.
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
	// Per-month FX rates. fx_rate is the CNY÷rate divisor for column G (NULL =
	// the system supplier_fx_rate at statement time); ref_fx_rate is column K,
	// the BOC USD rate entered by hand (NULL = the column G rate).
	`CREATE TABLE IF NOT EXISTS rs_tencent_month_param (
		month       TEXT PRIMARY KEY,
		fx_rate     DOUBLE PRECISION,
		ref_fx_rate DOUBLE PRECISION,
		note        TEXT   NOT NULL DEFAULT '',
		updated_at  BIGINT NOT NULL,
		updated_by  BIGINT NOT NULL DEFAULT 0
	)`,
	// Per supplier (rs_auth_user) × platform discount (percent, NULL = fall
	// through) and tax rate (column H, free text as in the template).
	// platform '*' applies to all platforms of the supplier.
	`CREATE TABLE IF NOT EXISTS rs_tencent_supplier_param (
		user_id      BIGINT NOT NULL,
		platform     TEXT   NOT NULL,
		discount_pct DOUBLE PRECISION,
		tax_rate     TEXT   NOT NULL DEFAULT '',
		updated_at   BIGINT NOT NULL,
		updated_by   BIGINT NOT NULL DEFAULT 0,
		PRIMARY KEY (user_id, platform)
	)`,
	// Per statement row overrides, keyed by tencentLineKey: RMB-priced rows
	// (L = J) and column M, the official bill name.
	`CREATE TABLE IF NOT EXISTS rs_tencent_line_flag (
		line_key   TEXT PRIMARY KEY,
		rmb_priced BOOLEAN NOT NULL DEFAULT FALSE,
		bill_name  TEXT    NOT NULL DEFAULT '',
		updated_at BIGINT  NOT NULL,
		updated_by BIGINT  NOT NULL DEFAULT 0
	)`,
	// Month-end snapshots (tencent_statement_export.go). A month is a draft
	// until confirmed; a confirmed month no longer follows the source data.
	// params holds the supplier parameters, line flags and vendor rules the
	// lines were built with (JSON); the rates are columns.
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

// tencentSourceRow is one platform's month usage of one account (proxy: one
// channel) and model, before any statement parameter is applied.
type tencentSourceRow struct {
	Platform  string
	UserID    int64
	Username  string
	Studio    string
	AccountID int64
	KeyLast8  string
	Provider  string
	Model     string
	CostCNY   float64
	CostUSD   float64
	// UploadDiscountPct is the discount the account was uploaded with, the
	// fallback when no supplier parameter sets one.
	UploadDiscountPct *float64
}

type tencentSupplierParamKey struct {
	UserID   int64
	Platform string
}

type tencentSupplierParam struct {
	DiscountPct *float64
	TaxRate     string
}

type tencentLineFlag struct {
	RMBPriced bool
	BillName  string
}

type tencentStatementParams struct {
	Month     time.Time
	FxRate    float64
	RefFxRate float64
	Suppliers map[tencentSupplierParamKey]tencentSupplierParam
	Flags     map[string]tencentLineFlag
	VendorMap map[string]string
}

// tencentLine is one 「API账单」 row; the json names of A–N follow the
// template column order.
type tencentLine struct {
	LineKey      string  `json:"line_key"`
	Platform     string  `json:"platform"`
	UserID       int64   `json:"user_id"`
	Supplier     string  `json:"supplier"`     // A
	BillMonth    int     `json:"bill_month"`   // B
	Vendor       string  `json:"vendor"`       // C
	Model        string  `json:"model"`        // D
	AccountID    string  `json:"account_id"`   // E
	Key          string  `json:"key"`          // F
	OfficialUSD  float64 `json:"official_usd"` // G
	TaxRate      string  `json:"tax_rate"`     // H
	Discount     float64 `json:"discount"`     // I, a fraction (0.88)
	TencentUSD   float64 `json:"tencent_usd"`  // J
	RefFxRate    float64 `json:"ref_fx_rate"`  // K
	InvoiceRMB   float64 `json:"invoice_rmb"`  // L
	BillName     string  `json:"bill_name"`    // M
	Source       string  `json:"source"`       // N
	CostCNY      float64 `json:"cost_cny"`
	CostUSD      float64 `json:"cost_usd"`
	RMBPriced    bool    `json:"rmb_priced"`
	DiscountFrom string  `json:"discount_from"`
}

// tencentSummaryRow is one 「汇总表」 row: G and L summed per supplier ×
// month × vendor × source platform.
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

// tencentLineKey identifies a statement row across rebuilds, for its flags.
func tencentLineKey(month time.Time, platform string, accountID int64, model string) string {
	return fmt.Sprintf("%s|%s|%d|%s", month.Format("2006-01"), platform, accountID, model)
}

// tencentMonthRange parses a YYYY-MM month into its Beijing [start, end).
func tencentMonthRange(month string) (time.Time, time.Time, error) {
	start, err := time.ParseInLocation("2006-01", strings.TrimSpace(month), billingExportZone)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("month 须为 YYYY-MM")
	}
	return start, start.AddDate(0, 1, 0), nil
}

// tencentDefaultVendorRules map a lowercase model / provider substring to the
// statement's vendor name (column C). Checked in order, model first.
var tencentDefaultVendorRules = []struct{ match, vendor string }{
	{"claude", "Anthropic"},
	{"anthropic", "Anthropic"},
	{"gpt", "OpenAI"},
	{"openai", "OpenAI"},
	{"chatgpt", "OpenAI"},
	{"codex", "OpenAI"},
	{"dall-e", "OpenAI"},
	{"whisper", "OpenAI"},
	{"tts-", "OpenAI"},
	{"text-embedding", "OpenAI"},
	{"sora", "OpenAI"},
	{"azure", "OpenAI"},
	{"gemini", "Google"},
	{"imagen", "Google"},
	{"veo", "Google"},
	{"vertex", "Google"},
	{"google", "Google"},
	{"deepseek", "DeepSeek"},
	{"grok", "xAI"},
	{"xai", "xAI"},
	{"qwen", "Alibaba"},
	{"qwq", "Alibaba"},
	{"dashscope", "Alibaba"},
	{"glm", "Zhipu"},
	{"zhipu", "Zhipu"},
	{"kimi", "Moonshot"},
	{"moonshot", "Moonshot"},
	{"doubao", "ByteDance"},
	{"volc", "ByteDance"},
	{"llama", "Meta"},
	{"mistral", "Mistral"},
	{"minimax", "MiniMax"},
	{"bedrock", "Anthropic"},
	{"aws", "Anthropic"},
}

// tencentVendor names the model vendor of a row. Custom rules (lowercase
// substring → vendor, longest first) win over the defaults; OpenAI's
// o-series ids have no distinctive substring and are matched by prefix.
func tencentVendor(model, provider string, custom map[string]string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	provider = strings.ToLower(strings.TrimSpace(provider))
	keys := slices.Collect(maps.Keys(custom))
	slices.SortFunc(keys, func(a, b string) int {
		return cmp.Or(cmp.Compare(len(b), len(a)), cmp.Compare(a, b))
	})
	for _, subject := range []string{model, provider} {
		if subject == "" {
			continue
		}
		for _, k := range keys {
			if k != "" && strings.Contains(subject, strings.ToLower(k)) {
				return custom[k]
			}
		}
		for _, r := range tencentDefaultVendorRules {
			if strings.Contains(subject, r.match) {
				return r.vendor
			}
		}
		if len(subject) >= 2 && subject[0] == 'o' && subject[1] >= '1' && subject[1] <= '9' {
			return "OpenAI"
		}
	}
	if provider != "" {
		return provider
	}
	return "未知"
}

// buildTencentStatement applies the month parameters to the source rows and
// returns the 「API账单」 rows and the 「汇总表」 rows. Rows with the same line
// key are merged first.
func buildTencentStatement(rows []tencentSourceRow, p tencentStatementParams) ([]tencentLine, []tencentSummaryRow) {
	merged := map[string]*tencentSourceRow{}
	order := []string{}
	for _, r := range rows {
		model := r.Model
		if r.Platform == tencentPlatformCCMax && strings.TrimSpace(model) == "" {
			model = tencentCCMaxFallbackModel
		}
		key := tencentLineKey(p.Month, r.Platform, r.AccountID, model)
		if m, ok := merged[key]; ok {
			m.CostCNY += r.CostCNY
			m.CostUSD += r.CostUSD
			continue
		}
		r.Model = model
		merged[key] = &r
		order = append(order, key)
	}

	lines := make([]tencentLine, 0, len(order))
	for _, key := range order {
		r := merged[key]
		flag := p.Flags[key]
		platformParam, hasPlatform := p.Suppliers[tencentSupplierParamKey{r.UserID, r.Platform}]
		allParam, hasAll := p.Suppliers[tencentSupplierParamKey{r.UserID, tencentPlatformAll}]

		discountPct, discountFrom := 100.0, "default"
		switch {
		case hasPlatform && platformParam.DiscountPct != nil:
			discountPct, discountFrom = *platformParam.DiscountPct, "supplier_platform"
		case hasAll && allParam.DiscountPct != nil:
			discountPct, discountFrom = *allParam.DiscountPct, "supplier"
		case r.UploadDiscountPct != nil:
			discountPct, discountFrom = *r.UploadDiscountPct, "upload"
		}
		taxRate := allParam.TaxRate
		if hasPlatform && platformParam.TaxRate != "" {
			taxRate = platformParam.TaxRate
		}

		// G is what the platform cost in the row's billing currency: USD
		// (CNY ÷ the month rate) normally, CNY for a row priced in RMB.
		official := r.CostUSD + r.CostCNY/p.FxRate
		if flag.RMBPriced {
			official = r.CostCNY + r.CostUSD*p.FxRate
		}
		g := tencentRound(official, 2)
		j := tencentRound(g*discountPct/100, 2)
		l := tencentRound(j*p.RefFxRate, 2)
		if flag.RMBPriced {
			l = j
		}

		keyCol := ""
		if r.Platform == tencentPlatformThirdParty && r.KeyLast8 != "" {
			keyCol = "****" + r.KeyLast8
		}
		lines = append(lines, tencentLine{
			LineKey:      key,
			Platform:     r.Platform,
			UserID:       r.UserID,
			Supplier:     cmp.Or(strings.TrimSpace(r.Studio), r.Username),
			BillMonth:    int(p.Month.Month()),
			Vendor:       tencentVendor(r.Model, r.Provider, p.VendorMap),
			Model:        r.Model,
			AccountID:    strconv.FormatInt(r.AccountID, 10),
			Key:          keyCol,
			OfficialUSD:  g,
			TaxRate:      taxRate,
			Discount:     discountPct / 100,
			TencentUSD:   j,
			RefFxRate:    p.RefFxRate,
			InvoiceRMB:   l,
			BillName:     flag.BillName,
			Source:       tencentSourcePrefix[r.Platform] + " - " + r.Username,
			CostCNY:      r.CostCNY,
			CostUSD:      r.CostUSD,
			RMBPriced:    flag.RMBPriced,
			DiscountFrom: discountFrom,
		})
	}
	slices.SortStableFunc(lines, func(a, b tencentLine) int {
		return cmp.Or(
			cmp.Compare(a.Supplier, b.Supplier),
			cmp.Compare(slices.Index(tencentPlatforms, a.Platform), slices.Index(tencentPlatforms, b.Platform)),
			cmp.Compare(a.Vendor, b.Vendor),
			cmp.Compare(a.AccountID, b.AccountID),
			cmp.Compare(a.Model, b.Model),
		)
	})

	return lines, tencentSummarize(lines)
}

// tencentSummarize builds the 「汇总表」 rows of statement lines: G and L
// summed per supplier × month × vendor × source platform, the grouping the
// exported SUMIFS formulas reproduce.
func tencentSummarize(lines []tencentLine) []tencentSummaryRow {
	type summaryKey struct {
		supplier string
		month    int
		vendor   string
		source   string
	}
	sums := map[summaryKey]*tencentSummaryRow{}
	for _, l := range lines {
		k := summaryKey{l.Supplier, l.BillMonth, l.Vendor, tencentSourcePrefix[l.Platform]}
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
			cmp.Compare(a.Vendor, b.Vendor), cmp.Compare(a.Source, b.Source))
	})
	return summary
}

// loadTencentSources reads every platform's usage in the Beijing month
// [start, end). Warnings name usage that could not be billed.
func loadTencentSources(start, end time.Time) ([]tencentSourceRow, []string, error) {
	out := []tencentSourceRow{}
	warnings := []string{}

	// third_party: the hourly portal log, cost = quota / 1e6 CNY. Log rows
	// carry the platform account id (a1_id on 1.0, a2_id on 2.0).
	uploadDiscount, _ := strconv.ParseFloat(supplierAccountDiscount(), 64)
	rows, err := db.Query(`
		SELECT sa.uploaded_by, COALESCE(u.username, ''), COALESCE(u.studio, ''), sa.remote_account_id,
		       sa.key_last8, sa.provider, sa.account_type, h.model, SUM(h.cost)
		  FROM rs_supplier_record_log_hourly h
		  JOIN rs_supplier_account sa
		    ON h.account_id <> 0
		   AND h.account_id = CASE h.platform WHEN '1.0' THEN sa.a1_id WHEN '2.0' THEN sa.a2_id END
		  LEFT JOIN rs_auth_user u ON u.id = sa.uploaded_by
		 WHERE h.hour >= $1 AND h.hour < $2
		 GROUP BY 1, 2, 3, 4, 5, 6, 7, 8
		HAVING SUM(h.cost) > 0`, start.Unix(), end.Unix())
	if err != nil {
		return nil, nil, fmt.Errorf("third_party usage: %w", err)
	}
	for rows.Next() {
		r := tencentSourceRow{Platform: tencentPlatformThirdParty}
		var accountType int
		var quota float64
		if err := rows.Scan(&r.UserID, &r.Username, &r.Studio, &r.AccountID, &r.KeyLast8, &r.Provider, &accountType, &r.Model, &quota); err != nil {
			rows.Close()
			return nil, nil, err
		}
		r.CostCNY = quota / supplierRecordLogQuotaUnit
		// 普通号 are uploaded at the admin default discount; 速刷号 carry none.
		if accountType == 0 && uploadDiscount > 0 {
			r.UploadDiscountPct = &uploadDiscount
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var orphanQuota float64
	if err := db.QueryRow(`
		SELECT COALESCE(SUM(h.cost), 0) FROM rs_supplier_record_log_hourly h
		 WHERE h.hour >= $1 AND h.hour < $2
		   AND NOT EXISTS (SELECT 1 FROM rs_supplier_account sa WHERE h.account_id <> 0
		         AND h.account_id = CASE h.platform WHEN '1.0' THEN sa.a1_id WHEN '2.0' THEN sa.a2_id END)`,
		start.Unix(), end.Unix()).Scan(&orphanQuota); err != nil {
		return nil, nil, err
	}
	if orphanQuota > 0 {
		warnings = append(warnings, fmt.Sprintf("third_party: ¥%.2f 用量的门户账号不在本地上号表，未出账", orphanQuota/supplierRecordLogQuotaUnit))
	}

	// ccmax: per-model bill rows where stored, else the day's total.
	rows, err = db.Query(`
		SELECT a.uploaded_by, COALESCE(u.username, ''), COALESCE(u.studio, ''), a.remote_account_id,
		       COALESCE(m.model, ''), SUM(COALESCE(m.cost_micro, b.cost_micro))
		  FROM rs_ccmax_daily_bill b
		  JOIN rs_ccmax_account a ON a.remote_account_id = b.remote_account_id
		  LEFT JOIN rs_auth_user u ON u.id = a.uploaded_by
		  LEFT JOIN rs_ccmax_daily_bill_model m ON m.remote_account_id = b.remote_account_id AND m.ds = b.ds
		 WHERE b.ds >= $1::date AND b.ds < $2::date
		 GROUP BY 1, 2, 3, 4, 5
		HAVING SUM(COALESCE(m.cost_micro, b.cost_micro)) > 0`,
		start.Format("2006-01-02"), end.Format("2006-01-02"))
	if err != nil {
		return nil, nil, fmt.Errorf("ccmax usage: %w", err)
	}
	for rows.Next() {
		r := tencentSourceRow{Platform: tencentPlatformCCMax, Provider: "claude"}
		var micro int64
		if err := rows.Scan(&r.UserID, &r.Username, &r.Studio, &r.AccountID, &r.Model, &micro); err != nil {
			rows.Close()
			return nil, nil, err
		}
		r.CostCNY = float64(micro) / 1e6
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// proxy: owned channels' used_quota growth over the month on the
	// configured upstream profile.
	profileID, _ := strconv.ParseInt(supplierConfigGet(cfgTencentProxyProfileID), 10, 64)
	var hasOwnership bool
	if err := db.QueryRow(`SELECT to_regclass('proxy_channel_ownership') IS NOT NULL`).Scan(&hasOwnership); err != nil {
		return nil, nil, err
	}
	switch {
	case profileID <= 0:
		warnings = append(warnings, "proxy: 未配置上游实例（tencent_proxy_profile_id），New API Proxy 未出账")
	case !hasOwnership:
		warnings = append(warnings, "proxy: 数据库中没有 proxy_channel_ownership 表，New API Proxy 未出账")
	default:
		proxyRows, proxyWarnings, err := loadTencentProxyRows(profileID, start, end)
		if err != nil {
			return nil, nil, fmt.Errorf("proxy usage: %w", err)
		}
		out = append(out, proxyRows...)
		warnings = append(warnings, proxyWarnings...)
	}
	return out, warnings, nil
}

// loadTencentProxyRows bills each owned channel's used_quota growth between
// the last capture at or before the month start and the last capture by the
// month end. Without a capture before the month (retention, new profile) the
// first capture inside the month is the baseline and the month is partial.
func loadTencentProxyRows(profileID int64, start, end time.Time) ([]tencentSourceRow, []string, error) {
	var capPrev, capFirst, capTo sql.NullInt64
	if err := db.QueryRow(`
		SELECT (SELECT MAX(captured_at) FROM remote_channel_snapshot WHERE profile_id = $1 AND captured_at <= $2),
		       (SELECT MIN(captured_at) FROM remote_channel_snapshot WHERE profile_id = $1 AND captured_at > $2 AND captured_at <= $3),
		       (SELECT MAX(captured_at) FROM remote_channel_snapshot WHERE profile_id = $1 AND captured_at <= $3)`,
		profileID, start.Unix(), end.Unix()).Scan(&capPrev, &capFirst, &capTo); err != nil {
		return nil, nil, err
	}
	warnings := []string{}
	capFrom := capPrev
	if !capPrev.Valid {
		capFrom = capFirst
		if capFirst.Valid {
			warnings = append(warnings, "proxy: 月初之前没有渠道快照，以本月第一次快照为起点，用量可能偏少")
		}
	}
	if !capFrom.Valid || !capTo.Valid || capTo.Int64 <= capFrom.Int64 {
		return nil, append(warnings, "proxy: 本月没有可用的渠道快照"), nil
	}
	rows, err := db.Query(`
		SELECT o.user_id, COALESCE(u.username, ''), COALESCE(u.studio, ''), o.remote_channel_id,
		       COALESCE(cc.name, ''), COALESCE(cc.models, ''),
		       GREATEST(t.used_quota - COALESCE(f.used_quota, 0), 0)
		  FROM proxy_channel_ownership o
		  JOIN remote_channel_snapshot t
		    ON t.profile_id = $1 AND t.captured_at = $3 AND t.remote_channel_id = o.remote_channel_id
		  LEFT JOIN remote_channel_snapshot f
		    ON f.profile_id = $1 AND f.captured_at = $2 AND f.remote_channel_id = o.remote_channel_id
		  LEFT JOIN remote_channel_current cc ON cc.profile_id = $1 AND cc.remote_channel_id = o.remote_channel_id
		  LEFT JOIN rs_auth_user u ON u.id = o.user_id
		 WHERE t.used_quota > COALESCE(f.used_quota, 0)`,
		profileID, capFrom.Int64, capTo.Int64)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := []tencentSourceRow{}
	for rows.Next() {
		r := tencentSourceRow{Platform: tencentPlatformProxy}
		var name, models string
		var quota int64
		if err := rows.Scan(&r.UserID, &r.Username, &r.Studio, &r.AccountID, &name, &models, &quota); err != nil {
			return nil, nil, err
		}
		// One row per channel: its model when it serves one, else its name.
		models = strings.TrimSpace(models)
		r.Model = cmp.Or(name, fmt.Sprintf("channel #%d", r.AccountID))
		if models != "" && !strings.Contains(models, ",") {
			r.Model = models
		}
		r.Provider = models
		r.CostUSD = float64(quota) / tencentProxyQuotaPerUSD
		out = append(out, r)
	}
	return out, warnings, rows.Err()
}

// loadTencentStatementParams reads the month's rates and every supplier
// parameter, line flag and vendor rule.
func loadTencentStatementParams(month time.Time) (tencentStatementParams, gin.H, error) {
	p := tencentStatementParams{
		Month:     month,
		Suppliers: map[tencentSupplierParamKey]tencentSupplierParam{},
		Flags:     map[string]tencentLineFlag{},
		VendorMap: tencentVendorMap(),
	}
	var fx, ref sql.NullFloat64
	err := db.QueryRow(`SELECT fx_rate, ref_fx_rate FROM rs_tencent_month_param WHERE month = $1`, month.Format("2006-01")).Scan(&fx, &ref)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, nil, err
	}
	fxFrom, refFrom := "month", "month"
	p.FxRate = fx.Float64
	if !fx.Valid || fx.Float64 <= 0 {
		p.FxRate, fxFrom = supplierFxRate(), "system"
	}
	p.RefFxRate = ref.Float64
	if !ref.Valid || ref.Float64 <= 0 {
		p.RefFxRate, refFrom = p.FxRate, "fx_rate"
	}

	rows, err := db.Query(`SELECT user_id, platform, discount_pct, tax_rate FROM rs_tencent_supplier_param`)
	if err != nil {
		return p, nil, err
	}
	for rows.Next() {
		var k tencentSupplierParamKey
		var discount sql.NullFloat64
		var sp tencentSupplierParam
		if err := rows.Scan(&k.UserID, &k.Platform, &discount, &sp.TaxRate); err != nil {
			rows.Close()
			return p, nil, err
		}
		if discount.Valid {
			sp.DiscountPct = &discount.Float64
		}
		p.Suppliers[k] = sp
	}
	rows.Close()

	rows, err = db.Query(`SELECT line_key, rmb_priced, bill_name FROM rs_tencent_line_flag WHERE line_key LIKE $1`, month.Format("2006-01")+"|%")
	if err != nil {
		return p, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var f tencentLineFlag
		if err := rows.Scan(&key, &f.RMBPriced, &f.BillName); err != nil {
			return p, nil, err
		}
		p.Flags[key] = f
	}
	return p, gin.H{"fx_rate_from": fxFrom, "ref_fx_rate_from": refFrom}, rows.Err()
}

func tencentVendorMap() map[string]string {
	out := map[string]string{}
	if raw := supplierConfigGet(cfgTencentVendorMap); raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	lines = slices.DeleteFunc(lines, func(l tencentLine) bool {
		return (userID > 0 && l.UserID != userID) || (platform != "" && l.Platform != platform)
	})
	summary := tencentSummarize(lines)
	c.JSON(http.StatusOK, gin.H{
		"month":            start.Format("2006-01"),
		"bill_month":       int(start.Month()),
		"fx_rate":          params.FxRate,
		"fx_rate_from":     paramFrom["fx_rate_from"],
		"ref_fx_rate":      params.RefFxRate,
		"ref_fx_rate_from": paramFrom["ref_fx_rate_from"],
		"headers":          tencentStatementHeaders,
		"summary_headers":  tencentSummaryHeaders,
		"lines":            lines,
		"summary":          summary,
		"warnings":         warnings,
	})
}

// ---- Month parameters ----

func handleTencentMonthParamList(c *gin.Context) {
	rows, err := db.Query(`SELECT month, fx_rate, ref_fx_rate, note, updated_at FROM rs_tencent_month_param ORDER BY month DESC`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	type item struct {
		Month     string   `json:"month"`
		FxRate    *float64 `json:"fx_rate"`
		RefFxRate *float64 `json:"ref_fx_rate"`
		Note      string   `json:"note"`
		UpdatedAt int64    `json:"updated_at"`
	}
	list := []item{}
	for rows.Next() {
		var it item
		var fx, ref sql.NullFloat64
		if err := rows.Scan(&it.Month, &fx, &ref, &it.Note, &it.UpdatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if fx.Valid {
			it.FxRate = &fx.Float64
		}
		if ref.Valid {
			it.RefFxRate = &ref.Float64
		}
		list = append(list, it)
	}
	c.JSON(http.StatusOK, gin.H{"list": list, "system_fx_rate": supplierFxRate()})
}

// handleTencentMonthParamSet upserts a month's rates; null clears a rate back
// to its default.
func handleTencentMonthParamSet(c *gin.Context) {
	var body struct {
		Month     string   `json:"month"`
		FxRate    *float64 `json:"fx_rate"`
		RefFxRate *float64 `json:"ref_fx_rate"`
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
	if _, err := db.Exec(`
		INSERT INTO rs_tencent_month_param (month, fx_rate, ref_fx_rate, note, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (month) DO UPDATE SET fx_rate=EXCLUDED.fx_rate, ref_fx_rate=EXCLUDED.ref_fx_rate,
		  note=EXCLUDED.note, updated_at=EXCLUDED.updated_at, updated_by=EXCLUDED.updated_by`,
		start.Format("2006-01"), body.FxRate, body.RefFxRate, strings.TrimSpace(body.Note), time.Now().Unix(), tencentCallerID(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- Supplier × platform parameters ----

func handleTencentSupplierParamList(c *gin.Context) {
	rows, err := db.Query(`
		SELECT p.user_id, COALESCE(u.username, ''), COALESCE(u.studio, ''), p.platform, p.discount_pct, p.tax_rate, p.updated_at
		  FROM rs_tencent_supplier_param p
		  LEFT JOIN rs_auth_user u ON u.id = p.user_id
		 ORDER BY 2, p.platform`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	type item struct {
		UserID      int64    `json:"user_id"`
		Username    string   `json:"username"`
		Studio      string   `json:"studio"`
		Platform    string   `json:"platform"`
		DiscountPct *float64 `json:"discount_pct"`
		TaxRate     string   `json:"tax_rate"`
		UpdatedAt   int64    `json:"updated_at"`
	}
	list := []item{}
	for rows.Next() {
		var it item
		var discount sql.NullFloat64
		if err := rows.Scan(&it.UserID, &it.Username, &it.Studio, &it.Platform, &discount, &it.TaxRate, &it.UpdatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if discount.Valid {
			it.DiscountPct = &discount.Float64
		}
		list = append(list, it)
	}
	upload, _ := strconv.ParseFloat(supplierAccountDiscount(), 64)
	c.JSON(http.StatusOK, gin.H{"list": list, "upload_discount_pct": upload, "platforms": append(slices.Clone(tencentPlatforms), tencentPlatformAll)})
}

func handleTencentSupplierParamSet(c *gin.Context) {
	var body struct {
		UserID      int64    `json:"user_id"`
		Platform    string   `json:"platform"`
		DiscountPct *float64 `json:"discount_pct"`
		TaxRate     string   `json:"tax_rate"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if body.Platform != tencentPlatformAll && !slices.Contains(tencentPlatforms, body.Platform) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown platform"})
		return
	}
	if body.DiscountPct != nil && (*body.DiscountPct <= 0 || *body.DiscountPct > 100) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "折扣须在 0–100 之间（百分比）"})
		return
	}
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM rs_auth_user WHERE id = $1)`, body.UserID).Scan(&exists); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user not found"})
		return
	}
	if _, err := db.Exec(`
		INSERT INTO rs_tencent_supplier_param (user_id, platform, discount_pct, tax_rate, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id, platform) DO UPDATE SET discount_pct=EXCLUDED.discount_pct, tax_rate=EXCLUDED.tax_rate,
		  updated_at=EXCLUDED.updated_at, updated_by=EXCLUDED.updated_by`,
		body.UserID, body.Platform, body.DiscountPct, strings.TrimSpace(body.TaxRate), time.Now().Unix(), tencentCallerID(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func handleTencentSupplierParamDelete(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user_id"})
		return
	}
	if _, err := db.Exec(`DELETE FROM rs_tencent_supplier_param WHERE user_id = $1 AND platform = $2`, userID, c.Param("platform")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- Line flags (RMB-priced rows, column M) ----

func handleTencentLineFlagList(c *gin.Context) {
	start, _, err := tencentMonthRange(c.Query("month"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rows, err := db.Query(`SELECT line_key, rmb_priced, bill_name, updated_at FROM rs_tencent_line_flag WHERE line_key LIKE $1 ORDER BY line_key`,
		start.Format("2006-01")+"|%")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	type item struct {
		LineKey   string `json:"line_key"`
		RMBPriced bool   `json:"rmb_priced"`
		BillName  string `json:"bill_name"`
		UpdatedAt int64  `json:"updated_at"`
	}
	list := []item{}
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.LineKey, &it.RMBPriced, &it.BillName, &it.UpdatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		list = append(list, it)
	}
	c.JSON(http.StatusOK, gin.H{"list": list})
}

// handleTencentLineFlagSet stores a row's flags; a row back to the defaults
// (not RMB-priced, no bill name) drops its record.
func handleTencentLineFlagSet(c *gin.Context) {
	var body struct {
		LineKey   string `json:"line_key"`
		RMBPriced bool   `json:"rmb_priced"`
		BillName  string `json:"bill_name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	parts := strings.SplitN(body.LineKey, "|", 4)
	if len(parts) != 4 || !slices.Contains(tencentPlatforms, parts[1]) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid line_key"})
		return
	}
	if _, _, err := tencentMonthRange(parts[0]); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid line_key"})
		return
	}
	billName := strings.TrimSpace(body.BillName)
	var err error
	if !body.RMBPriced && billName == "" {
		_, err = db.Exec(`DELETE FROM rs_tencent_line_flag WHERE line_key = $1`, body.LineKey)
	} else {
		_, err = db.Exec(`
			INSERT INTO rs_tencent_line_flag (line_key, rmb_priced, bill_name, updated_at, updated_by)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (line_key) DO UPDATE SET rmb_priced=EXCLUDED.rmb_priced, bill_name=EXCLUDED.bill_name,
			  updated_at=EXCLUDED.updated_at, updated_by=EXCLUDED.updated_by`,
			body.LineKey, body.RMBPriced, billName, time.Now().Unix(), tencentCallerID(c))
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- Vendor naming + settings ----

func handleTencentVendorsGet(c *gin.Context) {
	defaults := make([]gin.H, len(tencentDefaultVendorRules))
	for i, r := range tencentDefaultVendorRules {
		defaults[i] = gin.H{"match": r.match, "vendor": r.vendor}
	}
	c.JSON(http.StatusOK, gin.H{"custom": tencentVendorMap(), "defaults": defaults})
}

// handleTencentVendorsSet replaces the custom vendor rules (lowercase
// substring → vendor name).
func handleTencentVendorsSet(c *gin.Context) {
	var body struct {
		Custom map[string]string `json:"custom"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	clean := map[string]string{}
	for k, v := range body.Custom {
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if k == "" || v == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "匹配词和厂商名都不能为空"})
			return
		}
		clean[k] = v
	}
	blob, _ := json.Marshal(clean)
	if err := supplierConfigSet(cfgTencentVendorMap, string(blob)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	handleTencentVendorsGet(c)
}

func handleTencentSettingsGet(c *gin.Context) {
	profileID, _ := strconv.ParseInt(supplierConfigGet(cfgTencentProxyProfileID), 10, 64)
	upload, _ := strconv.ParseFloat(supplierAccountDiscount(), 64)
	c.JSON(http.StatusOK, gin.H{
		"proxy_profile_id":    profileID,
		"system_fx_rate":      supplierFxRate(),
		"upload_discount_pct": upload,
	})
}

// handleTencentSettingsSet picks the remote profile mirroring the proxy's
// upstream newapi (0 = none, proxy usage is not billed).
func handleTencentSettingsSet(c *gin.Context) {
	var body struct {
		ProxyProfileID *int64 `json:"proxy_profile_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if body.ProxyProfileID != nil {
		id := *body.ProxyProfileID
		if id > 0 {
			var exists bool
			if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM remote_newapi_profile WHERE id = $1)`, id).Scan(&exists); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if !exists {
				c.JSON(http.StatusBadRequest, gin.H{"error": "remote profile not found"})
				return
			}
		}
		if err := supplierConfigSet(cfgTencentProxyProfileID, strconv.FormatInt(max(id, 0), 10)); err != nil {
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
	g.GET("/params/suppliers", handleTencentSupplierParamList)
	g.PUT("/params/suppliers", handleTencentSupplierParamSet)
	g.DELETE("/params/suppliers/:user_id/:platform", handleTencentSupplierParamDelete)
	g.GET("/line-flags", handleTencentLineFlagList)
	g.PUT("/line-flags", handleTencentLineFlagSet)
	g.GET("/vendors", handleTencentVendorsGet)
	g.PUT("/vendors", handleTencentVendorsSet)
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
