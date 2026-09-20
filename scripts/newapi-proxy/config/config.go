package config

import (
	"log"
	"os"
	"strings"
)

var (
	// RemoteURL is the base URL of the upstream new-api instance, e.g. http://43.172.95.133:3000
	RemoteURL string
	// RemoteAdminToken is the Bearer token used for all upstream requests.
	RemoteAdminToken string
	// DatabaseURL is the PostgreSQL DSN shared with report-service.
	DatabaseURL string
	// JWTSecret must match report-service's JWT_SECRET so tokens are interoperable.
	JWTSecret []byte
	// ListenAddr is the bind address for this proxy, e.g. :8080
	ListenAddr string
	// GinMode is "debug" or "release"
	GinMode string
	// UpstreamProxy is an optional SOCKS5/HTTP proxy for upstream requests,
	// e.g. socks5h://user:pass@host:port. Empty = direct connection.
	UpstreamProxy string
)

func Load() {
	RemoteURL = mustEnv("REMOTE_URL")
	RemoteURL = strings.TrimRight(RemoteURL, "/")
	RemoteAdminToken = mustEnv("REMOTE_ADMIN_TOKEN")
	DatabaseURL = mustEnv("DATABASE_URL")
	JWTSecret = []byte(mustEnv("JWT_SECRET"))
	ListenAddr = envOr("LISTEN_ADDR", ":8080")
	GinMode = envOr("GIN_MODE", "release")
	UpstreamProxy = os.Getenv("UPSTREAM_PROXY")
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env %s is not set", key)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
