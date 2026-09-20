package main

import (
	"log"
	"net/http"
	"strings"

	"newapi-proxy/config"
	"newapi-proxy/handler"
	"newapi-proxy/middleware"
	"newapi-proxy/store"
)

func main() {
	config.Load()
	store.Init(config.DatabaseURL)

	mux := http.NewServeMux()

	// Public auth endpoints (no auth middleware)
	mux.HandleFunc("POST /api/user/login", handler.Login)
	mux.HandleFunc("GET /api/user/logout", handler.Logout)

	// Authenticated: user self
	mux.Handle("GET /api/user/self", middleware.AuthHTTP(http.HandlerFunc(handler.GetSelf)))

	// Authenticated: channel CRUD (exact paths)
	mux.Handle("GET /api/channel/", middleware.AuthHTTP(http.HandlerFunc(handler.ChannelList)))
	mux.Handle("POST /api/channel/", middleware.AuthHTTP(http.HandlerFunc(handler.ChannelCreate)))

	// Authenticated: all other /api/* → passthrough; channel /:id matched inside
	mux.Handle("/api/", middleware.AuthHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// /api/channel/:id — extract id from path
		if strings.HasPrefix(path, "/api/channel/") {
			rest := strings.TrimPrefix(path, "/api/channel/")
			// single segment only (no sub-resource interception)
			if rest != "" && !strings.Contains(rest, "/") {
				handler.ChannelDispatch(w, r, rest)
				return
			}
		}
		// Everything else → transparent passthrough
		handler.PassthroughHTTP(w, r)
	})))

	// Static frontend — catch-all
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handler.ServeStatic(w, r)
	})

	log.Printf("newapi-proxy listening on %s → upstream %s", config.ListenAddr, config.RemoteURL)
	if err := http.ListenAndServe(config.ListenAddr, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}
