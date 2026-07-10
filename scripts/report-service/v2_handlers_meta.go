package main

// V2 metadata endpoints: /me, /permissions, /studios.

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func v2HandleMe(c *gin.Context) {
	ctx := v2Ctx(c)
	perms := make([]string, 0, len(ctx.Permissions))
	for _, p := range ctx.Permissions {
		perms = append(perms, p.String())
	}
	c.JSON(http.StatusOK, gin.H{
		"user_id":        ctx.UserID,
		"username":       ctx.Username,
		"studio":         ctx.Studio,
		"max_role_level": ctx.MaxLevel,
		"is_super":       ctx.IsSuper,
		"permissions":    perms,
	})
}

func v2HandlePermissionsCatalog(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"actions": actionCatalog,
		"scopes":  scopeCatalog,
	})
}

// v2HandleStudiosList shares the query with V1 handleStudiosList so the
// dropdowns stay in sync.
func v2HandleStudiosList(c *gin.Context) {
	rows, err := db.Query(`
		SELECT DISTINCT s FROM (
		  SELECT TRIM(tag)    AS s FROM channels
		  UNION
		  SELECT TRIM(studio) AS s FROM rs_auth_user
		) t
		WHERE s IS NOT NULL AND s <> ''
		ORDER BY s ASC`,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out = append(out, tag)
	}
	c.JSON(http.StatusOK, gin.H{"studios": out})
}
