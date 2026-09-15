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
	indexModTime := time.Now()
	fileServer := http.FileServer(http.FS(distFS))
	serveIndex := func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		http.ServeContent(c.Writer, c.Request, "index.html", indexModTime, bytes.NewReader(indexBytes))
	}
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
