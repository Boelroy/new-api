package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

//go:embed frontend/dist
var frontendDist embed.FS

const quotaPerUnit = 500000.0

var db *sql.DB

// ---- Auth ----

var (
	adminUser      string
	adminPass      string
	jwtSecret      []byte
	mainServiceURL string
	mainServiceUID string
	ssoSecret      []byte
	reportAPIKey   string
	// profitEnabled controls whether the profit page and its API routes
	// are wired up at all. Off by default — set PROFIT_ENABLED=true to opt in.
	profitEnabled = false
)

// Role tiers mirror common.RoleCommonUser / RoleAdminUser / RoleRootUser in
// the main service. Routes are gated against these via requireRole.
//
// minTesterRole and minStudioOperatorRole are horizontal specializations,
// not tiers: they grant access to a narrow set of endpoints
// (Key Tester + Provider Testing for tester; batch channel creation scoped
// to their bound studio for studio operator) without inheriting admin
// permissions by virtue of being numerically above minUserRole.
const (
	minUserRole           = 1   // any authenticated main-service user
	minStudioOperatorRole = 2   // batch-create channels, scoped to bound studio
	minTesterRole         = 5   // Key Tester + Provider Testing only
	minAdminRole          = 10  // common.RoleAdminUser
	minSuperAdminRole     = 100 // common.RoleRootUser
)

// SSO session cache: maps session cookie value → (role, expiry).
type ssoCacheEntry struct {
	role int
	exp  time.Time
}

var (
	ssoCache   = map[string]ssoCacheEntry{}
	ssoCacheMu sync.Mutex
)

// checkMainServiceSession returns the user's role (>=1) and true when the
// supplied main-service session cookie is still valid; (0, false) otherwise.
func checkMainServiceSession(rawCookieHeader string) (int, bool) {
	if mainServiceURL == "" || rawCookieHeader == "" {
		return 0, false
	}

	ssoCacheMu.Lock()
	if entry, ok := ssoCache[rawCookieHeader]; ok && time.Now().Before(entry.exp) {
		ssoCacheMu.Unlock()
		return entry.role, true
	}
	ssoCacheMu.Unlock()

	req, err := http.NewRequest("GET", mainServiceURL+"/api/user/self", nil)
	if err != nil {
		log.Printf("[sso] build request error: %v", err)
		return 0, false
	}
	req.Header.Set("Cookie", rawCookieHeader)
	req.Header.Set("New-Api-User", mainServiceUID)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[sso] request error: %v", err)
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes := make([]byte, 200)
		n, _ := resp.Body.Read(bodyBytes)
		log.Printf("[sso] %d: %s", resp.StatusCode, bodyBytes[:n])
		return 0, false
	}

	var body struct {
		Data struct {
			Role int `json:"role"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		log.Printf("[sso] decode error: %v", err)
		return 0, false
	}
	log.Printf("[sso] user role=%d", body.Data.Role)
	if body.Data.Role < minUserRole {
		return 0, false
	}

	ssoCacheMu.Lock()
	ssoCache[rawCookieHeader] = ssoCacheEntry{role: body.Data.Role, exp: time.Now().Add(5 * time.Minute)}
	ssoCacheMu.Unlock()
	return body.Data.Role, true
}

// newJWT mints a local session token. user_id=0 marks SSO-issued tokens that
// have no rs_auth_user row; >0 corresponds to the DB-backed user. studio
// scopes a role=1 user's All Keys view (empty = unrestricted).
func newJWT(userID int64, username string, role int, studio string) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":      username,
		"user_id":  userID,
		"username": username,
		"role":     role,
		"studio":   studio,
		"exp":      time.Now().Add(24 * time.Hour).Unix(),
		"iat":      time.Now().Unix(),
	})
	return token.SignedString(jwtSecret)
}

func authMiddleware(c *gin.Context) {
	// User sessions (SSO cookie or local JWT) take strict priority over the
	// service-to-service X-API-Key. Reversing the order let a logged-in user
	// — whose browser auto-injects the api key for the /profit gate — be
	// silently promoted to super_admin and bypass the role gates.
	if rawCookie := c.GetHeader("Cookie"); rawCookie != "" && strings.Contains(rawCookie, "session=") {
		if role, ok := checkMainServiceSession(rawCookie); ok {
			c.Set("role", role)
			c.Next()
			return
		}
	}
	if tokenStr, err := c.Cookie("token"); err == nil {
		parsed, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method")
			}
			return jwtSecret, nil
		})
		if err == nil {
			// Older tokens predate the role claim: treat them as the embedded
			// admin (super admin) so existing sessions stay functional.
			role := minSuperAdminRole
			var userID int64
			var iat int64
			var username, studio string
			if claims, ok := parsed.Claims.(jwt.MapClaims); ok {
				if r, ok := claims["role"].(float64); ok {
					role = int(r)
				}
				if u, ok := claims["user_id"].(float64); ok {
					userID = int64(u)
				}
				if v, ok := claims["iat"].(float64); ok {
					iat = int64(v)
				}
				if u, ok := claims["username"].(string); ok {
					username = u
				}
				if s, ok := claims["studio"].(string); ok {
					studio = s
				}
			}
			// Local accounts (user_id > 0) get an additional live DB check:
			// status=0 or iat < disabled_at means the token is revoked, even
			// though it's still cryptographically valid. Tokens issued before
			// the process learned about disable stop working immediately.
			if userID > 0 {
				var status int
				var disabledAt int64
				dbErr := db.QueryRow(
					`SELECT status, disabled_at FROM rs_auth_user WHERE id=$1`,
					userID,
				).Scan(&status, &disabledAt)
				if dbErr == sql.ErrNoRows {
					c.JSON(http.StatusUnauthorized, gin.H{"error": "account no longer exists"})
					c.Abort()
					return
				}
				if dbErr != nil {
					log.Printf("[auth] status lookup for user %d failed: %v", userID, dbErr)
					c.JSON(http.StatusInternalServerError, gin.H{"error": "auth lookup failed"})
					c.Abort()
					return
				}
				if status == 0 {
					c.JSON(http.StatusUnauthorized, gin.H{"error": "account disabled"})
					c.Abort()
					return
				}
				if disabledAt > 0 && iat > 0 && iat < disabledAt {
					c.JSON(http.StatusUnauthorized, gin.H{"error": "token revoked (issued before account was disabled)"})
					c.Abort()
					return
				}
			}
			c.Set("role", role)
			if userID > 0 {
				c.Set("user_id", userID)
			}
			if username != "" {
				c.Set("username", username)
			}
			if studio != "" {
				c.Set("studio", studio)
			}
			c.Next()
			return
		}
	}
	// Service-to-service: X-API-Key fallback for cookie-less callers (cross
	// system sync). Granted super_admin since callers are trusted services.
	if reportAPIKey != "" {
		if k := c.GetHeader("X-API-Key"); k != "" && k == reportAPIKey {
			c.Set("role", minSuperAdminRole)
			c.Next()
			return
		}
	}
	log.Printf("[auth] unauthorized on %s %s", c.Request.Method, c.Request.URL.Path)
	c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
	c.Abort()
}

func requireRole(min int) gin.HandlerFunc {
	return func(c *gin.Context) {
		roleAny, _ := c.Get("role")
		role, _ := roleAny.(int)
		if role < min {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// requireRoleOrTester grants access to callers at min tier OR the tester
// role. Tester (role=5) is a horizontal specialization and does not inherit
// admin permissions via tier compare, so we special-case it for the two
// testing-related surface areas (Key Tester + Provider Testing).
func requireRoleOrTester(min int) gin.HandlerFunc {
	return func(c *gin.Context) {
		roleAny, _ := c.Get("role")
		role, _ := roleAny.(int)
		if role >= min || role == minTesterRole {
			c.Next()
			return
		}
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		c.Abort()
	}
}

// requireRoleOrStudioOperator grants access to callers at min tier OR the
// studio-operator role. Studio operator (role=2) is a horizontal
// specialization: they can batch-create channels but only into the studio
// bound to their account. handleBatchCreateChannels enforces the studio
// lock; this middleware just opens the endpoint to them.
func requireRoleOrStudioOperator(min int) gin.HandlerFunc {
	return func(c *gin.Context) {
		roleAny, _ := c.Get("role")
		role, _ := roleAny.(int)
		if role >= min || role == minStudioOperatorRole {
			c.Next()
			return
		}
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		c.Abort()
	}
}

func handleAuthMe(c *gin.Context) {
	role, _ := c.Get("role")
	resp := gin.H{"role": role}
	if uid, ok := c.Get("user_id"); ok {
		resp["user_id"] = uid
	}
	if uname, ok := c.Get("username"); ok {
		resp["username"] = uname
	}
	if studio, ok := c.Get("studio"); ok {
		resp["studio"] = studio
	} else {
		resp["studio"] = ""
	}
	c.JSON(http.StatusOK, resp)
}

// ---- Local user store (rs_auth_user) ----

type authUser struct {
	ID         int64  `json:"id"`
	Username   string `json:"username"`
	Role       int    `json:"role"`
	Studio     string `json:"studio"`
	Status     int    `json:"status"`
	DisabledAt int64  `json:"disabled_at"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

// authUserFromRow scans the user-facing columns (no password hash). Used by
// list/get endpoints that must never echo the hash back to clients.
func authUserFromRow(row interface{ Scan(...any) error }) (authUser, error) {
	var u authUser
	err := row.Scan(&u.ID, &u.Username, &u.Role, &u.Studio, &u.Status, &u.DisabledAt, &u.CreatedAt, &u.UpdatedAt)
	return u, err
}

func authUserByUsername(username string) (id int64, hash string, role int, studio string, status int, err error) {
	err = db.QueryRow(
		`SELECT id, password_hash, role, studio, status FROM rs_auth_user WHERE username=$1`,
		username,
	).Scan(&id, &hash, &role, &studio, &status)
	return
}

func authUserByID(id int64) (authUser, error) {
	row := db.QueryRow(
		`SELECT id, username, role, studio, status, disabled_at, created_at, updated_at FROM rs_auth_user WHERE id=$1`,
		id,
	)
	return authUserFromRow(row)
}

func countAuthUsers() (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM rs_auth_user`).Scan(&n)
	return n, err
}

// seedAdminUser is invoked once after table creation. It promotes the
// compose-supplied ADMIN_USERNAME/ADMIN_PASSWORD into a real DB row so the
// first deploy has a working super-admin login. Subsequent deploys leave the
// table untouched — admins manage users from the UI.
func seedAdminUser() {
	if adminUser == "" || adminPass == "" {
		return
	}
	n, err := countAuthUsers()
	if err != nil {
		log.Printf("[auth-seed] count error: %v", err)
		return
	}
	if n > 0 {
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(adminPass), bcrypt.DefaultCost)
	if err != nil {
		log.Printf("[auth-seed] hash error: %v", err)
		return
	}
	now := time.Now().Unix()
	if _, err := db.Exec(
		`INSERT INTO rs_auth_user (username, password_hash, role, studio, created_at, updated_at)
		 VALUES ($1, $2, $3, '', $4, $4)`,
		adminUser, string(hash), minSuperAdminRole, now,
	); err != nil {
		log.Printf("[auth-seed] insert error: %v", err)
		return
	}
	log.Printf("[auth-seed] seeded super-admin user %q from ADMIN_USERNAME env", adminUser)
}

// ---- Lark Notification ----

var (
	larkWebhook          string
	notifyHoursThreshold float64
	notifyUSDThreshold   float64
	notifyMu             sync.Mutex
	lastNotified         = map[string]time.Time{}

	// Single-flight guard for the manual aggregate-today refresh so a
	// double click (or multiple operators) can't race aggregateDay against
	// itself.
	refreshMu      sync.Mutex
	refreshRunning bool
)

func canNotify(key string) bool {
	notifyMu.Lock()
	defer notifyMu.Unlock()
	if t, ok := lastNotified[key]; ok && time.Since(t) < time.Hour {
		return false
	}
	lastNotified[key] = time.Now()
	return true
}

func sendLark(msg string) {
	if larkWebhook == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": msg},
	})
	resp, err := http.Post(larkWebhook, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("lark notify error: %v", err)
		return
	}
	resp.Body.Close()
}

func fmtHours(h float64) string {
	if h >= 24*30 {
		return ">30天"
	}
	if h >= 24 {
		return fmt.Sprintf("%d天%d小时", int(h/24), int(h)%24)
	}
	return fmt.Sprintf("%.1f小时", h)
}

func checkAndNotify() {
	if larkWebhook == "" {
		return
	}
	channels, err := queryKeyData()
	if err != nil {
		log.Printf("checkAndNotify query error: %v", err)
		return
	}

	// Only consider channels that have an explicit quota configured. Mixing
	// usage from unmetered channels with the configured-quota sum produced
	// nonsensical negative remainders in the notification text.
	var totalUsed, totalQuota float64
	hasQuota := false
	for _, ch := range channels {
		if ch.QuotaUSD == nil {
			continue
		}
		totalUsed += ch.UsedUSD
		totalQuota += *ch.QuotaUSD
		hasQuota = true
	}
	if !hasQuota {
		return
	}

	// Burn rate covers ALL channels — including unquota'd / disabled ones —
	// so the eta matches what users see in the "最近1小时消耗" card on the
	// Key Capacity page (which uses queryTotalLastHour). Pessimistic-but-
	// consistent: real Anthropic-side spend is included even when a key
	// hasn't been mapped to a quota row yet.
	totalLastHour, err := queryTotalLastHour()
	if err != nil {
		log.Printf("checkAndNotify last-hour error: %v", err)
		return
	}

	totalRemaining := totalQuota - totalUsed
	var etaHours float64
	hasETA := totalLastHour > 0
	if hasETA {
		etaHours = totalRemaining / totalLastHour
	}

	if notifyHoursThreshold > 0 && hasETA && etaHours < notifyHoursThreshold {
		if canNotify("hours") {
			sendLark(fmt.Sprintf(
				"⚠️ Key 余量预警\n剩余额度：$%.2f / $%.2f\n最近1小时消耗：$%.4f\n预计剩余时长：%s（低于阈值 %.0f 小时）",
				totalRemaining, totalQuota, totalLastHour, fmtHours(etaHours), notifyHoursThreshold,
			))
		}
	}

	if notifyUSDThreshold > 0 && totalRemaining < notifyUSDThreshold {
		if canNotify("usd") {
			sendLark(fmt.Sprintf(
				"🚨 Key 余额不足\n剩余额度：$%.2f（低于阈值 $%.2f）\n最近1小时消耗：$%.4f\n预计剩余时长：%s",
				totalRemaining, notifyUSDThreshold, totalLastHour,
				func() string {
					if hasETA {
						return fmtHours(etaHours)
					}
					return "未知"
				}(),
			))
		}
	}
}

func startNotifyLoop() {
	if larkWebhook == "" {
		return
	}
	ticker := time.NewTicker(10 * time.Minute)
	go func() {
		for range ticker.C {
			checkAndNotify()
		}
	}()
}

// computeNotifyState mirrors the calculation in checkAndNotify but returns the
// state without sending. Used by /api/notify/status for diagnosis.
type notifyState struct {
	ChannelsWithQuota int                  `json:"channels_with_quota"`
	TotalQuotaUSD     float64              `json:"total_quota_usd"`
	TotalUsedUSD      float64              `json:"total_used_usd"`
	TotalRemainingUSD float64              `json:"total_remaining_usd"`
	TotalLastHourUSD  float64              `json:"total_last_hour_usd"`
	ETAHours          float64              `json:"eta_hours"`
	LarkConfigured    bool                 `json:"lark_configured"`
	Thresholds        map[string]float64   `json:"thresholds"`
	WouldAlert        map[string]bool      `json:"would_alert"`
	LastNotified      map[string]time.Time `json:"last_notified"`
}

func snapshotNotify() notifyState {
	st := notifyState{
		LarkConfigured: larkWebhook != "",
		Thresholds: map[string]float64{
			"hours": notifyHoursThreshold,
			"usd":   notifyUSDThreshold,
		},
		WouldAlert:   map[string]bool{},
		LastNotified: map[string]time.Time{},
	}
	channels, err := queryKeyData()
	if err != nil {
		return st
	}
	for _, ch := range channels {
		if ch.QuotaUSD == nil {
			continue
		}
		st.ChannelsWithQuota++
		st.TotalUsedUSD += ch.UsedUSD
		st.TotalQuotaUSD += *ch.QuotaUSD
	}
	st.TotalRemainingUSD = st.TotalQuotaUSD - st.TotalUsedUSD
	// Burn rate sourced globally so it matches the UI's 最近1小时消耗 card.
	if lh, err := queryTotalLastHour(); err == nil {
		st.TotalLastHourUSD = lh
	}
	if st.TotalLastHourUSD > 0 {
		st.ETAHours = st.TotalRemainingUSD / st.TotalLastHourUSD
		st.WouldAlert["hours"] = notifyHoursThreshold > 0 && st.ETAHours < notifyHoursThreshold
	}
	st.WouldAlert["usd"] = notifyUSDThreshold > 0 && st.TotalRemainingUSD < notifyUSDThreshold

	notifyMu.Lock()
	for k, v := range lastNotified {
		st.LastNotified[k] = v
	}
	notifyMu.Unlock()
	return st
}

func handleNotifyStatus(c *gin.Context) {
	c.JSON(http.StatusOK, snapshotNotify())
}

// handleNotifyCheck runs the standard alert check (still respects canNotify).
func handleNotifyCheck(c *gin.Context) {
	go checkAndNotify()
	c.JSON(http.StatusOK, gin.H{"ok": true, "triggered": true})
}

// handleNotifyTest fires a synthetic test alert to Lark, bypassing thresholds
// and suppression so operators can verify wiring end-to-end.
func handleNotifyTest(c *gin.Context) {
	if larkWebhook == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "LARK_WEBHOOK not configured"})
		return
	}
	sendLark("🔧 Lark 通道测试 from report-service @ " + time.Now().Format("2006-01-02 15:04:05"))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- Daily Cache ----

type DailyRow struct {
	Hour             string  `json:"hour"`
	UserID           int     `json:"user_id"`
	Username         string  `json:"username"`
	TokenID          int     `json:"token_id"`
	TokenName        string  `json:"token_name"`
	ChannelID        int     `json:"channel_id"`
	ChannelName      string  `json:"channel_name"`
	Group            string  `json:"group"`
	Model            string  `json:"model"`
	RequestCount     int     `json:"request_count"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	InputCost        float64 `json:"input_cost"`
	OutputCost       float64 `json:"output_cost"`
	CacheReadCost    float64 `json:"cache_read_cost"`
	CacheWriteCost   float64 `json:"cache_write_cost"`
	TotalCost        float64 `json:"total_cost"`
}

// aggKey matches the DB PRIMARY KEY exactly to avoid ON CONFLICT drops
type aggKey struct {
	hour                       string
	userID, tokenID, channelID int
	model                      string
}

// aggregateHour processes a single UTC hour and merges into aggMap.
// Splitting by hour avoids loading 100k+ rows in a single query
// which caused silent iteration failures with large daily volumes.
func aggregateHour(startTS, endTS int64, aggMap map[aggKey]*DailyRow) error {
	query := `
SELECT
  l.created_at, l.user_id, COALESCE(l.username,''), l.token_id, COALESCE(l.token_name,''),
  l.channel_id, COALESCE(c.name,'') as channel_name, COALESCE(l."group",''), l.model_name,
  l.prompt_tokens, l.completion_tokens, l.quota, COALESCE(l.other, '{}') as other_json
FROM logs l
LEFT JOIN channels c ON l.channel_id = c.id
WHERE l.type = 2 AND l.created_at >= $1 AND l.created_at < $2`

	rows, err := db.Query(query, startTS, endTS)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var createdAt int64
		var userID, tokenID, channelID int
		var username, tokenName, channelName, groupName, modelName, otherJSON string
		var promptTokens, completionTokens, quota int64

		if err := rows.Scan(&createdAt, &userID, &username, &tokenID, &tokenName,
			&channelID, &channelName, &groupName, &modelName, &promptTokens, &completionTokens, &quota, &otherJSON); err != nil {
			return err
		}

		t := time.Unix(createdAt, 0).UTC()
		hour := t.Format("2006-01-02 15:00")

		var other map[string]interface{}
		json.Unmarshal([]byte(otherJSON), &other)

		cacheRead := getIntFromOther(other, "cache_tokens")
		cacheWrite := getIntFromOther(other, "cache_creation_tokens")
		modelRatio := getFloatFromOther(other, "model_ratio")
		completionRatio := getFloatFromOther(other, "completion_ratio")
		cacheRatio := getFloatFromOther(other, "cache_ratio")
		cacheCreationRatio := getFloatFromOther(other, "cache_creation_ratio")
		groupRatio := getFloatFromOther(other, "group_ratio")
		if groupRatio == 0 {
			groupRatio = 1
		}

		totalCost := float64(quota) / quotaPerUnit
		var inputCost, outputCost, cacheReadCost, cacheWriteCost float64
		if modelRatio > 0 {
			pricePerInputToken := modelRatio * groupRatio * 2 / 1000000
			inputCost = float64(promptTokens) * pricePerInputToken
			outputCost = float64(completionTokens) * pricePerInputToken * completionRatio
			cacheReadCost = float64(cacheRead) * pricePerInputToken * cacheRatio
			cacheWriteCost = float64(cacheWrite) * pricePerInputToken * cacheCreationRatio
		} else {
			inputCost = totalCost
		}

		k := aggKey{hour, userID, tokenID, channelID, modelName}
		row, ok := aggMap[k]
		if !ok {
			row = &DailyRow{
				Hour: hour, UserID: userID, Username: username,
				TokenID: tokenID, TokenName: tokenName,
				ChannelID: channelID, ChannelName: channelName,
				Group: groupName, Model: modelName,
			}
			aggMap[k] = row
		} else if row.Group == "" && groupName != "" {
			row.Group = groupName
		}
		row.RequestCount++
		row.InputTokens += promptTokens
		row.OutputTokens += completionTokens
		row.CacheReadTokens += cacheRead
		row.CacheWriteTokens += cacheWrite
		row.TotalTokens += promptTokens + completionTokens + cacheRead + cacheWrite
		row.InputCost += inputCost
		row.OutputCost += outputCost
		row.CacheReadCost += cacheReadCost
		row.CacheWriteCost += cacheWriteCost
		row.TotalCost += totalCost
	}
	// MUST check rows.Err() — silent iteration failures otherwise drop data
	return rows.Err()
}

func aggregateDay(dateStr string) error {
	loc := time.UTC
	day, err := time.ParseInLocation("2006-01-02", dateStr, loc)
	if err != nil {
		return err
	}

	aggMap := make(map[aggKey]*DailyRow)

	// Process each UTC hour separately to keep per-query row counts bounded
	for h := 0; h < 24; h++ {
		hourStart := day.Add(time.Duration(h) * time.Hour).Unix()
		hourEnd := hourStart + 3600
		if err := aggregateHour(hourStart, hourEnd, aggMap); err != nil {
			return fmt.Errorf("hour %d: %w", h, err)
		}
	}

	// Replace existing rows for this date atomically
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err = tx.Exec(`DELETE FROM report_daily_agg WHERE date = $1`, dateStr); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`
		INSERT INTO report_daily_agg
		(date, hour, user_id, username, token_id, token_name, channel_id, channel_name, "group", model,
		 request_count, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		 total_tokens, input_cost, output_cost, cache_read_cost, cache_write_cost, total_cost)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, row := range aggMap {
		if _, err = stmt.Exec(
			dateStr, row.Hour, row.UserID, row.Username, row.TokenID, row.TokenName,
			row.ChannelID, row.ChannelName, row.Group, row.Model,
			row.RequestCount, row.InputTokens, row.OutputTokens,
			row.CacheReadTokens, row.CacheWriteTokens, row.TotalTokens,
			roundTo(row.InputCost, 6), roundTo(row.OutputCost, 6),
			roundTo(row.CacheReadCost, 6), roundTo(row.CacheWriteCost, 6),
			roundTo(row.TotalCost, 6),
		); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("aggregated %s: %d rows", dateStr, len(aggMap))
	return nil
}

func backfillMissingDays() {
	// Find all UTC dates in logs (past 90 days) not yet in cache
	cutoff := time.Now().UTC().AddDate(0, 0, -90).Unix()
	rows, err := db.Query(`
		SELECT DISTINCT to_char(to_timestamp(created_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD') as d
		FROM logs WHERE type=2 AND created_at >= $1
		ORDER BY d`, cutoff)
	if err != nil {
		log.Printf("backfill query error: %v", err)
		return
	}
	defer rows.Close()

	var dates []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err == nil {
			dates = append(dates, d)
		}
	}

	today := time.Now().UTC().Format("2006-01-02")
	for _, d := range dates {
		if d == today {
			continue // today is handled by hourly refresh
		}
		var count int
		db.QueryRow(`SELECT COUNT(*) FROM report_daily_agg WHERE date=$1`, d).Scan(&count)
		if count == 0 {
			log.Printf("backfilling %s...", d)
			if err := aggregateDay(d); err != nil {
				log.Printf("backfill %s error: %v", d, err)
			}
		}
	}

	// one-time retrofill: re-aggregate recent days that have no group column populated
	// (covers data written before the `group` column existed)
	retroCutoff := time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02")
	retroRows, err := db.Query(`
		SELECT date FROM report_daily_agg
		WHERE date >= $1
		GROUP BY date
		HAVING MAX("group") = ''
		ORDER BY date`, retroCutoff)
	if err == nil {
		var retroDates []string
		for retroRows.Next() {
			var d string
			if err := retroRows.Scan(&d); err == nil {
				retroDates = append(retroDates, d)
			}
		}
		retroRows.Close()
		for _, d := range retroDates {
			if d == today {
				continue
			}
			log.Printf("retrofilling group for %s...", d)
			if err := aggregateDay(d); err != nil {
				log.Printf("retrofill %s error: %v", d, err)
			}
		}
	}

	// Always refresh today
	log.Printf("refreshing today (%s)...", today)
	if err := aggregateDay(today); err != nil {
		log.Printf("refresh today error: %v", err)
	}
}

func startDailyRefresh() {
	go backfillMissingDays()
	ticker := time.NewTicker(time.Hour)
	go func() {
		for range ticker.C {
			today := time.Now().UTC().Format("2006-01-02")
			if err := aggregateDay(today); err != nil {
				log.Printf("daily refresh error: %v", err)
			}
		}
	}()
}

// ---- Key data ----

type ChannelRow struct {
	ID           int      `json:"id"`
	Name         string   `json:"name"`
	Key          string   `json:"key"`
	Status       int      `json:"status"`
	Type         int      `json:"type"`
	Tag          string   `json:"tag"`
	Priority     int      `json:"priority"`
	UsedUSD      float64  `json:"used_usd"`
	LastHourUSD  float64  `json:"last_hour_usd"`
	// Rpm mirrors newapi's usage-log RPM (count of type=2 rows in the last
	// 60s). Populated per-channel by queryAllKeys so the client can sum
	// them into a system-wide real-time total. queryKeyData leaves it 0.
	Rpm          int      `json:"rpm"`
	QuotaUSD     *float64 `json:"quota_usd"`
	UnitPriceCNY *float64 `json:"unit_price_cny"`
	Note         string   `json:"note"`
}

type KeySummary struct {
	Channels      []ChannelRow `json:"channels"`
	TotalLastHour float64      `json:"total_last_hour"`
}

func queryKeyData() ([]ChannelRow, error) {
	rows, err := db.Query(`
		SELECT c.id, COALESCE(c.name,''), c.key, COALESCE(c.status,1), COALESCE(c.type,0), COALESCE(c.tag,''),
		       COALESCE(c.priority,0),
		       COALESCE(c.used_quota,0), q.quota_usd, q.unit_price_cny, COALESCE(q.note,'')
		FROM channels c
		LEFT JOIN report_key_quotas q ON q.channel_id = c.id
		WHERE c.status = 1
		ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	channels := make([]ChannelRow, 0)
	idxMap := make(map[int]int)
	for rows.Next() {
		var r ChannelRow
		var usedQuota int64
		var quotaUSD, unitPrice sql.NullFloat64
		if err := rows.Scan(&r.ID, &r.Name, &r.Key, &r.Status, &r.Type, &r.Tag, &r.Priority, &usedQuota, &quotaUSD, &unitPrice, &r.Note); err != nil {
			return nil, err
		}
		r.UsedUSD = roundTo(float64(usedQuota)/quotaPerUnit, 4)
		if quotaUSD.Valid {
			v := roundTo(quotaUSD.Float64, 4)
			r.QuotaUSD = &v
		}
		if unitPrice.Valid {
			v := roundTo(unitPrice.Float64, 4)
			r.UnitPriceCNY = &v
		}
		if len(r.Key) > 8 {
			r.Key = "…" + r.Key[len(r.Key)-8:]
		}
		idxMap[r.ID] = len(channels)
		channels = append(channels, r)
	}

	now := time.Now().Unix()
	oneHourAgo := now - 3600
	lhRows, err := db.Query(`SELECT channel_id, COALESCE(SUM(quota),0) FROM logs WHERE type=2 AND created_at>=$1 AND created_at<$2 GROUP BY channel_id`, oneHourAgo, now)
	if err != nil {
		return nil, err
	}
	defer lhRows.Close()
	for lhRows.Next() {
		var chID int
		var q int64
		if err := lhRows.Scan(&chID, &q); err != nil {
			return nil, err
		}
		if idx, ok := idxMap[chID]; ok {
			channels[idx].LastHourUSD = roundTo(float64(q)/quotaPerUnit, 6)
		}
	}
	return channels, nil
}

func queryTotalLastHour() (float64, error) {
	now := time.Now().Unix()
	oneHourAgo := now - 3600
	var total int64
	err := db.QueryRow(`SELECT COALESCE(SUM(quota),0) FROM logs WHERE type=2 AND created_at>=$1 AND created_at<$2`, oneHourAgo, now).Scan(&total)
	if err != nil {
		return 0, err
	}
	return roundTo(float64(total)/quotaPerUnit, 6), nil
}

func queryAllKeys(startTS, endTS int64, studio string) ([]ChannelRow, error) {
	query := `
		SELECT c.id, COALESCE(c.name,''), c.key, COALESCE(c.status,1), COALESCE(c.type,0), COALESCE(c.tag,''),
		       COALESCE(c.priority,0),
		       COALESCE(c.used_quota,0), q.quota_usd, q.unit_price_cny, COALESCE(q.note,'')
		FROM channels c
		LEFT JOIN report_key_quotas q ON q.channel_id = c.id`
	args := []any{}
	conds := []string{}
	if startTS > 0 {
		args = append(args, startTS)
		conds = append(conds, fmt.Sprintf("c.created_time >= $%d", len(args)))
	}
	if endTS > 0 {
		args = append(args, endTS)
		conds = append(conds, fmt.Sprintf("c.created_time < $%d", len(args)))
	}
	if studio != "" {
		args = append(args, studio)
		conds = append(conds, fmt.Sprintf("c.tag = $%d", len(args)))
	}
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += ` ORDER BY c.id`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	channels := make([]ChannelRow, 0)
	idxMap := make(map[int]int)
	for rows.Next() {
		var r ChannelRow
		var usedQuota int64
		var quotaUSD, unitPrice sql.NullFloat64
		if err := rows.Scan(&r.ID, &r.Name, &r.Key, &r.Status, &r.Type, &r.Tag, &r.Priority, &usedQuota, &quotaUSD, &unitPrice, &r.Note); err != nil {
			return nil, err
		}
		r.UsedUSD = roundTo(float64(usedQuota)/quotaPerUnit, 4)
		if quotaUSD.Valid {
			v := roundTo(quotaUSD.Float64, 4)
			r.QuotaUSD = &v
		}
		if unitPrice.Valid {
			v := roundTo(unitPrice.Float64, 4)
			r.UnitPriceCNY = &v
		}
		if len(r.Key) > 8 {
			r.Key = "…" + r.Key[len(r.Key)-8:]
		}
		idxMap[r.ID] = len(channels)
		channels = append(channels, r)
	}

	// Real-time RPM: count of type=2 rows in the last 60s, grouped by
	// channel_id. Frontend sums these into a system-wide RPM. Same window
	// newapi's usage-log page uses.
	rpmSince := time.Now().Add(-60 * time.Second).Unix()
	rpmRows, err := db.Query(`SELECT channel_id, COUNT(*) FROM logs WHERE type=2 AND created_at >= $1 GROUP BY channel_id`, rpmSince)
	if err != nil {
		return nil, err
	}
	defer rpmRows.Close()
	for rpmRows.Next() {
		var chID, cnt int
		if err := rpmRows.Scan(&chID, &cnt); err != nil {
			return nil, err
		}
		if idx, ok := idxMap[chID]; ok {
			channels[idx].Rpm = cnt
		}
	}
	return channels, nil
}

// ---- Helpers ----

func getIntFromOther(m map[string]interface{}, key string) int64 {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return int64(val)
	case json.Number:
		n, _ := val.Int64()
		return n
	}
	return 0
}

func getFloatFromOther(m map[string]interface{}, key string) float64 {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return val
	case json.Number:
		f, _ := val.Float64()
		return f
	}
	return 0
}

func roundTo(f float64, places int) float64 {
	pow := math.Pow(10, float64(places))
	return math.Round(f*pow) / pow
}

// ---- Handlers ----

func handleSSOCallback(c *gin.Context) {
	tokenStr := c.Query("sso_token")
	if tokenStr == "" || len(ssoSecret) == 0 {
		log.Printf("[sso-callback] rejected: token=%v secret_set=%v", tokenStr != "", len(ssoSecret) > 0)
		c.Redirect(http.StatusFound, "/login?error=sso_failed")
		return
	}
	parsed, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return ssoSecret, nil
	})
	if err != nil || !parsed.Valid {
		log.Printf("[sso-callback] token validation failed: %v", err)
		c.Redirect(http.StatusFound, "/login?error=sso_failed")
		return
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		c.Redirect(http.StatusFound, "/login?error=sso_failed")
		return
	}
	if iss, _ := claims["iss"].(string); iss != "new-api" {
		log.Printf("[sso-callback] unexpected issuer")
		c.Redirect(http.StatusFound, "/login?error=sso_failed")
		return
	}
	roleRaw, ok := claims["role"].(float64)
	if !ok {
		log.Printf("[sso-callback] missing or invalid role claim")
		c.Redirect(http.StatusFound, "/login?error=sso_failed")
		return
	}
	if int(roleRaw) < minUserRole {
		c.Redirect(http.StatusFound, "/login?error=sso_failed")
		return
	}
	ssoUsername, _ := claims["sub"].(string)
	localToken, err := newJWT(0, ssoUsername, int(roleRaw), "")
	if err != nil {
		c.Redirect(http.StatusFound, "/login?error=sso_failed")
		return
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("token", localToken, 86400, "/", "", false, true)
	c.Redirect(http.StatusFound, "/")
}

func handleAuthConfig(c *gin.Context) {
	resp := gin.H{
		"profit_enabled":    profitEnabled,
		"grader_configured": graderConfigured(),
		"r2_configured":     r2Configured(),
	}
	if mainServiceURL != "" {
		resp["sso_url"] = mainServiceURL + "/sign-in"
	} else {
		resp["sso_url"] = nil
	}
	c.JSON(http.StatusOK, resp)
}

// Brute-force defenses on /api/login. Tunable in one place: 5 wrong passwords
// against a username in 15 minutes locks the account, and 10 wrong attempts
// from the same IP in 5 minutes throttle further tries regardless of which
// username was targeted. Both windows are computed against rs_login_attempt
// which startPruneLoginAttempts trims down to ~24h.
const (
	loginLockoutWindowSec   = 15 * 60
	loginLockoutMaxFails    = 5
	loginIPRateWindowSec    = 5 * 60
	loginIPRateMaxFails     = 10
	loginAttemptRetentionSec = 24 * 60 * 60
)

// clientIPForLogin prefers Cloudflare's Cf-Connecting-Ip (always set when
// requests come through the tunnel) and falls back to Gin's RemoteIP. We
// avoid trusting X-Forwarded-For directly because it's spoofable on the
// hop between the cloudflared sidecar and us.
func clientIPForLogin(c *gin.Context) string {
	if ip := c.GetHeader("Cf-Connecting-Ip"); ip != "" {
		return ip
	}
	return c.ClientIP()
}

func recordLoginAttempt(username, ip string, ok bool) {
	if _, err := db.Exec(
		`INSERT INTO rs_login_attempt (username, ip, succeeded, attempted_at)
		 VALUES ($1, $2, $3, $4)`,
		username, ip, ok, time.Now().Unix(),
	); err != nil {
		log.Printf("[login] record attempt error: %v", err)
	}
}

func countRecentFailures(column, value string, sinceSec int64) (int, error) {
	var n int
	err := db.QueryRow(
		fmt.Sprintf(`SELECT COUNT(*) FROM rs_login_attempt
		             WHERE %s=$1 AND succeeded=false AND attempted_at >= $2`, column),
		value, sinceSec,
	).Scan(&n)
	return n, err
}

func startPruneLoginAttempts() {
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			cutoff := time.Now().Unix() - loginAttemptRetentionSec
			if _, err := db.Exec(
				`DELETE FROM rs_login_attempt WHERE attempted_at < $1`, cutoff,
			); err != nil {
				log.Printf("[login] prune error: %v", err)
			}
			<-t.C
		}
	}()
}

func handleLogin(c *gin.Context) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	ip := clientIPForLogin(c)
	now := time.Now().Unix()

	// IP rate limit first — protects against credential stuffing across many
	// usernames from a single attacker.
	if n, err := countRecentFailures("ip", ip, now-loginIPRateWindowSec); err == nil && n >= loginIPRateMaxFails {
		log.Printf("[login] ip throttled ip=%s recent_fails=%d", ip, n)
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": "too many failed attempts from this address, try again later",
		})
		return
	}
	// Per-username lockout — protects a single high-value account from a
	// targeted dictionary attack even when the attacker rotates IPs.
	if n, err := countRecentFailures("username", body.Username, now-loginLockoutWindowSec); err == nil && n >= loginLockoutMaxFails {
		log.Printf("[login] account locked user=%s recent_fails=%d ip=%s", body.Username, n, ip)
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": "account temporarily locked, try again later",
		})
		return
	}

	id, hash, role, studio, status, err := authUserByUsername(body.Username)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Password)) != nil {
		recordLoginAttempt(body.Username, ip, false)
		log.Printf("[login] failed user=%s ip=%s", body.Username, ip)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	if status == 0 {
		recordLoginAttempt(body.Username, ip, false)
		log.Printf("[login] disabled account user=%s id=%d ip=%s", body.Username, id, ip)
		c.JSON(http.StatusForbidden, gin.H{"error": "account disabled"})
		return
	}
	tokenStr, err := newJWT(id, body.Username, role, studio)
	if err != nil {
		recordLoginAttempt(body.Username, ip, false)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token generation failed"})
		return
	}
	recordLoginAttempt(body.Username, ip, true)
	c.SetCookie("token", tokenStr, 86400, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"ok": true, "username": body.Username, "role": role, "studio": studio})
}

func handleLogout(c *gin.Context) {
	c.SetCookie("token", "", -1, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- User management (super_admin only; mounted under superAPI) ----

func isValidRoleTier(role int) bool {
	switch role {
	case minUserRole, minStudioOperatorRole, minTesterRole, minAdminRole, minSuperAdminRole:
		return true
	}
	return false
}

func handleUsersList(c *gin.Context) {
	rows, err := db.Query(
		`SELECT id, username, role, studio, status, disabled_at, created_at, updated_at
		   FROM rs_auth_user ORDER BY id ASC`,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := make([]authUser, 0)
	for rows.Next() {
		u, err := authUserFromRow(rows)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out = append(out, u)
	}
	c.JSON(http.StatusOK, gin.H{"users": out})
}

func handleUserCreate(c *gin.Context) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     int    `json:"role"`
		Studio   string `json:"studio"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	body.Studio = strings.TrimSpace(body.Studio)
	if body.Username == "" || body.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username and password are required"})
		return
	}
	if !isValidRoleTier(body.Role) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "role must be 1, 2, 5, 10, or 100"})
		return
	}
	// Anti-escalation on create: an admin (non-super) can only mint accounts
	// at a strictly lower tier. Super admin can create any tier.
	callerRoleAny, _ := c.Get("role")
	callerRole, _ := callerRoleAny.(int)
	if callerRole < minSuperAdminRole && body.Role >= callerRole {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot create a user at your own tier or higher"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash error"})
		return
	}
	now := time.Now().Unix()
	var id int64
	err = db.QueryRow(
		`INSERT INTO rs_auth_user (username, password_hash, role, studio, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $5) RETURNING id`,
		body.Username, string(hash), body.Role, body.Studio, now,
	).Scan(&id)
	if err != nil {
		// Unique violation surfaces as a 409 with the bare DB string so the
		// frontend can show "username taken" without needing extra plumbing.
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			c.JSON(http.StatusConflict, gin.H{"error": "username already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	u, err := authUserByID(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, u)
}

func handleUserUpdate(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	target, err := authUserByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	var body struct {
		Password *string `json:"password,omitempty"`
		Role     *int    `json:"role,omitempty"`
		Studio   *string `json:"studio,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	// Guard rails: a super admin cannot demote themselves and lock everyone
	// out, and cannot demote the last remaining super admin via this handler.
	if body.Role != nil {
		if !isValidRoleTier(*body.Role) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "role must be 1, 2, 5, 10, or 100"})
			return
		}
		if target.Role >= minSuperAdminRole && *body.Role < minSuperAdminRole {
			var others int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM rs_auth_user WHERE role >= $1 AND id <> $2`,
				minSuperAdminRole, id,
			).Scan(&others); err == nil && others == 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "cannot demote the last super admin"})
				return
			}
		}
		if _, err := db.Exec(
			`UPDATE rs_auth_user SET role=$1, updated_at=$2 WHERE id=$3`,
			*body.Role, time.Now().Unix(), id,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.Password != nil && *body.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(*body.Password), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "hash error"})
			return
		}
		if _, err := db.Exec(
			`UPDATE rs_auth_user SET password_hash=$1, updated_at=$2 WHERE id=$3`,
			string(hash), time.Now().Unix(), id,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.Studio != nil {
		if _, err := db.Exec(
			`UPDATE rs_auth_user SET studio=$1, updated_at=$2 WHERE id=$3`,
			strings.TrimSpace(*body.Studio), time.Now().Unix(), id,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	u, err := authUserByID(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, u)
}

// handleStudiosList returns distinct studio names known to the system —
// the union of channels.tag (where the studio is "in use") and
// rs_auth_user.studio (where a user is bound but no channel exists yet).
// Pre-binding users before keys arrive is a common flow, so this prevents
// the operator-set studio from disappearing from the dropdown.
func handleStudiosList(c *gin.Context) {
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

// handleUserResetPassword lets admin+ callers reset any user's password
// without granting them the full user-management surface. Anti-escalation
// guard: caller cannot reset the password of a user at equal-or-higher
// tier (super admin bypasses the check and can reset anyone including
// themselves).
func handleUserResetPassword(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	callerRoleAny, _ := c.Get("role")
	callerRole, _ := callerRoleAny.(int)

	var targetRole int
	if err := db.QueryRow(`SELECT role FROM rs_auth_user WHERE id=$1`, id).Scan(&targetRole); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	if callerRole < minSuperAdminRole && callerRole <= targetRole {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot reset password of a peer or higher-privileged user"})
		return
	}

	var body struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if len(body.Password) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "password must be at least 6 characters"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash error"})
		return
	}
	now := time.Now().Unix()
	if _, err := db.Exec(
		`UPDATE rs_auth_user SET password_hash=$1, updated_at=$2 WHERE id=$3`,
		string(hash), now, id,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// callerCanManage returns whether the calling admin+ is authorised to
// mutate `target`. Super admin can mutate anyone (including themselves,
// with the exception of "last-of-tier" guards elsewhere). Non-super
// callers must be strictly higher tier than the target, so admin cannot
// touch peer admins or super admins.
func callerCanManage(c *gin.Context, target authUser) (int, bool) {
	roleAny, _ := c.Get("role")
	callerRole, _ := roleAny.(int)
	if callerRole >= minSuperAdminRole {
		return callerRole, true
	}
	if callerRole > target.Role {
		return callerRole, true
	}
	return callerRole, false
}

// handleUserSetStatus toggles rs_auth_user.status. status=0 also stamps
// disabled_at=now so any JWT issued before this moment is rejected on
// the next request (see authMiddleware). Re-enabling flips status back
// to 1 but leaves disabled_at as-is — the user must log in again to get
// a fresh token whose iat >= disabled_at.
func handleUserSetStatus(c *gin.Context, disable bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	target, err := authUserByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if _, ok := callerCanManage(c, target); !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot disable a peer or higher-privileged user"})
		return
	}
	// Never let anyone disable the only remaining active super admin — that
	// would lock everyone out of user management + Remote Channels.
	if disable && target.Role >= minSuperAdminRole {
		var others int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM rs_auth_user WHERE role >= $1 AND status = 1 AND id <> $2`,
			minSuperAdminRole, id,
		).Scan(&others); err == nil && others == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot disable the last active super admin"})
			return
		}
	}
	now := time.Now().Unix()
	if disable {
		if _, err := db.Exec(
			`UPDATE rs_auth_user SET status=0, disabled_at=$1, updated_at=$1 WHERE id=$2`,
			now, id,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	} else {
		if _, err := db.Exec(
			`UPDATE rs_auth_user SET status=1, updated_at=$1 WHERE id=$2`,
			now, id,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": map[bool]int{true: 0, false: 1}[disable]})
}

func handleUserDisable(c *gin.Context) { handleUserSetStatus(c, true) }
func handleUserEnable(c *gin.Context)  { handleUserSetStatus(c, false) }

func handleUserDelete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	// Forbid deleting self — too easy to footgun yourself out of the only
	// active super-admin slot.
	if uidAny, ok := c.Get("user_id"); ok {
		if uid, ok := uidAny.(int64); ok && uid == id {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot delete the currently logged-in user"})
			return
		}
	}
	target, err := authUserByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	// Anti-escalation: admin (role < super) can only delete strict-lower
	// tier users. Super admin can delete anyone (subject to the last-super
	// guard below).
	if _, ok := callerCanManage(c, target); !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot delete a peer or higher-privileged user"})
		return
	}
	if target.Role >= minSuperAdminRole {
		var others int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM rs_auth_user WHERE role >= $1 AND id <> $2`,
			minSuperAdminRole, id,
		).Scan(&others); err == nil && others == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot delete the last super admin"})
			return
		}
	}
	if _, err := db.Exec(`DELETE FROM rs_auth_user WHERE id=$1`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleRefresh re-aggregates today's logs on demand and returns when done.
// When pipi is configured (System 1), it ALSO fires System 2's /api/refresh
// in parallel and then pulls today's pipi rollup so /profit sees fresh
// numbers end-to-end. Concurrent calls return 409 immediately.
func handleRefresh(c *gin.Context) {
	refreshMu.Lock()
	if refreshRunning {
		refreshMu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "refresh already running", "running": true})
		return
	}
	refreshRunning = true
	refreshMu.Unlock()

	defer func() {
		refreshMu.Lock()
		refreshRunning = false
		refreshMu.Unlock()
	}()

	today := time.Now().UTC().Format("2006-01-02")
	start := time.Now()

	pipiConfigured := pipiReportURL != "" && pipiReportAPIKey != ""

	var wg sync.WaitGroup
	var localErr, remoteErr error
	var localElapsed, remoteElapsed int64

	wg.Add(1)
	go func() {
		defer wg.Done()
		s := time.Now()
		localErr = aggregateDay(today)
		localElapsed = time.Since(s).Milliseconds()
	}()
	if pipiConfigured {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := time.Now()
			remoteErr = pipiRefreshRemote()
			remoteElapsed = time.Since(s).Milliseconds()
		}()
	}
	wg.Wait()

	if localErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "local: " + localErr.Error()})
		return
	}

	// Pipi sync after remote refresh so we pull the freshly aggregated rows.
	var syncErr error
	var syncElapsed int64
	if pipiConfigured {
		s := time.Now()
		syncErr = syncPipiOnce(today, today)
		syncElapsed = time.Since(s).Milliseconds()
	}

	resp := gin.H{
		"ok":               true,
		"date":             today,
		"elapsed_ms":       time.Since(start).Milliseconds(),
		"local_elapsed_ms": localElapsed,
	}
	if pipiConfigured {
		resp["pipi_refresh_elapsed_ms"] = remoteElapsed
		if remoteErr != nil {
			resp["pipi_refresh_error"] = remoteErr.Error()
		}
		resp["pipi_sync_elapsed_ms"] = syncElapsed
		if syncErr != nil {
			resp["pipi_sync_error"] = syncErr.Error()
		}
	}
	c.JSON(http.StatusOK, resp)
}

func handleRefreshStatus(c *gin.Context) {
	refreshMu.Lock()
	running := refreshRunning
	refreshMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"running": running})
}

func handleReport(c *gin.Context) {
	startDate := c.DefaultQuery("start", time.Now().UTC().AddDate(0, 0, -6).Format("2006-01-02"))
	endDate := c.DefaultQuery("end", time.Now().UTC().Format("2006-01-02"))

	rows, err := db.Query(`
		SELECT hour, user_id, username, token_id, token_name, channel_id, channel_name, COALESCE("group",''), model,
		       request_count, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		       total_tokens, input_cost, output_cost, cache_read_cost, cache_write_cost, total_cost
		FROM report_daily_agg
		WHERE date >= $1 AND date <= $2
		ORDER BY hour, model`, startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	result := make([]DailyRow, 0)
	for rows.Next() {
		var r DailyRow
		if err := rows.Scan(&r.Hour, &r.UserID, &r.Username, &r.TokenID, &r.TokenName,
			&r.ChannelID, &r.ChannelName, &r.Group, &r.Model, &r.RequestCount,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens,
			&r.TotalTokens, &r.InputCost, &r.OutputCost, &r.CacheReadCost, &r.CacheWriteCost, &r.TotalCost); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		result = append(result, r)
	}
	c.JSON(http.StatusOK, result)
}

func handleExportCSV(c *gin.Context) {
	startDate := c.DefaultQuery("start", time.Now().UTC().AddDate(0, 0, -6).Format("2006-01-02"))
	endDate := c.DefaultQuery("end", time.Now().UTC().Format("2006-01-02"))

	rows, err := db.Query(`
		SELECT hour, user_id, username, token_id, token_name, channel_id, channel_name, COALESCE("group",''), model,
		       request_count, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		       total_tokens, input_cost, output_cost, cache_read_cost, cache_write_cost, total_cost
		FROM report_daily_agg WHERE date >= $1 AND date <= $2 ORDER BY hour, model`, startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	filename := fmt.Sprintf("report_%s_to_%s.csv", startDate, endDate)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	c.Writer.Write([]byte("\xEF\xBB\xBF"))

	w := csv.NewWriter(c.Writer)
	w.Write([]string{"Hour", "User ID", "Username", "Token ID", "Token Name",
		"Channel ID", "Channel Name", "Group", "Model",
		"Requests", "Input Tokens", "Output Tokens",
		"Cache Read Tokens", "Cache Write Tokens", "Total Tokens",
		"Input Cost", "Output Cost", "Cache Read Cost", "Cache Write Cost", "Total Cost"})
	for rows.Next() {
		var r DailyRow
		rows.Scan(&r.Hour, &r.UserID, &r.Username, &r.TokenID, &r.TokenName,
			&r.ChannelID, &r.ChannelName, &r.Group, &r.Model, &r.RequestCount,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens,
			&r.TotalTokens, &r.InputCost, &r.OutputCost, &r.CacheReadCost, &r.CacheWriteCost, &r.TotalCost)
		w.Write([]string{
			r.Hour, strconv.Itoa(r.UserID), r.Username,
			strconv.Itoa(r.TokenID), r.TokenName,
			strconv.Itoa(r.ChannelID), r.ChannelName, r.Group, r.Model,
			strconv.Itoa(r.RequestCount),
			strconv.FormatInt(r.InputTokens, 10), strconv.FormatInt(r.OutputTokens, 10),
			strconv.FormatInt(r.CacheReadTokens, 10), strconv.FormatInt(r.CacheWriteTokens, 10),
			strconv.FormatInt(r.TotalTokens, 10),
			fmt.Sprintf("%.6f", r.InputCost), fmt.Sprintf("%.6f", r.OutputCost),
			fmt.Sprintf("%.6f", r.CacheReadCost), fmt.Sprintf("%.6f", r.CacheWriteCost),
			fmt.Sprintf("%.6f", r.TotalCost),
		})
	}
	w.Flush()
}

func handleKeysData(c *gin.Context) {
	channels, err := queryKeyData()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	totalLastHour, err := queryTotalLastHour()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, KeySummary{Channels: channels, TotalLastHour: totalLastHour})
}

func handleSaveQuotas(c *gin.Context) {
	var payload []struct {
		Key      string  `json:"key"`
		QuotaUSD float64 `json:"quota_usd"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	now := time.Now().Unix()
	saved := 0
	for _, p := range payload {
		var channelID int
		err := db.QueryRow(`SELECT id FROM channels WHERE key = $1 LIMIT 1`, p.Key).Scan(&channelID)
		if err != nil {
			continue
		}
		_, err = db.Exec(`
			INSERT INTO report_key_quotas (channel_id, quota_usd, updated_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (channel_id) DO UPDATE SET quota_usd=$2, updated_at=$3`,
			channelID, p.QuotaUSD, now)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		saved++
	}
	c.JSON(http.StatusOK, gin.H{"saved": saved})
}

// Default model list for new Anthropic channels. Used as the fallback when
// the admin hasn't overridden it via report_config('batch_create_default_models').
// Keep this list in sync with the set of Claude models the org currently sells
// so a fresh deployment gets sensible defaults on batch create.
var defaultAnthropicModels = strings.Join([]string{
	"claude-opus-4-7",
	"claude-sonnet-4-6",
	"claude-opus-4-6",
	"claude-haiku-4-5-20251001",
	"claude-sonnet-4-5-20250929",
	"claude-opus-4-5-20251101",
	"claude-opus-4-8",
	"claude-fable-5",
	"claude-sonnet-5",
}, ",")

// getBatchCreateModels reads the runtime-configurable model list. Admins can
// edit it from the Key Capacity page to add / remove models without a redeploy.
// Empty rows fall back to defaultAnthropicModels.
func getBatchCreateModels() string {
	var v string
	err := db.QueryRow(`SELECT value FROM report_config WHERE key='batch_create_default_models'`).Scan(&v)
	if err != nil || strings.TrimSpace(v) == "" {
		return defaultAnthropicModels
	}
	// Preserve original ordering but strip any stray whitespace between commas.
	parts := strings.Split(v, ",")
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			cleaned = append(cleaned, p)
		}
	}
	if len(cleaned) == 0 {
		return defaultAnthropicModels
	}
	return strings.Join(cleaned, ",")
}

func handleGetBatchModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"models": getBatchCreateModels()})
}

func handleSetBatchModels(c *gin.Context) {
	var body struct {
		Models string `json:"models"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	// Normalize: collapse whitespace/comma/newline separators, drop empties.
	fields := strings.FieldsFunc(body.Models, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	})
	if len(fields) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one model is required"})
		return
	}
	joined := strings.Join(fields, ",")
	now := time.Now().Unix()
	if _, err := db.Exec(
		`INSERT INTO report_config (key, value, updated_at)
		 VALUES ('batch_create_default_models', $1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`,
		joined, now,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"models": joined})
}

const channelInfoDefault = `{"is_multi_key":false,"multi_key_size":0,"multi_key_status_list":null,"multi_key_polling_index":0,"multi_key_mode":""}`

func handleBatchCreateChannels(c *gin.Context) {
	var payload struct {
		Studio   string `json:"studio"`
		Suffix   string `json:"suffix"`
		// Default priority + unit price applied to every channel that does not
		// override them in the per-row entry. Lets the form set a single value
		// up top instead of repeating it on every key.
		Priority     int     `json:"priority"`
		UnitPriceCNY float64 `json:"unit_price_cny"`
		Channels     []struct {
			Key          string   `json:"key"`
			QuotaUSD     float64  `json:"quota_usd"`
			Priority     *int     `json:"priority,omitempty"`
			UnitPriceCNY *float64 `json:"unit_price_cny,omitempty"`
		} `json:"channels"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	studio := strings.TrimSpace(payload.Studio)
	suffix := strings.TrimSpace(payload.Suffix)
	// Studio Operator (role=2) is locked to their bound studio: ignore the
	// payload and use the JWT claim. Admin+ retain the ability to pick any
	// studio (including creating a new one) via the payload.
	roleAny, _ := c.Get("role")
	if role, _ := roleAny.(int); role == minStudioOperatorRole {
		userStudioAny, _ := c.Get("studio")
		userStudio, _ := userStudioAny.(string)
		userStudio = strings.TrimSpace(userStudio)
		if userStudio == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "your account has no studio binding; ask an admin to bind one before uploading keys"})
			return
		}
		studio = userStudio
	}
	// Backward compat: older admin clients only sent `suffix`. Keep the
	// legacy `pipi` literal so previously-named batches stay consistent.
	if studio == "" {
		studio = "pipi"
	}
	if suffix == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "suffix is required"})
		return
	}
	if len(payload.Channels) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no channels provided"})
		return
	}
	// Legacy default for channels.priority. Mirrors the value used in upstream
	// new-api so failover behavior stays consistent for batches that don't
	// override it.
	const defaultChannelPriority = 1001
	defaultPriority := payload.Priority
	if defaultPriority <= 0 {
		defaultPriority = defaultChannelPriority
	}

	dateStr := time.Now().UTC().Format("0102")
	activeModels := getBatchCreateModels()
	models := strings.Split(activeModels, ",")
	now := time.Now().Unix()

	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()

	type created struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	results := make([]created, 0, len(payload.Channels))

	for _, ch := range payload.Channels {
		key := strings.TrimSpace(ch.Key)
		if key == "" || ch.QuotaUSD <= 0 {
			continue
		}
		quotaInt := int(ch.QuotaUSD)
		name := fmt.Sprintf("%s-%s-%s-%d", dateStr, studio, suffix, quotaInt)
		priority := defaultPriority
		if ch.Priority != nil && *ch.Priority > 0 {
			priority = *ch.Priority
		}

		var channelID int
		// channels.tag = studio so rs_auth_user.studio bindings can filter
		// All Keys by the same identifier the operator picked here.
		err := tx.QueryRow(`
			INSERT INTO channels
			(type, key, status, name, weight, created_time, base_url, "group", models,
			 model_mapping, status_code_mapping, priority, auto_ban, used_quota, channel_info, tag)
			VALUES (14, $1, 1, $2, 0, $3, '', 'default', $4,
			        '', '', $7, 1, 0, $5::json, $6)
			RETURNING id`,
			key, name, now, activeModels, channelInfoDefault, studio, priority).Scan(&channelID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("insert channel: %v", err)})
			return
		}

		for _, m := range models {
			_, err = tx.Exec(`
				INSERT INTO abilities ("group", model, channel_id, enabled, priority, weight)
				VALUES ('default', $1, $2, true, $3, 0)
				ON CONFLICT DO NOTHING`,
				strings.TrimSpace(m), channelID, priority)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("insert ability: %v", err)})
				return
			}
		}

		// Persist quota + optional unit_price_cny in a single upsert so the
		// All Keys page picks them up without a second round trip.
		unitPrice := payload.UnitPriceCNY
		if ch.UnitPriceCNY != nil {
			unitPrice = *ch.UnitPriceCNY
		}
		if unitPrice > 0 {
			_, err = tx.Exec(`
				INSERT INTO report_key_quotas (channel_id, quota_usd, unit_price_cny, updated_at)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (channel_id)
				DO UPDATE SET quota_usd=$2, unit_price_cny=$3, updated_at=$4`,
				channelID, ch.QuotaUSD, unitPrice, now)
		} else {
			_, err = tx.Exec(`
				INSERT INTO report_key_quotas (channel_id, quota_usd, updated_at)
				VALUES ($1, $2, $3)
				ON CONFLICT (channel_id) DO UPDATE SET quota_usd=$2, updated_at=$3`,
				channelID, ch.QuotaUSD, now)
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("insert quota: %v", err)})
			return
		}

		results = append(results, created{ID: channelID, Name: name})
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"created": results, "count": len(results)})
}

// ---- Cache stats report ----

type cacheStatsBucket struct {
	Bucket             string  `json:"bucket"`
	Requests           int64   `json:"requests"`
	PromptTokens       int64   `json:"prompt_tokens"`
	CacheReadTokens    int64   `json:"cache_read_tokens"`
	CacheWriteTokens   int64   `json:"cache_write_tokens"`
	CompletionTokens   int64   `json:"completion_tokens"`
	HitPct             float64 `json:"hit_pct"`
	ReuseX             float64 `json:"reuse_x"`
}

// handleCacheStats returns time-bucketed Anthropic cache metrics for the
// admin dashboard. Reads from report_daily_agg (hourly pre-aggregate produced
// by startDailyRefresh + manual /api/refresh) — logs directly would be
// ~450× slower and JSON-parse `other` on every row.
//
// bucket=hour | day; start/end are inclusive UTC dates. model filter is a
// prefix; default "claude" matches claude-* rows. Pass "all" to disable.
// Latency to fresh data: up to 1 hour (aggregation runs on the hour). Users
// can trigger /api/refresh to catch up today immediately.
func handleCacheStats(c *gin.Context) {
	bucketMode := strings.ToLower(strings.TrimSpace(c.DefaultQuery("bucket", "hour")))
	if bucketMode != "hour" && bucketMode != "day" {
		bucketMode = "hour"
	}
	// Default: last 24h (hour bucket) or last 14d (day bucket).
	nowUTC := time.Now().UTC()
	defaultStart := nowUTC.AddDate(0, 0, -1)
	if bucketMode == "day" {
		defaultStart = nowUTC.AddDate(0, 0, -13)
	}
	startDate := defaultStart.Format("2006-01-02")
	endDate := nowUTC.Format("2006-01-02")
	if s := c.Query("start"); s != "" {
		if _, err := time.ParseInLocation("2006-01-02", s, time.UTC); err == nil {
			startDate = s
		}
	}
	if e := c.Query("end"); e != "" {
		if _, err := time.ParseInLocation("2006-01-02", e, time.UTC); err == nil {
			endDate = e
		}
	}
	if endDate < startDate {
		c.JSON(http.StatusBadRequest, gin.H{"error": "end must be >= start"})
		return
	}

	modelFilter := strings.TrimSpace(c.DefaultQuery("model", "claude"))

	// bucket column shape:
	//   hour → "2026-07-01 14:00" (matches report_daily_agg.hour verbatim)
	//   day  → "2026-07-01"
	var bucketExpr string
	if bucketMode == "day" {
		bucketExpr = `date`
	} else {
		// hour column already looks like "YYYY-MM-DD HH:mm"; fall back to
		// date-only when hour was empty (legacy pre-migration rows).
		bucketExpr = `CASE WHEN hour <> '' THEN hour ELSE date END`
	}

	query := fmt.Sprintf(`
		SELECT %s AS bucket,
		       COALESCE(SUM(request_count),0)::bigint      AS requests,
		       COALESCE(SUM(input_tokens),0)::bigint       AS prompt_tokens,
		       COALESCE(SUM(output_tokens),0)::bigint      AS completion_tokens,
		       COALESCE(SUM(cache_read_tokens),0)::bigint  AS cache_read_tokens,
		       COALESCE(SUM(cache_write_tokens),0)::bigint AS cache_write_tokens
		  FROM report_daily_agg
		 WHERE date >= $1 AND date <= $2
		   AND ($3 = 'all' OR model LIKE $3 || '-%%')
		 GROUP BY 1 ORDER BY 1`, bucketExpr)

	rows, err := db.Query(query, startDate, endDate, modelFilter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	buckets := make([]cacheStatsBucket, 0)
	var totRequests, totInput, totCacheRead, totCacheWrite, totCompletion int64
	for rows.Next() {
		var b cacheStatsBucket
		if err := rows.Scan(&b.Bucket, &b.Requests, &b.PromptTokens, &b.CompletionTokens, &b.CacheReadTokens, &b.CacheWriteTokens); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if b.PromptTokens+b.CacheReadTokens > 0 {
			b.HitPct = roundTo(100.0*float64(b.CacheReadTokens)/float64(b.PromptTokens+b.CacheReadTokens), 2)
		}
		if b.CacheWriteTokens > 0 {
			b.ReuseX = roundTo(float64(b.CacheReadTokens)/float64(b.CacheWriteTokens), 2)
		}
		buckets = append(buckets, b)
		totRequests += b.Requests
		totInput += b.PromptTokens
		totCacheRead += b.CacheReadTokens
		totCacheWrite += b.CacheWriteTokens
		totCompletion += b.CompletionTokens
	}
	summary := gin.H{
		"requests":            totRequests,
		"prompt_tokens":       totInput,
		"completion_tokens":   totCompletion,
		"cache_read_tokens":   totCacheRead,
		"cache_write_tokens":  totCacheWrite,
		"hit_pct":             0.0,
		"reuse_x":             0.0,
	}
	if totInput+totCacheRead > 0 {
		summary["hit_pct"] = roundTo(100.0*float64(totCacheRead)/float64(totInput+totCacheRead), 2)
	}
	if totCacheWrite > 0 {
		summary["reuse_x"] = roundTo(float64(totCacheRead)/float64(totCacheWrite), 2)
	}
	c.JSON(http.StatusOK, gin.H{
		"buckets": buckets,
		"summary": summary,
		"range":   gin.H{"start": startDate, "end": endDate, "bucket": bucketMode, "model": modelFilter},
	})
}

// handleBatchUpdateChannelPriority sets channels.priority and the matching
// abilities.priority for a list of channel ids in a single transaction. The
// pair must stay in sync — abilities.priority controls per-model selection
// weight on relay dispatch — so updating one without the other reintroduces
// the failover bug we saw when ability rows kept the legacy 1001 default.
func handleBatchUpdateChannelPriority(c *gin.Context) {
	var payload struct {
		ChannelIDs []int `json:"channel_ids"`
		Priority   int   `json:"priority"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(payload.ChannelIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_ids is required"})
		return
	}
	if payload.Priority <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "priority must be > 0"})
		return
	}
	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()
	// lib/pq needs pq.Array to marshal a Go slice into a Postgres int[] for the
	// ANY($n) operator; passing a bare []int surfaces as
	// "unsupported type []int, a slice of int".
	ids := pq.Array(payload.ChannelIDs)
	res, err := tx.Exec(`UPDATE channels SET priority=$1 WHERE id = ANY($2)`, payload.Priority, ids)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("update channels: %v", err)})
		return
	}
	updated, _ := res.RowsAffected()
	if _, err := tx.Exec(`UPDATE abilities SET priority=$1 WHERE channel_id = ANY($2)`, payload.Priority, ids); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("update abilities: %v", err)})
		return
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": updated, "priority": payload.Priority})
}

// ---- Key tester ----

var supportedTestModels = map[string]bool{
	"claude-sonnet-5":            true,
	"claude-opus-4-8":            true,
	"claude-opus-4-7":            true,
	"claude-sonnet-4-6":          true,
	"claude-opus-4-6":            true,
	"claude-haiku-4-5-20251001":  true,
	"claude-sonnet-4-5-20250929": true,
	"claude-opus-4-5-20251101":   true,
	"claude-fable-5":             true,
}

const anthropicTestEndpoint = "https://api.anthropic.com/v1/messages"

type keyTestResult struct {
	Key       string `json:"key"`
	OK        bool   `json:"ok"`
	Status    int    `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
	Message   string `json:"message,omitempty"`
}

func testSingleKey(key, model string) keyTestResult {
	res := keyTestResult{Key: key}
	body := map[string]any{
		"model":      model,
		"max_tokens": 1,
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
	}
	buf, _ := json.Marshal(body)

	start := time.Now()
	req, err := http.NewRequest("POST", anthropicTestEndpoint, bytes.NewReader(buf))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	res.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()
	res.Status = resp.StatusCode

	buf2 := make([]byte, 2048)
	n, _ := resp.Body.Read(buf2)
	payload := string(buf2[:n])

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		res.OK = true
		res.Message = "OK"
		return res
	}

	// Extract a friendlier message from common Anthropic error shape.
	var errEnv struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &errEnv); err == nil && errEnv.Error.Message != "" {
		res.Error = errEnv.Error.Message
		if errEnv.Error.Type != "" {
			res.Message = errEnv.Error.Type
		}
	} else {
		// Truncate raw body for readability.
		if len(payload) > 200 {
			payload = payload[:200] + "…"
		}
		res.Error = payload
	}
	return res
}

func handleTestKeys(c *gin.Context) {
	var payload struct {
		Keys  []string `json:"keys"`
		Model string   `json:"model"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	model := strings.TrimSpace(payload.Model)
	if !supportedTestModels[model] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported model"})
		return
	}

	// Dedup + cleanup
	seen := map[string]bool{}
	keys := make([]string, 0, len(payload.Keys))
	for _, k := range payload.Keys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no keys provided"})
		return
	}
	const maxKeys = 200
	if len(keys) > maxKeys {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("too many keys (max %d)", maxKeys)})
		return
	}

	results := make([]keyTestResult, len(keys))
	for i, k := range keys {
		results[i] = testSingleKey(k, model)
	}

	c.JSON(http.StatusOK, gin.H{"results": results})
}

func handleAllKeysData(c *gin.Context) {
	var startTS, endTS int64
	if s := c.Query("start"); s != "" {
		if t, err := time.ParseInLocation("2006-01-02", s, time.UTC); err == nil {
			startTS = t.Unix()
		}
	}
	if e := c.Query("end"); e != "" {
		if t, err := time.ParseInLocation("2006-01-02", e, time.UTC); err == nil {
			endTS = t.AddDate(0, 0, 1).Unix()
		}
	}
	// role=1 (regular user) is scoped to their studio. An unbound user-tier
	// account sees nothing — operators have to explicitly bind a studio in
	// the Users page before any channel becomes visible. Admin+ ignore the
	// studio entirely.
	studioFilter := ""
	roleAny, _ := c.Get("role")
	if role, _ := roleAny.(int); role > 0 && role < minAdminRole {
		studioAny, _ := c.Get("studio")
		studio, _ := studioAny.(string)
		if studio == "" {
			c.JSON(http.StatusOK, []ChannelRow{})
			return
		}
		studioFilter = studio
	}
	channels, err := queryAllKeys(startTS, endTS, studioFilter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, channels)
}

// ---- SPA static file serving ----

func spaHandler() gin.HandlerFunc {
	distFS, err := fs.Sub(frontendDist, "frontend/dist")
	if err != nil {
		log.Fatalf("failed to sub frontend/dist: %v", err)
	}
	fileServer := http.FileServer(http.FS(distFS))
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		// Unknown /api/* routes (e.g. disabled features) must surface as 404,
		// not the SPA shell — otherwise gated endpoints return 200 with HTML.
		if strings.HasPrefix(path, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		// Try to serve exact file; fall back to index.html for SPA routing
		f, err := distFS.Open(strings.TrimPrefix(path, "/"))
		if err == nil {
			f.Close()
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		// Serve index.html for all unknown paths (client-side routing)
		c.Request.URL.Path = "/"
		fileServer.ServeHTTP(c.Writer, c.Request)
	}
}

// ---- Report export as HTML (standalone) ----

func handleExportHTML(c *gin.Context) {
	startDate := c.DefaultQuery("start", time.Now().UTC().AddDate(0, 0, -6).Format("2006-01-02"))
	endDate := c.DefaultQuery("end", time.Now().UTC().Format("2006-01-02"))

	rows, err := db.Query(`
		SELECT hour, user_id, username, token_id, token_name, channel_id, channel_name, COALESCE("group",''), model,
		       request_count, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		       total_tokens, input_cost, output_cost, cache_read_cost, cache_write_cost, total_cost
		FROM report_daily_agg WHERE date >= $1 AND date <= $2 ORDER BY hour, model`, startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	result := make([]DailyRow, 0)
	for rows.Next() {
		var r DailyRow
		rows.Scan(&r.Hour, &r.UserID, &r.Username, &r.TokenID, &r.TokenName,
			&r.ChannelID, &r.ChannelName, &r.Group, &r.Model, &r.RequestCount,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens,
			&r.TotalTokens, &r.InputCost, &r.OutputCost, &r.CacheReadCost, &r.CacheWriteCost, &r.TotalCost)
		result = append(result, r)
	}

	// Sort result
	sort.Slice(result, func(i, j int) bool {
		if result[i].Hour != result[j].Hour {
			return result[i].Hour < result[j].Hour
		}
		return result[i].Model < result[j].Model
	})

	dataJSON, _ := json.Marshal(result)
	filename := fmt.Sprintf("report_%s_to_%s.html", startDate, endDate)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	// Minimal standalone HTML with embedded data
	c.Writer.WriteString(fmt.Sprintf(`<!DOCTYPE html><html><head><meta charset="UTF-8"><title>Report %s ~ %s</title>
<script>var DATA=%s;</script>
<script>
window.onload=function(){
  var total=0,tokens=0,reqs=0;
  DATA.forEach(function(r){total+=r.total_cost;tokens+=r.total_tokens;reqs+=r.request_count;});
  document.getElementById('summary').textContent='Total Cost: $'+total.toFixed(2)+' | Tokens: '+tokens.toLocaleString()+' | Requests: '+reqs.toLocaleString();
  var tb=document.getElementById('tb');
  DATA.forEach(function(r){
    tb.innerHTML+='<tr><td>'+r.hour+'</td><td>'+r.token_name+'</td><td>'+r.model+'</td><td>'+r.request_count+'</td><td>'+r.total_tokens.toLocaleString()+'</td><td>$'+r.total_cost.toFixed(4)+'</td></tr>';
  });
};
</script>
<style>body{font-family:system-ui;padding:24px;max-width:1200px}table{width:100%%;border-collapse:collapse;font-size:12px}th,td{padding:6px 10px;text-align:left;border-bottom:1px solid #eee}th{background:#f5f5f5}</style>
</head><body>
<h2>Report %s ~ %s (UTC)</h2>
<p id="summary"></p>
<table><thead><tr><th>Hour</th><th>Token</th><th>Model</th><th>Requests</th><th>Tokens</th><th>Cost</th></tr></thead>
<tbody id="tb"></tbody></table>
</body></html>`, startDate, endDate, string(dataJSON), startDate, endDate))
}

// ---- Main ----

func main() {
	dsn := os.Getenv("SQL_DSN")
	if dsn == "" {
		log.Fatal("SQL_DSN environment variable is required")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}

	adminUser = os.Getenv("ADMIN_USERNAME")
	if adminUser == "" {
		adminUser = "admin"
	}
	adminPass = os.Getenv("ADMIN_PASSWORD")
	if adminPass == "" {
		log.Fatal("ADMIN_PASSWORD environment variable is required")
	}

	if secret := os.Getenv("JWT_SECRET"); secret != "" {
		jwtSecret = []byte(secret)
	} else {
		jwtSecret = make([]byte, 32)
		rand.Read(jwtSecret)
		log.Println("JWT_SECRET not set, using random secret (sessions reset on restart)")
	}

	mainServiceURL = strings.TrimRight(os.Getenv("MAIN_SERVICE_URL"), "/")
	mainServiceUID = os.Getenv("MAIN_SERVICE_USER_ID")
	if mainServiceUID == "" {
		mainServiceUID = "1"
	}
	if s := os.Getenv("SSO_SECRET"); s != "" {
		ssoSecret = []byte(s)
	}
	reportAPIKey = os.Getenv("REPORT_API_KEY")
	if v := os.Getenv("PROFIT_ENABLED"); v != "" {
		switch strings.ToLower(v) {
		case "true", "1", "yes", "on":
			profitEnabled = true
		}
	}
	pipiReportURL = os.Getenv("PIPI_REPORT_URL")
	pipiReportAPIKey = os.Getenv("PIPI_REPORT_API_KEY")
	larkWebhook = os.Getenv("LARK_WEBHOOK")
	if v := os.Getenv("NOTIFY_HOURS_THRESHOLD"); v != "" {
		notifyHoursThreshold, _ = strconv.ParseFloat(v, 64)
	}
	if notifyHoursThreshold == 0 {
		notifyHoursThreshold = 24
	}
	if v := os.Getenv("NOTIFY_USD_THRESHOLD"); v != "" {
		notifyUSDThreshold, _ = strconv.ParseFloat(v, 64)
	}

	var err error
	db, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)

	if err = db.Ping(); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}
	log.Println("Connected to database")

	// Create tables
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS report_key_quotas (
			channel_id BIGINT PRIMARY KEY,
			quota_usd  NUMERIC(12,4) NOT NULL,
			updated_at BIGINT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS report_daily_agg (
			date              TEXT NOT NULL,
			hour              TEXT NOT NULL DEFAULT '',
			user_id           BIGINT NOT NULL,
			username          TEXT NOT NULL DEFAULT '',
			token_id          BIGINT NOT NULL,
			token_name        TEXT NOT NULL DEFAULT '',
			channel_id        BIGINT NOT NULL,
			channel_name      TEXT NOT NULL DEFAULT '',
			"group"           TEXT NOT NULL DEFAULT '',
			model             TEXT NOT NULL,
			request_count     INT NOT NULL DEFAULT 0,
			input_tokens      BIGINT NOT NULL DEFAULT 0,
			output_tokens     BIGINT NOT NULL DEFAULT 0,
			cache_read_tokens BIGINT NOT NULL DEFAULT 0,
			cache_write_tokens BIGINT NOT NULL DEFAULT 0,
			total_tokens      BIGINT NOT NULL DEFAULT 0,
			input_cost        NUMERIC(14,6) NOT NULL DEFAULT 0,
			output_cost       NUMERIC(14,6) NOT NULL DEFAULT 0,
			cache_read_cost   NUMERIC(14,6) NOT NULL DEFAULT 0,
			cache_write_cost  NUMERIC(14,6) NOT NULL DEFAULT 0,
			total_cost        NUMERIC(14,6) NOT NULL DEFAULT 0,
			PRIMARY KEY (date, hour, user_id, token_id, channel_id, model)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_report_daily_date ON report_daily_agg(date)`,
		// migration: add hour column if missing (table existed before this fix)
		`ALTER TABLE report_daily_agg ADD COLUMN IF NOT EXISTS hour TEXT NOT NULL DEFAULT ''`,
		// migration: add group column (filter dimension)
		`ALTER TABLE report_daily_agg ADD COLUMN IF NOT EXISTS "group" TEXT NOT NULL DEFAULT ''`,
		// migration: rebuild PK to include hour (old PK was missing hour)
		`DO $$ BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_index i
				JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
				WHERE i.indrelid = 'report_daily_agg'::regclass
				  AND i.indisprimary AND a.attname = 'hour'
			) THEN
				ALTER TABLE report_daily_agg DROP CONSTRAINT IF EXISTS report_daily_agg_pkey;
				ALTER TABLE report_daily_agg ADD PRIMARY KEY (date, hour, user_id, token_id, channel_id, model);
			END IF;
		END $$`,
		// profit reporting: per-key upstream unit price (CNY per USD of usage)
		`ALTER TABLE report_key_quotas ADD COLUMN IF NOT EXISTS unit_price_cny NUMERIC(8,4)`,
		`ALTER TABLE report_key_quotas ADD COLUMN IF NOT EXISTS note TEXT NOT NULL DEFAULT ''`,
		// downstream pricing (USD multiplier / discount, e.g. 0.85), keyed by token group
		`CREATE TABLE IF NOT EXISTS report_downstream_pricing (
			"group"         TEXT PRIMARY KEY,
			discount        NUMERIC(8,4) NOT NULL,
			note            TEXT NOT NULL DEFAULT '',
			updated_at      BIGINT NOT NULL
		)`,
		// migration: rename unit_price_cny -> discount, converting prior CNY/USD values
		// using the legacy default FX rate (6.77) so existing rows remain meaningful.
		`DO $$ BEGIN
			IF EXISTS (SELECT 1 FROM information_schema.columns
			           WHERE table_name='report_downstream_pricing' AND column_name='unit_price_cny')
			   AND NOT EXISTS (SELECT 1 FROM information_schema.columns
			           WHERE table_name='report_downstream_pricing' AND column_name='discount') THEN
				ALTER TABLE report_downstream_pricing RENAME COLUMN unit_price_cny TO discount;
				UPDATE report_downstream_pricing SET discount = ROUND(discount / 6.77, 4);
			END IF;
		END $$`,
		// per-day FX rate (CNY per USD). Falls back to default when a date has no row.
		`CREATE TABLE IF NOT EXISTS report_fx_rate (
			date        TEXT PRIMARY KEY,
			rate        NUMERIC(8,4) NOT NULL,
			updated_at  BIGINT NOT NULL
		)`,
		// generic key-value config (currently: default_fx_rate)
		`CREATE TABLE IF NOT EXISTS report_config (
			key         TEXT PRIMARY KEY,
			value       TEXT NOT NULL,
			updated_at  BIGINT NOT NULL
		)`,
		// pipi daily sync: cost snapshot pulled from System 2
		`CREATE TABLE IF NOT EXISTS report_pipi_daily (
			date              TEXT NOT NULL,
			channel_id        BIGINT NOT NULL,
			channel_name      TEXT NOT NULL DEFAULT '',
			channel_tag       TEXT NOT NULL DEFAULT '',
			request_count     INT NOT NULL DEFAULT 0,
			total_cost_usd    NUMERIC(14,6) NOT NULL DEFAULT 0,
			unit_price_cny    NUMERIC(8,4),
			updated_at        BIGINT NOT NULL,
			PRIMARY KEY (date, channel_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_report_pipi_date ON report_pipi_daily(date)`,
		// Provider testing — projects + per-project run history. Artifacts
		// (trace.md / report.md / stderr.log / result.json) live in R2;
		// these tables hold only metadata.
		`CREATE TABLE IF NOT EXISTS rs_test_project (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL,
			url         TEXT NOT NULL,
			api_key     TEXT NOT NULL,
			created_at  BIGINT NOT NULL,
			updated_at  BIGINT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_rs_test_project_created ON rs_test_project(created_at DESC)`,
		// migration: per-project Claude grader endpoint (URL + api key + model).
		// Empty grader_url or grader_api_key disables grading for the project.
		`ALTER TABLE rs_test_project ADD COLUMN IF NOT EXISTS grader_url     TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE rs_test_project ADD COLUMN IF NOT EXISTS grader_api_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE rs_test_project ADD COLUMN IF NOT EXISTS grader_model   TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS rs_test_run (
			id           TEXT PRIMARY KEY,
			project_id   TEXT NOT NULL REFERENCES rs_test_project(id) ON DELETE CASCADE,
			model        TEXT NOT NULL,
			kind         TEXT NOT NULL,
			status       TEXT NOT NULL,
			pass_at      INT  NOT NULL DEFAULT 1,
			run_grader   BOOLEAN NOT NULL DEFAULT TRUE,
			trace_bytes  BIGINT NOT NULL DEFAULT 0,
			report_bytes BIGINT NOT NULL DEFAULT 0,
			stderr_bytes BIGINT NOT NULL DEFAULT 0,
			result_bytes BIGINT NOT NULL DEFAULT 0,
			error_msg    TEXT NOT NULL DEFAULT '',
			llm_error    TEXT NOT NULL DEFAULT '',
			grader_ms    BIGINT NOT NULL DEFAULT 0,
			started_at   BIGINT NOT NULL,
			ended_at     BIGINT,
			elapsed_ms   BIGINT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_rs_test_run_project_started ON rs_test_run(project_id, started_at DESC)`,
		// migration: combined runs (detect + eval together)
		`ALTER TABLE rs_test_run ADD COLUMN IF NOT EXISTS detect_trace_bytes  BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE rs_test_run ADD COLUMN IF NOT EXISTS detect_report_bytes BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE rs_test_run ADD COLUMN IF NOT EXISTS detect_result_bytes BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE rs_test_run ADD COLUMN IF NOT EXISTS eval_trace_bytes    BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE rs_test_run ADD COLUMN IF NOT EXISTS eval_report_bytes   BIGINT NOT NULL DEFAULT 0`,
		// status rename: legacy 'ok' → 'done' (consistent terminal label)
		`UPDATE rs_test_run SET status='done' WHERE status='ok'`,
		// Local report-service users — independent from the main service's
		// user table. role mirrors common.RoleCommonUser/Admin/Root.
		// studio scopes a role=1 user's All Keys view to channels with the
		// same channels.tag value; '' means "no studio binding" (super_admin
		// + unbound users see everything).
		`CREATE TABLE IF NOT EXISTS rs_auth_user (
			id            BIGSERIAL PRIMARY KEY,
			username      TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			role          INT  NOT NULL DEFAULT 1,
			studio        TEXT NOT NULL DEFAULT '',
			created_at    BIGINT NOT NULL,
			updated_at    BIGINT NOT NULL
		)`,
		`ALTER TABLE rs_auth_user ADD COLUMN IF NOT EXISTS studio TEXT NOT NULL DEFAULT ''`,
		// User disable state. status=1 enabled, status=0 disabled. When a
		// user is disabled we also set disabled_at=now so authMiddleware can
		// reject any JWT whose iat < disabled_at, effectively cutting off
		// tokens issued before the disable moment. Re-enabling flips status
		// back to 1 but leaves disabled_at as-is; the user must log in again
		// to get a fresh token whose iat >= disabled_at.
		`ALTER TABLE rs_auth_user ADD COLUMN IF NOT EXISTS status INT NOT NULL DEFAULT 1`,
		`ALTER TABLE rs_auth_user ADD COLUMN IF NOT EXISTS disabled_at BIGINT NOT NULL DEFAULT 0`,
		// Login attempts log — used for per-username lockout and per-IP rate
		// limiting on /api/login. Pruned to ~24h by startPruneLoginAttempts.
		`CREATE TABLE IF NOT EXISTS rs_login_attempt (
			id           BIGSERIAL PRIMARY KEY,
			username     TEXT NOT NULL,
			ip           TEXT NOT NULL,
			succeeded    BOOLEAN NOT NULL,
			attempted_at BIGINT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_rs_login_attempt_user ON rs_login_attempt(username, attempted_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_rs_login_attempt_ip   ON rs_login_attempt(ip,       attempted_at DESC)`,
		// Saved remote new-api credentials. access_token is stored as
		// AES-256-GCM(nonce || ciphertext) base64-encoded, keyed by
		// SHA-256(jwtSecret). Super admin only.
		`CREATE TABLE IF NOT EXISTS remote_newapi_profile (
			id              BIGSERIAL PRIMARY KEY,
			name            TEXT NOT NULL UNIQUE,
			host            TEXT NOT NULL,
			user_id         BIGINT NOT NULL,
			access_token_enc TEXT NOT NULL,
			created_at      BIGINT NOT NULL,
			updated_at      BIGINT NOT NULL
		)`,
		// Batch-upload defaults per profile. Preloaded into the create modal
		// so the operator only has to type the "middle" segment of the name
		// (YYYYMMDD-<mid>-<key-tail>-<hash>). Empty string = no default.
		`ALTER TABLE remote_newapi_profile ADD COLUMN IF NOT EXISTS default_models TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE remote_newapi_profile ADD COLUMN IF NOT EXISTS default_group  TEXT NOT NULL DEFAULT ''`,
		// Global-FIFO pool throttle knobs. `pool_interval_sec` is the tick
		// interval for uploading queued pending_key rows; `pool_batch_size`
		// is how many keys the tick uploads at once. The tick skips if any
		// active pool row still exists (the previous batch hasn't died
		// yet). Applies only to pool-mode rows (pool_size > 0). Old defaults
		// (60s / 2) mirror the pre-refactor "every 20s, 2 at a time" pace
		// but per-profile-configurable now.
		`ALTER TABLE remote_newapi_profile ADD COLUMN IF NOT EXISTS pool_interval_sec INT NOT NULL DEFAULT 60`,
		`ALTER TABLE remote_newapi_profile ADD COLUMN IF NOT EXISTS pool_batch_size   INT NOT NULL DEFAULT 2`,
		// Per-channel operator metadata that does not live on the remote new-api
		// (额度上限 / 备注). Keyed by (profile_id, remote_channel_id). We keep it
		// local so remote `tag` retains its original grouping semantics.
		`CREATE TABLE IF NOT EXISTS remote_channel_meta (
			profile_id         BIGINT NOT NULL,
			remote_channel_id  BIGINT NOT NULL,
			quota_usd          DOUBLE PRECISION,
			note               TEXT NOT NULL DEFAULT '',
			updated_at         BIGINT NOT NULL,
			PRIMARY KEY (profile_id, remote_channel_id)
		)`,
		// Local-only per-channel cost tracking (CNY per USD of upstream
		// credit). NULL means "unset" so the UI can distinguish "no cost
		// recorded" from "known to be free". Set in bulk via the
		// PATCH .../channels/meta/bulk endpoint.
		`ALTER TABLE remote_channel_meta ADD COLUMN IF NOT EXISTS unit_price_cny DOUBLE PRECISION`,
		// Per-day per-channel downstream sell price. Look up rule for the
		// profit report: for a given (channel, day) take the row with the
		// max date ≤ that day — i.e. yesterday's rate carries over until
		// explicitly overridden. Purely local — never leaves the server.
		`CREATE TABLE IF NOT EXISTS remote_channel_downstream (
			profile_id         BIGINT NOT NULL,
			remote_channel_id  BIGINT NOT NULL,
			date               TEXT   NOT NULL,
			downstream_cny     DOUBLE PRECISION NOT NULL,
			updated_at         BIGINT NOT NULL,
			PRIMARY KEY (profile_id, remote_channel_id, date)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_remote_downstream_lookup
		   ON remote_channel_downstream(profile_id, remote_channel_id, date DESC)`,
		// Per-profile per-day downstream discount (multiplier applied to
		// daily used_usd to compute revenue). Simpler than the per-channel
		// remote_channel_downstream above — one number for a whole profile
		// on a given day. Missing days fall back to the latest date ≤ day.
		`CREATE TABLE IF NOT EXISTS remote_downstream_daily (
			profile_id  BIGINT NOT NULL,
			date        TEXT   NOT NULL,
			discount    DOUBLE PRECISION NOT NULL,
			note        TEXT   NOT NULL DEFAULT '',
			updated_at  BIGINT NOT NULL,
			PRIMARY KEY (profile_id, date)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_remote_daily_lookup
		   ON remote_downstream_daily(profile_id, date DESC)`,
		// Time series of channel state pulled from the remote. Written by
		// the interactive Fetch button AND by startRemoteSnapshotSync. Old
		// rows are pruned by pruneRemoteSnapshotsLoop after
		// REMOTE_SNAPSHOT_RETENTION_DAYS days (default 90).
		`CREATE TABLE IF NOT EXISTS remote_channel_snapshot (
			profile_id         BIGINT NOT NULL,
			remote_channel_id  BIGINT NOT NULL,
			captured_at        BIGINT NOT NULL,
			used_quota         BIGINT NOT NULL,
			status             INT    NOT NULL,
			PRIMARY KEY (profile_id, remote_channel_id, captured_at)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_remote_snapshot_by_time
		   ON remote_channel_snapshot(profile_id, captured_at DESC)`,
		// Scheduled upload queue: keys are staged here and either uploaded
		// immediately (pool_size=0) or throttled (pool_size>0, at most that
		// many active at a time — the "5-dollar drip" pattern). Keys are
		// AES-GCM encrypted at rest; key_hash lets us dedup without ever
		// hitting the DB with plaintext.
		`CREATE TABLE IF NOT EXISTS remote_pending_key (
			id                  BIGSERIAL PRIMARY KEY,
			profile_id          BIGINT NOT NULL,
			key_hash            TEXT   NOT NULL,
			key_encrypted       TEXT   NOT NULL,
			quota_usd           DOUBLE PRECISION NOT NULL DEFAULT 0,
			note                TEXT   NOT NULL DEFAULT '',
			name_prefix         TEXT   NOT NULL DEFAULT '',
			group_name          TEXT   NOT NULL DEFAULT 'default',
			tag                 TEXT   NOT NULL DEFAULT '',
			models              TEXT   NOT NULL DEFAULT '',
			priority            BIGINT NOT NULL DEFAULT 0,
			pool_size           INT    NOT NULL DEFAULT 0,
			status              TEXT   NOT NULL DEFAULT 'pending',
			remote_channel_id   BIGINT NOT NULL DEFAULT 0,
			attempts            INT    NOT NULL DEFAULT 0,
			activated_at        BIGINT NOT NULL DEFAULT 0,
			used_at             BIGINT NOT NULL DEFAULT 0,
			failed_reason       TEXT   NOT NULL DEFAULT '',
			created_at          BIGINT NOT NULL,
			updated_at          BIGINT NOT NULL,
			UNIQUE (profile_id, key_hash)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pending_scheduler
		   ON remote_pending_key(profile_id, status, pool_size, id)`,
		// Current mirror of the remote's channel list — one row per channel,
		// UPSERTed on every sync (both cron and interactive). Lets the page
		// render immediately on refresh without hitting the remote, and
		// survives full report-service restarts.
		`CREATE TABLE IF NOT EXISTS remote_channel_current (
			profile_id         BIGINT NOT NULL,
			remote_channel_id  BIGINT NOT NULL,
			name               TEXT   NOT NULL DEFAULT '',
			type               INT    NOT NULL DEFAULT 0,
			status             INT    NOT NULL DEFAULT 0,
			"group"            TEXT   NOT NULL DEFAULT '',
			tag                TEXT   NOT NULL DEFAULT '',
			priority           BIGINT NOT NULL DEFAULT 0,
			weight             BIGINT NOT NULL DEFAULT 0,
			models             TEXT   NOT NULL DEFAULT '',
			used_quota         BIGINT NOT NULL DEFAULT 0,
			created_time       BIGINT NOT NULL DEFAULT 0,
			updated_at         BIGINT NOT NULL,
			PRIMARY KEY (profile_id, remote_channel_id)
		)`,
	} {
		if _, err = db.Exec(ddl); err != nil {
			log.Fatalf("Failed to create table: %v", err)
		}
	}
	seedAdminUser()

	resetRunningTestRuns()
	startDailyRefresh()
	startNotifyLoop()
	startTestJobReaper()
	startPruneLoginAttempts()
	startRemoteSnapshotSync()
	startRemoteSnapshotPrune()
	startRemotePendingScheduler()
	if profitEnabled {
		startPipiSync()
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	// Auth (no middleware)
	r.POST("/api/login", handleLogin)
	r.POST("/api/logout", handleLogout)
	r.GET("/api/auth/config", handleAuthConfig)
	r.GET("/api/auth/callback", handleSSOCallback)

	// Protected API. Any authenticated caller (incl. role=1 regular users)
	// can hit the routes mounted directly on `api`. Admin-only and super
	// admin-only routes live under the requireRole subgroups below.
	api := r.Group("/api", authMiddleware)
	api.GET("/auth/me", handleAuthMe)
	api.GET("/allkeys/data", handleAllKeysData)
	// Studio Operator (role=2) can batch-create channels scoped to their
	// bound studio. The handler enforces the studio lock; admin+ retain
	// full freedom. GET /config/batch-models is opened at the same tier
	// so the operator can see which models will be assigned; POST stays
	// admin-only so they can't change the default list.
	api.POST("/channels/batch-create", requireRoleOrStudioOperator(minAdminRole), handleBatchCreateChannels)
	api.GET("/config/batch-models", requireRoleOrStudioOperator(minAdminRole), handleGetBatchModels)

	adminAPI := api.Group("", requireRole(minAdminRole))
	adminAPI.GET("/report", handleReport)
	adminAPI.GET("/export/csv", handleExportCSV)
	adminAPI.GET("/export/html", handleExportHTML)
	adminAPI.GET("/keys/data", handleKeysData)
	adminAPI.POST("/keys/quota", handleSaveQuotas)
	adminAPI.POST("/channels/batch-priority", handleBatchUpdateChannelPriority)
	adminAPI.POST("/config/batch-models", handleSetBatchModels)
	adminAPI.GET("/cache-stats", handleCacheStats)
	adminAPI.POST("/refresh", handleRefresh)
	adminAPI.GET("/refresh/status", handleRefreshStatus)
	adminAPI.GET("/notify/status", handleNotifyStatus)
	adminAPI.POST("/notify/check", handleNotifyCheck)
	adminAPI.POST("/notify/test", handleNotifyTest)
	// Per-key upstream pricing edit. Lives outside /profit/* so the All Keys
	// page can manage it even on deployments where the profit report is off.
	adminAPI.POST("/keys/pricing", handleSaveKeyPricing)
	adminAPI.POST("/keys/pricing/bulk", handleBulkSaveKeyPricing)
	adminAPI.GET("/studios", handleStudiosList)
	adminAPI.POST("/keys/test", handleTestKeys)
	adminAPI.GET("/detect/models", handleDetectModels)
	// Admin (role >= 10) gets the full user-list + create + delete +
	// disable/enable + password-reset surface. Each handler enforces the
	// anti-escalation guard (callerCanManage / callerRole > body.Role) so
	// admin can only touch users strictly below their own tier.
	// Editing role and studio still requires super admin — a tier change
	// is the one operation admin can't do without letting them promote a
	// puppet account to their own level.
	adminAPI.GET("/users", handleUsersList)
	adminAPI.POST("/users", handleUserCreate)
	adminAPI.POST("/users/:id/reset-password", handleUserResetPassword)
	adminAPI.POST("/users/:id/disable", handleUserDisable)
	adminAPI.POST("/users/:id/enable", handleUserEnable)
	adminAPI.DELETE("/users/:id", handleUserDelete)

	// PATCH (role/studio changes) + Profit stay super-admin-only.
	superAPI := api.Group("", requireRole(minSuperAdminRole))
	superAPI.PATCH("/users/:id", handleUserUpdate)

	// Remote New-API inspector: lets super admin save credentials for
	// external new-api deployments and pull channel + used_quota data.
	// Token is AES-GCM encrypted at rest.
	//
	// A narrow subset (profile list + pending-queue CRUD) is also opened
	// to studio operators via requireRoleOrStudioOperator below, so an
	// operator can batch-upload keys without seeing profile host/user_id
	// or touching other studios' rows. Each handler re-checks the caller
	// role and enforces the studio scope internally.
	remoteBatchAPI := api.Group("", requireRoleOrStudioOperator(minSuperAdminRole))
	remoteBatchAPI.GET("/remote-newapi/profiles", handleRemoteProfileList)
	remoteBatchAPI.POST("/remote-newapi/pending", handlePendingKeyEnqueue)
	remoteBatchAPI.GET("/remote-newapi/pending", handlePendingKeyList)
	remoteBatchAPI.DELETE("/remote-newapi/pending/:id", handlePendingKeyDelete)

	superAPI.POST("/remote-newapi/profiles", handleRemoteProfileCreate)
	superAPI.PATCH("/remote-newapi/profiles/:id", handleRemoteProfileUpdate)
	superAPI.DELETE("/remote-newapi/profiles/:id", handleRemoteProfileDelete)
	superAPI.POST("/remote-newapi/channels", handleRemoteFetchChannels)
	superAPI.GET("/remote-newapi/channels/:id", handleRemoteChannelGet)
	superAPI.POST("/remote-newapi/channels/create", handleRemoteChannelCreate)
	superAPI.PATCH("/remote-newapi/channels", handleRemoteChannelUpdate)
	superAPI.PATCH("/remote-newapi/channels/meta/bulk", handleRemoteMetaBulkUpdate)
	superAPI.POST("/remote-newapi/channels/downstream/bulk", handleRemoteDownstreamBulk)
	superAPI.GET("/remote-newapi/downstream-daily", handleRemoteDownstreamDailyList)
	superAPI.POST("/remote-newapi/downstream-daily", handleRemoteDownstreamDailyUpsert)
	superAPI.DELETE("/remote-newapi/downstream-daily", handleRemoteDownstreamDailyDelete)
	superAPI.GET("/remote-newapi/stat/summary", handleRemoteStatSummary)
	superAPI.DELETE("/remote-newapi/channels/:id", handleRemoteChannelDelete)
	superAPI.POST("/remote-newapi/channels/test", handleRemoteTestKey)
	superAPI.POST("/remote-newapi/channels/last-hour", handleRemoteChannelLastHour)
	superAPI.GET("/remote-newapi/snapshots", handleRemoteSnapshotHistory)
	superAPI.GET("/remote-newapi/channels/cached", handleRemoteCachedChannels)

	// Provider Testing: super_admin or tester role.
	testingAPI := api.Group("", requireRoleOrTester(minSuperAdminRole))
	testingAPI.GET("/testing/projects", handleTestingProjectsList)
	testingAPI.POST("/testing/projects", handleTestingProjectCreate)
	testingAPI.GET("/testing/projects/:id", handleTestingProjectGet)
	testingAPI.PATCH("/testing/projects/:id", handleTestingProjectUpdate)
	testingAPI.DELETE("/testing/projects/:id", handleTestingProjectDelete)
	testingAPI.GET("/testing/projects/:id/runs", handleTestingRunList)
	testingAPI.POST("/testing/projects/:id/runs", handleTestingRunStart)
	testingAPI.GET("/testing/runs/:id", handleTestingRunDetail)
	testingAPI.GET("/testing/runs/:id/status", handleTestingRunStatus)
	testingAPI.GET("/testing/runs/:id/file", handleTestingRunFile)
	testingAPI.POST("/testing/runs/:id/regrade", handleTestingRunRegrade)
	testingAPI.POST("/testing/runs/:id/cancel", handleTestingRunCancel)
	testingAPI.DELETE("/testing/runs/:id", handleTestingRunDelete)

	// Profit reporting — only mount when the feature is enabled.
	if profitEnabled {
		superAPI.GET("/profit/downstream/pricing", handleListDownstreamPricing)
		superAPI.POST("/profit/downstream/pricing", handleSaveDownstreamPricing)
		superAPI.DELETE("/profit/downstream/pricing/:group", handleDeleteDownstreamPricing)
		superAPI.GET("/profit/fx", handleListFXRate)
		superAPI.POST("/profit/fx", handleSaveFXRate)
		superAPI.POST("/profit/fx/default", handleSaveDefaultFXRate)
		superAPI.DELETE("/profit/fx/:date", handleDeleteFXRate)
		superAPI.GET("/profit/daily", handleProfitDaily)
		superAPI.POST("/profit/pipi/sync", handleSyncPipi)
		superAPI.GET("/profit/pipi/status", handlePipiStatus)
	}

	// SPA — serve for all non-API routes
	r.NoRoute(spaHandler())

	log.Printf("Report service listening on :%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
