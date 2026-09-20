package middleware

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/golang-jwt/jwt/v5"
	"newapi-proxy/config"
	"newapi-proxy/store"
)

type ctxKey int

const (
	ctxUserID   ctxKey = iota
	ctxUsername ctxKey = iota
	ctxRole     ctxKey = iota
	ctxStudio   ctxKey = iota
)

// UserIDFromCtx extracts the authenticated user ID from the request context.
func UserIDFromCtx(r *http.Request) int64 {
	v, _ := r.Context().Value(ctxUserID).(int64)
	return v
}

// RoleFromCtx extracts the authenticated user role from the request context.
func RoleFromCtx(r *http.Request) int {
	v, _ := r.Context().Value(ctxRole).(int)
	return v
}

// AuthHTTP validates the JWT cookie and injects user info into the context.
func AuthHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("token")
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "unauthorized"})
			return
		}

		parsed, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method")
			}
			return config.JWTSecret, nil
		})
		if err != nil || !parsed.Valid {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "invalid token"})
			return
		}

		claims, ok := parsed.Claims.(jwt.MapClaims)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "invalid claims"})
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

		if userID > 0 {
			var status int
			var disabledAt int64
			err := store.DB.QueryRow(
				`SELECT status, disabled_at FROM rs_auth_user WHERE id=$1`, userID,
			).Scan(&status, &disabledAt)
			if err == sql.ErrNoRows {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "account not found"})
				return
			}
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "auth lookup failed"})
				return
			}
			if status == 0 {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "account disabled"})
				return
			}
			if disabledAt > 0 && iat > 0 && iat < disabledAt {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "token revoked"})
				return
			}
		}

		username, _ := claims["username"].(string)
		role := 1
		if v, ok := claims["role"].(float64); ok {
			role = int(v)
		}
		studio, _ := claims["studio"].(string)

		ctx := r.Context()
		ctx = context.WithValue(ctx, ctxUserID, userID)
		ctx = context.WithValue(ctx, ctxUsername, username)
		ctx = context.WithValue(ctx, ctxRole, role)
		ctx = context.WithValue(ctx, ctxStudio, studio)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
