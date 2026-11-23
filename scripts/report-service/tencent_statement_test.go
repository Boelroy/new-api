package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"
	"golang.org/x/crypto/bcrypt"
)

// tencentOctoberSources are the October 2026 statement's source amounts (the
// 长河理想公司10月对账单 attachment): proxy USD per vendor × model with its
// channels, CC Max RMB per model, zhipu RMB per account × model.
func tencentOctoberSources() []tencentSourceRow {
	proxy := func(vendor, model string, usd float64, channels ...int64) []tencentSourceRow {
		out := []tencentSourceRow{}
		for i, ch := range channels {
			r := tencentSourceRow{Platform: tencentPlatformProxy, Vendor: vendor, Model: model, Channel: ch}
			if i == 0 {
				r.Cost = usd
			}
			out = append(out, r)
		}
		return out
	}
	openai := []int64{151511, 151536, 151748, 151753, 151757, 151758, 151762, 151763}
	rows := slices.Concat(
		proxy("azure-claude", "claude-opus-4-8", 35.020738, 59814, 59846, 95705),
		proxy("azure-claude", "claude-opus-5", 182.730832, 95705, 59814, 59846),
		proxy("azure-claude", "claude-opus-5-5", 568.03554, 59846, 59814, 95705),
		proxy("azure-openai", "gpt-5.5", 19.694958, openai...),
		proxy("azure-openai", "gpt-5.6-sol", 509.832794, openai...),
		proxy("azure-openai", "gpt-6-astra", 192.688568, append([]int64{41294, 54824, 54825}, openai...)...),
		proxy("azure-openai", "gpt-6-sol", 440.774938, openai...),
		proxy("azure-openai", "gpt-6.1-sol", 180.783626, openai...),
	)
	for model, rmb := range map[string]float64{"claude-fable-5": 6964.29, "claude-fable-5-1": 15406.62, "claude-haiku-5-5": 0.38,
		"claude-opus-4-8": 2216.95, "claude-opus-5": 964998.37, "claude-opus-5-5": 742015.34} {
		rows = append(rows, tencentSourceRow{Platform: tencentPlatformCCMax, Vendor: tencentCCMaxVendor, Model: model, Cost: rmb})
	}
	for _, z := range []struct {
		account, key, model string
		rmb                 float64
	}{
		{"250546", "sk-nu****00002", "glm-5.3", 91409.57}, {"250546", "sk-nu****00002", "glm-5.3-flash", 19434.25},
		{"254537", "sk-nu****00001", "glm-5.2", 238.42}, {"254537", "sk-nu****00001", "glm-5.3", 83581.91},
		{"254537", "sk-nu****00001", "glm-5.3-flash", 17669.78},
	} {
		rows = append(rows, tencentSourceRow{Platform: tencentPlatformThirdParty, Vendor: "zhipu", Model: z.model, Account: z.account, Key: z.key, Cost: z.rmb})
	}
	return rows
}

// Golden: the October 2026 source amounts with October's parameters give
// the invoice amounts of the statement settled with Tencent, per vendor and
// in total; CC Max is one row; lines keep full precision.
func TestTencentStatementGolden(t *testing.T) {
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, billingExportZone)
	lines, summary, warnings := buildTencentStatement(tencentOctoberSources(), tencentDefaultMonthConfig(), month)
	assert.Empty(t, warnings)
	got := map[string]float64{}
	for _, s := range summary {
		got[s.Vendor] = s.InvoiceRMB
	}
	assert.Equal(t, map[string]float64{"azure-claude": 4981.23, "azure-openai": 6545.72, "ConfluxCC": 802405.10, "zhipu": 159250.45}, got)
	assert.Equal(t, 973182.50, tencentInvoiceTotal(summary))
	assert.Equal(t, []string{"azure-claude", "azure-openai", "ConfluxCC", "zhipu"}, []string{summary[0].Vendor, summary[1].Vendor, summary[2].Vendor, summary[3].Vendor})

	require.Len(t, lines, 14)
	cc := slices.DeleteFunc(slices.Clone(lines), func(l tencentLine) bool { return l.Platform != tencentPlatformCCMax })
	require.Len(t, cc, 1, "CC Max is one row")
	assert.Equal(t, tencentLine{
		LineKey:  "2026-10|ccmax|ConfluxCC||claude-fable-5, claude-fable-5-1, claude-haiku-5-5, claude-opus-4-8, claude-opus-5, claude-opus-5-5",
		Platform: tencentPlatformCCMax, Pricing: tencentPricingCCMax, Supplier: "长河理想", BillMonth: 10, Vendor: "ConfluxCC",
		Model:     "claude-fable-5, claude-fable-5-1, claude-haiku-5-5, claude-opus-4-8, claude-opus-5, claude-opus-5-5",
		AccountID: "全部CCMax账户", Official: cc[0].Official, TaxRate: "含税", Discount: 0.5, Tencent: cc[0].Official / 7.2 * 0.5,
		RefFxRate: 6.6728, InvoiceRMB: cc[0].Official / 7.2 * 0.5 * 6.6728, BillName: "长河理想公司10月对账单_CCMax官方日明细.csv", Source: "ccmax",
	}, cc[0])
	assert.InDelta(t, 1731601.95, cc[0].Official, 1e-6)

	first := lines[0]
	assert.Equal(t, []any{"azure-claude", "claude-opus-4-8", "渠道 59814, 59846, 95705", "", "含票", 0.95, 6.6728, "长河理想公司10月对账单_newapi-proxy后台账单.zip"},
		[]any{first.Vendor, first.Model, first.AccountID, first.Key, first.TaxRate, first.Discount, first.RefFxRate, first.BillName})
	assert.InDelta(t, 35.020738*0.95*6.6728, first.InvoiceRMB, 1e-9, "no rounding per line")
	astra := lines[5]
	assert.Equal(t, "渠道 41294, 54824, 54825, 151511, 151536, 151748, 151753, 151757, 151758, 151762, 151763", astra.AccountID)
	zhipu := lines[9]
	assert.Equal(t, []any{"zhipu", "glm-5.3", "250546", "sk-nu****00002", "不另加税费", 0.75, 1.0, 91409.57 * 0.75, "长河理想公司10月对账单_zhipu原始账单.xlsx"},
		[]any{zhipu.Vendor, zhipu.Model, zhipu.AccountID, zhipu.Key, zhipu.TaxRate, zhipu.Discount, zhipu.RefFxRate, zhipu.InvoiceRMB, zhipu.BillName})

	// A vendor without an agreed discount is priced at 100% with a warning.
	cfg := tencentDefaultMonthConfig()
	cfg.Vendors[0].Discount = nil
	rows := append(tencentOctoberSources(), tencentSourceRow{Platform: tencentPlatformThirdParty, Vendor: "minimax", Model: "abab", Account: "9", Cost: 10})
	lines, summary, warnings = buildTencentStatement(rows, cfg, month)
	assert.Equal(t, []string{"厂商 azure-openai 未配置折扣，暂按 100% 计算，请在账单参数里补上", "厂商 minimax 未配置折扣，暂按 100% 计算，请在账单参数里补上"}, warnings)
	i := slices.IndexFunc(summary, func(s tencentSummaryRow) bool { return s.Vendor == "minimax" })
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, []any{tencentPricingRMB, 10.0, "不另加税费"}, []any{summary[i].Pricing, summary[i].InvoiceRMB,
		lines[slices.IndexFunc(lines, func(l tencentLine) bool { return l.Vendor == "minimax" })].TaxRate})
}

func TestTencentRound(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{1.005, 1.01},
		{2.675, 2.68},
		{-1.005, -1.01},
		{0.9 * 7.25, 6.53},
		{1.004, 1},
		{0, 0},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, tencentRound(tc.in, 2), "round(%v)", tc.in)
	}
}

// Column F shows a portal key's first and last five characters.
func TestTencentMaskKey(t *testing.T) {
	assert.Equal(t, "sk-nu****00002", tencentMaskKey("sk-nuXXXXXXXXXXXXXX00002"))
	assert.Equal(t, "ab****gh", tencentMaskKey(" abcdefgh "))
	assert.Equal(t, "", tencentMaskKey(""))
}

// Months are Beijing calendar months whatever the server's zone.
func TestTencentMonthRange(t *testing.T) {
	start, end, err := tencentMonthRange("2026-10")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 9, 30, 16, 0, 0, 0, time.UTC).Unix(), start.Unix())
	assert.Equal(t, time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC).Unix(), end.Unix())
	start, end, err = tencentMonthRange("2026-12")
	require.NoError(t, err)
	assert.Equal(t, "2027-01-01", end.Format("2006-01-02"))
	assert.Equal(t, 12, int(start.Month()))
	for _, bad := range []string{"", "2026-13", "2026/10", "202610"} {
		_, _, err := tencentMonthRange(bad)
		assert.Error(t, err, bad)
	}
}

// /api/tencent/* answers super admins only; a bad month proves the request
// reached the handler without touching the database.
func TestTencentRoutesSuperAdminOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api", func(c *gin.Context) {
		role, _ := strconv.Atoi(c.GetHeader("X-Test-Role"))
		c.Set("role", role)
	})
	registerTencentRoutes(api)
	cases := []struct {
		role int
		want int
	}{
		{minSupplierRole, http.StatusForbidden},
		{minSupplierCCMaxRole, http.StatusForbidden},
		{minCCMaxManagerRole, http.StatusForbidden},
		{minAdminRole, http.StatusForbidden},
		{minSuperAdminRole, http.StatusBadRequest},
	}
	for _, tc := range cases {
		for _, path := range []string{"/api/tencent/statement?month=bad", "/api/tencent/statements/bad", "/api/tencent/config?month=bad"} {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("X-Test-Role", strconv.Itoa(tc.role))
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.want, w.Code, "role %d %s", tc.role, path)
		}
	}
}

// tencentTestDB creates a throwaway database next to the one named by
// REPORT_SERVICE_TEST_DATABASE_URL, points the package db at it with the
// report-service tables the statement reads, and drops it afterwards.
func tencentTestDB(t *testing.T) {
	dsn := os.Getenv("REPORT_SERVICE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("REPORT_SERVICE_TEST_DATABASE_URL not set")
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	name := fmt.Sprintf("rs_tencent_test_%d", time.Now().UnixNano())
	_, err = admin.Exec(`CREATE DATABASE ` + name)
	require.NoError(t, err)
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	u.Path = "/" + name
	test, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	prev := db
	db = test
	t.Cleanup(func() {
		db = prev
		test.Close()
		_, _ = admin.Exec(`DROP DATABASE IF EXISTS ` + name)
		admin.Close()
	})
	// V1 tables live inline in main(); these copies carry the columns read.
	for _, ddl := range []string{
		`CREATE TABLE rs_auth_user (id BIGSERIAL PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL DEFAULT '',
		   role INT NOT NULL DEFAULT 1, studio TEXT NOT NULL DEFAULT '', created_at BIGINT NOT NULL DEFAULT 0, updated_at BIGINT NOT NULL DEFAULT 0,
		   status INT NOT NULL DEFAULT 1, disabled_at BIGINT NOT NULL DEFAULT 0)`,
		`CREATE TABLE rs_login_attempt (id BIGSERIAL PRIMARY KEY, username TEXT NOT NULL, ip TEXT NOT NULL, succeeded BOOLEAN NOT NULL, attempted_at BIGINT NOT NULL)`,
		`CREATE TABLE report_config (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at BIGINT NOT NULL)`,
		`CREATE TABLE remote_newapi_profile (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL DEFAULT '', host TEXT NOT NULL DEFAULT '',
		   user_id BIGINT NOT NULL DEFAULT 0, access_token_enc TEXT NOT NULL DEFAULT '', proxy TEXT NOT NULL DEFAULT '',
		   api_mode TEXT NOT NULL DEFAULT 'admin')`,
		`CREATE TABLE remote_channel_snapshot (profile_id BIGINT NOT NULL, remote_channel_id BIGINT NOT NULL, captured_at BIGINT NOT NULL,
		   used_quota BIGINT NOT NULL, status INT NOT NULL, PRIMARY KEY (profile_id, remote_channel_id, captured_at))`,
		`CREATE TABLE remote_channel_current (profile_id BIGINT NOT NULL, remote_channel_id BIGINT NOT NULL, name TEXT NOT NULL DEFAULT '',
		   type INT NOT NULL DEFAULT 0, models TEXT NOT NULL DEFAULT '', updated_at BIGINT NOT NULL DEFAULT 0, PRIMARY KEY (profile_id, remote_channel_id))`,
	} {
		_, err := db.Exec(ddl)
		require.NoError(t, err)
	}
}

// Startup applies the schema on every boot; a second run must be a no-op.
func TestTencentSchemaIdempotent(t *testing.T) {
	tencentTestDB(t)
	for range 2 {
		for _, ddl := range append(append([]string{}, v2SchemaStatements...), tencentSchemaStatements...) {
			_, err := db.Exec(ddl)
			require.NoError(t, err, "%.80s", ddl)
		}
	}
}

// tencentPortal stubs the 账号上号 portal's WEB roster and bill/query. Bill
// rows are filtered by the requested ids and days like the portal does, and
// every bill query is recorded.
type tencentPortal struct {
	roster  []map[string]any
	bill    []map[string]any
	queries []url.Values
}

func newTencentPortal(t *testing.T, roster, bill []map[string]any) *tencentPortal {
	p := &tencentPortal{roster: roster, bill: bill}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/supplier-account/api/supplier/accounts":
			_ = json.NewEncoder(w).Encode(map[string]any{"list": p.roster})
		case "/supplier-account/api/bill/query":
			q := r.URL.Query()
			p.queries = append(p.queries, q)
			ids := strings.Split(q.Get("account_ids"), ",")
			from, to := q.Get("start_ds"), q.Get("end_ds")
			list := []map[string]any{}
			for _, b := range p.bill {
				ds := strings.ReplaceAll(b["ds_text"].(string), "-", "")
				if slices.Contains(ids, fmt.Sprint(b["account_id"])) && ds >= from && ds <= to {
					list = append(list, b)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"list": list})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	prevURL, prevUser, prevPass, prevNow := supplierAccountBaseURL, supplierAccountUsername, supplierAccountPassword, tencentNow
	supplierAccountBaseURL, supplierAccountUsername, supplierAccountPassword = srv.URL, "portal-user", "portal-pass"
	tencentNow = func() time.Time { return time.Date(2026, 11, 19, 12, 0, 0, 0, billingExportZone) }
	supplierWeb.mu.Lock()
	supplierWeb.token, supplierWeb.expiry = "stub-token", time.Now().Add(time.Hour)
	supplierWeb.mu.Unlock()
	t.Cleanup(func() {
		supplierAccountBaseURL, supplierAccountUsername, supplierAccountPassword, tencentNow = prevURL, prevUser, prevPass, prevNow
		supplierWeb.invalidate()
	})
	return p
}

// tencentSeedOctober loads the schema, a stub portal, a stub upstream New API
// and synthetic October 2026 cost of every platform, with cost a day before
// and after the Beijing month.
func tencentSeedOctober(t *testing.T) (*tencentPortal, *tencentUpstream) {
	tencentTestDB(t)
	for _, ddl := range append(append([]string{}, v2SchemaStatements...), tencentSchemaStatements...) {
		_, err := db.Exec(ddl)
		require.NoError(t, err)
	}
	portal := newTencentPortal(t, []map[string]any{
		{"id": 1001, "provider": "anthropic", "account_alias": "anthropic-A-1", "api_key": "sk-ant-api03-xxxxtegAA", "model": "claude-opus-4-1"},
		{"id": 1002, "provider": "zhipu", "account_alias": "zhipu-1", "api_key": "sk-nuXXXXXXXXXXXX00002", "model": "glm-5.3-flash,glm-5.3"},
		{"id": 1003, "provider": "claude", "account_alias": "cc", "api_key": "x"},
		{"id": 1004, "provider": "zhipu", "account_alias": "zhipu-idle", "api_key": "zzzzzzzzz"},
	}, []map[string]any{
		{"account_id": 1001, "ds_text": "2026-09-30", "model": "claude-opus-4-1", "cost": 1000.0},
		{"account_id": 1001, "ds_text": "2026-10-01", "model": "claude-sonnet-4-5", "cost": 50.0},
		{"account_id": 1001, "ds_text": "2026-10-31", "model": "claude-opus-4-1", "cost": 27.0},
		{"account_id": 1001, "ds_text": "2026-11-01", "model": "claude-opus-4-1", "cost": 500.0},
		{"account_id": 1002, "ds_text": "2026-10-15", "model": "glm-5.3", "cost": 100.0},
		{"account_id": 1002, "ds_text": "2026-10-16", "model_name": "glm-5.3", "cost": 20.0},
		{"account_id": 1002, "ds_text": "2026-10-20", "cost": 10.0}, // no model: the account's first
		{"account_id": 1003, "ds_text": "2026-10-15", "model": "claude-opus-4-1", "cost": 999.0},
	})
	bj := func(y int, m time.Month, d, h int) int64 {
		return time.Date(y, m, d, h, 0, 0, 0, billingExportZone).Unix()
	}
	for _, s := range []string{
		// The record log is not a statement source.
		fmt.Sprintf(`INSERT INTO rs_supplier_record_log_hourly (platform, account_id, model, hour, cost) VALUES ('2.0', 501, 'gpt-5', %d, 9999e6)`, bj(2026, 10, 15, 12)),
		// Two CC Max accounts of different suppliers: one ConfluxCC row.
		fmt.Sprintf(`INSERT INTO rs_ccmax_account (uploaded_by, remote_account_id, alias, account, created_at) VALUES
		   (2, 2001, 'cc-alias', 'cc@example.com', %d), (5, 2002, 'cc-2', 'cc2@example.com', %d)`, bj(2026, 10, 23, 9), bj(2026, 10, 25, 9)),
		`INSERT INTO rs_ccmax_daily_bill (remote_account_id, ds, cost_micro, updated_at) VALUES
		   (2001, '2026-09-30', 100000000, 0), (2001, '2026-10-01', 35000000, 0), (2001, '2026-10-31', 7000000, 0), (2001, '2026-11-01', 9000000, 0),
		   (2002, '2026-10-12', 7000000, 0)`,
		// 2001's 10-31 bill has no models: it counts as the fallback model.
		`INSERT INTO rs_ccmax_daily_bill_model (remote_account_id, ds, model, cost_micro, updated_at) VALUES
		   (2001, '2026-10-01', 'claude-opus-4-1', 20000000, 0), (2001, '2026-10-01', 'claude-sonnet-4-5', 15000000, 0),
		   (2002, '2026-10-12', 'claude-haiku-4-5', 7000000, 0), (2002, '2026-10-12', 'claude-opus-4-1', 0, 0)`,
		`CREATE TABLE proxy_channel_ownership (remote_channel_id BIGINT PRIMARY KEY, user_id BIGINT NOT NULL, created_at BIGINT NOT NULL)`,
		`INSERT INTO proxy_channel_ownership VALUES (3001, 3, 0), (3002, 3, 0), (3003, 3, 0), (3004, 3, 0)`,
	} {
		_, err := db.Exec(s)
		require.NoError(t, err, "%.80s", s)
	}
	// Upstream: 250,000 quota per USD; #3001 is rate-limited twice and has
	// 1 USD outside its model list, #3003 fails, #3004 had no usage.
	prevWait := tencentProxyRetryWait
	tencentProxyRetryWait = time.Millisecond
	t.Cleanup(func() { tencentProxyRetryWait = prevWait })
	upstream := newTencentUpstream(t, 250000, map[int64]tencentUpstreamChannel{
		3001: {Name: "ch-a", Type: 3, Models: "gpt-5,gpt-6", Quota: 1_750_000, ModelQuota: map[string]int64{"gpt-5": 1_000_000, "gpt-6": 500_000}, Limited: 2},
		3002: {Name: "ch-b", Type: 3, Models: "gpt-5,claude-x", Quota: 1_000_000, ModelQuota: map[string]int64{"gpt-5": 500_000, "claude-x": 500_000}},
		3003: {Fail: true},
		3004: {Name: "ch-idle", Type: 1, Models: "gpt-5"},
	})
	return portal, upstream
}

type tencentUpstreamChannel struct {
	Name, Models string
	Type         int
	Quota        int64
	ModelQuota   map[string]int64
	Fail         bool
	// Limited answers 429 to that many log/stat calls before succeeding.
	Limited int
}

// tencentUpstream stubs newapi-proxy's upstream New API admin endpoints and
// records every request.
type tencentUpstream struct {
	requests []*http.Request
}

func newTencentUpstream(t *testing.T, quotaPerUnit float64, channels map[int64]tencentUpstreamChannel) *tencentUpstream {
	u := &tencentUpstream{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		u.requests = append(u.requests, r.Clone(context.Background()))
		mu.Unlock()
		if r.Header.Get("Authorization") != "upstream-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, _ := strconv.ParseInt(r.URL.Query().Get("channel"), 10, 64)
		if rest, ok := strings.CutPrefix(r.URL.Path, "/api/channel/"); ok {
			id, _ = strconv.ParseInt(rest, 10, 64)
		}
		mu.Lock()
		ch := channels[id]
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"quota_per_unit": quotaPerUnit}})
		case ch.Fail:
			http.Error(w, "boom", http.StatusBadGateway)
		case r.URL.Path == "/api/log/stat" && ch.Limited > 0:
			mu.Lock()
			ch.Limited--
			channels[id] = ch
			mu.Unlock()
			http.Error(w, "", http.StatusTooManyRequests)
		case r.URL.Path == "/api/log/stat":
			quota := ch.Quota
			if model := r.URL.Query().Get("model_name"); model != "" {
				quota = ch.ModelQuota[model]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"quota": quota}})
		case strings.HasPrefix(r.URL.Path, "/api/channel/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"name": ch.Name, "type": ch.Type, "models": ch.Models}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	// The upstream is a remote profile, as on dev (ttttt).
	enc, err := encryptRemoteToken("upstream-token")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO remote_newapi_profile (id, name, host, user_id, access_token_enc) VALUES (6, 'ttttt', $1, 1, $2)`, srv.URL, enc)
	require.NoError(t, err)
	require.NoError(t, supplierConfigSet(cfgTencentProxyProfileID, "6"))
	return u
}

// The three sources for exactly the Beijing month: proxy per vendor × model
// across channels, one ConfluxCC row for every CC Max account, 3P per
// account × model from the portal's bill of the non-claude roster.
func TestTencentLoadSources(t *testing.T) {
	portal, upstream := tencentSeedOctober(t)
	start, end, err := tencentMonthRange("2026-10")
	require.NoError(t, err)
	meta, lines, summary, warnings, err := tencentLiveStatement(start, end)
	require.NoError(t, err)
	require.Len(t, warnings, 3)
	assert.Equal(t, "proxy: 渠道 #3001 有 1.00 USD 不属于渠道当前模型列表，记为「其它模型」", warnings[0])
	assert.Regexp(t, `^proxy: 渠道 #3003 取数失败（.*502.*），未出账$`, warnings[1])
	assert.Equal(t, "厂商 anthropic 未配置折扣，暂按 100% 计算，请在账单参数里补上", warnings[2])
	assert.Equal(t, tencentDefaultMonthConfig(), meta.Config)
	assert.Equal(t, time.Date(2026, 10, 23, 9, 0, 0, 0, billingExportZone).Unix(), meta.CCMaxFirstAccount)

	// Every upstream call carries the profile's credentials; each stat covers
	// exactly the Beijing month.
	stats := []url.Values{}
	for _, r := range upstream.requests {
		assert.Equal(t, "upstream-token", r.Header.Get("Authorization"), r.URL.Path)
		assert.Equal(t, "1", r.Header.Get("New-Api-User"), r.URL.Path)
		if r.URL.Path == "/api/log/stat" {
			q := r.URL.Query()
			assert.Equal(t, []string{"2", "1790784000", "1793462400"}, []string{q.Get("type"), q.Get("start_timestamp"), q.Get("end_timestamp")})
			stats = append(stats, q)
		}
	}
	assert.Contains(t, stats, url.Values{"type": {"2"}, "channel": {"3002"}, "model_name": {"claude-x"},
		"start_timestamp": {"1790784000"}, "end_timestamp": {"1793462400"}})
	require.Len(t, portal.queries, 1)
	assert.Equal(t, "1001,1002,1004", portal.queries[0].Get("account_ids"), "claude accounts are CC Max's")

	type row struct {
		C, D, E, F, H string
		G, L          float64
	}
	got := []row{}
	for _, l := range lines {
		assert.Equal(t, "长河理想", l.Supplier)
		assert.Equal(t, 10, l.BillMonth)
		got = append(got, row{l.Vendor, l.Model, l.AccountID, l.Key, l.TaxRate, tencentRound(l.Official, 6), tencentRound(l.InvoiceRMB, 6)})
	}
	assert.Equal(t, []row{
		{"azure-claude", "claude-x", "渠道 3002", "", "含票", 2, tencentRound(2*0.95*6.6728, 6)},
		{"azure-openai", "gpt-5", "渠道 3001, 3002", "", "含票", 6, tencentRound(6*0.73*6.6728, 6)},
		{"azure-openai", "gpt-6", "渠道 3001", "", "含票", 2, tencentRound(2*0.73*6.6728, 6)},
		{"azure-openai", "其它模型", "渠道 3001", "", "含票", 1, tencentRound(0.73*6.6728, 6)},
		{"ConfluxCC", "Claude（CC Max 订阅）, claude-haiku-4-5, claude-opus-4-1, claude-sonnet-4-5", "全部CCMax账户", "", "含税",
			49, tencentRound(49/7.2*0.5*6.6728, 6)},
		{"anthropic", "claude-opus-4-1", "1001", "sk-an****tegAA", "不另加税费", 27, 27},
		{"anthropic", "claude-sonnet-4-5", "1001", "sk-an****tegAA", "不另加税费", 50, 50},
		{"zhipu", "glm-5.3", "1002", "sk-nu****00002", "不另加税费", 120, 90},
		{"zhipu", "glm-5.3-flash", "1002", "sk-nu****00002", "不另加税费", 10, 7.5},
	}, got)
	invoice := map[string]float64{}
	for _, s := range summary {
		invoice[s.Vendor] = s.InvoiceRMB
	}
	assert.Equal(t, map[string]float64{"azure-claude": 12.68, "azure-openai": 43.84, "ConfluxCC": 22.71, "anthropic": 77, "zhipu": 97.5}, invoice)
	assert.Equal(t, 253.73, tencentInvoiceTotal(summary))

	// The current month's bill runs to today; months beyond the portal's
	// retention say so.
	portal.queries = nil
	start, end, err = tencentMonthRange("2026-11")
	require.NoError(t, err)
	_, _, err = loadTencentThirdPartyRows(start, end, tencentNow())
	require.NoError(t, err)
	assert.Equal(t, "20261119", portal.queries[0].Get("end_ds"))
	start, end, err = tencentMonthRange("2026-08")
	require.NoError(t, err)
	_, warnings, err = loadTencentThirdPartyRows(start, end, tencentNow())
	require.NoError(t, err)
	assert.Equal(t, []string{"third_party: 门户只保留近 90 天账单，2026-08-21 之前的日期没有数据"}, warnings)
}

// A month uses its own config, else the latest earlier month's, else the
// built-in October one; the API validates a config before storing it and
// the statement warns when the BOC rate is not from the next month's 1st.
func TestTencentMonthConfig(t *testing.T) {
	tencentTestDB(t)
	for _, ddl := range append(append([]string{}, v2SchemaStatements...), tencentSchemaStatements...) {
		_, err := db.Exec(ddl)
		require.NoError(t, err)
	}
	r := tencentTestRouter()
	put := func(body string) (int, string) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/tencent/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	_, got := tencentDo(t, r, http.MethodGet, "/api/tencent/config?month=2026-10")
	assert.Equal(t, "default", got["from"])
	assert.Equal(t, "2026-11-01", got["default_rate_date"])

	nov := tencentDefaultMonthConfig()
	nov.BOCRate, nov.BOCRateDate = 7.1, "2026-12-01"
	nov.Vendors = append(nov.Vendors, tencentVendorConfig{Vendor: "minimax", Pricing: tencentPricingRMB, TaxText: "不另加税费"})
	raw, _ := json.Marshal(map[string]any{"month": "2026-11", "config": nov})
	code, body := put(string(raw))
	require.Equal(t, http.StatusOK, code, body)
	_, got = tencentDo(t, r, http.MethodGet, "/api/tencent/config?month=2027-01")
	assert.Equal(t, "inherit", got["from"])
	assert.Equal(t, "2026-11", got["inherited"])
	assert.Equal(t, 7.1, got["config"].(map[string]any)["boc_rate"])
	cfg, from, _, err := loadTencentMonthConfig(time.Date(2026, 11, 1, 0, 0, 0, 0, billingExportZone))
	require.NoError(t, err)
	assert.Equal(t, "month", from)
	assert.Equal(t, nov, cfg)

	for _, bad := range []string{
		`{"month":"2026-11","config":{"company":"","boc_rate":7,"boc_rate_date":"2026-12-01","ccmax_divisor":7.2}}`,
		`{"month":"2026-11","config":{"company":"X","boc_rate":0,"boc_rate_date":"2026-12-01","ccmax_divisor":7.2}}`,
		`{"month":"2026-11","config":{"company":"X","boc_rate":7,"boc_rate_date":"12/01","ccmax_divisor":7.2}}`,
		`{"month":"2026-11","config":{"company":"X","boc_rate":7,"boc_rate_date":"2026-12-01","ccmax_divisor":7.2,"vendors":[{"vendor":"a","pricing":"eur"}]}}`,
		`{"month":"2026-11","config":{"company":"X","boc_rate":7,"boc_rate_date":"2026-12-01","ccmax_divisor":7.2,"vendors":[{"vendor":"a","pricing":"rmb","discount":1.5}]}}`,
		`{"month":"2026-11","config":{"company":"X","boc_rate":7,"boc_rate_date":"2026-12-01","ccmax_divisor":7.2,"vendors":[{"vendor":"a","pricing":"rmb"},{"vendor":"a","pricing":"rmb"}]}}`,
	} {
		code, _ := put(bad)
		assert.Equal(t, http.StatusBadRequest, code, bad)
	}

	// January inherits November's rate date: the preview says so.
	start, end, err := tencentMonthRange("2027-01")
	require.NoError(t, err)
	_, _, _, warnings, err := tencentLiveStatement(start, end)
	require.NoError(t, err)
	assert.Contains(t, warnings, "中行汇率取价日期为 2026-12-01（沿用 2026-11 的配置），结算月次月 1 日为 2027-02-01，请核对汇率")
}

func tencentTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerTencentRoutes(r.Group("/api", func(c *gin.Context) { c.Set("role", minSuperAdminRole) }))
	return r
}

func tencentDo(t *testing.T, r *gin.Engine, method, path string) (int, map[string]any) {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	var body map[string]any
	if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	}
	return w.Code, body
}

func tencentSnapshotTotal(t *testing.T, r *gin.Engine) (string, float64) {
	code, body := tencentDo(t, r, http.MethodGet, "/api/tencent/statements/2026-10")
	require.Equal(t, http.StatusOK, code, body)
	return body["statement"].(map[string]any)["status"].(string), body["total_invoice_rmb"].(float64)
}

// 生成 → draft follows the source data on 重新生成 → 确认 freezes it: later
// source and config changes do not reach it and regenerating is refused.
func TestTencentSnapshotLifecycle(t *testing.T) {
	portal, _ := tencentSeedOctober(t)
	r := tencentTestRouter()

	code, _ := tencentDo(t, r, http.MethodPost, "/api/tencent/statements/2026-10/regenerate")
	assert.Equal(t, http.StatusNotFound, code, "regenerate before generate")
	code, _ = tencentDo(t, r, http.MethodPost, "/api/tencent/statements/2026-10/generate")
	require.Equal(t, http.StatusOK, code)
	status, total := tencentSnapshotTotal(t, r)
	assert.Equal(t, tencentStatementDraft, status)
	assert.Equal(t, 253.73, total)
	code, _ = tencentDo(t, r, http.MethodPost, "/api/tencent/statements/2026-10/generate")
	assert.Equal(t, http.StatusConflict, code, "generate twice")

	portal.bill = append(portal.bill, map[string]any{"account_id": 1002, "ds_text": "2026-10-20", "model": "glm-5.3", "cost": 100.0}) // +75 at 0.75
	_, total = tencentSnapshotTotal(t, r)
	assert.Equal(t, 253.73, total, "a draft only changes when regenerated")
	code, _ = tencentDo(t, r, http.MethodPost, "/api/tencent/statements/2026-10/regenerate")
	require.Equal(t, http.StatusOK, code)
	_, total = tencentSnapshotTotal(t, r)
	assert.Equal(t, 328.73, total)

	code, _ = tencentDo(t, r, http.MethodPost, "/api/tencent/statements/2026-10/confirm")
	require.Equal(t, http.StatusOK, code)
	code, _ = tencentDo(t, r, http.MethodPost, "/api/tencent/statements/2026-10/confirm")
	assert.Equal(t, http.StatusConflict, code, "confirm twice")

	portal.bill = append(portal.bill, map[string]any{"account_id": 1002, "ds_text": "2026-10-21", "cost": 700.0})
	cfg := tencentDefaultMonthConfig()
	cfg.BOCRate = 7.5
	raw, _ := json.Marshal(cfg)
	_, err := db.Exec(`INSERT INTO rs_tencent_month_config (month, config, updated_at) VALUES ('2026-10', $1, 0)`, string(raw))
	require.NoError(t, err)
	code, body := tencentDo(t, r, http.MethodPost, "/api/tencent/statements/2026-10/regenerate")
	assert.Equal(t, http.StatusConflict, code, body)
	status, total = tencentSnapshotTotal(t, r)
	assert.Equal(t, tencentStatementConfirmed, status)
	assert.Equal(t, 328.73, total, "a confirmed month ignores later source and config changes")

	// Exports read the snapshot and its config: the whole statement or one
	// vendor.
	for path, rows := range map[string]int{
		"/api/tencent/statements/2026-10/export":              10,
		"/api/tencent/statements/2026-10/export?vendor=zhipu": 3,
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, w.Code, path)
		f, err := excelize.OpenReader(bytes.NewReader(w.Body.Bytes()))
		require.NoError(t, err)
		got, err := f.GetRows(tencentLinesSheet)
		require.NoError(t, err)
		assert.Len(t, got, rows, path) // header + lines
		f.Close()
	}
	code, _ = tencentDo(t, r, http.MethodGet, "/api/tencent/statements/2026-10/export?vendor=nobody")
	assert.Equal(t, http.StatusNotFound, code)
}

// The October export is laid out like the statement settled with Tencent:
// sheet order, headers word for word, the parameter block, number formats,
// and formulas (I/K referring to the parameters, J/L, SUMIFS and the total)
// that evaluate to the golden amounts.
func TestTencentStatementWorkbook(t *testing.T) {
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, billingExportZone)
	lines, _, _ := buildTencentStatement(tencentOctoberSources(), tencentDefaultMonthConfig(), month)
	meta := tencentStatementMeta{Month: "2026-10", Config: tencentDefaultMonthConfig(),
		CCMaxFirstAccount: time.Date(2026, 10, 23, 15, 0, 0, 0, billingExportZone).Unix()}
	f, err := tencentStatementWorkbook(lines, meta)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, f.Write(&buf))
	f, err = excelize.OpenReader(&buf)
	require.NoError(t, err)
	defer f.Close()

	assert.Equal(t, []string{"汇总表", "API账单"}, f.GetSheetList())
	rows, err := f.GetRows("API账单")
	require.NoError(t, err)
	assert.Equal(t, []string{"供应商名称", "账单月份", "模型厂商名称", "模型名称", "账号ID", "key", "付给官方的费用（不区分币种）", "税率",
		"对应折扣", "腾讯支付金额（不区分币种）", "参考汇率\nhttps://www.kylc.com/huilv/d-boc-usd.html", "开票金额（RMB/元）", "官方导出的账单名称"}, rows[0])
	assert.Len(t, rows, 15)
	rows, err = f.GetRows("汇总表")
	require.NoError(t, err)
	assert.Equal(t, []string{"供应商名称", "账单月份", "模型厂商名称", "付给官方的费用（不区分币种）", "开票金额（RMB/元）", "备注"}, rows[0])

	value := func(sheet, cell string) string {
		v, err := f.GetCellValue(sheet, cell)
		require.NoError(t, err)
		return v
	}
	formula := func(sheet, cell string) string {
		v, err := f.GetCellFormula(sheet, cell)
		require.NoError(t, err)
		return v
	}
	calc := func(sheet, cell string) float64 {
		v, err := f.CalcCellValue(sheet, cell, excelize.Options{RawCellValue: true})
		require.NoError(t, err, cell)
		n, err := strconv.ParseFloat(v, 64)
		require.NoError(t, err, cell)
		return n
	}
	numFmt := func(sheet, cell string) string {
		id, err := f.GetCellStyle(sheet, cell)
		require.NoError(t, err)
		st, err := f.GetStyle(id)
		require.NoError(t, err)
		if st.CustomNumFmt != nil {
			return *st.CustomNumFmt
		}
		return strconv.Itoa(st.NumFmt)
	}

	// 汇总表: vendors, total, parameters and notes where the template has them.
	for cell, want := range map[string]string{
		"A2": "长河理想", "B2": "10", "C2": "azure-claude", "C3": "azure-openai", "C4": "ConfluxCC", "C5": "zhipu",
		"F2": "newapi proxy；原金额USD，已应用约定折扣", "F4": "CCMax；原RMB÷7.2×折扣×汇率，含税", "F5": "zhipu；人民币原价×折扣，不另加税费、不换汇",
		"A6": "开票金额合计", "A8": "2026-11-01 现汇卖出价", "D8": "中国银行美元现汇卖出价，人民币/1美元",
		"A9": "CCMax RMB/USD 除数", "D9": "CCMax原RMB金额 ÷ 7.2 × 折扣 × 本期汇率（含税）",
		"A10": "Azure OpenAI 折扣", "D10": "计费期间：2026-10-01 00:00至2026-11-01 00:00（北京时间，结束不含）",
		"A11": "Azure Claude 折扣", "D11": "计算保留原精度，金额显示2位；按来源及模型厂商汇总后四舍五入到分",
		"A12": "CCMax折扣（含税）", "D12": "zhipu 折扣", "F12": "人民币计价，不另加税费",
		"A13": "汇率来源：https://www.kylc.com/huilv/d-boc-usd.html（2026-11-01）",
		"A14": "账单期：2026年10月1日—10月31日（北京时间）。后台原始账单单独附送。",
		"A15": "CCMax：官方账户日账单，首次账户创建于10月23日；10月1日至22日无账户",
		"A16": "原始账单作为独立附件，本工作簿包含对账汇总与API账单明细",
	} {
		assert.Equal(t, want, value("汇总表", cell), cell)
	}
	for cell, want := range map[string]float64{"B8": 6.6728, "B9": 7.2, "B10": 0.73, "B11": 0.95, "B12": 0.5, "E12": 0.75} {
		assert.Equal(t, want, calc("汇总表", cell), cell)
	}
	assert.Equal(t, `SUMIFS('API账单'!$G$2:$G$15,'API账单'!$C$2:$C$15,C2)`, formula("汇总表", "D2"))
	assert.Equal(t, `ROUND(SUMIFS('API账单'!$L$2:$L$15,'API账单'!$C$2:$C$15,C2),2)`, formula("汇总表", "E2"))
	assert.Equal(t, `ROUND(SUM(E2:E5),2)`, formula("汇总表", "E6"))
	for cell, want := range map[string]float64{"E2": 4981.23, "E3": 6545.72, "E4": 802405.10, "E5": 159250.45, "E6": 973182.50} {
		assert.InDelta(t, want, calc("汇总表", cell), 1e-6, cell)
	}
	assert.Equal(t, tencentFmtUSD, numFmt("汇总表", "D2"))
	assert.Equal(t, tencentFmtRMB, numFmt("汇总表", "D4"))
	assert.Equal(t, tencentFmtRMB, numFmt("汇总表", "E6"))
	assert.Equal(t, "0.0000", numFmt("汇总表", "B8"))

	// API账单: the template's formula per pricing method.
	for cell, want := range map[string]string{
		"I2": `'汇总表'!$B$11`, "J2": "G2*I2", "K2": `'汇总表'!$B$8`, "L2": "J2*K2",
		"I5":  `'汇总表'!$B$10`,
		"I10": `'汇总表'!$B$12`, "J10": `G10/'汇总表'!$B$9*I10`, "K10": `'汇总表'!$B$8`, "L10": "J10*K10",
		"I11": `'汇总表'!$E$12`, "J11": "G11*I11", "L11": "J11*K11",
	} {
		assert.Equal(t, want, formula("API账单", cell), cell)
	}
	assert.Equal(t, 1.0, calc("API账单", "K11"))
	assert.Equal(t, []string{"ConfluxCC", "全部CCMax账户", "含税", "长河理想公司10月对账单_CCMax官方日明细.csv"},
		[]string{value("API账单", "C10"), value("API账单", "E10"), value("API账单", "H10"), value("API账单", "M10")})
	assert.Equal(t, "sk-nu****00002", value("API账单", "F11"))
	for cell, want := range map[string]string{
		"G2": tencentFmtUSD, "J2": tencentFmtUSD, "L2": tencentFmtRMB, "I2": "0%", "K2": "0.0000", "C2": "0_ ",
		"G10": tencentFmtRMB, "J10": tencentFmtUSD, "G11": tencentFmtRMB, "J11": tencentFmtRMB,
	} {
		assert.Equal(t, want, numFmt("API账单", cell), cell)
	}
	for i, l := range lines {
		assert.InDelta(t, l.InvoiceRMB, calc("API账单", fmt.Sprintf("L%d", i+2)), 1e-6, l.LineKey)
	}
}

// V2 supplier and CC Max roles are horizontal: their V1 gates match the exact
// tier, so the V1 role must be that tier, not the nearest lower one.
func TestLegacyV1RoleForLevel(t *testing.T) {
	cases := map[int]int{
		LevelSupplier01:           minSupplierRole,
		LevelSupplier02:           minSupplierRole02,
		LevelSupplierCCMax:        minSupplierCCMaxRole,
		LevelCCMaxManager:         minCCMaxManagerRole,
		LevelSupplierGynh:         minSupplierGynhRole,
		LevelStudioOperator:       minStudioOperatorRole,
		LevelRemoteStudioOperator: minRemoteStudioOperatorRole,
		LevelProjectAdmin:         minProjectAdminRole,
		LevelAdmin:                minAdminRole,
		LevelSuperadmin:           minSuperAdminRole,
		LevelTester:               minTesterRole,
		LevelUser:                 minUserRole,
	}
	for level, want := range cases {
		assert.Equal(t, want, legacyV1RoleForLevel(level), "level %d", level)
	}
}

// tencentAuthRouter mounts the real login, the tencent + V2 routes and the
// platform gates exactly as main() guards /api/supplier-account, /api/keyhub
// and /api/ccmax.
func tencentAuthRouter(t *testing.T) *gin.Engine {
	gin.SetMode(gin.TestMode)
	prevSecret, prevKey := jwtSecret, reportAPIKey
	jwtSecret, reportAPIKey = []byte("tencent-test-secret"), "tencent-test-key"
	t.Cleanup(func() { jwtSecret, reportAPIKey = prevSecret, prevKey })
	r := gin.New()
	r.POST("/api/login", handleLogin)
	api := r.Group("/api", authMiddleware)
	registerTencentRoutes(api)
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	api.GET("/supplier-account/ping", requireRoleOrSupplier(minAdminRole), ok)
	api.GET("/keyhub/ping", requireRoleOrSupplier02(minAdminRole), ok)
	api.GET("/ccmax/ping", requireRoleOrCCMaxRoles(minSuperAdminRole), ok)
	registerV2Routes(r)
	return r
}

func tencentAdminJSON(t *testing.T, r *gin.Engine, method, path, body string) (int, string) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "tencent-test-key")
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// tencentLoginGates logs in with report-service's own /api/login and returns
// which platform gates the session passes.
func tencentLoginGates(t *testing.T, r *gin.Engine, username, password string) map[string]int {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	cookies := w.Result().Cookies()
	require.NotEmpty(t, cookies)
	out := map[string]int{}
	for _, gate := range []string{"supplier-account", "keyhub", "ccmax"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/"+gate+"/ping", nil)
		req.AddCookie(cookies[0])
		r.ServeHTTP(w, req)
		out[gate] = w.Code
	}
	return out
}

// 统一新建用户: each platform role logs in and passes its platform's gate
// (proxy: the row newapi-proxy's login reads), the manager link is written,
// the generated password is returned once and never logged or listed.
func TestTencentUserCreate(t *testing.T) {
	tencentTestDB(t)
	for _, ddl := range append(append([]string{}, v2SchemaStatements...), tencentSchemaStatements...) {
		_, err := db.Exec(ddl)
		require.NoError(t, err)
	}
	seedV2RBAC()
	r := tencentAuthRouter(t)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	type created struct {
		ID       int64  `json:"id"`
		Password string `json:"password"`
	}
	create := func(body string) (int, created) {
		code, resp := tencentAdminJSON(t, r, http.MethodPost, "/api/tencent/users", body)
		var c created
		if code == http.StatusOK {
			require.NoError(t, json.Unmarshal([]byte(resp), &c))
		}
		return code, c
	}
	code, manager := create(`{"username":"mgr1","studio":"Mgr Co","role":"ccmax_manager"}`)
	require.Equal(t, http.StatusOK, code)

	users := map[string]created{}
	for _, tc := range []struct{ username, body string }{
		{"sup1", `{"username":"sup1","studio":"Studio A","role":"supplier_01"}`},
		{"cc1", fmt.Sprintf(`{"username":"cc1","studio":"CC Co","role":"supplier_ccmax","manager_id":%d}`, manager.ID)},
		{"px1", `{"username":"px1","studio":"Proxy Co","role":"proxy"}`},
	} {
		code, u := create(tc.body)
		require.Equal(t, http.StatusOK, code, tc.username)
		require.Len(t, u.Password, 20)
		users[tc.username] = u
	}
	users["mgr1"] = manager

	for name, body := range map[string]string{
		"duplicate username":        `{"username":"sup1","studio":"X","role":"supplier_01"}`,
		"studio required":           `{"username":"nostudio","studio":"  ","role":"supplier_01"}`,
		"unknown role":              `{"username":"bad1","studio":"X","role":"admin"}`,
		"KHub is not Tencent":       `{"username":"sup2","studio":"Studio B","role":"supplier_02"}`,
		"manager on non-CC Max":     fmt.Sprintf(`{"username":"bad2","studio":"X","role":"supplier_01","manager_id":%d}`, manager.ID),
		"manager must be a manager": fmt.Sprintf(`{"username":"bad3","studio":"X","role":"supplier_ccmax","manager_id":%d}`, users["sup1"].ID),
		"username charset / length": `{"username":"a b","studio":"X","role":"proxy"}`,
	} {
		code, _ := create(body)
		want := http.StatusBadRequest
		if name == "duplicate username" {
			want = http.StatusConflict
		}
		assert.Equal(t, want, code, name)
	}

	var memberOf int64
	require.NoError(t, db.QueryRow(`SELECT manager_id FROM rs_ccmax_manager_member WHERE member_id = $1`, users["cc1"].ID).Scan(&memberOf))
	assert.Equal(t, manager.ID, memberOf)

	forbidden, allowed := http.StatusForbidden, http.StatusOK
	for name, want := range map[string]map[string]int{
		"sup1": {"supplier-account": allowed, "keyhub": forbidden, "ccmax": forbidden},
		"cc1":  {"supplier-account": forbidden, "keyhub": forbidden, "ccmax": allowed},
		"mgr1": {"supplier-account": forbidden, "keyhub": forbidden, "ccmax": allowed},
		"px1":  {"supplier-account": forbidden, "keyhub": forbidden, "ccmax": forbidden},
	} {
		assert.Equal(t, want, tencentLoginGates(t, r, name, users[name].Password), name)
	}
	// newapi-proxy's login: the row by username, enabled, bcrypt match.
	var hash string
	var status int
	require.NoError(t, db.QueryRow(`SELECT password_hash, status FROM rs_auth_user WHERE username = 'px1'`).Scan(&hash, &status))
	assert.Equal(t, 1, status)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte(users["px1"].Password)))

	// The V2 role is bound with the user.
	var v2Role string
	require.NoError(t, db.QueryRow(`SELECT r.name FROM rs_user_role ur JOIN rs_role r ON r.id = ur.role_id WHERE ur.user_id = $1`, users["sup1"].ID).Scan(&v2Role))
	assert.Equal(t, RoleSupplier01, v2Role)

	// The list carries per-platform counts and never a password or hash.
	_, err := db.Exec(`INSERT INTO rs_supplier_account (uploaded_by, provider, remote_account_id, created_at, updated_at) VALUES ($1, 'openai', 9001, 0, 0)`, users["sup1"].ID)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE proxy_channel_ownership (remote_channel_id BIGINT PRIMARY KEY, user_id BIGINT NOT NULL, created_at BIGINT NOT NULL)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO proxy_channel_ownership VALUES (31, $1, 0), (32, $1, 0)`, users["px1"].ID)
	require.NoError(t, err)
	// A KHub (supplier_02) user made on KHub's own pages belongs to no
	// Tencent platform.
	_, err = db.Exec(`INSERT INTO rs_auth_user (username, role, studio) VALUES ('khub1', $1, 'KHub Co')`, minSupplierRole02)
	require.NoError(t, err)
	code, resp := tencentAdminJSON(t, r, http.MethodGet, "/api/tencent/users", "")
	require.Equal(t, http.StatusOK, code)
	var list struct {
		List []tencentUserItem `json:"list"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp), &list))
	byName := map[string]tencentUserItem{}
	for _, u := range list.List {
		byName[u.Username] = u
	}
	assert.Equal(t, int64(1), byName["sup1"].SupplierAccounts)
	assert.Equal(t, int64(2), byName["px1"].ProxyChannels)
	assert.Equal(t, "mgr1", byName["cc1"].ManagerUsername)
	assert.Equal(t, int64(1), byName["mgr1"].Members)
	assert.Equal(t, []string{tencentPlatformThirdParty}, byName["sup1"].Platforms)
	assert.Empty(t, byName["khub1"].Platforms)
	assert.Empty(t, byName["khub1"].RoleName)
	_, sup2Created := byName["sup2"]
	assert.False(t, sup2Created, "supplier_02 is refused")
	assert.NotContains(t, resp, "password")
	for _, u := range users {
		assert.NotContains(t, resp, u.Password)
		assert.NotContains(t, logs.String(), u.Password)
	}
	assert.Contains(t, logs.String(), "created supplier_01 user")

	code, resp = tencentAdminJSON(t, r, http.MethodGet, "/api/tencent/users?platform=proxy", "")
	require.Equal(t, http.StatusOK, code)
	require.NoError(t, json.Unmarshal([]byte(resp), &list))
	names := []string{}
	for _, u := range list.List {
		names = append(names, u.Username)
	}
	assert.Equal(t, []string{"px1"}, names)

	// V2 user creation: supplier_01 / supplier_02 now land on their own V1
	// tier and reach their platform.
	for roleName, gate := range map[string]string{RoleSupplier01: "supplier-account", RoleSupplier02: "keyhub"} {
		var roleID int64
		require.NoError(t, db.QueryRow(`SELECT id FROM rs_role WHERE name = $1`, roleName).Scan(&roleID))
		username := "v2-" + strings.ReplaceAll(roleName, "_", "-")
		code, resp := tencentAdminJSON(t, r, http.MethodPost, "/api/v2/users",
			fmt.Sprintf(`{"username":%q,"password":"Secret-123456","studio":"V2 Co","roles":[%d]}`, username, roleID))
		require.Equal(t, http.StatusOK, code, resp)
		assert.Equal(t, http.StatusOK, tencentLoginGates(t, r, username, "Secret-123456")[gate], roleName)
	}
}

// tencentLarkStub records card pushes and answers like Lark, or with an
// HTTP error while fail is set.
type tencentLarkStub struct {
	srv   *httptest.Server
	cards []map[string]any
	fail  bool
}

func newTencentLarkStub(t *testing.T) *tencentLarkStub {
	s := &tencentLarkStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var card map[string]any
		_ = json.NewDecoder(r.Body).Decode(&card)
		s.cards = append(s.cards, card)
		if s.fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok"}`))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// The card carries the snapshot's per supplier × platform totals and the
// export link; a failed send is recorded; the schedule pushes last month once
// (the claim survives restarts) and not at all when switched off.
func TestTencentStatementPush(t *testing.T) {
	tencentSeedOctober(t)
	r := tencentTestRouter()
	lark := newTencentLarkStub(t)
	require.NoError(t, supplierConfigSet(cfgTencentPushWebhook, lark.srv.URL))
	require.NoError(t, supplierConfigSet(cfgTencentPublicURL, "https://rs.example"))

	// No snapshot yet: the push generates the draft it links to.
	code, body := tencentPushJSON(t, r, `{"month":"2026-10"}`)
	require.Equal(t, http.StatusOK, code, body)
	require.Len(t, lark.cards, 1)
	card := lark.cards[0]["card"].(map[string]any)
	assert.Equal(t, "📑 腾讯对账单 · 2026-10", card["header"].(map[string]any)["title"].(map[string]any)["content"])

	_, snap := tencentDo(t, r, http.MethodGet, "/api/tencent/statements/2026-10")
	assert.Equal(t, tencentStatementDraft, snap["statement"].(map[string]any)["status"])
	type total struct {
		Lines    int
		Official string
		RMB      float64
	}
	got := map[string]total{}
	var button, headline string
	for _, raw := range card["elements"].([]any) {
		el := raw.(map[string]any)
		switch el["tag"] {
		case "markdown":
			headline = el["content"].(string)
		case "table":
			for _, rr := range el["rows"].([]any) {
				row := rr.(map[string]any)
				got[row["vendor"].(string)] = total{int(row["lines"].(float64)), row["official"].(string), row["rmb"].(float64)}
			}
		case "action":
			button = el["actions"].([]any)[0].(map[string]any)["url"].(string)
		}
	}
	// The card's per-vendor totals are the snapshot's 汇总表.
	want := map[string]total{}
	for _, raw := range snap["summary"].([]any) {
		s := raw.(map[string]any)
		cur := "RMB"
		if s["pricing"] == tencentPricingUSD {
			cur = "USD"
		}
		want[s["vendor"].(string)] = total{int(s["lines"].(float64)), fmt.Sprintf("%.2f %s", s["official"].(float64), cur), s["invoice_rmb"].(float64)}
	}
	assert.Equal(t, want, got)
	assert.Equal(t, map[string]total{
		"azure-claude": {1, "2.00 USD", 12.68},
		"azure-openai": {3, "9.00 USD", 43.84},
		"ConfluxCC":    {1, "49.00 RMB", 22.71},
		"anthropic":    {2, "77.00 RMB", 77},
		"zhipu":        {2, "130.00 RMB", 97.5},
	}, got)
	assert.Contains(t, headline, "开票金额合计 **¥253.73**")
	assert.Equal(t, "https://rs.example/api/tencent/statements/2026-10/export", button)

	lark.fail = true
	code, body = tencentPushJSON(t, r, `{"month":"2026-10"}`)
	assert.Equal(t, http.StatusFailedDependency, code, body)
	_, logs := tencentDo(t, r, http.MethodGet, "/api/tencent/push-logs?kind=statement")
	latest := logs["list"].([]any)[0].(map[string]any)
	assert.Equal(t, false, latest["ok"])
	assert.Equal(t, "manual", latest["trigger"])
	assert.Equal(t, "2026-10", latest["month"])
	assert.Contains(t, latest["error"], "lark HTTP 500")
	assert.Len(t, logs["list"], 2)
	lark.fail = false

	// Schedule: day 2 at 10:00 Beijing time by default.
	bj := func(m time.Month, d, h, min int) time.Time {
		return time.Date(2026, m, d, h, min, 0, 0, billingExportZone)
	}
	sent := len(lark.cards)
	claimed, err := tencentStatementScheduleTick(bj(11, 2, 9, 59))
	require.NoError(t, err)
	assert.False(t, claimed, "before the hour")
	for _, at := range []time.Time{bj(11, 2, 10, 0), bj(11, 2, 11, 0), bj(11, 20, 3, 0)} {
		claimed, err = tencentStatementScheduleTick(at)
		require.NoError(t, err)
		assert.Equal(t, at.Equal(bj(11, 2, 10, 0)), claimed, at.String())
	}
	assert.Len(t, lark.cards, sent+1, "October goes out once")
	assert.Equal(t, "2026-10", supplierConfigGet(cfgTencentPushMonth))

	require.NoError(t, supplierConfigSet(cfgTencentPushEnabled, "0"))
	claimed, err = tencentStatementScheduleTick(bj(12, 2, 10, 0))
	require.NoError(t, err)
	assert.False(t, claimed, "switched off")
	assert.Len(t, lark.cards, sent+1)
	assert.Equal(t, "2026-10", supplierConfigGet(cfgTencentPushMonth))
	require.NoError(t, supplierConfigSet(cfgTencentPushEnabled, "1"))
	claimed, err = tencentStatementScheduleTick(bj(12, 3, 8, 0))
	require.NoError(t, err)
	assert.True(t, claimed, "a late check still sends the month")
	assert.Equal(t, "2026-11", supplierConfigGet(cfgTencentPushMonth))
	_, logs = tencentDo(t, r, http.MethodGet, "/api/tencent/push-logs")
	assert.Equal(t, "schedule", logs["list"].([]any)[0].(map[string]any)["trigger"])
}

func tencentPushJSON(t *testing.T, r *gin.Engine, body string) (int, string) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tencent/statement-push", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// /push-settings writes the keys the owning pages read and shows what they
// write.
func TestTencentPushSettingsInterop(t *testing.T) {
	tencentTestDB(t)
	for _, ddl := range append(append([]string{}, v2SchemaStatements...), tencentSchemaStatements...) {
		_, err := db.Exec(ddl)
		require.NoError(t, err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api", func(c *gin.Context) { c.Set("role", minSuperAdminRole) })
	registerTencentRoutes(api)
	api.GET("/ccmax/report/settings", handleCCMaxReportSettingsGet)
	api.PUT("/ccmax/report/settings", handleCCMaxReportSettingsSet)
	api.GET("/proxy-report/settings", handleProxyReportSettingsGet)
	api.PUT("/proxy-report/settings", handleProxyReportSettingsSet)
	api.GET("/ops-notify/config", handleOpsNotifyConfigGet)
	api.POST("/ops-notify/config", handleOpsNotifyConfigSet)
	api.GET("/supplier-account/settings", handleSupplierSettingsGet)
	call := func(method, path, body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	code, got := call(http.MethodPut, "/api/tencent/push-settings", `{
		"statement": {"webhook": "https://lark.example/stmt", "enabled": false, "day": 5, "hour": 9, "base_url": "https://rs.example/"},
		"supplier_quota_webhook": "https://lark.example/quota",
		"ccmax_report": {"webhook": "https://lark.example/ccmax", "hours": "9, 21"},
		"proxy_report": {"webhook": "https://lark.example/proxy", "hours": "8"},
		"ops_daily_spend": {"enabled": true, "webhook": "https://lark.example/ops", "daily_spend": true, "daily_spend_hour": 7}}`)
	require.Equal(t, http.StatusOK, code, got)
	assert.Equal(t, map[string]any{"webhook": "https://lark.example/stmt", "enabled": false, "day": 5.0, "hour": 9.0, "base_url": "https://rs.example"}, got["statement"])
	assert.Equal(t, map[string]any{"webhook_set": true, "webhook_last4": "uota"}, got["supplier_quota"])

	_, ccmax := call(http.MethodGet, "/api/ccmax/report/settings", "")
	assert.Equal(t, "https://lark.example/ccmax", ccmax["webhook"])
	assert.Equal(t, "9,21", ccmax["hours"])
	_, proxy := call(http.MethodGet, "/api/proxy-report/settings", "")
	assert.Equal(t, "https://lark.example/proxy", proxy["webhook"])
	assert.Equal(t, "8", proxy["hours"])
	_, ops := call(http.MethodGet, "/api/ops-notify/config", "")
	assert.Equal(t, true, ops["daily_spend"])
	assert.Equal(t, 7.0, ops["daily_spend_hour"])
	assert.Equal(t, "https://lark.example/ops", ops["webhook"])
	assert.Equal(t, true, ops["uploads"], "other ops fields keep their values")
	_, supplier := call(http.MethodGet, "/api/supplier-account/settings", "")
	assert.Equal(t, "uota", supplier["quota_webhook_last4"])

	// Changes made on the owning pages show up here.
	code, _ = call(http.MethodPut, "/api/ccmax/report/settings", `{"hours":"10"}`)
	require.Equal(t, http.StatusOK, code)
	code, _ = call(http.MethodPut, "/api/proxy-report/settings", `{"webhook":""}`)
	require.Equal(t, http.StatusOK, code)
	code, _ = call(http.MethodPost, "/api/ops-notify/config", `{"daily_spend_hour":11}`)
	require.Equal(t, http.StatusOK, code)
	_, got = call(http.MethodGet, "/api/tencent/push-settings", "")
	assert.Equal(t, map[string]any{"webhook": "https://lark.example/ccmax", "hours": "10"}, got["ccmax_report"])
	assert.Equal(t, map[string]any{"webhook": "", "hours": "8"}, got["proxy_report"])
	assert.Equal(t, 11.0, got["ops_daily_spend"].(map[string]any)["daily_spend_hour"])

	// One invalid field rejects the request before anything is written.
	for _, bad := range []string{
		`{"statement": {"webhook": "https://lark.example/new", "day": 29}}`,
		`{"statement": {"webhook": "https://lark.example/new"}, "ccmax_report": {"webhook": "http://insecure.example"}}`,
		`{"statement": {"webhook": "https://lark.example/new"}, "proxy_report": {"hours": "25"}}`,
	} {
		code, _ := call(http.MethodPut, "/api/tencent/push-settings", bad)
		assert.Equal(t, http.StatusBadRequest, code, bad)
	}
	assert.Equal(t, "https://lark.example/stmt", loadTencentPushSchedule().Webhook)
}

// The proxy upstream is picked among the remote profiles; a seller-mode
// profile has no log API and is refused.
func TestTencentProxyProfileSetting(t *testing.T) {
	tencentTestDB(t)
	_, err := db.Exec(`INSERT INTO remote_newapi_profile (id, name, host, api_mode) VALUES (6, 'ttttt', 'http://a', 'admin'), (9, 's', 'http://b', 'seller')`)
	require.NoError(t, err)
	r := tencentTestRouter()
	put := func(body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/tencent/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, got := put(`{"proxy_profile_id":6}`)
	require.Equal(t, http.StatusOK, code, got)
	assert.Equal(t, 6.0, got["proxy_profile_id"])
	for body, want := range map[string]int{`{"proxy_profile_id":9}`: http.StatusBadRequest, `{"proxy_profile_id":99}`: http.StatusBadRequest} {
		code, _ := put(body)
		assert.Equal(t, want, code, body)
	}
	code, got = put(`{"proxy_profile_id":0}`)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, 0.0, got["proxy_profile_id"])
}

// tencentFakeSources replaces the live source read for a test with one that
// counts its calls and returns rows (blocking until release, when given).
func tencentFakeSources(t *testing.T, rows []tencentSourceRow, release chan struct{}) *atomic.Int32 {
	calls := &atomic.Int32{}
	prev := tencentLoadSourcesFn
	tencentLoadSourcesFn = func(start, end time.Time) ([]tencentSourceRow, int64, []string, error) {
		calls.Add(1)
		if release != nil {
			<-release
		}
		return rows, 0, []string{"fake source"}, nil
	}
	t.Cleanup(func() { tencentLoadSourcesFn = prev })
	return calls
}

// The preview prices the cached sources with the current config without
// reading the sources live, and matches the live statement after a config
// change.
func TestTencentSourceCachePreview(t *testing.T) {
	tencentSeedOctober(t)
	r := tencentTestRouter()
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, billingExportZone)
	ran, err := tencentRunSourceCompute(month)
	require.NoError(t, err)
	require.True(t, ran)

	cfg := tencentDefaultMonthConfig()
	cfg.BOCRate = 7.1
	cfg.Vendors[3].Discount = tencentFloatPtr(0.5) // zhipu
	raw, _ := json.Marshal(map[string]any{"month": "2026-10", "config": cfg})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/tencent/config", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	real := tencentLoadSourcesFn
	calls := tencentFakeSources(t, nil, nil)
	code, cached := tencentDo(t, r, http.MethodGet, "/api/tencent/statement?month=2026-10")
	require.Equal(t, http.StatusOK, code, cached)
	assert.Equal(t, int32(0), calls.Load(), "a cached month does not read the sources")
	assert.Equal(t, "cache", cached["source"])
	assert.Equal(t, false, cached["computing"])
	assert.Positive(t, cached["computed_at"].(float64))

	tencentLoadSourcesFn = real
	code, live := tencentDo(t, r, http.MethodGet, "/api/tencent/statement?month=2026-10&live=1")
	require.Equal(t, http.StatusOK, code, live)
	assert.Equal(t, "live", live["source"])
	require.NotEmpty(t, live["lines"])
	for _, k := range []string{"lines", "summary", "total_invoice_rmb", "config"} {
		assert.Equal(t, live[k], cached[k], k)
	}
	assert.Equal(t, 97.5*0.5/0.75, cached["summary"].([]any)[4].(map[string]any)["invoice_rmb"], "zhipu at the new discount")
}

// A month without a cache starts one background computation, however often
// it is asked for, and answers computing until it is stored; a recompute is
// refused while one runs.
func TestTencentSourceCacheBackground(t *testing.T) {
	tencentSeedOctober(t)
	r := tencentTestRouter()
	release := make(chan struct{})
	calls := tencentFakeSources(t, []tencentSourceRow{
		{Platform: tencentPlatformThirdParty, Vendor: "zhipu", Model: "glm-5.3", Account: "1", Cost: 100},
	}, release)

	for range 3 {
		code, body := tencentDo(t, r, http.MethodGet, "/api/tencent/statement?month=2026-10")
		require.Equal(t, http.StatusOK, code, body)
		assert.Equal(t, true, body["computing"])
		assert.Equal(t, float64(0), body["computed_at"])
		assert.Empty(t, body["lines"])
	}
	code, body := tencentDo(t, r, http.MethodPost, "/api/tencent/statement/recompute?month=2026-10")
	assert.Equal(t, http.StatusAccepted, code)
	assert.Equal(t, false, body["started"], "already computing")
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	close(release)

	require.Eventually(t, func() bool {
		_, body := tencentDo(t, r, http.MethodGet, "/api/tencent/statement?month=2026-10")
		return body["computing"] == false && len(body["lines"].([]any)) == 1
	}, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, int32(1), calls.Load(), "computed once")
	_, body = tencentDo(t, r, http.MethodGet, "/api/tencent/statement?month=2026-10")
	assert.Equal(t, 75.0, body["total_invoice_rmb"])
	assert.Contains(t, body["warnings"], "fake source")

	code, body = tencentDo(t, r, http.MethodPost, "/api/tencent/statement/recompute?month=2026-10")
	assert.Equal(t, http.StatusAccepted, code)
	assert.Equal(t, true, body["started"])
	require.Eventually(t, func() bool {
		cache, err := tencentLoadSourceCache(time.Date(2026, 10, 1, 0, 0, 0, 0, billingExportZone))
		return err == nil && calls.Load() == 2 && !cache.computing(time.Now())
	}, 5*time.Second, 10*time.Millisecond)
}

// The daily refresh runs once a day from its time on: last month and this
// month while last month's bills settle, then this month only.
func TestTencentSourceCacheDaily(t *testing.T) {
	tencentSeedOctober(t)
	var months []string
	prev := tencentLoadSourcesFn
	tencentLoadSourcesFn = func(start, end time.Time) ([]tencentSourceRow, int64, []string, error) {
		months = append(months, start.Format("2006-01"))
		return nil, 0, nil, nil
	}
	t.Cleanup(func() { tencentLoadSourcesFn = prev })
	at := func(m time.Month, d, h, min int) time.Time {
		return time.Date(2026, m, d, h, min, 0, 0, billingExportZone)
	}

	ran, err := tencentSourceCacheTick(at(11, 3, 3, 59))
	require.NoError(t, err)
	assert.False(t, ran, "before 04:00")
	ran, err = tencentSourceCacheTick(at(11, 3, 4, 0))
	require.NoError(t, err)
	assert.True(t, ran)
	assert.Equal(t, []string{"2026-10", "2026-11"}, months)
	ran, err = tencentSourceCacheTick(at(11, 3, 23, 0))
	require.NoError(t, err)
	assert.False(t, ran, "once a day")

	// After the settling days last month is computed once more (its cache is
	// older than the 6th), then left alone.
	months = nil
	_, err = db.Exec(`UPDATE rs_tencent_source_cache SET computed_at = $1 WHERE month = '2026-10'`, at(11, 3, 4, 0).Unix())
	require.NoError(t, err)
	due, err := tencentSourceCacheMonthsDue(at(11, 20, 4, 0))
	require.NoError(t, err)
	assert.Len(t, due, 2)
	_, err = db.Exec(`UPDATE rs_tencent_source_cache SET computed_at = $1 WHERE month = '2026-10'`, at(11, 6, 4, 0).Unix())
	require.NoError(t, err)
	due, err = tencentSourceCacheMonthsDue(at(11, 20, 4, 0))
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "2026-11", due[0].Format("2006-01"))

	require.NoError(t, supplierConfigSet(cfgTencentSourceCacheTime, "off"))
	ran, err = tencentSourceCacheTick(at(11, 21, 12, 0))
	require.NoError(t, err)
	assert.False(t, ran, "switched off")
}
