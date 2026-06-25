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
	_ "github.com/lib/pq"
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

// SSO session cache: maps session cookie value → expiry
var (
	ssoCache   = map[string]time.Time{}
	ssoCacheMu sync.Mutex
)

func checkMainServiceSession(rawCookieHeader string) bool {
	if mainServiceURL == "" || rawCookieHeader == "" {
		return false
	}

	ssoCacheMu.Lock()
	if exp, ok := ssoCache[rawCookieHeader]; ok && time.Now().Before(exp) {
		ssoCacheMu.Unlock()
		return true
	}
	ssoCacheMu.Unlock()

	req, err := http.NewRequest("GET", mainServiceURL+"/api/user/self", nil)
	if err != nil {
		log.Printf("[sso] build request error: %v", err)
		return false
	}
	req.Header.Set("Cookie", rawCookieHeader)
	req.Header.Set("New-Api-User", mainServiceUID)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[sso] request error: %v", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes := make([]byte, 200)
		n, _ := resp.Body.Read(bodyBytes)
		log.Printf("[sso] %d: %s", resp.StatusCode, bodyBytes[:n])
		return false
	}

	var body struct {
		Data struct {
			Role int `json:"role"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		log.Printf("[sso] decode error: %v", err)
		return false
	}
	log.Printf("[sso] user role=%d", body.Data.Role)
	if body.Data.Role < 10 {
		return false
	}

	ssoCacheMu.Lock()
	ssoCache[rawCookieHeader] = time.Now().Add(5 * time.Minute)
	ssoCacheMu.Unlock()
	return true
}

func newJWT() (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   adminUser,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	})
	return token.SignedString(jwtSecret)
}

func authMiddleware(c *gin.Context) {
	// Service-to-service: X-API-Key short-circuit (used by cross-system sync)
	if reportAPIKey != "" {
		if k := c.GetHeader("X-API-Key"); k != "" && k == reportAPIKey {
			c.Next()
			return
		}
	}
	// Try main service SSO first
	if rawCookie := c.GetHeader("Cookie"); rawCookie != "" && strings.Contains(rawCookie, "session=") {
		if checkMainServiceSession(rawCookie) {
			c.Next()
			return
		}
	} else {
		log.Printf("[sso] no session cookie on %s %s", c.Request.Method, c.Request.URL.Path)
	}
	// Fall back to local JWT
	tokenStr, err := c.Cookie("token")
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		c.Abort()
		return
	}
	_, err = jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return jwtSecret, nil
	})
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		c.Abort()
		return
	}
	c.Next()
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
	ID            int      `json:"id"`
	Name          string   `json:"name"`
	Key           string   `json:"key"`
	Status        int      `json:"status"`
	Type          int      `json:"type"`
	Tag           string   `json:"tag"`
	UsedUSD       float64  `json:"used_usd"`
	LastHourUSD   float64  `json:"last_hour_usd"`
	QuotaUSD      *float64 `json:"quota_usd"`
	UnitPriceCNY  *float64 `json:"unit_price_cny"`
	Note          string   `json:"note"`
}

type KeySummary struct {
	Channels      []ChannelRow `json:"channels"`
	TotalLastHour float64      `json:"total_last_hour"`
}

func queryKeyData() ([]ChannelRow, error) {
	rows, err := db.Query(`
		SELECT c.id, COALESCE(c.name,''), c.key, COALESCE(c.status,1), COALESCE(c.type,0), COALESCE(c.tag,''),
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
		if err := rows.Scan(&r.ID, &r.Name, &r.Key, &r.Status, &r.Type, &r.Tag, &usedQuota, &quotaUSD, &unitPrice, &r.Note); err != nil {
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

func queryAllKeys(startTS, endTS int64) ([]ChannelRow, error) {
	query := `
		SELECT c.id, COALESCE(c.name,''), c.key, COALESCE(c.status,1), COALESCE(c.type,0), COALESCE(c.tag,''),
		       COALESCE(c.used_quota,0), q.quota_usd, q.unit_price_cny, COALESCE(q.note,'')
		FROM channels c
		LEFT JOIN report_key_quotas q ON q.channel_id = c.id`
	args := []any{}
	if startTS > 0 && endTS > 0 {
		query += ` WHERE c.created_time >= $1 AND c.created_time < $2`
		args = append(args, startTS, endTS)
	} else if startTS > 0 {
		query += ` WHERE c.created_time >= $1`
		args = append(args, startTS)
	} else if endTS > 0 {
		query += ` WHERE c.created_time < $1`
		args = append(args, endTS)
	}
	query += ` ORDER BY c.id`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	channels := make([]ChannelRow, 0)
	for rows.Next() {
		var r ChannelRow
		var usedQuota int64
		var quotaUSD, unitPrice sql.NullFloat64
		if err := rows.Scan(&r.ID, &r.Name, &r.Key, &r.Status, &r.Type, &r.Tag, &usedQuota, &quotaUSD, &unitPrice, &r.Note); err != nil {
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
		channels = append(channels, r)
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

const minAdminRole = 10 // mirrors common.RoleAdminUser in the main service

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
	if int(roleRaw) < minAdminRole {
		c.Redirect(http.StatusFound, "/login?error=sso_failed")
		return
	}
	localToken, err := newJWT()
	if err != nil {
		c.Redirect(http.StatusFound, "/login?error=sso_failed")
		return
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("token", localToken, 86400, "/", "", false, true)
	c.Redirect(http.StatusFound, "/")
}

func handleAuthConfig(c *gin.Context) {
	resp := gin.H{"profit_enabled": profitEnabled}
	if mainServiceURL != "" {
		resp["sso_url"] = mainServiceURL + "/sign-in"
	} else {
		resp["sso_url"] = nil
	}
	c.JSON(http.StatusOK, resp)
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
	if body.Username != adminUser || body.Password != adminPass {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	tokenStr, err := newJWT()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token generation failed"})
		return
	}
	c.SetCookie("token", tokenStr, 86400, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func handleLogout(c *gin.Context) {
	c.SetCookie("token", "", -1, "/", "", false, true)
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

// Default model list for new Anthropic channels.
var defaultAnthropicModels = strings.Join([]string{
	"claude-opus-4-7",
	"claude-sonnet-4-6",
	"claude-opus-4-6",
	"claude-haiku-4-5-20251001",
	"claude-sonnet-4-5-20250929",
	"claude-opus-4-5-20251101",
	"claude-opus-4-8",
	"claude-fable-5",
}, ",")

const channelInfoDefault = `{"is_multi_key":false,"multi_key_size":0,"multi_key_status_list":null,"multi_key_polling_index":0,"multi_key_mode":""}`

func handleBatchCreateChannels(c *gin.Context) {
	var payload struct {
		Suffix   string `json:"suffix"`
		Channels []struct {
			Key      string  `json:"key"`
			QuotaUSD float64 `json:"quota_usd"`
		} `json:"channels"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	suffix := strings.TrimSpace(payload.Suffix)
	if suffix == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "suffix is required"})
		return
	}
	if len(payload.Channels) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no channels provided"})
		return
	}

	dateStr := time.Now().UTC().Format("0102")
	models := strings.Split(defaultAnthropicModels, ",")
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
		name := fmt.Sprintf("%s-pipi-%s-%d", dateStr, suffix, quotaInt)

		var channelID int
		err := tx.QueryRow(`
			INSERT INTO channels
			(type, key, status, name, weight, created_time, base_url, "group", models,
			 model_mapping, status_code_mapping, priority, auto_ban, used_quota, channel_info)
			VALUES (14, $1, 1, $2, 0, $3, '', 'default', $4,
			        '', '', 1001, 1, 0, $5::json)
			RETURNING id`,
			key, name, now, defaultAnthropicModels, channelInfoDefault).Scan(&channelID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("insert channel: %v", err)})
			return
		}

		for _, m := range models {
			_, err = tx.Exec(`
				INSERT INTO abilities ("group", model, channel_id, enabled, priority, weight)
				VALUES ('default', $1, $2, true, 1001, 0)
				ON CONFLICT DO NOTHING`,
				strings.TrimSpace(m), channelID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("insert ability: %v", err)})
				return
			}
		}

		_, err = tx.Exec(`
			INSERT INTO report_key_quotas (channel_id, quota_usd, updated_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (channel_id) DO UPDATE SET quota_usd=$2, updated_at=$3`,
			channelID, ch.QuotaUSD, now)
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

// ---- Key tester ----

var supportedTestModels = map[string]bool{
	"claude-opus-4-7":            true,
	"claude-sonnet-4-6":          true,
	"claude-opus-4-6":            true,
	"claude-haiku-4-5-20251001":  true,
	"claude-sonnet-4-5-20250929": true,
	"claude-opus-4-5-20251101":   true,
	"claude-opus-4-8":            true,
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
	channels, err := queryAllKeys(startTS, endTS)
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
	} {
		if _, err = db.Exec(ddl); err != nil {
			log.Fatalf("Failed to create table: %v", err)
		}
	}

	startDailyRefresh()
	startNotifyLoop()
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

	// Protected API
	api := r.Group("/api", authMiddleware)
	api.GET("/report", handleReport)
	api.GET("/export/csv", handleExportCSV)
	api.GET("/export/html", handleExportHTML)
	api.GET("/keys/data", handleKeysData)
	api.POST("/keys/quota", handleSaveQuotas)
	api.POST("/channels/batch-create", handleBatchCreateChannels)
	api.GET("/allkeys/data", handleAllKeysData)
	api.POST("/keys/test", handleTestKeys)
	api.GET("/detect/models", handleDetectModels)
	api.POST("/detect/run", handleDetectRun)
	api.POST("/refresh", handleRefresh)
	api.GET("/refresh/status", handleRefreshStatus)
	api.GET("/notify/status", handleNotifyStatus)
	api.POST("/notify/check", handleNotifyCheck)
	api.POST("/notify/test", handleNotifyTest)
	// Per-key upstream pricing edit. Lives outside /profit/* so the All Keys
	// page can manage it even on deployments where the profit report is off.
	api.POST("/keys/pricing", handleSaveKeyPricing)
	api.POST("/keys/pricing/bulk", handleBulkSaveKeyPricing)

	// Profit reporting — only mount when the feature is enabled.
	if profitEnabled {
		api.GET("/profit/downstream/pricing", handleListDownstreamPricing)
		api.POST("/profit/downstream/pricing", handleSaveDownstreamPricing)
		api.DELETE("/profit/downstream/pricing/:group", handleDeleteDownstreamPricing)
		api.GET("/profit/fx", handleListFXRate)
		api.POST("/profit/fx", handleSaveFXRate)
		api.POST("/profit/fx/default", handleSaveDefaultFXRate)
		api.DELETE("/profit/fx/:date", handleDeleteFXRate)
		api.GET("/profit/daily", handleProfitDaily)
		api.POST("/profit/pipi/sync", handleSyncPipi)
		api.GET("/profit/pipi/status", handlePipiStatus)
	}

	// SPA — serve for all non-API routes
	r.NoRoute(spaHandler())

	log.Printf("Report service listening on :%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
