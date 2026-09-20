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
	// NOTE: gin does not allow a catch-all wildcard on the same prefix as
	// concrete children. We attach the auth middleware per-route instead of
	// using a group so that /api/*path does not conflict with the explicit
	// /api/user/* and /api/channel/* routes.
	authMW := middleware.Auth()

	// Self
	r.GET("/api/user/self", authMW, handler.GetSelf)

	// Channels — ownership-filtered
	r.GET("/api/channel/", authMW, handler.ChannelList)
	r.POST("/api/channel/", authMW, handler.ChannelCreate)
	r.GET("/api/channel/:id", authMW, handler.ChannelGet)
	r.PUT("/api/channel/:id", authMW, handler.ChannelUpdate)
	r.DELETE("/api/channel/:id", authMW, handler.ChannelDelete)

	// Everything else — transparent passthrough to upstream new-api.
	// Use a dedicated sub-router so the wildcard does not see the routes above.
	pass := r.Group("/api", authMW)
	pass.Any("/*path", handler.Passthrough)

	// ── Static frontend (new-api web build) ─────────────────────────────────
	// Mount last so API routes always win.
	r.NoRoute(handler.ServeStatic)

	log.Printf("newapi-proxy listening on %s → upstream %s", config.ListenAddr, config.RemoteURL)
	if err := r.Run(config.ListenAddr); err != nil {
		log.Fatalf("server: %v", err)
	}
}
