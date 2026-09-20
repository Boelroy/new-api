package middleware

import (
	"database/sql"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"newapi-proxy/config"
	"newapi-proxy/store"
)

type Claims struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Role     int    `json:"role"`
	Studio   string `json:"studio"`
}

// Auth validates the JWT cookie set by report-service (shared JWT_SECRET).
// On success it sets user_id, username, role, studio on the gin context.
func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr, err := c.Cookie("token")
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "unauthorized"})
			c.Abort()
			return
		}

		parsed, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method")
			}
			return config.JWTSecret, nil
		})
		if err != nil || !parsed.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "invalid token"})
			c.Abort()
			return
		}

		claims, ok := parsed.Claims.(jwt.MapClaims)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "invalid claims"})
			c.Abort()
			return
		}

		var userID int64
		var iat int64
		if v, ok := claims["user_id"].(float64); ok {
			userID = int64(v)
		}
		if v, ok := claims["iat"].(float64); ok {
			iat = int64(v)
		}

		// Verify account is still active in the shared rs_auth_user table.
		if userID > 0 {
			var status int
			var disabledAt int64
			err := store.DB.QueryRow(
				`SELECT status, disabled_at FROM rs_auth_user WHERE id=$1`, userID,
			).Scan(&status, &disabledAt)
			if err == sql.ErrNoRows {
				c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "account not found"})
				c.Abort()
				return
			}
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "auth lookup failed"})
				c.Abort()
				return
			}
			if status == 0 {
				c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "account disabled"})
				c.Abort()
				return
			}
			if disabledAt > 0 && iat > 0 && iat < disabledAt {
				c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "token revoked"})
				c.Abort()
				return
			}
		}

		username, _ := claims["username"].(string)
		role := 1
		if v, ok := claims["role"].(float64); ok {
			role = int(v)
		}
		studio, _ := claims["studio"].(string)

		c.Set("user_id", userID)
		c.Set("username", username)
		c.Set("role", role)
		c.Set("studio", studio)
		c.Next()
	}
}

// RequireRole aborts with 403 when the caller's role is below min.
func RequireRole(min int) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("role")
		if r, ok := role.(int); ok && r >= min {
			c.Next()
			return
		}
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "forbidden"})
		c.Abort()
	}
}
