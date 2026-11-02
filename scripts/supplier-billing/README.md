# new-api Supplier Billing

Independent supplier settlement service with the Report Service V3 UI components.
It has its own process, database, users, opaque sessions and permissions. Gateway
and Report Service cookies, JWTs and users cannot sign in here.

## Run

From the repository root:

```sh
cd scripts/report-service/frontend-v3
bun install --frozen-lockfile
bun run build:billing
cd ../../..
go build -o /tmp/supplier-billing ./cmd/supplier-billing
```

Copy `.env.example` to a private location, set a unique initial manager password,
and configure the dedicated PostgreSQL database and its password in
`BILLING_DB_DSN`. Export the variables in the shell before running
`/tmp/supplier-billing`.
Open `BILLING_ORIGIN`. The initial account is created only for an empty user store;
changing the bootstrap environment never resets an existing password. Remove the
bootstrap password from the environment after first startup. Password changes and
account creation/disable require the current manager password.

Production requires an HTTPS `BILLING_ORIGIN`; terminate TLS at a trusted reverse
proxy and keep the backend listener private. Local HTTP requires the explicit
`BILLING_ALLOW_LOCAL_HTTP=true` option. For Vite development use `bun run dev:billing`
and set `BILLING_ORIGIN=http://localhost:5176`; its API proxy targets port 8092.

PostgreSQL is the default database (`BILLING_DB_DRIVER=postgres`). Set
`BILLING_DB_DSN` to a **dedicated** PostgreSQL database. SQLite and MySQL remain
available through an explicit `BILLING_DB_DRIVER` setting. All service tables use
`sb_`; no Report Service or gateway tables are read or migrated. Back up this
database as the financial ledger.

## Dev deployment

Dev host: `ubuntu@52.198.232.125`, using `~/.ssh/bo-dev.pem`. The independent
Compose project lives at `~/newapi/supplier-billing/compose.yml`; its container
is `newapi-supplier-billing`. It uses a dedicated `supplier_billing` database and
role in the existing PostgreSQL container. Secrets remain in the service's
mode-0600 `.env`; initial manager credentials are in its mode-0600 `.initial-admin`.

The dev service publishes only `127.0.0.1:8092`. Its public login address and
configured origin are **`https://billing.201201200.xyz`**. The existing Cloudflare
tunnel forwards that hostname to `http://localhost:8092`. `BILLING_ORIGIN` must
exactly match the browser's HTTPS origin, with no trailing path;
`BILLING_ALLOW_LOCAL_HTTP` is unset for this deployment.

The private port remains available for diagnostics over an SSH tunnel:

```sh
ssh -i ~/.ssh/bo-dev.pem -N \
  -L 127.0.0.1:8092:127.0.0.1:8092 ubuntu@52.198.232.125
```

Use `http://localhost:8092/health` for a private health check and the public HTTPS
address for browser login so that the origin and Secure session cookie match.
The earlier `billing.nexroute.cc` proposal is superseded by the configured
`billing.201201200.xyz` hostname. When changing the public address, update
`BILLING_ORIGIN` and recreate only this Compose service. For a subpath,
build with `BILLING_BASE_PATH=/supplier-billing/` and configure the reverse proxy
to strip that prefix; use the hostname's origin without the path in
`BILLING_ORIGIN`.

Releases use the same tag flow as the Report Service. Pushing a `v1.2.0-rc.<n>`
tag runs the `build-supplier-billing` job in `.github/workflows/docker-publish.yml`.
That job builds `scripts/supplier-billing/Dockerfile` from the repository root
(frontend, Go binary and runtime stages) and pushes
`ghcr.io/boelroy/polarcode-supplier-billing:<tag>`. To deploy on the dev host:

1. Back up the `supplier_billing` database.
2. Set `BILLING_IMAGE` in `~/newapi/supplier-billing/.env` to the new tag.
3. Run `docker compose -f compose.yml pull && docker compose -f compose.yml up -d`.

To roll back, restore the previous `BILLING_IMAGE`. Nothing is built or uploaded on the host.
The Compose project uses the existing `newapi_newapi-network` and a 60-second stop
grace period. Gateway and Report Service containers are managed by their own
Compose project. Releases before 2026-11-02 13:00 UTC were local images built on
the host from `~/newapi/supplier-billing/releases/`.

Initial dev release verified on 2026-11-02:
`new-api-supplier-billing:dev-20261102T083947Z`, running against PostgreSQL
**16.14** as the dedicated `supplier_billing` database role. Container health,
homepage/assets, initial manager login, account persistence after recreation,
foreign Report Service credentials, CSRF rejection and logout replay checks
passed. The SSH tunnel was checked from the local workstation. The gateway and
Report Service still returned HTTP 200. No supplier or financial ledger records
were created during deployment checks. The runtime bootstrap password was
removed after account creation; the protected initial-credentials file remains
available to the operator.

On 2026-11-02 the public hostname initially returned `Invalid request origin`
because the running service still used `http://localhost:8092`. Updating
`BILLING_ORIGIN=https://billing.201201200.xyz`, removing the local-HTTP override,
and recreating the service fixed the error. Public HTTPS checks passed for the
homepage/assets, login/session/logout, host-only `__Host-supplier_billing` cookies
with Secure/HttpOnly/SameSite=Strict, CSRF rejection and revoked-cookie replay.
Foreign origins remained forbidden. Only deployment configuration changed;
the database, account credentials and origin/CSRF enforcement code were retained.

After a release, check container health, `GET /health`, the SPA HTML and emitted
JS/CSS files, login/logout and unauthenticated access controls. Restart once to
verify that manager accounts persist. To roll back a later release, restore the
previous `BILLING_IMAGE` in `.env` and run `docker compose -f compose.yml up -d`.
For the first release, stopping this service removes its entry point while
retaining the dedicated database and ledger.

## Operation

1. Sign in as billing manager and create a supplier head with its settlement
   currency. The head owns the prepaid balance and can contain multiple suppliers.
2. Create a separate supplier login bound to that head. It can read only that
   head's suppliers, daily usage and statements, and submit additional usage,
   which posts immediately. There is no approval queue.
3. Record actual prepaid transfers as recharges with their payment reference.
   Balance is all credits minus all posted charges. Reversing a recharge preserves
   the original record and appends a debit on the current Beijing date.
4. Configure a source URL and explicit channel IDs. Gateway user ID scopes the
   upstream account: `0` includes all gateway users, while a positive ID selects
   that exact account. This is independent of billing and Report Service logins;
   it is immutable after source creation. Set its token in a server-side
   environment variable beginning `SUPPLIER_BILLING_SOURCE_` and add the URL's
   origin to `BILLING_SOURCE_ORIGINS`. Only its variable name appears in the UI.
5. Use source **Sync** for the chosen date range (up to 91 days), including today
   when needed. Sync is explicitly triggered; no scheduler is installed. Multiple
   sources can feed the same head. Each source/channel binding is unique.

Select **Usage only** to import original gateway USD quantities without applying
a supplier price, discount or FX rate. Such rows are explicitly unpriced and
append no charge to the prepaid ledger. Daily statements show gateway USD usage,
unpriced USD usage and exact request counts separately from posted expenses.
To price these rows later, switch the source to settlement mode, enter an agreed
discount/rate and sync again: each previously unpriced row is settled once, and
subsequent corrections retain that first pricing snapshot. Existing settled
rows always keep their prior quote. Rows predating account filtering and unpriced
imports receive the original all-users/settlement/settled defaults. Later
migrations preserve explicitly configured account filters, modes and statuses.

## Supplier hierarchy and daily views

The business hierarchy is **supplier head → supplier → usage**. `Supplier`
(`sb_suppliers`) remains the head, wallet owner and login boundary. `Vendor`
(`sb_vendors`) represents a supplier account under one head; `VendorKey`
(`sb_vendor_keys`) associates an upstream channel with that supplier. A canonical
Report Service URL/channel pair has one unique vendor binding. `Usage.ChannelID`
and `Usage.VendorID` retain the relationship on imported rows. These ownership
records do not connect billing logins to Report Service users.

**Daily usage** shows each head's period total and a day-by-day table of gateway
USD usage and request counts. Opening a day shows its heads and their suppliers.
**Supplier relationships** groups supplier names and key counts under each head.
Normal views use business names; raw channel IDs and source configuration stay
inside the manager's connection controls. Supplier logins see only their own
head's data.

The authenticated read APIs are:

- `GET /api/vendors[?supplier_id=...]`: supplier `id`, `supplier_id`, `name`,
  `source_studio` and `key_count`; no key values, channel lists or source URLs.
- `GET /api/daily-summary?start=YYYY-MM-DD&end=YYYY-MM-DD[&supplier_id=...]`:
  `timezone`, `start`, `end`, `heads`, per-head `totals`, and `days`. Each day
  contains overall usage/request totals and `heads`; each head contains its
  totals, `sources_synced` and `vendors`. USD quantities and request counts are
  exact decimal strings. Only imported `gateway_usd` rows contribute.

Managers can select one head or all heads. Supplier sessions are restricted to
their bound head, including when no filter is supplied; cross-head requests are
rejected. A missing vendor relationship appears as **Unassigned supplier** and
still contributes to totals. Missing source/date coverage remains explicit rather
than implying confirmed zero usage. Metadata reconciliation can fill existing
rows without changing their natural keys or amounts; a mapping to the wrong head
aborts the whole source sync transaction.

## Connected Dev usage

The current hierarchy release is **`hierarchy-20261102T112609Z`**, deployed on
2026-11-02. It builds on the Dev connection introduced by
`new-api-supplier-billing:dev-data-20261102T103730Z`. The existing Dev Report
Service at `https://gw.nexroute.cc` is read using a server-side token
in `SUPPLIER_BILLING_SOURCE_DEV_REPORT_TOKEN`. The token is copied only into the
protected service environment; no Report Service user or session is shared.

- Gateway account: **`admin`, ID 1**, rather than every administrator role.
- Alice head: supplier accounts **Alice** and **Alice 3**, from channel
  studios/tags **`alice` and `alice3`**.
- 皮皮 head: all remaining supplier accounts. The existing `pipi` head record
  was renamed, retaining its ID and USD denomination.
- Initial backfill: **2026-10-04 through 2026-11-02**, Beijing business dates.
- Scope: only gateway **Admin user 1** consumption, still in **Usage only** mode.
  No discount, supplier price or FX rate is applied, and no wallet charge is
  posted. These amounts remain unpriced gateway USD usage, not official
  list-price or finalized supplier payables.

The deployed relationship registry contains:

| Supplier head | Suppliers | Current channel/key relationships |
| --- | ---: | ---: |
| Alice | 2 | 735 |
| 皮皮 | 57 | 22,398 |
| Total | 59 | 23,133 |

The hierarchy deployment reconciled these **23,133** current channel relationships
and **52,341** historical usage rows. Transaction checks confirmed that quantities,
amounts, business dates and natural keys were unchanged. Adding ownership metadata
did not introduce duplicate usage or settle the unpriced rows.

Public HTTPS verification at `https://billing.201201200.xyz` passed after an
actual login. The deployed assets were `billing-C1INQ-bd` (JavaScript) and
`billing-DLx8JiYF` (CSS). `/api/vendors` returned 59 suppliers and 23,133 keys,
including Alice's **Alice** and **Alice 3**. `/api/daily-summary` reconciled all
30 days exactly from supplier to head to overall daily totals, with no unassigned
usage. Period totals remained **48,214.565800 USD** for Alice and
**64,637.756630 USD** for 皮皮. The ledger contained **zero entries**, and all
imported usage remained **unpriced**. The deployed service was healthy.

Eight sources cover 2,418 channels, partitioned by head/provider and the existing
500-channel source limit. The initial import contains 52,341 usage rows. Each
completed historical day's volume and request count was reconciled against
`report_daily_agg` for gateway user 1; today remains provisional. There were no
duplicate usage keys or ledger charges. Source sync can refresh the configured
bindings; newly added upstream channels need to be assigned to the relevant head
and added through a new source binding. No automatic discovery or schedule is
installed. The broader relationship registry records ownership; it does not
automatically expand the eight sources' usage-import channel allowlists.

The protected `dev-admin-connection.json` on the dev host records the source
bindings and period; `dev-admin-daily-summary.json` records the reconciliation
summary. The operational `connect-dev-admin.py` performs the initial backfill,
not a recurring scheduler. A database dump was taken before migrating/importing.

The hierarchy release passed the full backend suite and both historical schema
upgrade matrices with SQLite **3.50.4**, MySQL **8.0.46** and PostgreSQL **16.15**
(`ok`, 6.346s); the exact commands and coverage are below. The deployed database
remains PostgreSQL **16.14**. Its hierarchy migration and metadata reconciliation
preserved the existing raw usage and financial records.

The first connector reads Report Service `GET /api/report` with `X-API-Key`.
It does not modify or refresh Report Service. That service must already have
populated its hourly aggregates. Source errors are shown separately from zero
usage. Daily rows and supplier-visible source status identify dates that have not
synced and sources whose latest sync failed. Missing previously billed rows abort
a sync rather than create a refund.
An empty first response cannot prove source completeness; verify the upstream
report before treating an empty period as settled. There is no finalization/lock
workflow in this version.

Report `total_cost` is **gateway USD usage**, not official provider list-price.
Its settlement formula is `gateway USD × discount factor × FX rate`. For manual
items it is `quantity × unit price × discount factor × FX rate`. `0.88` means 88%
of the original amount. FX is settlement-currency units per original-currency
unit; same-currency entries require 1. Amounts are decimal values rounded to six
places per usage item; totals sum those posted values. All calendar days use
Asia/Shanghai (UTC+8). Type and model, requests and token counts are retained.

Settlement prices, discounts and FX are snapshotted when a row is first settled;
unpriced rows have no settlement quote. Re-syncs do not double charge. A corrected
settled quantity appends a ledger adjustment at the original business date using
that row's existing price snapshot. Pricing edits affect newly settled rows,
including previously unpriced rows, but do not reprice existing settled rows.
The ledger retains posting time and actor separately from the business date.
Manual items cannot overwrite imported rows.

## Verification

```sh
go test ./internal/supplierbilling ./cmd/supplier-billing
BILLING_TEST_MYSQL_DSN='root:password@tcp(127.0.0.1:13316)/supplier_billing_test?parseTime=true' \
BILLING_TEST_POSTGRES_DSN='host=127.0.0.1 port=15436 user=postgres password=password dbname=supplier_billing_test sslmode=disable' \
go test ./internal/supplierbilling -count=1 -v
go vet ./internal/supplierbilling ./cmd/supplier-billing
```

The matrix uses only disposable dedicated test databases: it resets `sb_` tables.
SQLite always runs; MySQL/PostgreSQL require the corresponding environment variable.
Replace the example DSN passwords with those of the disposable test instances.

Verified on 2026-11-02 against SQLite **3.50.4**, MySQL **8.0.46**, and PostgreSQL
**16.15** with both test DSNs configured: the complete verbose suite passed in
**6.346s**, and `go vet` passed. The captured run is
`/tmp/supplier-billing-db-verification.log` on the verification workstation.

`TestServiceDatabaseMatrix` covers fresh and populated migrations twice, ledger
and unique-index preservation, prepaid accounting, immediate supplements,
Admin-user filtering, unpriced imports and one-time settlement, repeat sync,
corrections, source failures and authentication. `TestServiceSchemaUpgradeMatrix`
starts with the earliest published source/usage schema.
`TestPublishedHierarchySchemaUpgradeMatrix` starts with the subsequent published
schema containing gateway-user filters and settlement status, but no hierarchy
tables or channel/vendor metadata. Both upgrades run twice and preserve existing
data, pricing, status and uniqueness; newly added metadata defaults to zero.

`TestVendorHierarchyDatabaseMatrix` covers exact supplier/head/day totals and
request counts, multiple keys without duplicate usage, explicit unassigned
suppliers, tenant isolation, metadata backfill without financial changes,
wrong-head transaction rollback and incomplete/confirmed source coverage. Each
engine fixture is independent and resets only its dedicated test database.
No Report Service/gateway database or separately configured log database is
migrated by this process.

The PostgreSQL default was also verified by starting the built service against
PostgreSQL 16.15 with `BILLING_DB_DRIVER` unset; startup migration and `GET /health`
both succeeded.

Authentication was designed against OWASP ASVS **5.0.0** and the
[Authentication](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html),
[Session Management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html),
[Password Storage](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html)
and [CSRF Prevention](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html)
guidance. Controls include Argon2id, common-password rejection, persistent attempt
limits, separate host-only HttpOnly sessions, CSRF/origin checks, expiry, revocation,
and reauthentication. This is not a full ASVS compliance certification; deployment
TLS/proxy behavior and organizational recovery/MFA requirements need operational
validation. No email recovery or external identity linking is implemented.

ASVS 5.0.0 references used for this scope: password controls **6.2.1–6.2.5,
6.2.8–6.2.10**, authentication defenses **6.3.1–6.3.2**, and session controls
**7.2.1–7.2.4, 7.3.1–7.3.2, 7.4.1–7.4.3, 7.5.1**. The focused regressions cover
failure/unknown-account responses, throttling, foreign credentials, origin/CSRF,
expiry, logout replay, reauthentication, password rotation, disabled accounts,
and the last-manager invariant. Session lifetime (ASVS 7.3.1 inactivity timeout, 7.3.2 absolute lifetime) is a
documented operator decision since 2026-11-02: both are **60 days** (previously 30
minutes idle / 12 hours absolute), at the owner's request to avoid frequent sign-ins.
Compensating controls: host-only Secure/HttpOnly/SameSite=Strict cookies, at most five
sessions per account, immediate revocation on logout, password change and account
disable, and current-password reauthentication for account changes. Financial actions
(pricing, recharges) do not require reauthentication; treat a lost device as a
credential compromise and change the password. MFA and session-inventory UI are not implemented;
the service must not be represented as ASVS Level 2 compliant.

The frontend directly reuses V3 cards, controls, side panels, form sections,
theme provider and styles. V3's existing shell embeds Report Service auth and
navigation, so the independent shell composes those same components. Monetary
display preserves decimal strings and each supplier's currency; the gateway's
global USD-conversion formatter would change the meaning of settled amounts.

Frontend validation: `bun run typecheck`,
`bun run test -- src/billing/__tests__/billing.test.tsx`, `bun run build:billing`,
and targeted oxlint checks. Shared locale-format regression tests also pass.
The installed Bun 1.3.11 does not provide `bun check -b`; root `web/` typechecking
was completed instead with `../scripts/report-service/frontend-v3/node_modules/.bin/tsc -b`.
Browser interaction is disabled by the local Work settings, so no screenshot
or browser visual acceptance is claimed.
