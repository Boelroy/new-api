package main

// V2 Key Pool endpoints — upload / list / assign / rebind / disable / delete / export.
//
// KEY VISIBILITY INVARIANT (V2_PRODUCT_SPEC.md §3.6):
//   All key serialization goes through serializeKeyRow in v2_keypool_serialize.go.
//   No handler in this file may format a key string on its own.
//   Only dead keys (linked remote_channel_current.status = 3 OR channels.status = 3)
//   are eligible for plaintext reveal, and only for callers holding
//   keys.reveal_dead.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

// Key type enum.
const (
	KeyTypeRegular    = "regular"
	KeyTypeTrial5USD  = "trial_5usd"
)

// Pool status enum.
const (
	PoolStatusAwaiting = "awaiting_assignment"
	PoolStatusPending  = "pending"
	PoolStatusActive   = "active"
	PoolStatusUsed     = "used"
	PoolStatusFailed   = "failed"
)

// Target mode for upload.
const (
	TargetModePoolOnly    = "pool_only"
	TargetModeDirectNewapi = "direct_newapi"
)

func isValidKeyType(t string) bool {
	return t == KeyTypeRegular || t == KeyTypeTrial5USD
}

// defaultQuotaForKeyType applies the spec's per-type default when the
// operator did not specify a quota. Returns nil for regular (unlimited).
func defaultQuotaForKeyType(t string, provided *float64) *float64 {
	if provided != nil {
		return provided
	}
	if t == KeyTypeTrial5USD {
		v := 5.0
		return &v
	}
	return nil
}

// hashKey returns sha256 hex of the raw key. Deterministic; used as the
// dedup key in rs_key_pool.key_hash and the pending queue tables.
func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// last8 returns the last 8 characters of a key for masked display. Empty
// string when the key is shorter than 8 (should not happen with real keys).
func last8(k string) string {
	if len(k) < 8 {
		return ""
	}
	return k[len(k)-8:]
}

// registerV2KeysRoutes mounts key-pool endpoints on the /api/v2 group.
func registerV2KeysRoutes(api *gin.RouterGroup) {
	// Upload: pool_only requires keys.pool.upload; direct_newapi requires
	// keys.newapi.upload_direct. Both are validated in the handler.
	api.POST("/keys/pool", v2HandleKeyPoolUpload)

	api.GET("/keys/pool", requirePermission(ActionKeysPoolView, ScopeOwnStudio), v2HandleKeyPoolList)
	api.GET("/keys/active", requirePermission(ActionKeysNewapiView, ScopeOwnStudio), v2HandleKeyActiveList)
	api.DELETE("/keys/pool/:id", requirePermission(ActionKeysPoolDelete, ScopeOwnStudio), v2HandleKeyPoolDelete)
	api.POST("/keys/pool/assign", requirePermission(ActionKeysPoolAssign, ScopeGlobal), v2HandleKeyPoolAssign)
	api.POST("/keys/rebind", requirePermission(ActionKeysNewapiRebind, ScopeGlobal), v2HandleKeyRebind)
	api.POST("/keys/disable", requirePermission(ActionKeysNewapiDisable, ScopeGlobal), v2HandleKeyDisable)
	api.GET("/keys/export.csv", requirePermission(ActionReportsExport, ScopeOwnStudio), v2HandleKeyExportCSV)
}

// -----------------------------------------------------------------------------
// Upload
// -----------------------------------------------------------------------------

type uploadKeyItem struct {
	Key      string   `json:"key"`
	QuotaUSD *float64 `json:"quota_usd,omitempty"`
}

type uploadKeysBody struct {
	KeyType         string          `json:"key_type"`
	TargetMode      string          `json:"target_mode"`
	TargetProfileID *int64          `json:"target_profile_id,omitempty"`
	Models          string          `json:"models"`
	Group           string          `json:"group"`
	NamePrefix      string          `json:"name_prefix"`
	Keys            []uploadKeyItem `json:"keys"`
}

type uploadResult struct {
	Row    int    `json:"row"`
	Last8  string `json:"key_last8"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	PoolID int64  `json:"pool_id,omitempty"`
}

func v2HandleKeyPoolUpload(c *gin.Context) {
	ctx := v2Ctx(c)
	var body uploadKeysBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if !isValidKeyType(body.KeyType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid key_type"})
		return
	}
	if body.TargetMode != TargetModePoolOnly && body.TargetMode != TargetModeDirectNewapi {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid target_mode"})
		return
	}
	if body.TargetMode == TargetModeDirectNewapi {
		if body.TargetProfileID == nil || *body.TargetProfileID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "target_profile_id required for direct_newapi"})
			return
		}
		if !ctx.has(ActionKeysNewapiUploadDir, ScopeOwnStudio) {
			c.JSON(http.StatusForbidden, gin.H{"error": "missing permission: keys.newapi.upload_direct"})
			return
		}
	} else {
		if !ctx.has(ActionKeysPoolUpload, ScopeOwnStudio) {
			c.JSON(http.StatusForbidden, gin.H{"error": "missing permission: keys.pool.upload"})
			return
		}
	}
	if len(body.Keys) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no keys provided"})
		return
	}
	if len(body.Keys) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many keys (max 500)"})
		return
	}
	// Studio locked for non-any_studio callers (studio_operator).
	studio := strings.TrimSpace(ctx.Studio)
	if ctx.has(ActionKeysPoolUpload, ScopeAnyStudio) {
		// Admin: allow request-supplied studio via ?studio=X or body override.
		if s := strings.TrimSpace(c.Query("studio")); s != "" {
			studio = s
		}
	}
	if studio == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no studio bound to caller; ask an admin to set one"})
		return
	}
	// For direct_newapi mode, check studio ↔ profile accept policy.
	if body.TargetMode == TargetModeDirectNewapi {
		ok, err := studioAccepting(*body.TargetProfileID, studio)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": sanitizeErr(err)})
			return
		}
		if !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "studio is not accepting keys on this profile"})
			return
		}
	}

	now := time.Now().Unix()
	status := PoolStatusAwaiting
	if body.TargetMode == TargetModeDirectNewapi {
		status = PoolStatusPending
	}

	results := make([]uploadResult, 0, len(body.Keys))
	for i, item := range body.Keys {
		raw := strings.TrimSpace(item.Key)
		if raw == "" {
			results = append(results, uploadResult{Row: i, Status: "error", Error: "empty key"})
			continue
		}
		h := hashKey(raw)
		enc, err := encryptRemoteToken(raw)
		if err != nil {
			results = append(results, uploadResult{Row: i, Last8: last8(raw), Status: "error", Error: sanitizeUpstreamMessage(err.Error())})
			continue
		}
		quota := defaultQuotaForKeyType(body.KeyType, item.QuotaUSD)
		tx, err := db.Begin()
		if err != nil {
			results = append(results, uploadResult{Row: i, Last8: last8(raw), Status: "error", Error: sanitizeUpstreamMessage(err.Error())})
			continue
		}
		// Guard: is any active row with this hash already present?
		var active int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM rs_key_pool WHERE key_hash=$1 AND status IN ('awaiting_assignment','pending','active')`,
			h,
		).Scan(&active); err != nil {
			tx.Rollback()
			results = append(results, uploadResult{Row: i, Last8: last8(raw), Status: "error", Error: sanitizeUpstreamMessage(err.Error())})
			continue
		}
		if active > 0 {
			tx.Rollback()
			results = append(results, uploadResult{Row: i, Last8: last8(raw), Status: "duplicate", Error: "key already in pool or active"})
			continue
		}

		var assignedID sql.NullInt64
		if body.TargetMode == TargetModeDirectNewapi {
			assignedID = sql.NullInt64{Int64: *body.TargetProfileID, Valid: true}
		}
		var poolID int64
		var qArg any
		if quota != nil {
			qArg = *quota
		} else {
			qArg = nil
		}
		err = tx.QueryRow(
			`INSERT INTO rs_key_pool
			   (studio, uploaded_by, key_type, key_hash, key_encrypted, key_last8, quota_usd, models, name_prefix, group_name, status, assigned_profile_id, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13) RETURNING id`,
			studio, ctx.UserID, body.KeyType, h, enc, last8(raw), qArg,
			body.Models, body.NamePrefix, body.Group, status, assignedID, now,
		).Scan(&poolID)
		if err != nil {
			tx.Rollback()
			if strings.Contains(err.Error(), "ux_key_pool_active_hash") {
				results = append(results, uploadResult{Row: i, Last8: last8(raw), Status: "duplicate", Error: "key already active elsewhere"})
			} else {
				results = append(results, uploadResult{Row: i, Last8: last8(raw), Status: "error", Error: sanitizeUpstreamMessage(err.Error())})
			}
			continue
		}

		// For direct_newapi: also enqueue into remote_pending_key so the
		// existing scheduler picks it up. pool_size=0 = immediate.
		if body.TargetMode == TargetModeDirectNewapi {
			if err := enqueueRemotePendingFromPool(tx, poolID, *body.TargetProfileID, enc, h, studio, body.Models, body.Group, body.NamePrefix, quota); err != nil {
				tx.Rollback()
				results = append(results, uploadResult{Row: i, Last8: last8(raw), Status: "error", Error: sanitizeUpstreamMessage(err.Error())})
				continue
			}
		}
		if err := tx.Commit(); err != nil {
			results = append(results, uploadResult{Row: i, Last8: last8(raw), Status: "error", Error: sanitizeUpstreamMessage(err.Error())})
			continue
		}
		results = append(results, uploadResult{Row: i, Last8: last8(raw), Status: status, PoolID: poolID})
	}

	c.JSON(http.StatusOK, gin.H{"results": results})
}

// enqueueRemotePendingFromPool writes a corresponding row into
// remote_pending_key so the existing scheduler in remote_newapi.go picks
// the key up. Runs inside the caller's transaction so both tables land
// atomically. pool_size=0 = immediate upload.
func enqueueRemotePendingFromPool(tx *sql.Tx, poolID int64, profileID int64, keyEnc, keyHash, studio, models, group, prefix string, quota *float64) error {
	now := time.Now().Unix()
	var qArg any
	if quota != nil {
		qArg = *quota
	} else {
		qArg = nil
	}
	// Use $10 twice for created_at/updated_at (same as V1 style).
	_, err := tx.Exec(
		`INSERT INTO remote_pending_key
		   (profile_id, key_hash, key_encrypted, quota_usd, name_prefix, group_name, tag, models, priority, pool_size, status, attempts, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,0,0,'pending',0,$9,$9)
		 ON CONFLICT (profile_id, key_hash) DO NOTHING`,
		profileID, keyHash, keyEnc, qArg, prefix, group, studio, models, now,
	)
	_ = poolID
	return err
}

// -----------------------------------------------------------------------------
// List (pool + active)
// -----------------------------------------------------------------------------

func v2HandleKeyPoolList(c *gin.Context) {
	ctx := v2Ctx(c)
	studioFilter := poolStudioFilter(ctx)
	// Status filter query param — default: awaiting + failed for admin,
	// otherwise just awaiting.
	status := c.DefaultQuery("status", "awaiting_assignment,failed")
	rows, err := queryKeyPoolRows(status, studioFilter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": sanitizeErr(err)})
		return
	}
	dtos := make([]KeyPoolDTO, 0, len(rows))
	for _, r := range rows {
		dtos = append(dtos, serializeKeyRow(r, ctx.has(ActionKeysRevealDead, ScopeGlobal)))
	}
	c.JSON(http.StatusOK, gin.H{"keys": dtos})
}

func v2HandleKeyActiveList(c *gin.Context) {
	ctx := v2Ctx(c)
	studioFilter := poolStudioFilter(ctx)
	rows, err := queryKeyPoolRows("active,used", studioFilter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": sanitizeErr(err)})
		return
	}
	dtos := make([]KeyPoolDTO, 0, len(rows))
	for _, r := range rows {
		dtos = append(dtos, serializeKeyRow(r, ctx.has(ActionKeysRevealDead, ScopeGlobal)))
	}
	c.JSON(http.StatusOK, gin.H{"keys": dtos})
}

// poolStudioFilter returns "" when caller has @any_studio (see everything),
// otherwise the caller's bound studio.
func poolStudioFilter(ctx *v2Context) string {
	if ctx.has(ActionKeysPoolView, ScopeAnyStudio) || ctx.has(ActionKeysNewapiView, ScopeAnyStudio) {
		return ""
	}
	return ctx.Studio
}

func queryKeyPoolRows(statusCSV, studio string) ([]KeyPoolRow, error) {
	statuses := splitCSV(statusCSV)
	// Dead-key signal comes from the local mirror of the remote newapi
	// (remote_channel_current.status=3). V2 rs_key_pool rows always live
	// on a remote profile, so the local V1 `channels` table is not a
	// meaningful join target here — its id space is orthogonal to
	// remote_channel_id. If a future V2 variant supports V1-style local
	// channel-backed keys, add a `channels.id`-joined branch here.
	q := `SELECT
	    p.id, p.studio, p.uploaded_by, p.key_type, p.key_last8, p.quota_usd,
	    p.models, p.name_prefix, p.group_name, p.status,
	    p.assigned_profile_id, p.remote_channel_id, p.failed_reason,
	    p.created_at, p.updated_at,
	    COALESCE(rc.status, 0) AS remote_status,
	    p.key_encrypted
	  FROM rs_key_pool p
	  LEFT JOIN remote_channel_current rc
	    ON rc.profile_id = p.assigned_profile_id AND rc.remote_channel_id = p.remote_channel_id
	  WHERE p.status = ANY($1)`
	args := []any{pq.Array(statuses)}
	if studio != "" {
		args = append(args, studio)
		q += " AND p.studio = $2"
	}
	q += " ORDER BY p.created_at DESC, p.id DESC LIMIT 5000"
	rowsDB, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rowsDB.Close()
	out := make([]KeyPoolRow, 0)
	for rowsDB.Next() {
		var r KeyPoolRow
		var quotaNS sql.NullFloat64
		var assigned sql.NullInt64
		var chID sql.NullInt64
		if err := rowsDB.Scan(&r.ID, &r.Studio, &r.UploadedBy, &r.KeyType, &r.KeyLast8, &quotaNS,
			&r.Models, &r.NamePrefix, &r.GroupName, &r.Status,
			&assigned, &chID, &r.FailedReason, &r.CreatedAt, &r.UpdatedAt,
			&r.RemoteStatus, &r.keyEncrypted); err != nil {
			return nil, err
		}
		if quotaNS.Valid {
			v := quotaNS.Float64
			r.QuotaUSD = &v
		}
		if assigned.Valid {
			r.AssignedProfileID = assigned.Int64
		}
		if chID.Valid {
			r.RemoteChannelID = chID.Int64
		}
		out = append(out, r)
	}
	return out, nil
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// -----------------------------------------------------------------------------
// Delete
// -----------------------------------------------------------------------------

func v2HandleKeyPoolDelete(c *gin.Context) {
	ctx := v2Ctx(c)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var studio, status string
	if err := db.QueryRow(`SELECT studio, status FROM rs_key_pool WHERE id=$1`, id).Scan(&studio, &status); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": sanitizeErr(err)})
		return
	}
	if status != PoolStatusAwaiting && status != PoolStatusFailed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "can only delete awaiting_assignment or failed rows"})
		return
	}
	if !ctx.has(ActionKeysPoolDelete, ScopeAnyStudio) && studio != ctx.Studio {
		c.JSON(http.StatusForbidden, gin.H{"error": "outside your studio"})
		return
	}
	if _, err := db.Exec(`DELETE FROM rs_key_pool WHERE id=$1`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": sanitizeErr(err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// -----------------------------------------------------------------------------
// Assign
// -----------------------------------------------------------------------------

type assignBody struct {
	KeyIDs    []int64 `json:"key_ids"`
	ProfileID int64   `json:"profile_id"`
}

func v2HandleKeyPoolAssign(c *gin.Context) {
	var body assignBody
	if err := c.ShouldBindJSON(&body); err != nil || len(body.KeyIDs) == 0 || body.ProfileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	now := time.Now().Unix()
	results := make([]uploadResult, 0, len(body.KeyIDs))

	for _, id := range body.KeyIDs {
		tx, err := db.Begin()
		if err != nil {
			results = append(results, uploadResult{PoolID: id, Status: "error", Error: sanitizeErr(err)})
			continue
		}
		var studio, keyEnc, keyHash, models, prefix, group, keyType, status string
		var quotaNS sql.NullFloat64
		if err := tx.QueryRow(
			`SELECT studio, key_encrypted, key_hash, models, name_prefix, group_name, key_type, status, quota_usd
			   FROM rs_key_pool WHERE id=$1 FOR UPDATE`,
			id,
		).Scan(&studio, &keyEnc, &keyHash, &models, &prefix, &group, &keyType, &status, &quotaNS); err != nil {
			tx.Rollback()
			results = append(results, uploadResult{PoolID: id, Status: "error", Error: sanitizeErr(err)})
			continue
		}
		if status != PoolStatusAwaiting {
			tx.Rollback()
			results = append(results, uploadResult{PoolID: id, Status: "error", Error: "not in awaiting_assignment"})
			continue
		}
		if ok, err := studioAccepting(body.ProfileID, studio); err != nil || !ok {
			tx.Rollback()
			if err != nil {
				results = append(results, uploadResult{PoolID: id, Status: "error", Error: sanitizeErr(err)})
			} else {
				results = append(results, uploadResult{PoolID: id, Status: "error", Error: "studio not accepting on target profile"})
			}
			continue
		}
		var quota *float64
		if quotaNS.Valid {
			v := quotaNS.Float64
			quota = &v
		}
		if err := enqueueRemotePendingFromPool(tx, id, body.ProfileID, keyEnc, keyHash, studio, models, group, prefix, quota); err != nil {
			tx.Rollback()
			results = append(results, uploadResult{PoolID: id, Status: "error", Error: sanitizeErr(err)})
			continue
		}
		if _, err := tx.Exec(
			`UPDATE rs_key_pool SET status=$1, assigned_profile_id=$2, updated_at=$3 WHERE id=$4`,
			PoolStatusPending, body.ProfileID, now, id,
		); err != nil {
			tx.Rollback()
			results = append(results, uploadResult{PoolID: id, Status: "error", Error: sanitizeErr(err)})
			continue
		}
		if err := tx.Commit(); err != nil {
			results = append(results, uploadResult{PoolID: id, Status: "error", Error: sanitizeErr(err)})
			continue
		}
		results = append(results, uploadResult{PoolID: id, Status: PoolStatusPending})
	}

	c.JSON(http.StatusOK, gin.H{"results": results})
}

// -----------------------------------------------------------------------------
// Rebind + Disable — placeholder actions that just flip status. Full remote
// channel deletion is a larger job that touches remote_newapi.go; V2 M2
// only supports "mark as used and re-enqueue".
// -----------------------------------------------------------------------------

func v2HandleKeyRebind(c *gin.Context) {
	var body struct {
		PoolID       int64 `json:"pool_id"`
		NewProfileID int64 `json:"new_profile_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.PoolID <= 0 || body.NewProfileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": sanitizeErr(err)})
		return
	}
	defer tx.Rollback()
	var studio, keyEnc, keyHash, models, prefix, group, status string
	var oldProfile sql.NullInt64
	var quotaNS sql.NullFloat64
	if err := tx.QueryRow(
		`SELECT studio, key_encrypted, key_hash, models, name_prefix, group_name, status, assigned_profile_id, quota_usd
		   FROM rs_key_pool WHERE id=$1 FOR UPDATE`,
		body.PoolID,
	).Scan(&studio, &keyEnc, &keyHash, &models, &prefix, &group, &status, &oldProfile, &quotaNS); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": sanitizeErr(err)})
		return
	}
	if status != PoolStatusActive {
		c.JSON(http.StatusBadRequest, gin.H{"error": "can only rebind active keys"})
		return
	}
	// Mark previous remote_pending_key row as 'used' so history is preserved.
	if _, err := tx.Exec(
		`UPDATE remote_pending_key SET status='used', used_at=$1, updated_at=$1 WHERE profile_id=$2 AND key_hash=$3 AND status='active'`,
		time.Now().Unix(), oldProfile.Int64, keyHash,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": sanitizeErr(err)})
		return
	}
	var quota *float64
	if quotaNS.Valid {
		v := quotaNS.Float64
		quota = &v
	}
	if err := enqueueRemotePendingFromPool(tx, body.PoolID, body.NewProfileID, keyEnc, keyHash, studio, models, group, prefix, quota); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": sanitizeErr(err)})
		return
	}
	if _, err := tx.Exec(
		`UPDATE rs_key_pool SET status=$1, assigned_profile_id=$2, remote_channel_id=NULL, updated_at=$3 WHERE id=$4`,
		PoolStatusPending, body.NewProfileID, time.Now().Unix(), body.PoolID,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": sanitizeErr(err)})
		return
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": sanitizeErr(err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func v2HandleKeyDisable(c *gin.Context) {
	var body struct {
		PoolID int64 `json:"pool_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.PoolID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	now := time.Now().Unix()
	// Just mark as used; the remote channel disable is a follow-up admin
	// action (V1 remote_newapi handlers do this today).
	res, err := db.Exec(
		`UPDATE rs_key_pool SET status=$1, updated_at=$2 WHERE id=$3 AND status IN ($4,$5,$6)`,
		PoolStatusUsed, now, body.PoolID, PoolStatusActive, PoolStatusAwaiting, PoolStatusPending,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": sanitizeErr(err)})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found or already used/failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// -----------------------------------------------------------------------------
// CSV Export
// -----------------------------------------------------------------------------

func v2HandleKeyExportCSV(c *gin.Context) {
	ctx := v2Ctx(c)
	studioFilter := poolStudioFilter(ctx)
	status := c.DefaultQuery("status", "awaiting_assignment,pending,active,used,failed")
	rows, err := queryKeyPoolRows(status, studioFilter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": sanitizeErr(err)})
		return
	}
	canReveal := ctx.has(ActionKeysRevealDead, ScopeGlobal)

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename=keys.csv")
	w := c.Writer
	// Header row.
	fmt.Fprintln(w, "id,studio,key_type,status,remote_status,key_masked,key_plaintext,assigned_profile_id,remote_channel_id,models,name_prefix,group,quota_usd,created_at,updated_at,failed_reason")
	for _, r := range rows {
		dto := serializeKeyRow(r, canReveal)
		plain := ""
		if dto.Key != "" {
			plain = dto.Key
		}
		fmt.Fprintf(w,
			"%d,%s,%s,%s,%d,%s,%s,%d,%d,%s,%s,%s,%s,%d,%d,%s\n",
			dto.ID, csvEscape(dto.Studio), dto.KeyType, dto.Status, dto.RemoteStatus,
			csvEscape(dto.KeyMasked), csvEscape(plain),
			dto.AssignedProfileID, dto.RemoteChannelID,
			csvEscape(dto.Models), csvEscape(dto.NamePrefix), csvEscape(dto.GroupName),
			csvFloat(dto.QuotaUSD), dto.CreatedAt, dto.UpdatedAt, csvEscape(dto.FailedReason),
		)
	}
}

func csvEscape(s string) string {
	if !strings.ContainsAny(s, ",\"\n\r") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func csvFloat(f *float64) string {
	if f == nil {
		return ""
	}
	return strconv.FormatFloat(*f, 'f', 4, 64)
}
