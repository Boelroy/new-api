package main

// V2 route registration. Called from main() after V1 routes are wired.
// All V2 routes live under /api/v2/*.

import "github.com/gin-gonic/gin"

// registerV2Routes wires every /api/v2/* endpoint onto r. Every route runs
// authMiddleware (V1) first to establish user_id/role, then v2AuthContext
// to populate the v2Context, then per-route requirePermission.
func registerV2Routes(r *gin.Engine) {
	api := r.Group("/api/v2", authMiddleware, v2AuthContext)

	// Metadata — any authenticated user.
	api.GET("/me", v2HandleMe)
	api.GET("/permissions", v2HandlePermissionsCatalog)
	api.GET("/studios", v2HandleStudiosList)

	// Role management.
	api.GET("/roles", requirePermission(ActionRolesView, ScopeGlobal), v2HandleRoleList)
	api.POST("/roles", requirePermission(ActionRolesManage, ScopeGlobal), v2HandleRoleCreate)
	api.PATCH("/roles/:id", requirePermission(ActionRolesManage, ScopeGlobal), v2HandleRoleUpdate)
	api.DELETE("/roles/:id", requirePermission(ActionRolesManage, ScopeGlobal), v2HandleRoleDelete)

	// User management. Route-level permission gates are the outer edge —
	// each handler additionally enforces the ladder guard against the
	// target user's max_role_level.
	api.GET("/users", requirePermission(ActionUsersView, ScopeGlobal), v2HandleUserList)
	api.POST("/users", requirePermission(ActionUsersCreate, ScopeGlobal), v2HandleUserCreate)
	api.PATCH("/users/:id", requirePermission(ActionUsersUpdate, ScopeGlobal), v2HandleUserUpdate)
	api.POST("/users/:id/reset-password", requirePermission(ActionUsersResetPassword, ScopeGlobal), v2HandleUserResetPassword)
	api.POST("/users/:id/disable", requirePermission(ActionUsersDisable, ScopeGlobal), v2HandleUserSetStatus(true))
	api.POST("/users/:id/enable", requirePermission(ActionUsersDisable, ScopeGlobal), v2HandleUserSetStatus(false))
	api.DELETE("/users/:id", requirePermission(ActionUsersDelete, ScopeGlobal), v2HandleUserDelete)
	api.POST("/users/:id/roles", requirePermission(ActionUsersAssignRole, ScopeGlobal), v2HandleUserAssignRoles)

	// Key Pool (M2).
	registerV2KeysRoutes(api)

	// Usage + profiles + settings (M3).
	registerV2UsageRoutes(api)
	registerV2ProfilesRoutes(api)
}
