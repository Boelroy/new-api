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
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
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
// is rejected before the single-use authorization code is spent.
func (f *ccmaxSubmitForm) validate() error {
	f.Plan = strings.TrimSpace(f.Plan)
	f.Account = strings.TrimSpace(f.Account)
	f.RegisterLocation = strings.TrimSpace(f.RegisterLocation)
	if f.Plan == "" || f.Account == "" || f.Password == "" || f.RegisterLocation == "" {
		return errors.New("套餐、账号、密码、注册地均为必填")
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
func handleCCMaxSubmit(c *gin.Context) {
	if !ccmaxOpenAPIConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "CC Max OpenAPI token not configured (set SUPPLIER_CCMAX_TOKEN)"})
		return
	}
	var body struct {
		State string `json:"state"`
		Code  string `json:"code"`
		ccmaxSubmitForm
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if err := body.validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
	c.JSON(http.StatusOK, gin.H{"id": upstream.ID, "alias": ccmaxDisplayAlias(upstream.Alias), "email": cred.Email, "msg": cmp.Or(upstream.Msg, "提交成功")})
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
	CreatedAt        string `json:"created_at"`
	Username         string `json:"username,omitempty"`
	// Usage as the portal reports it on the roster (extra.cpa_usage); the
	// OpenAPI metrics endpoint does not cover claude accounts. Cost is RMB.
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CostRMB             float64 `json:"cost_rmb"`
	FiveHourUsedPercent float64 `json:"five_hour_used_percent"`
	WeekUsedPercent     float64 `json:"week_used_percent"`
}

// ccmaxScopedAccounts loads the CC Max login's claude roster from the portal
// and keeps the rows the caller may see: admins see all, suppliers only the
// accounts they uploaded through report-service.
func ccmaxScopedAccounts(c *gin.Context) ([]ccmaxAccountRow, error) {
	raws, err := fetchPortalAccountsRaw(ccmaxWeb, "claude")
	if err != nil {
		return nil, err
	}
	isAdmin := callerIsSupplierAdmin(c)
	query := `SELECT a.remote_account_id, COALESCE(u.username, '') FROM rs_ccmax_account a
	            LEFT JOIN rs_auth_user u ON u.id = a.uploaded_by`
	args := []any{}
	if !isAdmin {
		uidAny, _ := c.Get("user_id")
		uid, _ := uidAny.(int64)
		query += ` WHERE a.uploaded_by=$1`
		args = append(args, uid)
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	owners := map[int64]string{}
	for rows.Next() {
		var id int64
		var username string
		if err := rows.Scan(&id, &username); err != nil {
			rows.Close()
			return nil, err
		}
		owners[id] = username
	}
	rows.Close()

	out := make([]ccmaxAccountRow, 0, len(raws))
	for _, raw := range raws {
		row, err := parseCCMaxPortalRow(raw)
		if err != nil {
			log.Printf("[ccmax] skip unparseable portal account row: %v", err)
			continue
		}
		username, owned := owners[row.ID]
		if !isAdmin && !owned {
			continue
		}
		if isAdmin {
			row.Username = username
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
		CostRMB:             usage.Cost,
		FiveHourUsedPercent: usage.Utilization.FiveHourUsedPercent,
		WeekUsedPercent:     usage.Utilization.WeekUsedPercent,
		ID:                  r.ID,
		Alias:               ccmaxDisplayAlias(r.AccountAlias),
		Account:             cmp.Or(form.Account, r.ProjectID),
		Status:              r.ExtStatus,
		Plan:                cmp.Or(form.Plan, r.Model),
		RegisterLocation:    form.RegisterLocation,
		PlanEndTime:         form.PlanEndTime,
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
