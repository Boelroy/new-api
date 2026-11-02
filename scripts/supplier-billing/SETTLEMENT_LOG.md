# Supplier settlement log

Running record of settlement-system changes, data sources and data-volume
issues. Newest entries at the bottom. All days are Beijing (UTC+8) unless noted.

## 2026-11-02 — Settlement requirements

Settlement follows the supplier's sheet format, one row per product line per day:

| Column | Meaning |
|---|---|
| 当日实际用量 | Gateway USD usage for the line and day |
| 单价/折扣 | RMB per 1 USD of usage (aws 5.6, openai 号池 3.9, 七牛 3.8, claude 官K 4.5, ccmax 2.5) |
| 金额 RMB | usage × unit price |
| 汇率 | RMB per 1 U (6.7) |
| 实付 U | RMB ÷ rate |
| U 支付金额 | Prepaid transfer in U (recharge) |
| U 剩余金额 | Running U balance; negative means we owe the supplier |

- Unit price and rate can differ per row and per day, so every priced row keeps its own snapshot.
- Each supplier has one 期初初始剩余金额 (initial balance) record. It can be edited, and each edit keeps the previous revision and posts a correction.
- Alice uses the same sheet format.
- The existing service multiplies by the FX rate; ÷ 6.7 has no exact decimal multiplier, so a "divide" rate mode is required.

Back-calculated from the sheet: 皮皮 initial balance ≈ 39,073 + 161.22 = **39,234.22 U** before the 10/29 ccmax row (to be confirmed).

Arithmetic errors in the 皮皮 sheet (to be confirmed with the supplier):

| Row | Sheet | Correct |
|---|---|---|
| openai 号池 11/1 | 2,091.07 RMB / 312.10 U | 394.1697 × 3.9 = 1,537.26 RMB / 229.44 U |
| ccmax 11/1 | 134.64 RMB / 20.10 U | 134.64 × 2.5 = 336.60 RMB / 50.24 U |

## 2026-11-02 — 皮皮 data sources (as defined by the user)

| Line | Where | Scope |
|---|---|---|
| openai 号池 | Dev Report (gw.nexroute.cc), existing sources | 皮皮 OpenAI sources, gateway user 1 |
| aws | 52.68.102.82 (`bo-dev.pem`) new-api + `newapi-report` rc.325 | user `admin` (id 1), group `AWS_Bedrock` |
| claude 官K | 52.68.102.82 | user `admin`, group `Anthropic` |
| 七牛 | polarcode 52.192.154.152 report rc.233, remote profile `aaaaasl` → `https://supplier-asl.sufy.com` | all tags except alice / alice2 |
| ccmax | **Dev** Report 52.198.232.125 (not polarcode: its report is rc.233 and has no CC Max) | uploaders `rongshan1` (163), `rangshan2` (164) |

### Reconciliation against the 皮皮 sheet

Only openai 号池 matches. Everything else differs, so the sheet is probably produced
from the supplier's own records. Treat our data as "measured" and the sheet as "claimed".

| Day | Line | Sheet | Measured | Notes |
|---|---|---|---|---|
| 10/30 | 号池 | 122.4076 | 122.41 | OpenAI 1/4 + 4/4 — match |
| 10/31 | 号池 | 1,957.01 | 1,957.01 | match |
| 11/1 | 号池 | 394.1697 | 394.17 | match |
| 10/29 | 号池 | 393.39 | 1,546.99 | mismatch |
| 10/29 | aws | 28,966.78 | 30,781.79 | ×0.941 |
| 10/30 | aws | 89,715.29 | 95,409.52 | ×0.940 |
| 10/31 | aws | 98,131.16 | 121,610.48 | ×0.807 |
| 11/1 | aws | 53,024.17 | 27,338.67 | sheet names `aws-bedrock-new-f` |
| 11/1 | claude 官K | 26,095.93 | 30,978.02 | |
| 10/30 | 七牛 | 22,895.781 | 5,885.39 | snapshot Δ; 10/30 + 10/31 total 23,831 vs sheet 24,345 |
| 10/31 | 七牛 | 1,449.3297 | 17,946.24 | |
| 10/30 | ccmax | 752.86 | ¥3,058.35 portal list price | unit unclear; matches the earlier 10-27 note that the sheet ≠ portal bill |

UTC-day bucketing does not explain the AWS gap (UTC 10/30 = 95,151.69).
Admin's AWS_Bedrock usage spans several channel families (`rs-1029`, `rf-1101`,
`ks-1101`, `Rri-1102`), which may belong to different suppliers.

## 2026-11-02 — Data-volume and integration issues

1. **Sync row volume.** The report connector stores one usage row per report row
   (hour × user × token × channel × model). Dev already has 52,341 rows for 30 days
   across 2 suppliers. On 52.68.102.82, admin alone produced 210–1,871 rows/day
   (10/29–11/1). Statements load every usage row in the range (up to 366 days), so
   memory and latency grow linearly. Plan: aggregate on import to day × channel ×
   model, or aggregate in SQL for statements and the overview.
2. **Report response cap.** `/api/report` returns every user and channel for the
   range, and the connector rejects responses over 32 MB. At ~2k rows/day, a 91-day
   sync can exceed the cap. Sync in ≤ 30-day chunks, or add server-side user and
   channel filters to the report API.
3. **Channel churn.** AWS channels are recreated almost daily (`rs-1029`,
   `ks-1101`, `Rri-1102`). Sources are bound by explicit channel IDs (max 500 per
   source), so each new channel needs a new source binding or its usage is never
   imported. A group/user-scoped binding (`AWS_Bedrock` + user 1) would remove this
   maintenance; channel-ownership uniqueness must then be enforced per group/user.
4. **Never read `logs`.** 52.68.102.82 `logs` is 102 GB; polarcode `logs` is 44 GB+.
   Use `report_daily_agg` (22 MB on 82) or the report API.
5. **七牛 has no usable API on polarcode.** Usage exists only as per-channel
   `used_quota` snapshots (`remote_channel_snapshot`, profile 1, ~1,240 channels,
   captures since 08-04). Report rc.233 only exposes the latest snapshot per channel
   or one channel's history. A daily usage-by-tag endpoint means upgrading
   polarcode-report (rc.233 → current), which is a prod deploy.
   - Channels removed from `remote_channel_current` lose their tag, so their usage
     cannot be attributed.
   - 544 channels have no tag and are currently counted as "not alice". Needs confirmation.
6. **CC Max is day-delayed and in CNY list price.** `rs_ccmax_daily_bill` is pulled
   hourly from 08:00, only for days up to yesterday. Today is never available.
   Amounts are portal list-price ¥, not gateway USD; the unit for the 2.5 price
   needs confirmation.
7. **Codex co-editing.** On 2026-11-02, Codex added supplier heads/vendors
   (`sb_vendors`, `sb_vendor_keys`, `usage.vendor_id`, `usage.channel_id`) and
   `/api/daily-summary`. The whole service is still untracked in git. Coordinate
   before editing the same files.

## 2026-11-02 — Settlement core implemented (local, not deployed)

Backend (`internal/supplierbilling`; most new logic in `settlement.go`):

- `sb_usage.rate_mode`: `multiply` (default; existing rows and sources) or `divide`
  (sheet style: original amount ÷ RMB-per-U rate). `calculateCharge` takes the mode.
- `sb_initial_balances` (`supplier_id`, `revision` unique): `PUT /api/suppliers/{id}/initial-balance`
  `{date, amount (may be negative), note, revision}`.
  - A stale revision returns 409.
  - Replacing the balance posts `initial_balance_reversal` at the old date plus a new `initial_balance` entry.
- `sb_claims` (unique `supplier_id`, `date`, `usage_type`): `PUT /api/claims` saves or deletes the
  supplier's sheet figures. Claims never touch the ledger.
- `POST /api/usage/price` `{supplier_id, date, usage_type, unit_price, currency, fx_rate}`
  prices every imported row of that product line and day in divide mode.
  - The first pricing posts `usage`; re-pricing posts `usage_adjustment` "Price correction".
  - It bumps all of the supplier's source versions first, so it serializes with sync (a concurrent sync returns 409; retry).
- `/api/statement` now includes `initial_balance`, `days[].initial` and `lines`.
  - `lines` has one sheet row per day × product line × price snapshot, plus balance movements and unmatched claims, each with a running balance.
  - The last line's balance equals the ledger balance (tested).
- `GET /api/overview` (manager only): every supplier's daily payable, payments and closing balance.

Frontend (`frontend-v3/src/billing`):

- New **Settlement / 结算** page.
  - With "All supplier heads" selected: the overview (balance cards, initial balance, daily table per supplier).
  - With one supplier selected: the sheet (usage, unit price, original amount, ÷ rate, settlement amount, payments, balance, supplier claim with gap).
- New forms: Set initial balance, Set price, Supplier claim. Manual usage now posts `rate_mode: divide`.
- zh: "Opening balance" (balance at period start) renamed to 区间期初余额 so it is not confused with 期初初始剩余金额.

Verification (2026-11-02):

- `go test ./internal/supplierbilling -count=1` passed on SQLite 3.50.4, MySQL 8.0.46 and PostgreSQL 16.15.
  Includes fresh migration ×2, the legacy-schema upgrade (`rate_mode` defaults to `multiply`) and the new settlement-sheet case.
- `go vet` clean.
- `bun run typecheck`, 14/14 billing tests, `bun run build:billing` and oxlint all clean.

Data-volume notes for this change:

- The statement still loads every usage row in range to build `lines` and the raw "Usage details" tab.
  Dev already has 52k rows per month; the raw tab ships all of them as JSON.
- The overview loads only six columns per usage row, but it still scans every row per supplier.
- Both should move to SQL aggregation once the aws/claude sources add ~1–2k rows per day.

## 2026-11-02 — Connector findings

- **七牛 is already mirrored on dev.** Dev Report profile 1 `aaaasl` is the same remote account as
  polarcode `aaaaasl` (`https://supplier-asl.sufy.com`, remote user 2).
  - 1,440 channels (51 tagged alice*); snapshots since 08-04 (8,929 captures).
  - Non-alice daily Δ on dev: 10/30 6,061.24, 10/31 17,799.94, 11/1 2,233.17. Polarcode gives 5,885 / 17,946 / 2,257
    (snapshot timing and channel set differ slightly).
  - Dev already runs remote model cooldown on this profile (`rmc_enabled=true`, `rmc_profiles=1`, `rmc_dry_run=false`),
    so dev is already writing to these production remote channels. This is not new with billing.
  - Needed: a dev Report endpoint for daily used-quota Δ by tag (Beijing days), plus a billing source kind that reads it.
    - Δ is computed from `max(used_quota)` per channel at each day boundary.
    - Channels deleted from `remote_channel_current` drop out.
    - The snapshot cadence limits how precisely usage lands on the right day.
- **aws / claude 官K (52.68.102.82).** `newapi-report` rc.325 is published on `:8090` (plain HTTP) and behind a
  Cloudflare tunnel whose hostname is not visible from the host. The billing connector requires HTTPS.
  - `/api/report` rows include `group`, so a source scoped by user and group (no channel list) is feasible and avoids
    the daily channel churn.
  - That source binds by `(base_url, group)` instead of per channel ID.
  - The response is unfiltered (all users and groups), at ~2k rows/day on this host. Sync ≤ 30 days at a time.
- **ccmax.** Dev `rs_ccmax_daily_bill` for uploaders 163/164 (`rongshan1`, `rangshan2`): daily ¥ list price, synced through
  the previous day.
  - `/api/ccmax/settlement` is super-admin only and grouped by settlement party, not by uploader.
  - A per-uploader daily endpoint is needed so billing can let the user pick which report users belong to a supplier.

## 2026-11-02 — Configurable source mappings (local, not deployed)

Each data source now maps upstream values to one supplier and product line (`usage_type`).
Mappings are managed under 供应商关系 → 连接设置 → Add/Edit source.

| Mapping type (`kind`) | Upstream | Mapped values (`selector`) | Unit |
|---|---|---|---|
| `report` (existing) | Report `/api/report` | explicit channel IDs (immutable) | gateway_usd |
| `report_group` | Report `/api/report`, optional gateway user | gateway groups, e.g. `AWS_Bedrock`, `Anthropic` | gateway_usd |
| `remote_tag` | Report `/api/billing/remote-tag-usage?profile_id=` | channel tags, or "all except" the listed tags | gateway_usd |
| `ccmax` | Report `/api/billing/ccmax-usage` | uploader usernames, e.g. `rongshan1`, `rangshan2` | cny_list (source currency CNY) |

- **Editable.** A changed mapping applies to later syncs. Already-imported rows keep their source;
  rows whose value was unmapped no longer trigger the "previously billed rows are missing" guard
  (`sb_usage.match_value` records the value).
- **No double billing.** `sb_source_channels` binding keys are scoped per upstream:
  - group: base URL + group (regardless of user filter);
  - tag: base URL + profile + tag; an "all except" source holds a per-profile `tag-rest` key and must exclude every tag mapped elsewhere;
  - ccmax: base URL + username.
  - Channel and group sources cannot share one upstream (a channel's group is unknown to billing).
- **Discovery.** `POST /api/sources/discover` (manager) reads the last 14 days and lists each value with its usage and current owner.
  The editor shows unmapped values and values already owned by another supplier.
- **Through date.** Sync records day coverage only up to the upstream's `through` date
  (remote tags: last day closed by a snapshot; ccmax: `ccmax_bill_synced_through`).
  Later days stay "awaiting sync" instead of a confirmed zero.

Report Service (`scripts/report-service/billing_export.go`, super-admin / X-API-Key):

- `GET /api/billing/remote-tag-usage?profile_id&start&end`
  - Per Beijing day: difference between the last full snapshot captures at or before each midnight, summed by current tag.
  - Untagged channels → `(untagged)`; channels gone from the mirror → `(deleted)`.
  - Dev, 31 days on profile 1: 1.75 s.
  - The first draft compared `captured_at` to a numeric `EXTRACT(EPOCH …)` and seq-scanned 4.9M rows per day (>60 s).
    It was cancelled on the dev DB and fixed with a `::bigint` cast.
- `GET /api/billing/ccmax-usage?start&end`: per day × uploader, `SUM(cost_micro)` as ¥ list price, account count.
  - Dev 10/30 has 18 uploaders with bills; only rongshan1 / rangshan2 belong to 皮皮.

Volume:

- `report_group` imports aggregate to one row per day × group × model instead of hour × token × channel × model.
  On 52.68.102.82 this replaces ~200–1,900 admin rows/day with a few dozen.
- Remote tags and ccmax produce one row per day per value.

Verification:

- `go test ./internal/supplierbilling -count=1 -v` passed on SQLite 3.50.4, MySQL 8.0.46 and PostgreSQL 16.15.
  The new matrix block covers tag include/exclude overlap, mapping edits, through-date coverage, ccmax CNY rows,
  group aggregation, channel/group conflict, discovery owners and supplier 403.
- `go vet` clean; `go build` of report-service clean.
- Frontend: typecheck, 15/15 billing tests, `build:billing`, oxlint all clean.
- The new Report endpoints are compiled but not deployed. Only their SQL was run read-only on the dev DB.
