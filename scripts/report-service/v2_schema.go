package main

// V2 schema: RBAC tables + rs_key_pool.
//
// Postgres-only. Report-service targets a single Postgres deployment; the
// root-repo three-DB rule does not apply here (V1 code already uses `$N`
// placeholders, pq.Array, and `AT TIME ZONE` throughout).
//
// initV2Schema is called from main() after the V1 schema block, before any
// route registration.

import (
	"log"
)

// v2SchemaStatements is the ordered list of idempotent DDLs for the V2
// scope (RBAC + Key Pool). Every statement uses IF NOT EXISTS or ALTER ...
// ADD COLUMN IF NOT EXISTS so re-running on an existing deployment is safe.
var v2SchemaStatements = []string{
	// -- RBAC --------------------------------------------------------------
	`CREATE TABLE IF NOT EXISTS rs_role (
		id           BIGSERIAL PRIMARY KEY,
		name         TEXT NOT NULL UNIQUE,
		display_name TEXT NOT NULL,
		level        INT  NOT NULL,
		is_builtin   BOOL NOT NULL DEFAULT false,
		created_at   BIGINT NOT NULL,
		updated_at   BIGINT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS rs_role_permission (
		role_id BIGINT NOT NULL,
		action  TEXT   NOT NULL,
		scope   TEXT   NOT NULL,
		PRIMARY KEY (role_id, action, scope)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_rs_role_permission_role ON rs_role_permission(role_id)`,
	`CREATE TABLE IF NOT EXISTS rs_user_role (
		user_id BIGINT NOT NULL,
		role_id BIGINT NOT NULL,
		PRIMARY KEY (user_id, role_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_rs_user_role_user ON rs_user_role(user_id)`,
	`CREATE INDEX IF NOT EXISTS idx_rs_user_role_role ON rs_user_role(role_id)`,

	// -- Key Pool ----------------------------------------------------------
	`CREATE TABLE IF NOT EXISTS rs_key_pool (
		id                  BIGSERIAL PRIMARY KEY,
		studio              TEXT   NOT NULL,
		uploaded_by         BIGINT NOT NULL,
		key_type            TEXT   NOT NULL,
		key_hash            TEXT   NOT NULL,
		key_encrypted       TEXT   NOT NULL,
		key_last8           TEXT   NOT NULL DEFAULT '',
		quota_usd           NUMERIC(12,4),
		models              TEXT   NOT NULL DEFAULT '',
		name_prefix         TEXT   NOT NULL DEFAULT '',
		group_name          TEXT   NOT NULL DEFAULT '',
		status              TEXT   NOT NULL,
		assigned_profile_id BIGINT,
		remote_channel_id   BIGINT,
		failed_reason       TEXT   NOT NULL DEFAULT '',
		created_at          BIGINT NOT NULL,
		updated_at          BIGINT NOT NULL
	)`,
	// Partial unique index enforces "one hash → one active row" across
	// studios and profiles. Postgres-specific syntax (`WHERE`).
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_key_pool_active_hash
		ON rs_key_pool (key_hash)
		WHERE status IN ('awaiting_assignment', 'pending', 'active')`,
	`CREATE INDEX IF NOT EXISTS idx_key_pool_status_created ON rs_key_pool(status, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_key_pool_studio ON rs_key_pool(studio)`,
	`CREATE INDEX IF NOT EXISTS idx_key_pool_uploaded_by ON rs_key_pool(uploaded_by)`,
	`CREATE INDEX IF NOT EXISTS idx_key_pool_assigned_profile ON rs_key_pool(assigned_profile_id)`,
}

// initV2Schema applies all V2 DDLs. Panics on failure — startup should not
// proceed with a half-built schema.
func initV2Schema() {
	for _, ddl := range v2SchemaStatements {
		if _, err := db.Exec(ddl); err != nil {
			log.Fatalf("v2 schema init failed on %.80q: %v", ddl, err)
		}
	}
	log.Println("V2 schema ready (rs_role / rs_role_permission / rs_user_role / rs_key_pool)")
}
