# V2 Landing — Regression Checklist

Before merging V2, verify that no V1 surface has been broken and that V2's
key-visibility invariants hold end-to-end.

## Environment

- Postgres reachable via `SQL_DSN`
- `ADMIN_PASSWORD` set
- `JWT_SECRET` set (persistent across restarts)
- Optional: `RS_POOL_AWAITING_TTL_DAYS` (default 30)

## Build

- [ ] `cd scripts/report-service/frontend-v2 && bun install && bun run build` — succeeds
- [ ] `cd scripts/report-service && go build ./...` — succeeds
- [ ] Binary starts; log line `V2 schema ready` appears
- [ ] Log line `V2 RBAC seed complete` appears
- [ ] No panic from the key pool bridge in first 30s

## V1 pages (must be untouched)

Log in via `/login`, verify each page loads and interactive elements work:

- [ ] `/report` — daily aggregates render
- [ ] `/all-keys` — key table renders; **only auto-disabled keys show plaintext in the CSV export** (commit 68b2f7b1 behavior)
- [ ] `/key-capacity` — Pool 上 Key form works for admin
- [ ] `/key-tester` — accessible to studio_operator + tester + admin
- [ ] `/provider-testing` — tester + super_admin only
- [ ] `/remote-channels` — super_admin only, full CRUD works
- [ ] `/users` — V1 user CRUD works (admin can create, super_admin can PATCH role)
- [ ] `/cache-report` — cache stats page renders
- [ ] `/profit` — hidden unless `PROFIT_ENABLED=true`

## V2 pages

Log in as super_admin, verify:

- [ ] `/v2/login` renders when not authenticated
- [ ] `/v2/` redirects to `/v2/keys/pool`
- [ ] `/v2/roles` — 5 built-in roles visible; can create custom role, can edit non-super built-ins, cannot delete built-in
- [ ] `/v2/users` — user list renders with role chips + max_level; can assign roles; role checkboxes greyed for level ≥ own
- [ ] `/v2/keys/upload` — pool_only + direct_newapi modes both submit
- [ ] `/v2/keys/pool` — awaiting rows visible; batch assign to profile works
- [ ] `/v2/keys/active` — active + used rows visible; rebind + disable buttons work
- [ ] `/v2/usage/{my,studio,all}` — 3 pages render with totals + per-key rows
- [ ] `/v2/profiles` — CRUD; `has_access_token` boolean shown, plaintext never returned
- [ ] `/v2/settings` — informational page renders

Then log in as a **studio_operator** and verify:

- [ ] Nav only shows: Upload Keys, Key Pool, Active Keys, My Usage, Studio Usage
- [ ] Pool and Active lists are filtered to own studio
- [ ] Cannot see /v2/roles or /v2/users in nav; navigating to them shows an empty state
- [ ] Upload with studio locked from JWT

## Key-visibility invariants (§3.6 hard check)

Manual API tests against `/api/v2/*`:

- [ ] `POST /api/v2/keys/pool` — response contains **no** `key` or `key_masked` field for created rows (only `pool_id` and `status`)
- [ ] `GET /api/v2/keys/pool` — every row has `key_masked`; **no** `key` field on any row unless `is_dead=true`
- [ ] `GET /api/v2/keys/active` — same
- [ ] Non-super user without `keys.reveal_dead` — **no** `key` field even on dead rows
- [ ] Super user (or `keys.reveal_dead` grantee) — `key` field appears **only** on `is_dead=true` rows
- [ ] `GET /api/v2/keys/export.csv` — `key_plaintext` column is empty except on dead rows for privileged callers
- [ ] `GET /api/v2/profiles` — no `access_token_enc`, no `access_token` field in any response
- [ ] `PATCH /api/v2/profiles/:id` with `{"access_token":""}` — token unchanged (blank means keep)
- [ ] Force an upload failure with a garbage upstream key — `rs_key_pool.failed_reason` contains `[REDACTED]` not the raw key

## Ladder guards

Log in as **admin** (not super), verify:

- [ ] Cannot create a role with level ≥ 50 (own level)
- [ ] Cannot edit or delete built-in roles
- [ ] Cannot assign superadmin role to any user
- [ ] Cannot PATCH / disable / delete peer admins or super_admin
- [ ] Cannot create the `remote_newapi.profile.manage` permission on a new role (checkbox greyed)
- [ ] Cannot see `keys.reveal_dead` grantable — checkbox greyed unless super grants explicitly

## Idempotence

- [ ] Restart the process; `V2 schema ready` reappears; no `duplicate` errors
- [ ] Existing user's roles survive restart (no re-seed clobber)

## Migration paths

- [ ] Deployment where `rs_key_pool` didn't exist → new table created, no ripple
- [ ] Deployment where `rs_auth_user` exists with role=10 user → mapped to admin role automatically
- [ ] Deployment where a user had a manually-assigned custom role → **NOT** overwritten by seed

## Rollback

If V2 needs to be dropped:

```sql
DROP TABLE IF EXISTS rs_user_role;
DROP TABLE IF EXISTS rs_role_permission;
DROP TABLE IF EXISTS rs_role;
DROP TABLE IF EXISTS rs_key_pool;
```

V1 tables (`rs_auth_user`, `channels`, `remote_*`) are untouched. Redeploy
the previous binary; V1 pages continue to work.
