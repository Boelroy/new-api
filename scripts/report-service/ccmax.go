package main

// CC Max (Claude subscription) account onboarding.
//
// Server-side port of claude_oauth.py: a supplier enters their own socks5
// proxy, we hand back a Claude Code OAuth2 + PKCE authorize URL, they click
// Authorize in any browser already logged in to claude.ai and paste the code
// back. Every Anthropic call (token exchange, profile, roles) and the exit-IP
// timezone probe goes through the supplier's socks5, so the account is only
// ever seen from that proxy's exit IP.
//
// The resulting credential JSON (same 13-field shape the script writes) is
// uploaded as auth_json to the 账号资源录入 portal's claude lane
// (/openapi/claude/accounts). The portal is the same one 账号上号 uses but with
// a separate portal user + OpenAPI token (SUPPLIER_CCMAX_*); the account list
// and usage come from the portal too. rs_ccmax_account only records which
// supplier uploaded which portal account, for scoping.

import (
	"bytes"
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

// Upstream (portal / Anthropic / proxy) failures answer 424, not 502: the
// site sits behind Cloudflare, which swaps origin 502 bodies for its own HTML
// error page and would hide the portal's rejection reason from the supplier.

// OAuth endpoints / client parameters of the Claude Code client.
const (
	ccmaxAuthURL     = "https://claude.ai/oauth/authorize"
	ccmaxTokenURL    = "https://platform.claude.com/v1/oauth/token"
	ccmaxProfileURL  = "https://api.anthropic.com/api/oauth/profile"
	ccmaxRolesURL    = "https://api.anthropic.com/api/oauth/claude_cli/roles"
	ccmaxClientID    = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	ccmaxRedirectURI = "http://localhost:54545/callback"
	ccmaxScope       = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	// ccmaxSessionTTL bounds how long a handed-out authorize URL stays usable.
	ccmaxSessionTTL = 30 * time.Minute
)

// requireRoleOrSupplierCCMax grants access to callers at min tier OR the
// supplier_ccmax role (role=8), a horizontal specialization that only does CC
// Max 上号. The CC Max handlers enforce the per-uploader scoping internally.
func requireRoleOrSupplierCCMax(min int) gin.HandlerFunc {
	return func(c *gin.Context) {
		roleAny, _ := c.Get("role")
		role, _ := roleAny.(int)
		if role >= min || role == minSupplierCCMaxRole {
			c.Next()
			return
		}
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		c.Abort()
	}
}

// requireRoleOrCCMaxRoles is requireRoleOrSupplierCCMax that also lets the
// ccmax_manager role (role=9) in, for the read-only CC Max surfaces.
func requireRoleOrCCMaxRoles(min int) gin.HandlerFunc {
	return func(c *gin.Context) {
		roleAny, _ := c.Get("role")
		role, _ := roleAny.(int)
		if role >= min || role == minSupplierCCMaxRole || role == minCCMaxManagerRole {
			c.Next()
			return
		}
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		c.Abort()
	}
}

// ccmaxProxyClient builds a one-off client routed through the supplier's
// proxy. Not cached: every supplier brings their own proxy and the flow makes
// only a handful of calls.
func ccmaxProxyClient(proxyURL string) (*http.Client, error) {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true},
	}, nil
}

// ccmaxDo sends a request with the Claude Code axios header set.
func ccmaxDo(client *http.Client, method, target, bearer string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, target, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "axios/1.15.2")
	req.Header.Set("Accept-Encoding", "identity")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Cache-Control", "no-cache")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, err
}

// ccmaxProxyExit is the proxy's exit as seen by ip-api.com.
type ccmaxProxyExit struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Query    string `json:"query"`
	Country  string `json:"country"`
	Timezone string `json:"timezone"`
}

// ccmaxProbeProxy looks up the proxy's exit IP + timezone through the proxy
// itself. Doubles as the connectivity check for the supplier's socks5.
func ccmaxProbeProxy(client *http.Client) (ccmaxProxyExit, error) {
	var exit ccmaxProxyExit
	status, raw, err := ccmaxDo(client, http.MethodGet, "http://ip-api.com/json/?fields=status,message,query,country,timezone", "", nil)
	if err != nil {
		return exit, err
	}
	if status != http.StatusOK {
		return exit, fmt.Errorf("ip-api HTTP %d", status)
	}
	if err := json.Unmarshal(raw, &exit); err != nil {
		return exit, err
	}
	if exit.Status != "success" {
		return exit, fmt.Errorf("ip-api: %s", exit.Message)
	}
	return exit, nil
}

// ccmaxParseCode accepts what the supplier pastes back: a bare code, the
// "code#state" form, a query/fragment string ("code=...&state=..."), or the
// full callback URL (code in query or fragment).
func ccmaxParseCode(raw string) (code, state string) {
	raw = strings.TrimSpace(raw)
	var values url.Values
	switch {
	case strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://"):
		if u, err := url.Parse(raw); err == nil {
			values = u.Query()
			if values.Get("code") == "" {
				values, _ = url.ParseQuery(u.Fragment)
			}
		}
	case strings.Contains(raw, "code="):
		values, _ = url.ParseQuery(strings.TrimLeft(raw, "?#"))
	default:
		code, state, _ = strings.Cut(raw, "#")
		return code, state
	}
	code, state = values.Get("code"), values.Get("state")
	if c, s, ok := strings.Cut(code, "#"); ok {
		code = c
		if state == "" {
			state = s
		}
	}
	return code, state
}

// ccmaxProxyInput is the supplier's socks5: either a pasted URL
// (socks5://user:pass@host:port, scheme optional) or the separate fields. URL
// wins when set.
type ccmaxProxyInput struct {
	URL      string `json:"url"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// proxyURL validates the input and returns the canonical socks5/socks5h URL
// used for every outbound call and written into the credential's proxy_url.
func (in ccmaxProxyInput) proxyURL() (string, error) {
	proxy := url.URL{Scheme: "socks5"}
	host, portStr := strings.TrimSpace(in.Host), strings.TrimSpace(in.Port)
	if raw := strings.TrimSpace(in.URL); raw != "" {
		if !strings.Contains(raw, "://") {
			raw = "socks5://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil {
			return "", errors.New("代理 URL 格式不正确")
		}
		if u.Scheme != "socks5" && u.Scheme != "socks5h" {
			return "", errors.New("代理 URL 须以 socks5:// 或 socks5h:// 开头")
		}
		proxy.Scheme, proxy.User = u.Scheme, u.User
		host, portStr = u.Hostname(), u.Port()
	} else if user := strings.TrimSpace(in.Username); user != "" {
		proxy.User = url.UserPassword(user, in.Password)
	}
	port, err := strconv.Atoi(portStr)
	if host == "" || err != nil || port < 1 || port > 65535 {
		return "", errors.New("请填写正确的 socks5 地址和端口")
	}
	proxy.Host = net.JoinHostPort(host, strconv.Itoa(port))
	return proxy.String(), nil
}

func handleCCMaxOAuthStart(c *gin.Context) {
	var body ccmaxProxyInput
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	proxyURL, err := body.proxyURL()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	client, err := ccmaxProxyClient(proxyURL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "代理地址无效: " + err.Error()})
		return
	}
	exit, err := ccmaxProbeProxy(client)
	if err != nil {
		c.JSON(http.StatusFailedDependency, gin.H{"error": "socks5 代理不通: " + err.Error()})
		return
	}

	// 96 random bytes -> 128-char verifier, S256 challenge; 16-byte hex state.
	verifierBuf := make([]byte, 96)
	_, _ = rand.Read(verifierBuf)
	verifier := base64.RawURLEncoding.EncodeToString(verifierBuf)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	stateBuf := make([]byte, 16)
	_, _ = rand.Read(stateBuf)
	state := hex.EncodeToString(stateBuf)

	proxyEnc, err := encryptRemoteToken(proxyURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "encrypt proxy: " + err.Error()})
		return
	}
	uidAny, _ := c.Get("user_id")
	uid, _ := uidAny.(int64)
	now := time.Now()
	// Opportunistic GC of abandoned logins; no background loop needed.
	_, _ = db.Exec(`DELETE FROM rs_ccmax_oauth_session WHERE created_at < $1`, now.Add(-ccmaxSessionTTL).Unix())
	if _, err := db.Exec(
		`INSERT INTO rs_ccmax_oauth_session (state, user_id, code_verifier, proxy_enc, proxy_tz, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		state, uid, verifier, proxyEnc, exit.Timezone, now.Unix(),
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save session: " + err.Error()})
		return
	}

	// url.Values.Encode sorts keys, matching the reference client byte-for-byte.
	q := url.Values{
		"client_id":             {ccmaxClientID},
		"code":                  {"true"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {ccmaxRedirectURI},
		"response_type":         {"code"},
		"scope":                 {ccmaxScope},
		"state":                 {state},
	}
	c.JSON(http.StatusOK, gin.H{
		"auth_url": ccmaxAuthURL + "?" + q.Encode(),
		"state":    state,
		"exit_ip":  exit.Query,
		"country":  exit.Country,
		"timezone": exit.Timezone,
	})
}

// ccmaxCredential is the stored credential JSON. Field order matches the
// claude-<email>.json files claude_oauth.py writes.
type ccmaxCredential struct {
	IDToken            string   `json:"id_token"`
	AccessToken        string   `json:"access_token"`
	RefreshToken       string   `json:"refresh_token"`
	LastRefresh        string   `json:"last_refresh"`
	Email              string   `json:"email"`
	AccountUUID        string   `json:"account_uuid,omitempty"`
	OrganizationUUID   string   `json:"organization_uuid,omitempty"`
	OrganizationName   string   `json:"organization_name,omitempty"`
	ClaudeDeviceIDs    []string `json:"claude_device_ids"`
	Type               string   `json:"type"`
	Expired            string   `json:"expired"`
	ProxyURL           string   `json:"proxy_url"`
	VerificationStatus string   `json:"verification_status"`
}

// ccmaxSubmitForm is the portal's required claude 上号 fields besides the
// credential (POST /openapi/claude/accounts). Optional portal fields are not
// offered.
type ccmaxSubmitForm struct {
	Plan             string `json:"plan"`
	Account          string `json:"account"`
	Password         string `json:"password"`
	RegisterLocation string `json:"register_location"`
	RegisterTime     string `json:"register_time"`
	PlanStartTime    string `json:"plan_start_time"`
	PlanEndTime      string `json:"plan_end_time"`
}

// validate trims the form and checks it the way the portal does, so a bad form
// is rejected before the single-use authorization code is spent. Re-authorizing
// an existing account may leave the password blank (unchanged).
func (f *ccmaxSubmitForm) validate(requirePassword bool) error {
	f.Plan = strings.TrimSpace(f.Plan)
	f.Account = strings.TrimSpace(f.Account)
	f.RegisterLocation = strings.TrimSpace(f.RegisterLocation)
	if f.Plan == "" || f.Account == "" || f.RegisterLocation == "" {
		return errors.New("套餐、账号、注册地均为必填")
	}
	if requirePassword && f.Password == "" {
		return errors.New("密码为必填")
	}
	if strings.ContainsFunc(f.Account, unicode.IsSpace) {
		return errors.New("账号不能包含空格")
	}
	for _, d := range []*string{&f.RegisterTime, &f.PlanStartTime, &f.PlanEndTime} {
		*d = strings.TrimSpace(*d)
		if _, err := time.Parse("2006-01-02", *d); err != nil {
			return errors.New("注册时间 / 套餐开始时间 / 套餐结束时间须为 YYYY-MM-DD")
		}
	}
	return nil
}

// handleCCMaxSubmit exchanges the pasted authorization code for tokens (through
// the supplier's socks5) and uploads the account to the portal's claude lane.
// The credential is parked on the session row between the two steps: codes are
// single-use, so if the portal rejects the form the supplier can fix it and
// resubmit without authorizing again.
// check applies the portal's auth_json rules up front so a bad credential
// fails with a clear message: every field non-empty except id_token,
// RFC3339 last_refresh / expired, http(s) or socks5(h) proxy_url.
func (cr ccmaxCredential) check() error {
	missing := []string{}
	for name, v := range map[string]string{
		"access_token": cr.AccessToken, "refresh_token": cr.RefreshToken,
		"last_refresh": cr.LastRefresh, "email": cr.Email,
		"account_uuid": cr.AccountUUID, "organization_uuid": cr.OrganizationUUID,
		"organization_name": cr.OrganizationName, "type": cr.Type,
		"expired": cr.Expired, "proxy_url": cr.ProxyURL,
		"verification_status": cr.VerificationStatus,
	} {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, name)
		}
	}
	if len(cr.ClaudeDeviceIDs) == 0 {
		missing = append(missing, "claude_device_ids")
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("授权结果缺少字段：%s", strings.Join(missing, "、"))
	}
	for _, ts := range []string{cr.LastRefresh, cr.Expired} {
		if _, err := time.Parse(time.RFC3339, ts); err != nil {
			return fmt.Errorf("时间格式不是 RFC3339：%s", ts)
		}
	}
	scheme, _, _ := strings.Cut(cr.ProxyURL, "://")
	if !slices.Contains([]string{"http", "https", "socks5", "socks5h"}, scheme) {
		return errors.New("proxy_url 须以 http://、https://、socks5:// 或 socks5h:// 开头")
	}
	return nil
}

// matchesAccount requires the form's 账号 to be the Claude login the
// credential was issued for, so an account can't be uploaded under one email
// while serving another (e.g. an Apple private-relay login).
func (cr ccmaxCredential) matchesAccount(account string) error {
	if strings.EqualFold(strings.TrimSpace(account), strings.TrimSpace(cr.Email)) {
		return nil
	}
	return fmt.Errorf("账号与授权邮箱不一致：授权的 Claude 账号是 %s，请把账号改成该邮箱，或用 %s 重新授权", cr.Email, account)
}

func handleCCMaxSubmit(c *gin.Context) {
	if !ccmaxOpenAPIConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "CC Max OpenAPI token not configured (set SUPPLIER_CCMAX_TOKEN)"})
		return
	}
	var body struct {
		State string `json:"state"`
		Code  string `json:"code"`
		// AccountID (portal account id) set ⇒ re-authorize that existing
		// account instead of uploading a new one.
		AccountID int64 `json:"account_id"`
		ccmaxSubmitForm
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	reauth := body.AccountID > 0
	if err := body.validate(!reauth); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if reauth && !ccmaxCallerOwns(c, body.AccountID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "账号不存在"})
		return
	}
	uidAny, _ := c.Get("user_id")
	uid, _ := uidAny.(int64)
	studioAny, _ := c.Get("studio")
	studio, _ := studioAny.(string)
	state := strings.TrimSpace(body.State)

	var verifier, proxyEnc, proxyTZ, credEnc string
	var createdAt int64
	err := db.QueryRow(
		`SELECT code_verifier, proxy_enc, proxy_tz, credential_enc, created_at
		   FROM rs_ccmax_oauth_session WHERE state=$1 AND user_id=$2`,
		state, uid,
	).Scan(&verifier, &proxyEnc, &proxyTZ, &credEnc, &createdAt)
	if err != nil || time.Since(time.Unix(createdAt, 0)) > ccmaxSessionTTL {
		c.JSON(http.StatusBadRequest, gin.H{"error": "授权会话不存在或已过期，请重新生成授权链接", "session_gone": true})
		return
	}

	var cred ccmaxCredential
	if credEnc != "" {
		plain, err := decryptRemoteToken(credEnc)
		if err == nil {
			err = json.Unmarshal([]byte(plain), &cred)
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "load credential: " + err.Error()})
			return
		}
	} else {
		proxyURL, err := decryptRemoteToken(proxyEnc)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "decrypt proxy: " + err.Error()})
			return
		}
		cred, err = ccmaxExchangeCode(proxyURL, proxyTZ, verifier, state, body.Code)
		if err != nil {
			// The code is spent (or was never valid); this session is dead.
			_, _ = db.Exec(`DELETE FROM rs_ccmax_oauth_session WHERE state=$1`, state)
			c.JSON(http.StatusFailedDependency, gin.H{"error": err.Error(), "session_gone": true})
			return
		}
		raw, _ := json.Marshal(cred)
		enc, err := encryptRemoteToken(string(raw))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "encrypt credential: " + err.Error()})
			return
		}
		_, _ = db.Exec(`UPDATE rs_ccmax_oauth_session SET credential_enc=$1 WHERE state=$2`, enc, state)
	}

	if err := cred.check(); err != nil {
		// Re-submitting can't fix a credential the authorization didn't fill.
		_, _ = db.Exec(`DELETE FROM rs_ccmax_oauth_session WHERE state=$1`, state)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "session_gone": true})
		return
	}
	// Keep the session: the credential is parked, so fixing 账号 resubmits
	// without authorizing again.
	if err := cred.matchesAccount(body.Account); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "email": cred.Email})
		return
	}

	if reauth {
		if err := ccmaxUpdatePortalAccount(body.AccountID, body.ccmaxSubmitForm, cred); err != nil {
			c.JSON(http.StatusFailedDependency, gin.H{"error": err.Error(), "email": cred.Email})
			return
		}
		_, _ = db.Exec(`DELETE FROM rs_ccmax_oauth_session WHERE state=$1`, state)
		_, _ = db.Exec(`UPDATE rs_ccmax_account SET plan=$1, account=$2 WHERE remote_account_id=$3`, body.Plan, body.Account, body.AccountID)
		ccmaxRecordCredential(body.AccountID, cred)
		c.JSON(http.StatusOK, gin.H{"id": body.AccountID, "email": cred.Email, "msg": "重新授权成功"})
		return
	}

	reqBytes, _ := json.Marshal(map[string]any{
		"plan":              body.Plan,
		"account":           body.Account,
		"password":          body.Password,
		"auth_json":         cred,
		"register_location": body.RegisterLocation,
		"register_time":     body.RegisterTime,
		"plan_start_time":   body.PlanStartTime,
		"plan_end_time":     body.PlanEndTime,
	})
	status, respBody, err := supplierProxy(http.MethodPost, "/supplier-account/api/openapi/claude/accounts", ccmaxOpenAPIToken(), reqBytes)
	if err != nil {
		c.JSON(http.StatusFailedDependency, gin.H{"error": err.Error(), "email": cred.Email})
		return
	}
	if status < 200 || status >= 300 {
		c.JSON(http.StatusFailedDependency, gin.H{"error": supplierErr(respBody), "email": cred.Email})
		return
	}
	var upstream struct {
		ID    int64  `json:"id"`
		Alias string `json:"alias"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal(respBody, &upstream); err != nil || upstream.ID == 0 {
		c.JSON(http.StatusFailedDependency, gin.H{"error": "unexpected upstream response: " + strings.TrimSpace(string(respBody))})
		return
	}
	_, _ = db.Exec(`DELETE FROM rs_ccmax_oauth_session WHERE state=$1`, state)

	// Local ownership record — the portal itself only knows our one login.
	if _, err := db.Exec(
		`INSERT INTO rs_ccmax_account (uploaded_by, studio, remote_account_id, alias, account, plan, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (remote_account_id) DO NOTHING`,
		uid, studio, upstream.ID, upstream.Alias, body.Account, body.Plan, time.Now().Unix(),
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record account: " + err.Error()})
		return
	}
	ccmaxRecordCredential(upstream.ID, cred)
	c.JSON(http.StatusOK, gin.H{"id": upstream.ID, "alias": ccmaxDisplayAlias(upstream.Alias), "email": cred.Email, "msg": cmp.Or(upstream.Msg, "提交成功")})
}

// ccmaxRecordCredential keeps an encrypted copy of the credential just
// uploaded for a portal account (plus its proxy host:port). Best-effort: the
// portal already holds the account, so a failure here is only logged.
func ccmaxRecordCredential(id int64, cred ccmaxCredential) {
	raw, _ := json.Marshal(cred)
	enc, err := encryptRemoteToken(string(raw))
	if err != nil {
		log.Printf("[ccmax] encrypt credential for %d: %v", id, err)
		return
	}
	proxyDisplay := ""
	if u, err := url.Parse(cred.ProxyURL); err == nil {
		proxyDisplay = u.Host
	}
	if _, err := db.Exec(
		`UPDATE rs_ccmax_account SET credential_enc=$1, proxy_display=$2, credential_at=$3 WHERE remote_account_id=$4`,
		enc, proxyDisplay, time.Now().Unix(), id,
	); err != nil {
		log.Printf("[ccmax] record credential for %d: %v", id, err)
	}
}

// ccmaxExchangeCode trades the pasted authorization code for tokens through
// the supplier's proxy and assembles the credential JSON.
func ccmaxExchangeCode(proxyURL, proxyTZ, verifier, sessionState, pasted string) (ccmaxCredential, error) {
	var cred ccmaxCredential
	code, pastedState := ccmaxParseCode(pasted)
	if code == "" {
		return cred, errors.New("未识别到授权 code")
	}
	client, err := ccmaxProxyClient(proxyURL)
	if err != nil {
		return cred, err
	}

	// Field order matters to the token endpoint; a struct keeps it fixed.
	exchange := struct {
		GrantType    string `json:"grant_type"`
		Code         string `json:"code"`
		RedirectURI  string `json:"redirect_uri"`
		ClientID     string `json:"client_id"`
		CodeVerifier string `json:"code_verifier"`
		State        string `json:"state"`
	}{"authorization_code", code, ccmaxRedirectURI, ccmaxClientID, verifier, cmp.Or(pastedState, sessionState)}
	status, raw, err := ccmaxDo(client, http.MethodPost, ccmaxTokenURL, "", exchange)
	if err != nil {
		return cred, fmt.Errorf("token exchange: %w", err)
	}
	if status != http.StatusOK {
		return cred, fmt.Errorf("token exchange HTTP %d: %s", status, strings.TrimSpace(string(raw)))
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Account      struct {
			UUID               string `json:"uuid"`
			EmailAddress       string `json:"email_address"`
			VerificationStatus string `json:"verification_status"`
		} `json:"account"`
		Organization struct {
			UUID string `json:"uuid"`
			Name string `json:"name"`
		} `json:"organization"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil || tok.AccessToken == "" {
		return cred, errors.New("unexpected token response")
	}
	cred = ccmaxCredential{
		AccessToken:        tok.AccessToken,
		RefreshToken:       tok.RefreshToken,
		Email:              tok.Account.EmailAddress,
		AccountUUID:        tok.Account.UUID,
		OrganizationUUID:   tok.Organization.UUID,
		OrganizationName:   tok.Organization.Name,
		Type:               "claude",
		ProxyURL:           proxyURL,
		VerificationStatus: tok.Account.VerificationStatus,
	}

	// Login companion lookups (profile, then roles). Failures only lose the
	// profile overrides, same as the script.
	if pstatus, praw, err := ccmaxDo(client, http.MethodGet, ccmaxProfileURL, tok.AccessToken, nil); err == nil && pstatus == http.StatusOK {
		var prof struct {
			Account struct {
				UUID               string `json:"uuid"`
				Email              string `json:"email"`
				VerificationStatus string `json:"verification_status"`
			} `json:"account"`
			Organization struct {
				UUID string `json:"uuid"`
				Name string `json:"name"`
			} `json:"organization"`
		}
		if json.Unmarshal(praw, &prof) == nil {
			cred.AccountUUID = cmp.Or(prof.Account.UUID, cred.AccountUUID)
			cred.Email = cmp.Or(prof.Account.Email, cred.Email)
			cred.VerificationStatus = cmp.Or(prof.Account.VerificationStatus, cred.VerificationStatus)
			cred.OrganizationUUID = cmp.Or(prof.Organization.UUID, cred.OrganizationUUID)
			cred.OrganizationName = cmp.Or(prof.Organization.Name, cred.OrganizationName)
		}
	} else {
		log.Printf("[ccmax] profile lookup failed (status=%d err=%v); ignoring", pstatus, err)
	}
	if rstatus, _, err := ccmaxDo(client, http.MethodGet, ccmaxRolesURL, tok.AccessToken, nil); err != nil || rstatus != http.StatusOK {
		log.Printf("[ccmax] claude_cli roles lookup failed (status=%d err=%v); ignoring", rstatus, err)
	}
	if cred.Email == "" {
		return cred, errors.New("未能获取账号邮箱")
	}
	cred.VerificationStatus = cmp.Or(cred.VerificationStatus, "verified")
	deviceID := make([]byte, 32)
	_, _ = rand.Read(deviceID)
	cred.ClaudeDeviceIDs = []string{hex.EncodeToString(deviceID)}

	// Timestamps in the proxy exit's timezone (UTC when unknown).
	loc := time.UTC
	if l, err := time.LoadLocation(proxyTZ); err == nil && proxyTZ != "" {
		loc = l
	}
	now := time.Now()
	cred.LastRefresh = now.In(loc).Format(time.RFC3339)
	if tok.ExpiresIn > 0 {
		cred.Expired = now.Add(time.Duration(tok.ExpiresIn) * time.Second).In(loc).Format(time.RFC3339)
	}
	return cred, nil
}

// handleCCMaxOptions returns the form's choices from the portal: claude plans
// (the claude provider's models) and register locations.
func handleCCMaxOptions(c *gin.Context) {
	if !ccmaxWeb.configured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "CC Max portal login not configured"})
		return
	}
	status, body, err := ccmaxWeb.proxy(http.MethodGet, "/supplier-account/api/models", nil)
	if err == nil && status != http.StatusOK {
		err = errors.New(supplierErr(body))
	}
	if err != nil {
		c.JSON(http.StatusFailedDependency, gin.H{"error": err.Error()})
		return
	}
	var models struct {
		List []struct {
			Provider  string `json:"provider"`
			ModelName string `json:"model_name"`
		} `json:"list"`
	}
	_ = json.Unmarshal(body, &models)
	plans := make([]string, 0)
	for _, m := range models.List {
		if m.Provider == "claude" {
			plans = append(plans, m.ModelName)
		}
	}

	status, body, err = ccmaxWeb.proxy(http.MethodGet, "/supplier-account/api/cpa/options", nil)
	if err == nil && status != http.StatusOK {
		err = errors.New(supplierErr(body))
	}
	if err != nil {
		c.JSON(http.StatusFailedDependency, gin.H{"error": err.Error()})
		return
	}
	var opts struct {
		RegisterLocations []struct {
			Value string `json:"value"`
			Label string `json:"label"`
		} `json:"register_locations"`
	}
	_ = json.Unmarshal(body, &opts)
	c.JSON(http.StatusOK, gin.H{"plans": plans, "register_locations": opts.RegisterLocations})
}

// ccmaxAccountRow is a portal claude account trimmed to what the page shows;
// the portal row also carries the credential and password, which never leave
// the server.
type ccmaxAccountRow struct {
	ID               int64  `json:"id"`
	Alias            string `json:"alias"`
	Account          string `json:"account"`
	Status           string `json:"status"`
	Plan             string `json:"plan"`
	RegisterLocation string `json:"register_location"`
	PlanEndTime      string `json:"plan_end_time"`
	RegisterTime     string `json:"register_time"`
	PlanStartTime    string `json:"plan_start_time"`
	CreatedAt        string `json:"created_at"`
	Username         string `json:"username,omitempty"`
	// Remark is the local note; RemarkEditable is false for portal rows that
	// were not uploaded through this page (no local record to hold a note).
	Remark         string `json:"remark"`
	RemarkEditable bool   `json:"remark_editable"`
	// Usage as the portal reports it on the roster (extra.cpa_usage); the
	// OpenAPI metrics endpoint does not cover claude accounts. The portal sends
	// cost in micro-yuan (303731485 = ¥303.73); CostRMB is converted to yuan.
	// The utilization values are fractions (0.04 = 4%), passed through unscaled.
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CostRMB             float64 `json:"cost_rmb"`
	FiveHourUsedPercent float64 `json:"five_hour_used_percent"`
	WeekUsedPercent     float64 `json:"week_used_percent"`
	// Window reset times (unix seconds; 0 = unknown).
	FiveHourResetAt int64 `json:"five_hour_reset_at"`
	WeekResetAt     int64 `json:"week_reset_at"`
	// Proxy host:port of the last credential uploaded through this page.
	ProxyDisplay string `json:"proxy_display"`
}

// ccmaxVisibleUploaders returns whose CC Max uploads the caller may see:
// all=true for admins; otherwise the caller's own uid plus, for a
// ccmax_manager, the members an admin assigned to them.
func ccmaxVisibleUploaders(c *gin.Context) (all bool, uids []int64, err error) {
	if callerIsSupplierAdmin(c) {
		return true, nil, nil
	}
	uidAny, _ := c.Get("user_id")
	uid, _ := uidAny.(int64)
	uids = []int64{uid}
	roleAny, _ := c.Get("role")
	if role, _ := roleAny.(int); role != minCCMaxManagerRole {
		return false, uids, nil
	}
	rows, err := db.Query(`SELECT member_id FROM rs_ccmax_manager_member WHERE manager_id=$1`, uid)
	if err != nil {
		return false, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return false, nil, err
		}
		uids = append(uids, id)
	}
	return false, uids, rows.Err()
}

// ccmaxScopedAccounts loads the CC Max login's claude roster from the portal
// and keeps the rows the caller may see (ccmaxVisibleUploaders). Remark and
// re-authorize stay with the uploader (admins: any locally recorded row), so
// a manager's view of members' rows is read-only.
func ccmaxScopedAccounts(c *gin.Context) ([]ccmaxAccountRow, error) {
	raws, err := fetchPortalAccountsRaw(ccmaxWeb, "claude")
	if err != nil {
		return nil, err
	}
	all, uids, err := ccmaxVisibleUploaders(c)
	if err != nil {
		return nil, err
	}
	uidAny, _ := c.Get("user_id")
	callerID, _ := uidAny.(int64)
	query := `SELECT a.remote_account_id, a.uploaded_by, COALESCE(u.username, ''), a.remark, a.proxy_display FROM rs_ccmax_account a
	            LEFT JOIN rs_auth_user u ON u.id = a.uploaded_by`
	args := []any{}
	if !all {
		query += ` WHERE a.uploaded_by = ANY($1)`
		args = append(args, pq.Array(uids))
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	type owner struct {
		uploadedBy              int64
		username, remark, proxy string
	}
	owners := map[int64]owner{}
	for rows.Next() {
		var id int64
		var o owner
		if err := rows.Scan(&id, &o.uploadedBy, &o.username, &o.remark, &o.proxy); err != nil {
			rows.Close()
			return nil, err
		}
		owners[id] = o
	}
	rows.Close()

	showUploader := all || len(uids) > 1
	out := make([]ccmaxAccountRow, 0, len(raws))
	for _, raw := range raws {
		row, err := parseCCMaxPortalRow(raw)
		if err != nil {
			log.Printf("[ccmax] skip unparseable portal account row: %v", err)
			continue
		}
		o, owned := owners[row.ID]
		if !all && !owned {
			continue
		}
		row.Remark, row.ProxyDisplay = o.remark, o.proxy
		row.RemarkEditable = owned && (all || o.uploadedBy == callerID)
		if showUploader {
			row.Username = o.username
		}
		out = append(out, row)
	}
	return out, nil
}

// ccmaxDisplayAlias drops the supplier/company segment from a portal alias
// ("claude-<supplier>-<seq>" → "claude-<seq>") instead of masking it, so the
// portal-side company name never shows on the CC Max page.
func ccmaxDisplayAlias(alias string) string {
	parts := strings.Split(alias, "-")
	if len(parts) < 3 {
		return alias
	}
	return parts[0] + "-" + parts[len(parts)-1]
}

// parseCCMaxPortalRow maps one WEB /supplier/accounts claude row onto the page
// row. Claude rows keep the 上号 form in auth_config (a JSON string), the
// lifecycle status in ext_status (status is a numeric enable flag), and the
// login in project_id / plan in model as fallbacks. Credential fields are
// never read.
func parseCCMaxPortalRow(raw json.RawMessage) (ccmaxAccountRow, error) {
	var r struct {
		ID           int64  `json:"id"`
		AccountAlias string `json:"account_alias"`
		ExtStatus    string `json:"ext_status"`
		Model        string `json:"model"`
		ProjectID    string `json:"project_id"`
		AuthConfig   string `json:"auth_config"`
		Extra        string `json:"extra"`
		CreatedAt    string `json:"created_at"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return ccmaxAccountRow{}, err
	}
	var form struct {
		Account          string `json:"account"`
		Plan             string `json:"plan"`
		RegisterLocation string `json:"register_location"`
		PlanEndTime      string `json:"plan_end_time"`
		RegisterTime     string `json:"register_time"`
		PlanStartTime    string `json:"plan_start_time"`
	}
	if r.AuthConfig != "" {
		_ = json.Unmarshal([]byte(r.AuthConfig), &form)
	}
	var extra struct {
		CpaUsage struct {
			Cost         float64 `json:"cost"`
			InputTokens  int64   `json:"input_tokens"`
			OutputTokens int64   `json:"output_tokens"`
			Utilization  struct {
				WeekUsedPercent     float64 `json:"week_used_percent"`
				FiveHourUsedPercent float64 `json:"five_hour_used_percent"`
				WeekResetAt         int64   `json:"week_reset_at"`
				FiveHourResetAt     int64   `json:"five_hour_reset_at"`
			} `json:"utilization"`
		} `json:"cpa_usage"`
	}
	if r.Extra != "" {
		_ = json.Unmarshal([]byte(r.Extra), &extra)
	}
	usage := extra.CpaUsage
	return ccmaxAccountRow{
		InputTokens:         usage.InputTokens,
		OutputTokens:        usage.OutputTokens,
		CostRMB:             usage.Cost / 1e6,
		FiveHourUsedPercent: usage.Utilization.FiveHourUsedPercent,
		WeekUsedPercent:     usage.Utilization.WeekUsedPercent,
		FiveHourResetAt:     usage.Utilization.FiveHourResetAt,
		WeekResetAt:         usage.Utilization.WeekResetAt,
		ID:                  r.ID,
		Alias:               ccmaxDisplayAlias(r.AccountAlias),
		Account:             cmp.Or(form.Account, r.ProjectID),
		Status:              r.ExtStatus,
		Plan:                cmp.Or(form.Plan, r.Model),
		RegisterLocation:    form.RegisterLocation,
		PlanEndTime:         form.PlanEndTime,
		RegisterTime:        form.RegisterTime,
		PlanStartTime:       form.PlanStartTime,
		CreatedAt:           r.CreatedAt,
	}, nil
}

func handleCCMaxAccountList(c *gin.Context) {
	if !ccmaxWeb.configured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "CC Max portal login not configured"})
		return
	}
	rows, err := ccmaxScopedAccounts(c)
	if err != nil {
		c.JSON(http.StatusFailedDependency, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"accounts": rows, "fx_rate": supplierFxRate()})
}

// ccmaxCallerOwns reports whether the caller may act on portal account id:
// suppliers only on accounts they uploaded here, admins on any.
func ccmaxCallerOwns(c *gin.Context, id int64) bool {
	if callerIsSupplierAdmin(c) {
		return true
	}
	uidAny, _ := c.Get("user_id")
	uid, _ := uidAny.(int64)
	var one int
	return db.QueryRow(`SELECT 1 FROM rs_ccmax_account WHERE remote_account_id=$1 AND uploaded_by=$2`, id, uid).Scan(&one) == nil
}

// ccmaxCallerCanView reports whether the caller may view portal account id:
// ccmaxCallerOwns plus a ccmax_manager's assigned members' accounts.
func ccmaxCallerCanView(c *gin.Context, id int64) bool {
	all, uids, err := ccmaxVisibleUploaders(c)
	if err != nil {
		return false
	}
	if all {
		return true
	}
	var one int
	return db.QueryRow(`SELECT 1 FROM rs_ccmax_account WHERE remote_account_id=$1 AND uploaded_by = ANY($2)`, id, pq.Array(uids)).Scan(&one) == nil
}

// ccmaxAccountIDParam parses :id (a portal account id) and answers 400/404
// unless allowed reports the caller may use it.
func ccmaxAccountIDParam(c *gin.Context, allowed func(*gin.Context, int64) bool) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return 0, false
	}
	if !allowed(c, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "账号不存在"})
		return 0, false
	}
	return id, true
}

// ccmaxUpdatePortalAccount re-submits an existing claude account through the
// portal's WEB edit endpoint with a fresh credential (new tokens + proxy_url),
// mirroring the portal UI's edit form. Optional portal fields not offered here
// (balance, payment method, warranty …), the model and the portal remark are
// carried over from the current row so the edit doesn't clear them. A blank
// password leaves it unchanged.
func ccmaxUpdatePortalAccount(id int64, form ccmaxSubmitForm, cred ccmaxCredential) error {
	raws, err := fetchPortalAccountsRaw(ccmaxWeb, "claude")
	if err != nil {
		return err
	}
	var cur struct {
		ID         int64  `json:"id"`
		Model      string `json:"model"`
		Remark     string `json:"remark"`
		AuthConfig string `json:"auth_config"`
		IsLocked   int    `json:"is_locked"`
	}
	found := false
	for _, raw := range raws {
		if json.Unmarshal(raw, &cur) == nil && cur.ID == id {
			found = true
			break
		}
	}
	if !found {
		return errors.New("portal 中找不到该账号")
	}
	if cur.IsLocked == 1 {
		return errors.New("该账号已被 portal 锁定，不可修改")
	}
	prev := map[string]any{}
	_ = json.Unmarshal([]byte(cur.AuthConfig), &prev)
	keep := func(key string) string {
		if v, ok := prev[key]; ok && v != nil {
			return strings.TrimSpace(fmt.Sprint(v))
		}
		return ""
	}

	credJSON, _ := json.Marshal(cred)
	encADC, err := ccmaxWeb.encrypt("")
	if err != nil {
		return err
	}
	req := map[string]any{
		"provider":              "claude",
		"model":                 cmp.Or(cur.Model, form.Plan),
		"adc_config":            "",
		"api_key":               "",
		"region":                "",
		"aws_region":            "",
		"aws_project_id":        "",
		"aws_auth_type":         "arn",
		"account_id":            form.Account,
		"proxy_type":            "",
		"account_type":          2,
		"remark":                cur.Remark,
		"cpa_plan":              form.Plan,
		"cpa_register_time":     form.RegisterTime,
		"cpa_plan_start_time":   form.PlanStartTime,
		"cpa_plan_end_time":     form.PlanEndTime,
		"cpa_register_location": form.RegisterLocation,
		"cpa_balance":           keep("balance"),
		"cpa_payment_method":    keep("payment_method"),
		"cpa_register_device":   keep("register_device"),
		"cpa_recharge_time":     keep("recharge_time"),
		"cpa_warranty_period":   keep("warranty_period"),
		"cpa_auth_json":         string(credJSON),
		"enc_adc_config":        encADC,
	}
	if form.Password != "" {
		encPwd, err := ccmaxWeb.encrypt(form.Password)
		if err != nil {
			return err
		}
		req["enc_api_key"] = encPwd
	}
	reqBytes, _ := json.Marshal(req)
	status, body, err := ccmaxWeb.proxy(http.MethodPut, fmt.Sprintf("/supplier-account/api/supplier/accounts/%d", id), reqBytes)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return errors.New(supplierErr(body))
	}
	return nil
}

// handleCCMaxAccountReason returns why a portal claude account failed, the
// same text the portal's 查看失败原因 shows. Sync / check failures and
// Claude-side states (封号, token 撤销 …) live behind two portal endpoints, so
// try both and return the first recorded reason.
func handleCCMaxAccountReason(c *gin.Context) {
	id, ok := ccmaxAccountIDParam(c, ccmaxCallerCanView)
	if !ok {
		return
	}
	for _, kind := range []string{"check-fail-reason", "claude-status-reason"} {
		status, body, err := ccmaxWeb.proxy(http.MethodGet, fmt.Sprintf("/supplier-account/api/supplier/accounts/%d/%s", id, kind), nil)
		if err == nil && status != http.StatusOK {
			err = errors.New(supplierErr(body))
		}
		if err != nil {
			c.JSON(http.StatusFailedDependency, gin.H{"error": err.Error()})
			return
		}
		var r struct {
			Content string `json:"content"`
			Found   bool   `json:"found"`
		}
		if json.Unmarshal(body, &r) == nil && r.Found && strings.TrimSpace(r.Content) != "" {
			c.JSON(http.StatusOK, gin.H{"found": true, "content": r.Content})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"found": false, "content": ""})
}

// handleCCMaxAccountRemark updates the local note on an account uploaded
// through this page.
func handleCCMaxAccountRemark(c *gin.Context) {
	id, ok := ccmaxAccountIDParam(c, ccmaxCallerOwns)
	if !ok {
		return
	}
	var body struct {
		Remark string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	remark := strings.TrimSpace(body.Remark)
	if utf8.RuneCountInString(remark) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "备注最多 200 字"})
		return
	}
	res, err := db.Exec(`UPDATE rs_ccmax_account SET remark=$1 WHERE remote_account_id=$2`, remark, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "该账号不是通过本页面上号的，无法添加备注"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"remark": remark})
}

// ---- CC Max portal credentials: env bootstrap, report_config override ----

var (
	// ccmaxPortalUsername / ccmaxPortalPassword / ccmaxPortalToken are the env
	// bootstrap (SUPPLIER_CCMAX_*) for the CC Max portal login + OpenAPI token,
	// a separate portal user from 账号上号 on the same portal.
	ccmaxPortalUsername string
	ccmaxPortalPassword string
	ccmaxPortalToken    string
)

const (
	cfgCCMaxPortalUsername = "ccmax_portal_username"
	cfgCCMaxPortalPassword = "ccmax_portal_password"
	cfgCCMaxOpenAPIToken   = "ccmax_openapi_token"
)

// ccmaxWeb is the CC Max WEB login (plans / options / roster).
var ccmaxWeb = &portalWebSession{
	name: "ccmax",
	creds: func() (string, string) {
		return cmp.Or(strings.TrimSpace(supplierConfigGet(cfgCCMaxPortalUsername)), ccmaxPortalUsername),
			cmp.Or(supplierConfigGet(cfgCCMaxPortalPassword), ccmaxPortalPassword)
	},
}

func ccmaxOpenAPIToken() string {
	return cmp.Or(strings.TrimSpace(supplierConfigGet(cfgCCMaxOpenAPIToken)), ccmaxPortalToken)
}

func ccmaxOpenAPIConfigured() bool {
	return supplierAccountBaseURL != "" && ccmaxOpenAPIToken() != ""
}

func handleCCMaxSettingsGet(c *gin.Context) {
	username, password := ccmaxWeb.creds()
	token := ccmaxOpenAPIToken()
	last4 := ""
	if len(token) > 4 {
		last4 = token[len(token)-4:]
	}
	c.JSON(http.StatusOK, gin.H{
		"username":            username,
		"password_set":        password != "",
		"openapi_token_set":   token != "",
		"openapi_token_last4": last4,
	})
}

// handleCCMaxSettingsSet updates the CC Max portal login / token. Omitted
// fields stay unchanged; an empty string clears the override (env applies).
func handleCCMaxSettingsSet(c *gin.Context) {
	var body struct {
		Username     *string `json:"username"`
		Password     *string `json:"password"`
		OpenAPIToken *string `json:"openapi_token"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	for key, val := range map[string]*string{
		cfgCCMaxPortalUsername: body.Username,
		cfgCCMaxPortalPassword: body.Password,
		cfgCCMaxOpenAPIToken:   body.OpenAPIToken,
	} {
		if val == nil {
			continue
		}
		if err := supplierConfigSet(key, strings.TrimSpace(*val)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	// New credentials ⇒ the cached WEB token belongs to the old login.
	ccmaxWeb.invalidate()
	handleCCMaxSettingsGet(c)
}

// ---- ccmax_manager ↔ supplier_ccmax assignment (admin) ----

type ccmaxUserRef struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// handleCCMaxManagersList returns every ccmax_manager with the supplier_ccmax
// users assigned to them, plus all supplier_ccmax users to pick from.
func handleCCMaxManagersList(c *gin.Context) {
	rows, err := db.Query(`SELECT id, username, role FROM rs_auth_user WHERE role IN ($1, $2) ORDER BY username`,
		minCCMaxManagerRole, minSupplierCCMaxRole)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	type manager struct {
		ccmaxUserRef
		MemberIDs []int64 `json:"member_ids"`
	}
	managers := []*manager{}
	byID := map[int64]*manager{}
	suppliers := []ccmaxUserRef{}
	for rows.Next() {
		var u ccmaxUserRef
		var role int
		if err := rows.Scan(&u.ID, &u.Username, &role); err != nil {
			rows.Close()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if role == minSupplierCCMaxRole {
			suppliers = append(suppliers, u)
			continue
		}
		m := &manager{ccmaxUserRef: u, MemberIDs: []int64{}}
		managers = append(managers, m)
		byID[u.ID] = m
	}
	rows.Close()

	rows, err = db.Query(`SELECT manager_id, member_id FROM rs_ccmax_manager_member ORDER BY member_id`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	for rows.Next() {
		var managerID, memberID int64
		if err := rows.Scan(&managerID, &memberID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if m := byID[managerID]; m != nil {
			m.MemberIDs = append(m.MemberIDs, memberID)
		}
	}
	c.JSON(http.StatusOK, gin.H{"managers": managers, "suppliers": suppliers})
}

// handleCCMaxManagerMembersSet replaces the supplier_ccmax users a
// ccmax_manager may see.
func handleCCMaxManagerMembersSet(c *gin.Context) {
	managerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || managerID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body struct {
		MemberIDs []int64 `json:"member_ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	target, err := authUserByID(managerID)
	if err != nil || target.Role != minCCMaxManagerRole {
		c.JSON(http.StatusNotFound, gin.H{"error": "CC Max manager not found"})
		return
	}
	slices.Sort(body.MemberIDs)
	body.MemberIDs = slices.Compact(body.MemberIDs)
	if len(body.MemberIDs) > 0 {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM rs_auth_user WHERE id = ANY($1) AND role=$2`,
			pq.Array(body.MemberIDs), minSupplierCCMaxRole).Scan(&n); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if n != len(body.MemberIDs) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "members must be supplier_ccmax users"})
			return
		}
	}
	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM rs_ccmax_manager_member WHERE manager_id=$1`, managerID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	now := time.Now().Unix()
	for _, memberID := range body.MemberIDs {
		if _, err := tx.Exec(`INSERT INTO rs_ccmax_manager_member (manager_id, member_id, created_at) VALUES ($1, $2, $3)`,
			managerID, memberID, now); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": managerID, "member_ids": body.MemberIDs})
}
