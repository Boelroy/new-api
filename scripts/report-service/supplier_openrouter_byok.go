package main

// 账号上号 「CF OpenRouter」: a Cloudflare API token is uploaded to the 号池 as an
// OpenRouter account. With the admin-configured OpenRouter management key the
// upload
//
//  1. creates a fresh OpenRouter API key (one per CF key, usage isolated);
//  2. registers the CF Account ID + token as a BYOK credential (provider cloudflare) that
//     only that OR key may use, with is_byok_only so requests never fall back
//     to OpenRouter's shared capacity (our OR credits);
//  3. uploads the OR key to the 号池 under its openrouter provider.
//
// A failure in step 2 deletes the OR key; a failure in step 3 deletes the BYOK
// credential and the OR key, so no orphan is left. Neither the CF token nor the
// OR key ever reaches a log line or an error message.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// supplierCFOpenRouterProvider is the local provider name of these uploads
	// (form choice, visibility allowlist, rs_supplier_account.provider).
	supplierCFOpenRouterProvider = "cf_openrouter"
	// supplierOpenRouterPortalProvider is the 号池's provider the OR key is
	// uploaded under (WEB /providers lists it as "openrouter").
	supplierOpenRouterPortalProvider = "openrouter"

	cfgOpenRouterMgmtKey  = "openrouter_mgmt_key"
	cfgOpenRouterKeyLimit = "openrouter_key_limit"
)

// openRouterBaseURL is the OpenRouter API root (env OPENROUTER_BASE_URL
// overrides it; tests point it at a fake).
var openRouterBaseURL = "https://openrouter.ai/api/v1"

var openRouterHTTPClient = &http.Client{Timeout: 20 * time.Second}

// openRouterMgmtKey is the management key from the admin settings ("" = the
// CF OpenRouter upload is unavailable).
func openRouterMgmtKey() string {
	return strings.TrimSpace(supplierConfigGet(cfgOpenRouterMgmtKey))
}

// openRouterKeyLimit is the optional USD credit limit set on each new OR key;
// nil when unset (no limit).
func openRouterKeyLimit() *float64 {
	raw := strings.TrimSpace(supplierConfigGet(cfgOpenRouterKeyLimit))
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 {
		return nil
	}
	return &v
}

// openRouterCall sends one management-API request and returns the status and
// body. Transport errors are reduced to a fixed sentence: url.Error text
// carries the request URL, never secrets, but keep the surface minimal.
func openRouterCall(mgmtKey, method, path string, payload any) (int, []byte, error) {
	var reader io.Reader
	if payload != nil {
		blob, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(blob)
	}
	req, err := http.NewRequest(method, strings.TrimRight(openRouterBaseURL, "/")+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+mgmtKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := openRouterHTTPClient.Do(req)
	if err != nil {
		return 0, nil, errors.New("OpenRouter 接口不可达")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

// openRouterErr renders a failed management call as "<step>失败（HTTP n）: msg"
// with every given secret blanked out of OpenRouter's message.
func openRouterErr(step string, status int, body []byte, secrets ...string) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := ""
	if json.Unmarshal(body, &e) == nil {
		msg = strings.TrimSpace(e.Error.Message)
	}
	for _, s := range secrets {
		if s != "" {
			msg = strings.ReplaceAll(msg, s, "***")
		}
	}
	if msg == "" {
		return fmt.Errorf("%s失败（HTTP %d）", step, status)
	}
	return fmt.Errorf("%s失败（HTTP %d）: %s", step, status, msg)
}

// supplierOpenRouterBYOK is what steps 1–2 created for one upload.
type supplierOpenRouterBYOK struct {
	mgmtKey string
	ORKey   string // raw OR API key, sent to the 号池 only
	KeyHash string // OR key hash (its id in the keys API)
	BYOKID  string
}

// validCloudflareAccountID reports whether id looks like a Cloudflare Account
// ID: 32 hexadecimal characters.
func validCloudflareAccountID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// provisionOpenRouterBYOK runs steps 1–2: a new OR key named name, then the CF
// account + token as a BYOK credential locked to that key. If step 2 fails the
// OR key is deleted before returning.
func provisionOpenRouterBYOK(mgmtKey, cfAccountID, cfKey, name string) (*supplierOpenRouterBYOK, error) {
	// OpenRouter's cloudflare BYOK key is a JSON string (not a nested object)
	// holding the account id and the API token.
	cfCredential, err := json.Marshal(map[string]string{"accountId": cfAccountID, "apiToken": cfKey})
	if err != nil {
		return nil, err
	}

	keyReq := map[string]any{"name": name}
	if limit := openRouterKeyLimit(); limit != nil {
		keyReq["limit"] = *limit
	}
	status, body, err := openRouterCall(mgmtKey, http.MethodPost, "/keys", keyReq)
	if err != nil {
		return nil, fmt.Errorf("创建 OpenRouter Key 失败: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, openRouterErr("创建 OpenRouter Key", status, body, mgmtKey, cfKey)
	}
	var created struct {
		Key  string `json:"key"`
		Data struct {
			Hash string `json:"hash"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &created) != nil || created.Key == "" || created.Data.Hash == "" {
		// A key may exist without us being able to name it; nothing to roll back.
		return nil, errors.New("创建 OpenRouter Key 失败: 返回格式异常")
	}
	out := &supplierOpenRouterBYOK{mgmtKey: mgmtKey, ORKey: created.Key, KeyHash: created.Data.Hash}

	status, body, err = openRouterCall(mgmtKey, http.MethodPost, "/byok", map[string]any{
		"provider":               "cloudflare",
		"key":                    string(cfCredential),
		"name":                   name,
		"allowed_api_key_hashes": []string{out.KeyHash},
		"is_byok_only":           true,
	})
	if err == nil && (status < 200 || status >= 300) {
		err = openRouterErr("登记 Cloudflare BYOK", status, body, mgmtKey, cfKey, out.ORKey)
	}
	if err == nil {
		var byok struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &byok) != nil || byok.Data.ID == "" {
			err = errors.New("登记 Cloudflare BYOK 失败: 返回格式异常")
		} else {
			out.BYOKID = byok.Data.ID
		}
	}
	if err != nil {
		out.rollback()
		return nil, err
	}
	return out, nil
}

// rollback deletes whatever steps 1–2 created (BYOK credential first, then the
// OR key). Failures are logged by id/hash only, for manual cleanup.
func (b *supplierOpenRouterBYOK) rollback() {
	if b.BYOKID != "" {
		if status, _, err := openRouterCall(b.mgmtKey, http.MethodDelete, "/byok/"+url.PathEscape(b.BYOKID), nil); err != nil || status < 200 || status >= 300 {
			log.Printf("[supplier-account] cf_openrouter rollback: delete BYOK %s failed (status %d, err %v)", b.BYOKID, status, err)
		}
	}
	if status, _, err := openRouterCall(b.mgmtKey, http.MethodDelete, "/keys/"+url.PathEscape(b.KeyHash), nil); err != nil || status < 200 || status >= 300 {
		log.Printf("[supplier-account] cf_openrouter rollback: delete OR key %s failed (status %d, err %v)", b.KeyHash, status, err)
	}
}

// rename sets the OR key's (and BYOK credential's) name once the 号池 alias is
// known. Best effort: the upload already succeeded.
func (b *supplierOpenRouterBYOK) rename(name string) {
	if status, _, err := openRouterCall(b.mgmtKey, http.MethodPatch, "/keys/"+url.PathEscape(b.KeyHash), map[string]any{"name": name}); err != nil || status < 200 || status >= 300 {
		log.Printf("[supplier-account] cf_openrouter: rename OR key %s failed (status %d, err %v)", b.KeyHash, status, err)
	}
	if status, _, err := openRouterCall(b.mgmtKey, http.MethodPatch, "/byok/"+url.PathEscape(b.BYOKID), map[string]any{"name": name}); err != nil || status < 200 || status >= 300 {
		log.Printf("[supplier-account] cf_openrouter: rename BYOK %s failed (status %d, err %v)", b.BYOKID, status, err)
	}
}

// openRouterKeyName labels the OR key / BYOK credential with the uploader and,
// once known, the 号池 alias. OpenRouter caps names at 255 characters.
func openRouterKeyName(uploader, alias string) string {
	name := "cf-openrouter " + uploader
	if alias != "" {
		name += " " + alias
	} else {
		name += " " + time.Now().In(supplierPortalLoc).Format("20060102-150405")
	}
	if len(name) > 255 {
		name = name[:255]
	}
	return name
}
