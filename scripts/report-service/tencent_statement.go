package main

// 腾讯一站 · 月对账: the statement we, as Tencent's supplier (company name in
// column A, default 长河理想), settle each Beijing month, in the layout of
// 「长河理想公司10月对账单」: 「API账单」 (columns A–M) and 「汇总表」. Rows come
// from three sources:
//
//   - proxy: each proxy_channel_ownership channel's consume logs of the month
//     on newapi-proxy's upstream New API (/api/log/stat), per model, USD; one
//     row per vendor × model (azure-openai / azure-claude), E lists channels.
//   - ccmax: rs_ccmax_daily_bill of every CC Max account, RMB; one row
//     (vendor ConfluxCC, E 全部CCMax账户) listing the models in D.
//   - third_party (账号上号 / 老台子): the portal WEB bill/query of the month
//     for every non-claude roster account, RMB; one row per account × model.
//
// Each vendor (column C) has a pricing method, discount, tax text and note in
// the month's config (rs_tencent_month_config, inherited from the latest
// earlier month):
//
//   - usd:   J = G × I, K = BOC USD rate, L = J × K
//   - ccmax: J = G ÷ CCMax divisor × I, K = BOC USD rate, L = J × K
//   - rmb:   J = G × I, K = 1, L = J
//
// Lines keep full precision; amounts are rounded to the cent only per vendor
// in the summary, and the total is the sum of those.

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	tencentPlatformThirdParty = "third_party"
	tencentPlatformCCMax      = "ccmax"
	tencentPlatformProxy      = "proxy"

	tencentPricingUSD   = "usd"
	tencentPricingCCMax = "ccmax"
	tencentPricingRMB   = "rmb"

	// tencentCCMaxVendor is column C of the CC Max row.
	tencentCCMaxVendor = "ConfluxCC"
	// tencentCCMaxFallbackModel is column D for CC Max cost whose bill carried
	// no model (days pulled before per-model storage).
	tencentCCMaxFallbackModel = "Claude（CC Max 订阅）"
	// tencentPortalBillDays is how far back the portal keeps bills.
	tencentPortalBillDays = 90
	// tencentBOCRateURL is where the BOC USD selling rate (K) is looked up.
	tencentBOCRateURL = "https://www.kylc.com/huilv/d-boc-usd.html"

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

// tencentPlatforms is also the row order: proxy, CC Max, then 3P, as in the
// template.
var tencentPlatforms = []string{tencentPlatformProxy, tencentPlatformCCMax, tencentPlatformThirdParty}

// tencentSourcePrefix names each platform's source for the summary notes and
// the stored lines.
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
	"官方导出的账单名称",
}

var tencentSummaryHeaders = []string{
	"供应商名称", "账单月份", "模型厂商名称", "付给官方的费用（不区分币种）", "开票金额（RMB/元）", "备注",
}

var tencentSchemaStatements = []string{
	// Left over from V1 (one rate / discount / tax per month); unused.
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
	// A month's statement config (tencentMonthConfig as JSON). A month without
	// a row uses the latest earlier month's.
	`CREATE TABLE IF NOT EXISTS rs_tencent_month_config (
		month      TEXT PRIMARY KEY,
		config     TEXT   NOT NULL,
		updated_at BIGINT NOT NULL,
		updated_by BIGINT NOT NULL DEFAULT 0
	)`,
	// Month-end snapshots (tencent_statement_export.go). A month is a draft
	// until confirmed; a confirmed month no longer follows the source data.
	// params holds the tencentStatementMeta (config) the lines were built
	// with; fx_rate is the CCMax divisor and ref_fx_rate the BOC rate.
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
	// One row per statement line (A–M plus the source and pricing).
	// official_usd / tencent_usd / invoice_rmb hold G / J / L at full
	// precision in the row's currency; rmb_priced and discount_from are left
	// over from the first version and unused.
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
	`ALTER TABLE rs_tencent_statement_line ADD COLUMN IF NOT EXISTS pricing TEXT NOT NULL DEFAULT ''`,
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
	// Each month's source data (tencentSourceRow JSON from loadTencentSources),
	// computed daily and on demand (tencent_source_cache.go) so the preview
	// does not read the slow sources live; it is priced with the current
	// config when read. computed_at = 0: never computed successfully.
	// computing_since > 0 claims a running computation; error is the last
	// failure (the previous rows stay).
	`CREATE TABLE IF NOT EXISTS rs_tencent_source_cache (
		month               TEXT PRIMARY KEY,
		rows                TEXT   NOT NULL DEFAULT '[]',
		warnings            TEXT   NOT NULL DEFAULT '[]',
		ccmax_first_account BIGINT NOT NULL DEFAULT 0,
		computed_at         BIGINT NOT NULL DEFAULT 0,
		duration_ms         BIGINT NOT NULL DEFAULT 0,
		computing_since     BIGINT NOT NULL DEFAULT 0,
		error               TEXT   NOT NULL DEFAULT '',
		updated_at          BIGINT NOT NULL DEFAULT 0
	)`,
}

// tencentConfigSeedMonth is the first settled month; its config (the October
// 2026 statement's parameters) is written on startup when missing, and is
// also the built-in default before it.
const tencentConfigSeedMonth = "2026-10"

// initTencentSchema applies the 腾讯一站 DDLs and seeds the first month's
// config. Idempotent; fatal on failure like initV2Schema.
func initTencentSchema() {
	for _, ddl := range tencentSchemaStatements {
		if _, err := db.Exec(ddl); err != nil {
			log.Fatalf("tencent schema init failed on %.80q: %v", ddl, err)
		}
	}
	raw, _ := json.Marshal(tencentDefaultMonthConfig())
	if _, err := db.Exec(`INSERT INTO rs_tencent_month_config (month, config, updated_at) VALUES ($1, $2, $3) ON CONFLICT (month) DO NOTHING`,
		tencentConfigSeedMonth, string(raw), time.Now().Unix()); err != nil {
		log.Fatalf("tencent month config seed: %v", err)
	}
}

// tencentVendorConfig is one column C vendor's pricing on the statement.
type tencentVendorConfig struct {
	Vendor   string   `json:"vendor"`
	Pricing  string   `json:"pricing"`  // usd | ccmax | rmb
	Discount *float64 `json:"discount"` // I as a fraction; nil = not agreed yet
	TaxText  string   `json:"tax_text"` // H
	Note     string   `json:"note"`     // 汇总表 备注
	Label    string   `json:"label"`    // the discount's name in the parameter block
}

// tencentMonthConfig is a month's statement config.
type tencentMonthConfig struct {
	Company      string                `json:"company"`       // A
	BOCRate      float64               `json:"boc_rate"`      // K: BOC USD selling rate
	BOCRateDate  string                `json:"boc_rate_date"` // YYYY-MM-DD the rate was taken
	CCMaxDivisor float64               `json:"ccmax_divisor"` // CC Max RMB ÷ this = USD
	Vendors      []tencentVendorConfig `json:"vendors"`
}

func tencentFloatPtr(v float64) *float64 { return &v }

// tencentDefaultMonthConfig is the October 2026 statement's parameters.
func tencentDefaultMonthConfig() tencentMonthConfig {
	proxyNote := "newapi proxy；原金额USD，已应用约定折扣"
	return tencentMonthConfig{
		Company:      "长河理想",
		BOCRate:      6.6728,
		BOCRateDate:  "2026-11-01",
		CCMaxDivisor: 7.2,
		Vendors: []tencentVendorConfig{
			{Vendor: "azure-openai", Pricing: tencentPricingUSD, Discount: tencentFloatPtr(0.73), TaxText: "含票", Note: proxyNote, Label: "Azure OpenAI 折扣"},
			{Vendor: "azure-claude", Pricing: tencentPricingUSD, Discount: tencentFloatPtr(0.95), TaxText: "含票", Note: proxyNote, Label: "Azure Claude 折扣"},
			{Vendor: tencentCCMaxVendor, Pricing: tencentPricingCCMax, Discount: tencentFloatPtr(0.5), TaxText: "含税",
				Note: "CCMax；原RMB÷7.2×折扣×汇率，含税", Label: "CCMax折扣（含税）"},
			{Vendor: "zhipu", Pricing: tencentPricingRMB, Discount: tencentFloatPtr(0.75), TaxText: "不另加税费",
				Note: "zhipu；人民币原价×折扣，不另加税费、不换汇", Label: "zhipu 折扣"},
		},
	}
}

// tencentPricingDefaults are a vendor's tax text and note when the config
// does not name it.
var tencentPricingDefaults = map[string]struct{ tax, note string }{
	tencentPricingUSD:   {"含票", "newapi proxy；原金额USD，已应用约定折扣"},
	tencentPricingCCMax: {"含税", "CCMax；原RMB÷除数×折扣×汇率，含税"},
	tencentPricingRMB:   {"不另加税费", "人民币原价×折扣，不另加税费、不换汇"},
}

// tencentPlatformPricing is the pricing of a vendor missing from the config.
var tencentPlatformPricing = map[string]string{
	tencentPlatformProxy:      tencentPricingUSD,
	tencentPlatformCCMax:      tencentPricingCCMax,
	tencentPlatformThirdParty: tencentPricingRMB,
}

// vendor returns the config of a vendor, filled with defaults for one the
// config does not name (pricing by its platform, no discount).
func (c tencentMonthConfig) vendor(name, platform string) tencentVendorConfig {
	for _, v := range c.Vendors {
		if v.Vendor == name {
			if v.Label == "" {
				v.Label = name + " 折扣"
			}
			return v
		}
	}
	pricing := tencentPlatformPricing[platform]
	d := tencentPricingDefaults[pricing]
	return tencentVendorConfig{Vendor: name, Pricing: pricing, TaxText: d.tax, Note: name + "；" + d.note, Label: name + " 折扣"}
}

var tencentDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// validate checks a config before it is stored.
func (c tencentMonthConfig) validate() error {
	switch {
	case strings.TrimSpace(c.Company) == "":
		return errors.New("公司名不能为空")
	case c.BOCRate <= 0 || c.BOCRate > 100:
		return errors.New("中行汇率须在 0–100 之间")
	case !tencentDateRe.MatchString(c.BOCRateDate):
		return errors.New("汇率日期须为 YYYY-MM-DD")
	case c.CCMaxDivisor <= 0 || c.CCMaxDivisor > 100:
		return errors.New("CCMax 除数须在 0–100 之间")
	}
	seen := map[string]bool{}
	for _, v := range c.Vendors {
		name := strings.TrimSpace(v.Vendor)
		switch {
		case name == "":
			return errors.New("厂商名不能为空")
		case seen[name]:
			return fmt.Errorf("厂商 %s 重复", name)
		case !slices.Contains([]string{tencentPricingUSD, tencentPricingCCMax, tencentPricingRMB}, v.Pricing):
			return fmt.Errorf("厂商 %s 的计价方式须为 usd / ccmax / rmb", name)
		case v.Discount != nil && (*v.Discount <= 0 || *v.Discount > 1):
			return fmt.Errorf("厂商 %s 的折扣须在 0–1 之间（如 0.73）", name)
		}
		seen[name] = true
	}
	return nil
}

// loadTencentMonthConfig reads the month's config, else the latest earlier
// month's, else the built-in default. from says which ("month", "inherit"
// or "default"); inherited is the month it came from.
func loadTencentMonthConfig(month time.Time) (cfg tencentMonthConfig, from, inherited string, err error) {
	key := month.Format("2006-01")
	var raw string
	err = db.QueryRow(`SELECT month, config FROM rs_tencent_month_config WHERE month <= $1 ORDER BY month DESC LIMIT 1`, key).Scan(&inherited, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return tencentDefaultMonthConfig(), "default", "", nil
	}
	if err != nil {
		return cfg, "", "", err
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, "", "", fmt.Errorf("month config %s: %w", inherited, err)
	}
	if inherited == key {
		return cfg, "month", inherited, nil
	}
	return cfg, "inherit", inherited, nil
}

// tencentSourceRow is one platform's month cost of one vendor and model
// (3P: of one account; proxy: of one channel), in the source's currency:
// USD for proxy, RMB otherwise.
type tencentSourceRow struct {
	Platform string
	Vendor   string // C
	Model    string // D
	Account  string // E (3P)
	Key      string // F (3P)
	Channel  int64  // proxy
	Cost     float64
}

// tencentStatementMeta is what a statement was built with, kept with its
// snapshot so an export reproduces it.
type tencentStatementMeta struct {
	Month  string             `json:"month"`
	Config tencentMonthConfig `json:"config"`
	// CCMaxFirstAccount is when the first CC Max account was created (unix),
	// for the summary's CC Max note; 0 when unknown.
	CCMaxFirstAccount int64 `json:"ccmax_first_account"`
}

// tencentLine is one 「API账单」 row. G, J and L are at full precision in the
// row's currency (pricing usd: USD; ccmax: G RMB, J USD; rmb: RMB).
type tencentLine struct {
	LineKey    string  `json:"line_key"`
	Platform   string  `json:"platform"`
	Pricing    string  `json:"pricing"`
	Supplier   string  `json:"supplier"`    // A
	BillMonth  int     `json:"bill_month"`  // B
	Vendor     string  `json:"vendor"`      // C
	Model      string  `json:"model"`       // D
	AccountID  string  `json:"account_id"`  // E
	Key        string  `json:"key"`         // F
	Official   float64 `json:"official"`    // G
	TaxRate    string  `json:"tax_rate"`    // H
	Discount   float64 `json:"discount"`    // I, a fraction
	Tencent    float64 `json:"tencent"`     // J
	RefFxRate  float64 `json:"ref_fx_rate"` // K
	InvoiceRMB float64 `json:"invoice_rmb"` // L
	BillName   string  `json:"bill_name"`   // M
	Source     string  `json:"source"`
}

// tencentSummaryRow is one 「汇总表」 row: a vendor's G and its L rounded to
// the cent.
type tencentSummaryRow struct {
	Supplier   string  `json:"supplier"`
	BillMonth  int     `json:"bill_month"`
	Vendor     string  `json:"vendor"`
	Pricing    string  `json:"pricing"`
	Official   float64 `json:"official"`
	InvoiceRMB float64 `json:"invoice_rmb"`
	Note       string  `json:"note"`
	Source     string  `json:"source"`
	Lines      int     `json:"lines"`
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

// tencentBillName is column M: the original bill the row comes from.
func tencentBillName(company string, month time.Time, platform, vendor string) string {
	prefix := fmt.Sprintf("%s公司%d月对账单_", company, int(month.Month()))
	switch platform {
	case tencentPlatformProxy:
		return prefix + "newapi-proxy后台账单.zip"
	case tencentPlatformCCMax:
		return prefix + "CCMax官方日明细.csv"
	}
	return prefix + vendor + "原始账单.xlsx"
}

// buildTencentStatement groups the source rows into 「API账单」 rows (proxy
// vendor × model, one CC Max row, 3P account × model), prices them with the
// month's config and returns them with the 「汇总表」 rows. Warnings name
// vendors without an agreed discount (priced at 100% until set).
func buildTencentStatement(rows []tencentSourceRow, cfg tencentMonthConfig, month time.Time) ([]tencentLine, []tencentSummaryRow, []string) {
	type group struct {
		platform, vendor, account, key string
		models                         []string
		channels                       []int64
		cost                           float64
	}
	groups := map[string]*group{}
	for _, r := range rows {
		var k string
		switch r.Platform {
		case tencentPlatformProxy:
			k = r.Platform + "|" + r.Vendor + "|" + r.Model
		case tencentPlatformCCMax:
			r.Vendor = tencentCCMaxVendor
			k = r.Platform
		default:
			k = r.Platform + "|" + r.Vendor + "|" + r.Account + "|" + r.Model
		}
		g, ok := groups[k]
		if !ok {
			g = &group{platform: r.Platform, vendor: r.Vendor, account: r.Account, key: r.Key}
			groups[k] = g
		}
		g.cost += r.Cost
		if r.Model != "" && !slices.Contains(g.models, r.Model) {
			g.models = append(g.models, r.Model)
		}
		if r.Channel > 0 && !slices.Contains(g.channels, r.Channel) {
			g.channels = append(g.channels, r.Channel)
		}
	}

	warned := map[string]bool{}
	warnings := []string{}
	lines := make([]tencentLine, 0, len(groups))
	for _, g := range groups {
		v := cfg.vendor(g.vendor, g.platform)
		discount := 1.0
		if v.Discount != nil {
			discount = *v.Discount
		} else if !warned[v.Vendor] {
			warned[v.Vendor] = true
			warnings = append(warnings, fmt.Sprintf("厂商 %s 未配置折扣，暂按 100%% 计算，请在账单参数里补上", v.Vendor))
		}
		slices.Sort(g.models)
		account := g.account
		switch g.platform {
		case tencentPlatformProxy:
			slices.Sort(g.channels)
			ids := make([]string, len(g.channels))
			for i, id := range g.channels {
				ids[i] = strconv.FormatInt(id, 10)
			}
			account = "渠道 " + strings.Join(ids, ", ")
		case tencentPlatformCCMax:
			account = "全部CCMax账户"
		}
		j := g.cost * discount
		k := cfg.BOCRate
		switch v.Pricing {
		case tencentPricingCCMax:
			j = g.cost / cfg.CCMaxDivisor * discount
		case tencentPricingRMB:
			k = 1
		}
		model := strings.Join(g.models, ", ")
		lines = append(lines, tencentLine{
			LineKey:    strings.Join([]string{month.Format("2006-01"), g.platform, g.vendor, g.account, model}, "|"),
			Platform:   g.platform,
			Pricing:    v.Pricing,
			Supplier:   cfg.Company,
			BillMonth:  int(month.Month()),
			Vendor:     g.vendor,
			Model:      model,
			AccountID:  account,
			Key:        g.key,
			Official:   g.cost,
			TaxRate:    v.TaxText,
			Discount:   discount,
			Tencent:    j,
			RefFxRate:  k,
			InvoiceRMB: j * k,
			BillName:   tencentBillName(cfg.Company, month, g.platform, g.vendor),
			Source:     tencentSourcePrefix[g.platform],
		})
	}
	// Platform, vendor, then (3P) account and model, as the template orders.
	slices.SortFunc(lines, func(a, b tencentLine) int {
		byAccount := 0
		if a.Platform == tencentPlatformThirdParty && b.Platform == tencentPlatformThirdParty {
			byAccount = cmp.Or(cmp.Compare(len(a.AccountID), len(b.AccountID)), cmp.Compare(a.AccountID, b.AccountID))
		}
		return cmp.Or(
			cmp.Compare(slices.Index(tencentPlatforms, a.Platform), slices.Index(tencentPlatforms, b.Platform)),
			cmp.Compare(a.Vendor, b.Vendor),
			byAccount,
			cmp.Compare(a.Model, b.Model),
		)
	})
	slices.Sort(warnings)
	return lines, tencentSummarize(lines, cfg), warnings
}

// tencentSummarize builds the 「汇总表」 rows: one per vendor in line order,
// G summed and L summed then rounded to the cent, as the exported formulas
// =SUMIFS(G) and =ROUND(SUMIFS(L),2) do.
func tencentSummarize(lines []tencentLine, cfg tencentMonthConfig) []tencentSummaryRow {
	summary := []tencentSummaryRow{}
	index := map[string]int{}
	for _, l := range lines {
		i, ok := index[l.Vendor]
		if !ok {
			v := cfg.vendor(l.Vendor, l.Platform)
			i = len(summary)
			index[l.Vendor] = i
			summary = append(summary, tencentSummaryRow{Supplier: l.Supplier, BillMonth: l.BillMonth, Vendor: l.Vendor,
				Pricing: l.Pricing, Note: v.Note, Source: l.Source})
		}
		summary[i].Official += l.Official
		summary[i].InvoiceRMB += l.InvoiceRMB
		summary[i].Lines++
	}
	for i := range summary {
		summary[i].InvoiceRMB = tencentRound(summary[i].InvoiceRMB, 2)
	}
	return summary
}

// tencentInvoiceTotal is the 开票金额合计: the sum of the vendors' rounded L.
func tencentInvoiceTotal(summary []tencentSummaryRow) float64 {
	total := 0.0
	for _, s := range summary {
		total += s.InvoiceRMB
	}
	return tencentRound(total, 2)
}

// tencentMaskKey is column F of a portal account: the key's first and last
// five characters around ****, as the template shows them (sk-nu****00002).
func tencentMaskKey(apiKey string) string {
	r := []rune(strings.TrimSpace(apiKey))
	if len(r) == 0 {
		return ""
	}
	n := min(5, len(r)/3)
	return string(r[:n]) + "****" + string(r[len(r)-n:])
}

// loadTencentSources reads every platform's cost in the Beijing month
// [start, end). Warnings name cost that could not be billed or may be partial.
func loadTencentSources(start, end time.Time) ([]tencentSourceRow, int64, []string, error) {
	out, warnings, err := loadTencentThirdPartyRows(start, end, tencentNow())
	if err != nil {
		return nil, 0, nil, fmt.Errorf("third_party bill: %w", err)
	}
	ccmaxRows, firstAccount, err := loadTencentCCMaxRows(start, end)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("ccmax bill: %w", err)
	}
	out = append(out, ccmaxRows...)

	// proxy: the owned channels' consume logs on the upstream New API.
	var hasOwnership bool
	if err := db.QueryRow(`SELECT to_regclass('proxy_channel_ownership') IS NOT NULL`).Scan(&hasOwnership); err != nil {
		return nil, 0, nil, err
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
			return nil, 0, nil, fmt.Errorf("proxy usage: %w", err)
		}
		out = append(out, proxyRows...)
		warnings = append(warnings, proxyWarnings...)
	}
	return out, firstAccount, warnings, nil
}

// loadTencentThirdPartyRows bills every non-claude account on the 账号上号
// portal roster with the portal's own bill of the month (WEB bill/query, RMB),
// per account and model, up to today; the portal only keeps
// tencentPortalBillDays of bills.
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
	type key struct {
		id    int64
		model string
	}
	costs := map[key]float64{}
	order := []key{}
	for _, b := range bill {
		a, ok := accounts[b.AccountID]
		if !ok || b.Cost == 0 {
			continue
		}
		model := b.Model
		if model == "" {
			model = cmp.Or(strings.TrimSpace(strings.Split(a.Model, ",")[0]), "未注明模型")
		}
		k := key{b.AccountID, model}
		if _, seen := costs[k]; !seen {
			order = append(order, k)
		}
		costs[k] += b.Cost
	}
	out := []tencentSourceRow{}
	for _, k := range order {
		if costs[k] <= 0 {
			continue
		}
		a := accounts[k.id]
		out = append(out, tencentSourceRow{
			Platform: tencentPlatformThirdParty,
			Vendor:   strings.ToLower(strings.TrimSpace(a.Provider)),
			Model:    k.model,
			Account:  strconv.FormatInt(k.id, 10),
			Key:      tencentMaskKey(a.APIKey),
			Cost:     costs[k],
		})
	}
	return out, warnings, nil
}

// loadTencentCCMaxRows reads every CC Max account's stored daily bills of the
// month (RMB) per model; a day stored without models counts under
// tencentCCMaxFallbackModel. It also returns when the first CC Max account was
// created (unix, 0 = none).
func loadTencentCCMaxRows(start, end time.Time) ([]tencentSourceRow, int64, error) {
	from, to := start.Format("2006-01-02"), end.Format("2006-01-02")
	var firstAccount sql.NullInt64
	if err := db.QueryRow(`SELECT MIN(created_at) FROM rs_ccmax_account`).Scan(&firstAccount); err != nil {
		return nil, 0, err
	}
	var totalMicro int64
	if err := db.QueryRow(`
		SELECT COALESCE(SUM(b.cost_micro), 0)::bigint
		  FROM rs_ccmax_daily_bill b
		  JOIN rs_ccmax_account a ON a.remote_account_id = b.remote_account_id
		 WHERE b.ds >= $1::date AND b.ds < $2::date`, from, to).Scan(&totalMicro); err != nil {
		return nil, 0, err
	}
	rows, err := db.Query(`
		SELECT m.model, SUM(m.cost_micro)::bigint
		  FROM rs_ccmax_daily_bill_model m
		  JOIN rs_ccmax_account a ON a.remote_account_id = m.remote_account_id
		 WHERE m.ds >= $1::date AND m.ds < $2::date AND m.model <> ''
		 GROUP BY 1 HAVING SUM(m.cost_micro) > 0 ORDER BY 1`, from, to)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []tencentSourceRow{}
	var modelMicro int64
	for rows.Next() {
		var model string
		var micro int64
		if err := rows.Scan(&model, &micro); err != nil {
			return nil, 0, err
		}
		modelMicro += micro
		out = append(out, tencentSourceRow{Platform: tencentPlatformCCMax, Vendor: tencentCCMaxVendor, Model: model, Cost: float64(micro) / 1e6})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if rest := totalMicro - modelMicro; rest > 0 {
		out = append(out, tencentSourceRow{Platform: tencentPlatformCCMax, Vendor: tencentCCMaxVendor, Model: tencentCCMaxFallbackModel, Cost: float64(rest) / 1e6})
	}
	return out, firstAccount.Int64, nil
}

// tencentProxyVendor names column C of a proxy model from its newapi channel
// type, telling Claude models on Azure / Vertex from the others.
func tencentProxyVendor(channelType int, model string) string {
	claude := strings.Contains(strings.ToLower(model), "claude")
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

// loadTencentProxyRows bills each channel in proxy_channel_ownership with its
// consume logs of the Beijing month (/api/log/stat type=2, up to now) on the
// remote profile of newapi-proxy's upstream, per model of the channel
// (/api/channel/:id), quota ÷ the upstream's quota_per_unit = USD. Requests go
// through remoteDoJSON, so the profile's token, user and proxy apply. A
// channel whose request fails is left out with a warning; quota not matched
// to a listed model is billed as 其它模型 with a warning.
func loadTencentProxyRows(profileID int64, start, end, now time.Time) ([]tencentSourceRow, []string, error) {
	host, userID, token, err := loadRemoteProfileByID(profileID)
	if err != nil {
		return nil, []string{fmt.Sprintf("proxy: 远端实例 #%d 不可用（%v），New API Proxy 未出账", profileID, err)}, nil
	}
	rows, err := db.Query(`SELECT remote_channel_id FROM proxy_channel_ownership ORDER BY 1`)
	if err != nil {
		return nil, nil, err
	}
	channels := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, err
		}
		channels = append(channels, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(channels) == 0 {
		return nil, nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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

	// stat is the channel's (and model's, when given) quota of the month,
	// backing off and retrying when the upstream rate-limits.
	stat := func(channel int64, model string) (int64, error) {
		q := url.Values{"type": {"2"}, "channel": {strconv.FormatInt(channel, 10)},
			"start_timestamp": {strconv.FormatInt(from, 10)}, "end_timestamp": {strconv.FormatInt(to, 10)}}
		if model != "" {
			q.Set("model_name", model)
		}
		raw, err := remoteDoJSON(ctx, http.MethodGet, host, "/api/log/stat", token, userID, q, nil)
		for wait := tencentProxyRetryWait; isRemoteRateLimited(err) && wait <= 8*tencentProxyRetryWait; wait *= 2 {
			select {
			case <-ctx.Done():
			case <-time.After(wait):
			}
			raw, err = remoteDoJSON(ctx, http.MethodGet, host, "/api/log/stat", token, userID, q, nil)
		}
		if err != nil {
			return 0, err
		}
		var s struct {
			Quota int64 `json:"quota"`
		}
		err = json.Unmarshal(raw, &s)
		return s.Quota, err
	}

	type result struct {
		rows      []tencentSourceRow
		unmatched float64
		err       error
	}
	results := make([]result, len(channels))
	sem := make(chan struct{}, tencentProxyConcurrency)
	var wg sync.WaitGroup
	for i, channel := range channels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			total, err := stat(channel, "")
			if err != nil || total <= 0 {
				results[i] = result{err: err}
				return
			}
			var ch struct {
				Type   int    `json:"type"`
				Models string `json:"models"`
			}
			// A channel deleted upstream keeps its logs; bill it as 其它模型.
			if raw, err := remoteDoJSON(ctx, http.MethodGet, host, fmt.Sprintf("/api/channel/%d", channel), token, userID, nil, nil); err == nil {
				_ = json.Unmarshal(raw, &ch)
			}
			res := result{}
			matched := int64(0)
			for m := range strings.SplitSeq(ch.Models, ",") {
				if m = strings.TrimSpace(m); m == "" {
					continue
				}
				quota, err := stat(channel, m)
				if err != nil {
					results[i] = result{err: err}
					return
				}
				if quota > 0 {
					matched += quota
					res.rows = append(res.rows, tencentSourceRow{Platform: tencentPlatformProxy, Vendor: tencentProxyVendor(ch.Type, m),
						Model: m, Channel: channel, Cost: float64(quota) / quotaPerUnit})
				}
			}
			if rest := total - matched; rest > 0 {
				res.unmatched = float64(rest) / quotaPerUnit
				res.rows = append(res.rows, tencentSourceRow{Platform: tencentPlatformProxy, Vendor: tencentProxyVendor(ch.Type, ""),
					Model: "其它模型", Channel: channel, Cost: res.unmatched})
			}
			results[i] = res
		}()
	}
	wg.Wait()

	out := []tencentSourceRow{}
	warnings := []string{}
	for i, res := range results {
		if res.err != nil {
			warnings = append(warnings, fmt.Sprintf("proxy: 渠道 #%d 取数失败（%v），未出账", channels[i], res.err))
			continue
		}
		if res.unmatched > 0 {
			warnings = append(warnings, fmt.Sprintf("proxy: 渠道 #%d 有 %.2f USD 不属于渠道当前模型列表，记为「其它模型」", channels[i], res.unmatched))
		}
		out = append(out, res.rows...)
	}
	return out, warnings, nil
}

func tencentCallerID(c *gin.Context) int64 {
	uidAny, _ := c.Get("user_id")
	uid, _ := uidAny.(int64)
	return uid
}

// tencentLoadSourcesFn reads a month's source data; tests replace it to count
// or block the slow live reads.
var tencentLoadSourcesFn = loadTencentSources

// tencentLiveStatement builds the Beijing month [start, end) statement from
// the current source data and the month's config.
func tencentLiveStatement(start, end time.Time) (tencentStatementMeta, []tencentLine, []tencentSummaryRow, []string, error) {
	sources, firstAccount, warnings, err := tencentLoadSourcesFn(start, end)
	if err != nil {
		return tencentStatementMeta{}, nil, nil, nil, err
	}
	return tencentPriceStatement(start, end, sources, firstAccount, warnings)
}

// tencentPriceStatement prices a month's source data with the month's current
// config: the step shared by the live statement and the cached preview, so a
// config change applies to cached sources at once.
func tencentPriceStatement(start, end time.Time, sources []tencentSourceRow, firstAccount int64, sourceWarnings []string) (tencentStatementMeta, []tencentLine, []tencentSummaryRow, []string, error) {
	cfg, from, inherited, err := loadTencentMonthConfig(start)
	if err != nil {
		return tencentStatementMeta{}, nil, nil, nil, err
	}
	meta := tencentStatementMeta{Month: start.Format("2006-01"), Config: cfg, CCMaxFirstAccount: firstAccount}
	lines, summary, priceWarnings := buildTencentStatement(sources, cfg, start)
	warnings := append(slices.Clone(sourceWarnings), priceWarnings...)
	if want := end.Format("2006-01-02"); cfg.BOCRateDate != want {
		note := ""
		if from == "inherit" {
			note = fmt.Sprintf("（沿用 %s 的配置）", inherited)
		}
		warnings = append(warnings, fmt.Sprintf("中行汇率取价日期为 %s%s，结算月次月 1 日为 %s，请核对汇率", cfg.BOCRateDate, note, want))
	}
	return meta, lines, summary, warnings, nil
}

// handleTencentStatement previews a month's statement (default: last month),
// optionally for one platform. It prices the month's cached source data
// (rs_tencent_source_cache) with the current config; a month without a cache
// starts a background computation and answers computing with no lines.
// ?live=1 reads the sources live instead (slow; for troubleshooting).
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
	platform := c.Query("platform")
	if platform != "" && !slices.Contains(tencentPlatforms, platform) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown platform"})
		return
	}
	resp := gin.H{"source": "live", "computed_at": time.Now().Unix(), "computing": false}
	var meta tencentStatementMeta
	var lines []tencentLine
	var summary []tencentSummaryRow
	var warnings []string
	if c.Query("live") == "1" {
		meta, lines, summary, warnings, err = tencentLiveStatement(start, end)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
	} else {
		cache, err := tencentLoadSourceCache(start)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		computing := cache.computing(time.Now())
		if cache.ComputedAt == 0 && !computing {
			// Another node may claim it first; either way it is now running.
			_, err := tencentStartSourceCompute(start)
			computing = err == nil
			if err != nil {
				log.Printf("[tencent-cache] start %s: %v", start.Format("2006-01"), err)
			}
		}
		meta, lines, summary, warnings, err = tencentPriceStatement(start, end, cache.Rows, cache.CCMaxFirstAccount, cache.Warnings)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if cache.Error != "" {
			warnings = append(warnings, "源数据最近一次计算失败："+cache.Error)
		}
		resp = gin.H{"source": "cache", "computed_at": cache.ComputedAt, "computing": computing, "cache_error": cache.Error}
	}
	if platform != "" {
		lines = slices.DeleteFunc(lines, func(l tencentLine) bool { return l.Platform != platform })
		summary = tencentSummarize(lines, meta.Config)
	}
	maps.Copy(resp, gin.H{
		"month":             meta.Month,
		"bill_month":        int(start.Month()),
		"config":            meta.Config,
		"headers":           tencentStatementHeaders,
		"summary_headers":   tencentSummaryHeaders,
		"lines":             lines,
		"summary":           summary,
		"total_invoice_rmb": tencentInvoiceTotal(summary),
		"warnings":          warnings,
	})
	c.JSON(http.StatusOK, resp)
}

// ---- Month config ----

// handleTencentConfigGet returns the config a month's statement uses (?month,
// default last month) and where it comes from.
func handleTencentConfigGet(c *gin.Context) {
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
	cfg, from, inherited, err := loadTencentMonthConfig(start)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"month":               start.Format("2006-01"),
		"config":              cfg,
		"from":                from,
		"inherited":           inherited,
		"default_rate_date":   end.Format("2006-01-02"),
		"boc_rate_source_url": tencentBOCRateURL,
	})
}

// handleTencentConfigSet stores a month's whole config; later months without
// their own config follow it.
func handleTencentConfigSet(c *gin.Context) {
	var body struct {
		Month  string             `json:"month"`
		Config tencentMonthConfig `json:"config"`
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
	cfg := body.Config
	cfg.Company = strings.TrimSpace(cfg.Company)
	for i := range cfg.Vendors {
		v := &cfg.Vendors[i]
		v.Vendor, v.TaxText, v.Note, v.Label = strings.TrimSpace(v.Vendor), strings.TrimSpace(v.TaxText), strings.TrimSpace(v.Note), strings.TrimSpace(v.Label)
	}
	if err := cfg.validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	raw, _ := json.Marshal(cfg)
	if _, err := db.Exec(`
		INSERT INTO rs_tencent_month_config (month, config, updated_at, updated_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (month) DO UPDATE SET config=EXCLUDED.config, updated_at=EXCLUDED.updated_at, updated_by=EXCLUDED.updated_by`,
		start.Format("2006-01"), string(raw), time.Now().Unix(), tencentCallerID(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "config": cfg})
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
	g.POST("/statement/recompute", handleTencentStatementRecompute)
	g.GET("/source-cache/settings", handleTencentSourceCacheSettingsGet)
	g.PUT("/source-cache/settings", handleTencentSourceCacheSettingsSet)
	g.GET("/config", handleTencentConfigGet)
	g.PUT("/config", handleTencentConfigSet)
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
