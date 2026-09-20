# Build context must be the repo root so the frontend (web/) is accessible.
# docker build -f scripts/newapi-proxy/Dockerfile -t newapi-proxy .

# ── Stage 1: build the new-api frontend ─────────────────────────────────────
FROM oven/bun:1 AS web-builder
WORKDIR /src/web
COPY web/package.json web/bun.lockb* ./
RUN bun install --frozen-lockfile
COPY web/ ./
RUN bun run build

# ── Stage 2: build the Go proxy ─────────────────────────────────────────────
FROM golang:1.24-alpine AS go-builder
WORKDIR /src
COPY scripts/newapi-proxy/go.mod scripts/newapi-proxy/go.sum ./
RUN go mod download
COPY scripts/newapi-proxy/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o newapi-proxy .

# ── Stage 3: minimal runtime image ──────────────────────────────────────────
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

COPY --from=go-builder  /src/newapi-proxy        ./newapi-proxy
COPY --from=web-builder /src/web/dist            ./web/dist

ENV WEB_DIST=/app/web/dist \
    LISTEN_ADDR=:8080 \
    GIN_MODE=release

EXPOSE 8080
ENTRYPOINT ["./newapi-proxy"]
