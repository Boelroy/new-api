#!/usr/bin/env bash
# Sync Claude model pricing (ratio/price options) from a source new-api
# Postgres deployment to a target new-api Postgres deployment via SSH.
#
# Default behavior: pulls the 8 pricing JSON option keys from SOURCE,
# keeps only entries whose key starts with `claude`, then merges them
# into TARGET's existing values (target wins for non-claude entries).
# Pass --mode full to overwrite the whole JSON instead.
#
# Usage:
#   scripts/sync_claude_pricing.sh [--mode claude|full] [--dry-run]
#
# All connection parameters are overridable via env vars (see SRC_*/TGT_*
# block below). Defaults match the current polarcode -> newapi setup.

set -euo pipefail

MODE="claude"
DRY_RUN=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode) MODE="$2"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help)
      sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
if [[ "$MODE" != "claude" && "$MODE" != "full" ]]; then
  echo "--mode must be claude or full" >&2; exit 2
fi

SRC_HOST="${SRC_HOST:-ec2-16-76-159-22.ap-northeast-1.compute.amazonaws.com}"
SRC_USER="${SRC_USER:-ubuntu}"
SRC_SSH_KEY="${SRC_SSH_KEY:-$HOME/.ssh/bo-dev.pem}"
SRC_PG_CONTAINER="${SRC_PG_CONTAINER:-polarcode-postgres}"
SRC_PG_USER="${SRC_PG_USER:-polarcode}"
SRC_PG_DB="${SRC_PG_DB:-polarcode}"

TGT_HOST="${TGT_HOST:-52.198.232.125}"
TGT_USER="${TGT_USER:-ubuntu}"
TGT_SSH_KEY="${TGT_SSH_KEY:-$HOME/.ssh/bo-dev.pem}"
TGT_PG_CONTAINER="${TGT_PG_CONTAINER:-newapi-postgres}"
TGT_PG_USER="${TGT_PG_USER:-postgres}"
TGT_PG_DB="${TGT_PG_DB:-newapi}"
TGT_APP_CONTAINER="${TGT_APP_CONTAINER:-newapi}"

KEYS=(ModelRatio CompletionRatio CacheRatio CreateCacheRatio ModelPrice ImageRatio AudioRatio AudioCompletionRatio)

WORK="$(mktemp -d -t newapi-pricing-XXXX)"
trap 'rm -rf "$WORK"' EXIT

ssh_src() { ssh -i "$SRC_SSH_KEY" -o StrictHostKeyChecking=no "$SRC_USER@$SRC_HOST" "$@"; }
ssh_tgt() { ssh -i "$TGT_SSH_KEY" -o StrictHostKeyChecking=no "$TGT_USER@$TGT_HOST" "$@"; }

echo "[1/4] Dumping pricing from $SRC_HOST ($SRC_PG_DB)..."
for k in "${KEYS[@]}"; do
  ssh_src "sudo docker exec $SRC_PG_CONTAINER psql -U $SRC_PG_USER -d $SRC_PG_DB -tAc \"SELECT value FROM options WHERE key='$k';\"" \
    > "$WORK/src_$k.json"
done

echo "[2/4] Fetching target's current values for merge..."
for k in "${KEYS[@]}"; do
  ssh_tgt "sudo docker exec $TGT_PG_CONTAINER psql -U $TGT_PG_USER -d $TGT_PG_DB -tAc \"SELECT value FROM options WHERE key='$k';\"" \
    > "$WORK/tgt_$k.json"
done

echo "[3/4] Computing merged JSON (mode=$MODE)..."
python3 - "$WORK" "$MODE" "${KEYS[@]}" <<'PY'
import json, sys
work, mode, *keys = sys.argv[1:]
def load(p):
    try:
        s = open(p).read().strip()
        return json.loads(s) if s else {}
    except Exception:
        return {}
for k in keys:
    src = load(f"{work}/src_{k}.json")
    tgt = load(f"{work}/tgt_{k}.json")
    if mode == "full":
        merged = src
    else:
        merged = dict(tgt)
        for kk, vv in src.items():
            if kk.startswith("claude"):
                merged[kk] = vv
    out = json.dumps(merged, separators=(',', ':'), ensure_ascii=False)
    open(f"{work}/merged_{k}.json", "w").write(out)
    print(f"  {k}: src={len(src)} tgt={len(tgt)} merged={len(merged)} bytes={len(out)}")
PY

echo "[4/4] Applying to target..."
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
  cp "$SQL" /tmp/newapi_pricing_sync.sql
  echo "[dry-run] SQL written to /tmp/newapi_pricing_sync.sql ($(wc -c <"$SQL") bytes). Nothing applied."
  exit 0
fi

scp -i "$TGT_SSH_KEY" -o StrictHostKeyChecking=no "$SQL" "$TGT_USER@$TGT_HOST:/tmp/newapi_pricing_sync.sql" >/dev/null
ssh_tgt "sudo docker cp /tmp/newapi_pricing_sync.sql $TGT_PG_CONTAINER:/tmp/sync.sql && sudo docker exec $TGT_PG_CONTAINER psql -U $TGT_PG_USER -d $TGT_PG_DB -f /tmp/sync.sql > /dev/null && echo applied"

echo "Restarting $TGT_APP_CONTAINER on $TGT_HOST..."
ssh_tgt "sudo docker restart $TGT_APP_CONTAINER >/dev/null && sleep 8 && curl -sS -o /dev/null -w 'health: HTTP %{http_code}\n' http://localhost:3000/api/status"

echo "Done."
