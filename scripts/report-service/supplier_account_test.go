package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

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
