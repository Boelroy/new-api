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
	"regexp"
	"sort"
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
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Host            string `json:"host"`
	UserID          int64  `json:"user_id"`
	HasToken        bool   `json:"has_token"` // token never returned; UI shows only whether set
	DefaultModels   string `json:"default_models"`
	DefaultGroup    string `json:"default_group"`
	PoolIntervalSec int    `json:"pool_interval_sec"`
	PoolBatchSize   int    `json:"pool_batch_size"`
	AutoMode        bool   `json:"auto_mode"`
	RPMBase         int    `json:"rpm_base"`
	RPMMin          int    `json:"rpm_min"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

// remoteProfilePublic is the studio-operator view of a profile: name and
// batch-upload defaults only. Fields the operator must not see (host,
// user_id, has_token, pool tuning knobs) are omitted from the JSON. Used
// by handleRemoteProfileList when the caller is not super_admin.
type remoteProfilePublic struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	DefaultModels string `json:"default_models"`
	DefaultGroup  string `json:"default_group"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

// Pool tuning safety bounds. Interval too small hammers the remote;
// batch too big undoes the "serial drip" invariant of the pool. Applied
// on create/update.
const (
	poolIntervalSecMin = 5
	poolIntervalSecMax = 3600
	poolBatchSizeMin   = 1
	poolBatchSizeMax   = 50
	poolIntervalSecDef = 60
	poolBatchSizeDef   = 2
	rpmBaseMin         = 1
	rpmBaseMax         = 100000
	rpmBaseDef         = 150
	rpmMinDef          = 50
	rpmMinMax          = 100000
)

func clampPoolInterval(v int) int {
	if v <= 0 {
		return poolIntervalSecDef
	}
	if v < poolIntervalSecMin {
		return poolIntervalSecMin
	}
	if v > poolIntervalSecMax {
		return poolIntervalSecMax
	}
	return v
}

func clampPoolBatchSize(v int) int {
	if v <= 0 {
		return poolBatchSizeDef
	}
	if v < poolBatchSizeMin {
		return poolBatchSizeMin
	}
	if v > poolBatchSizeMax {
		return poolBatchSizeMax
	}
	return v
}

func clampRPMBase(v int) int {
	if v <= 0 {
		return rpmBaseDef
	}
	if v < rpmBaseMin {
		return rpmBaseMin
	}
	if v > rpmBaseMax {
		return rpmBaseMax
	}
	return v
}

// rpm_min = 0 is legal (upload as soon as anything moves). Only clamp
// negatives and absurdly large values.
func clampRPMMin(v int) int {
	if v < 0 {
		return 0
	}
	if v > rpmMinMax {
		return rpmMinMax
	}
	return v
}

// callerIsStudioOperator reports whether the JWT claim role for the
// current request equals the studio-operator tier. Kept alongside route
// helpers in main.go (requireRoleOrStudioOperator) so both sides read the
// same claim shape.
func callerIsStudioOperator(c *gin.Context) bool {
	if v, ok := c.Get("role"); ok {
		if r, ok := v.(int); ok {
			return r == minStudioOperatorRole
		}
	}
	return false
}

// callerStudio returns the studio JWT claim, trimmed. Empty means the
// account isn't bound to any studio yet.
func callerStudio(c *gin.Context) string {
	if v, ok := c.Get("studio"); ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
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
		`SELECT id, name, host, user_id, access_token_enc,
		        default_models, default_group,
		        pool_interval_sec, pool_batch_size,
		        auto_mode, rpm_base, rpm_min,
		        created_at, updated_at
		 FROM remote_newapi_profile ORDER BY name ASC`,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	// Studio operators never learn the host / user_id / has_token / pool
	// tuning — they only need a name to pick a profile in the batch-upload
	// modal. Emit the stripped shape so those fields aren't even present
	// as empty strings in the JSON.
	stripped := callerIsStudioOperator(c)
	full := make([]remoteProfile, 0)
	slim := make([]remoteProfilePublic, 0)
	for rows.Next() {
		var p remoteProfile
		var enc string
		if err := rows.Scan(&p.ID, &p.Name, &p.Host, &p.UserID, &enc,
			&p.DefaultModels, &p.DefaultGroup,
			&p.PoolIntervalSec, &p.PoolBatchSize,
			&p.AutoMode, &p.RPMBase, &p.RPMMin,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		p.HasToken = enc != ""
		if stripped {
			slim = append(slim, remoteProfilePublic{
				ID:            p.ID,
				Name:          p.Name,
				DefaultModels: p.DefaultModels,
				DefaultGroup:  p.DefaultGroup,
				CreatedAt:     p.CreatedAt,
				UpdatedAt:     p.UpdatedAt,
			})
			continue
		}
		full = append(full, p)
	}
	if stripped {
		c.JSON(http.StatusOK, gin.H{"profiles": slim})
		return
	}
	c.JSON(http.StatusOK, gin.H{"profiles": full})
}

func handleRemoteProfileCreate(c *gin.Context) {
	var body struct {
		Name            string `json:"name"`
		Host            string `json:"host"`
		UserID          int64  `json:"user_id"`
		AccessToken     string `json:"access_token"`
		DefaultModels   string `json:"default_models"`
		DefaultGroup    string `json:"default_group"`
		PoolIntervalSec int    `json:"pool_interval_sec"`
		PoolBatchSize   int    `json:"pool_batch_size"`
		AutoMode        *bool  `json:"auto_mode,omitempty"`
		RPMBase         int    `json:"rpm_base"`
		RPMMin          int    `json:"rpm_min"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.AccessToken = strings.TrimSpace(body.AccessToken)
	body.DefaultModels = strings.TrimSpace(body.DefaultModels)
	body.DefaultGroup = strings.TrimSpace(body.DefaultGroup)
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
	// Missing pool tuning values fall back to the schema default via the
	// same clamp — keeps create + update paths converging on the same
	// safety bounds without duplicating the constants.
	poolInterval := clampPoolInterval(body.PoolIntervalSec)
	poolBatch := clampPoolBatchSize(body.PoolBatchSize)
	rpmBase := clampRPMBase(body.RPMBase)
	rpmMin := clampRPMMin(body.RPMMin)
	autoMode := false
	if body.AutoMode != nil {
		autoMode = *body.AutoMode
	}
	enc, err := encryptRemoteToken(body.AccessToken)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "encrypt: " + err.Error()})
		return
	}
	now := time.Now().Unix()
	var id int64
	err = db.QueryRow(
		`INSERT INTO remote_newapi_profile
		 (name, host, user_id, access_token_enc, default_models, default_group,
		  pool_interval_sec, pool_batch_size,
		  auto_mode, rpm_base, rpm_min,
		  created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12) RETURNING id`,
		body.Name, host, body.UserID, enc, body.DefaultModels, body.DefaultGroup,
		poolInterval, poolBatch,
		autoMode, rpmBase, rpmMin, now,
	).Scan(&id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, remoteProfile{
		ID: id, Name: body.Name, Host: host, UserID: body.UserID,
		HasToken: true, DefaultModels: body.DefaultModels, DefaultGroup: body.DefaultGroup,
		PoolIntervalSec: poolInterval, PoolBatchSize: poolBatch,
		AutoMode: autoMode, RPMBase: rpmBase, RPMMin: rpmMin,
		CreatedAt: now, UpdatedAt: now,
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
		Name            *string `json:"name,omitempty"`
		Host            *string `json:"host,omitempty"`
		UserID          *int64  `json:"user_id,omitempty"`
		AccessToken     *string `json:"access_token,omitempty"` // empty string = leave unchanged
		DefaultModels   *string `json:"default_models,omitempty"`
		DefaultGroup    *string `json:"default_group,omitempty"`
		PoolIntervalSec *int    `json:"pool_interval_sec,omitempty"`
		PoolBatchSize   *int    `json:"pool_batch_size,omitempty"`
		AutoMode        *bool   `json:"auto_mode,omitempty"`
		RPMBase         *int    `json:"rpm_base,omitempty"`
		RPMMin          *int    `json:"rpm_min,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	now := time.Now().Unix()
	if body.DefaultModels != nil {
		if _, err := db.Exec(
			`UPDATE remote_newapi_profile SET default_models=$1, updated_at=$2 WHERE id=$3`,
			strings.TrimSpace(*body.DefaultModels), now, id,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.DefaultGroup != nil {
		if _, err := db.Exec(
			`UPDATE remote_newapi_profile SET default_group=$1, updated_at=$2 WHERE id=$3`,
			strings.TrimSpace(*body.DefaultGroup), now, id,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
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
	if body.PoolIntervalSec != nil {
		v := clampPoolInterval(*body.PoolIntervalSec)
		if _, err := db.Exec(`UPDATE remote_newapi_profile SET pool_interval_sec=$1, updated_at=$2 WHERE id=$3`, v, now, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.PoolBatchSize != nil {
		v := clampPoolBatchSize(*body.PoolBatchSize)
		if _, err := db.Exec(`UPDATE remote_newapi_profile SET pool_batch_size=$1, updated_at=$2 WHERE id=$3`, v, now, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.AutoMode != nil {
		if _, err := db.Exec(`UPDATE remote_newapi_profile SET auto_mode=$1, updated_at=$2 WHERE id=$3`, *body.AutoMode, now, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.RPMBase != nil {
		v := clampRPMBase(*body.RPMBase)
		if _, err := db.Exec(`UPDATE remote_newapi_profile SET rpm_base=$1, updated_at=$2 WHERE id=$3`, v, now, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.RPMMin != nil {
		v := clampRPMMin(*body.RPMMin)
		if _, err := db.Exec(`UPDATE remote_newapi_profile SET rpm_min=$1, updated_at=$2 WHERE id=$3`, v, now, id); err != nil {
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
	ID                 int64    `json:"id"`
	Name               string   `json:"name"`
	Type               int      `json:"type"`
	Status             int      `json:"status"`
	Group              string   `json:"group"`
	Tag                string   `json:"tag"`
	Priority           int64    `json:"priority"`
	Weight             int64    `json:"weight"`
	Models             string   `json:"models"`
	UsedQuota          int64    `json:"used_quota"`
	CreatedTime        int64    `json:"created_time"`
	QuotaUSD           *float64 `json:"quota_usd,omitempty"`
	UnitPriceCNY       *float64 `json:"unit_price_cny,omitempty"`
	DownstreamCNY      *float64 `json:"downstream_cny,omitempty"`      // latest configured
	DownstreamCNYDate  string   `json:"downstream_cny_date,omitempty"` // date the latest was set for
	Note               string   `json:"note,omitempty"`
}

// remoteChannelMeta is the local operator-only overlay for a remote channel.
type remoteChannelMeta struct {
	QuotaUSD     *float64
	UnitPriceCNY *float64
	Note         string
	UpdatedAt    int64
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
	q := `SELECT remote_channel_id, quota_usd, unit_price_cny, note, updated_at
	      FROM remote_channel_meta
	      WHERE profile_id=$1 AND remote_channel_id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var chID int64
		var quota, price sql.NullFloat64
		var note string
		var updatedAt int64
		if err := rows.Scan(&chID, &quota, &price, &note, &updatedAt); err != nil {
			return nil, err
		}
		m := remoteChannelMeta{Note: note, UpdatedAt: updatedAt}
		if quota.Valid {
			v := quota.Float64
			m.QuotaUSD = &v
		}
		if price.Valid {
			v := price.Float64
			m.UnitPriceCNY = &v
		}
		out[chID] = m
	}
	return out, nil
}

// loadLatestDownstream picks the most recent downstream_cny row per
// (profile, channel) — i.e. yesterday's price still counts if today
// hasn't been set. Empty input → empty output.
func loadLatestDownstream(profileID int64, channelIDs []int64) (map[int64]struct {
	value float64
	date  string
}, error) {
	out := make(map[int64]struct {
		value float64
		date  string
	})
	if len(channelIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, 0, len(channelIDs))
	args := make([]any, 0, len(channelIDs)+1)
	args = append(args, profileID)
	for i, id := range channelIDs {
		placeholders = append(placeholders, "$"+strconv.Itoa(i+2))
		args = append(args, id)
	}
	// DISTINCT ON grabs the max date per channel in one pass; Postgres-only
	// but this service is Postgres-only anyway.
	q := `SELECT DISTINCT ON (remote_channel_id) remote_channel_id, downstream_cny, date
	        FROM remote_channel_downstream
	       WHERE profile_id = $1
	         AND remote_channel_id IN (` + strings.Join(placeholders, ",") + `)
	       ORDER BY remote_channel_id, date DESC`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var chID int64
		var val float64
		var date string
		if err := rows.Scan(&chID, &val, &date); err != nil {
			return nil, err
		}
		out[chID] = struct {
			value float64
			date  string
		}{val, date}
	}
	return out, nil
}

// upsertMeta writes only the fields the caller cared about, leaving the
// others untouched. Pass nil for a pointer field to preserve the existing
// value; note is likewise only overwritten when non-empty.
func upsertMeta(profileID, channelID int64, quotaUSD, unitPriceCNY *float64, note string) error {
	now := time.Now().Unix()
	// Ensure the row exists first — subsequent updates are then column-scoped.
	if _, err := db.Exec(
		`INSERT INTO remote_channel_meta (profile_id, remote_channel_id, quota_usd, unit_price_cny, note, updated_at)
		 VALUES ($1, $2, NULL, NULL, '', $3)
		 ON CONFLICT (profile_id, remote_channel_id) DO NOTHING`,
		profileID, channelID, now,
	); err != nil {
		return err
	}
	if quotaUSD != nil {
		if _, err := db.Exec(
			`UPDATE remote_channel_meta SET quota_usd=$1, updated_at=$2
			  WHERE profile_id=$3 AND remote_channel_id=$4`,
			*quotaUSD, now, profileID, channelID,
		); err != nil {
			return err
		}
	}
	if unitPriceCNY != nil {
		if _, err := db.Exec(
			`UPDATE remote_channel_meta SET unit_price_cny=$1, updated_at=$2
			  WHERE profile_id=$3 AND remote_channel_id=$4`,
			*unitPriceCNY, now, profileID, channelID,
		); err != nil {
			return err
		}
	}
	if note != "" {
		if _, err := db.Exec(
			`UPDATE remote_channel_meta SET note=$1, updated_at=$2
			  WHERE profile_id=$3 AND remote_channel_id=$4`,
			note, now, profileID, channelID,
		); err != nil {
			return err
		}
	}
	return nil
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

// channelKeyTail returns the last `n` alphanumeric chars of a key, meant to
// be embedded in the human-readable channel name. Non-alphanumeric chars
// (dashes, plus, equals from base64-ish encodings) are dropped so the tail
// stays safe to substring-search on. Returns the whole key when it has
// fewer than n alphanumeric chars.
func channelKeyTail(key string, n int) string {
	if n <= 0 {
		return ""
	}
	cleaned := make([]byte, 0, len(key))
	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			cleaned = append(cleaned, c)
		}
	}
	if len(cleaned) <= n {
		return string(cleaned)
	}
	return string(cleaned[len(cleaned)-n:])
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
					all[i].UnitPriceCNY = m.UnitPriceCNY
					all[i].Note = m.Note
				}
			}
		}
		if dsMap, err := loadLatestDownstream(body.ProfileID, ids); err == nil {
			for i := range all {
				if d, ok := dsMap[all[i].ID]; ok {
					v := d.value
					all[i].DownstreamCNY = &v
					all[i].DownstreamCNYDate = d.date
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

// ---- Scheduled uploads (queue + drip pool) ----

// pendingKeyRow mirrors one row of remote_pending_key. Kept private —
// the encrypted `key_encrypted` column never crosses the API boundary.
type pendingKeyRow struct {
	id           int64
	profileID    int64
	quotaUSD     float64
	note         string
	namePrefix   string
	group        string
	tag          string
	models       string
	priority     int64
	poolSize     int
	status       string
	remoteChID   int64
	attempts     int
	failedReason string
	createdAt    int64
	updatedAt    int64
}

// pendingKeyView is the JSON-safe projection returned to the frontend.
// key is masked to "…" + last 8 alphanumeric chars so a screenshot of the
// queue doesn't leak upstream credentials.
type pendingKeyView struct {
	ID           int64   `json:"id"`
	ProfileID    int64   `json:"profile_id"`
	KeyMasked    string  `json:"key_masked"`
	QuotaUSD     float64 `json:"quota_usd"`
	Note         string  `json:"note"`
	NamePrefix   string  `json:"name_prefix"`
	Group        string  `json:"group"`
	Tag          string  `json:"tag"`
	Models       string  `json:"models"`
	Priority     int64   `json:"priority"`
	PoolSize     int     `json:"pool_size"`
	Status       string  `json:"status"`
	RemoteChID   int64   `json:"remote_channel_id"`
	Attempts     int     `json:"attempts"`
	FailedReason string  `json:"failed_reason,omitempty"`
	CreatedAt    int64   `json:"created_at"`
	UpdatedAt    int64   `json:"updated_at"`
}

func pendingKeyHash(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// handlePendingKeyEnqueue accepts a batch of keys and stages them into
// remote_pending_key. pool_size=0 uploads immediately on the next
// scheduler tick, >0 respects that many concurrently active. Dedupes by
// (profile_id, key_hash) — a resubmitted key is a no-op.
func handlePendingKeyEnqueue(c *gin.Context) {
	var body struct {
		ProfileID  int64  `json:"profile_id"`
		NamePrefix string `json:"name_prefix"`
		Group      string `json:"group"`
		Tag        string `json:"tag"`
		Models     string `json:"models"`
		Priority   int64  `json:"priority"`
		PoolSize   int    `json:"pool_size"`
		// Studio operator switch: when true the row goes into the
		// pool_size=0 "immediate" lane instead of the FIFO pool. Only
		// meaningful for studio operator callers — super admin picks
		// pool_size directly.
		Immediate bool `json:"immediate"`
		Items     []struct {
			Key      string   `json:"key"`
			QuotaUSD *float64 `json:"quota_usd,omitempty"`
			Note     string   `json:"note,omitempty"`
			Priority *int64   `json:"priority,omitempty"`
		} `json:"items"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.ProfileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	// Studio operator scope: force tag = user.studio, wipe any priority the
	// client tried to hand-set (both batch-level and per-item), and pin
	// pool_size to a positive sentinel so the row goes into the new pool
	// FIFO instead of the "immediate upload" path — operators never bypass
	// the throttle. Super admin keeps free control over all these fields.
	if callerIsStudioOperator(c) {
		studio := callerStudio(c)
		if studio == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "your account has no studio binding; ask an admin to bind one before uploading keys"})
			return
		}
		// (profile, studio) rejection: admin has flipped this studio to
		// "not accepting" for the target profile. 403, not 400 — the
		// request is well-formed, it's an authorization decision.
		accepting, err := studioAccepting(body.ProfileID, studio)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "policy check: " + err.Error()})
			return
		}
		if !accepting {
			c.JSON(http.StatusForbidden, gin.H{"error": "该 profile 暂不接收本工作室 key，请联系管理员"})
			return
		}
		body.Tag = studio
		body.Priority = 0
		// immediate=true switches the row to pool_size=0 (the "上普通
		// Key" path — direct upload, no throttle). Otherwise pin to the
		// pool sentinel (1) so operators can't bypass the drip via a
		// large pool_size number. Priority stays 0 in both cases; the
		// scheduler will assign a real priority for pool rows.
		if body.Immediate {
			body.PoolSize = 0
		} else if body.PoolSize <= 0 {
			body.PoolSize = 1
		} else {
			body.PoolSize = 1
		}
		for i := range body.Items {
			body.Items[i].Priority = nil
		}
	}
	if strings.TrimSpace(body.NamePrefix) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name_prefix is required"})
		return
	}
	if strings.TrimSpace(body.Models) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "models is required"})
		return
	}
	if body.PoolSize < 0 || body.PoolSize > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pool_size must be 0..100"})
		return
	}
	if strings.TrimSpace(body.Group) == "" {
		body.Group = "default"
	}
	if len(body.Items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no items provided"})
		return
	}
	// Sanity check that the profile exists so the scheduler doesn't have
	// to skip a whole batch later.
	if _, _, _, err := loadRemoteProfileByID(body.ProfileID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile not found or unreadable: " + err.Error()})
		return
	}

	now := time.Now().Unix()
	inserted, skipped := 0, 0
	for _, it := range body.Items {
		key := strings.TrimSpace(it.Key)
		if key == "" {
			skipped++
			continue
		}
		enc, err := encryptRemoteToken(key)
		if err != nil {
			skipped++
			continue
		}
		hash := pendingKeyHash(key)
		quota := 0.0
		if it.QuotaUSD != nil {
			quota = *it.QuotaUSD
		}
		prio := body.Priority
		if it.Priority != nil && *it.Priority > 0 {
			prio = *it.Priority
		}
		res, err := db.Exec(
			`INSERT INTO remote_pending_key
			 (profile_id, key_hash, key_encrypted, quota_usd, note, name_prefix,
			  group_name, tag, models, priority, pool_size, status, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'pending',$12,$12)
			 ON CONFLICT (profile_id, key_hash) DO NOTHING`,
			body.ProfileID, hash, enc, quota, strings.TrimSpace(it.Note), strings.TrimSpace(body.NamePrefix),
			body.Group, body.Tag, body.Models, prio, body.PoolSize, now,
		)
		if err != nil {
			skipped++
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		} else {
			skipped++
		}
	}
	// Nudge the scheduler so pool_size=0 items start uploading now instead
	// of waiting up to 60s for the next tick.
	select {
	case pendingSchedulerNudge <- struct{}{}:
	default:
	}
	c.JSON(http.StatusOK, gin.H{"inserted": inserted, "skipped": skipped, "total": len(body.Items)})
}

func handlePendingKeyList(c *gin.Context) {
	profileID, err := strconv.ParseInt(c.Query("profile_id"), 10, 64)
	if err != nil || profileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	statusFilter := strings.TrimSpace(c.Query("status"))
	q := `SELECT id, profile_id, key_encrypted, quota_usd, note, name_prefix,
	             group_name, tag, models, priority, pool_size, status,
	             remote_channel_id, attempts, failed_reason, created_at, updated_at
	        FROM remote_pending_key WHERE profile_id=$1`
	args := []any{profileID}
	if statusFilter != "" {
		q += " AND status=$" + strconv.Itoa(len(args)+1)
		args = append(args, statusFilter)
	}
	// Studio operator only ever sees their own studio's rows. Empty studio
	// on their JWT is a config error — surface it instead of silently
	// returning an empty list, so admin can fix the binding.
	if callerIsStudioOperator(c) {
		studio := callerStudio(c)
		if studio == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "your account has no studio binding; ask an admin to bind one before viewing the queue"})
			return
		}
		q += " AND tag=$" + strconv.Itoa(len(args)+1)
		args = append(args, studio)
	}
	q += " ORDER BY id DESC LIMIT 2000"

	rows, err := db.Query(q, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := make([]pendingKeyView, 0)
	for rows.Next() {
		var (
			r   pendingKeyRow
			enc string
		)
		if err := rows.Scan(&r.id, &r.profileID, &enc, &r.quotaUSD, &r.note, &r.namePrefix,
			&r.group, &r.tag, &r.models, &r.priority, &r.poolSize, &r.status,
			&r.remoteChID, &r.attempts, &r.failedReason, &r.createdAt, &r.updatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// Decrypt just so we can mask — never leaves the process.
		masked := "***"
		if k, err := decryptRemoteToken(enc); err == nil {
			masked = maskKey(k)
		}
		out = append(out, pendingKeyView{
			ID: r.id, ProfileID: r.profileID, KeyMasked: masked,
			QuotaUSD: r.quotaUSD, Note: r.note, NamePrefix: r.namePrefix,
			Group: r.group, Tag: r.tag, Models: r.models, Priority: r.priority,
			PoolSize: r.poolSize, Status: r.status, RemoteChID: r.remoteChID,
			Attempts: r.attempts, FailedReason: r.failedReason,
			CreatedAt: r.createdAt, UpdatedAt: r.updatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// ---- Handler: per-(profile, studio) accept/reject policy ----

// studioPolicyView is what the queue panel renders — one row per known
// studio + whether it may enqueue new keys. `has_row` distinguishes
// "explicitly configured" (there's a row in remote_studio_policy) from
// "implicit default" (accepting because no row exists yet). Frontend
// uses that to show "Accepting (default)" vs "Accepting" / "Rejected".
type studioPolicyView struct {
	Studio        string `json:"studio"`
	AcceptingKeys bool   `json:"accepting_keys"`
	HasRow        bool   `json:"has_row"`
	UpdatedAt     int64  `json:"updated_at"`
}

// handleStudioPolicyList unions the studios seen on this profile
// (distinct tag of remote_pending_key) with any explicit policy rows,
// so the admin can flip a studio that has never uploaded a key too. The
// missing-row shape maps to accepting=true so operators default to open.
func handleStudioPolicyList(c *gin.Context) {
	profileID, err := strconv.ParseInt(c.Query("profile_id"), 10, 64)
	if err != nil || profileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	// Collect all known studios: policy rows first (so admin can list a
	// studio they explicitly rejected before it ever tried to enqueue),
	// then any pending_key.tag we've seen. Empty tags are skipped.
	knownStudios := make(map[string]bool)
	policyMap := make(map[string]studioPolicyView)
	if rows, err := db.Query(
		`SELECT studio, accepting_keys, updated_at
		   FROM remote_studio_policy WHERE profile_id=$1`,
		profileID,
	); err == nil {
		for rows.Next() {
			var s string
			var acc bool
			var upd int64
			if err := rows.Scan(&s, &acc, &upd); err != nil {
				continue
			}
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			knownStudios[s] = true
			policyMap[s] = studioPolicyView{Studio: s, AcceptingKeys: acc, HasRow: true, UpdatedAt: upd}
		}
		rows.Close()
	}
	if rows, err := db.Query(
		`SELECT DISTINCT tag FROM remote_pending_key
		  WHERE profile_id=$1 AND tag<>''`,
		profileID,
	); err == nil {
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				continue
			}
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			knownStudios[s] = true
		}
		rows.Close()
	}
	out := make([]studioPolicyView, 0, len(knownStudios))
	for s := range knownStudios {
		if v, ok := policyMap[s]; ok {
			out = append(out, v)
			continue
		}
		out = append(out, studioPolicyView{Studio: s, AcceptingKeys: true, HasRow: false})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Studio < out[j].Studio })
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// handleStudioPolicyUpsert flips the accepting flag. Passing accepting=true
// with an already-missing row is still an upsert (a no-op effectively —
// the row now exists but says "accepting"), which is fine and simpler
// than a special path.
func handleStudioPolicyUpsert(c *gin.Context) {
	var body struct {
		ProfileID     int64  `json:"profile_id"`
		Studio        string `json:"studio"`
		AcceptingKeys bool   `json:"accepting_keys"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	body.Studio = strings.TrimSpace(body.Studio)
	if body.ProfileID <= 0 || body.Studio == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id and studio are required"})
		return
	}
	now := time.Now().Unix()
	if _, err := db.Exec(
		`INSERT INTO remote_studio_policy (profile_id, studio, accepting_keys, updated_at)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (profile_id, studio) DO UPDATE
		   SET accepting_keys=EXCLUDED.accepting_keys, updated_at=EXCLUDED.updated_at`,
		body.ProfileID, body.Studio, body.AcceptingKeys, now,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// studioAccepting returns whether the (profile, studio) pair may
// enqueue new keys. Missing row = true (open by default).
func studioAccepting(profileID int64, studio string) (bool, error) {
	studio = strings.TrimSpace(studio)
	if studio == "" {
		return true, nil
	}
	var acc bool
	err := db.QueryRow(
		`SELECT accepting_keys FROM remote_studio_policy
		  WHERE profile_id=$1 AND studio=$2`,
		profileID, studio,
	).Scan(&acc)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return acc, nil
}

func handlePendingKeyDelete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	// Never let a caller cancel a key that's currently attached to a
	// live remote channel — that would leak the row without cleanup.
	// Studio operator additionally may only cancel rows tagged with their
	// own studio; a stray attempt at someone else's row yields deleted:0
	// (idempotent no-op, no info leak).
	if callerIsStudioOperator(c) {
		studio := callerStudio(c)
		if studio == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "your account has no studio binding; ask an admin to bind one before canceling queue entries"})
			return
		}
		res, err := db.Exec(
			`DELETE FROM remote_pending_key
			  WHERE id=$1 AND tag=$2 AND status IN ('pending','failed')`,
			id, studio,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		n, _ := res.RowsAffected()
		c.JSON(http.StatusOK, gin.H{"deleted": n})
		return
	}
	res, err := db.Exec(
		`DELETE FROM remote_pending_key WHERE id=$1 AND status IN ('pending','failed')`,
		id,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	c.JSON(http.StatusOK, gin.H{"deleted": n})
}

// pendingSchedulerNudge lets the enqueue handler skip the tick wait for
// immediate (pool_size=0) uploads. Buffered=1 so overlapping enqueues
// coalesce into one wake-up.
var pendingSchedulerNudge = make(chan struct{}, 1)

// profilePoolNext tracks when each profile is next eligible to run its
// pool refill. The global ticker calls into each profile every 20s to
// reconcile active→used and drain pool_size=0 rows, but the pool refill
// itself honours the profile's own pool_interval_sec knob. In-memory
// only — a restart just runs the first refill immediately, which is the
// safe default.
var (
	profilePoolNextMu sync.Mutex
	profilePoolNext   = map[int64]time.Time{}
)

func poolTickDue(profileID int64, now time.Time) bool {
	profilePoolNextMu.Lock()
	defer profilePoolNextMu.Unlock()
	next, ok := profilePoolNext[profileID]
	return !ok || !now.Before(next)
}

func poolTickMark(profileID int64, next time.Time) {
	profilePoolNextMu.Lock()
	defer profilePoolNextMu.Unlock()
	profilePoolNext[profileID] = next
}

// startRemotePendingScheduler runs the drip logic. Every 20s (or on
// nudge) it, for each profile with any pending/active row:
//   • Marks active keys as `used` when their remote channel is disabled
//     (status ≠ 1) or missing from the local mirror.
//   • Uploads all pool_size=0 pending items (immediate lane, unchanged).
//   • Runs the pool refill (pool_size > 0) at most once per profile
//     pool_interval_sec, and only when zero active pool rows remain —
//     the "batch drip" invariant: no new keys go up until the last
//     batch has fully died on the remote.
//   • On upload failure attempts++; ≥ 3 → status='failed' (treated as
//     "done" for pool-refill purposes so a poisoned key can't lock the
//     queue forever).
func startRemotePendingScheduler() {
	log.Printf("[pending-scheduler] starting, tick=20s, retries=3, per-profile pool")
	go func() {
		// Modest stagger so the process doesn't hammer the remote in the
		// very second it boots.
		time.Sleep(15 * time.Second)
		runPendingTickAllProfiles()
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				runPendingTickAllProfiles()
			case <-pendingSchedulerNudge:
				runPendingTickAllProfiles()
			}
		}
	}()
}

// pendingMaxAttempts caps retries. Kept low so a genuinely bad key
// (revoked / rate-limited long-term) doesn't churn indefinitely; the
// operator sees `failed` and decides.
const pendingMaxAttempts = 3

func runPendingTickAllProfiles() {
	rows, err := db.Query(`SELECT DISTINCT profile_id FROM remote_pending_key WHERE status IN ('pending','active')`)
	if err != nil {
		log.Printf("[pending-scheduler] list profiles: %v", err)
		return
	}
	defer rows.Close()
	profiles := make([]int64, 0)
	for rows.Next() {
		var pid int64
		if err := rows.Scan(&pid); err == nil {
			profiles = append(profiles, pid)
		}
	}
	for _, pid := range profiles {
		runPendingTickForProfile(pid)
	}
}

func runPendingTickForProfile(profileID int64) {
	host, userID, token, err := loadRemoteProfileByID(profileID)
	if err != nil {
		log.Printf("[pending-scheduler] profile %d unreadable: %v", profileID, err)
		return
	}
	now := time.Now().Unix()

	// Step 1: reconcile currently-active rows against the local mirror
	// (kept fresh by startRemoteSnapshotSync). A remote channel gone or
	// disabled means the key has run out; mark it 'used' so the pool
	// can advance.
	activeRows, err := db.Query(
		`SELECT p.id, p.remote_channel_id, COALESCE(c.status, 0)
		   FROM remote_pending_key p
		   LEFT JOIN remote_channel_current c
		     ON c.profile_id = p.profile_id AND c.remote_channel_id = p.remote_channel_id
		  WHERE p.profile_id = $1 AND p.status = 'active'`,
		profileID,
	)
	if err != nil {
		log.Printf("[pending-scheduler] scan active for profile %d: %v", profileID, err)
	} else {
		for activeRows.Next() {
			var pID, chID int64
			var chStatus int
			if err := activeRows.Scan(&pID, &chID, &chStatus); err != nil {
				continue
			}
			// chStatus == 0 means either the row is genuinely disabled OR the
			// mirror hasn't captured it yet (snapshot sync runs every 15 min).
			// Only mark 'used' when the mirror knows the channel and reports
			// it non-enabled, to avoid false positives during warm-up.
			if chID > 0 && chStatus != 0 && chStatus != 1 {
				if _, err := db.Exec(
					`UPDATE remote_pending_key SET status='used', used_at=$1, updated_at=$1 WHERE id=$2`,
					now, pID,
				); err != nil {
					log.Printf("[pending-scheduler] mark used %d: %v", pID, err)
				}
			}
		}
		activeRows.Close()
	}

	// Step 2: upload all pool_size=0 pending items (immediate lane).
	uploadImmediatePending(host, token, userID, profileID)

	// Step 3: pool refill. Runs at most once per profile.pool_interval_sec,
	// and only when the previous batch has fully drained. Fails soft on
	// missing profile / bad pool config — we already have the profile
	// creds from loadRemoteProfileByID.
	var (
		interval, batchSize, rpmBase, rpmMin int
		autoMode                             bool
	)
	if err := db.QueryRow(
		`SELECT pool_interval_sec, pool_batch_size, auto_mode, rpm_base, rpm_min
		   FROM remote_newapi_profile WHERE id=$1`,
		profileID,
	).Scan(&interval, &batchSize, &autoMode, &rpmBase, &rpmMin); err != nil {
		log.Printf("[pending-scheduler] profile config %d: %v", profileID, err)
		return
	}
	interval = clampPoolInterval(interval)
	batchSize = clampPoolBatchSize(batchSize)
	rpmBase = clampRPMBase(rpmBase)
	rpmMin = clampRPMMin(rpmMin)
	nowT := time.Now()
	if !poolTickDue(profileID, nowT) {
		return
	}
	// Mark the next tick before doing any work so an overrun doesn't cause
	// back-to-back refills. Uses the profile's configured interval every
	// time — cheap to look up again next tick if the admin changes it.
	poolTickMark(profileID, nowT.Add(time.Duration(interval)*time.Second))

	var activeCount int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM remote_pending_key
		  WHERE profile_id = $1 AND pool_size > 0 AND status = 'active'`,
		profileID,
	).Scan(&activeCount); err != nil {
		log.Printf("[pending-scheduler] count active pool for profile %d: %v", profileID, err)
		return
	}
	if activeCount > 0 {
		// Previous batch still alive. failed rows are excluded from this
		// count on purpose — a poisoned key must not lock the pool.
		return
	}
	effective := batchSize
	if autoMode {
		// Fresh RPM read — no cache. On failure we skip this tick rather
		// than fall back to the manual batch, since a wrong sizing under
		// unknown load is worse than waiting one interval.
		rpm, err := fetchRemoteRPM(host, token, userID)
		if err != nil {
			log.Printf("[pending-scheduler] auto profile %d rpm fetch: %v — skipping tick", profileID, err)
			return
		}
		effective = autoBatchSize(int(rpm), rpmBase, rpmMin, batchSize)
		log.Printf("[pending-scheduler] auto profile %d rpm=%d base=%d min=%d cap=%d → n=%d",
			profileID, rpm, rpmBase, rpmMin, batchSize, effective)
		if effective == 0 {
			return
		}
	}
	uploadPoolBatch(host, token, userID, profileID, effective)
}

// autoBatchSize maps live RPM to a tick batch. Rules:
//   • rpm < rpm_min → 0 (queue idles until traffic returns)
//   • else n = ceil(rpm / rpm_base), capped at cap (= pool_batch_size)
// Ceiling matches the operator model ("one key handles rpm_base RPM, so
// once we're past that base we already need a second key online"). cap
// is the admin's hard ceiling for how many keys they're willing to burn
// per tick regardless of load.
func autoBatchSize(rpm, base, min, cap int) int {
	if rpm < min {
		return 0
	}
	if base <= 0 {
		return cap
	}
	n := (rpm + base - 1) / base
	if n < 1 {
		n = 1
	}
	if n > cap {
		n = cap
	}
	return n
}

// fetchRemoteRPM does an uncached /api/log/stat call for the given
// profile creds and returns the last-hour rpm. Kept separate from
// handleRemoteStatSummary so the scheduler always sees fresh data.
func fetchRemoteRPM(host, token string, userID int64) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := time.Now()
	q := url.Values{}
	q.Set("type", "2")
	q.Set("start_timestamp", strconv.FormatInt(now.Add(-time.Hour).Unix(), 10))
	q.Set("end_timestamp", strconv.FormatInt(now.Unix(), 10))
	data, err := remoteDoJSON(ctx, http.MethodGet, host, "/api/log/stat", token, userID, q, nil)
	if err != nil {
		return 0, err
	}
	var stat struct {
		Rpm int64 `json:"rpm"`
	}
	if err := json.Unmarshal(data, &stat); err != nil {
		return 0, fmt.Errorf("decode: %v", err)
	}
	return stat.Rpm, nil
}

// uploadImmediatePending drains pool_size=0 rows (the "upload right away"
// lane). Priority is respected as stored — super admin explicitly picked
// it. Capped per-tick so a giant backlog can't monopolise the tick.
func uploadImmediatePending(host, token string, userID, profileID int64) {
	const immediatePerTick = 20
	rows, err := db.Query(
		`SELECT id, key_encrypted, quota_usd, note, name_prefix, group_name,
		        tag, models, priority
		   FROM remote_pending_key
		  WHERE profile_id = $1 AND pool_size = 0 AND status = 'pending'
		  ORDER BY id ASC LIMIT $2`,
		profileID, immediatePerTick,
	)
	if err != nil {
		log.Printf("[pending-scheduler] pick immediate: %v", err)
		return
	}
	jobs := scanUploadJobs(rows)
	for _, j := range jobs {
		runSingleUpload(host, token, userID, profileID, j, j.priority)
	}
}

// uploadPoolBatch picks up to `n` oldest pending pool rows (FIFO by
// created_at, id) and uploads each with a freshly-computed priority.
// P starts at the current max(priority) of live remote channels for the
// profile — this is what "累加" means: the newest uploaded key always
// beats every currently-alive key by one. Within a single tick the
// counter accumulates in-memory so we don't need remote_channel_current
// to be synced between uploads.
func uploadPoolBatch(host, token string, userID, profileID int64, n int) {
	if n <= 0 {
		return
	}
	rows, err := db.Query(
		`SELECT id, key_encrypted, quota_usd, note, name_prefix, group_name,
		        tag, models, priority
		   FROM remote_pending_key
		  WHERE profile_id = $1 AND pool_size > 0 AND status = 'pending'
		  ORDER BY created_at ASC, id ASC
		  LIMIT $2`,
		profileID, n,
	)
	if err != nil {
		log.Printf("[pending-scheduler] pick pool batch: %v", err)
		return
	}
	jobs := scanUploadJobs(rows)
	if len(jobs) == 0 {
		return
	}
	// Compute starting P from the profile's remote_channel_current mirror.
	// status=1 = enabled. No enabled rows → start from 0, so the first
	// key ever uploaded gets priority 1.
	var pMax int64
	if err := db.QueryRow(
		`SELECT COALESCE(MAX(priority), 0) FROM remote_channel_current
		  WHERE profile_id = $1 AND status = 1`,
		profileID,
	).Scan(&pMax); err != nil {
		log.Printf("[pending-scheduler] scan pMax profile %d: %v", profileID, err)
		return
	}
	for _, j := range jobs {
		pMax++
		runSingleUpload(host, token, userID, profileID, j, pMax)
	}
}

// pendingUploadJob mirrors the columns needed to upload one pending row.
// Kept private to the scheduler.
type pendingUploadJob struct {
	id       int64
	enc      string
	quota    float64
	note     string
	prefix   string
	group    string
	tag      string
	models   string
	priority int64
}

func scanUploadJobs(rows *sql.Rows) []pendingUploadJob {
	defer rows.Close()
	out := make([]pendingUploadJob, 0)
	for rows.Next() {
		var j pendingUploadJob
		if err := rows.Scan(&j.id, &j.enc, &j.quota, &j.note, &j.prefix,
			&j.group, &j.tag, &j.models, &j.priority); err != nil {
			continue
		}
		out = append(out, j)
	}
	return out
}

// runSingleUpload does the actual remote POST + status transition for one
// pending row. `priority` is the value to hand to the remote and to
// persist on the row — the caller decides whether it's the row's stored
// priority (immediate lane) or a freshly computed accumulator (pool lane).
func runSingleUpload(host, token string, userID, profileID int64, j pendingUploadJob, priority int64) {
	key, err := decryptRemoteToken(j.enc)
	if err != nil {
		pendingRecordFailure(j.id, "decrypt: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var quotaPtr *float64
	if j.quota > 0 {
		q := j.quota
		quotaPtr = &q
	}
	chID, err := uploadOneKeyToRemote(ctx, uploadOneKeyParams{
		Host:       host,
		Token:      token,
		UserID:     userID,
		ProfileID:  profileID,
		Key:        key,
		NamePrefix: j.prefix,
		Models:     j.models,
		Group:      j.group,
		Tag:        j.tag,
		Priority:   priority,
		QuotaUSD:   quotaPtr,
		Note:       j.note,
	})
	if err != nil {
		pendingRecordFailure(j.id, err.Error())
		return
	}
	now := time.Now().Unix()
	if _, err := db.Exec(
		`UPDATE remote_pending_key
		    SET status='active', remote_channel_id=$1, activated_at=$2, updated_at=$2,
		        priority=$3, failed_reason=''
		  WHERE id=$4`,
		chID, now, priority, j.id,
	); err != nil {
		log.Printf("[pending-scheduler] mark active %d: %v", j.id, err)
	}
}

func pendingRecordFailure(id int64, reason string) {
	now := time.Now().Unix()
	// Bump attempts; flip to 'failed' once we've hit the retry cap.
	if _, err := db.Exec(
		`UPDATE remote_pending_key
		    SET attempts = attempts + 1,
		        status = CASE WHEN attempts + 1 >= $1 THEN 'failed' ELSE 'pending' END,
		        failed_reason = $2,
		        updated_at = $3
		  WHERE id = $4`,
		pendingMaxAttempts, reason, now, id,
	); err != nil {
		log.Printf("[pending-scheduler] record failure %d: %v", id, err)
	}
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
		  ORDER BY remote_channel_id DESC`,
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
					all[i].UnitPriceCNY = m.UnitPriceCNY
					all[i].Note = m.Note
				}
			}
		}
		if dsMap, err := loadLatestDownstream(profileID, ids); err == nil {
			for i := range all {
				if d, ok := dsMap[all[i].ID]; ok {
					v := d.value
					all[i].DownstreamCNY = &v
					all[i].DownstreamCNYDate = d.date
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
			ch.UnitPriceCNY = m.UnitPriceCNY
			ch.Note = m.Note
		}
	}
	if dsMap, err := loadLatestDownstream(profileID, []int64{ch.ID}); err == nil {
		if d, ok := dsMap[ch.ID]; ok {
			v := d.value
			ch.DownstreamCNY = &v
			ch.DownstreamCNYDate = d.date
		}
	}
	c.JSON(http.StatusOK, gin.H{"channel": ch})
}

// ---- Handler: batch-create channels on the remote ----

// remoteChannelCreateItem is one entry in the batch upload payload.
// Priority is optional — when set, overrides the batch-level priority
// so callers can implement per-key sequential priorities (each key one
// step below or above the previous).
type remoteChannelCreateItem struct {
	Key      string   `json:"key"`
	QuotaUSD *float64 `json:"quota_usd,omitempty"`
	Note     string   `json:"note,omitempty"`
	Priority *int64   `json:"priority,omitempty"`
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
		// Per-item priority (from sequential-priority mode) overrides the
		// batch-level default. Same 1-minimum clamp as BatchCreatePanel.
		itemPriority := body.Priority
		if it.Priority != nil && *it.Priority > 0 {
			itemPriority = *it.Priority
		}
		matchedID, err := uploadOneKeyToRemote(ctx, uploadOneKeyParams{
			Host:       host,
			Token:      token,
			UserID:     userID,
			ProfileID:  body.ProfileID,
			Key:        key,
			NamePrefix: body.NamePrefix,
			Type:       body.Type,
			Models:     body.Models,
			Group:      body.Group,
			Tag:        body.Tag,
			Priority:   itemPriority,
			BaseURL:    body.BaseURL,
			QuotaUSD:   it.QuotaUSD,
			Note:       it.Note,
		})
		if err != nil {
			results = append(results, batchCreateResult{Key: masked, OK: false, Error: err.Error()})
			continue
		}
		name := body.NamePrefix + "-" + channelKeyTail(key, 8) + "-" + keySha8(key)
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

// uploadOneKeyParams captures everything one upload attempt needs. Shared
// between the interactive batch handler and the queue scheduler so a
// keyed retry from the queue behaves exactly like a manual create.
type uploadOneKeyParams struct {
	Host       string
	Token      string
	UserID     int64
	ProfileID  int64
	Key        string
	NamePrefix string
	Type       int    // new-api channel type; 0 → default 14 (Anthropic)
	Models     string
	Group      string // upstream group; empty → "default"
	Tag        string
	Priority   int64
	BaseURL    string
	QuotaUSD   *float64 // local meta persisted with the returned channel id
	Note       string
}

// uploadOneKeyToRemote creates one channel on the remote and returns the
// remote channel id. Returns a descriptive error on any failure — the
// caller decides whether to retry or record as failed. Also upserts the
// operator meta (quota_usd / note) locally on success, best-effort.
func uploadOneKeyToRemote(ctx context.Context, p uploadOneKeyParams) (int64, error) {
	key := strings.TrimSpace(p.Key)
	if key == "" {
		return 0, errors.New("empty key")
	}
	if p.Type == 0 {
		p.Type = 14
	}
	if strings.TrimSpace(p.Group) == "" {
		p.Group = "default"
	}
	if strings.TrimSpace(p.Models) == "" {
		return 0, errors.New("models is required")
	}
	sha := keySha8(key)
	name := p.NamePrefix + "-" + channelKeyTail(key, 8) + "-" + sha

	channelBody := gin.H{
		"type":         p.Type,
		"key":          key,
		"name":         name,
		"status":       1,
		"models":       p.Models,
		"group":        p.Group,
		"priority":     p.Priority,
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
	if p.Tag != "" {
		channelBody["tag"] = p.Tag
	}
	if p.BaseURL != "" {
		channelBody["base_url"] = p.BaseURL
	}
	payload := gin.H{"mode": "single", "channel": channelBody}
	if _, err := remoteDoJSON(ctx, http.MethodPost, p.Host, "/api/channel/", p.Token, p.UserID, nil, payload); err != nil {
		return 0, err
	}

	// Reverse-lookup — see the inline comment in handleRemoteChannelCreate
	// for the two shape branches. Kept in sync via the same helper below.
	channelID, err := lookupRemoteChannelBySha(ctx, p.Host, p.Token, p.UserID, sha, p.Group)
	if err != nil {
		return 0, fmt.Errorf("created but %v", err)
	}
	if p.QuotaUSD != nil || strings.TrimSpace(p.Note) != "" {
		_ = upsertMeta(p.ProfileID, channelID, p.QuotaUSD, nil, strings.TrimSpace(p.Note))
	}
	return channelID, nil
}

// lookupRemoteChannelBySha runs the sha8-keyword search and picks the
// newest matching row. Shared between the interactive create and the
// scheduler so both survive when new-api's response shape drifts.
func lookupRemoteChannelBySha(ctx context.Context, host, token string, userID int64, sha, group string) (int64, error) {
	q := url.Values{}
	q.Set("keyword", sha)
	if group != "" {
		q.Set("group", group)
	}
	data, err := remoteDoJSON(ctx, http.MethodGet, host, "/api/channel/search", token, userID, q, nil)
	if err != nil {
		return 0, fmt.Errorf("search failed: %v", err)
	}
	type hit struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	var hits []hit
	var paged struct {
		Items []hit `json:"items"`
	}
	if err := json.Unmarshal(data, &paged); err == nil && paged.Items != nil {
		hits = paged.Items
	} else if err := json.Unmarshal(data, &hits); err != nil {
		snippet := string(data)
		if len(snippet) > 160 {
			snippet = snippet[:160] + "…"
		}
		return 0, fmt.Errorf("decode search failed: %s", snippet)
	}
	var matched int64
	for _, h := range hits {
		if strings.Contains(h.Name, sha) && h.ID > matched {
			matched = h.ID
		}
	}
	if matched == 0 {
		return 0, errors.New("reverse-lookup returned no match")
	}
	return matched, nil
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
		ProfileID    int64    `json:"profile_id"`
		ChannelID    int64    `json:"channel_id"`
		Name         *string  `json:"name,omitempty"`
		Tag          *string  `json:"tag,omitempty"`
		Status       *int     `json:"status,omitempty"`
		Priority     *int64   `json:"priority,omitempty"`
		Group        *string  `json:"group,omitempty"`
		Models       *string  `json:"models,omitempty"`
		QuotaUSD     *float64 `json:"quota_usd,omitempty"`
		UnitPriceCNY *float64 `json:"unit_price_cny,omitempty"`
		Note         *string  `json:"note,omitempty"`
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

	// Local meta. upsertMeta already writes only the fields we pass in, so
	// no merge is needed — nil pointers preserve existing values.
	if body.QuotaUSD != nil || body.UnitPriceCNY != nil || body.Note != nil {
		note := ""
		if body.Note != nil {
			note = strings.TrimSpace(*body.Note)
		}
		if err := upsertMeta(body.ProfileID, body.ChannelID, body.QuotaUSD, body.UnitPriceCNY, note); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "save meta: " + err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleRemoteMetaBulkUpdate writes the same meta fields to a bunch of
// channels at once. Only fields the caller sends are touched. Purely
// local — never talks to the remote. Meant for the "select N rows +
// set their unit_price_cny" flow.
func handleRemoteMetaBulkUpdate(c *gin.Context) {
	var body struct {
		ProfileID    int64    `json:"profile_id"`
		ChannelIDs   []int64  `json:"channel_ids"`
		QuotaUSD     *float64 `json:"quota_usd,omitempty"`
		UnitPriceCNY *float64 `json:"unit_price_cny,omitempty"`
		Note         *string  `json:"note,omitempty"`
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_ids is required"})
		return
	}
	// Sanity cap so a runaway UI can't stall the process on a giant loop.
	const maxIDs = 5000
	if len(body.ChannelIDs) > maxIDs {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("too many channel_ids (max %d)", maxIDs)})
		return
	}
	if body.QuotaUSD == nil && body.UnitPriceCNY == nil && body.Note == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}

	updated := 0
	failed := make([]int64, 0)
	for _, id := range body.ChannelIDs {
		note := ""
		if body.Note != nil {
			note = strings.TrimSpace(*body.Note)
		}
		if err := upsertMeta(body.ProfileID, id, body.QuotaUSD, body.UnitPriceCNY, note); err != nil {
			log.Printf("[meta-bulk] channel %d: %v", id, err)
			failed = append(failed, id)
			continue
		}
		updated++
	}
	c.JSON(http.StatusOK, gin.H{"updated": updated, "failed": failed})
}

// handleRemoteStatSummary returns aggregate rpm/tpm/last-hour-quota for
// a whole profile in a single upstream call. Avoids the O(N) per-channel
// fan-out the removed RPM/TPM columns needed. Cached briefly so the UI
// can poll comfortably.
type statSummaryEntry struct {
	rpm     int64
	tpm     int64
	quota   int64
	fetched time.Time
}

var (
	statSummaryCache   = make(map[int64]statSummaryEntry)
	statSummaryCacheMu sync.Mutex
)

const statSummaryTTL = 30 * time.Second

// handleRemoteDownstreamBulk sets the downstream (sell-side) price for a
// batch of channels on a specific date. The profit report looks up
// (channel, day) by picking the latest row where date ≤ day, so setting
// the price once at the start of a rate change is enough — subsequent
// days inherit the value until a new row overrides.
func handleRemoteDownstreamBulk(c *gin.Context) {
	var body struct {
		ProfileID     int64    `json:"profile_id"`
		ChannelIDs    []int64  `json:"channel_ids"`
		DownstreamCNY float64  `json:"downstream_cny"`
		Date          string   `json:"date"` // YYYY-MM-DD; empty = today UTC
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_ids is required"})
		return
	}
	if body.DownstreamCNY < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "downstream_cny must be ≥ 0"})
		return
	}
	date := strings.TrimSpace(body.Date)
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "date must be YYYY-MM-DD"})
		return
	}
	const maxIDs = 5000
	if len(body.ChannelIDs) > maxIDs {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("too many channel_ids (max %d)", maxIDs)})
		return
	}

	now := time.Now().Unix()
	updated := 0
	for _, chID := range body.ChannelIDs {
		if _, err := db.Exec(
			`INSERT INTO remote_channel_downstream
			   (profile_id, remote_channel_id, date, downstream_cny, updated_at)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (profile_id, remote_channel_id, date)
			 DO UPDATE SET downstream_cny = EXCLUDED.downstream_cny,
			               updated_at    = EXCLUDED.updated_at`,
			body.ProfileID, chID, date, body.DownstreamCNY, now,
		); err != nil {
			log.Printf("[downstream-bulk] channel %d: %v", chID, err)
			continue
		}
		updated++
	}
	c.JSON(http.StatusOK, gin.H{"updated": updated, "date": date})
}

// ---- Per-profile per-day downstream discount ----

// remoteDownstreamRow mirrors one row of remote_downstream_daily; used by
// both the list handler and internal callers (profit report loader).
type remoteDownstreamRow struct {
	ProfileID int64   `json:"profile_id"`
	Date      string  `json:"date"`
	Discount  float64 `json:"discount"`
	Note      string  `json:"note,omitempty"`
	UpdatedAt int64   `json:"updated_at"`
}

// handleRemoteDownstreamDailyList returns every configured (profile,date)
// discount row within an optional date range. Frontend uses it to render
// the editor grid on the Profit page.
func handleRemoteDownstreamDailyList(c *gin.Context) {
	profileIDStr := c.Query("profile_id")
	start := strings.TrimSpace(c.Query("start"))
	end := strings.TrimSpace(c.Query("end"))
	q := `SELECT profile_id, date, discount, note, updated_at
	        FROM remote_downstream_daily`
	args := []any{}
	conds := []string{}
	if profileIDStr != "" {
		pid, err := strconv.ParseInt(profileIDStr, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid profile_id"})
			return
		}
		args = append(args, pid)
		conds = append(conds, fmt.Sprintf("profile_id = $%d", len(args)))
	}
	if start != "" {
		args = append(args, start)
		conds = append(conds, fmt.Sprintf("date >= $%d", len(args)))
	}
	if end != "" {
		args = append(args, end)
		conds = append(conds, fmt.Sprintf("date <= $%d", len(args)))
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY profile_id, date DESC"

	rows, err := db.Query(q, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := make([]remoteDownstreamRow, 0)
	for rows.Next() {
		var r remoteDownstreamRow
		if err := rows.Scan(&r.ProfileID, &r.Date, &r.Discount, &r.Note, &r.UpdatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out = append(out, r)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// handleRemoteDownstreamDailyUpsert upserts one (profile,date) row. Same
// endpoint handles create and update — PK conflict swaps the values in.
func handleRemoteDownstreamDailyUpsert(c *gin.Context) {
	var body struct {
		ProfileID int64   `json:"profile_id"`
		Date      string  `json:"date"`
		Discount  float64 `json:"discount"`
		Note      string  `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.ProfileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	date := strings.TrimSpace(body.Date)
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "date must be YYYY-MM-DD"})
		return
	}
	if body.Discount < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "discount must be ≥ 0"})
		return
	}
	now := time.Now().Unix()
	if _, err := db.Exec(
		`INSERT INTO remote_downstream_daily (profile_id, date, discount, note, updated_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (profile_id, date)
		 DO UPDATE SET discount   = EXCLUDED.discount,
		               note       = EXCLUDED.note,
		               updated_at = EXCLUDED.updated_at`,
		body.ProfileID, date, body.Discount, strings.TrimSpace(body.Note), now,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func handleRemoteDownstreamDailyDelete(c *gin.Context) {
	profileID, err := strconv.ParseInt(c.Query("profile_id"), 10, 64)
	if err != nil || profileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	date := strings.TrimSpace(c.Query("date"))
	if date == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "date is required"})
		return
	}
	res, err := db.Exec(
		`DELETE FROM remote_downstream_daily WHERE profile_id=$1 AND date=$2`,
		profileID, date,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	c.JSON(http.StatusOK, gin.H{"deleted": n})
}

func handleRemoteStatSummary(c *gin.Context) {
	profileID, err := strconv.ParseInt(c.Query("profile_id"), 10, 64)
	if err != nil || profileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	statSummaryCacheMu.Lock()
	entry, ok := statSummaryCache[profileID]
	statSummaryCacheMu.Unlock()
	if ok && time.Since(entry.fetched) < statSummaryTTL {
		c.JSON(http.StatusOK, gin.H{"rpm": entry.rpm, "tpm": entry.tpm, "quota_last_hour": entry.quota, "cached": true})
		return
	}
	host, userID, token, err := loadRemoteProfileByID(profileID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	now := time.Now()
	q := url.Values{}
	q.Set("type", "2")
	q.Set("start_timestamp", strconv.FormatInt(now.Add(-time.Hour).Unix(), 10))
	q.Set("end_timestamp", strconv.FormatInt(now.Unix(), 10))
	data, err := remoteDoJSON(ctx, http.MethodGet, host, "/api/log/stat", token, userID, q, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var stat struct {
		Quota int64 `json:"quota"`
		Rpm   int64 `json:"rpm"`
		Tpm   int64 `json:"tpm"`
	}
	if err := json.Unmarshal(data, &stat); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "decode: " + err.Error()})
		return
	}
	statSummaryCacheMu.Lock()
	statSummaryCache[profileID] = statSummaryEntry{rpm: stat.Rpm, tpm: stat.Tpm, quota: stat.Quota, fetched: now}
	statSummaryCacheMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"rpm": stat.Rpm, "tpm": stat.Tpm, "quota_last_hour": stat.Quota, "cached": false})
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

// ---- Handler: last-hour cost per channel + realtime rpm/tpm ----

// lastHourEntry caches everything /api/log/stat returns in one shot: the
// requested-window quota AND the remote's hardcoded-60s rpm/tpm. TTL is
// short (30s) since rpm/tpm are the "realtime" bit and a 5-min stale
// value defeats the point.
//
// errRpm mirrors rpm but for LogTypeError (type=5) rows — used to
// surface an error-rate column on the /remote-channels page. Fetched
// with the same TTL as the success stats via a piggyback call to
// /api/log/stat?type=5 per channel.
type lastHourEntry struct {
	quota   int64
	rpm     int64
	tpm     int64
	errRpm  int64
	fetched time.Time
}

var (
	lastHourCache   = make(map[string]lastHourEntry)
	lastHourCacheMu sync.Mutex
)

// The RPM/TPM columns were removed, so the interactive last-hour lookup
// no longer needs a 30s TTL for freshness. A 5-minute cache means
// selecting "加载 last-hour" from the toolbar doesn't hammer the remote
// even on repeated refreshes.
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

	// Three parallel maps keyed by channel_id-as-string so JSON serialises
	// cleanly to {channel_id: value} on the frontend. `data` preserves the
	// existing key (last-hour quota, raw units) so old clients keep working;
	// new clients also read `rpm` and `tpm`.
	dataOut := make(map[string]int64, len(body.ChannelIDs))
	rpmOut := make(map[string]int64, len(body.ChannelIDs))
	tpmOut := make(map[string]int64, len(body.ChannelIDs))
	errRpmOut := make(map[string]int64, len(body.ChannelIDs))
	for _, chID := range body.ChannelIDs {
		cacheKey := strconv.FormatInt(body.ProfileID, 10) + ":" + strconv.FormatInt(chID, 10)
		lastHourCacheMu.Lock()
		entry, ok := lastHourCache[cacheKey]
		lastHourCacheMu.Unlock()
		idStr := strconv.FormatInt(chID, 10)
		if ok && now.Sub(entry.fetched) < lastHourTTL {
			dataOut[idStr] = entry.quota
			rpmOut[idStr] = entry.rpm
			tpmOut[idStr] = entry.tpm
			errRpmOut[idStr] = entry.errRpm
			continue
		}
		// Two piggyback calls: successful logs (type=2, quota/rpm/tpm) and
		// error logs (type=5, rpm only — errors have 0 quota so quota/tpm
		// aren't meaningful). Cached together so a refresh doesn't double-hit.
		q := url.Values{}
		q.Set("type", "2")
		q.Set("channel", idStr)
		q.Set("start_timestamp", strconv.FormatInt(oneHourAgo.Unix(), 10))
		q.Set("end_timestamp", strconv.FormatInt(now.Unix(), 10))
		data, err := remoteDoJSON(ctx, http.MethodGet, host, "/api/log/stat", token, userID, q, nil)
		if err != nil {
			// One channel failing shouldn't block the rest — record 0 and press on.
			dataOut[idStr] = 0
			rpmOut[idStr] = 0
			tpmOut[idStr] = 0
			errRpmOut[idStr] = 0
			continue
		}
		// Remote returns {quota, rpm, tpm}; rpm/tpm are always over the last 60s
		// regardless of start/end_timestamp (hardcoded upstream), so we can trust
		// them as a realtime snapshot.
		var stat struct {
			Quota int64 `json:"quota"`
			Rpm   int64 `json:"rpm"`
			Tpm   int64 `json:"tpm"`
		}
		if err := json.Unmarshal(data, &stat); err != nil {
			dataOut[idStr] = 0
			rpmOut[idStr] = 0
			tpmOut[idStr] = 0
			errRpmOut[idStr] = 0
			continue
		}
		// Error stats — best-effort. If this call fails we keep the success
		// numbers and record err_rpm=0; the UI shows "—" and moves on.
		var errRpm int64
		qErr := url.Values{}
		qErr.Set("type", "5")
		qErr.Set("channel", idStr)
		qErr.Set("start_timestamp", strconv.FormatInt(oneHourAgo.Unix(), 10))
		qErr.Set("end_timestamp", strconv.FormatInt(now.Unix(), 10))
		if errData, errErr := remoteDoJSON(ctx, http.MethodGet, host, "/api/log/stat", token, userID, qErr, nil); errErr == nil {
			var errStat struct {
				Rpm int64 `json:"rpm"`
			}
			if err := json.Unmarshal(errData, &errStat); err == nil {
				errRpm = errStat.Rpm
			}
		}

		dataOut[idStr] = stat.Quota
		rpmOut[idStr] = stat.Rpm
		tpmOut[idStr] = stat.Tpm
		errRpmOut[idStr] = errRpm
		lastHourCacheMu.Lock()
		lastHourCache[cacheKey] = lastHourEntry{
			quota:   stat.Quota,
			rpm:     stat.Rpm,
			tpm:     stat.Tpm,
			errRpm:  errRpm,
			fetched: now,
		}
		lastHourCacheMu.Unlock()
	}
	c.JSON(http.StatusOK, gin.H{
		"data":    dataOut,
		"rpm":     rpmOut,
		"tpm":     tpmOut,
		"err_rpm": errRpmOut,
	})
}

// ---- Handler: per-channel error breakdown ----

// errorBreakdownTTL — cache the categorised counts a little longer than
// the last-hour stats. Errors don't shift by the second, and a paginated
// log fetch is heavier than /api/log/stat.
const errorBreakdownTTL = 5 * time.Minute

// errorBreakdownEntry caches one breakdown per (profile_id, channel_id, window).
type errorBreakdownEntry struct {
	total       int64
	buckets     []errorBucket
	fetched     time.Time
}

type errorBucket struct {
	ErrorType  string `json:"error_type"`
	StatusCode int    `json:"status_code"`
	Count      int    `json:"count"`
}

var (
	errorBreakdownCache   = make(map[string]errorBreakdownEntry)
	errorBreakdownCacheMu sync.Mutex
)

// handleRemoteChannelErrors returns the categorised breakdown of error
// logs (LogTypeError=5) for a single channel over `window_sec` seconds
// (default 3600). Groups by (error_type, status_code) from log.other
// JSON. Cached for 5 minutes per (profile_id, channel_id, window) so
// repeated inspection doesn't hammer the remote.
//
// Response:
//   {
//     "total":   425,
//     "buckets": [{"error_type":"openai_error","status_code":429,"count":300}, ...]
//   }
func handleRemoteChannelErrors(c *gin.Context) {
	var body struct {
		ProfileID  int64 `json:"profile_id"`
		ChannelID  int64 `json:"channel_id"`
		WindowSec  int64 `json:"window_sec"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.ProfileID <= 0 || body.ChannelID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id and channel_id are required"})
		return
	}
	if body.WindowSec <= 0 || body.WindowSec > 24*3600 {
		body.WindowSec = 3600
	}

	cacheKey := fmt.Sprintf("%d:%d:%d", body.ProfileID, body.ChannelID, body.WindowSec)
	now := time.Now()
	errorBreakdownCacheMu.Lock()
	entry, ok := errorBreakdownCache[cacheKey]
	errorBreakdownCacheMu.Unlock()
	if ok && now.Sub(entry.fetched) < errorBreakdownTTL {
		c.JSON(http.StatusOK, gin.H{"total": entry.total, "buckets": entry.buckets, "cached": true})
		return
	}

	host, userID, token, err := loadRemoteProfileByID(body.ProfileID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	windowStart := now.Add(-time.Duration(body.WindowSec) * time.Second)
	// One page of up to 200 rows. If the channel has > 200 errors in the
	// window we truncate — but the pageInfo.total from the remote gives
	// the real total, so the top-level count stays honest even when the
	// bucket breakdown is a sample. Sufficient for a UI hover.
	q := url.Values{}
	q.Set("type", "5")
	q.Set("channel", strconv.FormatInt(body.ChannelID, 10))
	q.Set("start_timestamp", strconv.FormatInt(windowStart.Unix(), 10))
	q.Set("end_timestamp", strconv.FormatInt(now.Unix(), 10))
	q.Set("p", "1")
	q.Set("page_size", "200")

	data, err := remoteDoJSON(ctx, http.MethodGet, host, "/api/log/", token, userID, q, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	// Remote paginated response wraps items + total.
	var resp struct {
		Items []struct {
			Content string `json:"content"`
			Other   string `json:"other"`
		} `json:"items"`
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "decode: " + err.Error()})
		return
	}

	// Aggregate by (error_type, status_code). Missing / unparseable values
	// fall into ("unknown", 0) so nothing is silently dropped.
	type key struct {
		Type   string
		Status int
	}
	counts := make(map[key]int)
	for _, it := range resp.Items {
		var oj map[string]interface{}
		_ = json.Unmarshal([]byte(it.Other), &oj)
		var k key
		if s, ok := oj["error_type"].(string); ok {
			k.Type = s
		} else {
			k.Type = "unknown"
		}
		if v, ok := oj["status_code"].(float64); ok {
			k.Status = int(v)
		}
		counts[k]++
	}
	buckets := make([]errorBucket, 0, len(counts))
	for k, n := range counts {
		buckets = append(buckets, errorBucket{ErrorType: k.Type, StatusCode: k.Status, Count: n})
	}
	// Sort descending by count for stable UI order.
	sort.Slice(buckets, func(i, j int) bool {
		if buckets[i].Count != buckets[j].Count {
			return buckets[i].Count > buckets[j].Count
		}
		if buckets[i].ErrorType != buckets[j].ErrorType {
			return buckets[i].ErrorType < buckets[j].ErrorType
		}
		return buckets[i].StatusCode < buckets[j].StatusCode
	})

	errorBreakdownCacheMu.Lock()
	errorBreakdownCache[cacheKey] = errorBreakdownEntry{total: resp.Total, buckets: buckets, fetched: now}
	errorBreakdownCacheMu.Unlock()

	c.JSON(http.StatusOK, gin.H{
		"total":       resp.Total,
		"buckets":     buckets,
		"sample_size": len(resp.Items),
		"window_sec":  body.WindowSec,
	})
}

// ---- Handler: per-channel success / error counts over a window ----

// channelCountsTTL — 5min cache lines up with the error-breakdown cache
// and prevents refresh spam. Counts don't shift by the second and users
// typically eyeball one profile at a time.
const channelCountsTTL = 5 * time.Minute

type channelCountsEntry struct {
	success int64
	errors  int64
	fetched time.Time
}

var (
	channelCountsCache   = make(map[string]channelCountsEntry)
	channelCountsCacheMu sync.Mutex
)

// handleRemoteChannelCounts returns exact success + error counts for
// each channel over the last `window_sec` seconds. Uses the remote's
// paginated /api/log/ endpoint with page_size=1 to grab `total` from
// pageInfo — cheap regardless of the actual count. Two calls per
// channel (type=2 success, type=5 error).
//
// Response:
//   {"data": {"123": {"success": 8123, "errors": 425}, ...}, "window_sec": 3600}
func handleRemoteChannelCounts(c *gin.Context) {
	var body struct {
		ProfileID  int64   `json:"profile_id"`
		ChannelIDs []int64 `json:"channel_ids"`
		WindowSec  int64   `json:"window_sec"`
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
		c.JSON(http.StatusOK, gin.H{"data": map[string]any{}})
		return
	}
	const maxIDs = 200
	if len(body.ChannelIDs) > maxIDs {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("too many channel_ids (max %d)", maxIDs)})
		return
	}
	if body.WindowSec <= 0 || body.WindowSec > 7*24*3600 {
		body.WindowSec = 3600
	}

	host, userID, token, err := loadRemoteProfileByID(body.ProfileID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()

	now := time.Now()
	windowStart := now.Add(-time.Duration(body.WindowSec) * time.Second)

	type row struct {
		Success int64 `json:"success"`
		Errors  int64 `json:"errors"`
	}
	out := make(map[string]row, len(body.ChannelIDs))

	// One-page probe of the paginated log endpoint. We only need `total`;
	// pull the smallest page possible to stay fast.
	probe := func(logType int, chID int64) int64 {
		q := url.Values{}
		q.Set("type", strconv.Itoa(logType))
		q.Set("channel", strconv.FormatInt(chID, 10))
		q.Set("start_timestamp", strconv.FormatInt(windowStart.Unix(), 10))
		q.Set("end_timestamp", strconv.FormatInt(now.Unix(), 10))
		q.Set("p", "1")
		q.Set("page_size", "1")
		data, err := remoteDoJSON(ctx, http.MethodGet, host, "/api/log/", token, userID, q, nil)
		if err != nil {
			return 0
		}
		var resp struct {
			Total int64 `json:"total"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return 0
		}
		return resp.Total
	}

	// Partition into cache hits (served instantly) and misses (probed
	// concurrently below). Cache hits fill `out` directly; misses go
	// through a bounded worker pool so a 200-channel refresh doesn't
	// serialise into a minute-long request.
	type miss struct {
		chID  int64
		idStr string
	}
	misses := make([]miss, 0, len(body.ChannelIDs))
	var outMu sync.Mutex
	for _, chID := range body.ChannelIDs {
		cacheKey := fmt.Sprintf("%d:%d:%d", body.ProfileID, chID, body.WindowSec)
		channelCountsCacheMu.Lock()
		entry, ok := channelCountsCache[cacheKey]
		channelCountsCacheMu.Unlock()
		idStr := strconv.FormatInt(chID, 10)
		if ok && now.Sub(entry.fetched) < channelCountsTTL {
			out[idStr] = row{Success: entry.success, Errors: entry.errors}
			continue
		}
		misses = append(misses, miss{chID: chID, idStr: idStr})
	}
	if len(misses) > 0 {
		// 8 workers ≈ 25 request-pairs per worker at 200 misses. Enough
		// concurrency to matter without hammering the remote — its
		// /api/log/ query is DB-bound and can pile up under contention.
		const workers = 8
		var wg sync.WaitGroup
		sem := make(chan struct{}, workers)
		for _, m := range misses {
			m := m
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				successCount := probe(2, m.chID)
				errorCount := probe(5, m.chID)
				outMu.Lock()
				out[m.idStr] = row{Success: successCount, Errors: errorCount}
				outMu.Unlock()
				cacheKey := fmt.Sprintf("%d:%d:%d", body.ProfileID, m.chID, body.WindowSec)
				channelCountsCacheMu.Lock()
				channelCountsCache[cacheKey] = channelCountsEntry{
					success: successCount,
					errors:  errorCount,
					fetched: now,
				}
				channelCountsCacheMu.Unlock()
			}()
		}
		wg.Wait()
	}

	c.JSON(http.StatusOK, gin.H{
		"data":       out,
		"window_sec": body.WindowSec,
	})
}

// ---- Handler: profile-wide error summary + bucket breakdown ----

// Now that errors come from the local mirror (kept fresh by the 60s
// sync loop), the summary itself only needs a short cache to
// deduplicate concurrent UI refreshes.
const profileErrorSummaryTTL = 30 * time.Second

type profileErrorSummaryEntry struct {
	totalSuccess int64
	totalErrors  int64
	buckets      []errorBucket
	sampleSize   int
	fetched      time.Time
}

var (
	profileErrorSummaryCache   = make(map[string]profileErrorSummaryEntry)
	profileErrorSummaryCacheMu sync.Mutex
)

// handleRemoteProfileErrorSummary returns profile-wide error stats over
// `window_sec` seconds (default 3600, max 7d).
//   - true totals via /api/log/?page_size=1&type=X reading pageInfo.total
//   - bucket distribution via one page of up to 500 recent error rows;
//     each row's `other` JSON contributes an (error_type, status_code)
//     tuple. When the bucket sample is smaller than total_errors the
//     bucket counts are scaled proportionally so the numbers reconcile.
//
// Response:
//   {
//     "total_success": 12345,
//     "total_errors":  678,
//     "error_rate":    0.0521,
//     "buckets": [
//       {"error_type":"openai_error","status_code":429,"count":300,"share":0.442},
//       ...
//     ],
//     "sample_size":  500,
//     "truncated":    true,
//     "window_sec":   3600
//   }
func handleRemoteProfileErrorSummary(c *gin.Context) {
	profileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || profileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid profile id"})
		return
	}
	windowSec, _ := strconv.ParseInt(c.DefaultQuery("window_sec", "3600"), 10, 64)
	if windowSec <= 0 || windowSec > 7*24*3600 {
		windowSec = 3600
	}

	cacheKey := fmt.Sprintf("%d:%d", profileID, windowSec)
	now := time.Now()
	profileErrorSummaryCacheMu.Lock()
	entry, ok := profileErrorSummaryCache[cacheKey]
	profileErrorSummaryCacheMu.Unlock()
	if ok && now.Sub(entry.fetched) < profileErrorSummaryTTL {
		writeProfileErrorSummary(c, entry, windowSec, true, profileID)
		return
	}

	windowStart := now.Add(-time.Duration(windowSec) * time.Second)

	// Error side is entirely local now — startRemoteErrorLogSync keeps
	// remote_error_log fresh on a 60s tick, and the query is a couple
	// of indexed range scans. Precise for any window we have coverage
	// for; the response's `sync_lag_sec` tells the UI when we last
	// caught up so it can flag stale windows.
	var totalErrors int64
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM remote_error_log
		  WHERE profile_id = $1 AND created_at >= $2 AND created_at <= $3`,
		profileID, windowStart.Unix(), now.Unix(),
	).Scan(&totalErrors); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "local errors: " + err.Error()})
		return
	}

	// Bucket breakdown — pure local aggregation, exact counts.
	bucketRows, err := db.Query(
		`SELECT COALESCE(NULLIF(error_type,''),'unknown') AS et,
		        status_code,
		        COUNT(*)
		   FROM remote_error_log
		  WHERE profile_id = $1 AND created_at >= $2 AND created_at <= $3
		  GROUP BY et, status_code
		  ORDER BY COUNT(*) DESC, status_code ASC, et ASC
		  LIMIT 200`,
		profileID, windowStart.Unix(), now.Unix(),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "local buckets: " + err.Error()})
		return
	}
	buckets := make([]errorBucket, 0)
	for bucketRows.Next() {
		var b errorBucket
		if err := bucketRows.Scan(&b.ErrorType, &b.StatusCode, &b.Count); err != nil {
			bucketRows.Close()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "scan bucket: " + err.Error()})
			return
		}
		buckets = append(buckets, b)
	}
	bucketRows.Close()

	// Success side still needs remote — one page_size=1 call gets the
	// exact total via pageInfo. Wrapped in a short timeout so a flaky
	// remote can't stall the local-only path.
	host, userID, token, err := loadRemoteProfileByID(profileID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	var totalSuccess int64
	q := url.Values{}
	q.Set("type", "2")
	q.Set("start_timestamp", strconv.FormatInt(windowStart.Unix(), 10))
	q.Set("end_timestamp", strconv.FormatInt(now.Unix(), 10))
	q.Set("p", "1")
	q.Set("page_size", "1")
	if data, err := remoteDoJSON(ctx, http.MethodGet, host, "/api/log/", token, userID, q, nil); err == nil {
		var resp struct {
			Total int64 `json:"total"`
		}
		if err := json.Unmarshal(data, &resp); err == nil {
			totalSuccess = resp.Total
		}
	}

	entry = profileErrorSummaryEntry{
		totalSuccess: totalSuccess,
		totalErrors:  totalErrors,
		buckets:      buckets,
		sampleSize:   int(totalErrors), // exact — buckets aren't sampled
		fetched:      now,
	}
	profileErrorSummaryCacheMu.Lock()
	profileErrorSummaryCache[cacheKey] = entry
	profileErrorSummaryCacheMu.Unlock()

	writeProfileErrorSummary(c, entry, windowSec, false, profileID)
}

func writeProfileErrorSummary(c *gin.Context, e profileErrorSummaryEntry, windowSec int64, cached bool, profileID int64) {
	total := e.totalSuccess + e.totalErrors
	rate := 0.0
	if total > 0 {
		rate = float64(e.totalErrors) / float64(total)
	}
	type wireBucket struct {
		ErrorType  string  `json:"error_type"`
		StatusCode int     `json:"status_code"`
		Count      int     `json:"count"`
		Share      float64 `json:"share"`
	}
	buckets := make([]wireBucket, 0, len(e.buckets))
	for _, b := range e.buckets {
		share := 0.0
		if e.totalErrors > 0 {
			share = float64(b.Count) / float64(e.totalErrors)
		}
		buckets = append(buckets, wireBucket{
			ErrorType:  b.ErrorType,
			StatusCode: b.StatusCode,
			Count:      b.Count,
			Share:      share,
		})
	}
	// Sync lag lets the UI flag "just deployed / still catching up".
	var lastSynced int64
	_ = db.QueryRow(
		`SELECT last_synced_at FROM remote_error_sync_state WHERE profile_id=$1`,
		profileID,
	).Scan(&lastSynced)
	var lag int64
	if lastSynced > 0 {
		lag = time.Now().Unix() - lastSynced
		if lag < 0 {
			lag = 0
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"total_success":  e.totalSuccess,
		"total_errors":   e.totalErrors,
		"error_rate":     rate,
		"buckets":        buckets,
		"sample_size":    e.sampleSize,
		"truncated":      false,
		"window_sec":     windowSec,
		"cached":         cached,
		"last_synced_at": lastSynced,
		"sync_lag_sec":   lag,
	})
}

// ---- Error-log sync: pull remote error logs to local table ----

const (
	// errorLogSyncInterval is how often we run one sync tick per profile.
	errorLogSyncInterval = 60 * time.Second
	// errorLogPageSize is the paginated /api/log/ page size per HTTP call.
	// 200 is the remote's typical soft cap and keeps a page under ~1MB.
	errorLogPageSize = 200
	// errorLogPagesPerTick bounds the pagination depth per tick so a
	// backlog doesn't monopolise the sync loop. 2000 rows / minute is
	// plenty for realistic error volumes; a genuine flood catches up
	// over subsequent ticks.
	errorLogPagesPerTick = 10
	// errorLogInitialBackfill is how far back we go on first sync per
	// profile (last_synced_at = 0). Trades startup time for immediate
	// usefulness on the "过去 24 小时" window.
	errorLogInitialBackfill = 24 * time.Hour
	// errorLogOverlap is how far we rewind from the high-water mark on
	// each incremental sync to catch any late-arriving remote rows.
	errorLogOverlap = 60 * time.Second
	// errorLogContentTrim caps the persisted content_snippet size so a
	// verbose upstream error doesn't blow up the table.
	errorLogContentTrim = 512
	// errorLogRetention drops rows older than this age. Kept aligned
	// with remote_channel_snapshot's default retention semantics.
	errorLogRetention = 14 * 24 * time.Hour
	errorLogRetentionInterval = time.Hour
)

// startRemoteErrorLogSync launches the per-profile sync loop. Each tick
// finds every configured remote profile and, one at a time, pulls new
// error log rows since the last high-water mark (or 24h back on first
// run). Serial across profiles to bound remote load — a deployment
// with dozens of profiles still uses at most 1 remote in flight.
func startRemoteErrorLogSync() {
	go func() {
		time.Sleep(3 * time.Second) // let the schema settle
		tick := time.NewTicker(errorLogSyncInterval)
		defer tick.Stop()
		syncAllProfilesErrorLogs()
		for range tick.C {
			syncAllProfilesErrorLogs()
		}
	}()
	go func() {
		tick := time.NewTicker(errorLogRetentionInterval)
		defer tick.Stop()
		pruneRemoteErrorLogs()
		for range tick.C {
			pruneRemoteErrorLogs()
		}
	}()
}

func syncAllProfilesErrorLogs() {
	rows, err := db.Query(`SELECT id FROM remote_newapi_profile ORDER BY id`)
	if err != nil {
		log.Printf("[error-sync] list profiles: %v", err)
		return
	}
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			log.Printf("[error-sync] scan profile id: %v", err)
			return
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, pid := range ids {
		syncOneProfileErrorLogs(pid)
	}
}

func syncOneProfileErrorLogs(profileID int64) {
	host, userID, token, err := loadRemoteProfileByID(profileID)
	if err != nil {
		recordErrorSyncFailure(profileID, "load profile: "+err.Error())
		return
	}

	// Read HWM. Missing row = first ever sync for this profile; fall
	// back to (now - initial backfill).
	var lastSynced int64
	if err := db.QueryRow(
		`SELECT last_synced_at FROM remote_error_sync_state WHERE profile_id=$1`,
		profileID,
	).Scan(&lastSynced); err != nil && err != sql.ErrNoRows {
		recordErrorSyncFailure(profileID, "read state: "+err.Error())
		return
	}
	now := time.Now()
	var startAt int64
	if lastSynced == 0 {
		startAt = now.Add(-errorLogInitialBackfill).Unix()
	} else {
		startAt = lastSynced - int64(errorLogOverlap.Seconds())
	}
	endAt := now.Unix()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ingested := 0
	maxCreated := lastSynced
	for page := 1; page <= errorLogPagesPerTick; page++ {
		q := url.Values{}
		q.Set("type", "5")
		q.Set("start_timestamp", strconv.FormatInt(startAt, 10))
		q.Set("end_timestamp", strconv.FormatInt(endAt, 10))
		q.Set("p", strconv.Itoa(page))
		q.Set("page_size", strconv.Itoa(errorLogPageSize))
		data, err := remoteDoJSON(ctx, http.MethodGet, host, "/api/log/", token, userID, q, nil)
		if err != nil {
			recordErrorSyncFailure(profileID, fmt.Sprintf("page %d: %v", page, err))
			return
		}
		var resp struct {
			Items []struct {
				ID        int64  `json:"id"`
				ChannelId int64  `json:"channel_id"`
				CreatedAt int64  `json:"created_at"`
				ModelName string `json:"model_name"`
				TokenName string `json:"token_name"`
				Group     string `json:"group"`
				Content   string `json:"content"`
				Other     string `json:"other"`
			} `json:"items"`
			Total int64 `json:"total"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			recordErrorSyncFailure(profileID, fmt.Sprintf("page %d decode: %v", page, err))
			return
		}
		if len(resp.Items) == 0 {
			break
		}
		for _, it := range resp.Items {
			if it.CreatedAt > maxCreated {
				maxCreated = it.CreatedAt
			}
			et, sc, ec, rp := parseErrorLogOther(it.Other)
			snippet := it.Content
			if len(snippet) > errorLogContentTrim {
				snippet = snippet[:errorLogContentTrim]
			}
			// Sanitize any accidental key material or bearer token.
			snippet = sanitizeUpstreamErrorSnippet(snippet)
			res, err := db.Exec(
				`INSERT INTO remote_error_log
				  (profile_id, remote_log_id, channel_id, model_name, token_name, group_name,
				   created_at, ingested_at, error_type, status_code, error_code, request_path, content_snippet)
				 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
				 ON CONFLICT (profile_id, remote_log_id) DO NOTHING`,
				profileID, it.ID, it.ChannelId, it.ModelName, it.TokenName, it.Group,
				it.CreatedAt, now.Unix(), et, sc, ec, rp, snippet,
			)
			if err != nil {
				recordErrorSyncFailure(profileID, "insert: "+err.Error())
				return
			}
			if n, _ := res.RowsAffected(); n > 0 {
				ingested++
			}
		}
		// If this page came back short of the requested size, we've
		// caught up — no need to fetch page+1.
		if len(resp.Items) < errorLogPageSize {
			break
		}
	}

	// Update state row. UPSERT so first-ever run creates it.
	_, err = db.Exec(
		`INSERT INTO remote_error_sync_state (profile_id, last_synced_at, last_run_at, last_error, total_ingested)
		 VALUES ($1, $2, $3, '', $4)
		 ON CONFLICT (profile_id) DO UPDATE
		   SET last_synced_at = GREATEST(remote_error_sync_state.last_synced_at, EXCLUDED.last_synced_at),
		       last_run_at    = EXCLUDED.last_run_at,
		       last_error     = '',
		       total_ingested = remote_error_sync_state.total_ingested + EXCLUDED.total_ingested`,
		profileID, maxCreated, now.Unix(), int64(ingested),
	)
	if err != nil {
		log.Printf("[error-sync] update state for profile=%d: %v", profileID, err)
		return
	}
	if ingested > 0 {
		log.Printf("[error-sync] profile=%d ingested=%d hwm=%d", profileID, ingested, maxCreated)
	}
}

func recordErrorSyncFailure(profileID int64, reason string) {
	log.Printf("[error-sync] profile=%d FAILED: %s", profileID, reason)
	now := time.Now().Unix()
	// Cap the failure message length to avoid pathological content.
	if len(reason) > 500 {
		reason = reason[:500]
	}
	_, err := db.Exec(
		`INSERT INTO remote_error_sync_state (profile_id, last_synced_at, last_run_at, last_error, total_ingested)
		 VALUES ($1, 0, $2, $3, 0)
		 ON CONFLICT (profile_id) DO UPDATE
		   SET last_run_at = EXCLUDED.last_run_at,
		       last_error  = EXCLUDED.last_error`,
		profileID, now, reason,
	)
	if err != nil {
		log.Printf("[error-sync] record failure for profile=%d: %v", profileID, err)
	}
}

// parseErrorLogOther extracts the fields we care about from the
// json-encoded `other` blob newapi attaches to error logs
// (see controller/relay.go RecordErrorLog callsite).
func parseErrorLogOther(otherJSON string) (errorType string, statusCode int, errorCode, requestPath string) {
	if otherJSON == "" {
		return "unknown", 0, "", ""
	}
	var oj map[string]interface{}
	if err := json.Unmarshal([]byte(otherJSON), &oj); err != nil {
		return "unknown", 0, "", ""
	}
	if s, ok := oj["error_type"].(string); ok {
		errorType = s
	}
	if errorType == "" {
		errorType = "unknown"
	}
	if v, ok := oj["status_code"].(float64); ok {
		statusCode = int(v)
	}
	if s, ok := oj["error_code"].(string); ok {
		errorCode = s
	} else if v, ok := oj["error_code"].(float64); ok {
		errorCode = strconv.FormatFloat(v, 'f', -1, 64)
	}
	if s, ok := oj["request_path"].(string); ok {
		requestPath = s
	}
	return
}

// sanitizeUpstreamErrorSnippet strips a couple of well-known key-shaped
// substrings out of an upstream error message before persisting it, so
// we don't accidentally hold onto anything that resembles a token in
// content_snippet. Cheap, best-effort — the primary guard is that
// error logs don't usually echo full tokens back in the first place.
var (
	keyLikePattern1 = regexp.MustCompile(`sk-[A-Za-z0-9\-_]{16,}`)
	keyLikePattern2 = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9\-_.]{16,}`)
	keyLikePattern3 = regexp.MustCompile(`(?i)api[_-]?key["'\s:=]+[A-Za-z0-9\-_]{16,}`)
)

func sanitizeUpstreamErrorSnippet(s string) string {
	s = keyLikePattern1.ReplaceAllString(s, "sk-****")
	s = keyLikePattern2.ReplaceAllString(s, "Bearer ****")
	s = keyLikePattern3.ReplaceAllString(s, "api_key=****")
	return s
}

func pruneRemoteErrorLogs() {
	cutoff := time.Now().Add(-errorLogRetention).Unix()
	res, err := db.Exec(`DELETE FROM remote_error_log WHERE created_at < $1`, cutoff)
	if err != nil {
		log.Printf("[error-sync] prune: %v", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("[error-sync] pruned %d rows older than %s", n, errorLogRetention)
	}
}
