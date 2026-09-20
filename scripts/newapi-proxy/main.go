package main

import (
	"log"

	"github.com/gin-gonic/gin"
	"newapi-proxy/config"
	"newapi-proxy/handler"
	"newapi-proxy/middleware"
	"newapi-proxy/store"
)

func main() {
	config.Load()
	store.Init(config.DatabaseURL)
	gin.SetMode(config.GinMode)

	r := gin.Default()

	// ── Auth (no cookie required) ────────────────────────────────────────────
	r.POST("/api/user/login", handler.Login)
	r.GET("/api/user/logout", handler.Logout)

	// ── Authenticated routes ─────────────────────────────────────────────────
	auth := r.Group("/", middleware.Auth())
	{
		// Self
		auth.GET("/api/user/self", handler.GetSelf)

		// Channels — ownership-filtered
		auth.GET("/api/channel/", handler.ChannelList)
		auth.POST("/api/channel/", handler.ChannelCreate)
		auth.GET("/api/channel/:id", handler.ChannelGet)
		auth.PUT("/api/channel/:id", handler.ChannelUpdate)
		auth.DELETE("/api/channel/:id", handler.ChannelDelete)

		// Everything else — transparent passthrough to upstream new-api
		auth.Any("/api/*path", handler.Passthrough)
	}

	// ── Static frontend (new-api web build) ─────────────────────────────────
	// Mount last so API routes always win.
	r.NoRoute(handler.ServeStatic)

	log.Printf("newapi-proxy listening on %s → upstream %s", config.ListenAddr, config.RemoteURL)
	if err := r.Run(config.ListenAddr); err != nil {
		log.Fatalf("server: %v", err)
	}
}
