package main

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"log"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type supplierPortalTransport func(*http.Request) (*http.Response, error)

func (f supplierPortalTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestSupplierAccountCreate(t *testing.T) {
	previousDB, previousClient := db, supplierHTTPClient
	previousOrigin, previousToken := supplierAccountBaseURL, supplierAccountToken
	previousLog := log.Writer()
	t.Cleanup(func() {
		db, supplierHTTPClient = previousDB, previousClient
		supplierAccountBaseURL, supplierAccountToken = previousOrigin, previousToken
		log.SetOutput(previousLog)
	})
	supplierAccountBaseURL, supplierAccountToken = "https://portal.invalid", "fixture-token"

	const proxyURL = "socks5://fixture-user:fixture%40password@proxy.invalid:1080"
	const echo = "fixture-user fixture@password " + proxyURL
	const success = `{"id":42,"alias":"fixture-alias","msg":"` + echo + `"}`
	account := gin.H{
		"provider": "openai", "model": "fixture-model", "api_key": "fixture-key",
		"account_type": 0, "tpm": "1000", "rpm": "10",
	}
	proxy := gin.H{"proxy_type": "bind_custom", "proxy_region": "日本", "proxy_url": proxyURL}

	for _, tc := range []struct {
		name           string
		input          gin.H // Fields added to the account by the caller.
		wantExtra      gin.H // Additional fields expected on the portal request.
		portalStatus   int   // Zero means validation must stop the request locally.
		portalBody     string
		transportError bool
		recordError    bool
		wantStatus     int
	}{
		{name: "absent preserves portal error", portalStatus: 409, portalBody: `{"detail":"account already exists"}`, wantStatus: 422},
		{name: "blank omits all proxy fields", input: gin.H{"proxy_type": "", "proxy_region": "  ", "proxy_url": "\t"}, portalStatus: 201, portalBody: `{"id":42,"alias":"fixture-alias"}`, wantStatus: 200},
		{name: "custom preserves encoded credentials", input: proxy, wantExtra: proxy, portalStatus: 201, portalBody: success, wantStatus: 200},
		{name: "proxy and AWS registration regions are independent",
			input: gin.H{"provider": "aws_third", "region": "US", "arns": gin.H{"fixture-model": "fixture-arn"},
				"proxy_type": "bind_custom", "proxy_region": " 日本 ", "proxy_url": proxyURL},
			wantExtra: gin.H{"provider": "aws_third", "region": "US", "adc_config": `{"arns":{"fixture-model":"fixture-arn"}}`,
				"proxy_type": "bind_custom", "proxy_region": "日本", "proxy_url": proxyURL},
			portalStatus: 201, portalBody: success, wantStatus: 200},
		{name: "URL without mode", input: gin.H{"proxy_url": proxyURL, "proxy_region": "日本"}, wantStatus: 400},
		{name: "missing URL", input: gin.H{"proxy_type": "bind_custom", "proxy_region": "日本"}, wantStatus: 400},
		{name: "missing region", input: gin.H{"proxy_type": "bind_custom", "proxy_url": proxyURL}, wantStatus: 400},
		{name: "unknown mode", input: gin.H{"proxy_type": "bind_multi", "proxy_region": "日本", "proxy_url": proxyURL}, wantStatus: 400},
		{name: "malformed URL", input: gin.H{"proxy_type": "bind_custom", "proxy_region": "日本", "proxy_url": proxyURL + "x"}, wantStatus: 400},
		{name: "upstream rejection echoes credentials", input: proxy, wantExtra: proxy, portalStatus: 400, portalBody: `{"detail":"` + echo + `"}`, wantStatus: 422},
		{name: "unexpected success body echoes credentials", input: proxy, wantExtra: proxy, portalStatus: 200, portalBody: echo, wantStatus: 422},
		{name: "transport error echoes credentials", input: proxy, wantExtra: proxy, transportError: true, wantStatus: 422},
		{name: "record error echoes credentials", input: proxy, wantExtra: proxy, portalStatus: 201, portalBody: success, recordError: true, wantStatus: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			db = pool
			// Leave config reads unstubbed to exercise the token and discount
			// fallbacks. Only a successful portal reply should reach the insert.
			if tc.wantStatus == 200 || tc.recordError {
				insert := mock.ExpectExec("INSERT INTO rs_supplier_account")
				if tc.recordError {
					insert.WillReturnError(errors.New(echo))
				} else {
					insert.WillReturnResult(sqlmock.NewResult(1, 1))
				}
			}

			var logs bytes.Buffer
			log.SetOutput(&logs)
			calls := 0
			supplierHTTPClient = &http.Client{Transport: supplierPortalTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				defer r.Body.Close()
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "https://portal.invalid/supplier-account/api/openapi/accounts", r.URL.String())
				assert.Equal(t, "Bearer fixture-token", r.Header.Get("Authorization"))
				assert.Empty(t, r.Header.Get("Proxy-Authorization"))
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				want := maps.Clone(account)
				want["discount"] = "88"
				maps.Copy(want, tc.wantExtra)
				encoded, err := json.Marshal(want)
				require.NoError(t, err)
				assert.JSONEq(t, string(encoded), string(body))

				if tc.transportError {
					return nil, errors.New(echo)
				}
				require.NotZero(t, tc.portalStatus, "invalid input reached the portal")
				w := httptest.NewRecorder()
				w.WriteHeader(tc.portalStatus)
				_, _ = w.WriteString(tc.portalBody)
				return w.Result(), nil
			})}

			input := maps.Clone(account)
			maps.Copy(input, tc.input)
			encoded, err := json.Marshal(input)
			require.NoError(t, err)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set("role", minAdminRole)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/supplier-account/accounts", bytes.NewReader(encoded))
			c.Request.Header.Set("Content-Type", "application/json")
			handleSupplierAccountCreate(c)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantStatus == 400 {
				assert.Zero(t, calls)
			} else {
				assert.Equal(t, 1, calls)
			}
			visible := logs.String() + w.Body.String()
			for _, secret := range []string{"fixture-user", "fixture%40password", "fixture@password"} {
				assert.NotContains(t, visible, secret)
			}
			if tc.wantStatus == 200 {
				assert.JSONEq(t, `{"id":42,"alias":"fixture-alias","msg":"提交成功"}`, w.Body.String())
			} else if tc.input == nil {
				assert.JSONEq(t, `{"error":"account already exists"}`, w.Body.String())
			} else {
				assert.Contains(t, w.Body.String(), `"error"`)
			}
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSupplierMetricsFilterAndSummary(t *testing.T) {
	previousDB, previousClient := db, supplierHTTPClient
	previousOrigin, previousToken := supplierAccountBaseURL, supplierAccountToken
	t.Cleanup(func() {
		db, supplierHTTPClient = previousDB, previousClient
		supplierAccountBaseURL, supplierAccountToken = previousOrigin, previousToken
	})
	supplierAccountBaseURL, supplierAccountToken = "https://portal.invalid", "fixture-token"

	// Local accounts: remote id, alias, provider, owner. Account 104 has no
	// metrics row and still counts as an account.
	type account struct {
		id       int64
		provider string
		owner    int64
		username string
		studio   string
	}
	all := []account{
		{104, "openai", 7, "alice", "Studio A"},
		{103, "openai", 8, "bob", "Studio B"},
		{102, "claude", 7, "alice", "Studio A"},
		{101, "openai", 7, "alice", "Studio A"},
	}
	alias := func(id int64) string { return "p-company-" + strconv.FormatInt(id, 10) }
	// Portal metrics keyed by an internal aid; rows join back via alias.
	type usage struct {
		requests, prompt, completion int64
		rate, costRMB                float64
	}
	portal := map[int64]usage{
		101: {100, 1000, 500, 0.75, 10},
		102: {50, 400, 200, 1.0, 5},
		103: {10, 80, 40, 0.5, 2},
	}

	for _, tc := range []struct {
		name       string
		role       int
		userID     int64
		request    gin.H
		wantArg    *int64  // The owner the account query must be scoped to.
		dbOwner    *int64  // Rows the database holds for that scope (nil = all).
		wantIDs    []int64 // Account ids sent to the portal.
		wantTotal  string
		wantByProv string
		wantBySupp string
	}{
		{
			name: "admin filters by supplier and provider", role: minAdminRole, userID: 1,
			request: gin.H{"uploaded_by": 7, "providers": []string{" OpenAI"}},
			wantArg: ptr[int64](7), dbOwner: ptr[int64](7), wantIDs: []int64{104, 101},
			wantTotal:  `{"accounts":2,"requests":100,"prompt_tokens":1000,"completion_tokens":500,"cost":10,"success_rate":0.75}`,
			wantByProv: `[{"provider":"openai","accounts":2,"requests":100,"prompt_tokens":1000,"completion_tokens":500,"cost":10,"success_rate":0.75}]`,
			wantBySupp: `[{"uploaded_by":7,"username":"alice","studio":"Studio A","accounts":2,"requests":100,"prompt_tokens":1000,"completion_tokens":500,"cost":10,"success_rate":0.75}]`,
		},
		{
			name: "admin without filters sees every account", role: minAdminRole, userID: 1,
			request: gin.H{}, wantIDs: []int64{104, 103, 102, 101},
			wantTotal:  `{"accounts":4,"requests":160,"prompt_tokens":1480,"completion_tokens":740,"cost":17,"success_rate":0.8125}`,
			wantByProv: `[{"provider":"openai","accounts":3,"requests":110,"prompt_tokens":1080,"completion_tokens":540,"cost":12,"success_rate":0.7272727272727273},{"provider":"claude","accounts":1,"requests":50,"prompt_tokens":400,"completion_tokens":200,"cost":5,"success_rate":1}]`,
			wantBySupp: `[{"uploaded_by":7,"username":"alice","studio":"Studio A","accounts":3,"requests":150,"prompt_tokens":1400,"completion_tokens":700,"cost":15,"success_rate":0.8333333333333334},{"uploaded_by":8,"username":"bob","studio":"Studio B","accounts":1,"requests":10,"prompt_tokens":80,"completion_tokens":40,"cost":2,"success_rate":0.5}]`,
		},
		{
			name: "supplier asking for another owner gets only their own", role: 4, userID: 7,
			request: gin.H{"uploaded_by": 8, "providers": []string{"claude"}},
			wantArg: ptr[int64](7), dbOwner: ptr[int64](7), wantIDs: []int64{102},
			wantTotal:  `{"accounts":1,"requests":50,"prompt_tokens":400,"completion_tokens":200,"cost":5,"success_rate":1}`,
			wantByProv: `[{"provider":"claude","accounts":1,"requests":50,"prompt_tokens":400,"completion_tokens":200,"cost":5,"success_rate":1}]`,
			wantBySupp: `[]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			db = pool
			rows := sqlmock.NewRows([]string{"remote_account_id", "alias", "provider", "uploaded_by", "username", "studio"})
			for _, a := range all {
				if tc.dbOwner == nil || a.owner == *tc.dbOwner {
					rows.AddRow(a.id, alias(a.id), a.provider, a.owner, a.username, a.studio)
				}
			}
			query := mock.ExpectQuery(`FROM rs_supplier_account a`)
			if tc.wantArg != nil {
				query.WithArgs(*tc.wantArg)
			}
			query.WillReturnRows(rows)

			supplierHTTPClient = &http.Client{Transport: supplierPortalTransport(func(r *http.Request) (*http.Response, error) {
				defer r.Body.Close()
				var req struct {
					AccountIDs []int64 `json:"account_ids"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
				assert.Equal(t, tc.wantIDs, req.AccountIDs)
				items := []gin.H{}
				for _, id := range req.AccountIDs {
					u, ok := portal[id]
					if !ok {
						continue
					}
					if r.URL.Path == "/supplier-account/api/openapi/bill/query" {
						items = append(items, gin.H{"id": id, "cost": u.costRMB})
						continue
					}
					items = append(items, gin.H{
						"aid": 9000 + id, "account_alias": alias(id), "status": "active",
						"requests": u.requests, "prompt_tokens": u.prompt, "completion_tokens": u.completion,
						"success_rate": u.rate,
					})
				}
				key := "accounts"
				if r.URL.Path == "/supplier-account/api/openapi/bill/query" {
					key = "list"
				}
				encoded, err := json.Marshal(gin.H{key: items})
				require.NoError(t, err)
				w := httptest.NewRecorder()
				_, _ = w.Write(encoded)
				return w.Result(), nil
			})}

			encoded, err := json.Marshal(gin.H{"begin_time": "2026-11-01 00:00:00", "end_time": "2026-11-20 00:00:00"})
			if len(tc.request) > 0 {
				req := gin.H{"begin_time": "2026-11-01 00:00:00", "end_time": "2026-11-20 00:00:00"}
				maps.Copy(req, tc.request)
				encoded, err = json.Marshal(req)
			}
			require.NoError(t, err)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set("role", tc.role)
			c.Set("user_id", tc.userID)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/supplier-account/metrics", bytes.NewReader(encoded))
			c.Request.Header.Set("Content-Type", "application/json")
			handleSupplierMetrics(c)

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var resp struct {
				Accounts []supplierMetric `json:"accounts"`
				Summary  struct {
					Total      json.RawMessage `json:"total"`
					ByProvider json.RawMessage `json:"by_provider"`
					BySupplier json.RawMessage `json:"by_supplier"`
				} `json:"summary"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.JSONEq(t, tc.wantTotal, string(resp.Summary.Total))
			assert.JSONEq(t, tc.wantByProv, string(resp.Summary.ByProvider))
			assert.JSONEq(t, tc.wantBySupp, string(resp.Summary.BySupplier))

			// The account rows keep their existing shape: re-keyed onto the
			// remote id, alias masked, cost from the bill.
			var rowRequests int64
			for _, m := range resp.Accounts {
				assert.Contains(t, tc.wantIDs, m.AID)
				assert.Equal(t, "p-***-"+strconv.FormatInt(m.AID, 10), m.AccountAlias)
				require.NotNil(t, m.Cost)
				assert.InDelta(t, portal[m.AID].costRMB, *m.Cost, 1e-9)
				rowRequests += *m.Requests
			}
			assert.Contains(t, tc.wantTotal, `"requests":`+strconv.FormatInt(rowRequests, 10)+`,`)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestSupplierRecordLogFilters(t *testing.T) {
	previousDB := db
	t.Cleanup(func() { db = previousDB })

	// Platform-2.0 ids and providers of user 7's accounts; user 8 owns 301.
	ownedBy7 := [][]driver.Value{{int64(201), "openai"}, {int64(202), "claude"}, {int64(203), " OpenAI"}}
	start := time.Now().Add(-time.Hour).In(supplierPortalLoc).Format(time.DateTime)

	for _, tc := range []struct {
		name    string
		role    int
		userID  int64
		request gin.H
		ownerQ  *int64 // Owner the account lookup must be scoped to (nil = no lookup).
		wantIDs string // account_id = ANY(...) argument; "" = no account filter.
		status  int
	}{
		{name: "admin by supplier and provider", role: minAdminRole, userID: 1,
			request: gin.H{"uploaded_by": 7, "providers": []string{" OpenAI"}}, ownerQ: ptr[int64](7), wantIDs: "{201,203}", status: 200},
		{name: "admin filters intersect picked accounts", role: minAdminRole, userID: 1,
			request: gin.H{"uploaded_by": 7, "providers": []string{"openai"}, "account_ids": []int{203, 301}}, ownerQ: ptr[int64](7), wantIDs: "{203}", status: 200},
		{name: "supplier asking for another owner gets only their own", role: 4, userID: 7,
			request: gin.H{"uploaded_by": 8}, ownerQ: ptr[int64](7), wantIDs: "{201,202,203}", status: 200},
		{name: "supplier picking another owner's account is refused", role: 4, userID: 7,
			request: gin.H{"account_ids": []int{301}}, ownerQ: ptr[int64](7), status: 403},
		{name: "admin without new params is unscoped", role: minAdminRole, userID: 1,
			request: gin.H{}, status: 200},
	} {
		for _, endpoint := range []string{"list", "aggregate"} {
			t.Run(tc.name+"/"+endpoint, func(t *testing.T) {
				pool, mock, err := sqlmock.New()
				require.NoError(t, err)
				t.Cleanup(func() { _ = pool.Close() })
				db = pool
				if tc.ownerQ != nil {
					rows := sqlmock.NewRows([]string{"a2_id", "provider"})
					for _, r := range ownedBy7 {
						rows.AddRow(r...)
					}
					mock.ExpectQuery(`SELECT a2_id, provider FROM rs_supplier_account WHERE a2_id > 0 AND uploaded_by=\$1`).
						WithArgs(*tc.ownerQ).WillReturnRows(rows)
				}
				// list and aggregate must share one filter.
				filter := []driver.Value{"2.0", sqlmock.AnyArg(), sqlmock.AnyArg()}
				if tc.wantIDs != "" {
					filter = append(filter, tc.wantIDs)
				}
				if tc.status == 200 && endpoint == "list" {
					mock.ExpectQuery(`SELECT COUNT\(\*\) FROM rs_supplier_record_log WHERE`).WithArgs(filter...).
						WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
					mock.ExpectQuery(`(?s)FROM rs_supplier_record_log WHERE .* ORDER BY`).WithArgs(append(filter, 50, 0)...).
						WillReturnRows(sqlmock.NewRows([]string{"id", "req_time", "account_id", "account_name", "provider", "model", "stream",
							"prompt_tokens", "completion_tokens", "cost", "duration_ms", "status_code", "err_code", "err_msg", "other"}).
							AddRow(1, time.Now().Unix(), 201, "openai-company-1", "openai", "m", false, 10, 5, 2e6, 100, 200, "", "", ""))
				}
				if tc.status == 200 && endpoint == "aggregate" {
					mock.ExpectQuery(`(?s)FROM rs_supplier_record_log WHERE .* GROUP BY account_id`).WithArgs(filter...).
						WillReturnRows(sqlmock.NewRows([]string{"account_id", "count", "failed", "cost"}).AddRow(201, 1, 0, 2e6))
				}

				body := gin.H{"platform_type": "2.0", "start_time": start}
				maps.Copy(body, tc.request)
				encoded, err := json.Marshal(body)
				require.NoError(t, err)
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Set("role", tc.role)
				c.Set("user_id", tc.userID)
				c.Request = httptest.NewRequest(http.MethodPost, "/api/supplier-account/record-log/"+endpoint, bytes.NewReader(encoded))
				c.Request.Header.Set("Content-Type", "application/json")
				if endpoint == "list" {
					handleSupplierRecordLogList(c)
				} else {
					handleSupplierRecordLogAggregate(c)
				}

				require.Equal(t, tc.status, w.Code, w.Body.String())
				if tc.status == 200 && endpoint == "list" {
					assert.Contains(t, w.Body.String(), `"total":1`)
				}
				if tc.status == 200 && endpoint == "aggregate" {
					assert.Contains(t, w.Body.String(), `"requests":1`)
				}
				assert.NoError(t, mock.ExpectationsWereMet())
			})
		}
	}
}

func TestSupplierRecordLogAccountsFilters(t *testing.T) {
	previousDB := db
	t.Cleanup(func() { db = previousDB })

	for _, tc := range []struct {
		name   string
		role   int
		userID int64
		query  string
		owner  int64
		want   string
	}{
		{name: "admin narrows by supplier and provider", role: minAdminRole, userID: 1,
			query: "?uploaded_by=7&providers=OpenAI", owner: 7,
			want: `[{"id":1,"provider":"openai","alias":"a","remark":"","a1_id":11,"a2_id":21,"uploaded_by":7,"username":"alice","studio":"Studio A"},` +
				`{"id":3,"provider":" OpenAI","alias":"c","remark":"","a1_id":13,"a2_id":23,"uploaded_by":7,"username":"alice","studio":"Studio A"}]`},
		{name: "supplier ignores uploaded_by and loses owner names", role: 4, userID: 7,
			query: "?uploaded_by=8&providers=claude,azure", owner: 7,
			want: `[{"id":2,"provider":"claude","alias":"b","remark":"","a1_id":12,"a2_id":22,"uploaded_by":7}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			db = pool
			rows := sqlmock.NewRows([]string{"id", "provider", "alias", "remark", "a1_id", "a2_id", "uploaded_by", "username", "studio"})
			for i, p := range []string{"openai", "claude", " OpenAI"} {
				id := int64(i + 1)
				rows.AddRow(id, p, string(rune('a'+i)), "", 10+id, 20+id, int64(7), "alice", "Studio A")
			}
			mock.ExpectQuery(`FROM rs_supplier_account a\s+LEFT JOIN rs_auth_user u ON u.id = a.uploaded_by WHERE a.uploaded_by=\$1`).
				WithArgs(tc.owner).WillReturnRows(rows)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set("role", tc.role)
			c.Set("user_id", tc.userID)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/supplier-account/record-log/accounts"+tc.query, nil)
			handleSupplierRecordLogAccounts(c)

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var resp struct {
				Accounts json.RawMessage `json:"accounts"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.JSONEq(t, tc.want, string(resp.Accounts))
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSupplierCFOpenRouterCreate(t *testing.T) {
	previousDB, previousClient := db, supplierHTTPClient
	previousOrigin, previousToken := supplierAccountBaseURL, supplierAccountToken
	previousORBase, previousORClient := openRouterBaseURL, openRouterHTTPClient
	previousLog := log.Writer()
	t.Cleanup(func() {
		db, supplierHTTPClient = previousDB, previousClient
		supplierAccountBaseURL, supplierAccountToken = previousOrigin, previousToken
		openRouterBaseURL, openRouterHTTPClient = previousORBase, previousORClient
		log.SetOutput(previousLog)
	})
	supplierAccountBaseURL, supplierAccountToken = "https://portal.invalid", "fixture-token"

	const (
		mgmtKey   = "fixture-mgmt-key-0000"
		cfKey     = "fixture-cloudflare-token-1234"
		cfAccount = "0123456789abcdef0123456789ABCDEF"
		orKey     = "sk-or-v1-fixture-openrouter-key"
		orHash    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		byokID    = "11111111-2222-3333-4444-555555555555"
	)

	for _, tc := range []struct {
		name        string
		cfAccountID string
		mgmtKey     string
		keyStatus   int // OpenRouter POST /keys
		byokStatus  int // OpenRouter POST /byok
		portalCode  int // 号池 /openapi/accounts; 0 = never reached
		wantStatus  int
		wantORCalls []string
		wantErr     string
	}{
		{cfAccountID: cfAccount, name: "success uploads the OR key", mgmtKey: mgmtKey, keyStatus: 201, byokStatus: 201, portalCode: 201, wantStatus: 200,
			wantORCalls: []string{"POST /keys", "POST /byok", "PATCH /keys/" + orHash, "PATCH /byok/" + byokID}},
		{cfAccountID: cfAccount, name: "management key missing", wantStatus: 503, wantErr: "未配置 OpenRouter 管理 Key"},
		{cfAccountID: cfAccount, name: "OR key creation fails", mgmtKey: mgmtKey, keyStatus: 500, wantStatus: 422,
			wantORCalls: []string{"POST /keys"}, wantErr: "创建 OpenRouter Key失败（HTTP 500）"},
		{cfAccountID: cfAccount, name: "BYOK fails deletes the OR key", mgmtKey: mgmtKey, keyStatus: 201, byokStatus: 400, wantStatus: 422,
			wantORCalls: []string{"POST /keys", "POST /byok", "DELETE /keys/" + orHash}, wantErr: "登记 Cloudflare BYOK失败（HTTP 400）"},
		{cfAccountID: cfAccount, name: "号池 rejection deletes BYOK and OR key", mgmtKey: mgmtKey, keyStatus: 201, byokStatus: 201, portalCode: 400, wantStatus: 422,
			wantORCalls: []string{"POST /keys", "POST /byok", "DELETE /byok/" + byokID, "DELETE /keys/" + orHash}},
		{name: "Account ID missing is rejected before OpenRouter", mgmtKey: mgmtKey, wantStatus: 400, wantErr: "Cloudflare Account ID"},
		{cfAccountID: "not-a-cloudflare-account-id-0000", name: "Account ID not 32 hex is rejected", mgmtKey: mgmtKey, wantStatus: 400,
			wantErr: "32 位十六进制"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = pool.Close() })
			db = pool
			mock.MatchExpectationsInOrder(false)
			// Other config reads are left unstubbed and fall back to defaults.
			if tc.mgmtKey != "" && tc.wantStatus != 400 {
				mock.ExpectQuery("SELECT value FROM report_config").WithArgs(cfgOpenRouterMgmtKey).
					WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(tc.mgmtKey))
			}
			if tc.wantStatus == 200 {
				mock.ExpectExec("INSERT INTO rs_supplier_account").
					WithArgs(int64(7), "Studio A", supplierCFOpenRouterProvider, "fixture-model", int64(42), "openrouter-co-01",
						0, "", orKey[len(orKey)-8:], sqlmock.AnyArg(), sqlmock.AnyArg(), orHash, byokID).
					WillReturnResult(sqlmock.NewResult(1, 1))
			}

			var calls []string
			var byokBody map[string]any
			or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				assert.Equal(t, "Bearer "+mgmtKey, r.Header.Get("Authorization"))
				switch r.Method + " " + r.URL.Path {
				case "POST /keys":
					w.WriteHeader(tc.keyStatus)
					if tc.keyStatus == 201 {
						_, _ = io.WriteString(w, `{"data":{"hash":"`+orHash+`","name":"x"},"key":"`+orKey+`"}`)
					} else {
						_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
					}
				case "POST /byok":
					require.NoError(t, json.NewDecoder(r.Body).Decode(&byokBody))
					w.WriteHeader(tc.byokStatus)
					if tc.byokStatus == 201 {
						_, _ = io.WriteString(w, `{"data":{"id":"`+byokID+`","provider":"cloudflare"}}`)
					} else {
						_, _ = io.WriteString(w, `{"error":{"message":"invalid key `+cfKey+`"}}`)
					}
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, `{}`)
				}
			}))
			t.Cleanup(or.Close)
			openRouterBaseURL, openRouterHTTPClient = or.URL, or.Client()

			var logs bytes.Buffer
			log.SetOutput(&logs)
			portalCalls := 0
			supplierHTTPClient = &http.Client{Transport: supplierPortalTransport(func(r *http.Request) (*http.Response, error) {
				portalCalls++
				var sent map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
				assert.Equal(t, "openrouter", sent["provider"])
				assert.Equal(t, orKey, sent["api_key"])
				w := httptest.NewRecorder()
				w.WriteHeader(tc.portalCode)
				if tc.portalCode == 201 {
					_, _ = w.WriteString(`{"id":42,"alias":"openrouter-co-01","msg":"ok"}`)
				} else {
					_, _ = w.WriteString(`{"detail":"duplicate key ` + orKey + `"}`)
				}
				return w.Result(), nil
			})}

			encoded, err := json.Marshal(gin.H{
				"provider": supplierCFOpenRouterProvider, "model": "fixture-model", "api_key": cfKey,
				"cf_account_id": " " + tc.cfAccountID + " ", "account_type": 0, "tpm": "1000", "rpm": "10",
			})
			require.NoError(t, err)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set("role", minAdminRole)
			c.Set("user_id", int64(7))
			c.Set("username", "alice")
			c.Set("studio", "Studio A")
			c.Request = httptest.NewRequest(http.MethodPost, "/api/supplier-account/accounts", bytes.NewReader(encoded))
			c.Request.Header.Set("Content-Type", "application/json")
			handleSupplierAccountCreate(c)

			require.Equal(t, tc.wantStatus, w.Code, w.Body.String())
			assert.Equal(t, tc.wantORCalls, calls)
			if tc.portalCode != 0 {
				assert.Equal(t, 1, portalCalls)
			} else {
				assert.Zero(t, portalCalls)
			}
			if byokBody != nil {
				assert.Equal(t, "cloudflare", byokBody["provider"])
				key, ok := byokBody["key"].(string)
				require.True(t, ok, "BYOK key must be a JSON string, got %T", byokBody["key"])
				var credential map[string]string
				require.NoError(t, json.Unmarshal([]byte(key), &credential))
				assert.Equal(t, map[string]string{"accountId": cfAccount, "apiToken": cfKey}, credential)
				assert.Equal(t, []any{orHash}, byokBody["allowed_api_key_hashes"])
				assert.Equal(t, true, byokBody["is_byok_only"])
				assert.Contains(t, byokBody["name"], "alice")
			}
			if tc.wantErr != "" {
				assert.Contains(t, w.Body.String(), tc.wantErr)
			}
			visible := logs.String() + w.Body.String()
			for _, secret := range []string{cfKey, orKey, mgmtKey} {
				assert.NotContains(t, visible, secret)
			}
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSupplierOpenRouterSettingsMasked(t *testing.T) {
	previousDB := db
	t.Cleanup(func() { db = previousDB })
	pool, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	db = pool
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery("SELECT value FROM report_config").WithArgs(cfgOpenRouterMgmtKey).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("sk-or-v1-management-secret-9876"))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/supplier-account/settings", nil)
	handleSupplierSettingsGet(c)

	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), "management-secret")
	var resp struct {
		Set   bool   `json:"openrouter_mgmt_key_set"`
		Last4 string `json:"openrouter_mgmt_key_last4"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Set)
	assert.Equal(t, "9876", resp.Last4)
}

func TestSupplierAccountProxyURL(t *testing.T) {
	for _, address := range []string{
		"http://proxy.invalid",
		"https://proxy.invalid:443/",
		"socks5h://[2001:db8::1]:1080",
	} {
		assert.True(t, validSupplierAccountProxyURL(address), "valid proxy address %q", address)
	}
	for _, address := range []string{
		"file://proxy.invalid:80", "socks5://:1080", "socks5://proxy.invalid",
		"http://proxy.invalid:", "http://proxy.invalid:0", "socks5://proxy.invalid:65536",
		"http://bad]host", "socks5://proxy.invalid:1080/path", "http://proxy.invalid?q=1",
		"http://proxy.invalid#fragment", "http://u:bad pass@proxy.invalid", "http://u:%GG@proxy.invalid",
	} {
		assert.False(t, validSupplierAccountProxyURL(address), "invalid proxy address %q", address)
	}
}

// supplier2ConfigMock stubs one read of each given report_config key; other
// keys read as unset.
func supplier2ConfigMock(t *testing.T, values map[string]string) sqlmock.Sqlmock {
	t.Helper()
	previousDB := db
	t.Cleanup(func() { db = previousDB })
	pool, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	db = pool
	mock.MatchExpectationsInOrder(false)
	for key, value := range values {
		mock.ExpectQuery("SELECT value FROM report_config").WithArgs(key).
			WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(value))
	}
	return mock
}

func TestSupplier2AdminOnly(t *testing.T) {
	for _, tc := range []struct {
		method  string
		handler gin.HandlerFunc
	}{
		{http.MethodGet, handleSupplier2SettingsGet},
		{http.MethodPut, handleSupplier2SettingsSet},
		{http.MethodGet, handleSupplier2Accounts},
		{http.MethodPost, handleSupplier2Metrics},
		{http.MethodPut, handleSupplier2OwnerSet},
		{http.MethodGet, handleSupplier2ReportSettingsGet},
		{http.MethodPut, handleSupplier2ReportSettingsSet},
		{http.MethodPost, handleSupplier2ReportPush},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("role", minAdminRole-1)
		c.Request = httptest.NewRequest(tc.method, "/api/supplier-account/secondary/x", bytes.NewReader([]byte(`{"password":"p"}`)))
		tc.handler(c)
		assert.Equal(t, http.StatusForbidden, w.Code)
	}
}

func TestSupplier2Settings(t *testing.T) {
	t.Run("GET never returns the password or token", func(t *testing.T) {
		previousOrigin := supplierAccountBaseURL
		t.Cleanup(func() { supplierAccountBaseURL = previousOrigin })
		supplierAccountBaseURL = "https://portal.invalid"
		supplier2ConfigMock(t, map[string]string{
			cfgSupplier2Username: "second-user",
			cfgSupplier2Password: "fixture-password-secret",
			cfgSupplier2Token:    "fixture-openapi-token-7788",
		})
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("role", minAdminRole)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/supplier-account/secondary/settings", nil)
		handleSupplier2SettingsGet(c)

		require.Equal(t, http.StatusOK, w.Code)
		assert.NotContains(t, w.Body.String(), "fixture-password-secret")
		assert.NotContains(t, w.Body.String(), "fixture-openapi-token")
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "账号 2", resp["label"])
		assert.Equal(t, "second-user", resp["username"])
		assert.Equal(t, true, resp["password_set"])
		assert.Equal(t, true, resp["openapi_token_set"])
		assert.Equal(t, "7788", resp["openapi_token_last4"])
	})

	t.Run("PUT with blank password and token keeps them", func(t *testing.T) {
		mock := supplier2ConfigMock(t, nil)
		mock.ExpectExec("INSERT INTO report_config").WithArgs(cfgSupplier2Username, "second-user", sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("INSERT INTO report_config").WithArgs(cfgSupplier2Label, "备用号池", sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		supplier2Web.token, supplier2Web.expiry = "cached", time.Now().Add(time.Hour)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("role", minAdminRole)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/supplier-account/secondary/settings",
			bytes.NewReader([]byte(`{"label":" 备用号池 ","username":" second-user ","password":"","openapi_token":"  "}`)))
		c.Request.Header.Set("Content-Type", "application/json")
		handleSupplier2SettingsSet(c)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.NoError(t, mock.ExpectationsWereMet())
		assert.Empty(t, supplier2Web.token, "a settings change drops the cached login")
	})
}

func TestSupplier2NotConfigured(t *testing.T) {
	supplier2ConfigMock(t, nil)
	for _, handler := range []gin.HandlerFunc{handleSupplier2Accounts, handleSupplier2Metrics} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("role", minAdminRole)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/supplier-account/secondary/x", nil)
		handler(c)
		require.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, `{"configured":false,"error":"账号 2 未配置：请先填写用户名和密码"}`, w.Body.String())
	}
}

func TestSupplier2Metrics(t *testing.T) {
	previousClient, previousOrigin := supplierHTTPClient, supplierAccountBaseURL
	t.Cleanup(func() {
		supplierHTTPClient, supplierAccountBaseURL = previousClient, previousOrigin
		supplier2Web.invalidate()
	})
	supplierAccountBaseURL = "https://portal.invalid"
	supplier2ConfigMock(t, map[string]string{
		cfgSupplier2Username: "second-user",
		cfgSupplier2Password: "fixture-password",
		cfgSupplier2Token:    "fixture-second-token",
	})
	supplier2Web.token, supplier2Web.expiry = "web-token", time.Now().Add(time.Hour)
	// An 8.5-day range ending in the past, in portal time.
	day := time.Now().In(supplierPortalLoc).AddDate(0, 0, -10)
	begin := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, supplierPortalLoc)
	split, end := begin.AddDate(0, 0, 7), begin.Add(204*time.Hour)

	var windows [][2]string
	supplierHTTPClient = &http.Client{Transport: supplierPortalTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		switch r.URL.Path {
		case "/supplier-account/api/supplier/accounts":
			assert.Equal(t, "Bearer web-token", r.Header.Get("Authorization"))
			_, _ = w.WriteString(`{"list":[
				{"id":11,"account_alias":"openai-co-01","provider":"openai","status":"active","api_key":"sk-full-secret-11112222"},
				{"id":12,"account_alias":"claude-co-02","provider":"claude","status":"disabled","api_key":"sk-full-secret-33334444"}]}`)
		case "/supplier-account/api/openapi/metrics/query":
			assert.Equal(t, "Bearer fixture-second-token", r.Header.Get("Authorization"))
			var q struct {
				IDs   []int64 `json:"account_ids"`
				Begin string  `json:"begin_time"`
				End   string  `json:"end_time"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&q))
			assert.Equal(t, []int64{11, 12}, q.IDs)
			windows = append(windows, [2]string{q.Begin, q.End})
			// The portal keys rows by its internal aid; alias maps them back.
			_, _ = w.WriteString(`{"accounts":[{"aid":901,"account_alias":"openai-co-01","status":"ok","requests":10,
				"success_rate":0.5,"prompt_tokens":100,"completion_tokens":20}]}`)
		case "/supplier-account/api/openapi/bill/query":
			var q map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&q))
			assert.Equal(t, begin.Format("2006-01-02"), q["start_ds"])
			_, _ = w.WriteString(`{"list":[{"id":11,"cost":72}]}`)
		default:
			t.Errorf("unexpected portal call %s", r.URL.Path)
		}
		return w.Result(), nil
	})}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("role", minAdminRole)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/supplier-account/secondary/metrics",
		bytes.NewReader([]byte(`{"begin_time":"`+begin.Format(supplier2TimeLayout)+`","end_time":"`+end.Format(supplier2TimeLayout)+`"}`)))
	c.Request.Header.Set("Content-Type", "application/json")
	handleSupplier2Metrics(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, [][2]string{
		{begin.Format(supplier2TimeLayout), split.Format(supplier2TimeLayout)},
		{split.Format(supplier2TimeLayout), end.Format(supplier2TimeLayout)},
	}, windows, "an 8.5-day range is fetched in ≤7-day windows")
	assert.NotContains(t, w.Body.String(), "sk-full-secret")
	var resp struct {
		Accounts []struct {
			AID         int64    `json:"aid"`
			Requests    int64    `json:"requests"`
			SuccessRate float64  `json:"success_rate"`
			Cost        *float64 `json:"cost"`
			CostUSD     *float64 `json:"cost_usd"`
		} `json:"accounts"`
		Summary struct {
			Total struct {
				Accounts     int   `json:"accounts"`
				Requests     int64 `json:"requests"`
				PromptTokens int64 `json:"prompt_tokens"`
			} `json:"total"`
		} `json:"summary"`
		TotalCostUSD float64 `json:"total_cost_usd"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Accounts, 1)
	assert.Equal(t, int64(11), resp.Accounts[0].AID)
	assert.Equal(t, int64(20), resp.Accounts[0].Requests)
	assert.InDelta(t, 0.5, resp.Accounts[0].SuccessRate, 1e-9)
	require.NotNil(t, resp.Accounts[0].Cost)
	assert.InDelta(t, 72, *resp.Accounts[0].Cost, 1e-9)
	require.NotNil(t, resp.Accounts[0].CostUSD)
	assert.InDelta(t, 10, *resp.Accounts[0].CostUSD, 1e-9)
	assert.Equal(t, 2, resp.Summary.Total.Accounts)
	assert.Equal(t, int64(20), resp.Summary.Total.Requests)
	assert.Equal(t, int64(200), resp.Summary.Total.PromptTokens)
	assert.InDelta(t, 10, resp.TotalCostUSD, 1e-9)
}

func TestSupplier2Mine(t *testing.T) {
	previousClient, previousOrigin := supplierHTTPClient, supplierAccountBaseURL
	t.Cleanup(func() {
		supplierHTTPClient, supplierAccountBaseURL = previousClient, previousOrigin
		supplier2Web.invalidate()
	})
	supplierAccountBaseURL = "https://portal.invalid"

	var metricIDs, billIDs []any
	portalCalls := 0
	supplierHTTPClient = &http.Client{Transport: supplierPortalTransport(func(r *http.Request) (*http.Response, error) {
		portalCalls++
		w := httptest.NewRecorder()
		var q map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&q)
		}
		switch r.URL.Path {
		case "/supplier-account/api/supplier/accounts":
			_, _ = w.WriteString(`{"list":[
				{"id":11,"account_alias":"openai-mine-01","provider":"openai","remark":"liyinzhang-GW T6-01","api_key":"sk-full-secret-11112222"},
				{"id":12,"account_alias":"openai-other-02","provider":"openai","remark":"bob-02","api_key":"sk-full-secret-33334444"}]}`)
		case "/supplier-account/api/openapi/metrics/query":
			metricIDs = q["account_ids"].([]any)
			_, _ = w.WriteString(`{"accounts":[{"aid":901,"account_alias":"openai-mine-01","requests":10,"success_rate":0.5,
				"prompt_tokens":100,"completion_tokens":20}]}`)
		case "/supplier-account/api/openapi/bill/query":
			billIDs = q["account_ids"].([]any)
			_, _ = w.WriteString(`{"list":[{"id":11,"cost":72}]}`)
		default:
			t.Errorf("unexpected portal call %s", r.URL.Path)
		}
		return w.Result(), nil
	})}

	call := func(t *testing.T, role int, owned []int64, handler gin.HandlerFunc, method string) (int, string) {
		t.Helper()
		mock := supplier2ConfigMock(t, map[string]string{
			cfgSupplier2Username: "second-user",
			cfgSupplier2Password: "fixture-password",
			cfgSupplier2Token:    "fixture-second-token",
		})
		rows := sqlmock.NewRows([]string{"remote_account_id", "user_id", "username"})
		for _, id := range owned {
			rows.AddRow(id, int64(7), "liyinzhang")
		}
		mock.ExpectQuery(`FROM rs_supplier2_owner o LEFT JOIN rs_auth_user u ON u.id = o.user_id WHERE o.user_id=\$1`).
			WithArgs(int64(7)).WillReturnRows(rows)
		supplier2Web.token, supplier2Web.expiry = "web-token", time.Now().Add(time.Hour)
		metricIDs, billIDs, portalCalls = nil, nil, 0

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("role", role)
		c.Set("user_id", int64(7))
		c.Request = httptest.NewRequest(method, "/api/supplier-account/secondary/mine", nil)
		handler(c)
		return w.Code, w.Body.String()
	}

	t.Run("lists only the caller's accounts", func(t *testing.T) {
		code, body := call(t, 1, []int64{11}, handleSupplier2MyAccounts, http.MethodGet)
		require.Equal(t, http.StatusOK, code, body)
		var resp struct {
			Accounts []map[string]any `json:"accounts"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &resp))
		require.Len(t, resp.Accounts, 1)
		assert.EqualValues(t, 11, resp.Accounts[0]["id"])
		assert.NotContains(t, body, "bob-02")
		assert.NotContains(t, body, "sk-full-secret")
	})

	t.Run("no assignment means nothing and no portal call", func(t *testing.T) {
		code, body := call(t, 1, nil, handleSupplier2MyMetrics, http.MethodPost)
		require.Equal(t, http.StatusOK, code, body)
		assert.Zero(t, portalCalls)
		assert.Contains(t, body, `"accounts":[]`)
	})

	t.Run("non-admin usage covers own accounts without cost", func(t *testing.T) {
		code, body := call(t, 1, []int64{11}, handleSupplier2MyMetrics, http.MethodPost)
		require.Equal(t, http.StatusOK, code, body)
		assert.Equal(t, []any{float64(11)}, metricIDs, "only the caller's account is queried")
		assert.Equal(t, []any{float64(11)}, billIDs)
		for _, field := range []string{`"cost"`, `"cost_usd"`, `"total_cost_usd"`, `"fx_rate"`} {
			assert.NotContains(t, body, field)
		}
		var resp struct {
			Accounts []struct {
				AID      int64 `json:"aid"`
				Requests int64 `json:"requests"`
			} `json:"accounts"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &resp))
		require.Len(t, resp.Accounts, 1)
		assert.Equal(t, int64(11), resp.Accounts[0].AID)
		assert.Equal(t, int64(10), resp.Accounts[0].Requests)
	})

	t.Run("admin keeps cost on their own view", func(t *testing.T) {
		code, body := call(t, minAdminRole, []int64{11}, handleSupplier2MyMetrics, http.MethodPost)
		require.Equal(t, http.StatusOK, code, body)
		assert.Contains(t, body, `"cost_usd":10`)
	})
}

func TestSupplier2OwnerSet(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		setup      func(sqlmock.Sqlmock)
		wantStatus int
		wantBody   string
	}{
		{name: "assigns an existing user", body: `{"user_id":7}`, wantStatus: 200, wantBody: `"owner_username":"liyinzhang"`,
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectQuery("SELECT username FROM rs_auth_user").WithArgs(int64(7)).
					WillReturnRows(sqlmock.NewRows([]string{"username"}).AddRow("liyinzhang"))
				m.ExpectExec("INSERT INTO rs_supplier2_owner").WithArgs(int64(11), int64(7), sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(0, 1))
			}},
		{name: "unknown user is rejected", body: `{"user_id":99}`, wantStatus: 400, wantBody: "用户不存在",
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectQuery("SELECT username FROM rs_auth_user").WithArgs(int64(99)).
					WillReturnRows(sqlmock.NewRows([]string{"username"}))
			}},
		{name: "null clears the owner", body: `{"user_id":null}`, wantStatus: 200, wantBody: `"owner_user_id":null`,
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectExec("DELETE FROM rs_supplier2_owner").WithArgs(int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := supplier2ConfigMock(t, nil)
			tc.setup(mock)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set("role", minAdminRole)
			c.Params = gin.Params{{Key: "id", Value: "11"}}
			c.Request = httptest.NewRequest(http.MethodPut, "/api/supplier-account/secondary/accounts/11/owner", bytes.NewReader([]byte(tc.body)))
			c.Request.Header.Set("Content-Type", "application/json")
			handleSupplier2OwnerSet(c)
			require.Equal(t, tc.wantStatus, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), tc.wantBody)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSupplier2ReportText(t *testing.T) {
	text := supplier2ReportText(supplier2Report{
		Label:  "账号 2",
		At:     time.Date(2026, 11, 25, 10, 5, 0, 0, supplierPortalLoc),
		FxRate: 7,
		Accounts: []supplier2ReportAccount{
			{ID: 2, Name: "liyinzhang-GW T6-03", Owner: "liyinzhang", TotalCostRMB: 14},
			{ID: 3, Name: "orphan", TodayRequests: 5, TodayCostRMB: 7, TotalCostRMB: 7},
			{ID: 1, Name: "liyinzhang-GW T6-02", Owner: "liyinzhang", TodayRequests: 100, TodayCostRMB: 70, TotalCostRMB: 700},
			{ID: 4, Name: "bob-01", Owner: "bob"},
			{ID: 5, Name: "broken", Owner: "liyinzhang", TodayRequests: 999, TodayCostRMB: 999, Failed: true},
		},
	})
	assert.Equal(t, `[账号 2] 用量播报 2026-11-25 10:00 (UTC+8)
合计：4 个账号 · 今日 $11.00 · 请求 105 · 累计 $103.00

【liyinzhang】2 个账号 · 今日 $10.00 · 请求 100
· #1 liyinzhang-GW T6-02：今日 $10.00 · 请求 100 · 累计 $100.00
· 另 1 个账号今日无用量

【bob】1 个账号 · 今日 $0.00 · 请求 0
· 另 1 个账号今日无用量

【未分配】1 个账号 · 今日 $1.00 · 请求 5
· #3 orphan：今日 $1.00 · 请求 5 · 累计 $1.00

⚠ 1 个账号用量查询失败，未计入合计：#5

注：费用来自号池账单，约有 2 小时延迟；今日从 00:00 (UTC+8) 起，累计为近 90 天；汇率 7`, text)
}

func TestSupplier2ReportTick(t *testing.T) {
	at := time.Date(2026, 11, 25, 10, 5, 0, 0, supplierPortalLoc)
	config := map[string]string{cfgSupplier2ReportWebhook: "https://lark.invalid/hook/x", cfgSupplier2ReportHours: "10,22"}

	pushes := 0
	push := func(time.Time) { pushes++ }

	mock := supplier2ConfigMock(t, config)
	mock.ExpectExec("INSERT INTO report_config").WithArgs(cfgSupplier2ReportLastSlot, "2026-11-25 10", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	assert.True(t, supplier2ReportTick(at, push), "first tick in a push hour pushes")
	assert.NoError(t, mock.ExpectationsWereMet())

	withSlot := maps.Clone(config)
	withSlot[cfgSupplier2ReportLastSlot] = "2026-11-25 10"
	supplier2ConfigMock(t, withSlot)
	assert.False(t, supplier2ReportTick(at.Add(30*time.Minute), push), "the same hour is not pushed twice")

	supplier2ConfigMock(t, config)
	assert.False(t, supplier2ReportTick(at.Add(2*time.Hour), push), "hours not configured are skipped")
	assert.Equal(t, 1, pushes)
}
