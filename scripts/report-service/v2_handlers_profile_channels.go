package main

// Per-profile channel drilldown: /api/v2/profiles/:id/channels.
//
// Returns every channel on the remote profile (not just V2-uploaded ones),
// LEFT JOINed with rs_key_pool to enrich with V2 metadata (uploader,
// key_type, quota, key_last8) when the channel came in through V2.
//
// The wire shape is the same KeyPoolDTO used by /keys/pool, /keys/active,
// and /usage — the operator sees "keys under this site" without caring
// whether the row originated in V2's pool or was already on the remote.
// Key material visibility still runs through the single serializeKeyRow.

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// registerV2ProfileChannelsRoute mounts the drilldown on the /api/v2 group.
// Called from v2_router.go.
func registerV2ProfileChannelsRoute(api *gin.RouterGroup) {
	api.GET("/profiles/:id/channels",
		requirePermission(ActionKeysNewapiView, ScopeOwnStudio),
		v2HandleProfileChannels)
}

func v2HandleProfileChannels(c *gin.Context) {
	ctx := v2Ctx(c)
	profileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || profileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid profile id"})
		return
	}

	// Confirm the profile exists so a bogus id returns 404 rather than an
	// empty list (easier for the frontend to differentiate).
	var profileName string
	if err := db.QueryRow(`SELECT name FROM remote_newapi_profile WHERE id=$1`, profileID).Scan(&profileName); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Non-any_studio callers only see channels tagged with their own studio.
	// tag on remote_channel_current mirrors channels.tag semantics from V1.
	studio := ""
	if !ctx.has(ActionKeysNewapiView, ScopeAnyStudio) {
		studio = ctx.Studio
	}

	rows, err := queryProfileChannels(profileID, studio, profileName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	canReveal := ctx.has(ActionKeysRevealDead, ScopeGlobal)
	dtos := make([]KeyPoolDTO, 0, len(rows))
	for _, r := range rows {
		dtos = append(dtos, serializeKeyRow(r, canReveal))
	}
	c.JSON(http.StatusOK, gin.H{
		"profile": gin.H{"id": profileID, "name": profileName},
		"keys":    dtos,
	})
}

// queryProfileChannels returns unified rows for every channel on the
// profile. rs_key_pool is LEFT JOINed so V2-uploaded rows carry their
// enriched metadata (uploader, key_type, key_last8, ciphertext for
// dead-key reveal); pre-V2 channels appear with ID=0, key_last8='', no
// ciphertext (so plaintext can never be revealed for them).
func queryProfileChannels(profileID int64, studio, profileName string) ([]KeyPoolRow, error) {
	q := `SELECT
	    COALESCE(p.id, 0)                       AS pool_id,
	    COALESCE(p.studio, COALESCE(rc.tag,'')) AS studio,
	    COALESCE(p.uploaded_by, 0)              AS uploaded_by,
	    COALESCE(p.key_type, '')                AS key_type,
	    COALESCE(p.key_last8, '')               AS key_last8,
	    p.quota_usd                             AS quota_usd,
	    COALESCE(rc.models, '')                 AS models,
	    COALESCE(p.name_prefix, '')             AS name_prefix,
	    COALESCE(rc."group", '')                AS group_name,
	    CASE
	        WHEN p.status IS NOT NULL THEN p.status
	        WHEN rc.status = 2            THEN 'used'
	        ELSE 'active'
	    END                                     AS status,
	    rc.profile_id                           AS assigned_profile_id,
	    rc.remote_channel_id                    AS remote_channel_id,
	    COALESCE(p.failed_reason, '')           AS failed_reason,
	    COALESCE(p.created_at, COALESCE(rc.created_time, 0)) AS created_at,
	    rc.updated_at                           AS updated_at,
	    COALESCE(rc.status, 0)                  AS remote_status,
	    COALESCE(rc.name, '')                   AS channel_name,
	    COALESCE(rc.used_quota, 0)              AS used_quota_raw,
	    COALESCE(p.key_encrypted, '')           AS key_encrypted
	  FROM remote_channel_current rc
	  LEFT JOIN rs_key_pool p
	    ON p.assigned_profile_id = rc.profile_id
	   AND p.remote_channel_id   = rc.remote_channel_id
	  WHERE rc.profile_id = $1`
	args := []any{profileID}
	if studio != "" {
		args = append(args, studio)
		q += " AND rc.tag = $2"
	}
	q += " ORDER BY rc.remote_channel_id DESC LIMIT 5000"

	rowsDB, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rowsDB.Close()

	out := make([]KeyPoolRow, 0)
	for rowsDB.Next() {
		var r KeyPoolRow
		var quotaNS sql.NullFloat64
		var updatedAt sql.NullInt64
		if err := rowsDB.Scan(
			&r.ID, &r.Studio, &r.UploadedBy, &r.KeyType, &r.KeyLast8, &quotaNS,
			&r.Models, &r.NamePrefix, &r.GroupName, &r.Status,
			&r.AssignedProfileID, &r.RemoteChannelID, &r.FailedReason,
			&r.CreatedAt, &updatedAt, &r.RemoteStatus,
			&r.ChannelName, &r.UsedQuotaRaw, &r.keyEncrypted,
		); err != nil {
			return nil, err
		}
		if quotaNS.Valid {
			v := quotaNS.Float64
			r.QuotaUSD = &v
		}
		if updatedAt.Valid {
			r.UpdatedAt = updatedAt.Int64
		}
		r.ProfileName = profileName
		out = append(out, r)
	}
	return out, nil
}
