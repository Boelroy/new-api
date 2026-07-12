package main

// Idempotent seed for V2 built-in roles.
//
// Every run:
//   1. Ensures the 5 built-in roles exist (superadmin, admin, studio_operator,
//      tester, user) with the levels defined in v2_rbac.go.
//   2. For non-superadmin built-ins, ensures the default permission set is
//      present (INSERT ... ON CONFLICT DO NOTHING). Existing extra rows are
//      NOT deleted — that would clobber legitimate customizations via
//      /v2/roles UI.
//   3. For every rs_auth_user row without any rs_user_role binding, inserts
//      one row mapping the V1 role tier to the corresponding built-in.
//
// This is safe to re-run on every startup.

import (
	"database/sql"
	"log"
	"time"
)

type builtinRoleSeed struct {
	Name        string
	DisplayName string
	Level       int
	Permissions []Permission
}

// builtinRoleSeeds is the source of truth for default built-in role
// permissions. Match this list against V2_PRODUCT_SPEC.md §2.4.
//
// Superadmin is intentionally *not* seeded with permission rows — the
// hasPermission short-circuit grants everything based on role membership
// alone. Seeding rows would risk drift if the catalog grows.
var builtinRoleSeeds = []builtinRoleSeed{
	{
		Name:        RoleSuperadmin,
		DisplayName: "Super Admin",
		Level:       LevelSuperadmin,
		Permissions: nil, // short-circuited in hasPermission
	},
	{
		Name:        RoleAdmin,
		DisplayName: "Admin",
		Level:       LevelAdmin,
		Permissions: []Permission{
			{ActionKeysPoolUpload, ScopeAnyStudio},
			{ActionKeysPoolAssign, ScopeGlobal},
			{ActionKeysPoolView, ScopeAnyStudio},
			{ActionKeysPoolDelete, ScopeAnyStudio},
			{ActionKeysNewapiUploadDir, ScopeAnyStudio},
			{ActionKeysNewapiView, ScopeAnyStudio},
			{ActionKeysNewapiRebind, ScopeGlobal},
			{ActionKeysNewapiDisable, ScopeGlobal},
			// keys.reveal_dead deliberately excluded — superadmin must grant explicitly.
			{ActionKeysPricingSet, ScopeGlobal},
			{ActionUsageView, ScopeAnyStudio},
			{ActionReportsView, ScopeAnyStudio},
			{ActionReportsExport, ScopeAnyStudio},
			// remote_newapi.profile.manage excluded — admin cannot add newapi.
			{ActionRemotePolicyManage, ScopeGlobal},
			{ActionUsersView, ScopeGlobal},
			{ActionUsersCreate, ScopeGlobal},
			{ActionUsersUpdate, ScopeGlobal},
			{ActionUsersDelete, ScopeGlobal},
			{ActionUsersDisable, ScopeGlobal},
			{ActionUsersResetPassword, ScopeGlobal},
			{ActionUsersAssignRole, ScopeGlobal},
			{ActionRolesView, ScopeGlobal},
			{ActionRolesManage, ScopeGlobal},
			{ActionTestingKeyTester, ScopeGlobal},
			{ActionTestingProviderTesting, ScopeGlobal},
			{ActionSystemConfig, ScopeGlobal},
		},
	},
	{
		Name:        RoleStudioOperator,
		DisplayName: "Studio Operator",
		Level:       LevelStudioOperator,
		Permissions: []Permission{
			{ActionKeysPoolUpload, ScopeOwnStudio},
			{ActionKeysPoolView, ScopeOwnStudio},
			{ActionKeysPoolDelete, ScopeOwnStudio},
			{ActionKeysNewapiUploadDir, ScopeOwnStudio},
			{ActionKeysNewapiView, ScopeOwnStudio},
			{ActionUsageView, ScopeOwnStudio},
			{ActionTestingKeyTester, ScopeGlobal},
		},
	},
	{
		Name:        RoleTester,
		DisplayName: "Tester",
		Level:       LevelTester,
		Permissions: []Permission{
			{ActionTestingKeyTester, ScopeGlobal},
			{ActionTestingProviderTesting, ScopeGlobal},
		},
	},
	{
		// Project Admin: Key Capacity + Key Tester only. A horizontal
		// role that owns per-project key-capacity oversight without any
		// broader admin surface.
		Name:        RoleProjectAdmin,
		DisplayName: "Project Admin",
		Level:       LevelProjectAdmin,
		Permissions: []Permission{
			{ActionKeysPoolView, ScopeAnyStudio},
			{ActionKeysPoolUpload, ScopeAnyStudio},
			{ActionKeysNewapiView, ScopeAnyStudio},
			{ActionKeysNewapiUploadDir, ScopeAnyStudio},
			{ActionTestingKeyTester, ScopeGlobal},
		},
	},
	{
		Name:        RoleUser,
		DisplayName: "User",
		Level:       LevelUser,
		Permissions: []Permission{
			{ActionKeysNewapiView, ScopeOwnStudio},
			{ActionUsageView, ScopeOwnStudio},
			{ActionReportsView, ScopeOwnStudio},
		},
	},
}

// seedV2RBAC is safe to call on every startup. It:
//   - upserts the 5 built-in roles (name, display_name, level, is_builtin=true)
//   - for each built-in with a non-nil Permissions list, inserts any missing
//     permission rows
//   - maps V1 rs_auth_user.role → rs_user_role for accounts that don't yet
//     have any V2 role assignment
func seedV2RBAC() {
	now := time.Now().Unix()

	roleIDByName := make(map[string]int64, len(builtinRoleSeeds))
	for _, seed := range builtinRoleSeeds {
		id, err := upsertBuiltinRole(seed, now)
		if err != nil {
			log.Fatalf("seedV2RBAC: upsert role %s failed: %v", seed.Name, err)
		}
		roleIDByName[seed.Name] = id

		for _, perm := range seed.Permissions {
			if _, err := db.Exec(
				`INSERT INTO rs_role_permission (role_id, action, scope) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				id, perm.Action, perm.Scope,
			); err != nil {
				log.Fatalf("seedV2RBAC: insert permission %s@%s for %s failed: %v", perm.Action, perm.Scope, seed.Name, err)
			}
		}
	}

	if err := mapLegacyUsersToRoles(roleIDByName); err != nil {
		log.Fatalf("seedV2RBAC: legacy user mapping failed: %v", err)
	}

	log.Println("V2 RBAC seed complete")
}

// upsertBuiltinRole ensures a role row exists with the given (name, level,
// display_name, is_builtin=true). Returns the resolved id.
func upsertBuiltinRole(seed builtinRoleSeed, now int64) (int64, error) {
	var id int64
	err := db.QueryRow(`SELECT id FROM rs_role WHERE name=$1`, seed.Name).Scan(&id)
	if err == sql.ErrNoRows {
		err = db.QueryRow(
			`INSERT INTO rs_role (name, display_name, level, is_builtin, created_at, updated_at)
			 VALUES ($1, $2, $3, TRUE, $4, $4) RETURNING id`,
			seed.Name, seed.DisplayName, seed.Level, now,
		).Scan(&id)
		return id, err
	}
	if err != nil {
		return 0, err
	}
	// Refresh display_name / level on existing rows — but keep is_builtin=true.
	_, err = db.Exec(
		`UPDATE rs_role SET display_name=$1, level=$2, is_builtin=TRUE, updated_at=$3 WHERE id=$4`,
		seed.DisplayName, seed.Level, now, id,
	)
	return id, err
}

// mapLegacyUsersToRoles seeds rs_user_role for any rs_auth_user that has no
// V2 role assignment. Existing rs_user_role rows are left untouched.
func mapLegacyUsersToRoles(roleIDByName map[string]int64) error {
	rows, err := db.Query(
		`SELECT u.id, u.role
		   FROM rs_auth_user u
		  WHERE NOT EXISTS (SELECT 1 FROM rs_user_role ur WHERE ur.user_id = u.id)`,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	type todo struct {
		userID  int64
		v1Role  int
	}
	pending := make([]todo, 0)
	for rows.Next() {
		var t todo
		if err := rows.Scan(&t.userID, &t.v1Role); err != nil {
			return err
		}
		pending = append(pending, t)
	}

	for _, t := range pending {
		builtinName := builtinRoleNameForLegacy(t.v1Role)
		if builtinName == "" {
			continue
		}
		rid, ok := roleIDByName[builtinName]
		if !ok {
			continue
		}
		if _, err := db.Exec(
			`INSERT INTO rs_user_role (user_id, role_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			t.userID, rid,
		); err != nil {
			return err
		}
	}
	return nil
}

// builtinRoleNameForLegacy maps V1 numeric role tiers to built-in role slugs.
func builtinRoleNameForLegacy(v1Role int) string {
	switch v1Role {
	case minSuperAdminRole:
		return RoleSuperadmin
	case minAdminRole:
		return RoleAdmin
	case minProjectAdminRole:
		return RoleProjectAdmin
	case minStudioOperatorRole:
		return RoleStudioOperator
	case minTesterRole:
		return RoleTester
	case minUserRole:
		return RoleUser
	}
	return ""
}
