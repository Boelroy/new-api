package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Remote new-api inspector: fetch channels + used_quota from an external
// new-api deployment via its admin HTTP API. Credentials are stored as
// named profiles in remote_newapi_profile; the token is AES-256-GCM
// encrypted with a key derived from jwtSecret. Super admin only.

// ---- Encryption helpers ----

// remoteTokenKey returns the AES-256 key derived from jwtSecret.
// Falls back to a zero key when jwtSecret is unset (dev / first-run);
// callers should not treat encrypted-at-rest as an integrity guarantee.
func remoteTokenKey() []byte {
	h := sha256.Sum256(jwtSecret)
	return h[:]
}

func encryptRemoteToken(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	block, err := aes.NewCipher(remoteTokenKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

func decryptRemoteToken(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(remoteTokenKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// ---- Data model ----

type remoteProfile struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	UserID    int64  `json:"user_id"`
	HasToken  bool   `json:"has_token"` // token never returned; UI shows only whether set
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// normalizeHost trims trailing slash and rejects empty / non-http hosts.
// The remote may include a path prefix; we only trim the trailing slash.
func normalizeHost(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("host is required")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid host: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("host must start with http:// or https://")
	}
	// Preserve any path prefix (e.g. reverse-proxied deployments), just drop
	// the trailing slash so we can concatenate /api/... cleanly.
	return strings.TrimRight(s, "/"), nil
}

// ---- Handlers: profile CRUD ----

func handleRemoteProfileList(c *gin.Context) {
	rows, err := db.Query(
		`SELECT id, name, host, user_id, access_token_enc, created_at, updated_at
		 FROM remote_newapi_profile ORDER BY name ASC`,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := make([]remoteProfile, 0)
	for rows.Next() {
		var p remoteProfile
		var enc string
		if err := rows.Scan(&p.ID, &p.Name, &p.Host, &p.UserID, &enc, &p.CreatedAt, &p.UpdatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		p.HasToken = enc != ""
		out = append(out, p)
	}
	c.JSON(http.StatusOK, gin.H{"profiles": out})
}

func handleRemoteProfileCreate(c *gin.Context) {
	var body struct {
		Name        string `json:"name"`
		Host        string `json:"host"`
		UserID      int64  `json:"user_id"`
		AccessToken string `json:"access_token"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.AccessToken = strings.TrimSpace(body.AccessToken)
	if body.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if body.UserID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id must be a positive integer"})
		return
	}
	if body.AccessToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "access_token is required"})
		return
	}
	host, err := normalizeHost(body.Host)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	enc, err := encryptRemoteToken(body.AccessToken)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "encrypt: " + err.Error()})
		return
	}
	now := time.Now().Unix()
	var id int64
	err = db.QueryRow(
		`INSERT INTO remote_newapi_profile (name, host, user_id, access_token_enc, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $5) RETURNING id`,
		body.Name, host, body.UserID, enc, now,
	).Scan(&id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, remoteProfile{
		ID: id, Name: body.Name, Host: host, UserID: body.UserID,
		HasToken: true, CreatedAt: now, UpdatedAt: now,
	})
}

func handleRemoteProfileUpdate(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body struct {
		Name        *string `json:"name,omitempty"`
		Host        *string `json:"host,omitempty"`
		UserID      *int64  `json:"user_id,omitempty"`
		AccessToken *string `json:"access_token,omitempty"` // empty string = leave unchanged
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	now := time.Now().Unix()
	if body.Name != nil {
		n := strings.TrimSpace(*body.Name)
		if n == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name cannot be empty"})
			return
		}
		if _, err := db.Exec(`UPDATE remote_newapi_profile SET name=$1, updated_at=$2 WHERE id=$3`, n, now, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.Host != nil {
		h, err := normalizeHost(*body.Host)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if _, err := db.Exec(`UPDATE remote_newapi_profile SET host=$1, updated_at=$2 WHERE id=$3`, h, now, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.UserID != nil {
		if *body.UserID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "user_id must be positive"})
			return
		}
		if _, err := db.Exec(`UPDATE remote_newapi_profile SET user_id=$1, updated_at=$2 WHERE id=$3`, *body.UserID, now, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.AccessToken != nil {
		t := strings.TrimSpace(*body.AccessToken)
		if t == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "access_token cannot be blanked; delete the profile to remove"})
			return
		}
		enc, err := encryptRemoteToken(t)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "encrypt: " + err.Error()})
			return
		}
		if _, err := db.Exec(`UPDATE remote_newapi_profile SET access_token_enc=$1, updated_at=$2 WHERE id=$3`, enc, now, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func handleRemoteProfileDelete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, err := db.Exec(`DELETE FROM remote_newapi_profile WHERE id=$1`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- Handler: fetch channels from a remote new-api ----

// remoteChannel is a lean projection of new-api's Channel struct. Fields
// that don't roundtrip well (nested json blobs) are omitted; used_quota
// is what we mostly care about here.
type remoteChannel struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Type        int    `json:"type"`
	Status      int    `json:"status"`
	Group       string `json:"group"`
	Tag         string `json:"tag"`
	Priority    int64  `json:"priority"`
	Weight      int64  `json:"weight"`
	Models      string `json:"models"`
	UsedQuota   int64  `json:"used_quota"`
	CreatedTime int64  `json:"created_time"`
}

// remoteChannelListResp mirrors the shape returned by new-api's
// GET /api/channel/. `success=true` + `data.items` on modern versions;
// older versions use a flat `data` array. We handle both.
type remoteChannelListResp struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// resolveProfile loads a saved profile (by id) or picks up ad-hoc creds
// from the request body. Returns host / user_id / plaintext token.
func resolveProfile(c *gin.Context, body struct {
	ProfileID   int64  `json:"profile_id,omitempty"`
	Host        string `json:"host,omitempty"`
	UserID      int64  `json:"user_id,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
	// Filters passed straight through to new-api.
	PageSize int    `json:"page_size,omitempty"`
	Group    string `json:"group,omitempty"`
	Status   string `json:"status,omitempty"`
	Type     string `json:"type,omitempty"`
}) (host string, userID int64, token string, err error) {
	if body.ProfileID > 0 {
		var enc string
		row := db.QueryRow(
			`SELECT host, user_id, access_token_enc FROM remote_newapi_profile WHERE id=$1`,
			body.ProfileID,
		)
		if err = row.Scan(&host, &userID, &enc); err != nil {
			return "", 0, "", fmt.Errorf("profile not found: %v", err)
		}
		token, err = decryptRemoteToken(enc)
		if err != nil {
			return "", 0, "", fmt.Errorf("decrypt token: %v", err)
		}
		return host, userID, token, nil
	}
	host, err = normalizeHost(body.Host)
	if err != nil {
		return "", 0, "", err
	}
	if body.UserID <= 0 {
		return "", 0, "", errors.New("user_id must be a positive integer")
	}
	if strings.TrimSpace(body.AccessToken) == "" {
		return "", 0, "", errors.New("access_token is required")
	}
	return host, body.UserID, strings.TrimSpace(body.AccessToken), nil
}

// fetchRemoteChannelPage calls one page of GET /api/channel/ on the
// remote and returns the parsed items + reported total.
func fetchRemoteChannelPage(ctx context.Context, host, token string, userID int64, page, pageSize int, filters map[string]string) ([]remoteChannel, int64, error) {
	q := url.Values{}
	q.Set("p", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(pageSize))
	q.Set("id_sort", "true")
	for k, v := range filters {
		if v != "" {
			q.Set(k, v)
		}
	}
	endpoint := host + "/api/channel/?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", token)
	req.Header.Set("New-Api-User", strconv.FormatInt(userID, 10))
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("http: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return nil, 0, fmt.Errorf("read body: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(raw))
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		return nil, 0, fmt.Errorf("remote returned %d: %s", resp.StatusCode, snippet)
	}
	var envelope remoteChannelListResp
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, 0, fmt.Errorf("decode envelope: %v", err)
	}
	if !envelope.Success {
		return nil, 0, fmt.Errorf("remote: %s", envelope.Message)
	}
	// Modern new-api returns { items: [...], total: N, page: p, page_size: n }.
	var paged struct {
		Items    []remoteChannel `json:"items"`
		Total    int64           `json:"total"`
		Page     int             `json:"page"`
		PageSize int             `json:"page_size"`
	}
	if err := json.Unmarshal(envelope.Data, &paged); err == nil && paged.Items != nil {
		return paged.Items, paged.Total, nil
	}
	// Legacy fallback: bare array.
	var arr []remoteChannel
	if err := json.Unmarshal(envelope.Data, &arr); err != nil {
		return nil, 0, fmt.Errorf("decode data: %v", err)
	}
	return arr, int64(len(arr)), nil
}

// handleRemoteFetchChannels iterates the remote's paginated channel list
// exhaustively (up to a cap) and returns the flat list. The remote may
// have thousands of channels; we cap at 5000 to avoid runaway calls.
func handleRemoteFetchChannels(c *gin.Context) {
	var body struct {
		ProfileID   int64  `json:"profile_id,omitempty"`
		Host        string `json:"host,omitempty"`
		UserID      int64  `json:"user_id,omitempty"`
		AccessToken string `json:"access_token,omitempty"`
		PageSize    int    `json:"page_size,omitempty"`
		Group       string `json:"group,omitempty"`
		Status      string `json:"status,omitempty"`
		Type        string `json:"type,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	host, userID, token, err := resolveProfile(c, body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	pageSize := body.PageSize
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 100
	}
	filters := map[string]string{
		"group":  body.Group,
		"status": body.Status,
		"type":   body.Type,
	}

	// Hard cap: never fetch more than 50 pages (5000 channels at page_size=100)
	// in a single request. Prevents accidental self-DoS when a remote has
	// tens of thousands of channels.
	const maxPages = 50
	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()

	all := make([]remoteChannel, 0)
	var total int64
	for page := 1; page <= maxPages; page++ {
		items, tot, err := fetchRemoteChannelPage(ctx, host, token, userID, page, pageSize, filters)
		if err != nil {
			// Return whatever we have so the caller can at least see partial data.
			c.JSON(http.StatusBadGateway, gin.H{
				"error":        err.Error(),
				"partial_data": all,
				"pages_fetched": page - 1,
			})
			return
		}
		total = tot
		all = append(all, items...)
		if len(items) < pageSize {
			break
		}
		// Also stop when we've caught up to the reported total (belt & suspenders
		// for remotes that return short pages).
		if total > 0 && int64(len(all)) >= total {
			break
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"channels":  all,
		"total":     total,
		"host":      host,
		"user_id":   userID,
		"truncated": len(all) < int(total) && total > 0,
	})
}
