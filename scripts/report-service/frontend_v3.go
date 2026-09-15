package main

// V3 frontend static hosting ("NewApi Dash").
//
// The V3 SPA is a separate Vite build under frontend-v3/. We embed its
// dist/ directory alongside V1's frontend/dist and V2's frontend-v2/dist and
// mount it at /v3/*. V1's spaHandler continues to own /, /api/*, and every
// non-/v2/, non-/v3/ path. This mirrors registerV2Frontend exactly.

import (
	"bytes"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

//go:embed all:frontend-v3/dist
var frontendV3Dist embed.FS

// v3 SPA shell, cached at registration so spaHandler can serve it at root for
// hosts listed in V3_ROOT_HOSTS (e.g. gw.nexroute.cc) — see serveV3Shell.
var (
	v3IndexBytes   []byte
	v3IndexModTime = time.Now()
)

// serveV3Shell writes the v3 index.html (the SPA shell). Its hashed assets are
// referenced absolutely under /v3/assets and stay served by registerV3Frontend,
// so this works whether the shell is served at /v3 or at root.
func serveV3Shell(c *gin.Context) {
	c.Header("Cache-Control", "no-cache")
	http.ServeContent(c.Writer, c.Request, "index.html", v3IndexModTime, bytes.NewReader(v3IndexBytes))
}

// Hosts that should serve the v3 SPA at root (/) instead of the legacy v1 UI.
// These get the new "AI Gateway" UI with no /v3 prefix in the URL, while every
// other host keeps v1 at /. Configured via V3_ROOT_HOSTS (comma-separated).
var v3RootHosts = map[string]bool{}

// initV3RootHosts seeds v3RootHosts from the env value. Empty env falls back to
// the known gw hostname so the split works out of the box.
func initV3RootHosts(raw string) {
	if strings.TrimSpace(raw) == "" {
		raw = "gw.nexroute.cc"
	}
	for _, h := range strings.Split(raw, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			v3RootHosts[h] = true
		}
	}
}

// isV3RootHost reports whether this request's Host should get the v3 SPA at root.
func isV3RootHost(host string) bool {
	if len(v3RootHosts) == 0 {
		return false
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return v3RootHosts[strings.ToLower(host)]
}

// registerV3Frontend mounts the V3 SPA at /v3/*. It must be called BEFORE
// r.NoRoute so a V3 client-side route like /v3/remote-channels falls back to
// index.html rather than V1's spaHandler.
//
// Why not use http.FileServer for the fallback: FileServer's serveFile()
// contains a "clean up the URL" step that redirects any request whose
// URL.Path ends in /index.html back to `./`. When the SPA hits a client-side
// route like /v3/login we set URL.Path to /index.html to serve the shell,
// which trips that redirect and produces an infinite /v3/login → /v3/ →
// /v3/login loop in the browser. We instead read index.html once at startup
// and write it back manually with the correct headers.
func registerV3Frontend(r *gin.Engine) {
	distFS, err := fs.Sub(frontendV3Dist, "frontend-v3/dist")
	if err != nil {
		log.Fatalf("failed to sub frontend-v3/dist: %v", err)
	}
	indexBytes, err := fs.ReadFile(distFS, "index.html")
	if err != nil {
		log.Fatalf("failed to read frontend-v3/dist/index.html: %v (did `bun run build` produce dist/?)", err)
	}
	v3IndexBytes = indexBytes
	fileServer := http.FileServer(http.FS(distFS))
	serveIndex := serveV3Shell
	handler := func(c *gin.Context) {
		// Strip the /v3 prefix so file lookups line up with dist/ (which has
		// index.html at its root, not /v3/index.html).
		p := strings.TrimPrefix(c.Request.URL.Path, "/v3")
		if p == "" || p == "/" || p == "/index.html" {
			serveIndex(c)
			return
		}
		clean := path.Clean(p)
		trimmed := strings.TrimPrefix(clean, "/")
		if trimmed == "" || strings.HasPrefix(trimmed, "..") {
			serveIndex(c)
			return
		}
		// Try to serve as a static asset (JS bundle, CSS, favicon).
		if f, err := distFS.Open(trimmed); err == nil {
			if st, statErr := f.Stat(); statErr == nil && !st.IsDir() {
				f.Close()
				orig := c.Request.URL.Path
				c.Request.URL.Path = "/" + trimmed
				fileServer.ServeHTTP(c.Writer, c.Request)
				c.Request.URL.Path = orig
				return
			}
			f.Close()
		}
		// Unknown path → SPA client-side route → serve index.html.
		serveIndex(c)
	}
	// Bare /v3 (no trailing slash) and /v3/*
	r.GET("/v3", handler)
	r.GET("/v3/*any", handler)
}
