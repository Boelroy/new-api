package main

// V2 RBAC: action + scope catalog, permission query, and role ladder helpers.
//
// Design invariants:
//  - Action + Scope are code-frozen enums. Unknown values are rejected at
//    the ingress boundary (POST /roles) and ignored on read.
//  - superadmin (built-in name) short-circuits to "has every permission".
//    We never write rows for it into rs_role_permission — the check in
//    hasPermission returns true before touching the DB.
//  - A user's effective permission set is the UNION of all its roles'
//    permissions plus superadmin short-circuit.
//  - Level ladder: caller can only grant/create roles at strictly lower
//    level than its own maximum bound role level.

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

// -----------------------------------------------------------------------------
// Catalog
// -----------------------------------------------------------------------------

// Built-in role slugs. Their permission sets are seeded in v2_rbac_seed.go.
const (
	RoleSuperadmin     = "superadmin"
	RoleAdmin          = "admin"
	RoleStudioOperator = "studio_operator"
	RoleTester         = "tester"
	RoleUser           = "user"
)

// Built-in level constants — larger = more powerful. Custom roles created
// through the UI must have level strictly less than the caller's max role
// level.
const (
	LevelSuperadmin     = 100
	LevelAdmin          = 50
	LevelStudioOperator = 20
	LevelTester         = 15
	LevelUser           = 10
)

// Scopes.
const (
	ScopeGlobal     = "global"
	ScopeOwnStudio  = "own_studio"
	ScopeAnyStudio  = "any_studio"
	ScopeSelf       = "self"
)

// Actions — grouped by module for readability. Keep in sync with
// V2_PRODUCT_SPEC.md §2.2. Adding an action here + granting it to a role
// via seed or the /v2/roles UI is the only way to introduce a new
// permission point.
const (
	ActionKeysPoolUpload       = "keys.pool.upload"
	ActionKeysPoolAssign       = "keys.pool.assign"
	ActionKeysPoolView         = "keys.pool.view"
	ActionKeysPoolDelete       = "keys.pool.delete"
	ActionKeysNewapiUploadDir  = "keys.newapi.upload_direct"
	ActionKeysNewapiView       = "keys.newapi.view"
	ActionKeysNewapiRebind     = "keys.newapi.rebind"
	ActionKeysNewapiDisable    = "keys.newapi.disable"
	ActionKeysRevealDead       = "keys.reveal_dead"
	ActionKeysPricingSet       = "keys.pricing.set"

	ActionUsageView            = "usage.view"

	ActionReportsView          = "reports.view"
	ActionReportsExport        = "reports.export"

	ActionRemoteProfileManage  = "remote_newapi.profile.manage"
	ActionRemotePolicyManage   = "remote_newapi.policy.manage"

	ActionUsersView            = "users.view"
	ActionUsersCreate          = "users.create"
	ActionUsersUpdate          = "users.update"
	ActionUsersDelete          = "users.delete"
	ActionUsersDisable         = "users.disable"
	ActionUsersResetPassword   = "users.reset_password"
	ActionUsersAssignRole      = "users.assign_role"

	ActionRolesView            = "roles.view"
	ActionRolesManage          = "roles.manage"

	ActionTestingKeyTester       = "testing.key_tester"
	ActionTestingProviderTesting = "testing.provider_testing"

	ActionSystemConfig         = "system.config"
)

// actionCatalog is the ordered list surfaced through /api/v2/permissions.
var actionCatalog = []struct {
	Group  string `json:"group"`
	Action string `json:"action"`
	Label  string `json:"label"`
}{
	{"keys.pool", ActionKeysPoolUpload, "Upload keys to pool"},
	{"keys.pool", ActionKeysPoolAssign, "Assign pooled keys to remote newapi"},
	{"keys.pool", ActionKeysPoolView, "View pool queue"},
	{"keys.pool", ActionKeysPoolDelete, "Delete pool rows before assignment"},
	{"keys.newapi", ActionKeysNewapiUploadDir, "Upload keys directly to a remote newapi"},
	{"keys.newapi", ActionKeysNewapiView, "View live keys on newapi (always masked)"},
	{"keys.newapi", ActionKeysNewapiRebind, "Rebind a live key to a different newapi"},
	{"keys.newapi", ActionKeysNewapiDisable, "Disable a live key"},
	{"keys.newapi", ActionKeysRevealDead, "Reveal plaintext of dead keys"},
	{"keys.pricing", ActionKeysPricingSet, "Set upstream unit price / quota"},
	{"usage", ActionUsageView, "View key usage"},
	{"reports", ActionReportsView, "View reports"},
	{"reports", ActionReportsExport, "Export reports (CSV / HTML)"},
	{"remote_newapi", ActionRemoteProfileManage, "Manage remote newapi profiles"},
	{"remote_newapi", ActionRemotePolicyManage, "Manage studio ↔ newapi accept policy"},
	{"users", ActionUsersView, "View user list"},
	{"users", ActionUsersCreate, "Create users"},
	{"users", ActionUsersUpdate, "Edit user studio / metadata"},
	{"users", ActionUsersDelete, "Delete users"},
	{"users", ActionUsersDisable, "Disable / enable users"},
	{"users", ActionUsersResetPassword, "Reset user password"},
	{"users", ActionUsersAssignRole, "Assign roles to users"},
	{"roles", ActionRolesView, "View roles"},
	{"roles", ActionRolesManage, "Create / edit / delete custom roles"},
	{"testing", ActionTestingKeyTester, "Use Key Tester"},
	{"testing", ActionTestingProviderTesting, "Use Provider Testing"},
	{"system", ActionSystemConfig, "System configuration"},
}

// scopeCatalog is surfaced through /api/v2/permissions.
var scopeCatalog = []struct {
	Scope string `json:"scope"`
	Label string `json:"label"`
}{
	{ScopeGlobal, "Global — no filter"},
	{ScopeOwnStudio, "Own studio only"},
	{ScopeAnyStudio, "Any studio (cross-studio)"},
	{ScopeSelf, "Self only"},
}

// validActions and validScopes back IsValidAction / IsValidScope. Populated
// lazily so tests can assert catalog contents match constants.
var (
	validActionsOnce sync.Once
	validActionsSet  map[string]bool
	validScopesSet   = map[string]bool{
		ScopeGlobal:    true,
		ScopeOwnStudio: true,
		ScopeAnyStudio: true,
		ScopeSelf:      true,
	}
)

// actionAllowedScopes constrains which scopes make sense for each action.
// Prevents nonsensical grants like keys.pool.view@self at role edit time.
// Actions not listed here fall back to "any scope" — safe default that
// forces us to opt in per action.
var actionAllowedScopes = map[string]map[string]bool{
	// User-management actions are global-only — they operate on a global
	// user table, not per-studio data.
	ActionUsersView:          {ScopeGlobal: true},
	ActionUsersCreate:        {ScopeGlobal: true},
	ActionUsersUpdate:        {ScopeGlobal: true},
	ActionUsersDelete:        {ScopeGlobal: true},
	ActionUsersDisable:       {ScopeGlobal: true},
	ActionUsersResetPassword: {ScopeGlobal: true},
	ActionUsersAssignRole:    {ScopeGlobal: true},
	ActionRolesView:          {ScopeGlobal: true},
	ActionRolesManage:        {ScopeGlobal: true},
	// System / newapi profile management is global.
	ActionRemoteProfileManage: {ScopeGlobal: true},
	ActionRemotePolicyManage:  {ScopeGlobal: true},
	ActionSystemConfig:        {ScopeGlobal: true},
	// keys.pool.assign, rebind, disable, pricing.set operate on rows across
	// studios — global-only.
	ActionKeysPoolAssign:    {ScopeGlobal: true},
	ActionKeysNewapiRebind:  {ScopeGlobal: true},
	ActionKeysNewapiDisable: {ScopeGlobal: true},
	ActionKeysPricingSet:    {ScopeGlobal: true},
	ActionKeysRevealDead:    {ScopeGlobal: true, ScopeAnyStudio: true, ScopeOwnStudio: true},
	// keys.pool + keys.newapi view/upload can be studio-scoped.
	ActionKeysPoolUpload:      {ScopeOwnStudio: true, ScopeAnyStudio: true, ScopeGlobal: true},
	ActionKeysPoolView:        {ScopeOwnStudio: true, ScopeAnyStudio: true, ScopeGlobal: true},
	ActionKeysPoolDelete:      {ScopeOwnStudio: true, ScopeAnyStudio: true, ScopeGlobal: true},
	ActionKeysNewapiUploadDir: {ScopeOwnStudio: true, ScopeAnyStudio: true, ScopeGlobal: true},
	ActionKeysNewapiView:      {ScopeOwnStudio: true, ScopeAnyStudio: true, ScopeGlobal: true},
	// Usage + reports can be studio-scoped or self-scoped (self = own uploads).
	ActionUsageView:     {ScopeSelf: true, ScopeOwnStudio: true, ScopeAnyStudio: true, ScopeGlobal: true},
	ActionReportsView:   {ScopeOwnStudio: true, ScopeAnyStudio: true, ScopeGlobal: true},
	ActionReportsExport: {ScopeOwnStudio: true, ScopeAnyStudio: true, ScopeGlobal: true},
	// Testing is global.
	ActionTestingKeyTester:       {ScopeGlobal: true},
	ActionTestingProviderTesting: {ScopeGlobal: true},
}

// IsAllowedActionScope returns whether (action, scope) is a sensible pair.
// Used at grant time (POST/PATCH /roles) to reject accidental combinations.
func IsAllowedActionScope(action, scope string) bool {
	if !IsValidAction(action) || !IsValidScope(scope) {
		return false
	}
	allowed, ok := actionAllowedScopes[action]
	if !ok {
		return true // unlisted → permissive default
	}
	return allowed[scope]
}

// IsValidAction reports whether the string names an action in the catalog.
func IsValidAction(action string) bool {
	validActionsOnce.Do(func() {
		validActionsSet = make(map[string]bool, len(actionCatalog))
		for _, a := range actionCatalog {
			validActionsSet[a.Action] = true
		}
	})
	return validActionsSet[action]
}

// IsValidScope reports whether the string names a scope in the catalog.
func IsValidScope(scope string) bool {
	return validScopesSet[scope]
}

// -----------------------------------------------------------------------------
// Permission struct + serialization
// -----------------------------------------------------------------------------

// Permission is one row of rs_role_permission.
type Permission struct {
	Action string `json:"action"`
	Scope  string `json:"scope"`
}

// String returns the "action@scope" wire form used everywhere in
// permission-check middlewares and the frontend permission list.
func (p Permission) String() string {
	return p.Action + "@" + p.Scope
}

// parsePermission accepts "action@scope" and returns the parsed pair. Empty
// action or scope returns ok=false.
func parsePermission(s string) (Permission, bool) {
	i := strings.LastIndex(s, "@")
	if i <= 0 || i == len(s)-1 {
		return Permission{}, false
	}
	return Permission{Action: s[:i], Scope: s[i+1:]}, true
}

// -----------------------------------------------------------------------------
// Role structs
// -----------------------------------------------------------------------------

// Role mirrors an rs_role row.
type Role struct {
	ID          int64        `json:"id"`
	Name        string       `json:"name"`
	DisplayName string       `json:"display_name"`
	Level       int          `json:"level"`
	IsBuiltin   bool         `json:"is_builtin"`
	Permissions []Permission `json:"permissions,omitempty"`
	CreatedAt   int64        `json:"created_at"`
	UpdatedAt   int64        `json:"updated_at"`
	UserCount   int          `json:"user_count,omitempty"`
}

// roleFromRow scans the base columns (no permissions).
func roleFromRow(scanner interface{ Scan(...any) error }) (Role, error) {
	var r Role
	err := scanner.Scan(&r.ID, &r.Name, &r.DisplayName, &r.Level, &r.IsBuiltin, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// listAllRoles returns roles (without permissions) sorted by level desc.
func listAllRoles() ([]Role, error) {
	rows, err := db.Query(`SELECT id, name, display_name, level, is_builtin, created_at, updated_at FROM rs_role ORDER BY level DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Role, 0)
	for rows.Next() {
		r, err := roleFromRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// loadRolePermissions fills the Permissions slice on r.
func loadRolePermissions(r *Role) error {
	rows, err := db.Query(`SELECT action, scope FROM rs_role_permission WHERE role_id=$1 ORDER BY action, scope`, r.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	r.Permissions = make([]Permission, 0)
	for rows.Next() {
		var p Permission
		if err := rows.Scan(&p.Action, &p.Scope); err != nil {
			return err
		}
		r.Permissions = append(r.Permissions, p)
	}
	return nil
}

// loadRoleUserCount fills UserCount on r.
func loadRoleUserCount(r *Role) error {
	return db.QueryRow(`SELECT COUNT(*) FROM rs_user_role WHERE role_id=$1`, r.ID).Scan(&r.UserCount)
}

// roleByID returns a role or sql.ErrNoRows.
func roleByID(id int64) (Role, error) {
	row := db.QueryRow(`SELECT id, name, display_name, level, is_builtin, created_at, updated_at FROM rs_role WHERE id=$1`, id)
	return roleFromRow(row)
}

// roleByName returns a role or sql.ErrNoRows.
func roleByName(name string) (Role, error) {
	row := db.QueryRow(`SELECT id, name, display_name, level, is_builtin, created_at, updated_at FROM rs_role WHERE name=$1`, name)
	return roleFromRow(row)
}

// -----------------------------------------------------------------------------
// Permission query
// -----------------------------------------------------------------------------

// isSuperadminUser is true when user_id has the built-in superadmin role.
// Cached briefly per-request via the Gin context — see permissionsForUser.
func isSuperadminUser(userID int64) (bool, error) {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM rs_user_role ur JOIN rs_role r ON r.id = ur.role_id WHERE ur.user_id=$1 AND r.name=$2`,
		userID, RoleSuperadmin,
	).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// permissionsForUser returns the effective permission set for user_id. The
// slice is sorted for deterministic output and de-duplicated by (action,scope).
// Superadmin short-circuits to "all cataloged actions @ global" so the
// frontend can render its UI without special-casing "everything".
func permissionsForUser(userID int64) ([]Permission, int, error) {
	// Fetch role rows first so we can compute max level.
	rows, err := db.Query(
		`SELECT r.id, r.name, r.level FROM rs_user_role ur JOIN rs_role r ON r.id = ur.role_id WHERE ur.user_id=$1`,
		userID,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	type roleRef struct {
		ID    int64
		Name  string
		Level int
	}
	roles := make([]roleRef, 0)
	maxLevel := 0
	isSuper := false
	for rows.Next() {
		var rr roleRef
		if err := rows.Scan(&rr.ID, &rr.Name, &rr.Level); err != nil {
			return nil, 0, err
		}
		if rr.Level > maxLevel {
			maxLevel = rr.Level
		}
		if rr.Name == RoleSuperadmin {
			isSuper = true
		}
		roles = append(roles, rr)
	}

	if isSuper {
		out := make([]Permission, 0, len(actionCatalog))
		for _, a := range actionCatalog {
			out = append(out, Permission{Action: a.Action, Scope: ScopeGlobal})
		}
		return out, LevelSuperadmin, nil
	}
	if len(roles) == 0 {
		return []Permission{}, 0, nil
	}

	// Batch-load permissions for all roles in a single query.
	roleIDs := make([]int64, 0, len(roles))
	for _, r := range roles {
		roleIDs = append(roleIDs, r.ID)
	}
	// pq.Array marshals int64 slice into a PG array.
	permRows, err := db.Query(
		`SELECT DISTINCT action, scope FROM rs_role_permission WHERE role_id = ANY($1) ORDER BY action, scope`,
		pq.Array(roleIDs),
	)
	if err != nil {
		return nil, 0, err
	}
	defer permRows.Close()
	perms := make([]Permission, 0)
	for permRows.Next() {
		var p Permission
		if err := permRows.Scan(&p.Action, &p.Scope); err != nil {
			return nil, 0, err
		}
		perms = append(perms, p)
	}
	sort.Slice(perms, func(i, j int) bool {
		if perms[i].Action != perms[j].Action {
			return perms[i].Action < perms[j].Action
		}
		return perms[i].Scope < perms[j].Scope
	})
	return perms, maxLevel, nil
}

// hasPermission is the low-level permission check. Superadmin always
// passes. Otherwise it looks for an rs_role_permission row (action, scope)
// bound to any of the caller's roles. For scope semantics see hasScope.
func hasPermission(userID int64, action, scope string) (bool, error) {
	super, err := isSuperadminUser(userID)
	if err != nil {
		return false, err
	}
	if super {
		return true, nil
	}
	// Baseline: does the user hold action@scope exactly, or a broader scope
	// that subsumes it?
	//   global    subsumes  everything
	//   any_studio subsumes own_studio
	//   own_studio subsumes self (only when target studio == user studio,
	//   which is a data-level check, so we treat own_studio as sufficient
	//   for @self reads that we already know are self-scoped)
	subsumers := scopeSubsumers(scope)
	rows, err := db.Query(
		`SELECT 1 FROM rs_user_role ur JOIN rs_role_permission p ON p.role_id = ur.role_id
		 WHERE ur.user_id=$1 AND p.action=$2 AND p.scope = ANY($3) LIMIT 1`,
		userID, action, pq.Array(subsumers),
	)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), nil
}

// scopeSubsumers returns the set of scopes that grant the requested one.
// Order does not matter — the SQL uses ANY().
func scopeSubsumers(requested string) []string {
	switch requested {
	case ScopeGlobal:
		return []string{ScopeGlobal}
	case ScopeAnyStudio:
		return []string{ScopeAnyStudio, ScopeGlobal}
	case ScopeOwnStudio:
		return []string{ScopeOwnStudio, ScopeAnyStudio, ScopeGlobal}
	case ScopeSelf:
		return []string{ScopeSelf, ScopeOwnStudio, ScopeAnyStudio, ScopeGlobal}
	default:
		return []string{requested}
	}
}

// hasAnyPermission returns true if the caller holds any of the given
// permission pairs. Used by handlers that expose the same data under two
// different actions (e.g. usage view under both pool.view and newapi.view).
func hasAnyPermission(userID int64, pairs []Permission) (bool, error) {
	for _, p := range pairs {
		ok, err := hasPermission(userID, p.Action, p.Scope)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// -----------------------------------------------------------------------------
// Middleware
// -----------------------------------------------------------------------------

// v2Context holds request-scoped RBAC state populated by v2AuthContext.
type v2Context struct {
	UserID      int64
	Username    string
	Studio      string
	MaxLevel    int
	Permissions []Permission
	IsSuper     bool
	// permSet is a fast lookup for hasPermSet.
	permSet map[string]bool
}

func (v *v2Context) has(action, scope string) bool {
	if v.IsSuper {
		return true
	}
	for _, s := range scopeSubsumers(scope) {
		if v.permSet[action+"@"+s] {
			return true
		}
	}
	return false
}

// v2AuthContext looks up the caller's V2 permission set once per request
// and stores it on the Gin context under key "v2". Handlers can call
// v2Ctx(c) to retrieve it.
func v2AuthContext(c *gin.Context) {
	uidAny, _ := c.Get("user_id")
	uid, _ := uidAny.(int64)

	usernameAny, _ := c.Get("username")
	username, _ := usernameAny.(string)

	studioAny, _ := c.Get("studio")
	studio, _ := studioAny.(string)

	// Service-to-service (X-API-Key) callers arrive with role=100 and no
	// user_id. Grant them full superadmin permission set with a synthetic
	// context so /api/v2/* handlers work uniformly.
	if uid == 0 {
		roleAny, _ := c.Get("role")
		role, _ := roleAny.(int)
		if role >= minSuperAdminRole {
			ctx := &v2Context{
				UserID:   0,
				Username: username,
				Studio:   studio,
				MaxLevel: LevelSuperadmin,
				IsSuper:  true,
				permSet:  map[string]bool{},
			}
			// Build permission set from catalog for the frontend.
			for _, a := range actionCatalog {
				ctx.Permissions = append(ctx.Permissions, Permission{Action: a.Action, Scope: ScopeGlobal})
			}
			c.Set("v2", ctx)
			c.Next()
			return
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "V2 requires a local user session (no user_id in token)"})
		c.Abort()
		return
	}

	perms, maxLevel, err := permissionsForUser(uid)
	if err != nil {
		log.Printf("[v2 auth] permission lookup for user %d failed: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "permission lookup failed"})
		c.Abort()
		return
	}
	isSuper, err := isSuperadminUser(uid)
	if err != nil {
		log.Printf("[v2 auth] superadmin check for user %d failed: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "permission lookup failed"})
		c.Abort()
		return
	}
	if isSuper && maxLevel < LevelSuperadmin {
		maxLevel = LevelSuperadmin
	}
	ctx := &v2Context{
		UserID:      uid,
		Username:    username,
		Studio:      studio,
		MaxLevel:    maxLevel,
		Permissions: perms,
		IsSuper:     isSuper,
		permSet:     make(map[string]bool, len(perms)),
	}
	for _, p := range perms {
		ctx.permSet[p.String()] = true
	}
	c.Set("v2", ctx)
	c.Next()
}

// v2Ctx returns the v2Context stored by v2AuthContext middleware. Panics if
// missing — that indicates a route was mounted without the middleware.
func v2Ctx(c *gin.Context) *v2Context {
	v, ok := c.Get("v2")
	if !ok {
		panic("v2Ctx called on a route that did not run v2AuthContext")
	}
	return v.(*v2Context)
}

// requirePermission returns a middleware that enforces action@scope on the
// caller. The caller must have run v2AuthContext first.
func requirePermission(action, scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := v2Ctx(c)
		if ctx.has(action, scope) {
			c.Next()
			return
		}
		c.JSON(http.StatusForbidden, gin.H{"error": fmt.Sprintf("missing permission: %s@%s", action, scope)})
		c.Abort()
	}
}

// requireAnyPermission grants when the caller holds any listed pair.
func requireAnyPermission(pairs ...Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := v2Ctx(c)
		for _, p := range pairs {
			if ctx.has(p.Action, p.Scope) {
				c.Next()
				return
			}
		}
		wanted := make([]string, 0, len(pairs))
		for _, p := range pairs {
			wanted = append(wanted, p.String())
		}
		c.JSON(http.StatusForbidden, gin.H{"error": "missing permission: any of " + strings.Join(wanted, ", ")})
		c.Abort()
	}
}

// targetUserMaxLevel returns the max role level of a target user (for the
// role ladder guard). Used by handlers that mutate other users; the guard
// lives in the handler because it needs the target's identity.
func targetUserMaxLevel(userID int64) (int, error) {
	var maxLevel sql.NullInt64
	err := db.QueryRow(
		`SELECT COALESCE(MAX(r.level), 0) FROM rs_user_role ur JOIN rs_role r ON r.id=ur.role_id WHERE ur.user_id=$1`,
		userID,
	).Scan(&maxLevel)
	if err != nil {
		return 0, err
	}
	// If the target has no rs_user_role rows (legacy pre-seed), fall back
	// to their rs_auth_user.role tier so ladder guards still work.
	if !maxLevel.Valid || maxLevel.Int64 == 0 {
		var legacyRole int
		if err := db.QueryRow(`SELECT role FROM rs_auth_user WHERE id=$1`, userID).Scan(&legacyRole); err != nil {
			return 0, err
		}
		return legacyLevelForRole(legacyRole), nil
	}
	return int(maxLevel.Int64), nil
}

// legacyLevelForRole maps V1 role tier -> V2 level for the ladder guard on
// pre-seed accounts. Kept private to this file.
func legacyLevelForRole(v1Role int) int {
	switch v1Role {
	case minSuperAdminRole:
		return LevelSuperadmin
	case minAdminRole:
		return LevelAdmin
	case minStudioOperatorRole:
		return LevelStudioOperator
	case minTesterRole:
		return LevelTester
	case minUserRole:
		return LevelUser
	}
	return 0
}
