package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tencentFloat(v float64) *float64 { return &v }

func tencentLinesByKey(lines []tencentLine) map[string]tencentLine {
	out := make(map[string]tencentLine, len(lines))
	for _, l := range lines {
		out[l.LineKey] = l
	}
	return out
}

// The template's amount columns: G = CNY ÷ month rate (USD-native rows as
// is), J = ROUND(G×I, 2), L = ROUND(J×K, 2) or L = J for an RMB-priced row;
// I falls back supplier×platform → supplier → upload discount → 100%.
func TestTencentStatementAmounts(t *testing.T) {
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, billingExportZone)
	upload := 88.0
	rows := []tencentSourceRow{
		// Two halves of one account × model merge into one row.
		{Platform: tencentPlatformThirdParty, UserID: 1, Username: "alice", Studio: "Studio A", AccountID: 1001, KeyLast8: "abcd1234", Provider: "openai", Model: "gpt-5", CostCNY: 50, UploadDiscountPct: &upload},
		{Platform: tencentPlatformThirdParty, UserID: 1, Username: "alice", Studio: "Studio A", AccountID: 1001, KeyLast8: "abcd1234", Provider: "openai", Model: "gpt-5", CostCNY: 27, UploadDiscountPct: &upload},
		{Platform: tencentPlatformThirdParty, UserID: 1, Username: "alice", Studio: "Studio A", AccountID: 1002, Provider: "aws", Model: "claude-sonnet-5-5", CostCNY: 14},
		{Platform: tencentPlatformCCMax, UserID: 2, Username: "bob", AccountID: 2001, Provider: "claude", Model: "claude-opus-5-5", CostCNY: 20},
		{Platform: tencentPlatformCCMax, UserID: 2, Username: "bob", AccountID: 2001, Provider: "claude", CostCNY: 7},
		{Platform: tencentPlatformProxy, UserID: 3, Username: "carol", Studio: "Proxy Co", AccountID: 3001, Provider: "gpt-5", Model: "gpt-5", CostUSD: 7},
	}
	params := tencentStatementParams{
		Month:     month,
		FxRate:    7,
		RefFxRate: 7.25,
		Suppliers: map[tencentSupplierParamKey]tencentSupplierParam{
			{2, tencentPlatformCCMax}: {DiscountPct: tencentFloat(90)},
			{2, tencentPlatformAll}:   {DiscountPct: tencentFloat(50), TaxRate: "3%"},
			{3, tencentPlatformAll}:   {TaxRate: "6%"},
		},
		Flags: map[string]tencentLineFlag{
			"2026-10|third_party|1002|claude-sonnet-5-5": {RMBPriced: true, BillName: "official-oct.csv"},
		},
	}
	lines, summary := buildTencentStatement(rows, params)
	require.Len(t, lines, 5)
	got := tencentLinesByKey(lines)

	l := got["2026-10|third_party|1001|gpt-5"]
	assert.Equal(t, tencentLine{
		LineKey: "2026-10|third_party|1001|gpt-5", Platform: tencentPlatformThirdParty, UserID: 1,
		Supplier: "Studio A", BillMonth: 10, Vendor: "OpenAI", Model: "gpt-5", AccountID: "1001", Key: "****abcd1234",
		OfficialUSD: 11, Discount: 0.88, TencentUSD: 9.68, RefFxRate: 7.25, InvoiceRMB: 70.18,
		Source: "third_party - alice", CostCNY: 77, DiscountFrom: "upload",
	}, l)

	// RMB-priced: G stays CNY, L = J; no upload discount → 100%.
	l = got["2026-10|third_party|1002|claude-sonnet-5-5"]
	assert.Equal(t, 14.0, l.OfficialUSD)
	assert.Equal(t, 1.0, l.Discount)
	assert.Equal(t, "default", l.DiscountFrom)
	assert.Equal(t, 14.0, l.TencentUSD)
	assert.Equal(t, 14.0, l.InvoiceRMB)
	assert.Equal(t, "official-oct.csv", l.BillName)
	assert.Equal(t, "Anthropic", l.Vendor)
	assert.Empty(t, l.Key)

	// The platform row beats the supplier-wide row; tax comes from '*'.
	l = got["2026-10|ccmax|2001|claude-opus-5-5"]
	assert.Equal(t, "bob", l.Supplier)
	assert.Equal(t, 2.86, l.OfficialUSD) // 20 / 7
	assert.Equal(t, 2.57, l.TencentUSD)  // ROUND(2.86 × 0.9, 2)
	assert.Equal(t, 18.63, l.InvoiceRMB) // ROUND(2.57 × 7.25, 2)
	assert.Equal(t, "3%", l.TaxRate)
	assert.Equal(t, "supplier_platform", l.DiscountFrom)
	assert.Equal(t, "ccmax - bob", l.Source)
	assert.Empty(t, l.Key)

	l = got["2026-10|ccmax|2001|"+tencentCCMaxFallbackModel]
	assert.Equal(t, 1.0, l.OfficialUSD)
	assert.Equal(t, 0.9, l.TencentUSD)
	assert.Equal(t, 6.53, l.InvoiceRMB) // 6.525 rounds half up
	assert.Equal(t, "Anthropic", l.Vendor)

	// USD-native proxy cost is G as is.
	l = got["2026-10|proxy|3001|gpt-5"]
	assert.Equal(t, 7.0, l.OfficialUSD)
	assert.Equal(t, 7.0, l.TencentUSD)
	assert.Equal(t, 50.75, l.InvoiceRMB)
	assert.Equal(t, "6%", l.TaxRate)
	assert.Equal(t, "newapi proxy - carol", l.Source)
	assert.Equal(t, "Proxy Co", l.Supplier)

	assert.Equal(t, []tencentSummaryRow{
		{Supplier: "Proxy Co", BillMonth: 10, Vendor: "OpenAI", Source: "newapi proxy", OfficialUSD: 7, InvoiceRMB: 50.75, Lines: 1},
		{Supplier: "Studio A", BillMonth: 10, Vendor: "Anthropic", Source: "third_party", OfficialUSD: 14, InvoiceRMB: 14, Lines: 1},
		{Supplier: "Studio A", BillMonth: 10, Vendor: "OpenAI", Source: "third_party", OfficialUSD: 11, InvoiceRMB: 70.18, Lines: 1},
		{Supplier: "bob", BillMonth: 10, Vendor: "Anthropic", Source: "ccmax", OfficialUSD: 3.86, InvoiceRMB: 25.16, Lines: 2},
	}, summary)
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

func TestTencentVendor(t *testing.T) {
	custom := map[string]string{"gpt-5-codex": "OpenAI Codex", "foo": "Foo Inc"}
	cases := []struct {
		model, provider, want string
	}{
		{"claude-opus-5-5", "aws", "Anthropic"},
		{"gpt-5", "azure", "OpenAI"},
		{"o3-mini", "", "OpenAI"},
		{"gemini-3-pro", "vertex", "Google"},
		{"deepseek-v4", "", "DeepSeek"},
		{"unknown-model", "bedrock", "Anthropic"},
		{"gpt-5-codex", "", "OpenAI Codex"},
		{"my-foo-model", "", "Foo Inc"},
		{"mystery", "SomeCloud", "somecloud"},
		{"", "", "未知"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, tencentVendor(tc.model, tc.provider, custom), "%s/%s", tc.model, tc.provider)
	}
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
		for _, path := range []string{"/api/tencent/statement?month=bad", "/api/tencent/line-flags?month=bad"} {
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
		   role INT NOT NULL DEFAULT 1, studio TEXT NOT NULL DEFAULT '', created_at BIGINT NOT NULL DEFAULT 0, updated_at BIGINT NOT NULL DEFAULT 0)`,
		`CREATE TABLE report_config (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at BIGINT NOT NULL)`,
		`CREATE TABLE remote_newapi_profile (id BIGSERIAL PRIMARY KEY)`,
		`CREATE TABLE remote_channel_snapshot (profile_id BIGINT NOT NULL, remote_channel_id BIGINT NOT NULL, captured_at BIGINT NOT NULL,
		   used_quota BIGINT NOT NULL, status INT NOT NULL, PRIMARY KEY (profile_id, remote_channel_id, captured_at))`,
		`CREATE TABLE remote_channel_current (profile_id BIGINT NOT NULL, remote_channel_id BIGINT NOT NULL, name TEXT NOT NULL DEFAULT '',
		   models TEXT NOT NULL DEFAULT '', updated_at BIGINT NOT NULL DEFAULT 0, PRIMARY KEY (profile_id, remote_channel_id))`,
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

// The three sources are read for exactly the Beijing month: usage an hour
// before or after it stays out.
func TestTencentLoadSources(t *testing.T) {
	tencentTestDB(t)
	for _, ddl := range append(append([]string{}, v2SchemaStatements...), tencentSchemaStatements...) {
		_, err := db.Exec(ddl)
		require.NoError(t, err)
	}
	bj := func(y int, m time.Month, d, h int) int64 {
		return time.Date(y, m, d, h, 0, 0, 0, billingExportZone).Unix()
	}
	for _, s := range []string{
		`INSERT INTO rs_auth_user (id, username, studio) VALUES (1, 'alice', 'Studio A'), (2, 'bob', ''), (3, 'carol', 'Proxy Co')`,
		`INSERT INTO remote_newapi_profile (id) VALUES (7)`,
		`INSERT INTO rs_supplier_account (uploaded_by, provider, remote_account_id, key_last8, account_type, a2_id, created_at, updated_at)
		   VALUES (1, 'openai', 1001, 'abcd1234', 0, 501, 0, 0)`,
		`INSERT INTO rs_supplier_account (uploaded_by, provider, remote_account_id, account_type, a1_id, created_at, updated_at)
		   VALUES (1, 'aws', 1002, 1, 601, 0, 0)`,
		fmt.Sprintf(`INSERT INTO rs_supplier_record_log_hourly (platform, account_id, model, hour, cost) VALUES
		   ('2.0', 501, 'gpt-5', %d, 1000e6), ('2.0', 501, 'gpt-5', %d, 70e6), ('2.0', 501, 'gpt-5', %d, 7e6),
		   ('2.0', 501, 'gpt-5', %d, 500e6), ('1.0', 601, 'claude-sonnet-5-5', %d, 14e6), ('2.0', 999, 'gpt-5', %d, 3e6)`,
			bj(2026, 9, 30, 23), bj(2026, 10, 1, 0), bj(2026, 10, 31, 23), bj(2026, 11, 1, 0), bj(2026, 10, 15, 12), bj(2026, 10, 15, 12)),
		`INSERT INTO rs_ccmax_account (uploaded_by, remote_account_id, created_at) VALUES (2, 2001, 0)`,
		`INSERT INTO rs_ccmax_daily_bill (remote_account_id, ds, cost_micro, updated_at) VALUES
		   (2001, '2026-09-30', 100000000, 0), (2001, '2026-10-01', 35000000, 0), (2001, '2026-10-31', 7000000, 0), (2001, '2026-11-01', 9000000, 0)`,
		`INSERT INTO rs_ccmax_daily_bill_model (remote_account_id, ds, model, cost_micro, updated_at) VALUES
		   (2001, '2026-10-01', 'claude-opus-5-5', 20000000, 0), (2001, '2026-10-01', 'claude-sonnet-5-5', 15000000, 0)`,
		`CREATE TABLE proxy_channel_ownership (remote_channel_id BIGINT PRIMARY KEY, user_id BIGINT NOT NULL, created_at BIGINT NOT NULL)`,
		`INSERT INTO proxy_channel_ownership VALUES (3001, 3, 0), (3002, 3, 0)`,
		`INSERT INTO remote_channel_current (profile_id, remote_channel_id, name, models) VALUES (7, 3001, 'ch-a', 'gpt-5'), (7, 3002, 'ch-b', 'gpt-5,claude-x')`,
		fmt.Sprintf(`INSERT INTO remote_channel_snapshot (profile_id, remote_channel_id, captured_at, used_quota, status) VALUES
		   (7, 3001, %d, 1000000, 1), (7, 3001, %d, 4500000, 1), (7, 3002, %d, 1000000, 1), (7, 3001, %d, 99000000, 1)`,
			bj(2026, 9, 30, 12), bj(2026, 10, 31, 20), bj(2026, 10, 31, 20), bj(2026, 11, 1, 1)),
		`INSERT INTO report_config (key, value, updated_at) VALUES ('tencent_proxy_profile_id', '7', 0)`,
		`INSERT INTO rs_tencent_month_param (month, fx_rate, ref_fx_rate, updated_at) VALUES ('2026-10', 7, 7.25, 0)`,
		`INSERT INTO rs_tencent_supplier_param (user_id, platform, discount_pct, tax_rate, updated_at) VALUES (2, 'ccmax', 90, '', 0)`,
		`INSERT INTO rs_tencent_line_flag (line_key, rmb_priced, updated_at) VALUES ('2026-10|third_party|1002|claude-sonnet-5-5', TRUE, 0)`,
	} {
		_, err := db.Exec(s)
		require.NoError(t, err, "%.80s", s)
	}

	start, end, err := tencentMonthRange("2026-10")
	require.NoError(t, err)
	sources, warnings, err := loadTencentSources(start, end)
	require.NoError(t, err)
	assert.Equal(t, []string{"third_party: ¥3.00 用量的门户账号不在本地上号表，未出账"}, warnings)
	params, _, err := loadTencentStatementParams(start)
	require.NoError(t, err)
	assert.Equal(t, 7.0, params.FxRate)
	assert.Equal(t, 7.25, params.RefFxRate)

	lines, _ := buildTencentStatement(sources, params)
	type amounts struct {
		G, I, J, L float64
		N          string
	}
	got := map[string]amounts{}
	for _, l := range lines {
		got[l.LineKey] = amounts{l.OfficialUSD, l.Discount, l.TencentUSD, l.InvoiceRMB, l.Source}
	}
	assert.Equal(t, map[string]amounts{
		"2026-10|third_party|1001|gpt-5":                  {11, 0.88, 9.68, 70.18, "third_party - alice"},
		"2026-10|third_party|1002|claude-sonnet-5-5":      {14, 1, 14, 14, "third_party - alice"},
		"2026-10|ccmax|2001|claude-opus-5-5":              {2.86, 0.9, 2.57, 18.63, "ccmax - bob"},
		"2026-10|ccmax|2001|claude-sonnet-5-5":            {2.14, 0.9, 1.93, 13.99, "ccmax - bob"},
		"2026-10|ccmax|2001|" + tencentCCMaxFallbackModel: {1, 0.9, 0.9, 6.53, "ccmax - bob"},
		"2026-10|proxy|3001|gpt-5":                        {7, 1, 7, 50.75, "newapi proxy - carol"},
		"2026-10|proxy|3002|ch-b":                         {2, 1, 2, 14.5, "newapi proxy - carol"},
	}, got)
}
