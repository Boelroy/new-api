package main

// 供给台 (GYNH) supply-console integration.
//
// GYNH is an external "API 账号管理" portal (Flask behind waitress) that only
// offers its browser API to us: POST /api/login {username,password} sets an
// HttpOnly `portal_session` cookie and returns a CSRF token that every later
// call must echo in X-CSRF-Token. Sessions last 8h (cookie Max-Age).
//
// The same browser API backs both 上号 (POST /api/accounts) and 用量 (the
// portal's Key 花费 view, /api/key-spend).
//
// report-service acts as a scoped proxy for supplier_gynh users (and admins):
// it holds ONE shared portal supplier login server-side and exposes
// /api/gynh/*. The portal sees every upload as that one supplier, so per-user
// ownership is tracked locally in rs_gynh_account and enforced here.
//
// Mirrors the shape of keyhub.go (config in report_config, mutex-guarded
// session cache, re-login once on 401).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// report_config keys for the admin-editable GYNH login.
const (
	cfgGynhBaseURL  = "gynh_base_url"
	cfgGynhUsername = "gynh_username"
	cfgGynhPassword = "gynh_password"
)

func effectiveGynhBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(supplierConfigGet(cfgGynhBaseURL)), "/")
}

func gynhConfigured() bool {
	return effectiveGynhBaseURL() != "" &&
		strings.TrimSpace(supplierConfigGet(cfgGynhUsername)) != "" &&
		strings.TrimSpace(supplierConfigGet(cfgGynhPassword)) != ""
}

var gynhHTTPClient = &http.Client{Timeout: 60 * time.Second}

// ---- session cache ----

var (
	gynhMu      sync.Mutex
	gynhSession string // portal_session cookie value
	gynhCSRF    string
	gynhExpiry  time.Time
)

// gynhLogin runs the shared login and returns (session cookie, csrf, expiry).
func gynhLogin() (string, string, time.Time, error) {
	if !gynhConfigured() {
		return "", "", time.Time{}, errors.New("gynh base url / username / password not configured")
	}
	body, _ := json.Marshal(map[string]string{
		"username": strings.TrimSpace(supplierConfigGet(cfgGynhUsername)),
		"password": supplierConfigGet(cfgGynhPassword),
	})
	req, err := http.NewRequest(http.MethodPost, effectiveGynhBaseURL()+"/api/login", bytes.NewReader(body))
	if err != nil {
		return "", "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", effectiveGynhBaseURL())
	resp, err := gynhHTTPClient.Do(req)
	if err != nil {
		return "", "", time.Time{}, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", time.Time{}, fmt.Errorf("login: %s", gynhErr(respBody))
	}
	var lg struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(respBody, &lg); err != nil || lg.CSRF == "" {
		return "", "", time.Time{}, errors.New("login: unexpected response")
	}
	for _, ck := range resp.Cookies() {
		if ck.Name != "portal_session" || ck.Value == "" {
			continue
		}
		// Refresh a few minutes before the portal drops the session.
		exp := time.Now().Add(8 * time.Hour)
		if ck.MaxAge > 0 {
			exp = time.Now().Add(time.Duration(ck.MaxAge) * time.Second)
		}
		return ck.Value, lg.CSRF, exp.Add(-5 * time.Minute), nil
	}
	return "", "", time.Time{}, errors.New("login: no session cookie")
}

func getGynhSession() (string, string, error) {
	gynhMu.Lock()
	defer gynhMu.Unlock()
	if gynhSession != "" && time.Now().Before(gynhExpiry) {
		return gynhSession, gynhCSRF, nil
	}
	sess, csrf, exp, err := gynhLogin()
	if err != nil {
		return "", "", err
	}
	gynhSession, gynhCSRF, gynhExpiry = sess, csrf, exp
	return sess, csrf, nil
}

func forceGynhRelogin() {
	gynhMu.Lock()
	gynhSession, gynhCSRF, gynhExpiry = "", "", time.Time{}
	gynhMu.Unlock()
}

func gynhRawRequest(method, path, session, csrf string, jsonBody []byte) (int, []byte, error) {
	var reader io.Reader
	if jsonBody != nil {
		reader = bytes.NewReader(jsonBody)
	}
	req, err := http.NewRequest(method, effectiveGynhBaseURL()+"/api"+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.AddCookie(&http.Cookie{Name: "portal_session", Value: session})
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", effectiveGynhBaseURL())
	if jsonBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := gynhHTTPClient.Do(req)
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

// gynhProxy issues an authenticated portal call, re-logging in once on a 401
// (expired session, or the portal revoked it after a password change).
func gynhProxy(method, path string, jsonBody []byte) (int, []byte, error) {
	sess, csrf, err := getGynhSession()
	if err != nil {
		return 0, nil, err
	}
	st, body, err := gynhRawRequest(method, path, sess, csrf, jsonBody)
	if err != nil || st != http.StatusUnauthorized {
		return st, body, err
	}
	forceGynhRelogin()
	if sess, csrf, err = getGynhSession(); err != nil {
		return 0, nil, err
	}
	return gynhRawRequest(method, path, sess, csrf, jsonBody)
}

// gynhErr extracts the portal's {"detail": "..."} message, falling back to the
// raw body.
func gynhErr(body []byte) string {
	var e struct {
		Detail any `json:"detail"`
	}
	if json.Unmarshal(body, &e) == nil {
		if s, ok := e.Detail.(string); ok && s != "" {
			return s
		}
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "upstream error"
	}
	return s
}

// ---- handlers ----

// handleGynhOptions returns the provider/model catalog the upload form needs,
// taken from the portal's /me (which also carries the CSRF token and the
// portal user, both kept server-side).
func handleGynhOptions(c *gin.Context) {
	if !gynhConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "gynh not configured"})
		return
	}
	st, body, err := gynhProxy(http.MethodGet, "/me", nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if st < 200 || st >= 300 {
		c.JSON(http.StatusBadGateway, gin.H{"error": gynhErr(body)})
		return
	}
	var me struct {
		Catalog        map[string][]string `json:"catalog"`
		Providers      json.RawMessage     `json:"providers"`
		AccountRegions json.RawMessage     `json:"account_regions"`
		Mode           string              `json:"mode"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "unexpected portal response"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"catalog":         me.Catalog,
		"providers":       me.Providers,
		"account_regions": me.AccountRegions,
		"mode":            me.Mode,
		"default_tpm":     supplierAccountDefaultTPM(),
		"default_rpm":     supplierAccountDefaultRPM(),
	})
}

// handleGynhAccountCreate uploads ONE key. The page loops over the pasted keys
// itself so a long batch never sits in a single request behind the proxy
// timeout, and each key gets its own result line.
func handleGynhAccountCreate(c *gin.Context) {
	if !gynhConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "gynh not configured"})
		return
	}
	uid := callerUserID(c)
	if uid <= 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "gynh: cannot resolve uploader identity"})
		return
	}
	var body struct {
		Provider    string   `json:"provider"`
		AccountType int      `json:"account_type"`
		Models      []string `json:"models"`
		APIKey      string   `json:"api_key"`
		TPM         string   `json:"tpm"`
		RPM         string   `json:"rpm"`
		Remark      string   `json:"remark"`
		Region      string   `json:"region"`
		GroupID     string   `json:"group_id"`
		URL         string   `json:"url"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	body.Provider = strings.TrimSpace(body.Provider)
	body.APIKey = strings.TrimSpace(body.APIKey)
	var models []string
	for _, m := range body.Models {
		if m = strings.TrimSpace(m); m != "" {
			models = append(models, m)
		}
	}
	if body.Provider == "" || body.APIKey == "" || len(models) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider, models and api_key are required"})
		return
	}
	// Same payload the portal's own 提交账号 form posts; owner_id is not sent
	// because the shared login is a portal supplier, not its admin.
	payload := map[string]any{
		"provider":     body.Provider,
		"account_type": body.AccountType,
		"model":        strings.Join(models, ","),
		"api_key":      body.APIKey,
		"tpm":          strings.TrimSpace(body.TPM),
		"rpm":          strings.TrimSpace(body.RPM),
		"remark":       strings.TrimSpace(body.Remark),
	}
	for k, v := range map[string]string{"region": body.Region, "group_id": body.GroupID, "url": body.URL} {
		if v = strings.TrimSpace(v); v != "" {
			payload[k] = v
		}
	}
	reqBytes, _ := json.Marshal(payload)
	st, respBody, err := gynhProxy(http.MethodPost, "/accounts", reqBytes)
	if err != nil {
		log.Printf("[gynh] create %s by user %d: %v", body.Provider, uid, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var res struct {
		ID      int64  `json:"id"`
		Msg     string `json:"msg"`
		LocalID int64  `json:"local_id"`
	}
	_ = json.Unmarshal(respBody, &res)

	keyHint := body.APIKey
	if len(keyHint) > 16 {
		keyHint = keyHint[:16]
	}
	ok := st >= 200 && st < 300
	// A failed or pending submit still leaves a portal record when local_id
	// comes back; keep it so the uploader can see the outcome in the list.
	if ok || res.LocalID > 0 {
		remoteID := int64(0)
		if ok {
			remoteID = res.ID
		}
		if _, err := db.Exec(
			`INSERT INTO rs_gynh_account (uploaded_by, provider, models, remote_id, portal_local_id, key_hint, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			uid, body.Provider, strings.Join(models, ","), remoteID, res.LocalID, keyHint, time.Now().Unix(),
		); err != nil {
			log.Printf("[gynh] record upload by user %d (remote %d, local %d): %v", uid, remoteID, res.LocalID, err)
		}
	}
	if !ok {
		msg := gynhErr(respBody)
		log.Printf("[gynh] create %s by user %d: portal %d: %s", body.Provider, uid, st, msg)
		// 422 rather than relaying a 5xx, which Cloudflare would replace with
		// its own HTML page and lose the portal's reason.
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": msg, "local_id": res.LocalID})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": res.ID, "msg": res.Msg})
}

// gynhOwners maps portal rows to the report-service user who uploaded them.
// The portal's own owner_id/owner_name always name the shared login, so this
// local record is the only source of per-user ownership.
type gynhOwners struct {
	isAdmin  bool
	byRemote map[int64]string
	byLocal  map[int64]string
	byHint   map[string]string
}

// loadGynhOwners loads the caller's upload records: every record for admins,
// only their own for a supplier_gynh user. ok=false means the response was
// already written.
func loadGynhOwners(c *gin.Context) (gynhOwners, bool) {
	roleAny, _ := c.Get("role")
	role, _ := roleAny.(int)
	o := gynhOwners{
		isAdmin:  role >= minAdminRole,
		byRemote: map[int64]string{},
		byLocal:  map[int64]string{},
		byHint:   map[string]string{},
	}
	uid := callerUserID(c)
	if !o.isAdmin && uid <= 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "gynh: cannot resolve uploader identity"})
		return o, false
	}
	query := `SELECT a.remote_id, a.portal_local_id, a.key_hint, COALESCE(u.username, '')
	            FROM rs_gynh_account a LEFT JOIN rs_auth_user u ON u.id = a.uploaded_by`
	var args []any
	if !o.isAdmin {
		query += ` WHERE a.uploaded_by = $1`
		args = append(args, uid)
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return o, false
	}
	defer rows.Close()
	for rows.Next() {
		var remoteID, localID int64
		var hint, owner string
		if err := rows.Scan(&remoteID, &localID, &hint, &owner); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return o, false
		}
		switch {
		case remoteID > 0:
			o.byRemote[remoteID] = owner
		case localID > 0:
			o.byLocal[localID] = owner
		case hint != "":
			o.byHint[hint] = owner
		}
	}
	return o, true
}

// match reports whether the caller may see a portal row (keyed by the portal
// record id, upstream account id and key prefix) and who uploaded it.
func (o gynhOwners) match(row map[string]any) (owner string, visible bool) {
	if owner, ok := o.byRemote[gynhID(row["upstream_id"])]; ok {
		return owner, true
	}
	if owner, ok := o.byLocal[gynhID(row["id"])]; ok {
		return owner, true
	}
	if prefix, _ := row["key_prefix"].(string); prefix != "" {
		if owner, ok := o.byHint[prefix]; ok {
			return owner, true
		}
	}
	return "", o.isAdmin
}

func gynhID(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		id, _ := strconv.ParseInt(n, 10, 64)
		return id
	}
	return 0
}

// gynhGetJSON GETs a portal endpoint and decodes it, writing a 502 on failure.
func gynhGetJSON(c *gin.Context, path string, out any) bool {
	st, body, err := gynhProxy(http.MethodGet, path, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return false
	}
	if st < 200 || st >= 300 {
		c.JSON(http.StatusBadGateway, gin.H{"error": gynhErr(body)})
		return false
	}
	if err := json.Unmarshal(body, out); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "unexpected portal response"})
		return false
	}
	return true
}

// gynhAccountFields are the portal account fields relayed to the page.
var gynhAccountFields = []string{
	"id", "alias", "upstream_id", "provider", "account_type", "model", "remark",
	"key_prefix", "state", "upstream_status", "last_error", "created_at",
}

// handleGynhAccountList returns the portal's accounts scoped to the caller:
// a supplier_gynh user sees only rows they uploaded; admins see every row
// with its uploader (blank for rows submitted on the portal directly).
func handleGynhAccountList(c *gin.Context) {
	if !gynhConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "gynh not configured"})
		return
	}
	owners, ok := loadGynhOwners(c)
	if !ok {
		return
	}
	var portal struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if !gynhGetJSON(c, "/accounts", &portal) {
		return
	}
	out := make([]map[string]any, 0, len(portal.Accounts))
	for _, acc := range portal.Accounts {
		owner, visible := owners.match(acc)
		if !visible {
			continue
		}
		row := make(map[string]any, len(gynhAccountFields)+1)
		for _, k := range gynhAccountFields {
			if v, ok := acc[k]; ok {
				row[k] = v
			}
		}
		if owners.isAdmin {
			row["uploader"] = owner
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"accounts": out})
}

// gynhUsageFields are the portal /key-spend row fields relayed to the page:
// cumulative spend since upload (cny = original RMB, usd at the portal's
// rate), per log platform, and the day-by-day backfill progress.
var gynhUsageFields = []string{
	"id", "alias", "upstream_id", "provider", "created_at", "status",
	"platforms", "cny", "usd", "complete", "days_done", "days_expected", "error",
}

// handleGynhUsage returns each visible key's cumulative spend (portal
// /key-spend) with its last-60s RPM/TPM (portal /key-spend/realtime), plus
// totals over the visible rows only — the portal's own totals cover every
// key of the shared login.
func handleGynhUsage(c *gin.Context) {
	if !gynhConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "gynh not configured"})
		return
	}
	owners, ok := loadGynhOwners(c)
	if !ok {
		return
	}
	var spend struct {
		Accounts    []map[string]any `json:"accounts"`
		Rate        string           `json:"rate"`
		GeneratedAt int64            `json:"generated_at"`
	}
	if !gynhGetJSON(c, "/key-spend", &spend) {
		return
	}
	// Realtime rates are best-effort; the page still shows spend without them.
	var realtime struct {
		At   int64 `json:"at"`
		Keys []struct {
			ID  int64    `json:"id"`
			RPM *float64 `json:"rpm"`
			TPM *float64 `json:"tpm"`
		} `json:"keys"`
	}
	rates := map[int64][2]*float64{}
	if st, body, err := gynhProxy(http.MethodGet, "/key-spend/realtime", nil); err == nil && st == http.StatusOK && json.Unmarshal(body, &realtime) == nil {
		for _, k := range realtime.Keys {
			rates[k.ID] = [2]*float64{k.RPM, k.TPM}
		}
	}

	out := make([]map[string]any, 0, len(spend.Accounts))
	var cnyCents, usdCents int64
	for _, acc := range spend.Accounts {
		owner, visible := owners.match(acc)
		if !visible {
			continue
		}
		row := make(map[string]any, len(gynhUsageFields)+3)
		for _, k := range gynhUsageFields {
			if v, ok := acc[k]; ok {
				row[k] = v
			}
		}
		if r, ok := rates[gynhID(acc["id"])]; ok {
			row["rpm"], row["tpm"] = r[0], r[1]
		}
		if owners.isAdmin {
			row["uploader"] = owner
		}
		// Sum in cents, like the portal page, so float noise stays out of totals.
		cny, _ := strconv.ParseFloat(fmt.Sprint(acc["cny"]), 64)
		usd, _ := strconv.ParseFloat(fmt.Sprint(acc["usd"]), 64)
		cnyCents += int64(math.Round(cny * 100))
		usdCents += int64(math.Round(usd * 100))
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{
		"accounts":     out,
		"rate":         spend.Rate,
		"generated_at": spend.GeneratedAt,
		"realtime_at":  realtime.At,
		"totals": gin.H{
			"cny":  fmt.Sprintf("%.2f", float64(cnyCents)/100),
			"usd":  fmt.Sprintf("%.2f", float64(usdCents)/100),
			"keys": len(out),
		},
	})
}

// gynhTierProbeMax bounds one pre-upload tier check; each key costs a
// one-token OpenAI call.
const gynhTierProbeMax = 200

// handleGynhTierProbe estimates the OpenAI usage tier of keys about to be
// uploaded (same one-token probe as Key Capacity), so the page can warn
// before low-tier keys go in. Results come back by index; keys are never
// echoed or stored.
func handleGynhTierProbe(c *gin.Context) {
	var body struct {
		Keys []string `json:"keys"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.Keys) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "keys are required"})
		return
	}
	if len(body.Keys) > gynhTierProbeMax {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("at most %d keys per check", gynhTierProbeMax)})
		return
	}
	type tierResult struct {
		Tier       *int   `json:"tier"`
		HTTPStatus int    `json:"http_status"`
		Error      string `json:"error,omitempty"`
	}
	results := make([]tierResult, len(body.Keys))
	sem := make(chan struct{}, keyTierProbeConcurrency)
	var wg sync.WaitGroup
	for i, key := range body.Keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := probeOpenAIKeyTier(c.Request.Context(), key, "", "")
			results[i] = tierResult{Tier: r.Tier, HTTPStatus: r.HTTPStatus, Error: r.Error}
		}()
	}
	wg.Wait()
	c.JSON(http.StatusOK, gin.H{"results": results})
}

// ---- admin settings ----

func handleGynhSettingsGet(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"base_url":     effectiveGynhBaseURL(),
		"username":     strings.TrimSpace(supplierConfigGet(cfgGynhUsername)),
		"password_set": strings.TrimSpace(supplierConfigGet(cfgGynhPassword)) != "",
		"configured":   gynhConfigured(),
	})
}

func handleGynhSettingsSet(c *gin.Context) {
	var body struct {
		// Pointers so an omitted field is left unchanged.
		BaseURL  *string `json:"base_url"`
		Username *string `json:"username"`
		Password *string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	updates := map[string]*string{cfgGynhBaseURL: body.BaseURL, cfgGynhUsername: body.Username}
	if body.Password != nil && *body.Password != "" {
		updates[cfgGynhPassword] = body.Password
	}
	for key, val := range updates {
		if val == nil {
			continue
		}
		v := *val
		if key != cfgGynhPassword {
			v = strings.TrimSpace(v)
		}
		if err := supplierConfigSet(key, v); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	forceGynhRelogin()
	handleGynhSettingsGet(c)
}
