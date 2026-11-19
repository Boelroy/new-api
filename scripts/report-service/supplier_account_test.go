package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type supplierPortalTransport func(*http.Request) (*http.Response, error)

func (f supplierPortalTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestSupplierAccountCreateWithoutProxy(t *testing.T) {
	previousDB, previousClient := db, supplierHTTPClient
	previousOrigin, previousToken := supplierAccountBaseURL, supplierAccountToken
	t.Cleanup(func() {
		db, supplierHTTPClient = previousDB, previousClient
		supplierAccountBaseURL, supplierAccountToken = previousOrigin, previousToken
	})

	// A closed pool exercises the configuration fallbacks without opening a connection.
	pool, err := sql.Open("postgres", "")
	require.NoError(t, err)
	require.NoError(t, pool.Close())
	db = pool
	supplierAccountBaseURL, supplierAccountToken = "https://portal.invalid", "fixture-token"
	supplierHTTPClient = &http.Client{Transport: supplierPortalTransport(func(r *http.Request) (*http.Response, error) {
		defer r.Body.Close()
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "https://portal.invalid/supplier-account/api/openapi/accounts", r.URL.String())
		assert.Equal(t, "Bearer fixture-token", r.Header.Get("Authorization"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.JSONEq(t, `{
			"provider":"openai", "model":"fixture-model", "api_key":"fixture-key",
			"account_type":0, "tpm":"1000", "rpm":"10", "discount":"88"
		}`, string(body))

		w := httptest.NewRecorder()
		w.WriteHeader(http.StatusConflict)
		_, _ = w.WriteString(`{"detail":"account already exists"}`)
		return w.Result(), nil
	})}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("role", minAdminRole)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/supplier-account/accounts", strings.NewReader(`{
		"provider":"openai", "model":"fixture-model", "api_key":"fixture-key",
		"account_type":0, "tpm":"1000", "rpm":"10"
	}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handleSupplierAccountCreate(c)

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.JSONEq(t, `{"error":"account already exists"}`, w.Body.String())
}
