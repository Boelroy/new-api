# Build context: repository root. CI builds this image on every v* tag; the
# frontend reuses Report Service V3 components plus two web/ helpers.
FROM oven/bun:1.4.4@sha256:1d22548f177f4ff38e0c15baa3c2c46c6c2060ba2d1bc573bb4720b407548f1d AS frontend
WORKDIR /src/scripts/report-service/frontend-v3
COPY scripts/report-service/frontend-v3/package.json scripts/report-service/frontend-v3/bun.lock ./
RUN bun install --frozen-lockfile
COPY scripts/report-service/frontend-v3/ ./
COPY web/src/i18n/languages.ts /src/web/src/i18n/languages.ts
COPY web/src/lib/number-format.ts /src/web/src/lib/number-format.ts
RUN bun run build:billing

FROM golang:1.26.10-alpine@sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca AS backend
ENV GO111MODULE=on CGO_ENABLED=0 GOWORK=off
ARG TARGETOS
ARG TARGETARCH
ENV GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64}
WORKDIR /build
COPY go.mod go.sum ./
COPY relaykit/go.mod ./relaykit/go.mod
COPY tokenkit/go.mod ./tokenkit/go.mod
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags "-s -w" -o /out/supplier-billing ./cmd/supplier-billing

FROM debian:bookworm-slim@sha256:f06537653ac770703bc45b4b113475bd402f451e85223f0f2837acbf89ab020a

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates wget \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 billing \
    && useradd --uid 10001 --gid 10001 --no-create-home \
       --home-dir /nonexistent --shell /usr/sbin/nologin billing

WORKDIR /app
COPY --from=backend --chmod=0555 /out/supplier-billing /app/supplier-billing
COPY --from=frontend /src/scripts/report-service/frontend-v3/dist-billing/ /app/frontend/
COPY LICENSE NOTICE THIRD-PARTY-LICENSES.md /licenses/

ENV BILLING_DB_DRIVER=postgres \
    BILLING_LISTEN=0.0.0.0:8092 \
    BILLING_FRONTEND_DIR=/app/frontend

USER 10001:10001
EXPOSE 8092
STOPSIGNAL SIGTERM
ENTRYPOINT ["/app/supplier-billing"]
