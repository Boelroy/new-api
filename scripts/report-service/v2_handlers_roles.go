package main

// Role management: /api/v2/roles CRUD.
//
// Ladder invariants enforced here:
//  - Caller can only create/edit roles at level strictly less than
//    caller.max_role_level.
//  - Caller can only grant permission pairs it holds itself (subset check).
//    Superadmin bypasses the subset check but not the level cap (there's no
//    level above superadmin so nothing to check).
//  - Built-in roles: only superadmin can edit; nobody can delete.

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// v2HandleRoleList returns roles + permissions + user count.
func v2HandleRoleList(c *gin.Context) {
	roles, err := listAllRoles()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for i := range roles {
		if err := loadRolePermissions(&roles[i]); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if err := loadRoleUserCount(&roles[i]); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"roles": roles})
}

type roleWriteBody struct {
	Name        string       `json:"name"`
	DisplayName string       `json:"display_name"`
	Level       int          `json:"level"`
	Permissions []Permission `json:"permissions"`
}

// validatePermissions ensures every listed permission is (a) in the
// catalog, (b) held by the caller, and (c) a sensible action×scope pair
// per actionAllowedScopes. Returns the list of offending permission strings.
func validatePermissions(caller *v2Context, perms []Permission) []string {
	bad := make([]string, 0)
	seen := make(map[string]bool)
	for _, p := range perms {
		if !IsValidAction(p.Action) || !IsValidScope(p.Scope) {
			bad = append(bad, "unknown: "+p.String())
			continue
		}
		if !IsAllowedActionScope(p.Action, p.Scope) {
			bad = append(bad, "scope not allowed for action: "+p.String())
			continue
		}
		if seen[p.String()] {
			continue
		}
		seen[p.String()] = true
		if !caller.has(p.Action, p.Scope) {
			bad = append(bad, "not held by caller: "+p.String())
		}
	}
	return bad
}

// v2HandleRoleCreate creates a custom (non-builtin) role.
func v2HandleRoleCreate(c *gin.Context) {
	ctx := v2Ctx(c)
	var body roleWriteBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.DisplayName = strings.TrimSpace(body.DisplayName)
	if body.Name == "" || body.DisplayName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and display_name are required"})
		return
	}
	if body.Level <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "level must be positive"})
		return
	}
	// Level ladder: non-superadmin callers must create roles strictly below
	// their own level. Superadmin can create any level but not another
	// superadmin (100).
	if !ctx.IsSuper && body.Level >= ctx.MaxLevel {
		c.JSON(http.StatusForbidden, gin.H{"error": "role level must be below your own level"})
		return
	}
	if body.Level >= LevelSuperadmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "level >= superadmin is reserved"})
		return
	}
	// Reject reserved names (all built-in slugs) so custom roles can't
	// shadow built-ins.
	for _, r := range builtinRoleSeeds {
		if body.Name == r.Name {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name is reserved"})
			return
		}
	}
	if bad := validatePermissions(ctx, body.Permissions); len(bad) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid permissions", "details": bad})
		return
	}

	now := time.Now().Unix()
	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRow(
		`INSERT INTO rs_role (name, display_name, level, is_builtin, created_at, updated_at)
		 VALUES ($1, $2, $3, FALSE, $4, $4) RETURNING id`,
		body.Name, body.DisplayName, body.Level, now,
	).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			c.JSON(http.StatusConflict, gin.H{"error": "role name already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, p := range body.Permissions {
		if _, err := tx.Exec(
			`INSERT INTO rs_role_permission (role_id, action, scope) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			id, p.Action, p.Scope,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	role, err := roleByID(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_ = loadRolePermissions(&role)
	c.JSON(http.StatusOK, role)
}

// v2HandleRoleUpdate patches display_name / level / permissions on a role.
// Editing built-in roles is restricted to superadmin.
func v2HandleRoleUpdate(c *gin.Context) {
	ctx := v2Ctx(c)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	role, err := roleByID(id)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "role not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if role.IsBuiltin && !ctx.IsSuper {
		c.JSON(http.StatusForbidden, gin.H{"error": "only superadmin can edit built-in roles"})
		return
	}
	// A caller cannot edit a role at or above its own level.
	if !ctx.IsSuper && role.Level >= ctx.MaxLevel {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot edit a role at or above your level"})
		return
	}
	var body struct {
		DisplayName *string       `json:"display_name,omitempty"`
		Level       *int          `json:"level,omitempty"`
		Permissions *[]Permission `json:"permissions,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	// Level changes must respect the ladder.
	if body.Level != nil {
		if *body.Level <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "level must be positive"})
			return
		}
		if !ctx.IsSuper && *body.Level >= ctx.MaxLevel {
			c.JSON(http.StatusForbidden, gin.H{"error": "cannot raise role level to or above your own"})
			return
		}
		if role.Name == RoleSuperadmin && *body.Level != LevelSuperadmin {
			c.JSON(http.StatusForbidden, gin.H{"error": "cannot change superadmin level"})
			return
		}
	}
	if body.Permissions != nil {
		if role.Name == RoleSuperadmin {
			c.JSON(http.StatusForbidden, gin.H{"error": "superadmin permissions are hardcoded"})
			return
		}
		if bad := validatePermissions(ctx, *body.Permissions); len(bad) > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid permissions", "details": bad})
			return
		}
	}

	now := time.Now().Unix()
	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()

	if body.DisplayName != nil {
		if _, err := tx.Exec(`UPDATE rs_role SET display_name=$1, updated_at=$2 WHERE id=$3`, strings.TrimSpace(*body.DisplayName), now, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.Level != nil {
		if _, err := tx.Exec(`UPDATE rs_role SET level=$1, updated_at=$2 WHERE id=$3`, *body.Level, now, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.Permissions != nil {
		if _, err := tx.Exec(`DELETE FROM rs_role_permission WHERE role_id=$1`, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		for _, p := range *body.Permissions {
			if _, err := tx.Exec(
				`INSERT INTO rs_role_permission (role_id, action, scope) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				id, p.Action, p.Scope,
			); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	updated, _ := roleByID(id)
	_ = loadRolePermissions(&updated)
	_ = loadRoleUserCount(&updated)
	c.JSON(http.StatusOK, updated)
}

// v2HandleRoleDelete deletes a custom role. Built-in roles cannot be deleted.
// Roles with users bound cannot be deleted.
func v2HandleRoleDelete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	role, err := roleByID(id)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "role not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if role.IsBuiltin {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot delete built-in role"})
		return
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rs_user_role WHERE role_id=$1`, id).Scan(&n); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if n > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "role is still assigned to users"})
		return
	}
	// Also ladder-gate: caller must be strictly above the target role level.
	ctx := v2Ctx(c)
	if !ctx.IsSuper && role.Level >= ctx.MaxLevel {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot delete a role at or above your level"})
		return
	}
	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM rs_role_permission WHERE role_id=$1`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if _, err := tx.Exec(`DELETE FROM rs_role WHERE id=$1`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
