# newapi-proxy

A lightweight reverse proxy that presents the new-api frontend to suppliers while isolating each supplier's channels. All traffic goes directly to the upstream new-api instance using your admin token — suppliers never touch your admin credentials.

## How it works

```
Supplier browser
    ↓ login (proxy's own session)
newapi-proxy  ←── shared rs_auth_user + JWT_SECRET with report-service
    ├── /api/user/*        handled locally (login / self)
    ├── /api/channel/*     filtered by proxy_channel_ownership table
    └── /api/*             transparent passthrough + admin token injected
    ↓
Upstream new-api (your admin token)
    ↓
Official AI APIs
```

- Suppliers log in with their report-service credentials.
- Channels: each supplier only sees and can modify channels they created through this proxy.
- Logs / usage / models: shared — passed through as-is.
- The `proxy_channel_ownership` table lives in the same Postgres DB as report-service and maps `remote_channel_id → user_id`.

## Setup

### 1. Build the frontend

```sh
cd ../new-api/web
bun install
bun run build
# output is in ../new-api/web/dist
```

### 2. Configure environment

```sh
cp .env.example .env
# edit .env — at minimum set REMOTE_URL, REMOTE_ADMIN_TOKEN, DATABASE_URL, JWT_SECRET
```

| Variable             | Required | Description |
|----------------------|----------|-------------|
| `REMOTE_URL`         | ✓        | Base URL of upstream new-api, e.g. `https://api.example.com` |
| `REMOTE_ADMIN_TOKEN` | ✓        | Bearer token with admin rights on upstream new-api |
| `DATABASE_URL`       | ✓        | PostgreSQL DSN shared with report-service |
| `JWT_SECRET`         | ✓        | Must match report-service's `JWT_SECRET` |
| `LISTEN_ADDR`        |          | Bind address (default `:8080`) |
| `GIN_MODE`           |          | `release` (default) or `debug` |
| `WEB_DIST`           |          | Path to web build output (default `../new-api/web/dist`) |

### 3. Run

```sh
go build -o newapi-proxy .
./newapi-proxy
```

Or with environment inline:

```sh
REMOTE_URL=https://... REMOTE_ADMIN_TOKEN=sk-... DATABASE_URL=postgres://... JWT_SECRET=... ./newapi-proxy
```

### 4. Docker (optional)

```dockerfile
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o newapi-proxy .

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /app/newapi-proxy .
COPY web/dist ./web/dist
ENV WEB_DIST=/app/web/dist
EXPOSE 8080
CMD ["./newapi-proxy"]
```

## Supplier account management

Suppliers log in with accounts created in report-service (`rs_auth_user` table). Create accounts there as usual — their credentials and roles carry over. Any role level works; the proxy always presents `role: 10` (admin) to the new-api frontend so the channel UI renders.

## Channel isolation

- **Create**: proxy records `(remote_channel_id, user_id)` in `proxy_channel_ownership` after the upstream creates the channel.
- **List**: filtered to owned channels only (superadmin role ≥ 100 sees all).
- **Get / Update / Delete**: ownership check before forwarding; 403 if not owner.
- Superadmins (role ≥ 100 in report-service) bypass all filters.
