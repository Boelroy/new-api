package main

// Supplier Account portal integration.
//
// A third-party "账号资源录入系统" (API Account Portal) exposes two auth lanes:
//
//   - WEB token: authenticates the portal UI endpoints (providers / models).
//     Obtained via a login flow — fetch a per-user RSA public key, encrypt the
//     password with hybrid AES-GCM + RSA-OAEP, then POST /supplier/login. The
//     token expires after `expires_in` seconds, so we cache + auto-refresh it.
//   - OpenAPI token: authenticates the /openapi/* endpoints (account upload +
//     metrics query). Issued separately by the portal admin and supplied via
//     SUPPLIER_ACCOUNT_TOKEN.
//
// report-service acts as a scoped proxy:
//   - supplier_01 studios upload keys and see ONLY their own accounts + usage.
//   - admin / super_admin see every account and its usage, and can upload too.
//
// Ownership is tracked locally in rs_supplier_account (uploaded_by). Upstream
// calls use a single server-side WEB login + OpenAPI token, so the scoping is
// enforced here, not by the upstream credentials.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	// supplierAccountBaseURL is the portal origin, e.g.
	// "https://120.233.254.152:8000". Empty disables the feature.
	supplierAccountBaseURL string
	// supplierAccountUsername / supplierAccountPassword drive the WEB login
	// flow (providers / models). Required for the page to render.
	supplierAccountUsername string
	supplierAccountPassword string
	// supplierAccountToken is the OpenAPI token for /openapi/* (upload +
	// metrics). Filled later by the portal admin.
	supplierAccountToken string
)

// supplierWebConfigured reports whether the WEB login flow can run (drives the
// nav item + providers/models).
func supplierWebConfigured() bool {
	return supplierAccountBaseURL != "" && supplierAccountUsername != "" && supplierAccountPassword != ""
}

// report_config keys for admin-editable supplier settings. The OpenAPI token
// and the supplier-visible provider allowlist are configured through the web
// UI and stored in report_config (env stays a bootstrap fallback for token).
const (
	cfgSupplierOpenAPIToken     = "supplier_openapi_token"
	cfgSupplierVisibleProvers   = "supplier_visible_providers"
	cfgSupplierProviderDefaults = "supplier_provider_defaults"
)

// supplierProviderDefault is the admin-configured prefill for one provider:
// which models to pre-select and the default account type when a studio picks
// that provider in the upload form.
type supplierProviderDefault struct {
	Models      []string `json:"models"`
	AccountType int      `json:"account_type"`
}

// supplierProviderDefaults returns the per-provider prefill map, empty when
// unset or unparseable.
func supplierProviderDefaults() map[string]supplierProviderDefault {
	out := map[string]supplierProviderDefault{}
	raw := strings.TrimSpace(supplierConfigGet(cfgSupplierProviderDefaults))
	if raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// supplierConfigGet reads a report_config value, empty string when unset.
func supplierConfigGet(key string) string {
	var v string
	if err := db.QueryRow(`SELECT value FROM report_config WHERE key=$1`, key).Scan(&v); err != nil {
		return ""
	}
	return v
}

// supplierConfigSet upserts a report_config value.
func supplierConfigSet(key, value string) error {
	_, err := db.Exec(
		`INSERT INTO report_config (key, value, updated_at) VALUES ($1, $2, $3)
		 ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=EXCLUDED.updated_at`,
		key, value, time.Now().Unix(),
	)
	return err
}

// effectiveOpenAPIToken prefers the web-configured token, falling back to the
// SUPPLIER_ACCOUNT_TOKEN env for bootstrap.
func effectiveOpenAPIToken() string {
	if t := strings.TrimSpace(supplierConfigGet(cfgSupplierOpenAPIToken)); t != "" {
		return t
	}
	return supplierAccountToken
}

// supplierVisibleProviders returns the admin-configured allowlist of provider
// names visible to supplier_01 users. Empty slice = all providers visible.
func supplierVisibleProviders() []string {
	raw := strings.TrimSpace(supplierConfigGet(cfgSupplierVisibleProvers))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// supplierProviderAllowed reports whether a provider is visible to a supplier
// given the allowlist. An empty allowlist permits everything.
func supplierProviderAllowed(allow []string, name string) bool {
	if len(allow) == 0 {
		return true
	}
	for _, a := range allow {
		if a == name {
			return true
		}
	}
	return false
}

// supplierOpenAPIConfigured reports whether upload + metrics can run.
func supplierOpenAPIConfigured() bool {
	return supplierAccountBaseURL != "" && effectiveOpenAPIToken() != ""
}

// supplierAccountEnabled gates the page/nav. The upload + metrics paths
// additionally require supplierOpenAPIConfigured().
func supplierAccountEnabled() bool {
	return supplierWebConfigured()
}

// supplierHTTPClient talks to a portal that serves a self-signed cert (every
// sample curl uses --insecure), so TLS verification is intentionally skipped.
var supplierHTTPClient = &http.Client{
	Timeout: 20 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	},
}

// ---- WEB token cache + refresh ----

var (
	supplierWebMu     sync.Mutex
	supplierWebToken  string
	supplierWebExpiry time.Time
)

// supplierProxy issues a request to the portal, optionally with a Bearer
// token, and returns the raw status code + body. jsonBody may be nil for GETs.
func supplierProxy(method, path, token string, jsonBody []byte) (int, []byte, error) {
	var reader io.Reader
	if jsonBody != nil {
		reader = bytes.NewReader(jsonBody)
	}
	req, err := http.NewRequest(method, supplierAccountBaseURL+path, reader)
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
	resp, err := supplierHTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// supplierErr extracts the portal's {"detail": "..."} error, falling back to
// the raw body.
func supplierErr(body []byte) string {
	var e struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &e) == nil && e.Detail != "" {
		return e.Detail
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "upstream error"
	}
	return s
}

// supplierEncryptPassword mirrors the portal's front-end hybrid scheme:
// AES-256-GCM encrypt the UTF-8 password, RSA-OAEP(SHA-256) encrypt the AES
// key, then base64 a {__enc__,k,iv,ct} JSON envelope. Returns the value the
// login endpoint expects in enc_password.
func supplierEncryptPassword(pubPEM, password string) (string, error) {
	block, _ := pem.Decode([]byte(pubPEM))
	if block == nil {
		return "", errors.New("invalid public key PEM")
	}
	pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse public key: %w", err)
	}
	pub, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		return "", errors.New("public key is not RSA")
	}

	aesKey := make([]byte, 32)
	if _, err := rand.Read(aesKey); err != nil {
		return "", err
	}
	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	blockC, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(blockC)
	if err != nil {
		return "", err
	}
	// Seal appends the 16-byte tag after the ciphertext — matches forge's
	// `output.getBytes() + tag.getBytes()`.
	ctTag := gcm.Seal(nil, iv, []byte(password), nil)

	encKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, aesKey, nil)
	if err != nil {
		return "", err
	}

	envelope := struct {
		Enc string `json:"__enc__"`
		K   string `json:"k"`
		IV  string `json:"iv"`
		CT  string `json:"ct"`
	}{
		Enc: "hybrid-aesgcm",
		K:   base64.StdEncoding.EncodeToString(encKey),
		IV:  base64.StdEncoding.EncodeToString(iv),
		CT:  base64.StdEncoding.EncodeToString(ctTag),
	}
	jsonBytes, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(jsonBytes), nil
}

// supplierLogin runs the full WEB login flow and returns (token, expiresIn).
func supplierLogin() (string, int, error) {
	if !supplierWebConfigured() {
		return "", 0, errors.New("supplier account username/password not configured")
	}
	// 1. Fetch the per-user RSA public key.
	pkReq, _ := json.Marshal(map[string]string{"username": supplierAccountUsername})
	st, resp, err := supplierProxy(http.MethodPost, "/supplier-account/api/supplier/pubkey", "", pkReq)
	if err != nil {
		return "", 0, err
	}
	if st < 200 || st >= 300 {
		return "", 0, fmt.Errorf("pubkey: %s", supplierErr(resp))
	}
	var pk struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(resp, &pk); err != nil || pk.PublicKey == "" {
		return "", 0, errors.New("pubkey: unexpected response")
	}

	// 2. Encrypt the password with the fetched key.
	enc, err := supplierEncryptPassword(pk.PublicKey, supplierAccountPassword)
	if err != nil {
		return "", 0, err
	}

	// 3. Log in.
	loginReq, _ := json.Marshal(map[string]string{"username": supplierAccountUsername, "enc_password": enc})
	st2, resp2, err := supplierProxy(http.MethodPost, "/supplier-account/api/supplier/login", "", loginReq)
	if err != nil {
		return "", 0, err
	}
	if st2 < 200 || st2 >= 300 {
		return "", 0, fmt.Errorf("login: %s", supplierErr(resp2))
	}
	var lg struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(resp2, &lg); err != nil || lg.Token == "" {
		return "", 0, errors.New("login: unexpected response")
	}
	if lg.ExpiresIn <= 0 {
		lg.ExpiresIn = 3600
	}
	return lg.Token, lg.ExpiresIn, nil
}

// getSupplierWebToken returns a valid WEB token, refreshing via login when the
// cached one is missing or within a minute of expiry.
func getSupplierWebToken() (string, error) {
	supplierWebMu.Lock()
	defer supplierWebMu.Unlock()
	if supplierWebToken != "" && time.Now().Before(supplierWebExpiry) {
		return supplierWebToken, nil
	}
	tok, expiresIn, err := supplierLogin()
	if err != nil {
		return "", err
	}
	supplierWebToken = tok
	// Refresh a minute early; for very short TTLs fall back to half the TTL.
	lead := 60
	if expiresIn <= 120 {
		lead = expiresIn / 2
	}
	supplierWebExpiry = time.Now().Add(time.Duration(expiresIn-lead) * time.Second)
	return tok, nil
}

// startSupplierWebTokenRefresher logs in at startup and keeps the WEB token
// fresh, re-logging in shortly before each expiry. No-op when not configured.
func startSupplierWebTokenRefresher() {
	if !supplierWebConfigured() {
		return
	}
	go func() {
		for {
			if _, err := getSupplierWebToken(); err != nil {
				log.Printf("[supplier] web token login failed: %v", err)
				time.Sleep(60 * time.Second)
				continue
			}
			supplierWebMu.Lock()
			sleep := time.Until(supplierWebExpiry)
			supplierWebMu.Unlock()
			if sleep < 30*time.Second {
				sleep = 30 * time.Second
			}
			time.Sleep(sleep)
		}
	}()
}

// ---- Proxy passthrough (providers / models) — WEB token ----

func handleSupplierProviders(c *gin.Context) {
	if !supplierAccountEnabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "supplier account portal not configured"})
		return
	}
	tok, err := getSupplierWebToken()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "supplier login failed: " + err.Error()})
		return
	}
	status, body, err := supplierProxy(http.MethodGet, "/supplier-account/api/providers", tok, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if status != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": supplierErr(body)})
		return
	}
	// Admins see every provider (needed to configure the allowlist); suppliers
	// see only the admin-permitted ones.
	if !callerIsSupplierAdmin(c) {
		if allow := supplierVisibleProviders(); len(allow) > 0 {
			var parsed struct {
				List []json.RawMessage `json:"list"`
			}
			if json.Unmarshal(body, &parsed) == nil {
				filtered := make([]json.RawMessage, 0, len(parsed.List))
				for _, raw := range parsed.List {
					var p struct {
						Name string `json:"name"`
					}
					if json.Unmarshal(raw, &p) == nil && supplierProviderAllowed(allow, p.Name) {
						filtered = append(filtered, raw)
					}
				}
				if out, err := json.Marshal(gin.H{"list": filtered}); err == nil {
					c.Data(http.StatusOK, "application/json", out)
					return
				}
			}
		}
	}
	c.Data(http.StatusOK, "application/json", body)
}

func handleSupplierModels(c *gin.Context) {
	if !supplierAccountEnabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "supplier account portal not configured"})
		return
	}
	tok, err := getSupplierWebToken()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "supplier login failed: " + err.Error()})
		return
	}
	status, body, err := supplierProxy(http.MethodGet, "/supplier-account/api/models", tok, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if status != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": supplierErr(body)})
		return
	}
	c.Data(http.StatusOK, "application/json", body)
}

// ---- Upload (create account) — OpenAPI token ----

func handleSupplierAccountCreate(c *gin.Context) {
	if supplierAccountBaseURL == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "supplier account portal not configured"})
		return
	}
	if !supplierOpenAPIConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "supplier OpenAPI token not configured (set SUPPLIER_ACCOUNT_TOKEN)"})
		return
	}
	var body struct {
		Provider    string `json:"provider"`
		Model       string `json:"model"`
		APIKey      string `json:"api_key"`
		AccountID   string `json:"account_id"`
		AccountType int    `json:"account_type"`
		Remark      string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	body.Provider = strings.TrimSpace(body.Provider)
	body.Model = strings.TrimSpace(body.Model)
	body.APIKey = strings.TrimSpace(body.APIKey)
	if body.Provider == "" || body.Model == "" || body.APIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider, model and api_key are required"})
		return
	}
	if body.AccountType != 0 && body.AccountType != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "account_type must be 0 or 1"})
		return
	}
	// Suppliers may only upload for providers the admin has made visible.
	if !callerIsSupplierAdmin(c) {
		if allow := supplierVisibleProviders(); !supplierProviderAllowed(allow, body.Provider) {
			c.JSON(http.StatusForbidden, gin.H{"error": "该厂商未开放上号，请联系管理员"})
			return
		}
	}

	upstreamReq := map[string]any{
		"provider":     body.Provider,
		"model":        body.Model,
		"api_key":      body.APIKey,
		"account_type": body.AccountType,
	}
	if body.AccountID != "" {
		upstreamReq["account_id"] = body.AccountID
	}
	if body.Remark != "" {
		upstreamReq["remark"] = body.Remark
	}
	reqBytes, _ := json.Marshal(upstreamReq)

	status, respBody, err := supplierProxy(http.MethodPost, "/supplier-account/api/openapi/accounts", effectiveOpenAPIToken(), reqBytes)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if status < 200 || status >= 300 {
		c.JSON(http.StatusBadGateway, gin.H{"error": supplierErr(respBody)})
		return
	}
	var upstream struct {
		ID    int64  `json:"id"`
		Alias string `json:"alias"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal(respBody, &upstream); err != nil || upstream.ID == 0 {
		c.JSON(http.StatusBadGateway, gin.H{"error": "unexpected upstream response: " + strings.TrimSpace(string(respBody))})
		return
	}

	uidAny, _ := c.Get("user_id")
	uid, _ := uidAny.(int64)
	studioAny, _ := c.Get("studio")
	studio, _ := studioAny.(string)

	now := time.Now().Unix()
	sum := sha256.Sum256([]byte(body.APIKey))
	keyHash := hex.EncodeToString(sum[:])
	last8 := body.APIKey
	if len(last8) > 8 {
		last8 = last8[len(last8)-8:]
	}

	// Upsert on remote_account_id so re-submitting the same account (portal
	// dedupes by key) refreshes ownership/metadata instead of erroring.
	if _, err := db.Exec(
		`INSERT INTO rs_supplier_account
		   (uploaded_by, studio, provider, models, remote_account_id, alias,
		    account_type, remark, key_last8, key_hash, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
		 ON CONFLICT (remote_account_id) DO UPDATE SET
		    uploaded_by=EXCLUDED.uploaded_by, studio=EXCLUDED.studio,
		    provider=EXCLUDED.provider, models=EXCLUDED.models,
		    alias=EXCLUDED.alias, account_type=EXCLUDED.account_type,
		    remark=EXCLUDED.remark, key_last8=EXCLUDED.key_last8,
		    key_hash=EXCLUDED.key_hash, updated_at=EXCLUDED.updated_at`,
		uid, studio, body.Provider, body.Model, upstream.ID, upstream.Alias,
		body.AccountType, body.Remark, last8, keyHash, now,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record account: " + err.Error()})
		return
	}

	msg := upstream.Msg
	if msg == "" {
		msg = "提交成功"
	}
	c.JSON(http.StatusOK, gin.H{"id": upstream.ID, "alias": upstream.Alias, "msg": msg})
}

// ---- List local accounts (scoped by role) ----

type supplierAccountRow struct {
	ID              int64  `json:"id"`
	RemoteAccountID int64  `json:"remote_account_id"`
	Provider        string `json:"provider"`
	Models          string `json:"models"`
	Alias           string `json:"alias"`
	AccountType     int    `json:"account_type"`
	Remark          string `json:"remark"`
	KeyLast8        string `json:"key_last8"`
	Studio          string `json:"studio"`
	UploadedBy      int64  `json:"uploaded_by"`
	Username        string `json:"username,omitempty"`
	CreatedAt       int64  `json:"created_at"`
}

func handleSupplierAccountList(c *gin.Context) {
	isAdmin := callerIsSupplierAdmin(c)

	query := `SELECT a.id, a.remote_account_id, a.provider, a.models, a.alias,
	                 a.account_type, a.remark, a.key_last8, a.studio,
	                 a.uploaded_by, COALESCE(u.username, ''), a.created_at
	            FROM rs_supplier_account a
	            LEFT JOIN rs_auth_user u ON u.id = a.uploaded_by`
	var (
		rows interface {
			Next() bool
			Scan(...any) error
			Close() error
		}
		err error
	)
	if isAdmin {
		r, e := db.Query(query + ` ORDER BY a.id DESC`)
		rows, err = r, e
	} else {
		uidAny, _ := c.Get("user_id")
		uid, _ := uidAny.(int64)
		r, e := db.Query(query+` WHERE a.uploaded_by=$1 ORDER BY a.id DESC`, uid)
		rows, err = r, e
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	out := make([]supplierAccountRow, 0)
	for rows.Next() {
		var row supplierAccountRow
		if err := rows.Scan(
			&row.ID, &row.RemoteAccountID, &row.Provider, &row.Models, &row.Alias,
			&row.AccountType, &row.Remark, &row.KeyLast8, &row.Studio,
			&row.UploadedBy, &row.Username, &row.CreatedAt,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// Non-admins never learn who else uploaded — strip owner identity.
		if !isAdmin {
			row.Username = ""
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"accounts": out})
}

// callerIsSupplierAdmin returns true when the caller may see every studio's
// accounts (admin and above). Suppliers see only their own uploads.
func callerIsSupplierAdmin(c *gin.Context) bool {
	roleAny, _ := c.Get("role")
	role, _ := roleAny.(int)
	return role >= minAdminRole
}

// ---- Realtime metrics (scoped by role) — OpenAPI token ----

// supplierMetric mirrors one entry of the portal metrics response. Cost is a
// pointer so it can be omitted for non-admin callers.
type supplierMetric struct {
	AID              int64    `json:"aid"`
	AccountAlias     string   `json:"account_alias"`
	Status           string   `json:"status"`
	Requests         *int64   `json:"requests"`
	Cost             *float64 `json:"cost,omitempty"`
	SuccessRate      *float64 `json:"success_rate"`
	PromptTokens     *int64   `json:"prompt_tokens"`
	CompletionTokens *int64   `json:"completion_tokens"`
}

func handleSupplierMetrics(c *gin.Context) {
	if supplierAccountBaseURL == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "supplier account portal not configured"})
		return
	}
	if !supplierOpenAPIConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "supplier OpenAPI token not configured (set SUPPLIER_ACCOUNT_TOKEN)"})
		return
	}
	var body struct {
		BeginTime string `json:"begin_time"`
		EndTime   string `json:"end_time"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	body.BeginTime = strings.TrimSpace(body.BeginTime)
	body.EndTime = strings.TrimSpace(body.EndTime)
	if body.BeginTime == "" || body.EndTime == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "begin_time and end_time are required"})
		return
	}

	isAdmin := callerIsSupplierAdmin(c)

	// Resolve the account id set this caller is allowed to query.
	var (
		idRows interface {
			Next() bool
			Scan(...any) error
			Close() error
		}
		err error
	)
	if isAdmin {
		r, e := db.Query(`SELECT remote_account_id FROM rs_supplier_account ORDER BY id DESC`)
		idRows, err = r, e
	} else {
		uidAny, _ := c.Get("user_id")
		uid, _ := uidAny.(int64)
		r, e := db.Query(`SELECT remote_account_id FROM rs_supplier_account WHERE uploaded_by=$1 ORDER BY id DESC`, uid)
		idRows, err = r, e
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ids := make([]int64, 0)
	for idRows.Next() {
		var id int64
		if err := idRows.Scan(&id); err != nil {
			idRows.Close()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		ids = append(ids, id)
	}
	idRows.Close()

	if len(ids) == 0 {
		c.JSON(http.StatusOK, gin.H{"accounts": []supplierMetric{}})
		return
	}

	// The portal caps a query at 100 account ids; batch and merge.
	merged := make([]supplierMetric, 0, len(ids))
	for start := 0; start < len(ids); start += 100 {
		end := start + 100
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		reqBytes, _ := json.Marshal(map[string]any{
			"account_ids": chunk,
			"begin_time":  body.BeginTime,
			"end_time":    body.EndTime,
			"aggregate":   false,
		})
		status, respBody, err := supplierProxy(http.MethodPost, "/supplier-account/api/openapi/metrics/query", effectiveOpenAPIToken(), reqBytes)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		if status < 200 || status >= 300 {
			c.JSON(http.StatusBadGateway, gin.H{"error": supplierErr(respBody)})
			return
		}
		var parsed struct {
			Accounts []supplierMetric `json:"accounts"`
		}
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "unexpected upstream response"})
			return
		}
		merged = append(merged, parsed.Accounts...)
	}

	// Suppliers must not see cost — strip it before serializing.
	if !isAdmin {
		for i := range merged {
			merged[i].Cost = nil
		}
	}

	c.JSON(http.StatusOK, gin.H{"accounts": merged})
}

// ---- Admin settings: OpenAPI token + provider visibility ----

func handleSupplierSettingsGet(c *gin.Context) {
	token := effectiveOpenAPIToken()
	last4 := ""
	if len(token) > 4 {
		last4 = token[len(token)-4:]
	}
	vp := supplierVisibleProviders()
	if vp == nil {
		vp = []string{}
	}
	c.JSON(http.StatusOK, gin.H{
		"openapi_token_set":   token != "",
		"openapi_token_last4": last4,
		"visible_providers":   vp,
		"provider_defaults":   supplierProviderDefaults(),
	})
}

// handleSupplierProviderDefaults exposes the prefill map to the upload form.
// Available to suppliers too (admin+supplier group) — the defaults are not
// sensitive and drive the studio's form pre-selection.
func handleSupplierProviderDefaults(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"defaults": supplierProviderDefaults()})
}

func handleSupplierSettingsSet(c *gin.Context) {
	var body struct {
		// Pointers so "omitted" (leave unchanged) is distinct from "" (clear)
		// and [] (allow all).
		OpenAPIToken     *string                             `json:"openapi_token"`
		VisibleProviders *[]string                           `json:"visible_providers"`
		ProviderDefaults *map[string]supplierProviderDefault `json:"provider_defaults"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if body.OpenAPIToken != nil {
		if err := supplierConfigSet(cfgSupplierOpenAPIToken, strings.TrimSpace(*body.OpenAPIToken)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.VisibleProviders != nil {
		seen := map[string]bool{}
		cleaned := make([]string, 0, len(*body.VisibleProviders))
		for _, p := range *body.VisibleProviders {
			if p = strings.TrimSpace(p); p != "" && !seen[p] {
				seen[p] = true
				cleaned = append(cleaned, p)
			}
		}
		if err := supplierConfigSet(cfgSupplierVisibleProvers, strings.Join(cleaned, ",")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.ProviderDefaults != nil {
		cleaned := map[string]supplierProviderDefault{}
		for prov, d := range *body.ProviderDefaults {
			prov = strings.TrimSpace(prov)
			if prov == "" {
				continue
			}
			at := d.AccountType
			if at != 0 && at != 1 {
				at = 0
			}
			seen := map[string]bool{}
			models := make([]string, 0, len(d.Models))
			for _, m := range d.Models {
				if m = strings.TrimSpace(m); m != "" && !seen[m] {
					seen[m] = true
					models = append(models, m)
				}
			}
			// Drop no-op entries (no default models and the default account
			// type) so the stored map stays tidy.
			if len(models) == 0 && at == 0 {
				continue
			}
			cleaned[prov] = supplierProviderDefault{Models: models, AccountType: at}
		}
		blob, _ := json.Marshal(cleaned)
		if err := supplierConfigSet(cfgSupplierProviderDefaults, string(blob)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	handleSupplierSettingsGet(c)
}
