package main

// V2 frontend static hosting.
//
// The V2 SPA is a separate Vite build under frontend-v2/. We embed its
// dist/ directory alongside V1's frontend/dist and mount it at /v2/*.
// V1's spaHandler continues to own /, /api/*, and every non-/v2/ path.

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed all:frontend-v2/dist
var frontendV2Dist embed.FS

// registerV2Frontend mounts the V2 SPA at /v2/*. It must be called BEFORE
// r.NoRoute so a V2 client-side route like /v2/roles falls back to
// index.html rather than V1's spaHandler.
func registerV2Frontend(r *gin.Engine) {
	distFS, err := fs.Sub(frontendV2Dist, "frontend-v2/dist")
	if err != nil {
		log.Fatalf("failed to sub frontend-v2/dist: %v", err)
	}
	fileServer := http.FileServer(http.FS(distFS))
	handler := func(c *gin.Context) {
		// Strip the /v2 prefix before serving so file paths line up with
		// dist/ (which has index.html at its root, not v2/index.html).
		p := strings.TrimPrefix(c.Request.URL.Path, "/v2")
		if p == "" || p == "/" {
			p = "/index.html"
		}
		// Try exact file first (JS bundles, CSS, favicon, etc.).
		if f, err := distFS.Open(strings.TrimPrefix(p, "/")); err == nil {
			f.Close()
			c.Request.URL.Path = p
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		// Fall back to index.html for client-side routing.
		c.Request.URL.Path = "/index.html"
		fileServer.ServeHTTP(c.Writer, c.Request)
	}
	// Bare /v2 (no trailing slash) and /v2/*
	r.GET("/v2", handler)
	r.GET("/v2/*any", handler)
}
