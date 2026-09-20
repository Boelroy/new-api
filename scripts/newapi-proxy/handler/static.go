package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// staticDir is where the new-api web build lives relative to the working directory.
// Set WEB_DIST env var to override (e.g. in Docker).
var staticDir = func() string {
	if v := os.Getenv("WEB_DIST"); v != "" {
		return v
	}
	// Default: ../new-api/web/dist relative to the binary's working directory.
	return "../new-api/web/dist"
}()

// ServeStatic serves the new-api SPA. For any path not matched by an API
// route, return index.html so client-side routing works.
func ServeStatic(c *gin.Context) {
	path := c.Request.URL.Path

	// Never serve the raw index.html for API paths that fell through — 404 them.
	if strings.HasPrefix(path, "/api/") {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "not found"})
		return
	}

	fullPath := filepath.Join(staticDir, filepath.Clean("/"+path))

	// If the exact file exists (JS, CSS, images, etc.), serve it.
	if info, err := os.Stat(fullPath); err == nil && !info.IsDir() {
		c.File(fullPath)
		return
	}

	// Otherwise serve index.html for SPA routing.
	indexPath := filepath.Join(staticDir, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"message": "frontend not built — set WEB_DIST or run `bun run build` in new-api/web/",
		})
		return
	}
	c.File(indexPath)
}
