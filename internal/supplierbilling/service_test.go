package supplierbilling

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const billingTestPassword = "Marble-river!coral-2026"

type billingTestClient struct {
	server  *Server
	handler http.Handler
	cookie  *http.Cookie
	csrf    string
}

func (c *billingTestClient) request(t *testing.T, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = common.Marshal(body)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.RemoteAddr = "192.0.2.20:12345"
	req.Header.Set("Origin", c.server.Origin)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "supplier-billing")
	req.Header.Set("X-CSRF-Token", c.csrf)
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	for key, value := range headers {
		if value == "" {
			req.Header.Del(key)
		} else {
			req.Header.Set(key, value)
		}
	}
	response := httptest.NewRecorder()
	c.handler.ServeHTTP(response, req)
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == c.server.cookieName() {
			c.cookie = cookie
		}
	}
	var session struct {
		CSRFToken string `json:"csrf_token"`
	}
	if common.Unmarshal(response.Body.Bytes(), &session) == nil && session.CSRFToken != "" {
		c.csrf = session.CSRFToken
	}
	return response
}

func billingResponse[T any](t *testing.T, response *httptest.ResponseRecorder, status int) T {
	t.Helper()
	require.Equal(t, status, response.Code, response.Body.String())
	var value T
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &value))
	return value
}

func billingSessionClient(t *testing.T, s *Server, user User) billingTestClient {
	t.Helper()
	session, token, err := newBillingSession(user.ID)
	require.NoError(t, err)
	require.NoError(t, s.DB.Create(&session).Error)
	return billingTestClient{server: s, handler: s.Handler(""), cookie: s.sessionCookie(token, session.ExpiresAt), csrf: session.CSRFToken}
}

type billingTestStatement struct {
	Balance        string                  `json:"balance"`
	OpeningBalance string                  `json:"opening_balance"`
	TotalUsage     string                  `json:"total_usage"`
	TotalRecharges string                  `json:"total_recharges"`
	GatewayUsage   string                  `json:"gateway_usage"`
	UnpricedUsage  string                  `json:"unpriced_usage"`
	Days           []DailyStatement        `json:"days"`
	Usage          []Usage                 `json:"usage"`
	Recharges      []Recharge              `json:"recharges"`
	Ledger         []LedgerEntry           `json:"ledger"`
	SourceStatus   []StatementSourceStatus `json:"source_status"`
}

type billingTestVendor struct {
	ID           int64  `json:"id"`
	SupplierID   int64  `json:"supplier_id"`
	Name         string `json:"name"`
	SourceStudio string `json:"source_studio"`
	KeyCount     int64  `json:"key_count"`
}

type billingTestVendorUsage struct {
	VendorID     int64  `json:"vendor_id"`
	VendorName   string `json:"vendor_name"`
	GatewayUsage string `json:"gateway_usage"`
	RequestCount string `json:"request_count"`
}

type billingTestHeadUsage struct {
	SupplierID    int64                    `json:"supplier_id"`
	SupplierName  string                   `json:"supplier_name"`
	GatewayUsage  string                   `json:"gateway_usage"`
	RequestCount  string                   `json:"request_count"`
	SourcesSynced bool                     `json:"sources_synced"`
	Vendors       []billingTestVendorUsage `json:"vendors"`
}

type billingTestSummaryDay struct {
	Date         string                 `json:"date"`
	GatewayUsage string                 `json:"gateway_usage"`
	RequestCount string                 `json:"request_count"`
	Heads        []billingTestHeadUsage `json:"heads"`
}

type billingTestDailySummary struct {
	Timezone string                  `json:"timezone"`
	Start    string                  `json:"start"`
	End      string                  `json:"end"`
	Heads    []Supplier              `json:"heads"`
	Totals   []billingTestHeadUsage  `json:"totals"`
	Days     []billingTestSummaryDay `json:"days"`
}

func billingDatabaseFixture(t *testing.T, engine, env string) *gorm.DB {
	t.Helper()
	dsn := os.Getenv(env)
	if engine == "sqlite" {
		dsn = filepath.Join(t.TempDir(), "billing.sqlite")
	} else if dsn == "" {
		t.Skip("Set " + env + " to a dedicated disposable test database")
	}
	db, err := OpenDatabase(engine, dsn)
	require.NoError(t, err)
	versionQuery := "SELECT VERSION()"
	if engine == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	var databaseVersion string
	require.NoError(t, db.Raw(versionQuery).Scan(&databaseVersion).Error)
	t.Logf("Database version: %s", databaseVersion)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	// Only these service-owned tables in the explicitly selected test DB
	// are reset. The matrix must never use a production DSN.
	if engine != "sqlite" {
		for _, model := range append(AuthModels(), &InitialBalance{}, &Claim{}, &SourceSync{}, &SourceChannel{}, &VendorKey{}, &Vendor{}, &LedgerEntry{}, &Recharge{}, &Usage{}, &Source{}, &User{}, &Supplier{}) {
			require.NoError(t, db.Migrator().DropTable(model))
		}
	}
	return db
}

func TestServiceDatabaseMatrix(t *testing.T) {
	for _, engine := range []struct{ name, env string }{
		{"sqlite", ""}, {"mysql", "BILLING_TEST_MYSQL_DSN"}, {"postgres", "BILLING_TEST_POSTGRES_DSN"},
	} {
		t.Run(engine.name, func(t *testing.T) {
			db := billingDatabaseFixture(t, engine.name, engine.env)
			s := &Server{DB: db, Origin: "https://billing.example.test", SecureCookies: true}
			require.NoError(t, s.Migrate())
			require.NoError(t, s.Migrate())
			hash, err := hashBillingPassword(billingTestPassword)
			require.NoError(t, err)
			supplier := Supplier{Name: "供应商甲", Currency: "CNY"}
			other := Supplier{Name: "Supplier B", Currency: "CNY"}
			require.NoError(t, db.Create(&supplier).Error)
			require.NoError(t, db.Create(&other).Error)
			managerUser := User{Username: "billing-manager", PasswordHash: hash, Role: RoleManager}
			supplierUser := User{Username: "supplier-a", PasswordHash: hash, Role: RoleSupplier, SupplierID: supplier.ID}
			require.NoError(t, db.Create(&managerUser).Error)
			require.NoError(t, db.Create(&supplierUser).Error)
			manager := billingSessionClient(t, s, managerUser)
			vendor := billingSessionClient(t, s, supplierUser)

			// Post prepaid funds and supplier supplements, then reverse the funds.
			{
				today := time.Now().In(businessZone).Format("2006-01-02")
				rechargeBody := map[string]any{"supplier_id": supplier.ID, "date": today, "amount": "1000", "reference": "transfer-001", "idempotency_key": "recharge-001"}
				recharge := billingResponse[Recharge](t, manager.request(t, "POST", "/api/recharges", rechargeBody, nil), 201)
				retry := billingResponse[Recharge](t, manager.request(t, "POST", "/api/recharges", rechargeBody, nil), 200)
				assert.Equal(t, recharge.ID, retry.ID)
				rechargeBody["amount"] = "2000"
				assert.Equal(t, 409, manager.request(t, "POST", "/api/recharges", rechargeBody, nil).Code)
				assert.Equal(t, 403, vendor.request(t, "POST", "/api/recharges", rechargeBody, nil).Code)
				usageBody := map[string]any{"supplier_id": supplier.ID, "date": today, "usage_type": "Other usage", "description": "Supplier supplement", "quantity": "10", "unit": "items", "unit_price": "2", "currency": "USD", "discount": "0.88", "fx_rate": "7.2", "idempotency_key": "manual-001"}
				usage := billingResponse[Usage](t, vendor.request(t, "POST", "/api/usage", usageBody, nil), 201)
				assert.Equal(t, "126.720000", usage.Amount)
				assert.Equal(t, "manual", usage.Origin)
				retried := billingResponse[Usage](t, vendor.request(t, "POST", "/api/usage", usageBody, nil), 200)
				assert.Equal(t, usage.ID, retried.ID)
				usageBody["quantity"] = "11"
				assert.Equal(t, 409, vendor.request(t, "POST", "/api/usage", usageBody, nil).Code)
				usageBody["supplier_id"] = other.ID
				assert.Equal(t, 403, vendor.request(t, "POST", "/api/usage", usageBody, nil).Code)
				statementURL := fmt.Sprintf("/api/statement?supplier_id=%d&start=%s&end=%s", supplier.ID, today, today)
				statement := billingResponse[billingTestStatement](t, vendor.request(t, "GET", statementURL, nil, nil), 200)
				assert.Equal(t, "873.280000", statement.Balance)
				assert.Equal(t, "126.720000", statement.TotalUsage)
				require.Len(t, statement.Days, 1)
				assert.Equal(t, "873.280000", statement.Days[0].ClosingBalance)
				assert.Len(t, statement.Ledger, 2)
				assert.Len(t, statement.Usage, 1)
				assert.Equal(t, 403, vendor.request(t, "GET", fmt.Sprintf("/api/statement?supplier_id=%d&start=%s&end=%s", other.ID, today, today), nil, nil).Code)
				visible := billingResponse[[]Supplier](t, vendor.request(t, "GET", "/api/suppliers", nil, nil), 200)
				require.Len(t, visible, 1)
				assert.Equal(t, supplier.ID, visible[0].ID)
				reversalURL := fmt.Sprintf("/api/recharges/%d/reverse", recharge.ID)
				assert.Equal(t, 200, manager.request(t, "POST", reversalURL, map[string]string{"note": "Payment was returned"}, nil).Code)
				assert.Equal(t, 200, manager.request(t, "POST", reversalURL, map[string]string{"note": "Payment was returned"}, nil).Code)
				statement = billingResponse[billingTestStatement](t, vendor.request(t, "GET", statementURL, nil, nil), 200)
				assert.Equal(t, "-126.720000", statement.Balance)
				assert.Equal(t, "0.000000", statement.TotalRecharges)
				assert.Len(t, statement.Ledger, 3)
			}

			// Import source facts, preserve pricing, and reject incomplete refreshes.
			{
				t.Setenv("SUPPLIER_BILLING_SOURCE_TEST", "test-only-source-secret")
				var responseBody atomic.Value
				var responseStatus atomic.Int32
				responseStatus.Store(200)
				rows := []map[string]any{
					reportUsageFixture("2024-01-01 16:00", "10", 7),
					reportUsageFixture("2024-01-02 15:00", "2", 7),
					reportUsageFixture("2024-01-02 16:00", "900", 7),
					reportUsageFixture("2024-01-01 15:00", "900", 7),
					reportUsageFixture("2024-01-02 10:00", "900", 8),
				}
				otherUserRow := reportUsageFixture("2024-01-01 16:00", "900", 7)
				otherUserRow["user_id"] = 2
				rows = append(rows, otherUserRow)
				data, err := common.Marshal(rows)
				require.NoError(t, err)
				responseBody.Store(string(data))
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, "/report/api/report", r.URL.Path)
					assert.Equal(t, "2024-01-01", r.URL.Query().Get("start"))
					assert.Equal(t, "2024-01-02", r.URL.Query().Get("end"))
					assert.Equal(t, "test-only-source-secret", r.Header.Get("X-API-Key"))
					w.WriteHeader(int(responseStatus.Load()))
					_, _ = io.WriteString(w, responseBody.Load().(string))
				}))
				defer upstream.Close()
				s.SourceOrigins = []string{upstream.URL}
				sourceBody := Source{SupplierID: other.ID, Name: "Dev OpenAI", Kind: "report", BaseURL: upstream.URL + "/report", CredentialEnv: "SUPPLIER_BILLING_SOURCE_TEST", ChannelIDs: "7", GatewayUserID: 1, UsageType: "OpenAI", Currency: "USD", Discount: "0.88", FxRate: "7.2"}
				source := billingResponse[Source](t, manager.request(t, "POST", "/api/sources", sourceBody, nil), 201)
				assert.Equal(t, int64(1), source.GatewayUserID)
				listed := billingResponse[[]Source](t, manager.request(t, "GET", "/api/sources", nil, nil), 200)
				require.Len(t, listed, 1)
				assert.Equal(t, source.ID, listed[0].ID)
				assert.Equal(t, int64(1), listed[0].GatewayUserID)
				for _, gatewayUserID := range []int64{-1, 9007199254740992} {
					invalid := sourceBody
					invalid.GatewayUserID = gatewayUserID
					assert.Equal(t, 400, manager.request(t, "POST", "/api/sources", invalid, nil).Code)
				}
				for _, gatewayUserID := range []int64{0, 2, -1, 9007199254740992} {
					changed := sourceBody
					changed.GatewayUserID = gatewayUserID
					assert.Equal(t, 400, manager.request(t, "PUT", fmt.Sprintf("/api/sources/%d", source.ID), changed, nil).Code)
				}
				overlap := sourceBody
				overlap.SupplierID = supplier.ID
				assert.Equal(t, 409, manager.request(t, "POST", "/api/sources", overlap, nil).Code)
				// An all-users scope must not make an already assigned channel billable
				// twice; distinct user scopes are covered by the purchase block below.
				overlap.GatewayUserID = 0
				assert.Equal(t, 409, manager.request(t, "POST", "/api/sources", overlap, nil).Code)
				overlap.GatewayUserID = 1
				overlap.BaseURL = upstream.URL + "/r%65port"
				assert.Equal(t, 409, manager.request(t, "POST", "/api/sources", overlap, nil).Code)
				overlap.BaseURL = upstream.URL + "/report?"
				assert.Equal(t, 409, manager.request(t, "POST", "/api/sources", overlap, nil).Code)
				assert.Equal(t, 403, vendor.request(t, "GET", "/api/sources", nil, nil).Code)
				syncURL := fmt.Sprintf("/api/sources/%d/sync", source.ID)
				dateRange := map[string]string{"start": "2024-01-02", "end": "2024-01-02"}
				statementURL := fmt.Sprintf("/api/statement?supplier_id=%d&start=2024-01-02&end=2024-01-02", other.ID)
				unknown := billingResponse[billingTestStatement](t, manager.request(t, "GET", statementURL, nil, nil), 200)
				require.Len(t, unknown.Days, 1)
				assert.False(t, unknown.Days[0].SourcesSynced)
				require.Len(t, unknown.SourceStatus, 1)
				assert.Equal(t, 1, unknown.SourceStatus[0].MissingDays)
				assert.False(t, unknown.SourceStatus[0].SyncFailed)
				responseStatus.Store(500)
				assert.Equal(t, 502, manager.request(t, "POST", syncURL, dateRange, nil).Code)
				failedFirst := billingResponse[billingTestStatement](t, manager.request(t, "GET", statementURL, nil, nil), 200)
				assert.False(t, failedFirst.Days[0].SourcesSynced)
				assert.True(t, failedFirst.SourceStatus[0].SyncFailed)
				assert.Zero(t, failedFirst.SourceStatus[0].LastSyncAt)
				responseStatus.Store(200)
				first := billingResponse[map[string]any](t, manager.request(t, "POST", syncURL, dateRange, nil), 200)
				assert.Equal(t, float64(2), first["created"])
				repeat := billingResponse[map[string]any](t, manager.request(t, "POST", syncURL, dateRange, nil), 200)
				assert.Equal(t, float64(0), repeat["created"])
				assert.Equal(t, float64(0), repeat["adjusted"])
				statement := billingResponse[billingTestStatement](t, manager.request(t, "GET", statementURL, nil, nil), 200)
				assert.Equal(t, "76.032000", statement.TotalUsage)
				assert.True(t, statement.Days[0].SourcesSynced)
				assert.False(t, statement.SourceStatus[0].SyncFailed)
				assert.Zero(t, statement.SourceStatus[0].MissingDays)
				assert.Positive(t, statement.SourceStatus[0].LastSyncAt)
				assert.Len(t, statement.Usage, 2)
				assert.Len(t, statement.Ledger, 2)
				for _, item := range statement.Usage {
					assert.Equal(t, "2024-01-02", item.Date)
					assert.Equal(t, "gateway_usd", item.Unit)
					assert.Equal(t, "gpt-test", item.Description)
				}
				sourceBody.Discount, sourceBody.FxRate = "0.5", "8"
				assert.Equal(t, 200, manager.request(t, "PUT", fmt.Sprintf("/api/sources/%d", source.ID), sourceBody, nil).Code)
				rows[0]["total_cost"] = json.Number("20")
				data, err = common.Marshal(rows)
				require.NoError(t, err)
				responseBody.Store(string(data))
				corrected := billingResponse[map[string]any](t, manager.request(t, "POST", syncURL, dateRange, nil), 200)
				assert.Equal(t, float64(1), corrected["adjusted"])
				statement = billingResponse[billingTestStatement](t, manager.request(t, "GET", statementURL, nil, nil), 200)
				assert.Equal(t, "139.392000", statement.TotalUsage)
				assert.Len(t, statement.Ledger, 3)
				for _, item := range statement.Usage {
					assert.Equal(t, "0.88", item.Discount)
					assert.Equal(t, "7.2", item.FxRate)
				}
				responseBody.Store("[]")
				assert.Equal(t, 409, manager.request(t, "POST", syncURL, dateRange, nil).Code)
				responseStatus.Store(500)
				responseBody.Store("test-only-source-secret upstream-private-body")
				failed := manager.request(t, "POST", syncURL, dateRange, nil)
				assert.Equal(t, 502, failed.Code)
				assert.NotContains(t, failed.Body.String(), "test-only-source-secret")
				assert.NotContains(t, failed.Body.String(), "upstream-private-body")
				unchanged := billingResponse[billingTestStatement](t, manager.request(t, "GET", statementURL, nil, nil), 200)
				assert.Equal(t, statement.Balance, unchanged.Balance)
				assert.Equal(t, statement.Ledger, unchanged.Ledger)
				assert.True(t, unchanged.SourceStatus[0].SyncFailed)
				zeroSourceBody := sourceBody
				zeroSourceBody.Name, zeroSourceBody.ChannelIDs = "Confirmed zero source", "9"
				zeroSource := billingResponse[Source](t, manager.request(t, "POST", "/api/sources", zeroSourceBody, nil), 201)
				pendingZero := billingResponse[billingTestStatement](t, manager.request(t, "GET", statementURL, nil, nil), 200)
				assert.False(t, pendingZero.Days[0].SourcesSynced)
				responseStatus.Store(200)
				responseBody.Store("[]")
				confirmedZero := billingResponse[map[string]any](t, manager.request(t, "POST", fmt.Sprintf("/api/sources/%d/sync", zeroSource.ID), dateRange, nil), 200)
				assert.Equal(t, float64(0), confirmedZero["records"])
				zeroStatementResponse := manager.request(t, "GET", statementURL, nil, nil)
				zeroStatement := billingResponse[billingTestStatement](t, zeroStatementResponse, 200)
				assert.True(t, zeroStatement.Days[0].SourcesSynced)
				assert.Equal(t, statement.Balance, zeroStatement.Balance)
				assert.Equal(t, statement.Ledger, zeroStatement.Ledger)
				require.Len(t, zeroStatement.SourceStatus, 2)
				for _, status := range zeroStatement.SourceStatus {
					assert.Zero(t, status.MissingDays)
					if status.ID == zeroSource.ID {
						assert.False(t, status.SyncFailed)
						assert.Positive(t, status.LastSyncAt)
					}
				}
				assert.NotContains(t, zeroStatementResponse.Body.String(), "credential_env")
				assert.NotContains(t, zeroStatementResponse.Body.String(), "base_url")
				assert.NotContains(t, zeroStatementResponse.Body.String(), "test-only-source-secret")
				for _, invalid := range []Source{
					{SupplierID: other.ID, Name: "Bad env", Kind: "report", BaseURL: upstream.URL, CredentialEnv: "PATH", ChannelIDs: "9", UsageType: "OpenAI", Currency: "USD", Discount: "1", FxRate: "7"},
					{SupplierID: other.ID, Name: "Bad origin", Kind: "report", BaseURL: "https://unapproved.example", CredentialEnv: "SUPPLIER_BILLING_SOURCE_TEST", ChannelIDs: "9", UsageType: "OpenAI", Currency: "USD", Discount: "1", FxRate: "7"},
				} {
					assert.Equal(t, 400, manager.request(t, "POST", "/api/sources", invalid, nil).Code)
				}
			}

			// Unpriced usage remains visible without debiting prepaid funds. Pricing
			// it later posts one charge and fixes the same immutable price snapshot.
			{
				t.Setenv("SUPPLIER_BILLING_SOURCE_UNPRICED", "test-unpriced-source-token")
				unpricedSupplier := Supplier{Name: "Awaiting supplier price", Currency: "CNY"}
				require.NoError(t, db.Create(&unpricedSupplier).Error)
				row := reportUsageFixture("2024-01-02 16:00", "10", 17)
				data, err := common.Marshal([]any{row})
				require.NoError(t, err)
				var responseBody atomic.Value
				responseBody.Store(string(data))
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, responseBody.Load().(string))
				}))
				defer upstream.Close()
				s.SourceOrigins = append(s.SourceOrigins, upstream.URL)
				sourceBody := Source{SupplierID: unpricedSupplier.ID, Name: "Usage awaiting price", Kind: "report", BaseURL: upstream.URL, CredentialEnv: "SUPPLIER_BILLING_SOURCE_UNPRICED", ChannelIDs: "17", GatewayUserID: 1, UsageType: "OpenAI", Currency: "USD", SettlementMode: "usage_only"}
				source := billingResponse[Source](t, manager.request(t, "POST", "/api/sources", sourceBody, nil), 201)
				assert.Equal(t, "usage_only", source.SettlementMode)
				assert.Empty(t, source.Discount)
				assert.Empty(t, source.FxRate)
				sourceURL := fmt.Sprintf("/api/sources/%d", source.ID)
				syncURL := sourceURL + "/sync"
				dateRange := map[string]string{"start": "2024-01-03", "end": "2024-01-03"}
				statementURL := fmt.Sprintf("/api/statement?supplier_id=%d&start=2024-01-03&end=2024-01-03", unpricedSupplier.ID)
				for range 2 {
					assert.Equal(t, 200, manager.request(t, "POST", syncURL, dateRange, nil).Code)
				}
				unpriced := billingResponse[billingTestStatement](t, manager.request(t, "GET", statementURL, nil, nil), 200)
				assert.Equal(t, "0.000000", unpriced.Balance)
				assert.Equal(t, "0.000000", unpriced.TotalUsage)
				assert.Equal(t, "10.000000", unpriced.GatewayUsage)
				assert.Equal(t, "10.000000", unpriced.UnpricedUsage)
				assert.Empty(t, unpriced.Ledger)
				require.Len(t, unpriced.Usage, 1)
				assert.Equal(t, "unpriced", unpriced.Usage[0].SettlementStatus)
				assert.Equal(t, "0.000000", unpriced.Usage[0].Amount)
				require.Len(t, unpriced.Days, 1)
				assert.Equal(t, "10.000000", unpriced.Days[0].GatewayUsage)
				assert.Equal(t, "10.000000", unpriced.Days[0].UnpricedUsage)
				assert.Equal(t, "1", unpriced.Days[0].RequestCount)
				assert.True(t, unpriced.Days[0].SourcesSynced)

				row["total_cost"], row["request_count"] = json.Number("12"), 4
				data, err = common.Marshal([]any{row})
				require.NoError(t, err)
				responseBody.Store(string(data))
				assert.Equal(t, 200, manager.request(t, "POST", syncURL, dateRange, nil).Code)
				corrected := billingResponse[billingTestStatement](t, manager.request(t, "GET", statementURL, nil, nil), 200)
				assert.Equal(t, "0.000000", corrected.Balance)
				assert.Equal(t, "12.000000", corrected.GatewayUsage)
				assert.Equal(t, "12.000000", corrected.UnpricedUsage)
				assert.Equal(t, "4", corrected.Days[0].RequestCount)
				var ledgerCount int64
				require.NoError(t, db.Model(&LedgerEntry{}).Where("supplier_id = ?", unpricedSupplier.ID).Count(&ledgerCount).Error)
				assert.Zero(t, ledgerCount, "Unpriced imports and corrections do not affect the ledger")
				sourceBody.SettlementMode = "settlement"
				assert.Equal(t, 400, manager.request(t, "PUT", sourceURL, sourceBody, nil).Code)
				sourceBody.Discount, sourceBody.FxRate = "0.8", "7.2"
				assert.Equal(t, 200, manager.request(t, "PUT", sourceURL, sourceBody, nil).Code)
				for range 2 {
					assert.Equal(t, 200, manager.request(t, "POST", syncURL, dateRange, nil).Code)
				}
				settled := billingResponse[billingTestStatement](t, manager.request(t, "GET", statementURL, nil, nil), 200)
				assert.Equal(t, "-69.120000", settled.Balance)
				assert.Equal(t, "69.120000", settled.TotalUsage)
				assert.Equal(t, "12.000000", settled.GatewayUsage)
				assert.Equal(t, "0.000000", settled.UnpricedUsage)
				require.Len(t, settled.Usage, 1)
				assert.Equal(t, unpriced.Usage[0].ID, settled.Usage[0].ID)
				assert.Equal(t, "settled", settled.Usage[0].SettlementStatus)
				assert.Equal(t, "0.8", settled.Usage[0].Discount)
				assert.Equal(t, "7.2", settled.Usage[0].FxRate)
				require.Len(t, settled.Ledger, 1)
				assert.Equal(t, "usage", settled.Ledger[0].Kind)
				require.NoError(t, db.Model(&LedgerEntry{}).Where("supplier_id = ?", unpricedSupplier.ID).Count(&ledgerCount).Error)
				assert.Equal(t, int64(1), ledgerCount)

				sourceBody.Discount, sourceBody.FxRate = "0.5", "8"
				assert.Equal(t, 200, manager.request(t, "PUT", sourceURL, sourceBody, nil).Code)
				assert.Equal(t, 200, manager.request(t, "POST", syncURL, dateRange, nil).Code)
				unchanged := billingResponse[billingTestStatement](t, manager.request(t, "GET", statementURL, nil, nil), 200)
				assert.Equal(t, settled.Balance, unchanged.Balance)
				assert.Equal(t, settled.Ledger, unchanged.Ledger)
				row["total_cost"] = json.Number("13")
				data, err = common.Marshal([]any{row})
				require.NoError(t, err)
				responseBody.Store(string(data))
				assert.Equal(t, 200, manager.request(t, "POST", syncURL, dateRange, nil).Code)
				adjusted := billingResponse[billingTestStatement](t, manager.request(t, "GET", statementURL, nil, nil), 200)
				assert.Equal(t, "-74.880000", adjusted.Balance)
				assert.Equal(t, "0.8", adjusted.Usage[0].Discount)
				assert.Equal(t, "7.2", adjusted.Usage[0].FxRate)
				assert.Len(t, adjusted.Ledger, 2)
			}

			// Settlement sheet: an editable initial balance, per-day CNY prices
			// converted at CNY per USD, supplier claims and the manager overview.
			{
				t.Setenv("SUPPLIER_BILLING_SOURCE_SHEET", "test-sheet-source-token")
				sheetSupplier := Supplier{Name: "皮皮", Currency: "USD"}
				require.NoError(t, db.Create(&sheetSupplier).Error)
				data, err := common.Marshal([]any{reportUsageFixture("2024-01-02 03:00", "100", 27), reportUsageFixture("2024-01-02 05:00", "22.4076", 27)})
				require.NoError(t, err)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write(data)
				}))
				defer upstream.Close()
				s.SourceOrigins = append(s.SourceOrigins, upstream.URL)
				sourceBody := Source{SupplierID: sheetSupplier.ID, Name: "号池", Kind: "report", BaseURL: upstream.URL, CredentialEnv: "SUPPLIER_BILLING_SOURCE_SHEET", ChannelIDs: "27", GatewayUserID: 1, UsageType: "openai 号池", Currency: "USD", SettlementMode: "usage_only"}
				source := billingResponse[Source](t, manager.request(t, "POST", "/api/sources", sourceBody, nil), 201)
				assert.Equal(t, 200, manager.request(t, "POST", fmt.Sprintf("/api/sources/%d/sync", source.ID), map[string]string{"start": "2024-01-02", "end": "2024-01-02"}, nil).Code)

				initialURL := fmt.Sprintf("/api/suppliers/%d/initial-balance", sheetSupplier.ID)
				initial := map[string]any{"date": "2024-01-01", "amount": "39234.22", "note": "Carried over", "revision": 0}
				first := billingResponse[InitialBalance](t, manager.request(t, "PUT", initialURL, initial, nil), 200)
				assert.Equal(t, int64(1), first.Revision)
				assert.Equal(t, 409, manager.request(t, "PUT", initialURL, initial, nil).Code, "A stale revision cannot replace the current balance")
				initial["amount"], initial["revision"] = "-100", 1
				assert.Equal(t, "-100.000000", billingResponse[InitialBalance](t, manager.request(t, "PUT", initialURL, initial, nil), 200).Amount)
				initial["amount"], initial["revision"] = "39234.22", 2
				assert.Equal(t, 200, manager.request(t, "PUT", initialURL, initial, nil).Code)
				initial["amount"], initial["revision"] = "1e3", 3
				assert.Equal(t, 400, manager.request(t, "PUT", initialURL, initial, nil).Code)
				assert.Equal(t, 403, vendor.request(t, "PUT", initialURL, initial, nil).Code)

				price := map[string]any{"supplier_id": sheetSupplier.ID, "date": "2024-01-02", "usage_type": "openai 号池", "unit_price": "3.9", "currency": "CNY", "fx_rate": "6.7"}
				assert.Equal(t, 403, vendor.request(t, "POST", "/api/usage/price", price, nil).Code, "A supplier cannot price another supplier's usage")
				priced := billingResponse[map[string]any](t, manager.request(t, "POST", "/api/usage/price", price, nil), 200)
				assert.Equal(t, float64(2), priced["priced"])
				repeat := billingResponse[map[string]any](t, manager.request(t, "POST", "/api/usage/price", price, nil), 200)
				assert.Equal(t, float64(0), repeat["priced"], "Re-applying the same price posts nothing")
				price["usage_type"] = "missing line"
				assert.Equal(t, 404, manager.request(t, "POST", "/api/usage/price", price, nil).Code)

				manual := map[string]any{"supplier_id": sheetSupplier.ID, "date": "2024-01-02", "usage_type": "ccmax", "description": "rongshan", "quantity": "752.86", "unit": "USD", "unit_price": "2.5", "currency": "CNY", "discount": "1", "fx_rate": "6.7", "rate_mode": "divide", "idempotency_key": "sheet-ccmax-0102"}
				assert.Equal(t, "280.917910", billingResponse[Usage](t, manager.request(t, "POST", "/api/usage", manual, nil), 201).Amount)
				manual["rate_mode"], manual["idempotency_key"] = "inverse", "sheet-ccmax-invalid"
				assert.Equal(t, 400, manager.request(t, "POST", "/api/usage", manual, nil).Code)
				recharge := map[string]any{"supplier_id": sheetSupplier.ID, "date": "2024-01-02", "amount": "60000", "reference": "U transfer", "idempotency_key": "sheet-recharge-0102"}
				assert.Equal(t, 201, manager.request(t, "POST", "/api/recharges", recharge, nil).Code)
				claim := map[string]any{"supplier_id": sheetSupplier.ID, "date": "2024-01-02", "usage_type": "openai 号池", "quantity": "122.4076", "amount": "71.25"}
				assert.Equal(t, 200, manager.request(t, "PUT", "/api/claims", claim, nil).Code)
				assert.Equal(t, 200, manager.request(t, "PUT", "/api/claims", claim, nil).Code, "Saving a claim again replaces it")
				missing := map[string]any{"supplier_id": sheetSupplier.ID, "date": "2024-01-02", "usage_type": "aws", "quantity": "89715.29", "amount": "74985.91"}
				assert.Equal(t, 200, manager.request(t, "PUT", "/api/claims", missing, nil).Code)

				type sheetStatement struct {
					billingTestStatement
					InitialBalance *InitialBalance `json:"initial_balance"`
					Lines          []StatementLine `json:"lines"`
				}
				statementURL := fmt.Sprintf("/api/statement?supplier_id=%d&start=2024-01-02&end=2024-01-02", sheetSupplier.ID)
				assert.Equal(t, 403, vendor.request(t, "GET", statementURL, nil, nil).Code)
				sheet := billingResponse[sheetStatement](t, manager.request(t, "GET", statementURL, nil, nil), 200)
				require.NotNil(t, sheet.InitialBalance)
				assert.Equal(t, int64(3), sheet.InitialBalance.Revision)
				assert.Equal(t, "39234.220000", sheet.OpeningBalance)
				require.Len(t, sheet.Lines, 4)
				pool := sheet.Lines[0]
				assert.Equal(t, "aws", pool.UsageType, "A claim without measured usage stays visible")
				assert.Equal(t, "claim", pool.Kind)
				assert.Equal(t, "ccmax", sheet.Lines[1].UsageType)
				assert.Equal(t, "1882.150000", sheet.Lines[1].OriginalAmount)
				assert.Equal(t, "-280.917910", sheet.Lines[1].Change)
				pool = sheet.Lines[2]
				assert.Equal(t, "openai 号池", pool.UsageType)
				assert.Equal(t, "122.407600", pool.Quantity)
				assert.Equal(t, "3.9", pool.UnitPrice)
				assert.Equal(t, "477.389640", pool.OriginalAmount)
				assert.Equal(t, "-71.252185", pool.Change)
				require.NotNil(t, pool.Claim)
				assert.Equal(t, "71.250000", pool.Claim.Amount)
				assert.Equal(t, "recharge", sheet.Lines[3].Kind)
				assert.Equal(t, "98882.049905", sheet.Lines[3].Balance)
				assert.Equal(t, sheet.Balance, sheet.Lines[3].Balance)
				assert.Equal(t, sheet.Balance, sheet.Days[0].ClosingBalance)

				sheetUser := User{Username: "sheet-supplier", PasswordHash: hash, Role: RoleSupplier, SupplierID: sheetSupplier.ID}
				require.NoError(t, db.Create(&sheetUser).Error)
				sheetClient := billingSessionClient(t, s, sheetUser)
				price["usage_type"], price["unit_price"] = "openai 号池", "4"
				assert.Equal(t, 200, sheetClient.request(t, "POST", "/api/usage/price", price, nil).Code, "A supplier prices its own line")
				var supplierPosting LedgerEntry
				require.NoError(t, db.Where("supplier_id = ? AND note = ?", sheetSupplier.ID, "Price correction").Order("id DESC").First(&supplierPosting).Error)
				assert.Equal(t, sheetUser.ID, supplierPosting.CreatedBy, "The ledger records who changed the price")
				repriced := billingResponse[sheetStatement](t, manager.request(t, "GET", statementURL, nil, nil), 200)
				assert.Equal(t, "-73.079165", repriced.Lines[2].Change)
				assert.Equal(t, repriced.Balance, repriced.Lines[len(repriced.Lines)-1].Balance)
				var corrections int64
				require.NoError(t, db.Model(&LedgerEntry{}).Where("supplier_id = ? AND kind = ?", sheetSupplier.ID, "usage_adjustment").Count(&corrections).Error)
				assert.Equal(t, int64(2), corrections, "Re-pricing appends corrections instead of rewriting charges")
				fullURL := fmt.Sprintf("/api/statement?supplier_id=%d&start=2024-01-01&end=2024-01-02", sheetSupplier.ID)
				full := billingResponse[sheetStatement](t, manager.request(t, "GET", fullURL, nil, nil), 200)
				assert.Equal(t, "0.000000", full.OpeningBalance)
				assert.Equal(t, "39234.220000", full.Days[0].Initial, "Replaced revisions net to the current initial balance")
				assert.Equal(t, full.Balance, full.Lines[len(full.Lines)-1].Balance)

				overview := billingResponse[struct {
					Suppliers []SupplierStatement `json:"suppliers"`
				}](t, manager.request(t, "GET", "/api/overview?start=2024-01-02&end=2024-01-02", nil, nil), 200)
				found := false
				for _, item := range overview.Suppliers {
					if item.Supplier.ID == sheetSupplier.ID {
						found = true
						assert.Equal(t, repriced.Balance, item.Days[0].ClosingBalance)
						assert.Nil(t, item.Lines)
						assert.Equal(t, []DailyProduct{
							{UsageType: "ccmax", Unit: "USD", Quantity: "752.860000", Unpriced: "0.000000", Amount: "280.917910"},
							{UsageType: "openai 号池", Unit: "gateway_usd", Quantity: "122.407600", Unpriced: "0.000000", Amount: "73.079165", RequestCount: 2},
						}, item.Days[0].Products, "Each day is broken down by product line")
					}
				}
				assert.True(t, found)
				assert.Equal(t, 403, vendor.request(t, "GET", "/api/overview?start=2024-01-02&end=2024-01-02", nil, nil).Code)
			}

			// Configurable mappings: tags, CC Max uploaders and gateway groups map
			// to supplier product lines, can be edited and never overlap.
			{
				t.Setenv("SUPPLIER_BILLING_SOURCE_MAPPING", "test-mapping-token")
				tagRows := []map[string]any{
					{"date": "2024-01-02", "tag": "alice", "usd": "10.5", "channels": 2},
					{"date": "2024-01-02", "tag": "promi1", "usd": "20", "channels": 3},
					{"date": "2024-01-02", "tag": "lang", "usd": "1.25", "channels": 1},
					{"date": "2024-01-03", "tag": "promi1", "usd": "4", "channels": 1},
				}
				ccmaxRows := []map[string]any{
					{"date": "2024-01-02", "user_id": 163, "username": "rongshan1", "accounts": 1, "cny": "1475.85", "usd": "202.171233"},
					{"date": "2024-01-02", "user_id": 99, "username": "migos", "accounts": 82, "cny": "138555.68", "usd": "18980.230137"},
				}
				groupRows := []any{reportUsageFixture("2024-01-02 03:00", "100", 41), reportUsageFixture("2024-01-02 04:00", "50", 42), reportUsageFixture("2024-01-02 05:00", "7", 43)}
				groupRows[0].(map[string]any)["group"] = "AWS_Bedrock"
				groupRows[1].(map[string]any)["group"] = "AWS_Bedrock"
				groupRows[2].(map[string]any)["group"] = "Anthropic"
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, "test-mapping-token", r.Header.Get("X-API-Key"))
					var payload any
					switch r.URL.Path {
					case "/api/billing/remote-tag-usage":
						assert.Equal(t, "1", r.URL.Query().Get("profile_id"))
						payload = map[string]any{"rows": tagRows, "through": "2024-01-02"}
					case "/api/billing/ccmax-usage":
						payload = map[string]any{"rows": ccmaxRows, "through": "2024-01-02"}
					default:
						payload = groupRows
					}
					data, err := common.Marshal(payload)
					require.NoError(t, err)
					_, _ = w.Write(data)
				}))
				defer upstream.Close()
				s.SourceOrigins = append(s.SourceOrigins, upstream.URL)
				pipi := Supplier{Name: "皮皮 mapping", Currency: "USD"}
				alice := Supplier{Name: "Alice mapping", Currency: "USD"}
				require.NoError(t, db.Create(&pipi).Error)
				require.NoError(t, db.Create(&alice).Error)
				base := Source{Kind: "remote_tag", BaseURL: upstream.URL, CredentialEnv: "SUPPLIER_BILLING_SOURCE_MAPPING", ProfileID: 1, Currency: "USD", SettlementMode: "usage_only"}
				rest := base
				rest.SupplierID, rest.Name, rest.UsageType, rest.Selector, rest.ExcludeSelector = pipi.ID, "七牛", "七牛", "alice", true
				restSource := billingResponse[Source](t, manager.request(t, "POST", "/api/sources", rest, nil), 201)
				aliceTags := base
				aliceTags.SupplierID, aliceTags.Name, aliceTags.UsageType, aliceTags.Selector = alice.ID, "Alice tags", "openai", "alice"
				aliceSource := billingResponse[Source](t, manager.request(t, "POST", "/api/sources", aliceTags, nil), 201)
				overlap := aliceTags
				overlap.Name, overlap.Selector = "Overlap", "promi1"
				assert.Equal(t, 409, manager.request(t, "POST", "/api/sources", overlap, nil).Code, "A tag already covered by 'all other tags' cannot be mapped twice")
				noProfile := aliceTags
				noProfile.ProfileID, noProfile.Selector = 0, "x"
				assert.Equal(t, 400, manager.request(t, "POST", "/api/sources", noProfile, nil).Code)

				dateRange := map[string]string{"start": "2024-01-02", "end": "2024-01-03"}
				synced := billingResponse[map[string]any](t, manager.request(t, "POST", fmt.Sprintf("/api/sources/%d/sync", restSource.ID), dateRange, nil), 200)
				assert.Equal(t, float64(3), synced["created"], "promi1 ×2 days and lang map to 'all other tags'")
				assert.Equal(t, float64(1), billingResponse[map[string]any](t, manager.request(t, "POST", fmt.Sprintf("/api/sources/%d/sync", aliceSource.ID), dateRange, nil), 200)["created"])
				var coverage []SourceSync
				require.NoError(t, db.Where("source_id = ?", restSource.ID).Find(&coverage).Error)
				require.Len(t, coverage, 1, "Days after the upstream's complete date stay unsynced")
				assert.Equal(t, "2024-01-02", coverage[0].Date)

				// Move lang to Alice: it must leave 'all other tags' first.
				aliceTags.Selector = "alice,lang"
				aliceURL := fmt.Sprintf("/api/sources/%d", aliceSource.ID)
				assert.Equal(t, 409, manager.request(t, "PUT", aliceURL, aliceTags, nil).Code)
				rest.Selector = "alice, lang"
				assert.Equal(t, 200, manager.request(t, "PUT", fmt.Sprintf("/api/sources/%d", restSource.ID), rest, nil).Code)
				assert.Equal(t, 200, manager.request(t, "PUT", aliceURL, aliceTags, nil).Code)
				assert.Equal(t, 200, manager.request(t, "POST", fmt.Sprintf("/api/sources/%d/sync", restSource.ID), dateRange, nil).Code, "Unmapped rows stay as history instead of failing the sync")
				assert.Equal(t, float64(1), billingResponse[map[string]any](t, manager.request(t, "POST", fmt.Sprintf("/api/sources/%d/sync", aliceSource.ID), dateRange, nil), 200)["created"])
				var langRows int64
				require.NoError(t, db.Model(&Usage{}).Where("match_value = ?", "lang").Count(&langRows).Error)
				assert.Equal(t, int64(2), langRows, "The earlier 七牛 row is kept; Alice imports lang from now on")

				// Discovery reads the last 14 days.
				for _, row := range tagRows {
					row["date"] = time.Now().In(businessZone).Format("2006-01-02")
				}
				tagRows = tagRows[:3]
				options := billingResponse[[]MappingOption](t, manager.request(t, "POST", "/api/sources/discover", base, nil), 200)
				owners := map[string]int64{}
				for _, option := range options {
					owners[option.Value] = option.SourceID
				}
				assert.Equal(t, map[string]int64{"alice": aliceSource.ID, "lang": aliceSource.ID, "promi1": restSource.ID}, owners)
				assert.Equal(t, 403, vendor.request(t, "POST", "/api/sources/discover", base, nil).Code)

				ccmax := Source{Kind: "ccmax", SupplierID: pipi.ID, Name: "ccmax", UsageType: "ccmax", BaseURL: upstream.URL, CredentialEnv: "SUPPLIER_BILLING_SOURCE_MAPPING", Currency: "CNY", SettlementMode: "usage_only", Selector: "rongshan1,rangshan2"}
				ccmaxSource := billingResponse[Source](t, manager.request(t, "POST", "/api/sources", ccmax, nil), 201)
				assert.Equal(t, "rangshan2,rongshan1", ccmaxSource.Selector)
				assert.Equal(t, 200, manager.request(t, "POST", fmt.Sprintf("/api/sources/%d/sync", ccmaxSource.ID), map[string]string{"start": "2024-01-02", "end": "2024-01-02"}, nil).Code)
				var ccmaxUsage []Usage
				require.NoError(t, db.Where("source_id = ?", ccmaxSource.ID).Find(&ccmaxUsage).Error)
				require.Len(t, ccmaxUsage, 1)
				assert.Equal(t, "202.171233", ccmaxUsage[0].Quantity)
				assert.Equal(t, "list_usd", ccmaxUsage[0].Unit)
				assert.Equal(t, "rongshan1 · 1 accounts · ¥1475.85", ccmaxUsage[0].Description)
				assert.Equal(t, "CNY", ccmaxUsage[0].Currency)
				ccmax.Name, ccmax.Selector = "Duplicate uploader", "rongshan1"
				assert.Equal(t, 409, manager.request(t, "POST", "/api/sources", ccmax, nil).Code)

				group := Source{Kind: "report_group", SupplierID: pipi.ID, Name: "aws", UsageType: "aws", BaseURL: upstream.URL + "/report82", CredentialEnv: "SUPPLIER_BILLING_SOURCE_MAPPING", GatewayUserID: 1, Currency: "USD", SettlementMode: "usage_only", Selector: "AWS_Bedrock"}
				groupSource := billingResponse[Source](t, manager.request(t, "POST", "/api/sources", group, nil), 201)
				assert.Equal(t, 200, manager.request(t, "POST", fmt.Sprintf("/api/sources/%d/sync", groupSource.ID), map[string]string{"start": "2024-01-02", "end": "2024-01-02"}, nil).Code)
				var groupUsage []Usage
				require.NoError(t, db.Where("source_id = ?", groupSource.ID).Find(&groupUsage).Error)
				require.Len(t, groupUsage, 1, "Hourly channel rows collapse to one day/group/model row")
				assert.Equal(t, "150", groupUsage[0].Quantity)
				assert.Equal(t, int64(2), groupUsage[0].RequestCount)
				channelMode := Source{Kind: "report", SupplierID: alice.ID, Name: "channels", UsageType: "aws", BaseURL: upstream.URL + "/report82", CredentialEnv: "SUPPLIER_BILLING_SOURCE_MAPPING", ChannelIDs: "43", GatewayUserID: 1, Currency: "USD", SettlementMode: "usage_only"}
				assert.Equal(t, 409, manager.request(t, "POST", "/api/sources", channelMode, nil).Code, "Channel and group sources cannot share an upstream")
			}

			// Purchase direction: another gateway user's usage on a head's
			// channels is bought by a different supplier and credits its balance.
			{
				t.Setenv("SUPPLIER_BILLING_SOURCE_PURCHASE", "test-purchase-token")
				rows := []map[string]any{reportUsageFixture("2024-01-02 03:00", "100", 61), reportUsageFixture("2024-01-02 04:00", "6.7", 61)}
				rows[1]["user_id"] = 3
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					data, err := common.Marshal(rows)
					require.NoError(t, err)
					_, _ = w.Write(data)
				}))
				defer upstream.Close()
				s.SourceOrigins = append(s.SourceOrigins, upstream.URL)
				seller := Supplier{Name: "Alice purchase test", Currency: "USD"}
				buyer := Supplier{Name: "皮皮 purchase test", Currency: "USD"}
				require.NoError(t, db.Create(&seller).Error)
				require.NoError(t, db.Create(&buyer).Error)
				sold := Source{Kind: "report", SupplierID: seller.ID, Name: "Alice keys", UsageType: "openai 号池", BaseURL: upstream.URL, CredentialEnv: "SUPPLIER_BILLING_SOURCE_PURCHASE", ChannelIDs: "61", GatewayUserID: 1, Currency: "USD", SettlementMode: "usage_only"}
				billingResponse[Source](t, manager.request(t, "POST", "/api/sources", sold, nil), 201)
				bought := sold
				bought.SupplierID, bought.Name, bought.UsageType, bought.GatewayUserID, bought.Direction = buyer.ID, "yfqh on Alice keys", "yfqh 采购", 3, "purchase"
				boughtSource := billingResponse[Source](t, manager.request(t, "POST", "/api/sources", bought, nil), 201)
				assert.Equal(t, "purchase", boughtSource.Direction)
				for _, user := range []int64{0, 3} {
					again := bought
					again.Name, again.GatewayUserID = "overlap", user
					assert.Equal(t, 409, manager.request(t, "POST", "/api/sources", again, nil).Code, "All-users or same-user scopes still overlap")
				}
				bought.Direction = "expense"
				assert.Equal(t, 400, manager.request(t, "PUT", fmt.Sprintf("/api/sources/%d", boughtSource.ID), bought, nil).Code, "Direction is fixed after creation")
				assert.Equal(t, float64(1), billingResponse[map[string]any](t, manager.request(t, "POST", fmt.Sprintf("/api/sources/%d/sync", boughtSource.ID), map[string]string{"start": "2024-01-02", "end": "2024-01-02"}, nil), 200)["created"])
				price := map[string]any{"supplier_id": buyer.ID, "date": "2024-01-02", "usage_type": "yfqh 采购", "unit_price": "3.9", "currency": "CNY", "fx_rate": "6.7"}
				assert.Equal(t, 200, manager.request(t, "POST", "/api/usage/price", price, nil).Code)
				type sheetStatement struct {
					billingTestStatement
					Lines []StatementLine `json:"lines"`
				}
				sheet := billingResponse[sheetStatement](t, manager.request(t, "GET", fmt.Sprintf("/api/statement?supplier_id=%d&start=2024-01-02&end=2024-01-02", buyer.ID), nil, nil), 200)
				assert.Equal(t, "3.900000", sheet.Balance, "6.7 USD × 3.9 ÷ 6.7 credits the buyer's balance")
				require.Len(t, sheet.Lines, 1)
				assert.Equal(t, "purchase", sheet.Lines[0].Direction)
				assert.Equal(t, "3.900000", sheet.Lines[0].Change)
				assert.Equal(t, "-26.130000", sheet.Lines[0].OriginalAmount)
				assert.Equal(t, "-3.900000", sheet.Days[0].Products[0].Amount)
				price["unit_price"] = "4"
				assert.Equal(t, 200, manager.request(t, "POST", "/api/usage/price", price, nil).Code)
				repriced := billingResponse[billingTestStatement](t, manager.request(t, "GET", fmt.Sprintf("/api/statement?supplier_id=%d&start=2024-01-02&end=2024-01-02", buyer.ID), nil, nil), 200)
				assert.Equal(t, "4.000000", repriced.Balance, "Re-pricing a purchase corrects the credit")
			}

			// Re-run startup against the populated service and preserve uniqueness.
			{
				var before []LedgerEntry
				require.NoError(t, db.Order("id").Find(&before).Error)
				require.NotEmpty(t, before)
				require.NoError(t, s.Migrate())
				require.NoError(t, s.Migrate())
				var after []LedgerEntry
				require.NoError(t, db.Order("id").Find(&after).Error)
				assert.Equal(t, before, after)
				var preservedSupplier Supplier
				require.NoError(t, db.First(&preservedSupplier, supplier.ID).Error)
				assert.Equal(t, "供应商甲", preservedSupplier.Name)
				duplicateUser := User{Username: managerUser.Username, PasswordHash: hash, Role: RoleManager}
				assert.Error(t, db.Create(&duplicateUser).Error)
				var original Recharge
				require.NoError(t, db.First(&original).Error)
				original.ID = 0
				assert.Error(t, db.Create(&original).Error)
				var imported Usage
				require.NoError(t, db.Where("origin = ?", "sync").First(&imported).Error)
				imported.ID = 0
				assert.Error(t, db.Create(&imported).Error)
				assert.Equal(t, 200, manager.request(t, "GET", "/api/session", nil, nil).Code)
			}

			testBillingAuthentication(t, s, managerUser, supplierUser, &manager, &vendor)
		})
	}
}

// This is the deployed Source schema before gateway-user filtering. Keep the
// baseline independent of Source so migrations run against a genuinely old table.
type billingSourceBeforeGatewayFilter struct {
	ID            int64  `gorm:"primaryKey"`
	SupplierID    int64  `gorm:"index;not null"`
	Name          string `gorm:"size:120;not null"`
	Kind          string `gorm:"size:32;not null"`
	BaseURL       string `gorm:"type:text;not null"`
	CredentialEnv string `gorm:"size:120;not null"`
	ChannelIDs    string `gorm:"type:text;not null"`
	UsageType     string `gorm:"size:120;not null"`
	Currency      string `gorm:"size:3;not null"`
	Discount      string `gorm:"size:32;not null"`
	FxRate        string `gorm:"size:32;not null"`
	Version       int64
	LastSyncAt    int64
	LastError     string `gorm:"type:text"`
}

func (billingSourceBeforeGatewayFilter) TableName() string { return "sb_sources" }

type billingUsageBeforeSettlementStatus struct {
	ID               int64  `gorm:"primaryKey"`
	SupplierID       int64  `gorm:"index;not null"`
	Date             string `gorm:"size:10;index;not null"`
	SourceID         int64  `gorm:"index"`
	SourceName       string `gorm:"size:120"`
	ExternalRef      string `gorm:"size:64"`
	UsageType        string `gorm:"size:120;not null"`
	Description      string `gorm:"type:text"`
	Quantity         string `gorm:"size:32;not null"`
	Unit             string `gorm:"size:64;not null"`
	UnitPrice        string `gorm:"size:32;not null"`
	Currency         string `gorm:"size:3;not null"`
	Discount         string `gorm:"size:32;not null"`
	FxRate           string `gorm:"size:32;not null"`
	Amount           string `gorm:"size:32;not null"`
	Origin           string `gorm:"size:16;not null"`
	RequestCount     int64
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CreatedBy        int64
	CreatedAt        int64
	IdempotencyKey   string `gorm:"size:64;uniqueIndex;not null"`
	PayloadHash      string `gorm:"size:64;not null"`
}

func (billingUsageBeforeSettlementStatus) TableName() string { return "sb_usage" }

// The next published schema added account scoping and unpriced imports, but
// still had neither channel/vendor metadata nor the supplier hierarchy tables.
type billingSourceBeforeVendorHierarchy struct {
	Legacy         billingSourceBeforeGatewayFilter `gorm:"embedded"`
	GatewayUserID  int64                            `gorm:"not null;default:0"`
	SettlementMode string                           `gorm:"size:16;not null;default:settlement"`
}

func (billingSourceBeforeVendorHierarchy) TableName() string { return "sb_sources" }

type billingUsageBeforeVendorHierarchy struct {
	Legacy           billingUsageBeforeSettlementStatus `gorm:"embedded"`
	SettlementStatus string                             `gorm:"size:16;not null;default:settled"`
}

func (billingUsageBeforeVendorHierarchy) TableName() string { return "sb_usage" }

func TestServiceSchemaUpgradeMatrix(t *testing.T) {
	for _, engine := range []struct{ name, env string }{
		{"sqlite", ""}, {"mysql", "BILLING_TEST_MYSQL_DSN"}, {"postgres", "BILLING_TEST_POSTGRES_DSN"},
	} {
		t.Run(engine.name, func(t *testing.T) {
			db := billingDatabaseFixture(t, engine.name, engine.env)
			t.Setenv("SUPPLIER_BILLING_SOURCE_LEGACY", "test-legacy-source-token")
			body, err := common.Marshal([]any{reportUsageFixture("2024-01-02 10:00", "10", 7)})
			require.NoError(t, err)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(body)
			}))
			defer upstream.Close()
			baseline := db
			if engine.name == "mysql" {
				baseline = db.Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4")
			}
			require.NoError(t, baseline.AutoMigrate(&Supplier{}, &billingSourceBeforeGatewayFilter{}, &SourceChannel{}, &SourceSync{}, &billingUsageBeforeSettlementStatus{}, &LedgerEntry{}))
			require.False(t, db.Migrator().HasColumn(&Source{}, "gateway_user_id"))
			require.False(t, db.Migrator().HasColumn(&Source{}, "settlement_mode"))
			require.False(t, db.Migrator().HasColumn(&Usage{}, "settlement_status"))
			supplier := Supplier{Name: "历史供应商", Currency: "CNY", CreatedAt: 1704153600}
			require.NoError(t, db.Create(&supplier).Error)
			source := billingSourceBeforeGatewayFilter{SupplierID: supplier.ID, Name: "Existing all-users source", Kind: "report", BaseURL: upstream.URL, CredentialEnv: "SUPPLIER_BILLING_SOURCE_LEGACY", ChannelIDs: "7", UsageType: "OpenAI", Currency: "USD", Discount: "0.8", FxRate: "7.2", Version: 3, LastSyncAt: 1704153600}
			require.NoError(t, db.Create(&source).Error)
			binding := SourceChannel{SourceID: source.ID, BindingKey: digest([]any{source.BaseURL, int64(7)})}
			require.NoError(t, db.Create(&binding).Error)
			usage := billingUsageBeforeSettlementStatus{SupplierID: supplier.ID, Date: "2024-01-02", SourceID: source.ID, SourceName: source.Name, ExternalRef: "b9ed8eb6364096c5c62399835c53dde3cbf72ec460c09727283cf34b56b3d850", UsageType: source.UsageType, Description: "gpt-test", Quantity: "10", Unit: "gateway_usd", UnitPrice: "1", Currency: source.Currency, Discount: source.Discount, FxRate: source.FxRate, Amount: "57.600000", Origin: "sync", RequestCount: 1, InputTokens: 10, OutputTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4, CreatedAt: 1704153600, PayloadHash: digest("legacy-import")}
			usage.IdempotencyKey = digest([]any{"sync", source.ID, usage.ExternalRef})
			require.NoError(t, db.Create(&usage).Error)
			ledger := LedgerEntry{SupplierID: supplier.ID, Date: usage.Date, Kind: "usage", Amount: "-57.600000", UsageID: usage.ID, CreatedAt: 1704153600}
			require.NoError(t, db.Create(&ledger).Error)
			coverage := SourceSync{SourceID: source.ID, Date: usage.Date, SyncedAt: 1704153600}
			require.NoError(t, db.Create(&coverage).Error)

			s := &Server{DB: db, Origin: "https://billing.example.test", SecureCookies: true, SourceOrigins: []string{upstream.URL}}
			require.NoError(t, s.Migrate())
			require.NoError(t, s.Migrate())
			var upgraded Source
			require.NoError(t, db.First(&upgraded, source.ID).Error)
			assert.Zero(t, upgraded.GatewayUserID, "Existing sources retain all-users scope")
			assert.Equal(t, "settlement", upgraded.SettlementMode)
			var preservedSource billingSourceBeforeGatewayFilter
			require.NoError(t, db.First(&preservedSource, source.ID).Error)
			assert.Equal(t, source, preservedSource)
			var preservedUsage billingUsageBeforeSettlementStatus
			require.NoError(t, db.First(&preservedUsage, usage.ID).Error)
			assert.Equal(t, usage, preservedUsage)
			var upgradedUsage Usage
			require.NoError(t, db.First(&upgradedUsage, usage.ID).Error)
			assert.Equal(t, "settled", upgradedUsage.SettlementStatus)
			assert.Equal(t, "multiply", upgradedUsage.RateMode, "Existing prices keep multiplying by their exchange rate")
			assert.Zero(t, upgradedUsage.ChannelID)
			assert.Zero(t, upgradedUsage.VendorID)
			var preservedLedger LedgerEntry
			require.NoError(t, db.First(&preservedLedger, ledger.ID).Error)
			assert.Equal(t, ledger, preservedLedger)
			var preservedCoverage SourceSync
			require.NoError(t, db.First(&preservedCoverage, coverage.ID).Error)
			assert.Equal(t, coverage, preservedCoverage)
			assert.True(t, db.Migrator().HasIndex(&Source{}, "idx_sb_sources_supplier_id"))
			managerUser := User{Username: "migration-manager", PasswordHash: "session-fixture-only", Role: RoleManager}
			require.NoError(t, db.Create(&managerUser).Error)
			manager := billingSessionClient(t, s, managerUser)
			synced := billingResponse[map[string]any](t, manager.request(t, "POST", fmt.Sprintf("/api/sources/%d/sync", source.ID), map[string]string{"start": usage.Date, "end": usage.Date}, nil), 200)
			assert.Equal(t, float64(0), synced["created"])
			assert.Equal(t, float64(0), synced["adjusted"])
			statement := billingResponse[billingTestStatement](t, manager.request(t, "GET", fmt.Sprintf("/api/statement?supplier_id=%d&start=%s&end=%s", supplier.ID, usage.Date, usage.Date), nil, nil), 200)
			assert.Equal(t, "-57.600000", statement.Balance)
			assert.Equal(t, "10.000000", statement.GatewayUsage)
			assert.Equal(t, "0.000000", statement.UnpricedUsage)
			assert.Equal(t, []LedgerEntry{ledger}, statement.Ledger)
			require.Len(t, statement.Usage, 1)
			assert.Equal(t, usage.ID, statement.Usage[0].ID)
			usage.ID, binding.ID, coverage.ID = 0, 0, 0
			assert.Error(t, db.Create(&usage).Error, "Imported identity remains unique")
			assert.Error(t, db.Create(&binding).Error, "Channel ownership remains unique")
			assert.Error(t, db.Create(&coverage).Error, "Source/date coverage remains unique")
		})
	}
}

func TestPublishedHierarchySchemaUpgradeMatrix(t *testing.T) {
	for _, engine := range []struct{ name, env string }{
		{"sqlite", ""}, {"mysql", "BILLING_TEST_MYSQL_DSN"}, {"postgres", "BILLING_TEST_POSTGRES_DSN"},
	} {
		t.Run(engine.name, func(t *testing.T) {
			db := billingDatabaseFixture(t, engine.name, engine.env)
			baseline := db
			if engine.name == "mysql" {
				baseline = db.Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4")
			}
			require.NoError(t, baseline.AutoMigrate(&Supplier{}, &billingSourceBeforeVendorHierarchy{}, &billingUsageBeforeVendorHierarchy{}, &SourceChannel{}, &SourceSync{}, &LedgerEntry{}))
			require.False(t, db.Migrator().HasTable(&Vendor{}))
			require.False(t, db.Migrator().HasTable(&VendorKey{}))
			require.False(t, db.Migrator().HasColumn(&Usage{}, "channel_id"))
			require.False(t, db.Migrator().HasColumn(&Usage{}, "vendor_id"))
			supplier := Supplier{Name: "既有供应商头", Currency: "CNY", CreatedAt: 1704153600}
			require.NoError(t, db.Create(&supplier).Error)
			source := billingSourceBeforeVendorHierarchy{Legacy: billingSourceBeforeGatewayFilter{SupplierID: supplier.ID, Name: "Existing scoped source", Kind: "report", BaseURL: "https://report.example.test", CredentialEnv: "SUPPLIER_BILLING_SOURCE_LEGACY", ChannelIDs: "7", UsageType: "OpenAI", Currency: "USD", Version: 8, LastSyncAt: 1704153600}, GatewayUserID: 42, SettlementMode: "usage_only"}
			require.NoError(t, db.Create(&source).Error)
			binding := SourceChannel{SourceID: source.Legacy.ID, BindingKey: digest([]any{source.Legacy.BaseURL, int64(7)})}
			require.NoError(t, db.Create(&binding).Error)
			settled := billingUsageBeforeVendorHierarchy{Legacy: billingUsageBeforeSettlementStatus{SupplierID: supplier.ID, Date: "2024-01-02", SourceID: source.Legacy.ID, SourceName: source.Legacy.Name, ExternalRef: digest("published-settled-row"), UsageType: "OpenAI", Description: "gpt-test", Quantity: "10", Unit: "gateway_usd", UnitPrice: "1", Currency: "USD", Discount: "0.8", FxRate: "7.2", Amount: "57.600000", Origin: "sync", RequestCount: 3, InputTokens: 10, CreatedAt: 1704153600, IdempotencyKey: digest("published-settled-import"), PayloadHash: digest("published-settled-payload")}, SettlementStatus: "settled"}
			unpriced := settled
			unpriced.SettlementStatus = "unpriced"
			unpriced.Legacy.ExternalRef, unpriced.Legacy.IdempotencyKey, unpriced.Legacy.PayloadHash = digest("published-unpriced-row"), digest("published-unpriced-import"), digest("published-unpriced-payload")
			unpriced.Legacy.Quantity, unpriced.Legacy.Amount, unpriced.Legacy.RequestCount = "12.345678", "0.000000", 5
			unpriced.Legacy.UnitPrice, unpriced.Legacy.Discount, unpriced.Legacy.FxRate = "", "", ""
			usage := []billingUsageBeforeVendorHierarchy{settled, unpriced}
			require.NoError(t, db.Create(&usage).Error)
			ledger := LedgerEntry{SupplierID: supplier.ID, Date: "2024-01-02", Kind: "usage", Amount: "-57.600000", UsageID: usage[0].Legacy.ID, CreatedAt: 1704153600}
			require.NoError(t, db.Create(&ledger).Error)
			coverage := SourceSync{SourceID: source.Legacy.ID, Date: "2024-01-02", SyncedAt: 1704153600}
			require.NoError(t, db.Create(&coverage).Error)
			s := &Server{DB: db}
			require.NoError(t, s.Migrate())
			require.NoError(t, s.Migrate())
			var preservedSource billingSourceBeforeVendorHierarchy
			require.NoError(t, db.First(&preservedSource, source.Legacy.ID).Error)
			assert.Equal(t, source, preservedSource)
			var preservedUsage []billingUsageBeforeVendorHierarchy
			require.NoError(t, db.Order("id").Find(&preservedUsage).Error)
			assert.Equal(t, usage, preservedUsage)
			var upgraded []Usage
			require.NoError(t, db.Order("id").Find(&upgraded).Error)
			require.Len(t, upgraded, 2)
			for i, item := range upgraded {
				assert.Zero(t, item.ChannelID)
				assert.Zero(t, item.VendorID)
				assert.Equal(t, usage[i].SettlementStatus, item.SettlementStatus)
			}
			var preservedLedger []LedgerEntry
			require.NoError(t, db.Find(&preservedLedger).Error)
			assert.Equal(t, []LedgerEntry{ledger}, preservedLedger)
			var preservedCoverage SourceSync
			require.NoError(t, db.First(&preservedCoverage, coverage.ID).Error)
			assert.Equal(t, coverage, preservedCoverage)
			assert.True(t, db.Migrator().HasTable(&Vendor{}))
			assert.True(t, db.Migrator().HasTable(&VendorKey{}))
			assert.True(t, db.Migrator().HasIndex(&Source{}, "idx_sb_sources_supplier_id"))
			duplicate := usage[0]
			duplicate.Legacy.ID = 0
			assert.Error(t, db.Create(&duplicate).Error)
			binding.ID, coverage.ID = 0, 0
			assert.Error(t, db.Create(&binding).Error)
			assert.Error(t, db.Create(&coverage).Error)
		})
	}
}

func TestVendorHierarchyDatabaseMatrix(t *testing.T) {
	for _, engine := range []struct{ name, env string }{
		{"sqlite", ""}, {"mysql", "BILLING_TEST_MYSQL_DSN"}, {"postgres", "BILLING_TEST_POSTGRES_DSN"},
	} {
		t.Run(engine.name, func(t *testing.T) {
			db := billingDatabaseFixture(t, engine.name, engine.env)
			s := &Server{DB: db, Origin: "https://billing.example.test", SecureCookies: true}
			require.NoError(t, s.Migrate())
			heads := []Supplier{{Name: "Alice", Currency: "CNY"}, {Name: "皮皮", Currency: "CNY"}}
			require.NoError(t, db.Create(&heads).Error)
			managerUser := User{Username: "hierarchy-manager", PasswordHash: "session-fixture-only", Role: RoleManager}
			aliceUser := User{Username: "alice-head", PasswordHash: "session-fixture-only", Role: RoleSupplier, SupplierID: heads[0].ID}
			require.NoError(t, db.Create(&managerUser).Error)
			require.NoError(t, db.Create(&aliceUser).Error)
			manager, alice := billingSessionClient(t, s, managerUser), billingSessionClient(t, s, aliceUser)
			t.Setenv("SUPPLIER_BILLING_SOURCE_HIERARCHY", "test-hierarchy-source-token")
			rows := []map[string]any{}
			for _, item := range []struct {
				hour, amount      string
				channel, requests int64
			}{
				{"2024-01-01 16:00", "0.1", 11, 2},
				{"2024-01-02 10:00", "0.2", 12, 3},
				{"2024-01-02 09:00", "0.3", 21, 5},
				{"2024-01-02 12:00", "0.4", 22, 7},
				{"2024-01-02 16:00", "0.000001", 11, 11},
				{"2024-01-03 10:00", "0.000002", 21, 13},
				{"2024-01-03 11:00", "0.000003", 21, 17},
			} {
				row := reportUsageFixture(item.hour, item.amount, item.channel)
				row["request_count"] = item.requests
				rows = append(rows, row)
			}
			body, err := common.Marshal(rows)
			require.NoError(t, err)
			var responseBody atomic.Value
			responseBody.Store(string(body))
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, responseBody.Load().(string))
			}))
			defer upstream.Close()
			s.SourceOrigins = []string{upstream.URL}
			vendors := []Vendor{
				{SupplierID: heads[0].ID, Name: "Alice", SourceStudio: "alice", BaseURL: upstream.URL, IdentityKey: digest([]any{upstream.URL, "alice"})},
				{SupplierID: heads[0].ID, Name: "Alice 3", SourceStudio: "alice3", BaseURL: upstream.URL, IdentityKey: digest([]any{upstream.URL, "alice3"})},
				{SupplierID: heads[1].ID, Name: "Supplier B", SourceStudio: "studio-b", BaseURL: upstream.URL, IdentityKey: digest([]any{upstream.URL, "studio-b"})},
			}
			require.NoError(t, db.Create(&vendors).Error)
			keys := []VendorKey{}
			for _, item := range []struct{ vendorID, channelID int64 }{
				{vendors[0].ID, 11}, {vendors[0].ID, 13}, {vendors[2].ID, 12}, {vendors[2].ID, 21},
			} {
				keys = append(keys, VendorKey{VendorID: item.vendorID, BaseURL: upstream.URL, ChannelID: item.channelID, BindingKey: digest([]any{upstream.URL, item.channelID})})
			}
			require.NoError(t, db.Create(&keys).Error)
			require.NoError(t, s.Migrate())
			require.NoError(t, s.Migrate())
			duplicateVendor, duplicateKey := vendors[0], keys[0]
			duplicateVendor.ID, duplicateKey.ID = 0, 0
			assert.Error(t, db.Create(&duplicateVendor).Error)
			assert.Error(t, db.Create(&duplicateKey).Error)
			sources := []Source{}
			for i, channels := range []string{"11,12", "21,22"} {
				input := Source{SupplierID: heads[i].ID, Name: heads[i].Name + " usage", Kind: "report", BaseURL: upstream.URL, CredentialEnv: "SUPPLIER_BILLING_SOURCE_HIERARCHY", ChannelIDs: channels, GatewayUserID: 1, UsageType: "OpenAI", Currency: "USD", SettlementMode: "usage_only"}
				sources = append(sources, billingResponse[Source](t, manager.request(t, "POST", "/api/sources", input, nil), 201))
			}
			dateRange := map[string]string{"start": "2024-01-02", "end": "2024-01-03"}
			failed := manager.request(t, "POST", fmt.Sprintf("/api/sources/%d/sync", sources[0].ID), dateRange, nil)
			assert.Equal(t, 409, failed.Code)
			assert.NotContains(t, failed.Body.String(), upstream.URL)
			var count int64
			require.NoError(t, db.Model(&Usage{}).Where("source_id = ?", sources[0].ID).Count(&count).Error)
			assert.Zero(t, count, "A later wrong-head key rolls back earlier imported rows")
			require.NoError(t, db.Model(&SourceSync{}).Where("source_id = ?", sources[0].ID).Count(&count).Error)
			assert.Zero(t, count)
			require.NoError(t, db.Model(&VendorKey{}).Where("id = ?", keys[2].ID).Update("vendor_id", vendors[1].ID).Error)
			for _, source := range sources {
				assert.Equal(t, 200, manager.request(t, "POST", fmt.Sprintf("/api/sources/%d/sync", source.ID), dateRange, nil).Code)
			}
			// A manual item named gateway_usd must not contaminate imported usage totals.
			manual := map[string]any{"supplier_id": heads[0].ID, "date": "2024-01-02", "usage_type": "Other usage", "quantity": "999", "unit": "gateway_usd", "unit_price": "1", "currency": "USD", "discount": "1", "fx_rate": "1", "idempotency_key": "hierarchy-manual"}
			assert.Equal(t, 201, manager.request(t, "POST", "/api/usage", manual, nil).Code)
			vendorResponse := manager.request(t, "GET", "/api/vendors", nil, nil)
			listed := billingResponse[[]billingTestVendor](t, vendorResponse, 200)
			assert.ElementsMatch(t, []billingTestVendor{
				{vendors[0].ID, heads[0].ID, "Alice", "alice", 2},
				{vendors[1].ID, heads[0].ID, "Alice 3", "alice3", 1},
				{vendors[2].ID, heads[1].ID, "Supplier B", "studio-b", 1},
			}, listed)
			for _, privateField := range []string{"base_url", "channel_id", "binding_key", "identity_key"} {
				assert.NotContains(t, vendorResponse.Body.String(), privateField)
			}
			summaryURL := "/api/daily-summary?start=2024-01-02&end=2024-01-03"
			summary := billingResponse[billingTestDailySummary](t, manager.request(t, "GET", summaryURL, nil, nil), 200)
			assert.Equal(t, "Asia/Shanghai", summary.Timezone)
			assert.Equal(t, dateRange["start"], summary.Start)
			assert.Equal(t, dateRange["end"], summary.End)
			assert.ElementsMatch(t, heads, summary.Heads)
			assert.ElementsMatch(t, []billingTestHeadUsage{
				{SupplierID: heads[0].ID, SupplierName: "Alice", GatewayUsage: "0.300001", RequestCount: "16"},
				{SupplierID: heads[1].ID, SupplierName: "皮皮", GatewayUsage: "0.700005", RequestCount: "42"},
			}, summary.Totals)
			expectedDays := map[string]billingTestSummaryDay{
				"2024-01-02": {Date: "2024-01-02", GatewayUsage: "1.000000", RequestCount: "17", Heads: []billingTestHeadUsage{
					{heads[0].ID, "Alice", "0.300000", "5", true, []billingTestVendorUsage{{vendors[0].ID, "Alice", "0.100000", "2"}, {vendors[1].ID, "Alice 3", "0.200000", "3"}}},
					{heads[1].ID, "皮皮", "0.700000", "12", true, []billingTestVendorUsage{{vendors[2].ID, "Supplier B", "0.300000", "5"}, {0, "Unassigned supplier", "0.400000", "7"}}},
				}},
				"2024-01-03": {Date: "2024-01-03", GatewayUsage: "0.000006", RequestCount: "41", Heads: []billingTestHeadUsage{
					{heads[0].ID, "Alice", "0.000001", "11", true, []billingTestVendorUsage{{vendors[0].ID, "Alice", "0.000001", "11"}}},
					{heads[1].ID, "皮皮", "0.000005", "30", true, []billingTestVendorUsage{{vendors[2].ID, "Supplier B", "0.000005", "30"}}},
				}},
			}
			require.Len(t, summary.Days, 2)
			for _, day := range summary.Days {
				expected, ok := expectedDays[day.Date]
				require.True(t, ok)
				assert.Equal(t, expected.GatewayUsage, day.GatewayUsage)
				assert.Equal(t, expected.RequestCount, day.RequestCount)
				require.Len(t, day.Heads, 2)
				wantedHeads := map[int64]billingTestHeadUsage{}
				for _, head := range expected.Heads {
					wantedHeads[head.SupplierID] = head
				}
				for _, head := range day.Heads {
					want, ok := wantedHeads[head.SupplierID]
					require.True(t, ok)
					delete(wantedHeads, head.SupplierID)
					assert.ElementsMatch(t, want.Vendors, head.Vendors)
					want.Vendors, head.Vendors = nil, nil
					assert.Equal(t, want, head)
				}
			}
			ownVendors := billingResponse[[]billingTestVendor](t, alice.request(t, "GET", "/api/vendors", nil, nil), 200)
			require.Len(t, ownVendors, 2)
			for _, item := range ownVendors {
				assert.Equal(t, heads[0].ID, item.SupplierID)
			}
			assert.Equal(t, 403, alice.request(t, "GET", fmt.Sprintf("/api/vendors?supplier_id=%d", heads[1].ID), nil, nil).Code)
			assert.Equal(t, 403, alice.request(t, "GET", summaryURL+fmt.Sprintf("&supplier_id=%d", heads[1].ID), nil, nil).Code)
			for _, client := range []*billingTestClient{&manager, &alice} {
				url := summaryURL
				if client == &manager {
					url += fmt.Sprintf("&supplier_id=%d", heads[0].ID)
				}
				response := client.request(t, "GET", url, nil, nil)
				own := billingResponse[billingTestDailySummary](t, response, 200)
				assert.Equal(t, []Supplier{heads[0]}, own.Heads)
				require.Len(t, own.Totals, 1)
				assert.Equal(t, "0.300001", own.Totals[0].GatewayUsage)
				assert.NotContains(t, response.Body.String(), "Supplier B")
				assert.NotContains(t, response.Body.String(), "皮皮")
				for _, day := range own.Days {
					require.Len(t, day.Heads, 1)
					assert.Equal(t, heads[0].ID, day.Heads[0].SupplierID)
					assert.Equal(t, day.Heads[0].GatewayUsage, day.GatewayUsage)
					assert.Equal(t, day.Heads[0].RequestCount, day.RequestCount)
				}
			}

			// Filling old zero metadata preserves natural keys and raw usage totals.
			var before []Usage
			require.NoError(t, db.Where("origin = ?", "sync").Order("id").Find(&before).Error)
			require.Len(t, before, 7)
			require.NoError(t, db.Model(&Usage{}).Where("source_id = ?", sources[0].ID).Updates(map[string]any{"channel_id": 0, "vendor_id": 0}).Error)
			unassigned := Vendor{SupplierID: heads[1].ID, Name: "Supplier C", SourceStudio: "studio-c", BaseURL: upstream.URL, IdentityKey: digest([]any{upstream.URL, "studio-c"})}
			require.NoError(t, db.Create(&unassigned).Error)
			require.NoError(t, db.Create(&VendorKey{VendorID: unassigned.ID, BaseURL: upstream.URL, ChannelID: 22, BindingKey: digest([]any{upstream.URL, int64(22)})}).Error)
			rows[0]["request_count"] = 4
			body, err = common.Marshal(rows)
			require.NoError(t, err)
			responseBody.Store(string(body))
			require.NoError(t, db.Model(&VendorKey{}).Where("id = ?", keys[2].ID).Update("vendor_id", vendors[2].ID).Error)
			assert.Equal(t, 409, manager.request(t, "POST", fmt.Sprintf("/api/sources/%d/sync", sources[0].ID), dateRange, nil).Code)
			var unchangedMetadata []Usage
			require.NoError(t, db.Where("source_id = ?", sources[0].ID).Find(&unchangedMetadata).Error)
			oldByID := map[int64]Usage{}
			for _, item := range before {
				oldByID[item.ID] = item
			}
			for _, item := range unchangedMetadata {
				assert.Zero(t, item.ChannelID)
				assert.Zero(t, item.VendorID)
				assert.Equal(t, oldByID[item.ID].RequestCount, item.RequestCount)
				assert.Equal(t, oldByID[item.ID].Quantity, item.Quantity)
			}
			require.NoError(t, db.Model(&LedgerEntry{}).Where("supplier_id = ?", heads[0].ID).Count(&count).Error)
			assert.Equal(t, int64(1), count, "Failed metadata reconciliation preserves the existing manual charge only")
			require.NoError(t, db.Model(&VendorKey{}).Where("id = ?", keys[2].ID).Update("vendor_id", vendors[1].ID).Error)
			for _, source := range sources {
				assert.Equal(t, 200, manager.request(t, "POST", fmt.Sprintf("/api/sources/%d/sync", source.ID), dateRange, nil).Code)
			}
			var after []Usage
			require.NoError(t, db.Where("origin = ?", "sync").Order("id").Find(&after).Error)
			require.Len(t, after, len(before))
			channelsByQuantity := map[string]int64{"0.1": 11, "0.2": 12, "0.3": 21, "0.4": 22, "0.000001": 11, "0.000002": 21, "0.000003": 21}
			vendorsByChannel := map[int64]int64{11: vendors[0].ID, 12: vendors[1].ID, 21: vendors[2].ID, 22: unassigned.ID}
			for i, item := range after {
				assert.Equal(t, before[i].ID, item.ID)
				assert.Equal(t, before[i].ExternalRef, item.ExternalRef)
				assert.Equal(t, before[i].IdempotencyKey, item.IdempotencyKey)
				assert.Equal(t, before[i].Quantity, item.Quantity)
				assert.Equal(t, before[i].Amount, item.Amount)
				assert.Equal(t, channelsByQuantity[item.Quantity], item.ChannelID)
				assert.Equal(t, vendorsByChannel[item.ChannelID], item.VendorID)
			}
			reconciled := billingResponse[billingTestDailySummary](t, manager.request(t, "GET", summaryURL, nil, nil), 200)
			for _, day := range reconciled.Days {
				assert.Equal(t, expectedDays[day.Date].GatewayUsage, day.GatewayUsage)
				if day.Date == "2024-01-02" {
					assert.Equal(t, "19", day.RequestCount)
				} else {
					assert.Equal(t, "41", day.RequestCount)
				}
				for _, head := range day.Heads {
					for _, vendor := range head.Vendors {
						assert.NotZero(t, vendor.VendorID)
					}
				}
			}
			pending := sources[0]
			pending.ID, pending.Name, pending.ChannelIDs = 0, "Pending coverage", "99"
			pending = billingResponse[Source](t, manager.request(t, "POST", "/api/sources", pending, nil), 201)
			incomplete := billingResponse[billingTestDailySummary](t, manager.request(t, "GET", summaryURL, nil, nil), 200)
			for _, day := range incomplete.Days {
				for _, head := range day.Heads {
					assert.Equal(t, head.SupplierID != heads[0].ID, head.SourcesSynced)
				}
			}
			assert.Equal(t, 200, manager.request(t, "POST", fmt.Sprintf("/api/sources/%d/sync", pending.ID), dateRange, nil).Code)
			complete := billingResponse[billingTestDailySummary](t, manager.request(t, "GET", summaryURL, nil, nil), 200)
			for _, day := range complete.Days {
				for _, head := range day.Heads {
					assert.True(t, head.SourcesSynced)
				}
			}
		})
	}
}

func testBillingAuthentication(t *testing.T, s *Server, managerUser, supplierUser User, manager, vendor *billingTestClient) {
	t.Helper()
	anonymous := billingTestClient{server: s, handler: s.Handler("")}
	foreignCookies := map[string]string{"Cookie": "report_token=report-token; session=gateway-session", "Authorization": "Bearer report-jwt", "X-API-Key": "report-api-key"}
	assert.Equal(t, 401, anonymous.request(t, "GET", "/api/session", nil, foreignCookies).Code)
	assert.Equal(t, 403, vendor.request(t, "GET", "/api/users", nil, nil).Code)
	for _, headers := range []map[string]string{
		{"Origin": ""}, {"Origin": "https://attacker.example"}, {"X-CSRF-Token": ""}, {"X-CSRF-Token": "wrong-token"}, {"X-Requested-With": ""},
	} {
		assert.Equal(t, 403, manager.request(t, "POST", "/api/suppliers", map[string]string{"name": "CSRF must not write", "currency": "CNY"}, headers).Code)
	}
	var csrfWrites int64
	require.NoError(t, s.DB.Model(&Supplier{}).Where("name = ?", "CSRF must not write").Count(&csrfWrites).Error)
	assert.Zero(t, csrfWrites)
	for _, expiry := range []map[string]any{{"expires_at": time.Now().Unix() - 1}, {"last_seen_at": time.Now().Add(-31 * time.Minute).Unix()}} {
		expired := billingSessionClient(t, s, supplierUser)
		require.NoError(t, s.DB.Model(&Session{}).Where("token_hash = ?", hashSessionToken(expired.cookie.Value)).Updates(expiry).Error)
		assert.Equal(t, 401, expired.request(t, "GET", "/api/session", nil, nil).Code)
	}
	loginBody := map[string]string{"username": managerUser.Username, "password": "wrong-password"}
	wrong := anonymous.request(t, "POST", "/api/login", loginBody, nil)
	assert.Equal(t, 401, wrong.Code)
	loginBody["username"] = "nonexistent-user"
	unknown := anonymous.request(t, "POST", "/api/login", loginBody, nil)
	assert.Equal(t, 401, unknown.Code)
	assert.Equal(t, wrong.Body.String(), unknown.Body.String())
	loginBody["username"], loginBody["password"] = managerUser.Username, billingTestPassword
	assert.Equal(t, 403, anonymous.request(t, "POST", "/api/login", loginBody, map[string]string{"Origin": ""}).Code)
	assert.Equal(t, 200, anonymous.request(t, "POST", "/api/login", loginBody, nil).Code)
	require.NotNil(t, anonymous.cookie)
	assert.True(t, anonymous.cookie.HttpOnly)
	assert.True(t, anonymous.cookie.Secure)
	assert.Equal(t, http.SameSiteStrictMode, anonymous.cookie.SameSite)
	assert.Empty(t, anonymous.cookie.Domain)
	require.NotEmpty(t, anonymous.csrf)
	oldLogin := anonymous
	assert.Equal(t, 200, anonymous.request(t, "POST", "/api/login", loginBody, nil).Code)
	assert.NotEqual(t, oldLogin.cookie.Value, anonymous.cookie.Value)
	assert.Equal(t, 401, oldLogin.request(t, "GET", "/api/session", nil, nil).Code)
	beforeLogout := anonymous
	assert.Equal(t, 200, anonymous.request(t, "POST", "/api/logout", map[string]string{}, nil).Code)
	assert.Equal(t, 401, beforeLogout.request(t, "GET", "/api/session", nil, nil).Code)
	assert.Less(t, anonymous.cookie.MaxAge, 0)

	// Creation rejects weak and known-compromised passwords before storing a user.
	require.NoError(t, s.DB.Where("1 = 1").Delete(&LoginAttempt{}).Error)
	createBody := map[string]any{"username": "new-supplier", "password": "short", "role": RoleSupplier, "supplier_id": supplierUser.SupplierID, "current_password": billingTestPassword}
	for _, password := range []string{"short", "123456789123456789", strings.Repeat("x", 129)} {
		createBody["password"] = password
		assert.Equal(t, 400, manager.request(t, "POST", "/api/users", createBody, nil).Code)
	}
	createBody["password"] = "Cedar-orbit!violet-2026"
	createBody["current_password"] = "incorrect"
	assert.Equal(t, 403, manager.request(t, "POST", "/api/users", createBody, nil).Code)
	createBody["current_password"] = billingTestPassword
	oldManager := *manager
	createdResponse := manager.request(t, "POST", "/api/users", createBody, nil)
	createdUser := billingResponse[User](t, createdResponse, 201)
	assert.NotContains(t, createdResponse.Body.String(), "password")
	assert.Equal(t, 401, oldManager.request(t, "GET", "/api/session", nil, nil).Code)
	assert.Equal(t, 200, manager.request(t, "GET", "/api/users", nil, nil).Code)
	lastManagerURL := fmt.Sprintf("/api/users/%d/disable", managerUser.ID)
	assert.Equal(t, 409, manager.request(t, "POST", lastManagerURL, map[string]string{"current_password": billingTestPassword}, nil).Code)
	assert.Equal(t, 200, manager.request(t, "GET", "/api/session", nil, nil).Code)
	createdSession := billingSessionClient(t, s, createdUser)
	disableURL := fmt.Sprintf("/api/users/%d/disable", createdUser.ID)
	assert.Equal(t, 200, manager.request(t, "POST", disableURL, map[string]string{"current_password": billingTestPassword}, nil).Code)
	assert.Equal(t, 401, createdSession.request(t, "GET", "/api/session", nil, nil).Code)
	disabledLogin := billingTestClient{server: s, handler: s.Handler("")}
	disabled := disabledLogin.request(t, "POST", "/api/login", map[string]string{"username": createdUser.Username, "password": "Cedar-orbit!violet-2026"}, nil)
	assert.Equal(t, 401, disabled.Code)
	assert.Equal(t, wrong.Body.String(), disabled.Body.String())

	require.NoError(t, s.DB.Where("1 = 1").Delete(&LoginAttempt{}).Error)
	passwordBody := map[string]string{"current_password": "incorrect", "new_password": "Ocean-canvas!gold-2026"}
	assert.Equal(t, 403, vendor.request(t, "POST", "/api/password", passwordBody, nil).Code)
	passwordBody["current_password"] = billingTestPassword
	passwordBody["new_password"] = "short"
	assert.Equal(t, 400, vendor.request(t, "POST", "/api/password", passwordBody, nil).Code)
	passwordBody["new_password"] = "Ocean-canvas!gold-2026"
	oldVendor := *vendor
	otherSession := billingSessionClient(t, s, supplierUser)
	assert.Equal(t, 200, vendor.request(t, "POST", "/api/password", passwordBody, nil).Code)
	assert.NotEqual(t, oldVendor.cookie.Value, vendor.cookie.Value)
	assert.NotEqual(t, oldVendor.csrf, vendor.csrf)
	assert.Equal(t, 401, oldVendor.request(t, "GET", "/api/session", nil, nil).Code)
	assert.Equal(t, 401, otherSession.request(t, "GET", "/api/session", nil, nil).Code)
	assert.Equal(t, 200, vendor.request(t, "GET", "/api/session", nil, nil).Code)
	passwordLogin := billingTestClient{server: s, handler: s.Handler("")}
	assert.Equal(t, 401, passwordLogin.request(t, "POST", "/api/login", map[string]string{"username": supplierUser.Username, "password": billingTestPassword}, nil).Code)
	assert.Equal(t, 200, passwordLogin.request(t, "POST", "/api/login", map[string]string{"username": supplierUser.Username, "password": passwordBody["new_password"]}, nil).Code)

	// Seed the exact threshold instead of spending time on repeated expensive KDFs.
	now := time.Now().Unix()
	window := now - now%loginWindowSeconds
	blockedName := "throttled-user"
	require.NoError(t, s.DB.Create(&LoginAttempt{ID: hashSessionToken("account:" + blockedName + ":" + strconv.FormatInt(window, 10)), Attempts: 10, WindowStart: window}).Error)
	throttled := anonymous.request(t, "POST", "/api/login", map[string]string{"username": blockedName, "password": billingTestPassword}, nil)
	assert.Equal(t, 429, throttled.Code)
	assert.NotEmpty(t, throttled.Header().Get("Retry-After"))
}

func reportUsageFixture(hour, amount string, channel int64) map[string]any {
	return map[string]any{"hour": hour, "user_id": 1, "token_id": 2, "channel_id": channel, "model": "gpt-test", "request_count": 1, "input_tokens": 10, "output_tokens": 2, "cache_read_tokens": 3, "cache_write_tokens": 4, "total_cost": json.Number(amount), "username": "private-customer", "token_name": "private-token"}
}

func TestBillingChargeBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, quantity, price, discount, fx, want string }{
		{"discount_and_fx", "10", "2", "0.88", "7.2", "126.720000"},
		{"round_half_up", "0.000001", "0.5", "1", "1", "0.000001"},
		{"zero_discount", "100", "1", "0", "7.2", "0.000000"},
		{"max_amount", "1000000000000", "1", "1", "1", "1000000000000.000000"},
		{"negative_quantity", "-1", "1", "1", "1", ""},
		{"oversized_quantity", "1000000000001", "1", "1", "1", ""},
		{"oversized_total", "1000000000000", "2", "1", "1", ""},
		{"zero_fx", "1", "1", "1", "0", ""},
		{"discount_over_one", "1", "1", "1.01", "1", ""},
		{"exponent", "1e999999", "1", "1", "1", ""},
		{"too_precise", "0.0000001", "1", "1", "1", ""},
		{"nan", "NaN", "1", "1", "1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			amount, err := calculateCharge(tc.quantity, tc.price, tc.discount, tc.fx, "multiply")
			if tc.want == "" {
				require.Error(t, err)
				assert.Empty(t, amount)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, amount)
		})
	}
}

func TestReportSourceRejectsUnsafeOrIncompleteData(t *testing.T) {
	t.Setenv("SUPPLIER_BILLING_SOURCE_TEST", "test-source-token")
	day := time.Date(2024, 1, 2, 0, 0, 0, 0, businessZone)
	validRow := reportUsageFixture("2024-01-02 10:00", "1", 7)
	valid, err := common.Marshal([]any{validRow})
	require.NoError(t, err)
	duplicate, err := common.Marshal([]any{validRow, validRow})
	require.NoError(t, err)
	missing := reportUsageFixture("2024-01-02 10:00", "1", 7)
	delete(missing, "cache_write_tokens")
	missingJSON, err := common.Marshal([]any{missing})
	require.NoError(t, err)
	for _, tc := range []struct{ name, body string }{
		{"null", "null"}, {"object", "{}"}, {"truncated", string(valid[:len(valid)-1])},
		{"duplicate", string(duplicate)}, {"missing_count", string(missingJSON)},
		{"negative", strings.Replace(string(valid), `"total_cost":1`, `"total_cost":-1`, 1)},
		{"too_large", strings.Replace(string(valid), `"total_cost":1`, `"total_cost":1000000001`, 1)},
		{"huge_exponent", strings.Replace(string(valid), `"total_cost":1`, `"total_cost":1e999999`, 1)},
		{"fractional_tokens", strings.Replace(string(valid), `"input_tokens":10`, `"input_tokens":1.5`, 1)},
		{"invalid_hour", strings.Replace(string(valid), "2024-01-02 10:00", "2024-02-30 10:00", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, tc.body) }))
			defer upstream.Close()
			rows, err := FetchReportUsage(context.Background(), Source{BaseURL: upstream.URL, CredentialEnv: "SUPPLIER_BILLING_SOURCE_TEST", ChannelIDs: "7"}, day, day)
			require.Error(t, err)
			assert.Nil(t, rows)
		})
	}
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); _, _ = io.WriteString(w, "[]") }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	_, err = FetchReportUsage(context.Background(), Source{BaseURL: redirect.URL, CredentialEnv: "SUPPLIER_BILLING_SOURCE_TEST", ChannelIDs: "7"}, day, day)
	require.Error(t, err)
	assert.Zero(t, requests.Load())
	for _, source := range []Source{
		{BaseURL: target.URL, CredentialEnv: "PATH", ChannelIDs: "7"},
		{BaseURL: target.URL, CredentialEnv: "SUPPLIER_BILLING_SOURCE_TEST", ChannelIDs: ""},
		{BaseURL: target.URL, CredentialEnv: "SUPPLIER_BILLING_SOURCE_TEST", ChannelIDs: "7", GatewayUserID: -1},
		{BaseURL: target.URL, CredentialEnv: "SUPPLIER_BILLING_SOURCE_TEST", ChannelIDs: "7", GatewayUserID: 9007199254740992},
		{BaseURL: "http://external.example", CredentialEnv: "SUPPLIER_BILLING_SOURCE_TEST", ChannelIDs: "7"},
	} {
		_, err := FetchReportUsage(context.Background(), source, day, day)
		require.Error(t, err)
	}
	assert.Zero(t, requests.Load())
	rows, err := FetchReportUsage(context.Background(), Source{BaseURL: target.URL, CredentialEnv: "SUPPLIER_BILLING_SOURCE_TEST", ChannelIDs: "7"}, day, day)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestReportSourceGatewayUserScope(t *testing.T) {
	t.Setenv("SUPPLIER_BILLING_SOURCE_TEST", "test-source-token")
	day := time.Date(2024, 1, 2, 0, 0, 0, 0, businessZone)
	adminRow := reportUsageFixture("2024-01-02 10:00", "1.25", 7)
	otherRow := reportUsageFixture("2024-01-02 10:00", "900", 7)
	otherRow["user_id"] = 2
	body, err := common.Marshal([]any{adminRow, otherRow})
	require.NoError(t, err)
	var responseBody atomic.Value
	responseBody.Store(string(body))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, responseBody.Load().(string))
	}))
	defer upstream.Close()
	source := Source{BaseURL: upstream.URL, CredentialEnv: "SUPPLIER_BILLING_SOURCE_TEST", ChannelIDs: "7"}
	all, err := FetchReportUsage(context.Background(), source, day, day)
	require.NoError(t, err)
	require.Len(t, all, 2, "Zero gateway user ID preserves the existing all-users behavior")
	assert.Equal(t, "b9ed8eb6364096c5c62399835c53dde3cbf72ec460c09727283cf34b56b3d850", all[0].ExternalRef, "Persisted pre-filter import identity remains stable")
	assert.NotEqual(t, all[0].ExternalRef, all[1].ExternalRef)
	for _, tc := range []struct {
		userID int64
		want   []ImportedUsage
	}{
		{1, all[:1]}, {2, all[1:]}, {3, []ImportedUsage{}},
	} {
		source.GatewayUserID = tc.userID
		filtered, err := FetchReportUsage(context.Background(), source, day, day)
		require.NoError(t, err)
		assert.Equal(t, tc.want, filtered)
	}
	// Invalid facts outside the selected account still fail the complete import.
	otherRow["input_tokens"] = -1
	body, err = common.Marshal([]any{adminRow, otherRow})
	require.NoError(t, err)
	responseBody.Store(string(body))
	source.GatewayUserID = 1
	rows, err := FetchReportUsage(context.Background(), source, day, day)
	require.Error(t, err)
	assert.Nil(t, rows)
}
