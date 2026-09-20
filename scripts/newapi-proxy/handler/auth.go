package handler

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"newapi-proxy/config"
	"newapi-proxy/store"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Login authenticates against rs_auth_user (shared with report-service) and
// issues a JWT cookie using the shared JWT_SECRET.
func Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid request"})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "username and password required"})
		return
	}

	var id int64
	var hash string
	var role int
	var studio string
	var status int
	err := store.DB.QueryRow(
		`SELECT id, password_hash, role, studio, status FROM rs_auth_user WHERE username=$1`,
		req.Username,
	).Scan(&id, &hash, &role, &studio, &status)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "invalid username or password"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "database error"})
		return
	}
	if status == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "account disabled"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "invalid username or password"})
		return
	}

	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":      req.Username,
		"user_id":  id,
		"username": req.Username,
		// Always present as role=10 (admin) to new-api frontend so channel UI is visible.
		// Actual privilege checks happen server-side in this proxy.
		"role":   10,
		"studio": studio,
		"exp":    now.Add(24 * time.Hour).Unix(),
		"iat":    now.Unix(),
	})
	signed, err := token.SignedString(config.JWTSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "token sign error"})
		return
	}

	c.SetCookie("token", signed, 86400, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"access_token": signed,
			"token_type":   "Bearer",
			"user": gin.H{
				"id":           id,
				"username":     req.Username,
				"display_name": req.Username,
				"role":         10, // admin tier — lets the frontend render all channel controls
				"status":       status,
				"group":        "default",
				"quota":        0,
				"used_quota":   0,
				"permissions": gin.H{
					"sidebar_settings": false,
					"sidebar_modules":  gin.H{},
					"admin_permissions": gin.H{},
				},
			},
		},
	})
}

// Logout clears the token cookie.
func Logout(c *gin.Context) {
	c.SetCookie("token", "", -1, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

// GetSelf returns the caller's own user record, shaped to match new-api's /api/user/self.
func GetSelf(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(int64)

	var username, studio string
	var status, role int
	err := store.DB.QueryRow(
		`SELECT username, role, studio, status FROM rs_auth_user WHERE id=$1`, uid,
	).Scan(&username, &role, &studio, &status)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "user lookup failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"id":           uid,
			"username":     username,
			"display_name": username,
			"role":         10,
			"status":       status,
			"group":        "default",
			"quota":        0,
			"used_quota":   0,
			"permissions": gin.H{
				"sidebar_settings": false,
				"sidebar_modules":  gin.H{},
				"admin_permissions": gin.H{},
			},
		},
	})
}
