# newapi-proxy deploy runbook

The newapi-proxy (image: `ghcr.io/boelroy/newapi-proxy`) is built by the
same GitHub Actions workflow that builds `polarcode` and `polarcode-report`.
It triggers on any `v*` tag and pushes to GHCR. Deployment is a manual
compose pin-bump over SSH, same shape as report-service.

Deployment target is wherever you want the proxy to live — typically the
same host as `polarcode-report` (prod: `52.192.154.152`, dev:
`52.198.232.125`).

---

## 1. Local verification

```bash
# From repo root — build context must be the repo root
cd /path/to/new-api

# Go build (inside scripts/newapi-proxy)
cd scripts/newapi-proxy
go build -o /tmp/newapi-proxy-check ./...

# Frontend (if not already built)
cd ../../web
bun install
bun run build
```

Both must exit 0 before tagging.

---

## 2. Commit + tag

Same tag sequence as report-service. Check the latest tag first:

```bash
git tag --sort=-v:refname | head -3
```

Stage only the changed files:

```bash
git add scripts/newapi-proxy/...
git add .github/workflows/docker-publish.yml   # only if the CI job changed

git commit -m "feat(newapi-proxy): <what>

<why>

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"

git push origin main
git tag v1.2.0-rc.<n>
git push origin v1.2.0-rc.<n>
```

---

## 3. Wait for the image

The `build-proxy` CI job runs in parallel with `build-and-push` and
`build-report`. It takes roughly the same time as `build-report` (~4 min)
because it also builds the frontend.

Poll from either host:

```bash
for i in $(seq 1 12); do
  if ssh -o StrictHostKeyChecking=no -i ~/.ssh/bo-dev.pem \
       ubuntu@52.198.232.125 \
       "docker manifest inspect ghcr.io/boelroy/newapi-proxy:v1.2.0-rc.<n> >/dev/null 2>&1"; then
    echo "[$i] image ready"; break
  fi
  echo "[$(date +%H:%M:%S) $i] not ready"; sleep 30
done
```

---

## 4. Add the service to docker-compose (first deploy only)

On whichever host you are deploying to, add a `newapi-proxy` service block
to `docker-compose.yml`. The required environment variables are documented
in `scripts/newapi-proxy/.env.example`.

Example block to append:

```yaml
  newapi-proxy:
    image: ghcr.io/boelroy/newapi-proxy:v1.2.0-rc.<n>
    restart: unless-stopped
    ports:
      - "8081:8080"
    environment:
      REMOTE_URL: "https://your-newapi.example.com"
      REMOTE_ADMIN_TOKEN: "<token>"
      DATABASE_URL: "postgres://user:pass@host/db?sslmode=require"
      JWT_SECRET: "<same as report-service JWT_SECRET>"
      GIN_MODE: "release"
```

`JWT_SECRET` must match the report-service instance sharing the same
Postgres DB. `DATABASE_URL` is the same DSN.

---

## 5. Deploy

**Dev** (`52.198.232.125`):

```bash
ssh -o StrictHostKeyChecking=no -i ~/.ssh/bo-dev.pem ubuntu@52.198.232.125 "
  cd newapi &&
  cp docker-compose.yml docker-compose.yml.bak.rc<n>-\$(date +%s) &&
  sed -i 's|newapi-proxy:v1.2.0-rc.<prev>|newapi-proxy:v1.2.0-rc.<n>|' docker-compose.yml &&
  docker compose pull newapi-proxy 2>&1 | tail -3 &&
  docker compose up -d newapi-proxy 2>&1 | tail -3 &&
  sleep 5 &&
  docker ps --filter name=newapi-proxy --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}' &&
  curl -s -o /dev/null -w 'dev HTTP %{http_code}\n' http://localhost:8081/
"
```

**Prod** (`52.192.154.152`):

```bash
ssh -o StrictHostKeyChecking=no -i ~/.ssh/polarcode.pem ubuntu@52.192.154.152 "
  cd polarcode &&
  cp docker-compose.yml docker-compose.yml.bak.rc<n>-\$(date +%s) &&
  sed -i 's|newapi-proxy:v1.2.0-rc.<prev>|newapi-proxy:v1.2.0-rc.<n>|' docker-compose.yml &&
  docker compose pull newapi-proxy 2>&1 | tail -3 &&
  docker compose up -d newapi-proxy 2>&1 | tail -3 &&
  sleep 5 &&
  docker ps --filter name=newapi-proxy --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}' &&
  curl -s -o /dev/null -w 'prod HTTP %{http_code}\n' http://localhost:8081/
"
```

Watch for `Up N seconds` with the new tag in the `IMAGE` column and `HTTP 200`.

---

## 6. Verify

```bash
# Dev
ssh -i ~/.ssh/bo-dev.pem ubuntu@52.198.232.125 "docker logs newapi-proxy --tail 20"

# Prod
ssh -i ~/.ssh/polarcode.pem ubuntu@52.192.154.152 "docker logs newapi-proxy --tail 20"
```

Expected startup lines:
- `store: ready` — Postgres connected, `proxy_channel_ownership` migrated
- `newapi-proxy listening on :8080 → upstream https://...`

---

## 7. Rollback

```bash
ssh -i ~/.ssh/bo-dev.pem ubuntu@52.198.232.125 "
  cd newapi &&
  ls -1t docker-compose.yml.bak.rc<n>-* | head -1 | xargs -I{} cp {} docker-compose.yml &&
  grep newapi-proxy docker-compose.yml &&
  docker compose up -d newapi-proxy
"
```

The `proxy_channel_ownership` table uses only `CREATE TABLE IF NOT EXISTS`,
so rollback leaves the schema in a safe superset state.

---

## Quick reference

| Item | Value |
|------|-------|
| Image | `ghcr.io/boelroy/newapi-proxy:<tag>` |
| Internal port | 8080 |
| Suggested host port | 8081 |
| DB | shared with report-service (`rs_auth_user` for auth) |
| New table | `proxy_channel_ownership` (auto-migrated on startup) |
| Required secrets | `REMOTE_URL`, `REMOTE_ADMIN_TOKEN`, `DATABASE_URL`, `JWT_SECRET` |
| JWT_SECRET | must match report-service on the same DB |
