package supplierbilling

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"golang.org/x/crypto/argon2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	sessionIdleSeconds = int64(30 * 60)
	sessionMaxSeconds  = int64(12 * 60 * 60)
	loginWindowSeconds = int64(15 * 60)
	// A fixed, valid Argon2id encoding makes unknown-account checks perform
	// the same KDF work as known accounts without creating a usable account.
	dummyPasswordHash = "$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

//go:embed password_blocklist.txt
var passwordBlocklist string

var (
	authKDFSlots     = make(chan struct{}, 2)
	errAuthBusy      = errors.New("Authentication is busy. Try again shortly.")
	errAuthChanged   = errors.New("Account changed. Sign in again.")
	errLastManager   = errors.New("The last billing manager cannot be disabled.")
	blockedPasswords = sync.OnceValue(func() map[string]struct{} {
		blocked := make(map[string]struct{}, 3000)
		for line := range strings.SplitSeq(passwordBlocklist, "\n") {
			if line != "" && !strings.HasPrefix(line, "# ") {
				blocked[line] = struct{}{}
			}
		}
		return blocked
	})
)

// Session belongs only to this service. Neither gateway nor Report Service
// cookies, JWTs, user IDs, API keys, or password configuration are accepted.
type Session struct {
	TokenHash  string `gorm:"primaryKey;size:64" json:"-"`
	UserID     int64  `gorm:"index;not null" json:"-"`
	CSRFToken  string `gorm:"size:64;not null" json:"-"`
	CreatedAt  int64  `json:"-"`
	LastSeenAt int64  `json:"-"`
	ExpiresAt  int64  `gorm:"index" json:"-"`
}

func (Session) TableName() string { return "sb_sessions" }

// Rate windows are persisted and shared by all replicas, including attempts
// against unknown accounts. Throttling expires automatically after 15 minutes;
// it never permanently disables an account. Only peer IPs are trusted.
type LoginAttempt struct {
	ID          string `gorm:"primaryKey;size:64"`
	Attempts    int64
	WindowStart int64 `gorm:"index"`
}

func (LoginAttempt) TableName() string { return "sb_login_attempts" }

// One portable write lock serializes credential and session issuance changes.
// Unlike a process mutex, this also protects multiple application replicas.
type AuthGuard struct {
	ID      int64 `gorm:"primaryKey;autoIncrement:false"`
	Version int64
}

func (AuthGuard) TableName() string { return "sb_auth_guard" }

func AuthModels() []any { return []any{&Session{}, &LoginAttempt{}, &AuthGuard{}} }

type authContextKey struct{}

type authenticatedRequest struct {
	User    User
	Session Session
}

func currentUser(r *http.Request) User {
	value, _ := r.Context().Value(authContextKey{}).(authenticatedRequest)
	return value.User
}

func (s *Server) RegisterAuth(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("GET /api/session", s.auth(s.session))
	mux.HandleFunc("POST /api/logout", s.auth(s.logout))
	mux.HandleFunc("POST /api/password", s.auth(s.changePassword))
	mux.HandleFunc("GET /api/users", s.manager(s.listUsers))
	mux.HandleFunc("POST /api/users", s.manager(s.createUser))
	mux.HandleFunc("POST /api/users/{id}/disable", s.manager(s.disableUser))
}

func (s *Server) cookieName() string {
	if s.SecureCookies {
		return "__Host-supplier_billing"
	}
	return "supplier_billing"
}

func (s *Server) sessionCookie(token string, expires int64) *http.Cookie {
	return &http.Cookie{
		Name: s.cookieName(), Value: token, Path: "/", HttpOnly: true,
		Secure: s.SecureCookies, SameSite: http.SameSiteStrictMode,
		Expires: time.Unix(expires, 0),
	}
}

func hashSessionToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(s.cookieName())
		if err != nil || len(cookie.Value) != 43 {
			s.fail(w, http.StatusUnauthorized, "Please sign in.")
			return
		}
		now := time.Now().Unix()
		var session Session
		err = s.DB.Where("token_hash = ? AND expires_at > ? AND last_seen_at > ?", hashSessionToken(cookie.Value), now, now-sessionIdleSeconds).First(&session).Error
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				s.fail(w, http.StatusServiceUnavailable, "Authentication is unavailable.")
				return
			}
			s.fail(w, http.StatusUnauthorized, "Please sign in.")
			return
		}
		var user User
		err = s.DB.Where("id = ? AND disabled = ?", session.UserID, false).First(&user).Error
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				s.fail(w, http.StatusServiceUnavailable, "Authentication is unavailable.")
				return
			}
			s.fail(w, http.StatusUnauthorized, "Please sign in.")
			return
		}
		if user.Role != RoleManager && user.Role != RoleSupplier || user.Role == RoleSupplier && user.SupplierID <= 0 {
			s.fail(w, http.StatusForbidden, "Access denied.")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			csrf := r.Header.Get("X-CSRF-Token")
			if r.Header.Get("Origin") != s.Origin || len(csrf) != len(session.CSRFToken) || subtle.ConstantTimeCompare([]byte(csrf), []byte(session.CSRFToken)) != 1 {
				s.fail(w, http.StatusForbidden, "Request verification failed. Refresh and try again.")
				return
			}
		}
		touched := s.DB.Model(&Session{}).Where("token_hash = ? AND expires_at > ? AND last_seen_at > ?", session.TokenHash, now, now-sessionIdleSeconds).UpdateColumn("last_seen_at", now)
		if touched.Error != nil {
			s.fail(w, http.StatusServiceUnavailable, "Authentication is unavailable.")
			return
		}
		if touched.RowsAffected == 0 {
			// MySQL can report zero for a same-second no-op update. Recheck
			// existence so a concurrent logout is rejected on every dialect.
			var active int64
			if err := s.DB.Model(&Session{}).Where("token_hash = ? AND expires_at > ? AND last_seen_at > ?", session.TokenHash, now, now-sessionIdleSeconds).Count(&active).Error; err != nil {
				s.fail(w, http.StatusServiceUnavailable, "Authentication is unavailable.")
				return
			}
			if active != 1 {
				s.fail(w, http.StatusUnauthorized, "Please sign in.")
				return
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, authenticatedRequest{User: user, Session: session}))
		next(w, r)
	}
}

func (s *Server) manager(next http.HandlerFunc) http.HandlerFunc {
	return s.auth(func(w http.ResponseWriter, r *http.Request) {
		if currentUser(r).Role != RoleManager {
			s.fail(w, http.StatusForbidden, "Access denied.")
			return
		}
		next(w, r)
	})
}

func authPeer(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "unknown"
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "unknown"
	}
	return ip.String()
}

func authAudit(r *http.Request, event string, userID int64, username string) {
	// Account fingerprints correlate failed attempts without logging arbitrary
	// submitted text. Passwords, session IDs and CSRF tokens are never included.
	fingerprint := hashSessionToken(username)
	log.Print(common.LogText("[supplier-billing auth] event=%s actor_id=%d user_id=%d account=%s peer=%s", event, currentUser(r).ID, userID, fingerprint[:16], authPeer(r)))
}

func (s *Server) allowAuthAttempt(w http.ResponseWriter, r *http.Request, username string) bool {
	now := time.Now().Unix()
	windowStart := now - now%loginWindowSeconds
	subjects := []struct {
		value string
		limit int64
	}{{"account:" + username, 10}, {"peer:" + authPeer(r), 50}}
	blocked := false
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		for _, subject := range subjects {
			attempt := LoginAttempt{ID: hashSessionToken(subject.value + ":" + strconv.FormatInt(windowStart, 10)), Attempts: 1, WindowStart: windowStart}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.Assignments(map[string]any{"attempts": gorm.Expr("? + ?", clause.Column{Table: "sb_login_attempts", Name: "attempts"}, 1)})}).Create(&attempt).Error; err != nil {
				return err
			}
			if err := tx.First(&attempt, "id = ?", attempt.ID).Error; err != nil {
				return err
			}
			blocked = blocked || attempt.Attempts > subject.limit
		}
		return tx.Where("window_start < ?", now-24*60*60).Delete(&LoginAttempt{}).Error
	})
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, "Authentication is unavailable.")
		return false
	}
	if blocked {
		w.Header().Set("Retry-After", strconv.FormatInt(windowStart+loginWindowSeconds-now, 10))
		authAudit(r, "throttled", 0, username)
		s.fail(w, http.StatusTooManyRequests, "Too many authentication attempts. Try again later.")
		return false
	}
	return true
}

func validateBillingPassword(password, username string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 15 || utf8.RuneCountInString(password) > 128 {
		return errors.New("Password must contain between 15 and 128 characters.")
	}
	_, blocked := blockedPasswords()[password]
	_, lowerBlocked := blockedPasswords()[strings.ToLower(password)]
	if blocked || lowerBlocked || strings.EqualFold(password, username) {
		return errors.New("Choose a password that is not commonly used.")
	}
	return nil
}

func normalizeBillingUsername(username string) (string, bool) {
	username = strings.ToLower(strings.TrimSpace(username))
	if len(username) < 3 || len(username) > 64 {
		return "", false
	}
	for _, char := range username {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || strings.ContainsRune("._-@", char) {
			continue
		}
		return "", false
	}
	return username, true
}

func hashBillingPassword(password string) (string, error) {
	select {
	case authKDFSlots <- struct{}{}:
		defer func() { <-authKDFSlots }()
	default:
		return "", errAuthBusy
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	return fmt.Sprintf("$argon2id$v=19$m=19456,t=2,p=1$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyBillingPassword(password, hash string) (bool, error) {
	select {
	case authKDFSlots <- struct{}{}:
		defer func() { <-authKDFSlots }()
	default:
		return false, errAuthBusy
	}
	// Only the service's fixed Argon2id format is accepted. Legacy gateway
	// bcrypt hashes cannot introduce another password policy or KDF cost.
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		hash = dummyPasswordHash
	}
	return common.ValidatePasswordAndHash(password, hash), nil
}

func (s *Server) authTransaction(fn func(*gorm.DB) error) error {
	return s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&AuthGuard{ID: 1}).Error; err != nil {
			return err
		}
		if err := tx.Model(&AuthGuard{}).Where("id = ?", 1).UpdateColumn("version", gorm.Expr("version + ?", 1)).Error; err != nil {
			return err
		}
		return fn(tx)
	})
}

func newBillingSession(userID int64) (Session, string, error) {
	tokenBytes := make([]byte, 32)
	csrfBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return Session{}, "", err
	}
	if _, err := rand.Read(csrfBytes); err != nil {
		return Session{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	now := time.Now().Unix()
	return Session{TokenHash: hashSessionToken(token), UserID: userID, CSRFToken: base64.RawURLEncoding.EncodeToString(csrfBytes), CreatedAt: now, LastSeenAt: now, ExpiresAt: now + sessionMaxSeconds}, token, nil
}

func (s *Server) BootstrapManager(username, password string) error {
	var count int64
	if err := s.DB.Model(&User{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	username, ok := normalizeBillingUsername(username)
	if !ok {
		return errors.New("set a valid initial billing manager username")
	}
	if err := validateBillingPassword(password, username); err != nil {
		return err
	}
	hash, err := hashBillingPassword(password)
	if err != nil {
		return err
	}
	return s.authTransaction(func(tx *gorm.DB) error {
		if err := tx.Model(&User{}).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		return tx.Create(&User{Username: username, PasswordHash: hash, Role: RoleManager, CreatedAt: time.Now().Unix()}).Error
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != s.Origin || r.Header.Get("X-Requested-With") != "supplier-billing" {
		s.fail(w, http.StatusForbidden, "Request verification failed. Refresh and try again.")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	username, validUsername := normalizeBillingUsername(body.Username)
	if !validUsername {
		// Invalid usernames share a bucket but still execute the dummy KDF.
		username = "invalid-username"
	}
	if !s.allowAuthAttempt(w, r, username) {
		return
	}
	var user User
	err := s.DB.Where("username = ?", username).First(&user).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		s.fail(w, http.StatusServiceUnavailable, "Authentication is unavailable.")
		return
	}
	hash := dummyPasswordHash
	if err == nil {
		hash = user.PasswordHash
	}
	valid, err := verifyBillingPassword(body.Password, hash)
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, errAuthBusy.Error())
		return
	}
	if !valid || !validUsername || user.ID == 0 || user.Disabled || user.Role != RoleManager && user.Role != RoleSupplier {
		authAudit(r, "login_failed", 0, username)
		s.fail(w, http.StatusUnauthorized, "Invalid username or password.")
		return
	}
	session, token, err := newBillingSession(user.ID)
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, "Authentication is unavailable.")
		return
	}
	err = s.authTransaction(func(tx *gorm.DB) error {
		var unchanged User
		if err := tx.Where("id = ? AND password_hash = ? AND disabled = ?", user.ID, hash, false).First(&unchanged).Error; err != nil {
			return errAuthChanged
		}
		if unchanged.Role != user.Role || unchanged.SupplierID != user.SupplierID {
			return errAuthChanged
		}
		// Login replaces a presented billing session without touching sessions
		// belonging to any other service. Expired rows are safely pruned here.
		if cookie, err := r.Cookie(s.cookieName()); err == nil {
			if err := tx.Where("token_hash = ?", hashSessionToken(cookie.Value)).Delete(&Session{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("expires_at <= ? OR last_seen_at <= ?", session.CreatedAt, session.CreatedAt-sessionIdleSeconds).Delete(&Session{}).Error; err != nil {
			return err
		}
		// Keep at most five active sessions per account; a sixth login expires
		// the oldest. Credential changes and disabling revoke all of them.
		var retainedTokens []string
		if err := tx.Model(&Session{}).Where("user_id = ?", user.ID).Order("created_at DESC, token_hash DESC").Limit(4).Pluck("token_hash", &retainedTokens).Error; err != nil {
			return err
		}
		if len(retainedTokens) == 4 {
			if err := tx.Where("user_id = ? AND token_hash NOT IN ?", user.ID, retainedTokens).Delete(&Session{}).Error; err != nil {
				return err
			}
		}
		return tx.Create(&session).Error
	})
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, "Authentication is unavailable.")
		return
	}
	http.SetCookie(w, s.sessionCookie(token, session.ExpiresAt))
	authAudit(r, "login_succeeded", user.ID, username)
	s.write(w, http.StatusOK, map[string]any{"user": user, "csrf_token": session.CSRFToken})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	auth := r.Context().Value(authContextKey{}).(authenticatedRequest)
	s.write(w, http.StatusOK, map[string]any{"user": auth.User, "csrf_token": auth.Session.CSRFToken})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	auth := r.Context().Value(authContextKey{}).(authenticatedRequest)
	if err := s.DB.Where("token_hash = ?", auth.Session.TokenHash).Delete(&Session{}).Error; err != nil {
		s.fail(w, http.StatusServiceUnavailable, "Authentication is unavailable.")
		return
	}
	cookie := s.sessionCookie("", 1)
	cookie.MaxAge = -1
	http.SetCookie(w, cookie)
	authAudit(r, "logout", auth.User.ID, auth.User.Username)
	s.write(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *Server) reauthenticate(w http.ResponseWriter, r *http.Request, password string) bool {
	user := currentUser(r)
	if !s.allowAuthAttempt(w, r, user.Username) {
		return false
	}
	valid, err := verifyBillingPassword(password, user.PasswordHash)
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, errAuthBusy.Error())
		return false
	}
	if !valid {
		authAudit(r, "reauthentication_failed", user.ID, user.Username)
		s.fail(w, http.StatusForbidden, "Invalid username or password.")
		return false
	}
	previous := r.Context().Value(authContextKey{}).(authenticatedRequest)
	session, token, err := newBillingSession(user.ID)
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, "Authentication is unavailable.")
		return false
	}
	// Keep the secret CSRF value across this cookie rotation so account
	// management responses need not expose a second response envelope. The
	// previous session identifier itself is deleted and cannot be replayed.
	session.CSRFToken = previous.Session.CSRFToken
	err = s.authTransaction(func(tx *gorm.DB) error {
		var activeUser User
		if err := tx.Where("id = ? AND password_hash = ? AND disabled = ?", user.ID, user.PasswordHash, false).First(&activeUser).Error; err != nil {
			return errAuthChanged
		}
		removed := tx.Where("token_hash = ? AND expires_at > ? AND last_seen_at > ?", previous.Session.TokenHash, session.CreatedAt, session.CreatedAt-sessionIdleSeconds).Delete(&Session{})
		if removed.Error != nil {
			return removed.Error
		}
		if removed.RowsAffected != 1 {
			return errAuthChanged
		}
		return tx.Create(&session).Error
	})
	if err != nil {
		s.fail(w, http.StatusUnauthorized, "Account changed. Sign in again.")
		return false
	}
	http.SetCookie(w, s.sessionCookie(token, session.ExpiresAt))
	*r = *r.WithContext(context.WithValue(r.Context(), authContextKey{}, authenticatedRequest{User: user, Session: session}))
	return true
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !s.decode(w, r, &body) || !s.reauthenticate(w, r, body.CurrentPassword) {
		return
	}
	user := currentUser(r)
	if err := validateBillingPassword(body.NewPassword, user.Username); err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.NewPassword == body.CurrentPassword {
		s.fail(w, http.StatusBadRequest, "New password must be different from current password.")
		return
	}
	hash, err := hashBillingPassword(body.NewPassword)
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, errAuthBusy.Error())
		return
	}
	session, token, err := newBillingSession(user.ID)
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, "Authentication is unavailable.")
		return
	}
	err = s.authTransaction(func(tx *gorm.DB) error {
		result := tx.Model(&User{}).Where("id = ? AND password_hash = ? AND disabled = ?", user.ID, user.PasswordHash, false).UpdateColumn("password_hash", hash)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errAuthChanged
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&Session{}).Error; err != nil {
			return err
		}
		return tx.Create(&session).Error
	})
	if err != nil {
		s.fail(w, http.StatusConflict, "Account changed. Sign in again.")
		return
	}
	http.SetCookie(w, s.sessionCookie(token, session.ExpiresAt))
	authAudit(r, "password_changed", user.ID, user.Username)
	s.write(w, http.StatusOK, map[string]any{"user": user, "csrf_token": session.CSRFToken})
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users := []User{}
	if err := s.DB.Order("id ASC").Find(&users).Error; err != nil {
		s.fail(w, http.StatusServiceUnavailable, "Authentication is unavailable.")
		return
	}
	s.write(w, http.StatusOK, users)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username        string `json:"username"`
		Password        string `json:"password"`
		Role            string `json:"role"`
		SupplierID      int64  `json:"supplier_id"`
		CurrentPassword string `json:"current_password"`
	}
	if !s.decode(w, r, &body) || !s.reauthenticate(w, r, body.CurrentPassword) {
		return
	}
	username, ok := normalizeBillingUsername(body.Username)
	if !ok {
		s.fail(w, http.StatusBadRequest, "Username must contain 3 to 64 letters, numbers, dots, underscores, hyphens or @.")
		return
	}
	if body.Role != RoleManager && body.Role != RoleSupplier || body.Role == RoleManager && body.SupplierID != 0 || body.Role == RoleSupplier && body.SupplierID <= 0 {
		s.fail(w, http.StatusBadRequest, "Choose a valid role and supplier.")
		return
	}
	if err := validateBillingPassword(body.Password, username); err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Role == RoleSupplier {
		var supplier Supplier
		if err := s.DB.First(&supplier, body.SupplierID).Error; err != nil {
			s.fail(w, http.StatusBadRequest, "Choose a valid role and supplier.")
			return
		}
	}
	hash, err := hashBillingPassword(body.Password)
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, errAuthBusy.Error())
		return
	}
	user := User{Username: username, PasswordHash: hash, Role: body.Role, SupplierID: body.SupplierID, CreatedAt: time.Now().Unix()}
	caller := currentUser(r)
	err = s.authTransaction(func(tx *gorm.DB) error {
		var activeCaller User
		if err := tx.Where("id = ? AND role = ? AND password_hash = ? AND disabled = ?", caller.ID, RoleManager, caller.PasswordHash, false).First(&activeCaller).Error; err != nil {
			return errAuthChanged
		}
		return tx.Create(&user).Error
	})
	if err != nil {
		s.fail(w, http.StatusConflict, "Unable to create account. Check the username and try again.")
		return
	}
	authAudit(r, "user_created", user.ID, user.Username)
	s.write(w, http.StatusCreated, user)
}

func (s *Server) disableUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.fail(w, http.StatusBadRequest, "Invalid account.")
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
	}
	if !s.decode(w, r, &body) || !s.reauthenticate(w, r, body.CurrentPassword) {
		return
	}
	caller := currentUser(r)
	var target User
	err = s.authTransaction(func(tx *gorm.DB) error {
		var activeCaller User
		if err := tx.Where("id = ? AND role = ? AND password_hash = ? AND disabled = ?", caller.ID, RoleManager, caller.PasswordHash, false).First(&activeCaller).Error; err != nil {
			return errAuthChanged
		}
		if err := tx.First(&target, id).Error; err != nil {
			return err
		}
		if !target.Disabled && target.Role == RoleManager {
			var managers int64
			if err := tx.Model(&User{}).Where("role = ? AND disabled = ?", RoleManager, false).Count(&managers).Error; err != nil {
				return err
			}
			if managers <= 1 {
				return errLastManager
			}
		}
		if err := tx.Model(&target).UpdateColumn("disabled", true).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ?", target.ID).Delete(&Session{}).Error
	})
	if err != nil {
		if errors.Is(err, errLastManager) {
			s.fail(w, http.StatusConflict, err.Error())
			return
		}
		s.fail(w, http.StatusConflict, "Unable to disable account. Refresh and try again.")
		return
	}
	authAudit(r, "user_disabled", target.ID, target.Username)
	s.write(w, http.StatusOK, map[string]bool{"success": true})
}
