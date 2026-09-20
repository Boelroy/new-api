package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// staticDir is where the new-api web build lives.
// Set WEB_DIST env var to override (e.g. in Docker).
var staticDir = func() string {
	if v := os.Getenv("WEB_DIST"); v != "" {
		return v
	}
	return "../new-api/web/dist"
}()

// ServeStatic serves the new-api SPA. Static assets are served directly;
// everything else falls through to index.html for client-side routing.
func ServeStatic(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	if strings.HasPrefix(path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{"success": false, "message": "not found"})
		return
	}

	fullPath := filepath.Join(staticDir, filepath.Clean("/"+path))
	if info, err := os.Stat(fullPath); err == nil && !info.IsDir() {
		http.ServeFile(w, r, fullPath)
		return
	}

	indexPath := filepath.Join(staticDir, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"message": "frontend not built — set WEB_DIST or run `bun run build` in new-api/web/",
		})
		return
	}
	http.ServeFile(w, r, indexPath)
}
