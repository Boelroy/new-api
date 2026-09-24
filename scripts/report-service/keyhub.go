package main

// KHub (pd-maas) integration.
//
// KHub is an external key-management + usage portal reachable under
// https://khub.modelink.ai/pd-maas. It authenticates with a JWT obtained from
// POST /api/auth/login {username,password}; the token is then sent as
// `Authorization: Bearer <token>` and expires roughly every 24h.
//
// report-service acts as a scoped proxy for supplier_02 users (and admins):
// it holds ONE shared KHub "provider" account server-side, caches the JWT and
// auto-refreshes it, and exposes /api/keyhub/* endpoints. The browser never
// sees the KHub token or credentials, and the cross-origin call stays on the
// server (KHub sits behind APISIX with no CORS grant for our origin).
//
// Mirrors the shape of supplier_account.go (config in report_config with env
// fallback, mutex-guarded token cache, thin JSON pass-through handlers).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	// keyhubBaseURL is the KHub origin + app prefix, e.g.
	// "https://khub.modelink.ai/pd-maas". Empty disables the feature.
	keyhubBaseURL string
	// keyhubUsername / keyhubPassword drive the shared server-side login.
	keyhubUsername string
	keyhubPassword string
)

// report_config keys for admin-editable KHub settings. Env vars stay a
// bootstrap fallback (see the init in main.go).
const (
	cfgKeyhubBaseURL  = "keyhub_base_url"
	cfgKeyhubUsername = "keyhub_username"
	cfgKeyhubPassword = "keyhub_password"
)

// effectiveKeyhubBaseURL / Username / Password prefer the web-configured value,
// falling back to the env bootstrap. Base URL is normalised without a trailing
// slash so path concatenation is predictable.
func effectiveKeyhubBaseURL() string {
	if v := strings.TrimSpace(supplierConfigGet(cfgKeyhubBaseURL)); v != "" {
		return strings.TrimRight(v, "/")
	}
	return keyhubBaseURL
}

func effectiveKeyhubUsername() string {
	if v := strings.TrimSpace(supplierConfigGet(cfgKeyhubUsername)); v != "" {
		return v
	}
	return keyhubUsername
}

func effectiveKeyhubPassword() string {
	if v := supplierConfigGet(cfgKeyhubPassword); strings.TrimSpace(v) != "" {
		return v
	}
	return keyhubPassword
}

// keyhubConfigured reports whether the shared login can run (gates the page +
// every proxy handler).
func keyhubConfigured() bool {
	return effectiveKeyhubBaseURL() != "" && effectiveKeyhubUsername() != "" && effectiveKeyhubPassword() != ""
}

// keyhubHTTPClient talks to a public HTTPS endpoint (valid cert), so unlike the
// supplier portal we keep normal TLS verification.
var keyhubHTTPClient = &http.Client{Timeout: 30 * time.Second}

// ---- token cache + refresh ----

var (
	keyhubMu     sync.Mutex
	keyhubToken  string
	keyhubExpiry time.Time
)

// keyhubLoginResponse mirrors the KHub /api/auth/login payload.
type keyhubLoginResponse struct {
	Code string `json:"code"`
	Data struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	} `json:"data"`
	Message string `json:"message"`
}

// keyhubLogin runs the shared login and returns (token, expiry).
func keyhubLogin() (string, time.Time, error) {
	if !keyhubConfigured() {
		return "", time.Time{}, errors.New("keyhub base url / username / password not configured")
	}
	body, _ := json.Marshal(map[string]string{
		"username": effectiveKeyhubUsername(),
		"password": effectiveKeyhubPassword(),
	})
	st, resp, err := keyhubRawRequest(http.MethodPost, "/api/auth/login", "", body)
	if err != nil {
		return "", time.Time{}, err
	}
	if st < 200 || st >= 300 {
		return "", time.Time{}, fmt.Errorf("login: %s", keyhubErr(resp))
	}
	var lg keyhubLoginResponse
	if err := json.Unmarshal(resp, &lg); err != nil || lg.Data.Token == "" {
		return "", time.Time{}, errors.New("login: unexpected response")
	}
	// Prefer the server-declared expiry; fall back to a conservative 12h.
	exp := time.Now().Add(12 * time.Hour)
	if lg.Data.ExpiresAt != "" {
		if t, perr := time.Parse(time.RFC3339, lg.Data.ExpiresAt); perr == nil {
			exp = t
		}
	}
	return lg.Data.Token, exp, nil
}

// getKeyhubToken returns a valid token, refreshing via login when the cached
// one is missing or within a minute of expiry.
func getKeyhubToken() (string, error) {
	keyhubMu.Lock()
	defer keyhubMu.Unlock()
	if keyhubToken != "" && time.Now().Before(keyhubExpiry.Add(-time.Minute)) {
		return keyhubToken, nil
	}
	tok, exp, err := keyhubLogin()
	if err != nil {
		return "", err
	}
	keyhubToken = tok
	keyhubExpiry = exp
	return keyhubToken, nil
}

// forceKeyhubRelogin clears the cache so the next getKeyhubToken re-authenticates.
func forceKeyhubRelogin() {
	keyhubMu.Lock()
	keyhubToken = ""
	keyhubExpiry = time.Time{}
	keyhubMu.Unlock()
}

// keyhubRawRequest issues a single request to the base URL, no token cache.
func keyhubRawRequest(method, path, token string, jsonBody []byte) (int, []byte, error) {
	var reader io.Reader
	if jsonBody != nil {
		reader = bytes.NewReader(jsonBody)
	}
	req, err := http.NewRequest(method, effectiveKeyhubBaseURL()+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	if jsonBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := keyhubHTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// keyhubProxy issues an authenticated request, transparently re-logging in once
// on a 401 (expired/rotated token).
func keyhubProxy(method, path string, jsonBody []byte) (int, []byte, error) {
	tok, err := getKeyhubToken()
	if err != nil {
		return 0, nil, err
	}
	st, body, err := keyhubRawRequest(method, path, tok, jsonBody)
	if err != nil {
		return 0, nil, err
	}
	if st == http.StatusUnauthorized {
		forceKeyhubRelogin()
		tok, err = getKeyhubToken()
		if err != nil {
			return 0, nil, err
		}
		return keyhubRawRequest(method, path, tok, jsonBody)
	}
	return st, body, nil
}

// keyhubErr extracts the KHub {"message": "..."} error, falling back to the raw
// body.
func keyhubErr(body []byte) string {
	var e struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		return e.Message
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "upstream error"
	}
	return s
}

// keyhubForward proxies a GET to KHub and relays the status + raw JSON body back
// to the caller. Used by every read-only handler.
func keyhubForward(c *gin.Context, path string) {
	if !keyhubConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "keyhub not configured"})
		return
	}
	st, body, err := keyhubProxy(http.MethodGet, path, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.Data(st, "application/json; charset=utf-8", body)
}

// keyhubForwardQuery builds "<base>?<whitelisted query>" from the request's
// query string, keeping only the params KHub understands (and repeating
// multi-value ones like import_batch_id).
func keyhubForwardQuery(c *gin.Context, base string, allowed []string) {
	q := url.Values{}
	src := c.Request.URL.Query()
	for _, k := range allowed {
		for _, v := range src[k] {
			if v != "" {
				q.Add(k, v)
			}
		}
	}
	path := base
	if enc := q.Encode(); enc != "" {
		path = base + "?" + enc
	}
	keyhubForward(c, path)
}

// ---- handlers ----

// keyhubUsageLogParams are the filters accepted by the usage-log + stat views.
var keyhubUsageLogParams = []string{
	"start_timestamp", "end_timestamp", "page", "page_size", "import_batch_id",
	"category_code", "model_name", "api_key_id", "group", "log_type", "is_stream",
	"request_id", "upstream_request_id",
}

func handleKeyhubCategories(c *gin.Context) {
	keyhubForward(c, "/api/admin/key-management/categories")
}

func handleKeyhubKeysList(c *gin.Context) {
	keyhubForwardQuery(c, "/api/admin/key-management/keys", []string{"page", "page_size", "category_code", "keyword"})
}

func handleKeyhubImport(c *gin.Context) {
	if !keyhubConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "keyhub not configured"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 8<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	// Minimal shape guard so we don't forward obviously empty imports.
	var probe struct {
		CategoryCode string `json:"category_code"`
		RawText      string `json:"raw_text"`
	}
	if json.Unmarshal(raw, &probe) != nil || strings.TrimSpace(probe.CategoryCode) == "" || strings.TrimSpace(probe.RawText) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "category_code and raw_text are required"})
		return
	}
	st, body, err := keyhubProxy(http.MethodPost, "/api/admin/key-management/keys/import", raw)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.Data(st, "application/json; charset=utf-8", body)
}

func handleKeyhubUsageOverview(c *gin.Context) {
	keyhubForwardQuery(c, "/api/provider/keyhub-usage/dashboard/overview", []string{"start_timestamp", "end_timestamp"})
}

func handleKeyhubUsageFilterOptions(c *gin.Context) {
	keyhubForward(c, "/api/provider/keyhub-usage/logs/filter-options")
}

func handleKeyhubUsageLogs(c *gin.Context) {
	keyhubForwardQuery(c, "/api/provider/keyhub-usage/logs", keyhubUsageLogParams)
}

func handleKeyhubUsageLogStat(c *gin.Context) {
	keyhubForwardQuery(c, "/api/provider/keyhub-usage/logs/stat", keyhubUsageLogParams)
}

// ---- admin settings ----

func handleKeyhubSettingsGet(c *gin.Context) {
	pw := effectiveKeyhubPassword()
	c.JSON(http.StatusOK, gin.H{
		"base_url":     effectiveKeyhubBaseURL(),
		"username":     effectiveKeyhubUsername(),
		"password_set": strings.TrimSpace(pw) != "",
		"configured":   keyhubConfigured(),
	})
}

func handleKeyhubSettingsSet(c *gin.Context) {
	var body struct {
		// Pointers so an omitted field is left unchanged, distinct from "" (clear).
		BaseURL  *string `json:"base_url"`
		Username *string `json:"username"`
		Password *string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	set := func(key string, val *string, trim bool) bool {
		if val == nil {
			return true
		}
		v := *val
		if trim {
			v = strings.TrimSpace(v)
		}
		if err := supplierConfigSet(key, v); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return false
		}
		return true
	}
	if !set(cfgKeyhubBaseURL, body.BaseURL, true) {
		return
	}
	if !set(cfgKeyhubUsername, body.Username, true) {
		return
	}
	// Password stored as-is (no trim) but only when a non-empty value is sent;
	// an omitted field keeps the current password.
	if !set(cfgKeyhubPassword, body.Password, false) {
		return
	}
	// Any credential change invalidates the cached token.
	forceKeyhubRelogin()
	handleKeyhubSettingsGet(c)
}
