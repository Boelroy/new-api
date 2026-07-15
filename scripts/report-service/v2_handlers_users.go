package main

// V2 user + role-assignment endpoints.
//
// User CRUD reuses the V1 authUser struct + rs_auth_user table verbatim.
// V2 adds a `roles: [role_id, ...]` view on top and role assign/unassign
// endpoints that enforce the ladder guard.
//
// Password + status handlers are thin re-uses of V1 helpers via direct SQL
// (V1 handlers rely on gin.Context for role from JWT; we don't want to mix
// V1 middleware into V2 routes, so the logic is duplicated here with the
// V2 v2Context source of truth).

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

type v2UserRow struct {
	authUser
	Roles       []int64 `json:"roles"`
	MaxLevel    int     `json:"max_level"`
	RoleNames   []string `json:"role_names"`
}

// loadUserRoles fills the Roles slice + max level + names on a user row.
func loadUserRoles(u *v2UserRow) error {
	rows, err := db.Query(
		`SELECT r.id, r.name, r.level FROM rs_user_role ur JOIN rs_role r ON r.id=ur.role_id WHERE ur.user_id=$1 ORDER BY r.level DESC`,
		u.ID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	u.Roles = make([]int64, 0)
	u.RoleNames = make([]string, 0)
	for rows.Next() {
		var id int64
		var name string
		var level int
		if err := rows.Scan(&id, &name, &level); err != nil {
			return err
		}
		u.Roles = append(u.Roles, id)
		u.RoleNames = append(u.RoleNames, name)
		if level > u.MaxLevel {
			u.MaxLevel = level
		}
	}
	return nil
}

func v2HandleUserList(c *gin.Context) {
	rows, err := db.Query(
		`SELECT id, username, role, studio, status, disabled_at, created_at, updated_at FROM rs_auth_user ORDER BY id ASC`,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := make([]v2UserRow, 0)
	for rows.Next() {
		u, err := authUserFromRow(rows)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		row := v2UserRow{authUser: u}
		if err := loadUserRoles(&row); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"users": out})
}

func v2HandleUserCreate(c *gin.Context) {
	ctx := v2Ctx(c)
	var body struct {
		Username string  `json:"username"`
		Password string  `json:"password"`
		Studio   string  `json:"studio"`
		Roles    []int64 `json:"roles"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	body.Studio = strings.TrimSpace(body.Studio)
	if body.Username == "" || len(body.Password) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username required, password must be >= 6 chars"})
		return
	}

	// Validate every role id: exists + caller can grant it (ladder).
	targetLevel, err := validateGrantableRoles(ctx, body.Roles)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	_ = targetLevel

	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash error"})
		return
	}
	now := time.Now().Unix()
	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()

	// V1 rs_auth_user.role stays as a compatibility field — pick the numeric
	// tier corresponding to the highest role granted so V1 handlers still
	// see the caller correctly.
	v1Role := minUserRole
	if len(body.Roles) > 0 {
		// Look up highest level's V1 tier.
		var maxLegacy int
		if err := tx.QueryRow(
			`SELECT COALESCE(MAX(r.level), 0) FROM rs_role r WHERE r.id = ANY($1)`,
			pq.Array(body.Roles),
		).Scan(&maxLegacy); err == nil {
			v1Role = legacyV1RoleForLevel(maxLegacy)
		}
	}
	var id int64
	err = tx.QueryRow(
		`INSERT INTO rs_auth_user (username, password_hash, role, studio, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $5) RETURNING id`,
		body.Username, string(hash), v1Role, body.Studio, now,
	).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			c.JSON(http.StatusConflict, gin.H{"error": "username already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, rid := range body.Roles {
		if _, err := tx.Exec(
			`INSERT INTO rs_user_role (user_id, role_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			id, rid,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	u, _ := authUserByID(id)
	row := v2UserRow{authUser: u}
	_ = loadUserRoles(&row)
	c.JSON(http.StatusOK, row)
}

// validateGrantableRoles: each role must exist AND be at level strictly
// below caller.MaxLevel (superadmin bypasses).
func validateGrantableRoles(ctx *v2Context, roleIDs []int64) (maxLevel int, err error) {
	if len(roleIDs) == 0 {
		return 0, nil
	}
	rows, err := db.Query(
		`SELECT id, name, level FROM rs_role WHERE id = ANY($1)`,
		pq.Array(roleIDs),
	)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	found := make(map[int64]bool, len(roleIDs))
	for rows.Next() {
		var rid int64
		var name string
		var level int
		if err := rows.Scan(&rid, &name, &level); err != nil {
			return 0, err
		}
		found[rid] = true
		if level > maxLevel {
			maxLevel = level
		}
		if !ctx.IsSuper && level >= ctx.MaxLevel {
			return 0, errStr("cannot grant role at or above your own level: " + name)
		}
	}
	for _, rid := range roleIDs {
		if !found[rid] {
			return 0, errStr("unknown role id: " + strconv.FormatInt(rid, 10))
		}
	}
	return maxLevel, nil
}

// errStr wraps a string into a Go error without importing errors.
type errString string

func (e errString) Error() string { return string(e) }

func errStr(s string) error { return errString(s) }

// v2HandleUserUpdate patches password / studio. Role changes go through the
// dedicated role assign/unassign endpoints so ladder logic isn't duplicated.
func v2HandleUserUpdate(c *gin.Context) {
	ctx := v2Ctx(c)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	target, err := authUserByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	targetLevel, err := targetUserMaxLevel(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ctx.IsSuper && targetLevel >= ctx.MaxLevel {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot modify a peer or higher-privileged user"})
		return
	}
	var body struct {
		Password *string `json:"password,omitempty"`
		Studio   *string `json:"studio,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	now := time.Now().Unix()
	if body.Password != nil && *body.Password != "" {
		if len(*body.Password) < 6 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "password must be at least 6 chars"})
			return
		}
		if !ctx.has(ActionUsersResetPassword, ScopeGlobal) {
			c.JSON(http.StatusForbidden, gin.H{"error": "missing permission: users.reset_password"})
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*body.Password), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "hash error"})
			return
		}
		if _, err := db.Exec(`UPDATE rs_auth_user SET password_hash=$1, updated_at=$2 WHERE id=$3`, string(hash), now, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.Studio != nil {
		if _, err := db.Exec(`UPDATE rs_auth_user SET studio=$1, updated_at=$2 WHERE id=$3`, strings.TrimSpace(*body.Studio), now, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	_ = target
	u, _ := authUserByID(id)
	row := v2UserRow{authUser: u}
	_ = loadUserRoles(&row)
	c.JSON(http.StatusOK, row)
}

// v2HandleUserResetPassword mirrors V1.
func v2HandleUserResetPassword(c *gin.Context) {
	ctx := v2Ctx(c)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	targetLevel, err := targetUserMaxLevel(id)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ctx.IsSuper && targetLevel >= ctx.MaxLevel {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot reset password of a peer or higher-privileged user"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.Password) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "password must be at least 6 chars"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash error"})
		return
	}
	if _, err := db.Exec(`UPDATE rs_auth_user SET password_hash=$1, updated_at=$2 WHERE id=$3`, string(hash), time.Now().Unix(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// v2HandleUserSetStatus toggles user status. Sets disabled_at when disabling
// so old JWTs get revoked (same rule V1 uses in authMiddleware).
func v2HandleUserSetStatus(disable bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := v2Ctx(c)
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
			return
		}
		targetLevel, err := targetUserMaxLevel(id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !ctx.IsSuper && targetLevel >= ctx.MaxLevel {
			c.JSON(http.StatusForbidden, gin.H{"error": "cannot disable a peer or higher-privileged user"})
			return
		}
		// Last-super guard.
		if disable && targetLevel >= LevelSuperadmin {
			var others int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM rs_user_role ur JOIN rs_role r ON r.id=ur.role_id JOIN rs_auth_user u ON u.id=ur.user_id
				 WHERE r.name=$1 AND u.status=1 AND ur.user_id<>$2`,
				RoleSuperadmin, id,
			).Scan(&others); err == nil && others == 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "cannot disable the last active superadmin"})
				return
			}
		}
		now := time.Now().Unix()
		if disable {
			if _, err := db.Exec(`UPDATE rs_auth_user SET status=0, disabled_at=$1, updated_at=$1 WHERE id=$2`, now, id); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		} else {
			if _, err := db.Exec(`UPDATE rs_auth_user SET status=1, updated_at=$1 WHERE id=$2`, now, id); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

// v2HandleUserDelete removes a user + all role assignments.
func v2HandleUserDelete(c *gin.Context) {
	ctx := v2Ctx(c)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if ctx.UserID == id {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot delete the currently logged-in user"})
		return
	}
	targetLevel, err := targetUserMaxLevel(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ctx.IsSuper && targetLevel >= ctx.MaxLevel {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot delete a peer or higher-privileged user"})
		return
	}
	if targetLevel >= LevelSuperadmin {
		var others int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM rs_user_role ur JOIN rs_role r ON r.id=ur.role_id WHERE r.name=$1 AND ur.user_id<>$2`,
			RoleSuperadmin, id,
		).Scan(&others); err == nil && others == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot delete the last superadmin"})
			return
		}
	}
	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM rs_user_role WHERE user_id=$1`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if _, err := tx.Exec(`DELETE FROM rs_auth_user WHERE id=$1`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// v2HandleUserAssignRoles replaces the user's role set with the provided
// list. Every role must pass validateGrantableRoles.
func v2HandleUserAssignRoles(c *gin.Context) {
	ctx := v2Ctx(c)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, err := authUserByID(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	// Ladder guard: caller must be strictly above target.
	targetLevel, err := targetUserMaxLevel(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ctx.IsSuper && targetLevel >= ctx.MaxLevel {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot re-role a peer or higher-privileged user"})
		return
	}
	var body struct {
		Roles []int64 `json:"roles"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	newMax, err := validateGrantableRoles(ctx, body.Roles)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	// Refuse to demote the last superadmin.
	if targetLevel >= LevelSuperadmin && newMax < LevelSuperadmin {
		var others int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM rs_user_role ur JOIN rs_role r ON r.id=ur.role_id WHERE r.name=$1 AND ur.user_id<>$2`,
			RoleSuperadmin, id,
		).Scan(&others); err == nil && others == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot demote the last superadmin"})
			return
		}
	}

	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM rs_user_role WHERE user_id=$1`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, rid := range body.Roles {
		if _, err := tx.Exec(`INSERT INTO rs_user_role (user_id, role_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, id, rid); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	// Refresh V1 compat role field.
	v1Role := legacyV1RoleForLevel(newMax)
	if _, err := tx.Exec(`UPDATE rs_auth_user SET role=$1, updated_at=$2 WHERE id=$3`, v1Role, time.Now().Unix(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	u, _ := authUserByID(id)
	row := v2UserRow{authUser: u}
	_ = loadUserRoles(&row)
	c.JSON(http.StatusOK, row)
}

// legacyV1RoleForLevel picks the V1 numeric tier the closest to (but not
// above) the given V2 level. Keeps the V1 `role` column in sync so V1
// pages/routes remain usable.
func legacyV1RoleForLevel(level int) int {
	switch {
	case level >= LevelSuperadmin:
		return minSuperAdminRole
	case level >= LevelAdmin:
		return minAdminRole
	case level >= LevelProjectAdmin:
		return minProjectAdminRole
	case level >= LevelRemoteStudioOperator:
		return minRemoteStudioOperatorRole
	case level >= LevelStudioOperator:
		return minStudioOperatorRole
	case level >= LevelTester:
		return minTesterRole
	default:
		return minUserRole
	}
}
