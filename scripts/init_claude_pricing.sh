#!/usr/bin/env bash
# Initialize Claude model pricing for a new-api Postgres deployment.
#
# Unlike sync_claude_pricing.sh (which mirrors prices from a source server),
# this script bundles the target values in-line and merges them into the
# target's options table. Non-listed models — Claude or otherwise — are
# preserved as-is.
#
# Target models + ratios (USD prices computed with QUOTA_PER_UNIT=500000):
#
#   Model                        ModelRatio  Input$/M  Output$/M  Cache R$/M  Cache W$/M
#   ---------------------------------------------------------------------------------
#   claude-opus-4-7              2.5         5.00      25.00      0.50        6.25
#   claude-opus-4-6              2.5         5.00      25.00      0.50        6.25
#   claude-opus-4-8              2.5         5.00      25.00      0.50        6.25
#   claude-opus-4-5-20251101     2.5         5.00      25.00      0.50        6.25
#   claude-sonnet-4-6            1.5         3.00      15.00      0.30        3.75
#   claude-sonnet-4-5-20250929   1.5         3.00      15.00      0.30        3.75
#   claude-sonnet-5              1           2.00      10.00      0.20        2.50
#   claude-haiku-4-5-20251001    0.5         1.00      5.00       0.10        1.25
#   claude-fable-5               5           10.00     50.00      1.00        12.50
#
# CompletionRatio is 5, CacheRatio is 0.1, CreateCacheRatio is 1.25 for all.
#
# Usage:
#   scripts/init_claude_pricing.sh                     # apply to default target
#   TGT_HOST=52.68.102.82 scripts/init_claude_pricing.sh
#   scripts/init_claude_pricing.sh --dry-run
#   scripts/init_claude_pricing.sh --no-restart
#
# Env overrides (same names as sync_claude_pricing.sh):
#   TGT_HOST, TGT_USER, TGT_SSH_KEY,
#   TGT_PG_CONTAINER, TGT_PG_USER, TGT_PG_DB,
#   TGT_APP_CONTAINER

set -euo pipefail

DRY_RUN=0
DO_RESTART=1
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)    DRY_RUN=1; shift ;;
    --no-restart) DO_RESTART=0; shift ;;
    -h|--help)    sed -n '2,32p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

TGT_HOST="${TGT_HOST:-52.198.232.125}"
TGT_USER="${TGT_USER:-ubuntu}"
TGT_SSH_KEY="${TGT_SSH_KEY:-$HOME/.ssh/bo-dev.pem}"
TGT_PG_CONTAINER="${TGT_PG_CONTAINER:-newapi-postgres}"
TGT_PG_USER="${TGT_PG_USER:-postgres}"
TGT_PG_DB="${TGT_PG_DB:-newapi}"
TGT_APP_CONTAINER="${TGT_APP_CONTAINER:-newapi}"

# Target pricing. Each model gets one line per ratio map; the associated
# Python step below folds these into JSON dicts.
#
# Format:  MODEL=<name> RATIO=<ModelRatio> COMPLETION=<CompletionRatio> \
#          CACHE=<CacheRatio> CREATE_CACHE=<CreateCacheRatio>
read -r -d '' TARGET_ROWS <<'ROWS' || true
claude-opus-4-7|2.5|5|0.1|1.25
claude-opus-4-6|2.5|5|0.1|1.25
claude-opus-4-8|2.5|5|0.1|1.25
claude-opus-4-5-20251101|2.5|5|0.1|1.25
claude-sonnet-4-6|1.5|5|0.1|1.25
claude-sonnet-4-5-20250929|1.5|5|0.1|1.25
claude-sonnet-5|1|5|0.1|1.25
claude-haiku-4-5-20251001|0.5|5|0.1|1.25
claude-fable-5|5|5|0.1|1.25
ROWS

KEYS=(ModelRatio CompletionRatio CacheRatio CreateCacheRatio)

WORK="$(mktemp -d -t newapi-pricing-init-XXXX)"
trap 'rm -rf "$WORK"' EXIT

ssh_tgt() { ssh -i "$TGT_SSH_KEY" -o StrictHostKeyChecking=no "$TGT_USER@$TGT_HOST" "$@"; }

echo "[1/3] Reading current target values from $TGT_HOST ($TGT_PG_DB)..."
for k in "${KEYS[@]}"; do
  ssh_tgt "sudo docker exec $TGT_PG_CONTAINER psql -U $TGT_PG_USER -d $TGT_PG_DB -tAc \"SELECT value FROM options WHERE key='$k';\"" \
    > "$WORK/tgt_$k.json"
done

echo "[2/3] Computing merged JSON..."
printf '%s\n' "$TARGET_ROWS" > "$WORK/rows.txt"
python3 - "$WORK" "${KEYS[@]}" <<'PY'
import json, sys, os
work, *keys = sys.argv[1:]

def load(p):
    try:
        s = open(p).read().strip()
        return json.loads(s) if s else {}
    except Exception:
        return {}

# Column index inside the ROWS heredoc for each ratio map. All target
# models get overwritten wholesale; non-target keys are preserved.
col_by_key = {"ModelRatio": 1, "CompletionRatio": 2, "CacheRatio": 3, "CreateCacheRatio": 4}

target = {k: {} for k in keys}
with open(f"{work}/rows.txt") as f:
    for line in f:
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        parts = line.split("|")
        if len(parts) < 5:
            continue
        model = parts[0]
        for k in keys:
            v = parts[col_by_key[k]]
            target[k][model] = float(v) if "." in v else int(v)

for k in keys:
    tgt = load(f"{work}/tgt_{k}.json")
    merged = dict(tgt)
    for model, val in target[k].items():
        merged[model] = val
    out = json.dumps(merged, separators=(',', ':'), ensure_ascii=False, sort_keys=True)
    open(f"{work}/merged_{k}.json", "w").write(out)
    added = sum(1 for m in target[k] if m not in tgt)
    changed = sum(1 for m in target[k] if m in tgt and tgt[m] != target[k][m])
    print(f"  {k}: existing={len(tgt)} added={added} changed={changed} total={len(merged)} bytes={len(out)}")
PY

echo "[3/3] Applying to target..."
SQL="$WORK/apply.sql"
: > "$SQL"
python3 - "$WORK" "${KEYS[@]}" <<'PY' >> "$SQL"
import sys
work, *keys = sys.argv[1:]
for k in keys:
    val = open(f"{work}/merged_{k}.json").read()
    val_esc = val.replace("'", "''")
    print(f"INSERT INTO options(key, value) VALUES ('{k}', '{val_esc}') "
          f"ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;")
PY

if [[ $DRY_RUN -eq 1 ]]; then
  cp "$SQL" /tmp/newapi_pricing_init.sql
  echo "[dry-run] SQL written to /tmp/newapi_pricing_init.sql ($(wc -c <"$SQL") bytes). Nothing applied."
  exit 0
fi

scp -i "$TGT_SSH_KEY" -o StrictHostKeyChecking=no "$SQL" "$TGT_USER@$TGT_HOST:/tmp/newapi_pricing_init.sql" >/dev/null
ssh_tgt "sudo docker cp /tmp/newapi_pricing_init.sql $TGT_PG_CONTAINER:/tmp/init.sql && sudo docker exec $TGT_PG_CONTAINER psql -U $TGT_PG_USER -d $TGT_PG_DB -f /tmp/init.sql > /dev/null && echo applied"

if [[ $DO_RESTART -eq 1 ]]; then
  echo "Restarting $TGT_APP_CONTAINER on $TGT_HOST..."
  ssh_tgt "sudo docker restart $TGT_APP_CONTAINER >/dev/null && sleep 8 && curl -sS -o /dev/null -w 'health: HTTP %{http_code}\n' http://localhost:3000/api/status"
else
  echo "Skipped restart (--no-restart). Values are live in DB; app must be restarted for them to load into memory."
fi

echo "Done."
