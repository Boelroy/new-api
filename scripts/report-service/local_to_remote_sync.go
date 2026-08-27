package main

// Push a LOCAL new-api channel (a studio-uploaded key) UP to a remote new-api
// instance as a channel — the mirror image of local_channel_sync.go.
//
// Studios upload keys into the local pool (local_pool.go), which land as rows
// in the local `channels` table tagged with the studio. This takes one such
// local channel, reads its plaintext key / models / group / type / quota, and
// creates the matching channel on a chosen remote profile via the exact same
// upload path the interactive batch-create uses (uploadOneKeyToRemote). The
// studio tag is carried onto the remote channel so the remote snapshot mirror
// already attributes usage back to the studio.
//
// local_remote_sync records the local→remote mapping so re-syncing is
// idempotent and the studio usage view can join a local key to the remote
// channel's used_quota. Sync (syncable list + create) is Admin+ only; the
// usage view is readable by studio operators, server-side scoped to their own
// studio exactly like /api/allkeys/data.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// localSyncableChannel is one row of the "which local channels can I push up"
// list, resolved against a specific target profile so already_synced reflects
// that target.
type localSyncableChannel struct {
	LocalChannelID  int64    `json:"local_channel_id"`
	Name            string   `json:"name"`
	Studio          string   `json:"studio"`
	Group           string   `json:"group"`
	Models          string   `json:"models"`
	ChannelType     int      `json:"channel_type"`
	ChannelTypeName string   `json:"channel_type_name"`
	KeyMasked       string   `json:"key_masked"`
	QuotaUSD        *float64 `json:"quota_usd,omitempty"`
	UsedUSD         float64  `json:"used_usd"`
	Status          int      `json:"status"`
	AlreadySynced   bool     `json:"already_synced"`
	RemoteChannelID int64    `json:"remote_channel_id"`
}

// handleLocalToRemoteSyncable (GET /api/local-remote-sync/syncable) lists the
// local channels eligible to push to profile_id, with already_synced state for
// that target. Optional studio / group filters narrow the list.
func handleLocalToRemoteSyncable(c *gin.Context) {
	profileID, _ := strconv.ParseInt(c.Query("profile_id"), 10, 64)
	if profileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	args := []any{profileID}
	conds := []string{"c.status = 1"}
	if s := strings.TrimSpace(c.Query("studio")); s != "" {
		args = append(args, s)
		conds = append(conds, fmt.Sprintf("c.tag = $%d", len(args)))
	}
	if g := strings.TrimSpace(c.Query("group")); g != "" {
		args = append(args, g)
		conds = append(conds, fmt.Sprintf(`c."group" = $%d`, len(args)))
	}
	q := `
		SELECT c.id, COALESCE(c.name,''), COALESCE(c.type,0), COALESCE(c.tag,''),
		       COALESCE(c."group",''), COALESCE(c.models,''), c.key,
		       COALESCE(c.used_quota,0), qk.quota_usd,
		       COALESCE(ls.remote_channel_id,0), (ls.local_channel_id IS NOT NULL)
		  FROM channels c
		  LEFT JOIN report_key_quotas qk ON qk.channel_id = c.id
		  LEFT JOIN local_remote_sync ls ON ls.local_channel_id = c.id AND ls.profile_id = $1
		 WHERE ` + strings.Join(conds, " AND ") + `
		 ORDER BY c.id DESC`
	rows, err := db.Query(q, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	items := make([]localSyncableChannel, 0)
	for rows.Next() {
		var it localSyncableChannel
		var key string
		var usedQuota int64
		var quotaUSD sql.NullFloat64
		if err := rows.Scan(&it.LocalChannelID, &it.Name, &it.ChannelType, &it.Studio,
			&it.Group, &it.Models, &key, &usedQuota, &quotaUSD,
			&it.RemoteChannelID, &it.AlreadySynced); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		it.Status = 1
		it.ChannelTypeName = channelTypeLabel(it.ChannelType)
		it.KeyMasked = maskKey(key)
		it.UsedUSD = roundTo(float64(usedQuota)/quotaPerUnit, 4)
		if quotaUSD.Valid {
			v := roundTo(quotaUSD.Float64, 4)
			it.QuotaUSD = &v
		}
		items = append(items, it)
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// localToRemoteResult is one item's outcome in the batch sync response.
type localToRemoteResult struct {
	LocalChannelID  int64  `json:"local_channel_id"`
	ProfileID       int64  `json:"profile_id"`
	OK              bool   `json:"ok"`
	Skipped         bool   `json:"skipped"`
	RemoteChannelID int64  `json:"remote_channel_id,omitempty"`
	Error           string `json:"error,omitempty"`
}

// handleLocalToRemoteSyncCreate (POST /api/local-remote-sync) pushes a batch of
// local channels up to one remote profile. Each item is independent — one
// failure doesn't abort the rest.
func handleLocalToRemoteSyncCreate(c *gin.Context) {
	var body struct {
		ProfileID int64  `json:"profile_id"`
		Group     string `json:"group"` // optional: override the remote group for the whole batch
		Force     bool   `json:"force"` // re-upload even if already synced to this profile
		Items     []struct {
			LocalChannelID int64 `json:"local_channel_id"`
		} `json:"items"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if body.ProfileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	if len(body.Items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "items is required"})
		return
	}
	const maxItems = 200
	if len(body.Items) > maxItems {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("too many items (max %d)", maxItems)})
		return
	}

	callerID := int64(0)
	if v, ok := c.Get("user_id"); ok {
		if uid, ok := v.(int64); ok {
			callerID = uid
		}
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(60+len(body.Items)*10)*time.Second)
	defer cancel()

	results := make([]localToRemoteResult, 0, len(body.Items))
	okCount := 0
	for _, it := range body.Items {
		res := localToRemoteResult{LocalChannelID: it.LocalChannelID, ProfileID: body.ProfileID}
		remoteID, skipped, err := syncLocalChannelToRemote(ctx, it.LocalChannelID, body.ProfileID, callerID, strings.TrimSpace(body.Group), body.Force)
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
			res.Skipped = skipped
			res.RemoteChannelID = remoteID
			okCount++
		}
		results = append(results, res)
	}
	c.JSON(http.StatusOK, gin.H{"results": results, "ok": okCount, "total": len(body.Items)})
}

// syncLocalChannelToRemote reads one local channel and creates the matching
// channel on the remote profile. groupOverride (when non-empty) replaces the
// local channel's group on the remote. Returns skipped=true (with the existing
// remote channel id) when this local channel — or the same key — already went
// up to this profile and force is false, so a plain re-sync is a no-op. With
// force=true it re-uploads regardless (e.g. to push into a different group).
func syncLocalChannelToRemote(ctx context.Context, localChannelID, profileID, callerID int64, groupOverride string, force bool) (remoteChannelID int64, skipped bool, err error) {
	var name, key, group, models, tag string
	var chType int
	var quotaUSD sql.NullFloat64
	row := db.QueryRow(`
		SELECT COALESCE(c.name,''), c.key, COALESCE(c.type,0), COALESCE(c.tag,''),
		       COALESCE(c."group",''), COALESCE(c.models,''), qk.quota_usd
		  FROM channels c
		  LEFT JOIN report_key_quotas qk ON qk.channel_id = c.id
		 WHERE c.id = $1`, localChannelID)
	switch e := row.Scan(&name, &key, &chType, &tag, &group, &models, &quotaUSD); e {
	case nil:
	case sql.ErrNoRows:
		return 0, false, fmt.Errorf("local channel %d not found", localChannelID)
	default:
		return 0, false, e
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, false, fmt.Errorf("local channel %d has an empty key", localChannelID)
	}
	models = strings.TrimSpace(models)
	if models == "" {
		return 0, false, fmt.Errorf("local channel %d has no models", localChannelID)
	}
	if strings.TrimSpace(groupOverride) != "" {
		group = strings.TrimSpace(groupOverride)
	}
	if strings.TrimSpace(group) == "" {
		group = "default"
	}
	if chType == 0 {
		chType = 14
	}
	keyHash := pendingKeyHash(key)

	// Dedupe (skipped unless force): this local channel already synced to this
	// profile, or the same physical key already went up to this profile under
	// another local channel. force re-uploads regardless — used to push into a
	// different group or re-create a deleted remote channel.
	if !force {
		var existingRemote int64
		switch e := db.QueryRow(`
			SELECT remote_channel_id FROM local_remote_sync
			 WHERE profile_id = $1 AND (local_channel_id = $2 OR key_hash = $3) AND remote_channel_id > 0
			 ORDER BY remote_channel_id DESC LIMIT 1`,
			profileID, localChannelID, keyHash).Scan(&existingRemote); e {
		case nil:
			upsertLocalRemoteSyncMap(localChannelID, profileID, existingRemote, tag, keyHash, callerID)
			return existingRemote, true, nil
		case sql.ErrNoRows:
			// fall through to upload
		default:
			return 0, false, e
		}
	}

	host, userID, token, err := loadRemoteProfileByID(profileID)
	if err != nil {
		return 0, false, err
	}

	prefix := strings.TrimSpace(tag)
	if prefix == "" {
		prefix = "l2r"
	}
	namePrefix := fmt.Sprintf("%s-%d", prefix, localChannelID)

	var quotaPtr *float64
	if quotaUSD.Valid && quotaUSD.Float64 > 0 {
		v := quotaUSD.Float64
		quotaPtr = &v
	}

	remoteChannelID, err = uploadOneKeyToRemote(ctx, uploadOneKeyParams{
		Host:       host,
		Token:      token,
		UserID:     userID,
		ProfileID:  profileID,
		Key:        key,
		NamePrefix: namePrefix,
		Type:       chType,
		Models:     models,
		Group:      group,
		Tag:        tag,
		QuotaUSD:   quotaPtr,
	})
	if err != nil {
		return 0, false, err
	}

	upsertLocalRemoteSyncMap(localChannelID, profileID, remoteChannelID, tag, keyHash, callerID)
	return remoteChannelID, false, nil
}

// upsertLocalRemoteSyncMap records a local→remote channel mapping. Best effort;
// a failure here doesn't fail the caller (the remote channel already exists).
func upsertLocalRemoteSyncMap(localChannelID, profileID, remoteChannelID int64, studio, keyHash string, callerID int64) {
	now := time.Now().Unix()
	_, _ = db.Exec(`
		INSERT INTO local_remote_sync
		(local_channel_id, profile_id, remote_channel_id, studio, key_hash, status, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'active', $6, $7, $7)
		ON CONFLICT (local_channel_id, profile_id)
		DO UPDATE SET remote_channel_id=$3, studio=$4, key_hash=$5, status='active', updated_at=$7`,
		localChannelID, profileID, remoteChannelID, studio, keyHash, callerID, now)
}

// localRemoteUsageRow is one linked (local key ↔ remote channel) pair with its
// remote consumption, for the studio-facing usage view.
type localRemoteUsageRow struct {
	LocalChannelID     int64    `json:"local_channel_id"`
	ProfileID          int64    `json:"profile_id"`
	ProfileName        string   `json:"profile_name"`
	RemoteChannelID    int64    `json:"remote_channel_id"`
	Studio             string   `json:"studio"`
	Name               string   `json:"name"`
	RemoteStatus       int      `json:"remote_status"`
	RemoteUsedUSD      float64  `json:"remote_used_usd"`
	RemoteQuotaUSD     *float64 `json:"remote_quota_usd,omitempty"`
	RemoteRemainingUSD *float64 `json:"remote_remaining_usd,omitempty"`
	RemoteRecentUSD    *float64 `json:"remote_recent_usd,omitempty"`
	LocalUsedUSD       float64  `json:"local_used_usd"`
	UpdatedAt          int64    `json:"updated_at"`
}

// handleLocalToRemoteUsage (GET /api/local-remote-sync/usage) returns the
// remote consumption of each key a studio pushed up. Studio operators (role
// below admin) are server-side scoped to their own studio — the same gate
// /api/allkeys/data uses; admin+ may pass ?studio= and ?profile_id= to filter.
// window_sec (default 3600) sets the "recent burn" delta window against the
// already-captured remote snapshots.
func handleLocalToRemoteUsage(c *gin.Context) {
	studioFilter := strings.TrimSpace(c.Query("studio"))
	roleAny, _ := c.Get("role")
	if role, _ := roleAny.(int); role > 0 && role < minAdminRole {
		studioAny, _ := c.Get("studio")
		studio, _ := studioAny.(string)
		if studio == "" {
			c.JSON(http.StatusOK, gin.H{"items": []localRemoteUsageRow{}})
			return
		}
		studioFilter = studio // force own studio, ignore any query value
	}

	windowSec, _ := strconv.ParseInt(c.Query("window_sec"), 10, 64)
	if windowSec <= 0 {
		windowSec = 3600
	}
	windowStart := time.Now().Unix() - windowSec

	args := []any{windowStart}
	conds := []string{"ls.remote_channel_id > 0"}
	if studioFilter != "" {
		args = append(args, studioFilter)
		conds = append(conds, fmt.Sprintf("ls.studio = $%d", len(args)))
	}
	if pid, _ := strconv.ParseInt(c.Query("profile_id"), 10, 64); pid > 0 {
		args = append(args, pid)
		conds = append(conds, fmt.Sprintf("ls.profile_id = $%d", len(args)))
	}
	q := `
		SELECT ls.local_channel_id, ls.profile_id, COALESCE(p.name,''), ls.remote_channel_id,
		       ls.studio, COALESCE(cc.name,''), COALESCE(cc.status,0), COALESCE(cc.used_quota,0),
		       m.quota_usd, COALESCE(lc.used_quota,0), ls.updated_at,
		       (SELECT s.used_quota FROM remote_channel_snapshot s
		         WHERE s.profile_id = ls.profile_id AND s.remote_channel_id = ls.remote_channel_id
		           AND s.captured_at >= $1
		         ORDER BY s.captured_at ASC LIMIT 1)
		  FROM local_remote_sync ls
		  JOIN remote_newapi_profile p ON p.id = ls.profile_id
		  LEFT JOIN remote_channel_current cc ON cc.profile_id = ls.profile_id AND cc.remote_channel_id = ls.remote_channel_id
		  LEFT JOIN remote_channel_meta m ON m.profile_id = ls.profile_id AND m.remote_channel_id = ls.remote_channel_id
		  LEFT JOIN channels lc ON lc.id = ls.local_channel_id
		 WHERE ` + strings.Join(conds, " AND ") + `
		 ORDER BY ls.updated_at DESC`
	rows, err := db.Query(q, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	items := make([]localRemoteUsageRow, 0)
	for rows.Next() {
		var it localRemoteUsageRow
		var remoteUsed, localUsed int64
		var quotaUSD sql.NullFloat64
		var baseUsed sql.NullInt64
		if err := rows.Scan(&it.LocalChannelID, &it.ProfileID, &it.ProfileName, &it.RemoteChannelID,
			&it.Studio, &it.Name, &it.RemoteStatus, &remoteUsed,
			&quotaUSD, &localUsed, &it.UpdatedAt, &baseUsed); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		it.RemoteUsedUSD = roundTo(float64(remoteUsed)/quotaPerUnit, 4)
		it.LocalUsedUSD = roundTo(float64(localUsed)/quotaPerUnit, 4)
		if quotaUSD.Valid {
			capUSD := roundTo(quotaUSD.Float64, 4)
			it.RemoteQuotaUSD = &capUSD
			remaining := roundTo(capUSD-it.RemoteUsedUSD, 4)
			it.RemoteRemainingUSD = &remaining
		}
		if baseUsed.Valid {
			recent := roundTo(float64(remoteUsed-baseUsed.Int64)/quotaPerUnit, 4)
			if recent < 0 {
				recent = 0 // channel reset / re-created; don't show negative burn
			}
			it.RemoteRecentUSD = &recent
		}
		items = append(items, it)
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "window_sec": windowSec})
}
