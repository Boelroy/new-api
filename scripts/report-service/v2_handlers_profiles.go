package main

// V2 remote newapi profile management.
//
// Contract vs V1 handleRemoteProfileList:
//   - Slim variant (any logged-in user): id, name, default_models,
//     default_group, has_access_token, accepts_studio (for the caller's
//     studio if bound).
//   - Full variant (requires remote_newapi.profile.manage): everything
//     except the encrypted access token itself. `has_access_token: true`
//     indicates a token is set; a POST/PATCH is the only way to change it.
//   - access_token_enc is NEVER included in any response.

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func registerV2ProfilesRoutes(api *gin.RouterGroup) {
	api.GET("/profiles", v2HandleProfilesList)
	api.POST("/profiles", requirePermission(ActionRemoteProfileManage, ScopeGlobal), v2HandleProfileCreate)
	api.PATCH("/profiles/:id", requirePermission(ActionRemoteProfileManage, ScopeGlobal), v2HandleProfileUpdate)
	api.DELETE("/profiles/:id", requirePermission(ActionRemoteProfileManage, ScopeGlobal), v2HandleProfileDelete)
	// Drilldown: list all channels on a profile (unified V2 + pre-V2 rows).
	registerV2ProfileChannelsRoute(api)
}

type v2ProfileSlim struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	DefaultModels   string `json:"default_models"`
	DefaultGroup    string `json:"default_group"`
	HasAccessToken  bool   `json:"has_access_token"`
	AcceptsStudio   *bool  `json:"accepts_studio,omitempty"`
}

type v2ProfileFull struct {
	v2ProfileSlim
	Host            string `json:"host"`
	UserID          int64  `json:"user_id"`
	PoolIntervalSec int    `json:"pool_interval_sec"`
	PoolBatchSize   int    `json:"pool_batch_size"`
	AutoMode        bool   `json:"auto_mode"`
	RPMBase         int    `json:"rpm_base"`
	RPMMin          int    `json:"rpm_min"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

func v2HandleProfilesList(c *gin.Context) {
	ctx := v2Ctx(c)
	full := ctx.has(ActionRemoteProfileManage, ScopeGlobal)
	rowsDB, err := db.Query(
		`SELECT id, name, host, user_id, access_token_enc, default_models, default_group,
		        pool_interval_sec, pool_batch_size, auto_mode, rpm_base, rpm_min,
		        created_at, updated_at
		   FROM remote_newapi_profile ORDER BY id ASC`,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rowsDB.Close()

	slim := make([]v2ProfileSlim, 0)
	fullRows := make([]v2ProfileFull, 0)
	for rowsDB.Next() {
		var id, uid int64
		var name, host, enc, defModels, defGroup string
		var pInt, pBatch, rbase, rmin int
		var auto bool
		var ca, ua int64
		if err := rowsDB.Scan(&id, &name, &host, &uid, &enc, &defModels, &defGroup, &pInt, &pBatch, &auto, &rbase, &rmin, &ca, &ua); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s := v2ProfileSlim{
			ID:             id,
			Name:           name,
			DefaultModels:  defModels,
			DefaultGroup:   defGroup,
			HasAccessToken: enc != "",
		}
		if ctx.Studio != "" {
			ok, _ := studioAccepting(id, ctx.Studio)
			s.AcceptsStudio = &ok
		}
		if full {
			fullRows = append(fullRows, v2ProfileFull{
				v2ProfileSlim:   s,
				Host:            host,
				UserID:          uid,
				PoolIntervalSec: pInt,
				PoolBatchSize:   pBatch,
				AutoMode:        auto,
				RPMBase:         rbase,
				RPMMin:          rmin,
				CreatedAt:       ca,
				UpdatedAt:       ua,
			})
		} else {
			slim = append(slim, s)
		}
	}
	if full {
		c.JSON(http.StatusOK, gin.H{"profiles": fullRows})
		return
	}
	c.JSON(http.StatusOK, gin.H{"profiles": slim})
}

type profileWriteBody struct {
	Name            string `json:"name"`
	Host            string `json:"host"`
	UserID          int64  `json:"user_id"`
	AccessToken     string `json:"access_token,omitempty"`
	DefaultModels   string `json:"default_models"`
	DefaultGroup    string `json:"default_group"`
	PoolIntervalSec int    `json:"pool_interval_sec"`
	PoolBatchSize   int    `json:"pool_batch_size"`
	AutoMode        bool   `json:"auto_mode"`
	RPMBase         int    `json:"rpm_base"`
	RPMMin          int    `json:"rpm_min"`
}

func v2HandleProfileCreate(c *gin.Context) {
	var body profileWriteBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.Host = strings.TrimSpace(body.Host)
	if body.Name == "" || body.Host == "" || body.AccessToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name, host, access_token required"})
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
		`INSERT INTO remote_newapi_profile
		   (name, host, user_id, access_token_enc, default_models, default_group,
		    pool_interval_sec, pool_batch_size, auto_mode, rpm_base, rpm_min,
		    created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6, COALESCE(NULLIF($7,0),60), COALESCE(NULLIF($8,0),2), $9, COALESCE(NULLIF($10,0),150), COALESCE(NULLIF($11,0),50), $12, $12)
		 RETURNING id`,
		body.Name, body.Host, body.UserID, enc, body.DefaultModels, body.DefaultGroup,
		body.PoolIntervalSec, body.PoolBatchSize, body.AutoMode, body.RPMBase, body.RPMMin, now,
	).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			c.JSON(http.StatusConflict, gin.H{"error": "profile name already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id})
}

func v2HandleProfileUpdate(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body struct {
		Name            *string `json:"name,omitempty"`
		Host            *string `json:"host,omitempty"`
		UserID          *int64  `json:"user_id,omitempty"`
		AccessToken     *string `json:"access_token,omitempty"`
		DefaultModels   *string `json:"default_models,omitempty"`
		DefaultGroup    *string `json:"default_group,omitempty"`
		PoolIntervalSec *int    `json:"pool_interval_sec,omitempty"`
		PoolBatchSize   *int    `json:"pool_batch_size,omitempty"`
		AutoMode        *bool   `json:"auto_mode,omitempty"`
		RPMBase         *int    `json:"rpm_base,omitempty"`
		RPMMin          *int    `json:"rpm_min,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	// Build a single dynamic UPDATE so partial-failure state can't leak.
	// Each addSet appends "col=$N" to the SET list and its value to args.
	sets := []string{}
	args := []any{}
	addSet := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, col+"=$"+strconv.Itoa(len(args)))
	}
	if body.Name != nil {
		addSet("name", strings.TrimSpace(*body.Name))
	}
	if body.Host != nil {
		addSet("host", strings.TrimSpace(*body.Host))
	}
	if body.UserID != nil {
		addSet("user_id", *body.UserID)
	}
	if body.AccessToken != nil && *body.AccessToken != "" {
		enc, err := encryptRemoteToken(*body.AccessToken)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "encrypt: " + sanitizeUpstreamMessage(err.Error())})
			return
		}
		addSet("access_token_enc", enc)
	}
	if body.DefaultModels != nil {
		addSet("default_models", *body.DefaultModels)
	}
	if body.DefaultGroup != nil {
		addSet("default_group", *body.DefaultGroup)
	}
	if body.PoolIntervalSec != nil {
		addSet("pool_interval_sec", *body.PoolIntervalSec)
	}
	if body.PoolBatchSize != nil {
		addSet("pool_batch_size", *body.PoolBatchSize)
	}
	if body.AutoMode != nil {
		addSet("auto_mode", *body.AutoMode)
	}
	if body.RPMBase != nil {
		addSet("rpm_base", *body.RPMBase)
	}
	if body.RPMMin != nil {
		addSet("rpm_min", *body.RPMMin)
	}
	if len(sets) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}
	addSet("updated_at", time.Now().Unix())
	args = append(args, id)
	q := "UPDATE remote_newapi_profile SET " + strings.Join(sets, ", ") + " WHERE id=$" + strconv.Itoa(len(args))
	res, err := db.Exec(q, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": sanitizeUpstreamMessage(err.Error())})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func v2HandleProfileDelete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	// Cascade: refuse if any live rs_key_pool row points at this profile.
	var live int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM rs_key_pool WHERE assigned_profile_id=$1 AND status IN ('pending','active')`,
		id,
	).Scan(&live); err != nil && err != sql.ErrNoRows {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if live > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile has active pool assignments"})
		return
	}
	if _, err := db.Exec(`DELETE FROM remote_newapi_profile WHERE id=$1`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
