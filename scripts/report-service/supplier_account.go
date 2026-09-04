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
	"sort"
	"strconv"
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
	// supplierQuotaWebhook is the bootstrap fallback (env SUPPLIER_QUOTA_WEBHOOK)
	// for the per-account quota alert channel. Independent of LARK_WEBHOOK
	// (group-balance alerts). The report_config override takes precedence.
	supplierQuotaWebhook string
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
	cfgSupplierQuotaWebhook     = "supplier_quota_webhook"
	cfgSupplierFxRate           = "supplier_fx_rate"
	cfgSupplierQuotaTickSec     = "supplier_quota_tick_sec"
	cfgSupplierDefaultDiscount  = "supplier_default_discount"
	cfgSupplierAnnouncePushed   = "supplier_announce_pushed_max"
	cfgDefaultFxRate            = "default_fx_rate"
)

// supplierDefaultFxRate is the RMB->USD divisor used when no rate is
// configured. The portal reports cost in RMB; the UI and quotas are in USD.
const supplierDefaultFxRate = 7.2

// supplierDefaultDiscount is the settlement discount (percentage, % stripped)
// applied to 普通号 uploads when the admin has not configured one. Not exposed
// in the upload form — filled server-side from the admin config.
const supplierDefaultDiscount = "88"

// supplierAccountDiscount returns the admin-configured default settlement
// discount for 普通号 uploads, falling back to supplierDefaultDiscount. Always
// a valid non-negative number string.
func supplierAccountDiscount() string {
	if raw := strings.TrimSpace(supplierConfigGet(cfgSupplierDefaultDiscount)); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
			return raw
		}
	}
	return supplierDefaultDiscount
}

// supplierQuotaTickSec bounds for the alert loop interval (seconds).
const (
	supplierQuotaTickDef = 3600
	supplierQuotaTickMin = 300
	supplierQuotaTickMax = 86400
)

// supplierFxRate returns the RMB->USD divisor: the supplier-specific rate if
// set, else the service-wide default_fx_rate, else supplierDefaultFxRate.
// Always > 0.
func supplierFxRate() float64 {
	for _, key := range []string{cfgSupplierFxRate, cfgDefaultFxRate} {
		if raw := strings.TrimSpace(supplierConfigGet(key)); raw != "" {
			if f, err := strconv.ParseFloat(raw, 64); err == nil && f > 0 {
				return f
			}
		}
	}
	return supplierDefaultFxRate
}

// effectiveQuotaWebhook prefers the web-configured quota webhook, falling back
// to the SUPPLIER_QUOTA_WEBHOOK env for bootstrap.
func effectiveQuotaWebhook() string {
	if w := strings.TrimSpace(supplierConfigGet(cfgSupplierQuotaWebhook)); w != "" {
		return w
	}
	return supplierQuotaWebhook
}

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
		Tpm         string `json:"tpm"`
		Rpm         string `json:"rpm"`
		Remark      string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	body.Provider = strings.TrimSpace(body.Provider)
	body.Model = strings.TrimSpace(body.Model)
	body.APIKey = strings.TrimSpace(body.APIKey)
	body.Tpm = strings.TrimSpace(body.Tpm)
	body.Rpm = strings.TrimSpace(body.Rpm)
	if body.Provider == "" || body.Model == "" || body.APIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider, model and api_key are required"})
		return
	}
	if body.AccountType != 0 && body.AccountType != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "account_type must be 0 or 1"})
		return
	}
	// tpm / rpm are mandatory for 普通号 (account_type=0); 速刷号
	// (account_type=1) may omit them. When present they must be non-negative
	// integers. discount is NOT user-facing — it is filled server-side from the
	// admin-configured default (see supplierAccountDiscount).
	if body.AccountType == 0 && (body.Tpm == "" || body.Rpm == "") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "普通号必须填写 tpm 和 rpm"})
		return
	}
	if body.Tpm != "" {
		if n, err := strconv.Atoi(body.Tpm); err != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "tpm 必须为非负整数"})
			return
		}
	}
	if body.Rpm != "" {
		if n, err := strconv.Atoi(body.Rpm); err != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "rpm 必须为非负整数"})
			return
		}
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
	if body.Tpm != "" {
		upstreamReq["tpm"] = body.Tpm
	}
	if body.Rpm != "" {
		upstreamReq["rpm"] = body.Rpm
	}
	// discount is server-side policy: 普通号 always settles at the admin default.
	// 速刷号 leaves it unset (portal allows omitting it).
	if body.AccountType == 0 {
		upstreamReq["discount"] = supplierAccountDiscount()
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

// ---- Sync portal roster into local table (admin) — WEB token ----

// portalAccount is one entry of the WEB `/supplier/accounts` list. The portal
// returns the full api_key here (unlike our masked local view). `ID` is the
// account id used as `account_ids` in metrics queries; CreatedAt is
// "2006-01-02 15:04" in the portal's local time.
type portalAccount struct {
	ID           int64  `json:"id"`
	AccountAlias string `json:"account_alias"`
	AccountType  int    `json:"account_type"`
	APIKey       string `json:"api_key"`
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	Remark       string `json:"remark"`
	SupplierName string `json:"supplier_name"`
	CreatedAt    string `json:"created_at"`
}

// fetchSupplierPortalAccounts pages through the WEB `/supplier/accounts`
// endpoint and returns the full roster the logged-in supplier can see.
func fetchSupplierPortalAccounts() ([]portalAccount, error) {
	tok, err := getSupplierWebToken()
	if err != nil {
		return nil, fmt.Errorf("supplier login failed: %w", err)
	}
	const pageSize = 100
	out := make([]portalAccount, 0, pageSize)
	// Cap pages as a runaway guard (100 pages = 10k accounts).
	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf(
			"/supplier-account/api/supplier/accounts?status=all&keyword=&page=%d&page_size=%d&provider=&model=&account_type=",
			page, pageSize,
		)
		status, body, err := supplierProxy(http.MethodGet, path, tok, nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, errors.New(supplierErr(body))
		}
		var parsed struct {
			List []portalAccount `json:"list"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("unexpected accounts response: %w", err)
		}
		out = append(out, parsed.List...)
		if len(parsed.List) < pageSize {
			break
		}
	}
	return out, nil
}

// supplierPortalLoc is the portal's clock (UTC+8). The metrics endpoint checks
// end_time against "current time" and rejects >7-day spans in this timezone,
// and created_at strings are in it, so we format/parse all portal times here
// rather than relying on the container's TZ / tzdata availability.
var supplierPortalLoc = time.FixedZone("CST", 8*3600)

// parsePortalCreatedAt converts the portal's "2006-01-02 15:04" local-time
// string to a unix timestamp, falling back to fallback on any parse error.
func parsePortalCreatedAt(s string, fallback int64) int64 {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, supplierPortalLoc); err == nil {
			return t.Unix()
		}
	}
	return fallback
}

func handleSupplierAccountSync(c *gin.Context) {
	if !supplierWebConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "supplier account portal not configured"})
		return
	}
	accounts, err := fetchSupplierPortalAccounts()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	uidAny, _ := c.Get("user_id")
	uid, _ := uidAny.(int64)

	now := time.Now().Unix()
	synced := 0
	for _, a := range accounts {
		if a.ID == 0 || strings.TrimSpace(a.Provider) == "" {
			continue
		}
		sum := sha256.Sum256([]byte(a.APIKey))
		keyHash := hex.EncodeToString(sum[:])
		last8 := a.APIKey
		if len(last8) > 8 {
			last8 = last8[len(last8)-8:]
		}
		created := parsePortalCreatedAt(a.CreatedAt, now)

		// Upsert on remote_account_id. quota_usd / quota_alerted_at are omitted
		// from the column list so a re-sync preserves any configured quota.
		// created_at is preserved on conflict (only updated_at bumps).
		if _, err := db.Exec(
			`INSERT INTO rs_supplier_account
			   (uploaded_by, studio, provider, models, remote_account_id, alias,
			    account_type, remark, key_last8, key_hash, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			 ON CONFLICT (remote_account_id) DO UPDATE SET
			    uploaded_by=EXCLUDED.uploaded_by, studio=EXCLUDED.studio,
			    provider=EXCLUDED.provider, models=EXCLUDED.models,
			    alias=EXCLUDED.alias, account_type=EXCLUDED.account_type,
			    remark=EXCLUDED.remark, key_last8=EXCLUDED.key_last8,
			    key_hash=EXCLUDED.key_hash, updated_at=EXCLUDED.updated_at`,
			uid, a.SupplierName, a.Provider, a.Model, a.ID, a.AccountAlias,
			a.AccountType, a.Remark, last8, keyHash, created, now,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record account: " + err.Error()})
			return
		}
		synced++
	}

	c.JSON(http.StatusOK, gin.H{"synced": synced, "total": len(accounts)})
}

// handleSupplierAccountSetQuota sets a per-account USD cost cap (0 clears it).
// Resets the alert cooldown so a lowered/raised quota re-evaluates cleanly.
func handleSupplierAccountSetQuota(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid account id"})
		return
	}
	var body struct {
		QuotaUSD float64 `json:"quota_usd"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if body.QuotaUSD < 0 || body.QuotaUSD > 1e7 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "quota_usd must be between 0 and 10000000"})
		return
	}
	res, err := db.Exec(
		`UPDATE rs_supplier_account SET quota_usd=$1, quota_alerted_at=0, updated_at=$2 WHERE id=$3`,
		body.QuotaUSD, time.Now().Unix(), id,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "quota_usd": body.QuotaUSD})
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
	UploadedBy      int64   `json:"uploaded_by"`
	Username        string  `json:"username,omitempty"`
	CreatedAt       int64   `json:"created_at"`
	QuotaUSD        float64 `json:"quota_usd"`
}

// maskSupplierAlias hides the supplier/company name embedded in a portal alias
// of the form "<provider>-<supplier name>-<datetime seq>", returning
// "<provider>-***-<seq>". Aliases with fewer than three "-" segments have
// nothing unambiguous to mask and are returned unchanged.
func maskSupplierAlias(alias string) string {
	parts := strings.Split(alias, "-")
	if len(parts) < 3 {
		return alias
	}
	return parts[0] + "-***-" + parts[len(parts)-1]
}

func handleSupplierAccountList(c *gin.Context) {
	isAdmin := callerIsSupplierAdmin(c)

	query := `SELECT a.id, a.remote_account_id, a.provider, a.models, a.alias,
	                 a.account_type, a.remark, a.key_last8, a.studio,
	                 a.uploaded_by, COALESCE(u.username, ''), a.created_at, a.quota_usd
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
			&row.UploadedBy, &row.Username, &row.CreatedAt, &row.QuotaUSD,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// Non-admins never learn who else uploaded — strip owner identity.
		if !isAdmin {
			row.Username = ""
		}
		// Hide the supplier/company name embedded in the portal alias.
		row.Alias = maskSupplierAlias(row.Alias)
		out = append(out, row)
	}
	// fx_rate lets both admins and suppliers convert the portal's RMB cost to
	// USD client-side, consistent with the USD quota.
	c.JSON(http.StatusOK, gin.H{"accounts": out, "fx_rate": supplierFxRate()})
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
// pointer so a missing upstream value serializes as null rather than 0.
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
		r, e := db.Query(`SELECT remote_account_id, alias FROM rs_supplier_account ORDER BY id DESC`)
		idRows, err = r, e
	} else {
		uidAny, _ := c.Get("user_id")
		uid, _ := uidAny.(int64)
		r, e := db.Query(`SELECT remote_account_id, alias FROM rs_supplier_account WHERE uploaded_by=$1 ORDER BY id DESC`, uid)
		idRows, err = r, e
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ids := make([]int64, 0)
	// The portal returns metrics keyed by its own internal aid, which is NOT the
	// account_id we query with (our remote_account_id). alias is the only field
	// shared by both sides, so keep an alias -> remote_account_id map to re-key.
	aliasToRemoteID := make(map[string]int64)
	for idRows.Next() {
		var (
			id    int64
			alias string
		)
		if err := idRows.Scan(&id, &alias); err != nil {
			idRows.Close()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		ids = append(ids, id)
		if alias != "" {
			aliasToRemoteID[alias] = id
		}
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

	// Re-key each row onto the queried remote_account_id (via the shared alias)
	// so the client can join metrics to accounts, then mask the supplier name
	// embedded in the alias. Cost is shown to studios as well as admins.
	for i := range merged {
		if rid, ok := aliasToRemoteID[merged[i].AccountAlias]; ok {
			merged[i].AID = rid
		}
		merged[i].AccountAlias = maskSupplierAlias(merged[i].AccountAlias)
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
	webhook := effectiveQuotaWebhook()
	whLast4 := ""
	if len(webhook) > 4 {
		whLast4 = webhook[len(webhook)-4:]
	}
	c.JSON(http.StatusOK, gin.H{
		"openapi_token_set":   token != "",
		"openapi_token_last4": last4,
		"visible_providers":   vp,
		"provider_defaults":   supplierProviderDefaults(),
		"quota_webhook_set":   webhook != "",
		"quota_webhook_last4": whLast4,
		"fx_rate":             supplierFxRate(),
		"quota_tick_sec":      supplierQuotaTickSeconds(),
		"default_discount":    supplierAccountDiscount(),
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
		QuotaWebhook     *string                             `json:"quota_webhook"`
		FxRate           *float64                            `json:"fx_rate"`
		QuotaTickSec     *int                                `json:"quota_tick_sec"`
		DefaultDiscount  *string                             `json:"default_discount"`
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
	if body.QuotaWebhook != nil {
		if err := supplierConfigSet(cfgSupplierQuotaWebhook, strings.TrimSpace(*body.QuotaWebhook)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.FxRate != nil {
		if *body.FxRate <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "fx_rate must be > 0"})
			return
		}
		if err := supplierConfigSet(cfgSupplierFxRate, strconv.FormatFloat(*body.FxRate, 'f', -1, 64)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.DefaultDiscount != nil {
		d := strings.TrimSpace(*body.DefaultDiscount)
		if d != "" {
			if v, err := strconv.ParseFloat(d, 64); err != nil || v < 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "default_discount must be a non-negative number"})
				return
			}
		}
		if err := supplierConfigSet(cfgSupplierDefaultDiscount, d); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.QuotaTickSec != nil {
		// Clamp to the loop's accepted range before persisting.
		n := *body.QuotaTickSec
		if n < supplierQuotaTickMin {
			n = supplierQuotaTickMin
		}
		if n > supplierQuotaTickMax {
			n = supplierQuotaTickMax
		}
		if err := supplierConfigSet(cfgSupplierQuotaTickSec, strconv.Itoa(n)); err != nil {
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

// ---- Per-account quota alert loop (leader-gated) — OpenAPI token ----

const supplierQuotaLookbackDays = 180

// supplierQuotaTickSeconds reads the alert-loop interval from report_config,
// clamped, defaulting when unset/invalid. Re-read each tick so the interval is
// tunable without a restart.
func supplierQuotaTickSeconds() int {
	n := supplierQuotaTickDef
	if raw := strings.TrimSpace(supplierConfigGet(cfgSupplierQuotaTickSec)); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			n = v
		}
	}
	if n < supplierQuotaTickMin {
		n = supplierQuotaTickMin
	}
	if n > supplierQuotaTickMax {
		n = supplierQuotaTickMax
	}
	return n
}

// startSupplierQuotaAlertLoop periodically compares each account's cumulative
// USD spend to its configured quota and fires a Lark alert to the dedicated
// quota webhook. Leader-gated so only one node alerts across the deployment.
// No-op when the portal isn't configured at all; webhook/token/quota are
// re-checked each tick so live config changes take effect without a restart.
func startSupplierQuotaAlertLoop() {
	if supplierAccountBaseURL == "" {
		return
	}
	go func() {
		// Stagger past the other schedulers' first ticks on a cold start.
		time.Sleep(90 * time.Second)
		for {
			if IsLeader() {
				runSupplierQuotaCheck()
			}
			time.Sleep(time.Duration(supplierQuotaTickSeconds()) * time.Second)
		}
	}()
}

// supplierAnnouncement is one entry of the WEB /supplier/announcements list.
// The portal returns {"list": [...]}; each notice carries an incrementing id
// and its rendered content.
type supplierAnnouncement struct {
	ID        int64  `json:"id"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

// startSupplierAnnouncementLoop pushes newly published supplier announcements to
// the supplier group chat twice a day (10:00 / 22:00 portal time). It reuses the
// account portal's configured Lark webhook (the "额度报警 Webhook" on the 账号上号
// page — supplier_quota_webhook / SUPPLIER_QUOTA_WEBHOOK), so no separate config
// is needed. Only notices newer than the last-pushed id are sent, so the group
// never sees the same announcement twice. Leader-gated; no-op when the portal WEB
// credentials aren't configured.
func startSupplierAnnouncementLoop() {
	if supplierAccountBaseURL == "" || !supplierWebConfigured() {
		return
	}
	go func() {
		for {
			now := time.Now().In(supplierPortalLoc)
			var next time.Time
			for _, h := range []int{10, 22} {
				t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, supplierPortalLoc)
				if t.After(now) {
					next = t
					break
				}
			}
			if next.IsZero() {
				next = time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, supplierPortalLoc).AddDate(0, 0, 1)
			}
			time.Sleep(time.Until(next))
			if IsLeader() {
				runSupplierAnnouncementPush()
			}
		}
	}()
}

// runSupplierAnnouncementPush fetches the supplier announcement list and sends
// any not-yet-pushed notices (id above the stored high-water mark) to the
// supplier portal's Lark webhook, oldest first. On the very first run it adopts
// the current max id silently so the group isn't flooded with the historical
// backlog.
func runSupplierAnnouncementPush() {
	webhook := effectiveQuotaWebhook()
	if webhook == "" {
		return
	}
	tok, err := getSupplierWebToken()
	if err != nil {
		log.Printf("[supplier-announce] web token: %v", err)
		return
	}
	status, body, err := supplierProxy(http.MethodGet, "/supplier-account/api/supplier/announcements", tok, nil)
	if err != nil {
		log.Printf("[supplier-announce] fetch: %v", err)
		return
	}
	if status < 200 || status >= 300 {
		log.Printf("[supplier-announce] fetch status %d: %s", status, supplierErr(body))
		return
	}
	var payload struct {
		List []supplierAnnouncement `json:"list"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Printf("[supplier-announce] decode: %v", err)
		return
	}
	if len(payload.List) == 0 {
		return
	}
	// Push oldest-first so the group reads them in order and the high-water mark
	// advances monotonically.
	sort.Slice(payload.List, func(i, j int) bool { return payload.List[i].ID < payload.List[j].ID })
	maxID := payload.List[len(payload.List)-1].ID

	stored := strings.TrimSpace(supplierConfigGet(cfgSupplierAnnouncePushed))
	if stored == "" {
		// First run: adopt the current max without replaying history.
		if err := supplierConfigSet(cfgSupplierAnnouncePushed, strconv.FormatInt(maxID, 10)); err != nil {
			log.Printf("[supplier-announce] init mark: %v", err)
		}
		return
	}
	lastMax, _ := strconv.ParseInt(stored, 10, 64)

	pushed := lastMax
	for _, a := range payload.List {
		if a.ID <= lastMax {
			continue
		}
		content := strings.TrimSpace(a.Content)
		if content != "" {
			msg := "📢 供应商公告\n" + content
			if a.CreatedAt != "" {
				msg += "\n\n发布时间：" + a.CreatedAt
			}
			sendLarkTo(webhook, msg)
		}
		pushed = a.ID
	}
	if pushed > lastMax {
		if err := supplierConfigSet(cfgSupplierAnnouncePushed, strconv.FormatInt(pushed, 10)); err != nil {
			log.Printf("[supplier-announce] save mark: %v", err)
		}
	}
}

// quotaAccount is one account under quota evaluation.
type quotaAccount struct {
	id, remoteID, alertedAt, createdAt int64
	alias                              string
	quotaUSD                           float64
}

func runSupplierQuotaCheck() {
	webhook := effectiveQuotaWebhook()
	if webhook == "" || !supplierOpenAPIConfigured() {
		return
	}
	rows, err := db.Query(`SELECT id, remote_account_id, alias, quota_usd, quota_alerted_at, created_at
	                        FROM rs_supplier_account WHERE quota_usd > 0`)
	if err != nil {
		log.Printf("[supplier-quota] load accounts: %v", err)
		return
	}
	var (
		accts         []quotaAccount
		ids           = make([]int64, 0)
		aliasToRemote = make(map[string]int64)
		minCreated    = time.Now().Unix()
	)
	for rows.Next() {
		var a quotaAccount
		if err := rows.Scan(&a.id, &a.remoteID, &a.alias, &a.quotaUSD, &a.alertedAt, &a.createdAt); err != nil {
			rows.Close()
			log.Printf("[supplier-quota] scan: %v", err)
			return
		}
		accts = append(accts, a)
		ids = append(ids, a.remoteID)
		if a.alias != "" {
			aliasToRemote[a.alias] = a.remoteID
		}
		if a.createdAt > 0 && a.createdAt < minCreated {
			minCreated = a.createdAt
		}
	}
	rows.Close()
	if len(accts) == 0 {
		return
	}

	costRMB, err := supplierLifetimeCostRMB(ids, aliasToRemote, minCreated)
	if err != nil {
		log.Printf("[supplier-quota] usage query: %v", err)
		return
	}

	fx := supplierFxRate()
	now := time.Now().Unix()
	for _, a := range accts {
		usd := costRMB[a.remoteID] / fx
		if usd < a.quotaUSD {
			continue
		}
		// Re-alert at most once per 24h while still over quota.
		if now-a.alertedAt < 24*3600 {
			continue
		}
		msg := fmt.Sprintf(
			"🚨 账号额度预警\n账号：%s (id=%d)\n累计消耗：$%.2f / 额度 $%.2f\n汇率：%.2f（口径：单账号累计）",
			maskSupplierAlias(a.alias), a.remoteID, usd, a.quotaUSD, fx,
		)
		sendLarkTo(webhook, msg)
		if _, err := db.Exec(`UPDATE rs_supplier_account SET quota_alerted_at=$1 WHERE id=$2`, now, a.id); err != nil {
			log.Printf("[supplier-quota] mark alerted id=%d: %v", a.id, err)
		}
	}
}

// supplierLifetimeCostRMB sums each account's portal cost (RMB) from its
// creation (bounded to a 180-day lookback) to now. The metrics endpoint caps a
// single query at 7 days / 100 ids, so we window in <7-day chunks and batch
// ids, keying results back onto remote_account_id via the shared alias.
func supplierLifetimeCostRMB(ids []int64, aliasToRemote map[string]int64, minCreated int64) (map[int64]float64, error) {
	const layout = "2006-01-02 15:04:05"
	now := time.Now().In(supplierPortalLoc)
	earliest := now.AddDate(0, 0, -supplierQuotaLookbackDays)
	begin := time.Unix(minCreated, 0).In(supplierPortalLoc)
	if begin.Before(earliest) {
		begin = earliest
	}
	// Just under 7 days so the portal's ">7天" guard never trips.
	step := 7*24*time.Hour - time.Minute

	out := make(map[int64]float64, len(ids))
	for chunkStart := begin; chunkStart.Before(now); chunkStart = chunkStart.Add(step) {
		chunkEnd := chunkStart.Add(step)
		if chunkEnd.After(now) {
			chunkEnd = now
		}
		for start := 0; start < len(ids); start += 100 {
			end := start + 100
			if end > len(ids) {
				end = len(ids)
			}
			reqBytes, _ := json.Marshal(map[string]any{
				"account_ids": ids[start:end],
				"begin_time":  chunkStart.Format(layout),
				"end_time":    chunkEnd.Format(layout),
				"aggregate":   false,
			})
			status, body, err := supplierProxy(http.MethodPost, "/supplier-account/api/openapi/metrics/query", effectiveOpenAPIToken(), reqBytes)
			if err != nil {
				return nil, err
			}
			if status < 200 || status >= 300 {
				return nil, errors.New(supplierErr(body))
			}
			var parsed struct {
				Accounts []supplierMetric `json:"accounts"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return nil, err
			}
			for _, m := range parsed.Accounts {
				if m.Cost == nil {
					continue
				}
				rid, ok := aliasToRemote[m.AccountAlias]
				if !ok {
					rid = m.AID
				}
				out[rid] += *m.Cost
			}
		}
	}
	return out, nil
}
