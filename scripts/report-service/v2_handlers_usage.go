package main

// V2 usage aggregation.
//
// /api/v2/usage returns per-key usage numbers, filtered by scope. The wire
// format uses KeyPoolDTO so any key material still routes through
// serializeKeyRow (no plaintext except dead + reveal_dead).
//
// Data sources:
//   - remote_channel_current.used_quota  (current running total, refreshed
//     by V1's snapshot loop every 15 min + interactive fetch)
//   - remote_channel_snapshot            (time series, for sparklines —
//     exposed via the existing /api/remote-newapi/snapshots V1 endpoint,
//     not re-implemented here)
//
// The GET returns a list of per-key rows, joined with the live-quota
// mirror. Aggregation across studios / profiles / key_types is done client
// side from the row set so we don't have to bake the sum into SQL.

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

func registerV2UsageRoutes(api *gin.RouterGroup) {
	api.GET("/usage", requirePermission(ActionUsageView, ScopeOwnStudio), v2HandleUsage)
}

type usageRow struct {
	KeyPoolDTO
	UsedQuota  int64   `json:"used_quota_raw"`
	UsedUSD    float64 `json:"used_usd"`
}

func v2HandleUsage(c *gin.Context) {
	ctx := v2Ctx(c)
	studioFilter := ""
	if !ctx.has(ActionUsageView, ScopeAnyStudio) {
		studioFilter = ctx.Studio
	}
	// Optional filters.
	uploadedByFilter := int64(0)
	if strings.EqualFold(c.Query("mine"), "true") {
		uploadedByFilter = ctx.UserID
	}
	keyTypeFilter := c.Query("key_type")
	profileFilter := c.Query("profile_id")
	// Base query: rs_key_pool LEFT JOIN remote_channel_current.
	q := `SELECT
	    p.id, p.studio, p.uploaded_by, p.key_type, p.key_last8, p.quota_usd,
	    p.models, p.name_prefix, p.group_name, p.status,
	    p.assigned_profile_id, p.remote_channel_id, p.failed_reason,
	    p.created_at, p.updated_at,
	    COALESCE(rc.status, 0), COALESCE(rc.used_quota, 0),
	    p.key_encrypted
	  FROM rs_key_pool p
	  LEFT JOIN remote_channel_current rc
	    ON rc.profile_id = p.assigned_profile_id AND rc.remote_channel_id = p.remote_channel_id
	  WHERE p.status <> 'awaiting_assignment'`
	args := []any{}
	if studioFilter != "" {
		args = append(args, studioFilter)
		q += " AND p.studio = $1"
	}
	if uploadedByFilter > 0 {
		args = append(args, uploadedByFilter)
		q += " AND p.uploaded_by = $" + strconv.Itoa(len(args))
	}
	if keyTypeFilter != "" {
		args = append(args, keyTypeFilter)
		q += " AND p.key_type = $" + strconv.Itoa(len(args))
	}
	if profileFilter != "" {
		args = append(args, profileFilter)
		q += " AND p.assigned_profile_id = $" + strconv.Itoa(len(args))
	}
	// Accept a status filter (CSV) so admin dashboards can drill down.
	if s := c.Query("status"); s != "" {
		statuses := splitCSV(s)
		args = append(args, pq.Array(statuses))
		q += " AND p.status = ANY($" + strconv.Itoa(len(args)) + ")"
	}
	q += " ORDER BY p.created_at DESC LIMIT 10000"

	rowsDB, err := db.Query(q, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rowsDB.Close()

	canReveal := ctx.has(ActionKeysRevealDead, ScopeGlobal)
	out := make([]usageRow, 0)
	for rowsDB.Next() {
		var r KeyPoolRow
		var quotaNS sql.NullFloat64
		var assigned sql.NullInt64
		var chID sql.NullInt64
		var usedQuota int64
		if err := rowsDB.Scan(&r.ID, &r.Studio, &r.UploadedBy, &r.KeyType, &r.KeyLast8, &quotaNS,
			&r.Models, &r.NamePrefix, &r.GroupName, &r.Status,
			&assigned, &chID, &r.FailedReason, &r.CreatedAt, &r.UpdatedAt,
			&r.RemoteStatus, &usedQuota, &r.keyEncrypted); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
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
		dto := serializeKeyRow(r, canReveal)
		row := usageRow{
			KeyPoolDTO: dto,
			UsedQuota:  usedQuota,
			UsedUSD:    roundTo(float64(usedQuota)/quotaPerUnit, 4),
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"rows": out})
}

