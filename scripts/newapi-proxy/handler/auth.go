package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"newapi-proxy/config"
	"newapi-proxy/middleware"
	"newapi-proxy/store"
)

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"success": false, "message": msg})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		jsonErr(w, http.StatusBadRequest, "username and password required")
		return
	}

	var id int64
	var hash, studio string
	var role, status int
	err := store.DB.QueryRow(
		`SELECT id, password_hash, role, studio, status FROM rs_auth_user WHERE username=$1`,
		req.Username,
	).Scan(&id, &hash, &role, &studio, &status)
	if err == sql.ErrNoRows {
		jsonErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "database error")
		return
	}
	if status == 0 {
		jsonErr(w, http.StatusUnauthorized, "account disabled")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		jsonErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":      req.Username,
		"user_id":  id,
		"username": req.Username,
		// Always present role=10 (admin) so the new-api frontend renders channel controls.
		// Actual privilege enforcement happens server-side in this proxy.
		"role":   10,
		"studio": studio,
		"exp":    now.Add(24 * time.Hour).Unix(),
		"iat":    now.Unix(),
	})
	signed, err := token.SignedString(config.JWTSecret)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "token sign error")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "token",
		Value:    signed,
		Path:     "/",
		MaxAge:   86400,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	jsonOK(w, map[string]any{
		"success": true,
		"message": "",
		"data": map[string]any{
			"access_token": signed,
			"token_type":   "Bearer",
			"user":         buildUser(id, req.Username, status),
		},
	})
}

func Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "token", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	jsonOK(w, map[string]any{"success": true, "message": ""})
}

// RefreshToken re-issues a fresh JWT for the already-authenticated caller.
// The upstream new-api requires a browser session cookie for its refresh
// endpoint which this proxy cannot provide, so we handle it locally.
func RefreshToken(w http.ResponseWriter, r *http.Request) {
	uid := middleware.UserIDFromCtx(r)
	var username, studio string
	var status int
	err := store.DB.QueryRow(
		`SELECT username, studio, status FROM rs_auth_user WHERE id=$1`, uid,
	).Scan(&username, &studio, &status)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "user lookup failed")
		return
	}
	if status == 0 {
		jsonErr(w, http.StatusUnauthorized, "account disabled")
		return
	}

	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":      username,
		"user_id":  uid,
		"username": username,
		"role":     10,
		"studio":   studio,
		"exp":      now.Add(24 * time.Hour).Unix(),
		"iat":      now.Unix(),
	})
	signed, err := token.SignedString(config.JWTSecret)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "token sign error")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "token",
		Value:    signed,
		Path:     "/",
		MaxAge:   86400,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	jsonOK(w, map[string]any{
		"success": true,
		"message": "",
		"data": map[string]any{
			"access_token":      signed,
			"token_type":        "Bearer",
			"access_expires_at": now.Add(24 * time.Hour).Unix(),
			"user":              buildUser(uid, username, status),
		},
	})
}

func GetSelf(w http.ResponseWriter, r *http.Request) {
	uid := middleware.UserIDFromCtx(r)
	var username string
	var status int
	err := store.DB.QueryRow(
		`SELECT username, status FROM rs_auth_user WHERE id=$1`, uid,
	).Scan(&username, &status)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "user lookup failed")
		return
	}
	jsonOK(w, map[string]any{
		"success": true,
		"message": "",
		"data":    buildUser(uid, username, status),
	})
}

func buildUser(id int64, username string, status int) map[string]any {
	return map[string]any{
		"id":           id,
		"username":     username,
		"display_name": username,
		"role":         10,
		"status":       status,
		"group":        "default",
		"quota":        0,
		"used_quota":   0,
		"has_password": true,
		"email":        "",
		"permissions": map[string]any{
			"sidebar_settings":  false,
			"sidebar_modules":   map[string]any{},
			"admin_permissions": map[string]any{},
		},
	}
}
