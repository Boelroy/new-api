package main

// V2 frontend static hosting.
//
// The V2 SPA is a separate Vite build under frontend-v2/. We embed its
// dist/ directory alongside V1's frontend/dist and mount it at /v2/*.
// V1's spaHandler continues to own /, /api/*, and every non-/v2/ path.

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

//go:embed all:frontend-v2/dist
var frontendV2Dist embed.FS

// registerV2Frontend mounts the V2 SPA at /v2/*. It must be called BEFORE
// r.NoRoute so a V2 client-side route like /v2/roles falls back to
// index.html rather than V1's spaHandler.
//
// Why not use http.FileServer for the fallback: FileServer's serveFile()
// contains a "clean up the URL" step that redirects any request whose
// URL.Path ends in /index.html back to `./`. When the SPA hits a
// client-side route like /v2/login we set URL.Path to /index.html to
// serve the shell, which trips that redirect and produces an infinite
// /v2/login → /v2/ → /v2/login loop in the browser. We instead read
// index.html once at startup and write it back manually with the correct
// headers.
func registerV2Frontend(r *gin.Engine) {
	distFS, err := fs.Sub(frontendV2Dist, "frontend-v2/dist")
	if err != nil {
		log.Fatalf("failed to sub frontend-v2/dist: %v", err)
	}
	indexBytes, err := fs.ReadFile(distFS, "index.html")
	if err != nil {
		log.Fatalf("failed to read frontend-v2/dist/index.html: %v (did `bun run build` produce dist/?)", err)
	}
	// Reader used with http.ServeContent so ETag/If-Modified-Since work.
	indexModTime := time.Now()
	fileServer := http.FileServer(http.FS(distFS))
	serveIndex := func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		http.ServeContent(c.Writer, c.Request, "index.html", indexModTime, bytes.NewReader(indexBytes))
	}
	handler := func(c *gin.Context) {
		// Strip the /v2 prefix so file lookups line up with dist/ (which
		// has index.html at its root, not /v2/index.html).
		p := strings.TrimPrefix(c.Request.URL.Path, "/v2")
		if p == "" || p == "/" || p == "/index.html" {
			// Bare /v2, /v2/, and /v2/index.html all serve the SPA shell
			// directly — bypass FileServer to avoid its /index.html → ./
			// canonicalization redirect.
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
				// Rewrite URL.Path so FileServer opens the same file we
				// just confirmed exists.
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
	// Bare /v2 (no trailing slash) and /v2/*
	r.GET("/v2", handler)
	r.GET("/v2/*any", handler)
}
