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
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"
	"golang.org/x/crypto/bcrypt"
)

// One account is one 「API账单」 row: G = RMB ÷ month rate (USD-native proxy
// rows as is), J = ROUND(G×I, 2), L = J×K, M = 厂商+账号ID+供应商, N = source.
func TestTencentStatementAmounts(t *testing.T) {
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, billingExportZone)
	rows := []tencentSourceRow{
		{Platform: tencentPlatformThirdParty, UserID: 1, Supplier: "Studio A", AccountID: 1001, Vendor: "anthropic",
			Models: []string{"claude-opus-4-1", "claude-sonnet-4-5"}, Label: "anthropic-A-001 ***tegAA", CostCNY: 77},
		{Platform: tencentPlatformProxy, UserID: 3, Supplier: "Proxy Co", AccountID: 3001, Vendor: "azure-openai",
			Models: []string{"gpt-5"}, Label: "ch-a #3001", CostUSD: 7},
	}
	lines, summary := buildTencentStatement(rows, tencentStatementParams{Month: month, FxRate: 7, RefFxRate: 7.25, Discount: 0.88, TaxRate: "含票"})
	require.Len(t, lines, 2)
	assert.Equal(t, tencentLine{
		LineKey: "2026-10|proxy|3001", Platform: tencentPlatformProxy, UserID: 3,
		Supplier: "Proxy Co", BillMonth: 10, Vendor: "azure-openai", Model: "gpt-5", AccountID: "3001", Key: "ch-a #3001",
		OfficialUSD: 7, TaxRate: "含票", Discount: 0.88, TencentUSD: 6.16, RefFxRate: 7.25, InvoiceRMB: 44.66,
		BillName: "azure-openai+3001+Proxy Co", Source: "newapi proxy", CostUSD: 7,
	}, lines[0])
	l := lines[1]
	assert.Equal(t, "claude-opus-4-1, claude-sonnet-4-5", l.Model)
	assert.Equal(t, 11.0, l.OfficialUSD) // 77 / 7
	assert.Equal(t, 9.68, l.TencentUSD)  // ROUND(11 × 0.88, 2)
	assert.InDelta(t, 70.18, l.InvoiceRMB, 1e-9)
	assert.Equal(t, "anthropic+1001+Studio A", l.BillName)
	assert.Equal(t, "third_party", l.Source)

	assert.Equal(t, []tencentSummaryRow{
		{Supplier: "Proxy Co", BillMonth: 10, Vendor: "azure-openai", Source: "newapi proxy", OfficialUSD: 7, InvoiceRMB: 44.66, Lines: 1},
		{Supplier: "Studio A", BillMonth: 10, Vendor: "anthropic", Source: "third_party", OfficialUSD: 11, InvoiceRMB: 70.18, Lines: 1},
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

// Column F shows a portal account's alias and the last five key characters.
func TestTencentAccountLabel(t *testing.T) {
	assert.Equal(t, "anthropic-长河理想-20260902001 ***tegAA", tencentAccountLabel("anthropic-长河理想-20260902001", "sk-ant-api03-xxxxtegAA"))
	assert.Equal(t, "alias ***abc", tencentAccountLabel("alias", "abc"))
	assert.Equal(t, "alias", tencentAccountLabel(" alias ", ""))
	assert.Equal(t, "***12345", tencentAccountLabel("", "k-12345"))
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
		for _, path := range []string{"/api/tencent/statement?month=bad", "/api/tencent/statements/bad"} {
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
		{"id": 1001, "provider": "anthropic", "account_alias": "anthropic-StudioA-20260902001", "api_key": "sk-ant-api03-xxxxtegAA",
			"model": "claude-opus-4-1", "supplier_name": "portal-sup"},
		{"id": 1002, "provider": "azure-openai", "account_alias": "azure-openai-X-1", "api_key": "abcdeKEY12",
			"model": "gpt-5", "supplier_name": "Portal Supplier"},
		{"id": 1003, "provider": "claude", "account_alias": "cc", "api_key": "x", "supplier_name": "portal-sup"},
		{"id": 1004, "provider": "zhipu", "account_alias": "zhipu-idle", "api_key": "zzzzz", "supplier_name": "portal-sup"},
	}, []map[string]any{
		{"account_id": 1001, "ds_text": "2026-09-30", "model": "claude-opus-4-1", "cost": 1000.0},
		{"account_id": 1001, "ds_text": "2026-10-01", "model": "claude-sonnet-4-5", "cost": 50.0},
		{"account_id": 1001, "ds_text": "2026-10-31", "model": "claude-opus-4-1", "cost": 27.0},
		{"account_id": 1001, "ds_text": "2026-11-01", "model": "claude-opus-4-1", "cost": 500.0},
		{"account_id": 1002, "ds_text": "2026-10-15", "model_name": "gpt-5", "cost": 14.0},
		{"account_id": 1003, "ds_text": "2026-10-15", "model": "claude-opus-4-1", "cost": 999.0},
	})
	bj := func(y int, m time.Month, d, h int) int64 {
		return time.Date(y, m, d, h, 0, 0, 0, billingExportZone).Unix()
	}
	for _, s := range []string{
		`INSERT INTO rs_auth_user (id, username, studio) VALUES (1, 'alice', 'Studio A'), (2, 'bob', ''), (3, 'carol', 'Proxy Co')`,
		`INSERT INTO rs_supplier_account (uploaded_by, provider, remote_account_id, key_last8, account_type, a2_id, created_at, updated_at)
		   VALUES (1, 'anthropic', 1001, 'xxtegAA', 0, 501, 0, 0)`,
		// The record log is no longer a statement source.
		fmt.Sprintf(`INSERT INTO rs_supplier_record_log_hourly (platform, account_id, model, hour, cost) VALUES ('2.0', 501, 'gpt-5', %d, 9999e6)`, bj(2026, 10, 15, 12)),
		// bob runs two CC Max accounts: one statement row with their total.
		`INSERT INTO rs_ccmax_account (uploaded_by, remote_account_id, alias, account, created_at) VALUES
		   (2, 2001, 'cc-alias', 'cc@example.com', 0), (2, 2002, 'cc-2', 'cc2@example.com', 0)`,
		`INSERT INTO rs_ccmax_daily_bill (remote_account_id, ds, cost_micro, updated_at) VALUES
		   (2001, '2026-09-30', 100000000, 0), (2001, '2026-10-01', 35000000, 0), (2001, '2026-10-31', 7000000, 0), (2001, '2026-11-01', 9000000, 0),
		   (2002, '2026-10-12', 7000000, 0)`,
		`INSERT INTO rs_ccmax_daily_bill_model (remote_account_id, ds, model, cost_micro, updated_at) VALUES
		   (2001, '2026-10-01', 'claude-opus-4-1', 20000000, 0), (2001, '2026-10-01', 'claude-sonnet-4-5', 15000000, 0),
		   (2002, '2026-10-12', 'claude-haiku-4-5', 7000000, 0), (2002, '2026-10-12', 'claude-opus-4-1', 0, 0)`,
		`CREATE TABLE proxy_channel_ownership (remote_channel_id BIGINT PRIMARY KEY, user_id BIGINT NOT NULL, created_at BIGINT NOT NULL)`,
		`INSERT INTO proxy_channel_ownership VALUES (3001, 3, 0), (3002, 3, 0), (3003, 3, 0), (3004, 3, 0)`,
		`INSERT INTO rs_tencent_month_param (month, fx_rate, ref_fx_rate, updated_at) VALUES ('2026-10', 7, 7.25, 0)`,
	} {
		_, err := db.Exec(s)
		require.NoError(t, err, "%.80s", s)
	}
	// Upstream: 250,000 quota per USD; #3001 is rate-limited twice, #3003
	// fails, #3004 had no usage.
	prevWait := tencentProxyRetryWait
	tencentProxyRetryWait = time.Millisecond
	t.Cleanup(func() { tencentProxyRetryWait = prevWait })
	upstream := newTencentUpstream(t, 250000, map[int64]tencentUpstreamChannel{
		3001: {Name: "ch-a", Type: 3, Models: "gpt-5", Quota: 1_750_000, Limited: 2}, // billed after retries
		3002: {Name: "ch-b", Type: 3, Models: "gpt-5,claude-x", Quota: 500_000},
		3003: {Fail: true},
		3004: {Name: "ch-idle", Type: 1, Models: "gpt-5"},
	})
	return portal, upstream
}

type tencentUpstreamChannel struct {
	Name, Models string
	Type         int
	Quota        int64
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
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"quota": ch.Quota}})
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

// The three sources give one row per account for exactly the Beijing month:
// 3P from the portal's own bill of the roster's non-claude accounts, CC Max
// from the stored daily bills, proxy from the channels' quota growth.
func TestTencentLoadSources(t *testing.T) {
	portal, upstream := tencentSeedOctober(t)
	start, end, err := tencentMonthRange("2026-10")
	require.NoError(t, err)
	sources, warnings, err := loadTencentSources(start, end)
	require.NoError(t, err)
	require.Len(t, warnings, 2)
	assert.Equal(t, "third_party: 1 个有账单的门户账号不在本地上号表，供应商名称取门户的供应商名", warnings[0])
	assert.Regexp(t, `^proxy: 渠道 #3003 取数失败（.*502.*），未出账$`, warnings[1])
	// Every upstream call carries newapi-proxy's headers; each channel's
	// consume-log stat covers exactly the Beijing month.
	stats := map[string]url.Values{}
	for _, r := range upstream.requests {
		assert.Equal(t, "upstream-token", r.Header.Get("Authorization"), r.URL.Path)
		assert.Equal(t, "1", r.Header.Get("New-Api-User"), r.URL.Path)
		if r.URL.Path == "/api/log/stat" {
			stats[r.URL.Query().Get("channel")] = r.URL.Query()
		}
	}
	require.Len(t, stats, 4)
	assert.Equal(t, url.Values{"type": {"2"}, "channel": {"3001"},
		"start_timestamp": {strconv.FormatInt(start.Unix(), 10)}, "end_timestamp": {strconv.FormatInt(end.Unix(), 10)}}, stats["3001"])
	assert.Equal(t, "1790784000", stats["3001"].Get("start_timestamp")) // 2026-10-01 00:00 UTC+8
	assert.Equal(t, "1793462400", stats["3001"].Get("end_timestamp"))   // 2026-11-01 00:00 UTC+8
	require.Len(t, portal.queries, 1)
	assert.Equal(t, "1001,1002,1004", portal.queries[0].Get("account_ids"), "claude accounts are CC Max's")
	assert.Equal(t, []string{"20261001", "20261031"}, []string{portal.queries[0].Get("start_ds"), portal.queries[0].Get("end_ds")})

	params, from, err := loadTencentStatementParams(start)
	require.NoError(t, err)
	assert.Equal(t, tencentStatementParams{Month: start, FxRate: 7, RefFxRate: 7.25, Discount: 0.88, TaxRate: "含票"}, params)
	assert.Equal(t, "upload", from["discount_from"])

	lines, summary := buildTencentStatement(sources, params)
	type row struct {
		A, C, D, F string
		G, J, L    float64
		M, N       string
	}
	got := map[string]row{}
	for _, l := range lines {
		got[l.LineKey] = row{l.Supplier, l.Vendor, l.Model, l.Key, l.OfficialUSD, l.TencentUSD, tencentRound(l.InvoiceRMB, 2), l.BillName, l.Source}
	}
	assert.Equal(t, map[string]row{
		"2026-10|third_party|1001": {"Studio A", "anthropic", "claude-opus-4-1, claude-sonnet-4-5", "anthropic-StudioA-20260902001 ***tegAA",
			11, 9.68, 70.18, "anthropic+1001+Studio A", "third_party"},
		"2026-10|third_party|1002": {"Portal Supplier", "azure-openai", "gpt-5", "azure-openai-X-1 ***KEY12",
			2, 1.76, 12.76, "azure-openai+1002+Portal Supplier", "third_party"},
		"2026-10|ccmax|bob": {"bob", "claude", "claude-haiku-4-5, claude-opus-4-1, claude-sonnet-4-5", "2 个账号",
			7, 6.16, 44.66, "claude+bob", "ccmax"},
		"2026-10|proxy|3001": {"Proxy Co", "azure-openai", "gpt-5", "ch-a #3001", 7, 6.16, 44.66, "azure-openai+3001+Proxy Co", "newapi proxy"},
		"2026-10|proxy|3002": {"Proxy Co", "azure-claude", "gpt-5, claude-x", "ch-b #3002", 2, 1.76, 12.76, "azure-claude+3002+Proxy Co", "newapi proxy"},
	}, got)
	for _, l := range lines {
		assert.Equal(t, "含票", l.TaxRate)
		assert.Equal(t, 0.88, l.Discount)
		assert.Equal(t, 10, l.BillMonth)
	}
	assert.Equal(t, []tencentSummaryRow{
		{Supplier: "Portal Supplier", BillMonth: 10, Vendor: "azure-openai", Source: "third_party", OfficialUSD: 2, InvoiceRMB: 12.76, Lines: 1},
		{Supplier: "Proxy Co", BillMonth: 10, Vendor: "azure-claude", Source: "newapi proxy", OfficialUSD: 2, InvoiceRMB: 12.76, Lines: 1},
		{Supplier: "Proxy Co", BillMonth: 10, Vendor: "azure-openai", Source: "newapi proxy", OfficialUSD: 7, InvoiceRMB: 44.66, Lines: 1},
		{Supplier: "Studio A", BillMonth: 10, Vendor: "anthropic", Source: "third_party", OfficialUSD: 11, InvoiceRMB: 70.18, Lines: 1},
		{Supplier: "bob", BillMonth: 10, Vendor: "claude", Source: "ccmax", OfficialUSD: 7, InvoiceRMB: 44.66, Lines: 1},
	}, summary)

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
	total := 0.0
	for _, l := range body["lines"].([]any) {
		total += l.(map[string]any)["official_usd"].(float64)
	}
	return body["statement"].(map[string]any)["status"].(string), tencentRound(total, 2)
}

// 生成 → draft follows the source data on 重新生成 → 确认 freezes it: later
// source changes do not reach it and regenerating is refused.
func TestTencentSnapshotLifecycle(t *testing.T) {
	portal, _ := tencentSeedOctober(t)
	r := tencentTestRouter()

	code, _ := tencentDo(t, r, http.MethodPost, "/api/tencent/statements/2026-10/regenerate")
	assert.Equal(t, http.StatusNotFound, code, "regenerate before generate")
	code, _ = tencentDo(t, r, http.MethodPost, "/api/tencent/statements/2026-10/generate")
	require.Equal(t, http.StatusOK, code)
	status, total := tencentSnapshotTotal(t, r)
	assert.Equal(t, tencentStatementDraft, status)
	assert.Equal(t, 29.0, total) // 11 + 2 + 7 + 7 + 2
	code, _ = tencentDo(t, r, http.MethodPost, "/api/tencent/statements/2026-10/generate")
	assert.Equal(t, http.StatusConflict, code, "generate twice")

	portal.bill = append(portal.bill, map[string]any{"account_id": 1002, "ds_text": "2026-10-20", "model": "gpt-5", "cost": 70.0}) // +10 USD at 7
	_, total = tencentSnapshotTotal(t, r)
	assert.Equal(t, 29.0, total, "a draft only changes when regenerated")
	code, _ = tencentDo(t, r, http.MethodPost, "/api/tencent/statements/2026-10/regenerate")
	require.Equal(t, http.StatusOK, code)
	_, total = tencentSnapshotTotal(t, r)
	assert.Equal(t, 39.0, total)

	code, _ = tencentDo(t, r, http.MethodPost, "/api/tencent/statements/2026-10/confirm")
	require.Equal(t, http.StatusOK, code)
	code, _ = tencentDo(t, r, http.MethodPost, "/api/tencent/statements/2026-10/confirm")
	assert.Equal(t, http.StatusConflict, code, "confirm twice")

	portal.bill = append(portal.bill, map[string]any{"account_id": 1002, "ds_text": "2026-10-21", "cost": 700.0})
	_, err := db.Exec(`UPDATE rs_tencent_month_param SET fx_rate = 6.5 WHERE month = '2026-10'`)
	require.NoError(t, err)
	code, body := tencentDo(t, r, http.MethodPost, "/api/tencent/statements/2026-10/regenerate")
	assert.Equal(t, http.StatusConflict, code, body)
	status, total = tencentSnapshotTotal(t, r)
	assert.Equal(t, tencentStatementConfirmed, status)
	assert.Equal(t, 39.0, total, "a confirmed month ignores later source and parameter changes")

	// Exports read the snapshot: one supplier, or everyone in one file.
	for path, rows := range map[string]int{
		"/api/tencent/statements/2026-10/export":                     6,
		"/api/tencent/statements/2026-10/export?supplier=Proxy%20Co": 3,
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
	code, _ = tencentDo(t, r, http.MethodGet, "/api/tencent/statements/2026-10/export?supplier=nobody")
	assert.Equal(t, http.StatusNotFound, code)
}

// The export follows the template: sheet order, headers, number formats, and
// J / L / summary totals as the template's formulas, which evaluate to the
// statement's own amounts. A one-supplier file keeps the template's SUMIFS
// (vendor + source); a multi-supplier file also matches the supplier.
func TestTencentStatementWorkbook(t *testing.T) {
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, billingExportZone)
	params := tencentStatementParams{Month: month, FxRate: 7, RefFxRate: 7.25, Discount: 0.88, TaxRate: "含票"}
	rows := []tencentSourceRow{
		{Platform: tencentPlatformThirdParty, Supplier: "Studio A", AccountID: 1001, Vendor: "anthropic", Models: []string{"claude-opus-4-1"}, Label: "a ***tegAA", CostCNY: 77},
		{Platform: tencentPlatformThirdParty, Supplier: "Studio A", AccountID: 1002, Vendor: "anthropic", Models: []string{"claude-sonnet-4-5"}, Label: "b ***xyz12", CostCNY: 14},
		{Platform: tencentPlatformProxy, Supplier: "Studio A", AccountID: 3001, Vendor: "azure-openai", Models: []string{"gpt-5"}, Label: "ch-a #3001", CostUSD: 7},
	}
	lines, summary := buildTencentStatement(rows, params)
	f, err := tencentStatementWorkbook(lines)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, f.Write(&buf))
	f, err = excelize.OpenReader(&buf)
	require.NoError(t, err)
	defer f.Close()

	assert.Equal(t, []string{tencentSummarySheet, tencentLinesSheet}, f.GetSheetList())
	got, err := f.GetRows(tencentLinesSheet)
	require.NoError(t, err)
	assert.Equal(t, []string{"供应商名称", "账单月份", "模型厂商名称", "模型名称", "账号ID", "key", "付给官方的费用（不区分币种）", "税率",
		"对应折扣", "腾讯支付金额（不区分币种）", "参考汇率\nhttps://www.kylc.com/huilv/d-boc-usd.html", "开票金额（RMB/元）", "官方导出的账单名称", "来源系统 / 上号人"}, got[0])
	got, err = f.GetRows(tencentSummarySheet)
	require.NoError(t, err)
	assert.Equal(t, []string{"供应商名称", "账单月份", "模型厂商名称", "付给官方的费用（不区分币种）", "开票金额（RMB/元）", "备注"}, got[0])

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
	assert.Equal(t, `0_ `, numFmt(tencentLinesSheet, "C2"))
	assert.Equal(t, `#,##0.00" USD"`, numFmt(tencentLinesSheet, "G2"))
	assert.Equal(t, `#,##0.00" USD"`, numFmt(tencentLinesSheet, "J2"))
	assert.Equal(t, "9", numFmt(tencentLinesSheet, "I2")) // built-in 0%
	assert.Equal(t, "4", numFmt(tencentLinesSheet, "L2")) // built-in #,##0.00
	assert.Equal(t, `#,##0.00" USD"`, numFmt(tencentSummarySheet, "D2"))
	assert.Equal(t, "4", numFmt(tencentSummarySheet, "E2"))

	for i, l := range lines {
		r := i + 2
		j, err := f.GetCellFormula(tencentLinesSheet, fmt.Sprintf("J%d", r))
		require.NoError(t, err)
		assert.Equal(t, fmt.Sprintf("ROUND(G%d*I%d,2)", r, r), j)
		lf, err := f.GetCellFormula(tencentLinesSheet, fmt.Sprintf("L%d", r))
		require.NoError(t, err)
		assert.Equal(t, fmt.Sprintf(`IF(J%d="","",J%d*K%d)`, r, r, r), lf)
		for col, expect := range map[string]float64{"J": l.TencentUSD, "L": l.InvoiceRMB} {
			v, err := f.CalcCellValue(tencentLinesSheet, fmt.Sprintf("%s%d", col, r), excelize.Options{RawCellValue: true})
			require.NoError(t, err)
			got, err := strconv.ParseFloat(v, 64)
			require.NoError(t, err)
			assert.InDelta(t, expect, got, 1e-9, "%s%d %s", col, r, l.LineKey)
		}
		h, err := f.GetCellValue(tencentLinesSheet, fmt.Sprintf("H%d", r))
		require.NoError(t, err)
		assert.Equal(t, "含票", h)
	}

	require.Len(t, summary, 2)
	d, err := f.GetCellFormula(tencentSummarySheet, "D2")
	require.NoError(t, err)
	assert.Equal(t, `SUMIFS('API账单'!G$2:G$4,'API账单'!C$2:C$4,C2,'API账单'!N$2:N$4,"third_party*")`, d)
	for i, s := range summary {
		r := i + 2
		note, err := f.GetCellValue(tencentSummarySheet, fmt.Sprintf("F%d", r))
		require.NoError(t, err)
		assert.Empty(t, note)
		for col, expect := range map[string]float64{"D": s.OfficialUSD, "E": s.InvoiceRMB} {
			v, err := f.CalcCellValue(tencentSummarySheet, fmt.Sprintf("%s%d", col, r), excelize.Options{RawCellValue: true})
			require.NoError(t, err)
			got, err := strconv.ParseFloat(v, 64)
			require.NoError(t, err)
			assert.InDelta(t, expect, got, 1e-9, "%s%d %s/%s", col, r, s.Vendor, s.Source)
		}
	}

	// Two suppliers with the same vendor and source stay apart.
	rows = append(rows, tencentSourceRow{Platform: tencentPlatformThirdParty, Supplier: "Studio B", AccountID: 1009, Vendor: "anthropic", CostCNY: 70})
	lines, summary = buildTencentStatement(rows, params)
	f2, err := tencentStatementWorkbook(lines)
	require.NoError(t, err)
	defer f2.Close()
	d, err = f2.GetCellFormula(tencentSummarySheet, "D2")
	require.NoError(t, err)
	assert.Equal(t, `SUMIFS('API账单'!G$2:G$5,'API账单'!A$2:A$5,A2,'API账单'!C$2:C$5,C2,'API账单'!N$2:N$5,"third_party*")`, d)
	for i, s := range summary {
		v, err := f2.CalcCellValue(tencentSummarySheet, fmt.Sprintf("D%d", i+2), excelize.Options{RawCellValue: true})
		require.NoError(t, err)
		got, err := strconv.ParseFloat(v, 64)
		require.NoError(t, err)
		assert.InDelta(t, s.OfficialUSD, got, 1e-9, "%s/%s", s.Supplier, s.Vendor)
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
		USD, RMB float64
	}
	want := map[string]total{}
	for _, raw := range snap["lines"].([]any) {
		l := raw.(map[string]any)
		k := l["supplier"].(string) + "/" + tencentSourcePrefix[l["platform"].(string)]
		sum := want[k]
		sum.Lines++
		sum.USD = tencentRound(sum.USD+l["official_usd"].(float64), 2)
		sum.RMB = tencentRound(sum.RMB+l["invoice_rmb"].(float64), 2)
		want[k] = sum
	}
	got := map[string]total{}
	var button string
	for _, raw := range card["elements"].([]any) {
		el := raw.(map[string]any)
		switch el["tag"] {
		case "table":
			for _, rr := range el["rows"].([]any) {
				row := rr.(map[string]any)
				got[row["supplier"].(string)+"/"+row["platform"].(string)] = total{int(row["lines"].(float64)), row["usd"].(float64), row["rmb"].(float64)}
			}
		case "action":
			button = el["actions"].([]any)[0].(map[string]any)["url"].(string)
		}
	}
	assert.Equal(t, want, got)
	assert.Equal(t, map[string]total{
		"Portal Supplier/third_party": {1, 2, 12.76},
		"Proxy Co/newapi proxy":       {2, 9, 57.42},
		"Studio A/third_party":        {1, 11, 70.18},
		"bob/ccmax":                   {1, 7, 44.66},
	}, got)
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
