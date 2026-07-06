package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
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
// is what we mostly care about here. QuotaUSD/Note come from the local
// remote_channel_meta table and are merged in on read.
type remoteChannel struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Type        int      `json:"type"`
	Status      int      `json:"status"`
	Group       string   `json:"group"`
	Tag         string   `json:"tag"`
	Priority    int64    `json:"priority"`
	Weight      int64    `json:"weight"`
	Models      string   `json:"models"`
	UsedQuota   int64    `json:"used_quota"`
	CreatedTime int64    `json:"created_time"`
	QuotaUSD    *float64 `json:"quota_usd,omitempty"`
	Note        string   `json:"note,omitempty"`
}

// remoteChannelMeta is the local operator-only overlay for a remote channel.
type remoteChannelMeta struct {
	QuotaUSD  *float64
	Note      string
	UpdatedAt int64
}

// loadMetaMap fetches operator metadata for a set of channels of one profile.
func loadMetaMap(profileID int64, channelIDs []int64) (map[int64]remoteChannelMeta, error) {
	out := make(map[int64]remoteChannelMeta)
	if len(channelIDs) == 0 {
		return out, nil
	}
	// Build placeholders $2..$N+1 (profile_id occupies $1) — keeps the query
	// portable to any db/sql driver that expects numbered params.
	placeholders := make([]string, 0, len(channelIDs))
	args := make([]any, 0, len(channelIDs)+1)
	args = append(args, profileID)
	for i, id := range channelIDs {
		placeholders = append(placeholders, "$"+strconv.Itoa(i+2))
		args = append(args, id)
	}
	q := `SELECT remote_channel_id, quota_usd, note, updated_at
	      FROM remote_channel_meta
	      WHERE profile_id=$1 AND remote_channel_id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var chID int64
		var quota sql.NullFloat64
		var note string
		var updatedAt int64
		if err := rows.Scan(&chID, &quota, &note, &updatedAt); err != nil {
			return nil, err
		}
		m := remoteChannelMeta{Note: note, UpdatedAt: updatedAt}
		if quota.Valid {
			v := quota.Float64
			m.QuotaUSD = &v
		}
		out[chID] = m
	}
	return out, nil
}

func upsertMeta(profileID, channelID int64, quotaUSD *float64, note string) error {
	now := time.Now().Unix()
	// Try UPDATE first; if 0 rows, INSERT. Portable to any driver.
	res, err := db.Exec(
		`UPDATE remote_channel_meta SET quota_usd=$1, note=$2, updated_at=$3
		 WHERE profile_id=$4 AND remote_channel_id=$5`,
		quotaUSD, note, now, profileID, channelID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return nil
	}
	_, err = db.Exec(
		`INSERT INTO remote_channel_meta (profile_id, remote_channel_id, quota_usd, note, updated_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		profileID, channelID, quotaUSD, note, now,
	)
	return err
}

func deleteMeta(profileID, channelID int64) error {
	_, err := db.Exec(
		`DELETE FROM remote_channel_meta WHERE profile_id=$1 AND remote_channel_id=$2`,
		profileID, channelID,
	)
	return err
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

// remoteEnvelope matches new-api's standard `{success, message, data}` shape.
type remoteEnvelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// remoteDoJSON performs an authenticated HTTP call against a remote new-api.
// On 2xx + success=true it returns the raw `data` payload. On any other
// outcome it returns a wrapped error including a snippet of the body.
func remoteDoJSON(ctx context.Context, method, host, path, token string, userID int64, query url.Values, body any) (json.RawMessage, error) {
	endpoint := host + path
	if query != nil && len(query) > 0 {
		if strings.Contains(endpoint, "?") {
			endpoint += "&" + query.Encode()
		} else {
			endpoint += "?" + query.Encode()
		}
	}
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %v", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", token)
	req.Header.Set("New-Api-User", strconv.FormatInt(userID, 10))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read body: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(raw))
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		return nil, fmt.Errorf("remote returned %d: %s", resp.StatusCode, snippet)
	}
	var env remoteEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %v", err)
	}
	if !env.Success {
		return nil, fmt.Errorf("remote: %s", env.Message)
	}
	return env.Data, nil
}

// loadRemoteProfileByID hydrates saved credentials for a profile row. Never
// returns the ciphertext, only the plaintext token.
func loadRemoteProfileByID(profileID int64) (host string, userID int64, token string, err error) {
	var enc string
	row := db.QueryRow(
		`SELECT host, user_id, access_token_enc FROM remote_newapi_profile WHERE id=$1`,
		profileID,
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

// keySha8 returns the first 8 hex chars of SHA256(key). Used as a
// deterministic, low-entropy tag we can embed in the remote channel `name`
// so we can reverse-lookup the newly created channel_id via /channel/search
// without ever transmitting the raw key over the URL.
func keySha8(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])[:8]
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
	data, err := remoteDoJSON(ctx, http.MethodGet, host, "/api/channel/", token, userID, q, nil)
	if err != nil {
		return nil, 0, err
	}
	// Modern new-api returns { items: [...], total: N, page: p, page_size: n }.
	var paged struct {
		Items    []remoteChannel `json:"items"`
		Total    int64           `json:"total"`
		Page     int             `json:"page"`
		PageSize int             `json:"page_size"`
	}
	if err := json.Unmarshal(data, &paged); err == nil && paged.Items != nil {
		return paged.Items, paged.Total, nil
	}
	// Legacy fallback: bare array.
	var arr []remoteChannel
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, 0, fmt.Errorf("decode data: %v", err)
	}
	return arr, int64(len(arr)), nil
}

// handleRemoteFetchChannels iterates the remote's paginated channel list
// exhaustively (up to a cap) and returns the flat list merged with local
// operator metadata (quota_usd/note). The remote may have thousands of
// channels; we cap at 5000 to avoid runaway calls.
// iterateRemoteChannels walks the remote's paginated /api/channel/ list to
// completion (up to 50 pages / 5000 channels — same hard cap as before).
// Returns whatever it managed to collect plus the reported total; on error
// it also returns the partial list so callers can decide what to do.
//
// Extracted from handleRemoteFetchChannels so the background snapshot loop
// can reuse the exact same pagination logic without going through gin.
func iterateRemoteChannels(ctx context.Context, host, token string, userID int64, pageSize int, filters map[string]string) ([]remoteChannel, int64, error) {
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 100
	}
	const maxPages = 50
	all := make([]remoteChannel, 0)
	var total int64
	for page := 1; page <= maxPages; page++ {
		items, tot, err := fetchRemoteChannelPage(ctx, host, token, userID, page, pageSize, filters)
		if err != nil {
			return all, total, err
		}
		total = tot
		all = append(all, items...)
		if len(items) < pageSize {
			break
		}
		if total > 0 && int64(len(all)) >= total {
			break
		}
	}
	return all, total, nil
}

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
	filters := map[string]string{
		"group":  body.Group,
		"status": body.Status,
		"type":   body.Type,
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()

	all, total, err := iterateRemoteChannels(ctx, host, token, userID, body.PageSize, filters)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error":        err.Error(),
			"partial_data": all,
		})
		return
	}

	// Merge operator meta (quota_usd/note) only for saved profiles — ad-hoc
	// requests (host+token typed in place) have no profile_id to key off.
	if body.ProfileID > 0 && len(all) > 0 {
		ids := make([]int64, len(all))
		for i, ch := range all {
			ids[i] = ch.ID
		}
		metaMap, err := loadMetaMap(body.ProfileID, ids)
		if err == nil {
			for i := range all {
				if m, ok := metaMap[all[i].ID]; ok {
					all[i].QuotaUSD = m.QuotaUSD
					all[i].Note = m.Note
				}
			}
		}
		// Interactive fetch also feeds both mirrors:
		//   • remote_channel_snapshot — time-series point for the sparkline.
		//   • remote_channel_current  — mirror of the live list so page
		//     reloads render immediately without another remote hit.
		// Both writes are async so a slow DB doesn't add HTTP latency.
		go func(pid int64, snapshot []remoteChannel) {
			if err := writeRemoteSnapshot(pid, snapshot); err != nil {
				log.Printf("[remote-snapshot] interactive write for profile %d failed: %v", pid, err)
			}
			if err := upsertRemoteCurrent(pid, snapshot); err != nil {
				log.Printf("[remote-current] interactive upsert for profile %d failed: %v", pid, err)
			}
		}(body.ProfileID, all)
	}

	c.JSON(http.StatusOK, gin.H{
		"channels":  all,
		"total":     total,
		"host":      host,
		"user_id":   userID,
		"truncated": len(all) < int(total) && total > 0,
	})
}

// ---- Snapshot persistence ----

// writeRemoteSnapshot batch-inserts one row per channel into
// remote_channel_snapshot. All rows share the same captured_at so the
// series aligns cleanly across channels.
func writeRemoteSnapshot(profileID int64, channels []remoteChannel) error {
	if profileID <= 0 || len(channels) == 0 {
		return nil
	}
	ts := time.Now().Unix()
	// Build a single multi-row INSERT to keep the write cheap. Cap the
	// per-INSERT chunk at 500 rows so the parameter budget stays sane on
	// tiny Postgres instances.
	const chunk = 500
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for start := 0; start < len(channels); start += chunk {
		end := start + chunk
		if end > len(channels) {
			end = len(channels)
		}
		batch := channels[start:end]
		args := make([]any, 0, len(batch)*5)
		valuesFrag := make([]string, 0, len(batch))
		for i, ch := range batch {
			base := i * 5
			valuesFrag = append(valuesFrag, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4, base+5))
			args = append(args, profileID, ch.ID, ts, ch.UsedQuota, ch.Status)
		}
		q := `INSERT INTO remote_channel_snapshot
		      (profile_id, remote_channel_id, captured_at, used_quota, status)
		      VALUES ` + strings.Join(valuesFrag, ",") + `
		      ON CONFLICT (profile_id, remote_channel_id, captured_at) DO NOTHING`
		if _, err := tx.Exec(q, args...); err != nil {
			return fmt.Errorf("insert snapshot: %v", err)
		}
	}
	return tx.Commit()
}

// upsertRemoteCurrent writes the full current channel state into
// remote_channel_current: one row per channel. Channels present in the
// table but absent from `channels` are deleted, so the local mirror stays
// exactly in sync with what the remote returned.
//
// Only call this on a COMPLETE fetch (all pages loaded without error) —
// otherwise a truncated result would delete rows for channels that still
// exist but weren't in the partial response.
func upsertRemoteCurrent(profileID int64, channels []remoteChannel) error {
	if profileID <= 0 {
		return nil
	}
	now := time.Now().Unix()

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// UPSERT in chunks. Each row uses 12 params.
	const chunk = 200
	for start := 0; start < len(channels); start += chunk {
		end := start + chunk
		if end > len(channels) {
			end = len(channels)
		}
		batch := channels[start:end]
		args := make([]any, 0, len(batch)*12)
		valuesFrag := make([]string, 0, len(batch))
		for i, ch := range batch {
			b := i * 12
			valuesFrag = append(valuesFrag, fmt.Sprintf(
				"($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
				b+1, b+2, b+3, b+4, b+5, b+6, b+7, b+8, b+9, b+10, b+11, b+12,
			))
			args = append(args,
				profileID, ch.ID, ch.Name, ch.Type, ch.Status,
				ch.Group, ch.Tag, ch.Priority, ch.Weight, ch.Models,
				ch.UsedQuota, ch.CreatedTime,
			)
		}
		q := `INSERT INTO remote_channel_current
		      (profile_id, remote_channel_id, name, type, status, "group", tag,
		       priority, weight, models, used_quota, created_time, updated_at)
		      VALUES ` + strings.Join(valuesFrag, ",")
		// Append updated_at as the last extra param and rewrite the trailing
		// close-paren — simpler: use a fixed updated_at column set to now via
		// COALESCE in ON CONFLICT below. To avoid two SQL variants, just
		// include updated_at in the row-level values.
		q = strings.Replace(q,
			") VALUES ",
			", updated_at) VALUES ",
			-1) // no-op — we already have updated_at in the column list
		// Instead: build columns without updated_at, then upsert with a
		// literal `updated_at=$N` where N is the last param. We tweak by
		// appending ",<now>" to each tuple. Rewrite:
		_ = q
		// Rebuild the query properly with updated_at baked into each row:
		args = args[:0]
		valuesFrag = valuesFrag[:0]
		for i, ch := range batch {
			b := i * 13
			valuesFrag = append(valuesFrag, fmt.Sprintf(
				"($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
				b+1, b+2, b+3, b+4, b+5, b+6, b+7, b+8, b+9, b+10, b+11, b+12, b+13,
			))
			args = append(args,
				profileID, ch.ID, ch.Name, ch.Type, ch.Status,
				ch.Group, ch.Tag, ch.Priority, ch.Weight, ch.Models,
				ch.UsedQuota, ch.CreatedTime, now,
			)
		}
		q = `INSERT INTO remote_channel_current
		     (profile_id, remote_channel_id, name, type, status, "group", tag,
		      priority, weight, models, used_quota, created_time, updated_at)
		     VALUES ` + strings.Join(valuesFrag, ",") + `
		     ON CONFLICT (profile_id, remote_channel_id) DO UPDATE SET
		       name         = EXCLUDED.name,
		       type         = EXCLUDED.type,
		       status       = EXCLUDED.status,
		       "group"      = EXCLUDED."group",
		       tag          = EXCLUDED.tag,
		       priority     = EXCLUDED.priority,
		       weight       = EXCLUDED.weight,
		       models       = EXCLUDED.models,
		       used_quota   = EXCLUDED.used_quota,
		       created_time = EXCLUDED.created_time,
		       updated_at   = EXCLUDED.updated_at`
		if _, err := tx.Exec(q, args...); err != nil {
			return fmt.Errorf("upsert current: %v", err)
		}
	}

	// Reconcile: delete rows for channels that are no longer on the remote.
	// Use `NOT IN (…)` since the channel-id set is small enough (<5000) to
	// fit in a single statement comfortably.
	if len(channels) > 0 {
		ids := make([]string, len(channels))
		for i, ch := range channels {
			ids[i] = strconv.FormatInt(ch.ID, 10)
		}
		q := fmt.Sprintf(
			`DELETE FROM remote_channel_current
			  WHERE profile_id=$1 AND remote_channel_id NOT IN (%s)`,
			strings.Join(ids, ","),
		)
		if _, err := tx.Exec(q, profileID); err != nil {
			return fmt.Errorf("reconcile delete: %v", err)
		}
	} else {
		// Empty result — wipe everything for this profile.
		if _, err := tx.Exec(`DELETE FROM remote_channel_current WHERE profile_id=$1`, profileID); err != nil {
			return fmt.Errorf("reconcile wipe: %v", err)
		}
	}

	return tx.Commit()
}

// remoteSnapshotIntervalDefault: 15-minute cadence gives ~96 samples/day per
// channel — enough for a smooth sparkline, cheap enough for both the DB and
// the remote (one paginated call per profile per interval).
const remoteSnapshotIntervalDefault = 15 * time.Minute
const remoteSnapshotRetentionDefault = 90 * 24 * time.Hour

// remoteSnapshotInflight guards against overlapping sync attempts for a
// given profile — if the previous fetch is still in flight (slow remote),
// don't start another. Interactive fetches from the browser bypass this
// guard entirely; only the background loop respects it.
var (
	remoteSnapshotInflightMu sync.Mutex
	remoteSnapshotInflight   = map[int64]bool{}
)

func remoteSnapshotIntervalFromEnv() time.Duration {
	if s := os.Getenv("REMOTE_SNAPSHOT_INTERVAL_SEC"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 60 {
			return time.Duration(n) * time.Second
		}
	}
	return remoteSnapshotIntervalDefault
}

func remoteSnapshotRetentionFromEnv() time.Duration {
	if s := os.Getenv("REMOTE_SNAPSHOT_RETENTION_DAYS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 1 {
			return time.Duration(n) * 24 * time.Hour
		}
	}
	return remoteSnapshotRetentionDefault
}

// startRemoteSnapshotSync spawns a background goroutine that periodically
// pulls channels from every saved profile and appends a snapshot row per
// channel. Set REMOTE_SNAPSHOT_INTERVAL_SEC=0 (or unset with a value <60)
// to disable via env; anything ≥60 sets a custom cadence.
func startRemoteSnapshotSync() {
	interval := remoteSnapshotIntervalFromEnv()
	log.Printf("[remote-snapshot] sync loop starting, interval=%s", interval)
	go func() {
		// Small stagger on startup so we don't slam every remote in the same
		// second the process comes up.
		time.Sleep(20 * time.Second)
		syncAllRemoteProfilesOnce()
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			syncAllRemoteProfilesOnce()
		}
	}()
}

// syncAllRemoteProfilesOnce iterates every profile and captures one snapshot.
// Failures on any single profile are logged and never abort the loop.
func syncAllRemoteProfilesOnce() {
	rows, err := db.Query(`SELECT id, host, user_id, access_token_enc FROM remote_newapi_profile`)
	if err != nil {
		log.Printf("[remote-snapshot] list profiles: %v", err)
		return
	}
	type prof struct {
		id     int64
		host   string
		userID int64
		enc    string
	}
	profiles := make([]prof, 0)
	for rows.Next() {
		var p prof
		if err := rows.Scan(&p.id, &p.host, &p.userID, &p.enc); err != nil {
			log.Printf("[remote-snapshot] scan profile: %v", err)
			continue
		}
		profiles = append(profiles, p)
	}
	rows.Close()

	for _, p := range profiles {
		remoteSnapshotInflightMu.Lock()
		if remoteSnapshotInflight[p.id] {
			remoteSnapshotInflightMu.Unlock()
			log.Printf("[remote-snapshot] profile %d still in flight, skipping tick", p.id)
			continue
		}
		remoteSnapshotInflight[p.id] = true
		remoteSnapshotInflightMu.Unlock()

		go func(p prof) {
			defer func() {
				remoteSnapshotInflightMu.Lock()
				delete(remoteSnapshotInflight, p.id)
				remoteSnapshotInflightMu.Unlock()
			}()
			token, err := decryptRemoteToken(p.enc)
			if err != nil {
				log.Printf("[remote-snapshot] decrypt token for profile %d: %v", p.id, err)
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			channels, _, fetchErr := iterateRemoteChannels(ctx, p.host, token, p.userID, 100, nil)
			if fetchErr != nil {
				log.Printf("[remote-snapshot] fetch profile %d (%s): %v", p.id, p.host, fetchErr)
				// Partial snapshot is still worth appending (time series
				// tolerates gaps), but DO NOT reconcile current — a
				// truncated list would delete rows for channels that
				// actually still exist upstream.
			}
			if len(channels) == 0 {
				return
			}
			if err := writeRemoteSnapshot(p.id, channels); err != nil {
				log.Printf("[remote-snapshot] write profile %d: %v", p.id, err)
			}
			if fetchErr == nil {
				if err := upsertRemoteCurrent(p.id, channels); err != nil {
					log.Printf("[remote-current] upsert profile %d: %v", p.id, err)
				}
			}
			log.Printf("[remote-snapshot] profile %d: wrote %d rows (complete=%t)", p.id, len(channels), fetchErr == nil)
		}(p)
	}
}

// startRemoteSnapshotPrune deletes snapshot rows older than the retention
// window. Runs once every 6 hours; the exact cadence isn't important as
// long as the window doesn't drift.
func startRemoteSnapshotPrune() {
	retention := remoteSnapshotRetentionFromEnv()
	log.Printf("[remote-snapshot] prune loop starting, retention=%s", retention)
	go func() {
		// Delay first prune so schema init has clearly finished.
		time.Sleep(2 * time.Minute)
		for {
			cutoff := time.Now().Add(-retention).Unix()
			res, err := db.Exec(`DELETE FROM remote_channel_snapshot WHERE captured_at < $1`, cutoff)
			if err != nil {
				log.Printf("[remote-snapshot] prune: %v", err)
			} else if n, _ := res.RowsAffected(); n > 0 {
				log.Printf("[remote-snapshot] pruned %d rows older than %s", n, time.Unix(cutoff, 0).UTC().Format(time.RFC3339))
			}
			time.Sleep(6 * time.Hour)
		}
	}()
}

// ---- Handler: snapshot history query ----

// handleRemoteSnapshotHistory returns the time series for either one
// specific channel (profile_id + channel_id + since) or all channels of a
// profile at a coarser aggregation (used by the "total burn" chart). Keep
// the API narrow — sparkline / bulk shape only.
// handleRemoteCachedChannels reads the locally-mirrored channel list from
// remote_channel_current (kept fresh by the sync loop + interactive fetch).
// No remote call — this is what powers the "refresh brings you back to
// where you were" behaviour.
//
// Returns the same channel shape as handleRemoteFetchChannels, plus a
// `cached_at` epoch (the freshest updated_at across all rows for the
// profile) so the UI can label the view as "cached · N min ago".
func handleRemoteCachedChannels(c *gin.Context) {
	profileID, err := strconv.ParseInt(c.Query("profile_id"), 10, 64)
	if err != nil || profileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	rows, err := db.Query(
		`SELECT remote_channel_id, name, type, status, "group", tag,
		        priority, weight, models, used_quota, created_time, updated_at
		   FROM remote_channel_current
		  WHERE profile_id = $1
		  ORDER BY remote_channel_id ASC`,
		profileID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	all := make([]remoteChannel, 0)
	var cachedAt int64
	for rows.Next() {
		var ch remoteChannel
		var updatedAt int64
		if err := rows.Scan(
			&ch.ID, &ch.Name, &ch.Type, &ch.Status, &ch.Group, &ch.Tag,
			&ch.Priority, &ch.Weight, &ch.Models, &ch.UsedQuota, &ch.CreatedTime, &updatedAt,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if updatedAt > cachedAt {
			cachedAt = updatedAt
		}
		all = append(all, ch)
	}

	// Merge operator meta (quota_usd / note).
	if len(all) > 0 {
		ids := make([]int64, len(all))
		for i, ch := range all {
			ids[i] = ch.ID
		}
		metaMap, err := loadMetaMap(profileID, ids)
		if err == nil {
			for i := range all {
				if m, ok := metaMap[all[i].ID]; ok {
					all[i].QuotaUSD = m.QuotaUSD
					all[i].Note = m.Note
				}
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"channels":  all,
		"total":     int64(len(all)),
		"cached_at": cachedAt,
		"cached":    true,
	})
}

func handleRemoteSnapshotHistory(c *gin.Context) {
	profileID, err := strconv.ParseInt(c.Query("profile_id"), 10, 64)
	if err != nil || profileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	// since: epoch seconds. Default 24h back.
	since, _ := strconv.ParseInt(c.Query("since"), 10, 64)
	if since <= 0 {
		since = time.Now().Add(-24 * time.Hour).Unix()
	}

	channelIDStr := c.Query("channel_id")
	if channelIDStr != "" {
		channelID, err := strconv.ParseInt(channelIDStr, 10, 64)
		if err != nil || channelID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid channel_id"})
			return
		}
		rows, err := db.Query(
			`SELECT captured_at, used_quota, status
			   FROM remote_channel_snapshot
			  WHERE profile_id=$1 AND remote_channel_id=$2 AND captured_at>=$3
			  ORDER BY captured_at ASC`,
			profileID, channelID, since,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		defer rows.Close()
		type point struct {
			CapturedAt int64 `json:"captured_at"`
			UsedQuota  int64 `json:"used_quota"`
			Status     int   `json:"status"`
		}
		out := make([]point, 0)
		for rows.Next() {
			var p point
			if err := rows.Scan(&p.CapturedAt, &p.UsedQuota, &p.Status); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			out = append(out, p)
		}
		c.JSON(http.StatusOK, gin.H{"channel_id": channelID, "points": out})
		return
	}

	// Bulk mode: latest snapshot per (profile, channel) since `since`, so the
	// UI can show a per-row Δ quickly. Uses DISTINCT ON — Postgres-specific
	// but this service is Postgres-only anyway.
	rows, err := db.Query(
		`SELECT DISTINCT ON (remote_channel_id)
		        remote_channel_id, captured_at, used_quota
		   FROM remote_channel_snapshot
		  WHERE profile_id=$1 AND captured_at>=$2
		  ORDER BY remote_channel_id, captured_at DESC`,
		profileID, since,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	byID := make(map[int64]map[string]int64)
	for rows.Next() {
		var cid, ts, quota int64
		if err := rows.Scan(&cid, &ts, &quota); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		byID[cid] = map[string]int64{"captured_at": ts, "used_quota": quota}
	}
	c.JSON(http.StatusOK, gin.H{"latest": byID})
}

// ---- Handler: single-channel GET (fresh used_quota after edit) ----

// handleRemoteChannelGet returns one channel from the remote, plus its local
// operator meta. Handy for refreshing a single row after an edit without
// re-pulling the whole list.
func handleRemoteChannelGet(c *gin.Context) {
	profileID, err := strconv.ParseInt(c.Query("profile_id"), 10, 64)
	if err != nil || profileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	channelID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || channelID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid channel id"})
		return
	}
	host, userID, token, err := loadRemoteProfileByID(profileID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	data, err := remoteDoJSON(ctx, http.MethodGet, host, "/api/channel/"+strconv.FormatInt(channelID, 10), token, userID, nil, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var ch remoteChannel
	if err := json.Unmarshal(data, &ch); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "decode: " + err.Error()})
		return
	}
	if metaMap, err := loadMetaMap(profileID, []int64{ch.ID}); err == nil {
		if m, ok := metaMap[ch.ID]; ok {
			ch.QuotaUSD = m.QuotaUSD
			ch.Note = m.Note
		}
	}
	c.JSON(http.StatusOK, gin.H{"channel": ch})
}

// ---- Handler: batch-create channels on the remote ----

// remoteChannelCreateItem is one entry in the batch upload payload.
type remoteChannelCreateItem struct {
	Key      string   `json:"key"`
	QuotaUSD *float64 `json:"quota_usd,omitempty"`
	Note     string   `json:"note,omitempty"`
}

// batchCreateResult mirrors what the frontend renders per key.
type batchCreateResult struct {
	Key       string `json:"key"`                 // last 8 chars only
	OK        bool   `json:"ok"`
	ChannelID int64  `json:"channel_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Error     string `json:"error,omitempty"`
}

// handleRemoteChannelCreate uploads a batch of keys to the remote new-api as
// individual channels (mode=single). Each channel gets a deterministic name
// suffix (sha8(key)) so we can reverse-lookup the newly assigned id via
// GET /api/channel/search?keyword=<sha8>. Any per-key operator meta
// (quota_usd, note) is stored locally against the returned channel id.
func handleRemoteChannelCreate(c *gin.Context) {
	var body struct {
		ProfileID  int64                     `json:"profile_id"`
		NamePrefix string                    `json:"name_prefix"`
		Type       int                       `json:"type"`
		Models     string                    `json:"models"`
		Group      string                    `json:"group"`
		Tag        string                    `json:"tag,omitempty"`
		Priority   int64                     `json:"priority,omitempty"`
		BaseURL    string                    `json:"base_url,omitempty"`
		Items      []remoteChannelCreateItem `json:"items"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.ProfileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	if strings.TrimSpace(body.NamePrefix) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name_prefix is required"})
		return
	}
	if len(body.Items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no items provided"})
		return
	}
	if body.Type == 0 {
		body.Type = 14 // Anthropic default in this project
	}
	if strings.TrimSpace(body.Models) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "models is required"})
		return
	}
	if strings.TrimSpace(body.Group) == "" {
		body.Group = "default"
	}
	// Enforce a per-request cap so a runaway UI can't submit thousands.
	const maxItems = 200
	if len(body.Items) > maxItems {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("too many items (max %d)", maxItems)})
		return
	}

	host, userID, token, err := loadRemoteProfileByID(body.ProfileID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Give the whole batch a generous window; each item has its own timeout
	// inside remoteDoJSON.
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(60+len(body.Items)*10)*time.Second)
	defer cancel()

	results := make([]batchCreateResult, 0, len(body.Items))
	for _, it := range body.Items {
		key := strings.TrimSpace(it.Key)
		masked := maskKey(key)
		if key == "" {
			results = append(results, batchCreateResult{Key: masked, OK: false, Error: "empty key"})
			continue
		}
		sha := keySha8(key)
		name := body.NamePrefix + "-" + sha

		// Build the channel payload. Fields not sent stay as new-api defaults.
		channelBody := gin.H{
			"type":         body.Type,
			"key":          key,
			"name":         name,
			"status":       1,
			"models":       body.Models,
			"group":        body.Group,
			"priority":     body.Priority,
			"weight":       0,
			"created_time": time.Now().Unix(),
			"channel_info": gin.H{
				"is_multi_key":            false,
				"multi_key_size":          0,
				"multi_key_status_list":   nil,
				"multi_key_polling_index": 0,
				"multi_key_mode":          "",
			},
		}
		if body.Tag != "" {
			channelBody["tag"] = body.Tag
		}
		if body.BaseURL != "" {
			channelBody["base_url"] = body.BaseURL
		}
		payload := gin.H{
			"mode":    "single",
			"channel": channelBody,
		}
		if _, err := remoteDoJSON(ctx, http.MethodPost, host, "/api/channel/", token, userID, nil, payload); err != nil {
			results = append(results, batchCreateResult{Key: masked, OK: false, Error: err.Error()})
			continue
		}

		// Reverse-lookup: search by the sha8 tag we embedded in the name.
		// remoteDoJSON strips the envelope; response `data` is a bare array.
		q := url.Values{}
		q.Set("keyword", sha)
		q.Set("group", body.Group)
		data, err := remoteDoJSON(ctx, http.MethodGet, host, "/api/channel/search", token, userID, q, nil)
		if err != nil {
			// Channel was created but we lost the id. Report partial success so
			// the operator can retry / manual-fix.
			results = append(results, batchCreateResult{Key: masked, OK: false, Name: name, Error: "created but search failed: " + err.Error()})
			continue
		}
		var hits []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(data, &hits); err != nil {
			results = append(results, batchCreateResult{Key: masked, OK: false, Name: name, Error: "created but decode search failed"})
			continue
		}
		var matchedID int64
		for _, h := range hits {
			if strings.Contains(h.Name, sha) {
				if h.ID > matchedID {
					matchedID = h.ID // newest wins if the sha collides across old rows
				}
			}
		}
		if matchedID == 0 {
			results = append(results, batchCreateResult{Key: masked, OK: false, Name: name, Error: "created but reverse-lookup returned no match"})
			continue
		}

		// Persist operator meta locally. Best-effort — a DB hiccup here doesn't
		// invalidate the remote create.
		if it.QuotaUSD != nil || strings.TrimSpace(it.Note) != "" {
			_ = upsertMeta(body.ProfileID, matchedID, it.QuotaUSD, strings.TrimSpace(it.Note))
		}
		results = append(results, batchCreateResult{Key: masked, OK: true, ChannelID: matchedID, Name: name})
	}

	ok := 0
	for _, r := range results {
		if r.OK {
			ok++
		}
	}
	c.JSON(http.StatusOK, gin.H{"results": results, "ok": ok, "total": len(results)})
}

// maskKey returns "…" + last 8 chars, matching the report page convention.
func maskKey(k string) string {
	k = strings.TrimSpace(k)
	if len(k) <= 8 {
		return k
	}
	return "…" + k[len(k)-8:]
}

// ---- Handler: update one channel (name/tag/status/priority/group + meta) ----

func handleRemoteChannelUpdate(c *gin.Context) {
	var body struct {
		ProfileID int64    `json:"profile_id"`
		ChannelID int64    `json:"channel_id"`
		Name      *string  `json:"name,omitempty"`
		Tag       *string  `json:"tag,omitempty"`
		Status    *int     `json:"status,omitempty"`
		Priority  *int64   `json:"priority,omitempty"`
		Group     *string  `json:"group,omitempty"`
		Models    *string  `json:"models,omitempty"`
		QuotaUSD  *float64 `json:"quota_usd,omitempty"`
		Note      *string  `json:"note,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.ProfileID <= 0 || body.ChannelID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id and channel_id are required"})
		return
	}
	host, userID, token, err := loadRemoteProfileByID(body.ProfileID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Any remote-side change? Only call PUT if at least one channel-level
	// field was provided.
	remoteChanged := body.Name != nil || body.Tag != nil || body.Status != nil ||
		body.Priority != nil || body.Group != nil || body.Models != nil

	if remoteChanged {
		// PUT /api/channel/ expects the full Channel struct. Fetch first so
		// we can preserve fields we're not editing (avoids blanking type/key/etc).
		ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
		defer cancel()
		data, err := remoteDoJSON(ctx, http.MethodGet, host, "/api/channel/"+strconv.FormatInt(body.ChannelID, 10), token, userID, nil, nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "fetch current: " + err.Error()})
			return
		}
		// Decode into a generic map so unknown/nested fields (channel_info,
		// settings, etc.) round-trip untouched.
		var current map[string]any
		if err := json.Unmarshal(data, &current); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "decode current: " + err.Error()})
			return
		}
		if body.Name != nil {
			current["name"] = *body.Name
		}
		if body.Tag != nil {
			current["tag"] = *body.Tag
		}
		if body.Status != nil {
			current["status"] = *body.Status
		}
		if body.Priority != nil {
			current["priority"] = *body.Priority
		}
		if body.Group != nil {
			current["group"] = *body.Group
		}
		if body.Models != nil {
			current["models"] = *body.Models
		}
		if _, err := remoteDoJSON(ctx, http.MethodPut, host, "/api/channel/", token, userID, nil, current); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "update: " + err.Error()})
			return
		}
	}

	// Local meta. Only touch if either field is present.
	if body.QuotaUSD != nil || body.Note != nil {
		// Merge onto existing meta so we don't clobber a field the caller
		// didn't send.
		var quotaUSD *float64
		var note string
		if metaMap, err := loadMetaMap(body.ProfileID, []int64{body.ChannelID}); err == nil {
			if m, ok := metaMap[body.ChannelID]; ok {
				quotaUSD = m.QuotaUSD
				note = m.Note
			}
		}
		if body.QuotaUSD != nil {
			quotaUSD = body.QuotaUSD
		}
		if body.Note != nil {
			note = strings.TrimSpace(*body.Note)
		}
		if err := upsertMeta(body.ProfileID, body.ChannelID, quotaUSD, note); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "save meta: " + err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- Handler: delete one channel on the remote + purge local meta ----

func handleRemoteChannelDelete(c *gin.Context) {
	profileID, err := strconv.ParseInt(c.Query("profile_id"), 10, 64)
	if err != nil || profileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	channelID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || channelID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid channel id"})
		return
	}
	host, userID, token, err := loadRemoteProfileByID(profileID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	if _, err := remoteDoJSON(ctx, http.MethodDelete, host, "/api/channel/"+strconv.FormatInt(channelID, 10), token, userID, nil, nil); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	// Best-effort meta cleanup; a leftover row is harmless but noisy.
	_ = deleteMeta(profileID, channelID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- Handler: test one key against Anthropic directly ----

// handleRemoteTestKey reuses testSingleKey which hits Anthropic directly and
// does not touch the remote new-api at all. Kept under /remote-newapi/* so
// the Remote Channels page can wire it up naturally.
func handleRemoteTestKey(c *gin.Context) {
	var body struct {
		Key   string `json:"key"`
		Model string `json:"model"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	key := strings.TrimSpace(body.Key)
	model := strings.TrimSpace(body.Model)
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key is required"})
		return
	}
	if !supportedTestModels[model] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported model"})
		return
	}
	res := testSingleKey(key, model)
	// The mask matches other endpoints; never echo the raw key back.
	res.Key = maskKey(key)
	c.JSON(http.StatusOK, res)
}

// ---- Handler: last-hour cost per channel (with 5-min in-memory cache) ----

type lastHourEntry struct {
	quota    int64
	fetched  time.Time
}

var (
	lastHourCache   = make(map[string]lastHourEntry)
	lastHourCacheMu sync.Mutex
)

const lastHourTTL = 5 * time.Minute

// handleRemoteChannelLastHour returns quota (raw units) per channel for the
// last hour, computed from the remote's GET /api/log/stat endpoint. Cached
// per (profile_id, channel_id) for 5 minutes so refreshing the list doesn't
// hammer the remote.
func handleRemoteChannelLastHour(c *gin.Context) {
	var body struct {
		ProfileID  int64   `json:"profile_id"`
		ChannelIDs []int64 `json:"channel_ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.ProfileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	if len(body.ChannelIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"data": map[string]int64{}})
		return
	}
	const maxIDs = 200
	if len(body.ChannelIDs) > maxIDs {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("too many channel_ids (max %d)", maxIDs)})
		return
	}
	host, userID, token, err := loadRemoteProfileByID(body.ProfileID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	now := time.Now()
	oneHourAgo := now.Add(-time.Hour)

	out := make(map[string]int64, len(body.ChannelIDs))
	for _, chID := range body.ChannelIDs {
		cacheKey := strconv.FormatInt(body.ProfileID, 10) + ":" + strconv.FormatInt(chID, 10)
		lastHourCacheMu.Lock()
		entry, ok := lastHourCache[cacheKey]
		lastHourCacheMu.Unlock()
		if ok && now.Sub(entry.fetched) < lastHourTTL {
			out[strconv.FormatInt(chID, 10)] = entry.quota
			continue
		}
		q := url.Values{}
		q.Set("type", "2") // consume logs only
		q.Set("channel", strconv.FormatInt(chID, 10))
		q.Set("start_timestamp", strconv.FormatInt(oneHourAgo.Unix(), 10))
		q.Set("end_timestamp", strconv.FormatInt(now.Unix(), 10))
		data, err := remoteDoJSON(ctx, http.MethodGet, host, "/api/log/stat", token, userID, q, nil)
		if err != nil {
			// One channel failing shouldn't block the rest — record 0 and press on.
			out[strconv.FormatInt(chID, 10)] = 0
			continue
		}
		var stat struct {
			Quota int64 `json:"quota"`
		}
		if err := json.Unmarshal(data, &stat); err != nil {
			out[strconv.FormatInt(chID, 10)] = 0
			continue
		}
		out[strconv.FormatInt(chID, 10)] = stat.Quota
		lastHourCacheMu.Lock()
		lastHourCache[cacheKey] = lastHourEntry{quota: stat.Quota, fetched: now}
		lastHourCacheMu.Unlock()
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}
