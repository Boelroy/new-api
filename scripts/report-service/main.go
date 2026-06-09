package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	_ "github.com/lib/pq"
)

const quotaPerUnit = 500000.0

var db *sql.DB

// ---- Auth ----

var (
	sessions   = map[string]time.Time{}
	sessionsMu sync.Mutex
	adminUser  string
	adminPass  string
)

// ---- Lark Notification ----

var (
	larkWebhook          string
	notifyHoursThreshold float64
	notifyUSDThreshold   float64
	notifyMu             sync.Mutex
	lastNotified         = map[string]time.Time{}
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

	var totalUsed, totalQuota, totalLastHour float64
	hasQuota := false
	for _, ch := range channels {
		totalUsed += ch.UsedUSD
		totalLastHour += ch.LastHourUSD
		if ch.QuotaUSD != nil {
			totalQuota += *ch.QuotaUSD
			hasQuota = true
		}
	}
	if !hasQuota {
		return
	}

	totalRemaining := totalQuota - totalUsed
	var etaHours float64
	hasETA := totalLastHour > 0
	if hasETA {
		etaHours = totalRemaining / totalLastHour
	}

	// hours threshold alert
	if notifyHoursThreshold > 0 && hasETA && etaHours < notifyHoursThreshold {
		if canNotify("hours") {
			sendLark(fmt.Sprintf(
				"⚠️ Key 余量预警\n剩余额度：$%.2f / $%.2f\n上小时消耗：$%.4f\n预计剩余时长：%s（低于阈值 %.0f 小时）",
				totalRemaining, totalQuota, totalLastHour, fmtHours(etaHours), notifyHoursThreshold,
			))
		}
	}

	// usd threshold alert
	if notifyUSDThreshold > 0 && totalRemaining < notifyUSDThreshold {
		if canNotify("usd") {
			sendLark(fmt.Sprintf(
				"🚨 Key 余额不足\n剩余额度：$%.2f（低于阈值 $%.2f）\n上小时消耗：$%.4f\n预计剩余时长：%s",
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
	ticker := time.NewTicker(10 * time.Minute)
	go func() {
		for range ticker.C {
			checkAndNotify()
		}
	}()
}

func newSession() string {
	b := make([]byte, 16)
	rand.Read(b)
	token := hex.EncodeToString(b)
	sessionsMu.Lock()
	sessions[token] = time.Now().Add(12 * time.Hour)
	sessionsMu.Unlock()
	return token
}

func validSession(token string) bool {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	exp, ok := sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(sessions, token)
		return false
	}
	return true
}

func authMiddleware(c *gin.Context) {
	token, err := c.Cookie("session")
	if err != nil || !validSession(token) {
		c.Redirect(http.StatusFound, "/login?next="+c.Request.URL.RequestURI())
		c.Abort()
		return
	}
	c.Next()
}

// LogRow represents a single usage log entry from the database.
type LogRow struct {
	Hour               string  `json:"hour"`
	UserID             int     `json:"user_id"`
	Username           string  `json:"username"`
	TokenID            int     `json:"token_id"`
	TokenName          string  `json:"token_name"`
	ChannelID          int     `json:"channel_id"`
	ChannelName        string  `json:"channel_name"`
	Model              string  `json:"model"`
	RequestCount       int     `json:"request_count"`
	InputTokens        int64   `json:"input_tokens"`
	OutputTokens       int64   `json:"output_tokens"`
	CacheReadTokens    int64   `json:"cache_read_tokens"`
	CacheWriteTokens   int64   `json:"cache_write_tokens"`
	TotalTokens        int64   `json:"total_tokens"`
	InputCost          float64 `json:"input_cost"`
	OutputCost         float64 `json:"output_cost"`
	CacheReadCost      float64 `json:"cache_read_cost"`
	CacheWriteCost     float64 `json:"cache_write_cost"`
	TotalCost          float64 `json:"total_cost"`
}

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

	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS report_key_quotas (
		channel_id BIGINT PRIMARY KEY,
		quota_usd  NUMERIC(12,4) NOT NULL,
		updated_at BIGINT NOT NULL
	)`); err != nil {
		log.Fatalf("Failed to create report_key_quotas table: %v", err)
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	r.GET("/login", handleLoginPage)
	r.POST("/login", handleLoginPost)
	r.GET("/logout", handleLogout)

	auth := r.Group("/", authMiddleware)
	auth.GET("/", handleIndex)
	auth.GET("/keys", handleKeysPage)
	auth.GET("/api/report", handleReport)
	auth.GET("/api/export/html", handleExportHTML)
	auth.GET("/api/export/csv", handleExportCSV)
	auth.GET("/api/keys/data", handleKeysData)
	auth.POST("/api/keys/quota", handleSaveQuotas)

	startNotifyLoop()

	log.Printf("Report service listening on :%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func handleLoginPage(c *gin.Context) {
	next := c.DefaultQuery("next", "/")
	errMsg := c.DefaultQuery("error", "")
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Writer.WriteString(generateLoginHTML(next, errMsg))
}

func handleLoginPost(c *gin.Context) {
	username := c.PostForm("username")
	password := c.PostForm("password")
	next := c.DefaultPostForm("next", "/")
	if username == adminUser && password == adminPass {
		token := newSession()
		c.SetCookie("session", token, 43200, "/", "", false, true)
		c.Redirect(http.StatusFound, next)
		return
	}
	c.Redirect(http.StatusFound, "/login?next="+next+"&error=1")
}

func handleLogout(c *gin.Context) {
	token, err := c.Cookie("session")
	if err == nil {
		sessionsMu.Lock()
		delete(sessions, token)
		sessionsMu.Unlock()
	}
	c.SetCookie("session", "", -1, "/", "", false, true)
	c.Redirect(http.StatusFound, "/login")
}

func generateLoginHTML(next, errMsg string) string {
	errBlock := ""
	if errMsg != "" {
		errBlock = `<div class="err">用户名或密码错误</div>`
	}
	return `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Login — Report</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,Inter,'Segoe UI',sans-serif;background:#f3f4f6;display:flex;align-items:center;justify-content:center;min-height:100vh}
.card{background:#fff;border:1px solid #e5e7eb;border-radius:12px;padding:36px 40px;width:340px;box-shadow:0 1px 3px rgba(0,0,0,.06)}
h1{font-size:1.125rem;font-weight:600;margin-bottom:24px;text-align:center;letter-spacing:-.02em}
label{display:block;font-size:.75rem;color:#6b7280;margin-bottom:4px;font-weight:500}
input{width:100%;border:1px solid #e5e7eb;border-radius:6px;padding:8px 12px;font-size:.875rem;outline:0;margin-bottom:14px;background:#fafafa}
input:focus{border-color:#111827;background:#fff}
button{width:100%;background:#111827;color:#fff;border:none;border-radius:6px;padding:9px;font-size:.875rem;cursor:pointer;font-weight:500}
button:hover{opacity:.85}
.err{background:#fee2e2;color:#991b1b;border-radius:6px;padding:8px 12px;font-size:.8125rem;margin-bottom:14px;text-align:center}
</style></head><body>
<div class="card">
  <h1>Report Service</h1>` + errBlock + `
  <form method="POST" action="/login">
    <input type="hidden" name="next" value="` + next + `">
    <label>用户名</label>
    <input type="text" name="username" autocomplete="username" required>
    <label>密码</label>
    <input type="password" name="password" autocomplete="current-password" required>
    <button type="submit">登录</button>
  </form>
</div>
</body></html>`
}

func queryLogs(startDate, endDate string) ([]LogRow, error) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	startTime, err := time.ParseInLocation("2006-01-02", startDate, loc)
	if err != nil {
		return nil, fmt.Errorf("invalid start date: %v", err)
	}
	endTime, err := time.ParseInLocation("2006-01-02", endDate, loc)
	if err != nil {
		return nil, fmt.Errorf("invalid end date: %v", err)
	}
	endTime = endTime.AddDate(0, 0, 1) // inclusive end

	startTS := startTime.Unix()
	endTS := endTime.Unix()

	query := `
SELECT
  l.created_at,
  l.user_id,
  l.username,
  l.token_id,
  l.token_name,
  l.channel_id,
  COALESCE(c.name, '') as channel_name,
  l.model_name,
  l.prompt_tokens,
  l.completion_tokens,
  l.quota,
  COALESCE(l.other, '{}') as other_json
FROM logs l
LEFT JOIN channels c ON l.channel_id = c.id
WHERE l.type = 2
  AND l.created_at >= $1 AND l.created_at < $2
ORDER BY l.created_at`

	rows, err := db.Query(query, startTS, endTS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// aggregate by hour + user + token + channel + model
	type aggKey struct {
		hour        string
		userID      int
		username    string
		tokenID     int
		tokenName   string
		channelID   int
		channelName string
		model       string
	}
	aggMap := make(map[aggKey]*LogRow)

	for rows.Next() {
		var createdAt int64
		var userID, tokenID, channelID int
		var username, tokenName, channelName, modelName, otherJSON string
		var promptTokens, completionTokens, quota int64

		if err := rows.Scan(
			&createdAt, &userID, &username, &tokenID, &tokenName,
			&channelID, &channelName, &modelName,
			&promptTokens, &completionTokens, &quota, &otherJSON,
		); err != nil {
			return nil, err
		}

		t := time.Unix(createdAt, 0).In(loc)
		hour := t.Format("2006-01-02 15:00")

		// parse other JSON for cache and ratio info
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

		// Calculate per-component cost from quota breakdown
		// total_cost = quota / quotaPerUnit (already charged amount)
		totalCost := float64(quota) / quotaPerUnit

		// Estimate component costs using ratios
		var inputCost, outputCost, cacheReadCost, cacheWriteCost float64
		if modelRatio > 0 {
			pricePerInputToken := modelRatio * groupRatio * 2 / 1000000 // $/token
			inputCost = float64(promptTokens) * pricePerInputToken
			outputCost = float64(completionTokens) * pricePerInputToken * completionRatio
			cacheReadCost = float64(cacheRead) * pricePerInputToken * cacheRatio
			cacheWriteCost = float64(cacheWrite) * pricePerInputToken * cacheCreationRatio
		} else {
			inputCost = totalCost
		}

		key := aggKey{hour, userID, username, tokenID, tokenName, channelID, channelName, modelName}
		row, ok := aggMap[key]
		if !ok {
			row = &LogRow{
				Hour:        hour,
				UserID:      userID,
				Username:    username,
				TokenID:     tokenID,
				TokenName:   tokenName,
				ChannelID:   channelID,
				ChannelName: channelName,
				Model:       modelName,
			}
			aggMap[key] = row
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

	result := make([]LogRow, 0, len(aggMap))
	for _, row := range aggMap {
		// round costs
		row.InputCost = roundTo(row.InputCost, 6)
		row.OutputCost = roundTo(row.OutputCost, 6)
		row.CacheReadCost = roundTo(row.CacheReadCost, 6)
		row.CacheWriteCost = roundTo(row.CacheWriteCost, 6)
		row.TotalCost = roundTo(row.TotalCost, 6)
		result = append(result, *row)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Hour != result[j].Hour {
			return result[i].Hour < result[j].Hour
		}
		return result[i].Model < result[j].Model
	})

	return result, nil
}

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
	default:
		return 0
	}
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
	default:
		return 0
	}
}

func roundTo(f float64, places int) float64 {
	pow := math.Pow(10, float64(places))
	return math.Round(f*pow) / pow
}

func handleReport(c *gin.Context) {
	startDate := c.DefaultQuery("start", time.Now().AddDate(0, 0, -30).Format("2006-01-02"))
	endDate := c.DefaultQuery("end", time.Now().Format("2006-01-02"))

	data, err := queryLogs(startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"start": startDate,
		"end":   endDate,
		"data":  data,
	})
}

func handleExportCSV(c *gin.Context) {
	startDate := c.DefaultQuery("start", time.Now().AddDate(0, 0, -30).Format("2006-01-02"))
	endDate := c.DefaultQuery("end", time.Now().Format("2006-01-02"))

	data, err := queryLogs(startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	filename := fmt.Sprintf("report_%s_to_%s.csv", startDate, endDate)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	c.Writer.Write([]byte("\xEF\xBB\xBF")) // UTF-8 BOM for Excel

	w := csv.NewWriter(c.Writer)
	w.Write([]string{
		"Hour", "User ID", "Username", "Token ID", "Token Name",
		"Channel ID", "Channel Name", "Model",
		"Requests", "Input Tokens", "Output Tokens",
		"Cache Read Tokens", "Cache Write Tokens", "Total Tokens",
		"Input Cost", "Output Cost", "Cache Read Cost", "Cache Write Cost", "Total Cost",
	})

	for _, row := range data {
		w.Write([]string{
			row.Hour,
			strconv.Itoa(row.UserID),
			row.Username,
			strconv.Itoa(row.TokenID),
			row.TokenName,
			strconv.Itoa(row.ChannelID),
			row.ChannelName,
			row.Model,
			strconv.Itoa(row.RequestCount),
			strconv.FormatInt(row.InputTokens, 10),
			strconv.FormatInt(row.OutputTokens, 10),
			strconv.FormatInt(row.CacheReadTokens, 10),
			strconv.FormatInt(row.CacheWriteTokens, 10),
			strconv.FormatInt(row.TotalTokens, 10),
			fmt.Sprintf("%.6f", row.InputCost),
			fmt.Sprintf("%.6f", row.OutputCost),
			fmt.Sprintf("%.6f", row.CacheReadCost),
			fmt.Sprintf("%.6f", row.CacheWriteCost),
			fmt.Sprintf("%.6f", row.TotalCost),
		})
	}
	w.Flush()
}

func handleExportHTML(c *gin.Context) {
	startDate := c.DefaultQuery("start", time.Now().AddDate(0, 0, -30).Format("2006-01-02"))
	endDate := c.DefaultQuery("end", time.Now().Format("2006-01-02"))

	data, err := queryLogs(startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	dataJSON, _ := json.Marshal(data)

	filename := fmt.Sprintf("report_%s_to_%s.html", startDate, endDate)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	c.Writer.WriteString(generateStandaloneHTML(startDate, endDate, string(dataJSON)))
}

func handleIndex(c *gin.Context) {
	startDate := c.DefaultQuery("start", time.Now().AddDate(0, 0, -7).Format("2006-01-02"))
	endDate := c.DefaultQuery("end", time.Now().Format("2006-01-02"))

	data, err := queryLogs(startDate, endDate)
	if err != nil {
		c.HTML(http.StatusInternalServerError, "", nil)
		return
	}

	dataJSON, _ := json.Marshal(data)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Writer.WriteString(generateReportHTML(startDate, endDate, string(dataJSON)))
}

func generateStandaloneHTML(start, end, dataJSON string) string {
	return generateReportHTMLInner(start, end, dataJSON, true)
}

func generateReportHTML(start, end, dataJSON string) string {
	return generateReportHTMLInner(start, end, dataJSON, false)
}

func generateReportHTMLInner(start, end, dataJSON string, standalone bool) string {
	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html><html lang="zh-CN"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Usage Report</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4"></script>
<style>
:root{--bg:#fafafa;--surface:#fff;--border:#e5e7eb;--text:#1f2937;--text-muted:#6b7280;--accent:#111827;--green:#059669;--amber:#d97706;--rose:#e11d48;--purple:#7c3aed}
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,Inter,'Segoe UI',sans-serif;background:var(--bg);color:var(--text);padding:32px 40px;max-width:1600px;margin:0 auto;line-height:1.5}
h1{font-size:1.25rem;font-weight:600;letter-spacing:-.02em;margin-bottom:2px}
.subtitle{color:var(--text-muted);font-size:.8125rem;margin-bottom:24px}
.cards{display:flex;gap:32px;margin-bottom:28px;padding-bottom:20px;border-bottom:1px solid var(--border)}
.card{min-width:0}.card .label{font-size:.6875rem;color:var(--text-muted);text-transform:uppercase;letter-spacing:.06em;font-weight:500}
.card .value{font-size:1.375rem;font-weight:600;margin-top:2px;font-variant-numeric:tabular-nums}
.card .value.cost{color:var(--green)}.card .value.tokens{color:var(--accent)}.card .value.requests{color:var(--amber)}.card .value.keys{color:var(--purple)}
.controls{display:flex;gap:12px;align-items:center;margin-bottom:12px;flex-wrap:wrap}
.controls label{font-size:.8125rem;color:var(--text-muted)}
select,input[type=date]{background:var(--surface);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:5px 10px;font-size:.8125rem;outline:0}
select:focus,input:focus{border-color:var(--accent)}
button{background:var(--accent);color:#fff;border:none;border-radius:6px;padding:6px 14px;font-size:.8125rem;cursor:pointer}
button:hover{opacity:.85}
button.secondary{background:var(--surface);color:var(--text);border:1px solid var(--border)}
button.secondary:hover{background:#f3f4f6}
.tabs{display:flex;gap:0;margin-bottom:16px;border-bottom:1px solid var(--border)}
.tab{padding:8px 16px;cursor:pointer;border:none;background:0 0;color:var(--text-muted);font-size:.8125rem;border-bottom:2px solid transparent;margin-bottom:-1px;transition:all .15s}
.tab:hover{color:var(--text)}.tab.active{color:var(--accent);border-bottom-color:var(--accent);font-weight:600}
.main-layout{display:grid;grid-template-columns:1fr 380px;gap:24px;align-items:start}
.charts{display:flex;flex-direction:column;gap:16px;position:sticky;top:24px}
.chart-box{background:var(--surface);border:1px solid var(--border);border-radius:8px;padding:16px}
.chart-box h3{font-size:.6875rem;color:var(--text-muted);text-transform:uppercase;letter-spacing:.04em;font-weight:500;margin-bottom:8px}
.chart-box canvas{max-height:160px}
@media(max-width:1100px){.main-layout{grid-template-columns:1fr}.charts{position:static}body{padding:16px}}
.table-wrap{overflow-x:auto;border:1px solid var(--border);border-radius:8px;max-height:80vh;overflow-y:auto;background:var(--surface)}
table{width:100%;border-collapse:separate;border-spacing:0;font-size:.75rem;white-space:nowrap}
th{background:var(--bg);position:sticky;top:0;z-index:1;text-align:left;padding:8px 12px;color:var(--text-muted);font-weight:500;text-transform:uppercase;font-size:.625rem;letter-spacing:.06em;border-bottom:1px solid var(--border)}
td{padding:6px 12px;border-bottom:1px solid #f3f4f6}
tr:hover td{background:#f9fafb}
tr.summary-row td{background:#f0fdf4;font-weight:600;border-top:2px solid var(--green);position:sticky;bottom:0;z-index:1}
.num{text-align:right;font-variant-numeric:tabular-nums}
td.sticky-col,th.sticky-col{position:sticky;left:0;z-index:2;background:var(--surface)}
th.sticky-col{background:var(--bg);z-index:3}
tr.summary-row td.sticky-col{background:#f0fdf4}
</style></head><body>
<h1>API Usage Report</h1>
<div class="subtitle" id="dateRange"></div>
`)

	if !standalone {
		sb.WriteString(`<nav style="display:flex;gap:16px;margin-bottom:20px;padding-bottom:12px;border-bottom:1px solid #e5e7eb;font-size:.8125rem">
  <a href="/" style="color:#111827;font-weight:600;text-decoration:none">Report</a>
  <a href="/keys" style="color:#6b7280;text-decoration:none">Key Capacity</a>
  <a href="/logout" style="margin-left:auto;color:#6b7280;text-decoration:none">退出</a>
</nav>
<div class="controls" id="dateControls">
  <label>Start: <input type="date" id="startDate" value="` + start + `"></label>
  <label>End: <input type="date" id="endDate" value="` + end + `"></label>
  <button onclick="reloadReport()">Query</button>
  <button class="secondary" onclick="location.href='/api/export/csv?start='+document.getElementById('startDate').value+'&end='+document.getElementById('endDate').value">Export CSV</button>
  <button class="secondary" onclick="location.href='/api/export/html?start='+document.getElementById('startDate').value+'&end='+document.getElementById('endDate').value">Export HTML</button>
</div>`)
	}

	sb.WriteString(`
<div class="cards" id="summaryCards"></div>
<div class="controls">
  <label>Week: <select id="weekFilter"><option value="__all__">All Weeks</option></select></label>
  <label>Dimension: <select id="dimFilter">
    <option value="key">By Key</option>
    <option value="user">By User</option>
    <option value="channel">By Channel</option>
  </select></label>
  <label>Filter: <select id="entityFilter"><option value="__all__">All</option></select></label>
</div>
<div class="tabs" id="viewTabs">
  <div class="tab active" data-view="hourly">Hourly Detail</div>
  <div class="tab" data-view="daily">Daily Summary</div>
  <div class="tab" data-view="entity">Per-Key Summary</div>
  <div class="tab" data-view="model">Per-Model Summary</div>
</div>
<div class="main-layout">
  <div class="table-wrap"><table id="dataTable"></table></div>
  <div class="charts">
    <div class="chart-box"><h3>Cost Over Time ($)</h3><canvas id="costChart"></canvas></div>
    <div class="chart-box"><h3>Tokens Over Time</h3><canvas id="tokenChart"></canvas></div>
    <div class="chart-box"><h3 id="entityChartTitle">Cost By Key ($)</h3><canvas id="entityChart"></canvas></div>
    <div class="chart-box"><h3>Cost By Model ($)</h3><canvas id="modelChart"></canvas></div>
  </div>
</div>
<script>
var DATA=` + dataJSON + `;
var REPORT_START="` + start + `";
var REPORT_END="` + end + `";
var COLORS=["#2563eb","#059669","#d97706","#e11d48","#7c3aed","#ea580c","#0d9488","#c026d3","#3b82f6","#10b981","#eab308","#ef4444","#6366f1","#f97316","#06b6d4","#a855f7"];

function fmtNum(v){return v.toLocaleString("en-US")}
function fmtCost(v){return "$"+v.toFixed(4)}
function fmtCost2(v){return "$"+v.toFixed(2)}

// ---- dimension helpers ----
function getDimLabel(d, row) {
  if(d==="key") return row.token_name || ("token#"+row.token_id);
  if(d==="user") return row.username || ("user#"+row.user_id);
  if(d==="channel") return row.channel_name || ("ch#"+row.channel_id);
  return "";
}
function getDimId(d, row) {
  if(d==="key") return row.token_id;
  if(d==="user") return row.user_id;
  if(d==="channel") return row.channel_id;
  return 0;
}

// ---- date range ----
document.getElementById("dateRange").textContent = REPORT_START + " ~ " + REPORT_END + " (Asia/Shanghai)";

// ---- weeks ----
var allDates=[], dateSet=new Set();
DATA.forEach(function(r){dateSet.add(r.hour.slice(0,10))});
dateSet.forEach(function(d){allDates.push(d)});
allDates.sort();

function fmtLocalDate(d){return d.getFullYear()+"-"+("0"+(d.getMonth()+1)).slice(-2)+"-"+("0"+d.getDate()).slice(-2)}
var weeks=[], weekMap={};
allDates.forEach(function(d){
  var dt=new Date(d+"T00:00:00"),dow=dt.getDay(),
      mon=new Date(dt); mon.setDate(dt.getDate()-dow+(dow===0?-6:1));
  var sun=new Date(mon); sun.setDate(sun.getDate()+6);
  var k=fmtLocalDate(mon);
  if(!weekMap[k]){weekMap[k]={start:k,end:fmtLocalDate(sun),label:k.slice(5)+" ~ "+fmtLocalDate(sun).slice(5)};weeks.push(weekMap[k])}
});
weeks.sort(function(a,b){return a.start.localeCompare(b.start)});
var weekSel=document.getElementById("weekFilter");
weeks.forEach(function(w){var o=document.createElement("option");o.value=w.start;o.textContent=w.label;weekSel.appendChild(o)});

// ---- entity filter ----
var dimSel=document.getElementById("dimFilter");
var entitySel=document.getElementById("entityFilter");
var entityTabEl=document.querySelectorAll('[data-view="entity"]')[0];

function populateEntityFilter(){
  while(entitySel.options.length>1) entitySel.remove(1);
  var dim=dimSel.value, seen={};
  DATA.forEach(function(r){
    var id=getDimId(dim,r), label=getDimLabel(dim,r);
    if(!seen[id]){seen[id]=label;var o=document.createElement("option");o.value=id;o.textContent=label;entitySel.appendChild(o)}
  });
  entityTabEl.textContent={key:"Per-Key Summary",user:"Per-User Summary",channel:"Per-Channel Summary"}[dim];
  document.getElementById("entityChartTitle").textContent={key:"Cost By Key ($)",user:"Cost By User ($)",channel:"Cost By Channel ($)"}[dim];
}
populateEntityFilter();
dimSel.addEventListener("change",function(){populateEntityFilter();refresh()});

// ---- charts ----
var costChart,tokenChart,entityChart,modelChart;
function initCharts(){
  Chart.defaults.color="#6b7280";Chart.defaults.borderColor="#e5e7eb";
  var base={responsive:true,maintainAspectRatio:false,animation:{duration:300},plugins:{legend:{display:false}}};
  var xAxis={ticks:{maxRotation:45,font:{size:10}}};
  costChart=new Chart(document.getElementById("costChart"),{type:"bar",data:{labels:[],datasets:[]},options:Object.assign({},base,{scales:{x:xAxis,y:{beginAtZero:true,ticks:{callback:function(v){return"$"+v}}}}})});
  tokenChart=new Chart(document.getElementById("tokenChart"),{type:"bar",data:{labels:[],datasets:[]},options:Object.assign({},base,{scales:{x:xAxis,y:{beginAtZero:true,stacked:true,ticks:{callback:function(v){return v/1e3+"k"}}}},plugins:Object.assign({},base.plugins)})});
  tokenChart.options.scales.x.stacked=true;
  entityChart=new Chart(document.getElementById("entityChart"),{type:"doughnut",data:{labels:[],datasets:[]},options:Object.assign({},base,{plugins:Object.assign({},base.plugins,{tooltip:{callbacks:{label:function(c){return c.label+": $"+c.parsed.toFixed(2)}}}})})});
  modelChart=new Chart(document.getElementById("modelChart"),{type:"bar",data:{labels:[],datasets:[]},options:Object.assign({},base,{scales:{x:Object.assign({},xAxis,{stacked:true}),y:{beginAtZero:true,stacked:true,ticks:{callback:function(v){return"$"+v}}}}})});
}

function updateCharts(filtered){
  var dim=dimSel.value;
  var span=(allDates.length>0?(new Date(allDates[allDates.length-1])-new Date(allDates[0]))/864e5:0)>7;
  // cost over time
  var timeMap={};
  filtered.forEach(function(r){var t=span?r.hour.slice(0,10):r.hour;if(!timeMap[t])timeMap[t]={t:t,cost:0,input:0,output:0,cacheR:0,cacheW:0};var b=timeMap[t];b.cost+=r.total_cost;b.input+=r.input_tokens;b.output+=r.output_tokens;b.cacheR+=r.cache_read_tokens;b.cacheW+=r.cache_write_tokens});
  var buckets=Object.values(timeMap).sort(function(a,b){return a.t.localeCompare(b.t)});
  var labels=buckets.map(function(b){return b.t});
  costChart.data={labels:labels,datasets:[{label:"Cost",data:buckets.map(function(b){return b.cost}),backgroundColor:"#2563eb"}]};costChart.update();
  tokenChart.data={labels:labels,datasets:[
    {label:"Input",data:buckets.map(function(b){return b.input}),backgroundColor:"#2563eb",stack:"t"},
    {label:"Output",data:buckets.map(function(b){return b.output}),backgroundColor:"#059669",stack:"t"},
    {label:"Cache Write",data:buckets.map(function(b){return b.cacheW}),backgroundColor:"#d97706",stack:"t"},
    {label:"Cache Read",data:buckets.map(function(b){return b.cacheR}),backgroundColor:"#7c3aed",stack:"t"}
  ]};tokenChart.update();
  // entity chart
  var eCosts={};
  filtered.forEach(function(r){var k=getDimLabel(dim,r);eCosts[k]=(eCosts[k]||0)+r.total_cost});
  var eSorted=Object.entries(eCosts).sort(function(a,b){return b[1]-a[1]});
  entityChart.data={labels:eSorted.map(function(e){return e[0]}),datasets:[{data:eSorted.map(function(e){return e[1]}),backgroundColor:eSorted.map(function(e,i){return COLORS[i%COLORS.length]})}]};entityChart.update();
  // model chart
  var mData={};
  filtered.forEach(function(r){var t=span?r.hour.slice(0,10):r.hour;if(!mData[r.model])mData[r.model]={};mData[r.model][t]=(mData[r.model][t]||0)+r.total_cost});
  var models=Object.keys(mData).sort();
  modelChart.data={labels:labels,datasets:models.map(function(m,i){return{label:m,stack:"m",backgroundColor:COLORS[i%COLORS.length],data:labels.map(function(l){return mData[m][l]||0})}})};modelChart.update();
}

// ---- summary ----
function renderSummary(filtered){
  var cost=0,tokens=0,reqs=0,keys=new Set(),users=new Set(),channels=new Set();
  filtered.forEach(function(r){cost+=r.total_cost;tokens+=r.total_tokens;reqs+=r.request_count;keys.add(r.token_id);users.add(r.user_id);channels.add(r.channel_id)});
  document.getElementById("summaryCards").innerHTML=[
    {l:"Total Cost",v:fmtCost2(cost),c:"cost"},
    {l:"Total Tokens",v:fmtNum(tokens),c:"tokens"},
    {l:"Requests",v:fmtNum(reqs),c:"requests"},
    {l:"Keys",v:keys.size,c:"keys"},
    {l:"Users",v:users.size,c:"keys"},
    {l:"Channels",v:channels.size,c:"keys"}
  ].map(function(x){return '<div class="card"><div class="label">'+x.l+'</div><div class="value '+x.c+'">'+x.v+'</div></div>'}).join("");
}

// ---- table ----
var currentView="hourly";
function renderTable(filtered,view){
  var dim=dimSel.value;
  var rows,cols;
  if(view==="hourly"){
    var m=new Map();
    filtered.forEach(function(r){
      var k=r.hour+"|"+getDimId(dim,r)+"|"+r.model;
      var e=m.get(k);
      if(!e){e={hour:r.hour,entity:getDimLabel(dim,r),model:r.model,request_count:0,input_tokens:0,output_tokens:0,cache_read_tokens:0,cache_write_tokens:0,total_tokens:0,total_cost:0};m.set(k,e)}
      e.request_count+=r.request_count;e.input_tokens+=r.input_tokens;e.output_tokens+=r.output_tokens;e.cache_read_tokens+=r.cache_read_tokens;e.cache_write_tokens+=r.cache_write_tokens;e.total_tokens+=r.total_tokens;e.total_cost+=r.total_cost;
    });
    rows=Array.from(m.values()).sort(function(a,b){return a.hour.localeCompare(b.hour)||a.entity.localeCompare(b.entity)});
    cols=[{k:"hour",l:"Hour",c:"sticky-col"},{k:"entity",l:dim.charAt(0).toUpperCase()+dim.slice(1)},{k:"model",l:"Model"},{k:"request_count",l:"Requests",c:"num"},{k:"input_tokens",l:"Input",c:"num",f:fmtNum},{k:"output_tokens",l:"Output",c:"num",f:fmtNum},{k:"cache_read_tokens",l:"Cache R",c:"num",f:fmtNum},{k:"cache_write_tokens",l:"Cache W",c:"num",f:fmtNum},{k:"total_tokens",l:"Total Tk",c:"num",f:fmtNum},{k:"total_cost",l:"Cost",c:"num",f:fmtCost}];
  } else if(view==="daily"){
    var m=new Map();
    filtered.forEach(function(r){
      var day=r.hour.slice(0,10),k=day+"|"+getDimId(dim,r);
      var e=m.get(k);
      if(!e){e={day:day,entity:getDimLabel(dim,r),request_count:0,input_tokens:0,output_tokens:0,cache_read_tokens:0,cache_write_tokens:0,total_tokens:0,total_cost:0};m.set(k,e)}
      e.request_count+=r.request_count;e.input_tokens+=r.input_tokens;e.output_tokens+=r.output_tokens;e.cache_read_tokens+=r.cache_read_tokens;e.cache_write_tokens+=r.cache_write_tokens;e.total_tokens+=r.total_tokens;e.total_cost+=r.total_cost;
    });
    rows=Array.from(m.values()).sort(function(a,b){return a.day.localeCompare(b.day)||a.entity.localeCompare(b.entity)});
    cols=[{k:"day",l:"Date",c:"sticky-col"},{k:"entity",l:dim.charAt(0).toUpperCase()+dim.slice(1)},{k:"request_count",l:"Requests",c:"num"},{k:"input_tokens",l:"Input",c:"num",f:fmtNum},{k:"output_tokens",l:"Output",c:"num",f:fmtNum},{k:"cache_read_tokens",l:"Cache R",c:"num",f:fmtNum},{k:"cache_write_tokens",l:"Cache W",c:"num",f:fmtNum},{k:"total_tokens",l:"Total Tk",c:"num",f:fmtNum},{k:"total_cost",l:"Cost",c:"num",f:fmtCost}];
  } else if(view==="entity"){
    var m=new Map();
    filtered.forEach(function(r){
      var id=getDimId(dim,r);
      var e=m.get(id);
      if(!e){e={entity:getDimLabel(dim,r),request_count:0,input_tokens:0,output_tokens:0,cache_read_tokens:0,cache_write_tokens:0,total_tokens:0,total_cost:0};m.set(id,e)}
      e.request_count+=r.request_count;e.input_tokens+=r.input_tokens;e.output_tokens+=r.output_tokens;e.cache_read_tokens+=r.cache_read_tokens;e.cache_write_tokens+=r.cache_write_tokens;e.total_tokens+=r.total_tokens;e.total_cost+=r.total_cost;
    });
    rows=Array.from(m.values()).sort(function(a,b){return b.total_cost-a.total_cost});
    cols=[{k:"entity",l:dim.charAt(0).toUpperCase()+dim.slice(1),c:"sticky-col"},{k:"request_count",l:"Requests",c:"num"},{k:"input_tokens",l:"Input",c:"num",f:fmtNum},{k:"output_tokens",l:"Output",c:"num",f:fmtNum},{k:"cache_read_tokens",l:"Cache R",c:"num",f:fmtNum},{k:"cache_write_tokens",l:"Cache W",c:"num",f:fmtNum},{k:"total_tokens",l:"Total Tk",c:"num",f:fmtNum},{k:"total_cost",l:"Cost",c:"num",f:fmtCost}];
  } else { // model
    var m=new Map();
    filtered.forEach(function(r){
      var e=m.get(r.model);
      if(!e){e={model:r.model,request_count:0,input_tokens:0,output_tokens:0,cache_read_tokens:0,cache_write_tokens:0,total_tokens:0,total_cost:0};m.set(r.model,e)}
      e.request_count+=r.request_count;e.input_tokens+=r.input_tokens;e.output_tokens+=r.output_tokens;e.cache_read_tokens+=r.cache_read_tokens;e.cache_write_tokens+=r.cache_write_tokens;e.total_tokens+=r.total_tokens;e.total_cost+=r.total_cost;
    });
    rows=Array.from(m.values()).sort(function(a,b){return b.total_cost-a.total_cost});
    cols=[{k:"model",l:"Model",c:"sticky-col"},{k:"request_count",l:"Requests",c:"num"},{k:"input_tokens",l:"Input",c:"num",f:fmtNum},{k:"output_tokens",l:"Output",c:"num",f:fmtNum},{k:"cache_read_tokens",l:"Cache R",c:"num",f:fmtNum},{k:"cache_write_tokens",l:"Cache W",c:"num",f:fmtNum},{k:"total_tokens",l:"Total Tk",c:"num",f:fmtNum},{k:"total_cost",l:"Cost",c:"num",f:fmtCost}];
  }
  // summary row
  var sums={};
  cols.forEach(function(c){
    if(c.k==="hour"||c.k==="day"||c.k==="entity"||c.k==="model")sums[c.k]="TOTAL";
    else sums[c.k]=rows.reduce(function(s,r){return s+(r[c.k]||0)},0);
  });
  var html="<thead><tr>"+cols.map(function(c){return '<th class="'+(c.c||'')+'">'+c.l+'</th>'}).join("")+"</tr></thead>";
  html+="<tbody>"+rows.map(function(r){return "<tr>"+cols.map(function(c){return '<td class="'+(c.c||'')+'">'+(c.f?c.f(r[c.k]):r[c.k])+'</td>'}).join("")+"</tr>"}).join("");
  html+='<tr class="summary-row">'+cols.map(function(c){return '<td class="'+(c.c||'')+'">'+(c.f&&typeof sums[c.k]==="number"?c.f(sums[c.k]):sums[c.k])+'</td>'}).join("")+"</tr></tbody>";
  document.getElementById("dataTable").innerHTML=html;
}

// ---- refresh ----
function refresh(){
  var w=weekSel.value,dim=dimSel.value,eid=entitySel.value;
  var filtered=DATA;
  if(w!=="__all__"){var wk=weekMap[w];if(wk)filtered=filtered.filter(function(r){var d=r.hour.slice(0,10);return d>=wk.start&&d<=wk.end})}
  if(eid!=="__all__")filtered=filtered.filter(function(r){return getDimId(dim,r)==eid});
  renderSummary(filtered);
  updateCharts(filtered);
  renderTable(filtered,currentView);
}

weekSel.addEventListener("change",refresh);
entitySel.addEventListener("change",refresh);
document.getElementById("viewTabs").addEventListener("click",function(e){
  var t=e.target.closest(".tab");
  if(t){document.querySelectorAll(".tab").forEach(function(x){x.classList.remove("active")});t.classList.add("active");currentView=t.dataset.view;refresh()}
});

function reloadReport(){
  var s=document.getElementById("startDate").value;
  var e=document.getElementById("endDate").value;
  location.href="/?start="+s+"&end="+e;
}

initCharts();
refresh();
</script></body></html>`)
	return sb.String()
}

// ---- Key Capacity Page ----

type ChannelRow struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	Key         string   `json:"key"`
	Status      int      `json:"status"`
	UsedUSD     float64  `json:"used_usd"`
	LastHourUSD float64  `json:"last_hour_usd"`
	QuotaUSD    *float64 `json:"quota_usd"`
}

func queryKeyData() ([]ChannelRow, error) {
	rows, err := db.Query(`
		SELECT c.id, COALESCE(c.name,''), c.key, COALESCE(c.status,1), COALESCE(c.used_quota,0), q.quota_usd
		FROM channels c
		LEFT JOIN report_key_quotas q ON q.channel_id = c.id
		WHERE c.status = 1
		ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	channels := make([]ChannelRow, 0)
	idxMap := make(map[int]int) // channel_id -> index in channels
	for rows.Next() {
		var r ChannelRow
		var usedQuota int64
		var quotaUSD sql.NullFloat64
		if err := rows.Scan(&r.ID, &r.Name, &r.Key, &r.Status, &usedQuota, &quotaUSD); err != nil {
			return nil, err
		}
		r.UsedUSD = roundTo(float64(usedQuota)/quotaPerUnit, 4)
		if quotaUSD.Valid {
			v := roundTo(quotaUSD.Float64, 4)
			r.QuotaUSD = &v
		}
		idxMap[r.ID] = len(channels)
		channels = append(channels, r)
	}

	// last hour usage per channel
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

func handleSaveQuotas(c *gin.Context) {
	var payload []struct {
		ChannelID int     `json:"channel_id"`
		QuotaUSD  float64 `json:"quota_usd"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	now := time.Now().Unix()
	for _, p := range payload {
		_, err := db.Exec(`
			INSERT INTO report_key_quotas (channel_id, quota_usd, updated_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (channel_id) DO UPDATE SET quota_usd=$2, updated_at=$3`,
			p.ChannelID, p.QuotaUSD, now)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"saved": len(payload)})
}

func handleKeysData(c *gin.Context) {
	data, err := queryKeyData()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, data)
}

func handleKeysPage(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Writer.WriteString(generateKeysHTML())
}

func generateKeysHTML() string {
	return `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Key Capacity</title>
<style>
:root{--bg:#fafafa;--surface:#fff;--border:#e5e7eb;--text:#1f2937;--text-muted:#6b7280;--accent:#111827;--green:#059669;--amber:#d97706;--rose:#e11d48;--purple:#7c3aed;--blue:#2563eb}
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,Inter,'Segoe UI',sans-serif;background:var(--bg);color:var(--text);padding:32px 40px;max-width:1400px;margin:0 auto;line-height:1.5}
h1{font-size:1.25rem;font-weight:600;letter-spacing:-.02em;margin-bottom:4px}
.subtitle{color:var(--text-muted);font-size:.8125rem;margin-bottom:24px}
nav{display:flex;gap:16px;margin-bottom:28px;border-bottom:1px solid var(--border);padding-bottom:12px}
nav a{font-size:.8125rem;color:var(--text-muted);text-decoration:none}nav a:hover,nav a.active{color:var(--accent);font-weight:600}
.layout{display:grid;grid-template-columns:320px 1fr;gap:24px;align-items:start}
.panel{background:var(--surface);border:1px solid var(--border);border-radius:8px;padding:16px}
.panel h2{font-size:.75rem;color:var(--text-muted);text-transform:uppercase;letter-spacing:.06em;font-weight:500;margin-bottom:10px}
textarea{width:100%;height:320px;border:1px solid var(--border);border-radius:6px;padding:10px;font-size:.75rem;font-family:monospace;resize:vertical;outline:0;background:var(--bg);color:var(--text)}
textarea:focus{border-color:var(--accent)}
.hint{font-size:.6875rem;color:var(--text-muted);margin-top:6px;line-height:1.6}
button{background:var(--accent);color:#fff;border:none;border-radius:6px;padding:6px 14px;font-size:.8125rem;cursor:pointer;margin-top:10px}
button:hover{opacity:.85}
.cards{display:flex;gap:20px;margin-bottom:16px;flex-wrap:wrap}
.card{background:var(--surface);border:1px solid var(--border);border-radius:8px;padding:12px 16px;min-width:120px}
.card .label{font-size:.6875rem;color:var(--text-muted);text-transform:uppercase;letter-spacing:.06em;font-weight:500}
.card .value{font-size:1.125rem;font-weight:600;margin-top:2px;font-variant-numeric:tabular-nums}
.table-wrap{overflow-x:auto;border:1px solid var(--border);border-radius:8px;background:var(--surface)}
table{width:100%;border-collapse:separate;border-spacing:0;font-size:.75rem;white-space:nowrap}
th{background:var(--bg);position:sticky;top:0;z-index:1;text-align:left;padding:8px 12px;color:var(--text-muted);font-weight:500;text-transform:uppercase;font-size:.625rem;letter-spacing:.06em;border-bottom:1px solid var(--border)}
td{padding:7px 12px;border-bottom:1px solid #f3f4f6}
tr:last-child td{border-bottom:0}
tr:hover td{background:#f9fafb}
.num{text-align:right;font-variant-numeric:tabular-nums}
.badge{display:inline-block;padding:2px 7px;border-radius:999px;font-size:.625rem;font-weight:600;letter-spacing:.04em}
.badge-on{background:#dcfce7;color:#166534}
.badge-off{background:#fee2e2;color:#991b1b}
.badge-auto{background:#fef3c7;color:#92400e}
.bar-wrap{width:80px;display:inline-block;vertical-align:middle;margin-left:6px}
.bar-bg{height:5px;background:#e5e7eb;border-radius:3px;overflow:hidden}
.bar-fill{height:100%;border-radius:3px;transition:width .3s}
.eta-ok{color:var(--green);font-weight:600}
.eta-warn{color:var(--amber);font-weight:600}
.eta-crit{color:var(--rose);font-weight:600}
.eta-na{color:var(--text-muted)}
.key-text{font-family:monospace;font-size:.6875rem;color:var(--text-muted)}
@media(max-width:900px){.layout{grid-template-columns:1fr}body{padding:16px}}
.refreshed{font-size:.6875rem;color:var(--text-muted);margin-left:12px}
</style></head><body>
<h1>Key Capacity</h1>
<div class="subtitle">每个 Key 的用量与剩余寿命估算</div>
<nav>
  <a href="/">Report</a>
  <a href="/keys" class="active">Key Capacity</a>
  <a href="/logout" style="margin-left:auto">退出</a>
</nav>
<div class="layout">
  <div>
    <div class="panel">
      <h2>Key 额度配置</h2>
      <textarea id="quotaInput" placeholder="每行一个 Key 及其额度（USD），用空格/Tab/逗号分隔：

sk-ant-api03-xxxx    150
sk-ant-api03-yyyy    200
sk-ant-api03-zzzz    100"></textarea>
      <div class="hint">
        支持格式：<code>key&nbsp;&nbsp;quota_usd</code><br>
        可用空格、Tab 或逗号分隔<br>
        # 开头的行为注释，自动忽略<br>
        配置保存在浏览器本地
      </div>
      <button onclick="applyAndRefresh()">应用并刷新</button>
    </div>
  </div>
  <div>
    <div class="cards" id="summaryCards"></div>
    <div style="display:flex;align-items:center;margin-bottom:10px">
      <button onclick="loadData()" style="margin-top:0">刷新数据</button>
      <span class="refreshed" id="refreshedAt"></span>
    </div>
    <div class="table-wrap">
      <table>
        <thead><tr>
          <th>ID</th>
          <th>名称</th>
          <th>Key 末尾</th>
          <th class="num">已用 ($)</th>
          <th class="num">额度 ($)</th>
          <th class="num">剩余 ($)</th>
          <th>剩余%</th>
          <th class="num">上小时消耗 ($)</th>
          <th>预计剩余时长</th>
        </tr></thead>
        <tbody id="tableBody"></tbody>
      </table>
    </div>
  </div>
</div>
<script>
var quotaMap = {};
var rawData = [];

function parseQuotas() {
  quotaMap = {};
  var lines = document.getElementById("quotaInput").value.split("\n");
  lines.forEach(function(line) {
    line = line.trim();
    if (!line || line.startsWith("#")) return;
    var parts = line.split(/[\s,]+/);
    if (parts.length >= 2) {
      var key = parts[0].trim();
      var q = parseFloat(parts[1]);
      if (key && !isNaN(q)) quotaMap[key] = q;
    }
  });
}

function applyAndRefresh() {
  parseQuotas();
  // build payload: match key -> channel_id from rawData
  var payload = [];
  rawData.forEach(function(r) {
    if (quotaMap[r.key] !== undefined) {
      payload.push({channel_id: r.id, quota_usd: quotaMap[r.key]});
    }
  });
  fetch("/api/keys/quota", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify(payload)
  }).then(function() {
    loadData();
  }).catch(function(e) { alert("保存失败: "+e); });
}

function fillTextareaFromData() {
  var lines = rawData.map(function(r) {
    var q = r.quota_usd !== null && r.quota_usd !== undefined ? r.quota_usd : "";
    return r.key + (q !== "" ? "\t" + q : "");
  });
  document.getElementById("quotaInput").value = lines.join("\n");
  parseQuotas();
}

function statusBadge(s) {
  if (s === 1) return '<span class="badge badge-on">启用</span>';
  if (s === 2) return '<span class="badge badge-off">手动禁用</span>';
  return '<span class="badge badge-auto">自动禁用</span>';
}

function fmtETA(hours) {
  if (hours === null) return '<span class="eta-na">—</span>';
  if (hours < 0) return '<span class="eta-crit">已超额</span>';
  var cls = hours > 48 ? "eta-ok" : hours > 12 ? "eta-warn" : "eta-crit";
  if (hours >= 24*30) return '<span class="'+cls+'">>30天</span>';
  if (hours >= 24) {
    var d = Math.floor(hours/24), h = Math.floor(hours%24);
    return '<span class="'+cls+'">'+d+'天'+h+'小时</span>';
  }
  return '<span class="'+cls+'">'+hours.toFixed(1)+'小时</span>';
}

function barHTML(pct) {
  var color = pct > 20 ? "#059669" : pct > 5 ? "#d97706" : "#e11d48";
  return '<span class="bar-wrap"><div class="bar-bg"><div class="bar-fill" style="width:'+Math.min(100,Math.max(0,pct))+'%;background:'+color+'"></div></div></span>';
}

function render() {
  var totalUsed = 0, totalQuota = 0, totalLastHour = 0, warnCount = 0;
  var rows = rawData.map(function(r) {
    var quota = (r.quota_usd !== null && r.quota_usd !== undefined) ? r.quota_usd : null;
    var remaining = quota !== null ? quota - r.used_usd : null;
    var pct = quota ? (remaining / quota * 100) : null;
    var eta = null;
    if (remaining !== null && r.last_hour_usd > 0) {
      eta = remaining / r.last_hour_usd;
    } else if (remaining !== null && r.last_hour_usd === 0 && remaining > 0) {
      eta = Infinity;
    }
    if (pct !== null && pct < 20) warnCount++;
    totalUsed += r.used_usd;
    totalLastHour += r.last_hour_usd;
    if (quota) totalQuota += quota;
    return {r:r, quota:quota, remaining:remaining, pct:pct, eta:eta};
  });

  var totalRemaining = totalQuota ? totalQuota - totalUsed : null;
  var totalETA = null;
  if (totalRemaining !== null && totalLastHour > 0) {
    totalETA = totalRemaining / totalLastHour;
  } else if (totalRemaining !== null && totalRemaining > 0) {
    totalETA = Infinity;
  }

  function etaText(h) {
    if (h === null) return "—";
    if (h === Infinity) return "无限";
    if (h < 0) return "已超额";
    if (h >= 24*30) return ">30天";
    if (h >= 24) return Math.floor(h/24)+"天"+Math.floor(h%24)+"小时";
    return h.toFixed(1)+"小时";
  }
  function etaColor(h) {
    if (h === null) return "var(--text-muted)";
    if (h === Infinity || h >= 48) return "var(--green)";
    if (h >= 12) return "var(--amber)";
    return "var(--rose)";
  }

  // summary
  document.getElementById("summaryCards").innerHTML = [
    {l:"启用 Key 数",v:rawData.length,c:"var(--blue)"},
    {l:"总额度",v: totalQuota ? "$"+totalQuota.toFixed(2) : "未配置",c:"var(--text)"},
    {l:"总已用",v:"$"+totalUsed.toFixed(2),c:"var(--rose)"},
    {l:"总剩余",v: totalRemaining !== null ? "$"+totalRemaining.toFixed(2) : "—",c: totalRemaining !== null && totalRemaining < totalQuota*0.2 ? "var(--amber)" : "var(--green)"},
    {l:"上小时消耗",v: totalLastHour > 0 ? "$"+totalLastHour.toFixed(4) : "$0",c:"var(--text-muted)"},
    {l:"预计剩余时长",v:etaText(totalETA),c:etaColor(totalETA)}
  ].map(function(x){return '<div class="card"><div class="label">'+x.l+'</div><div class="value" style="color:'+x.c+'">'+x.v+'</div></div>'}).join("");

  // table
  var html = rows.map(function(x) {
    var r = x.r;
    var keyTail = r.key.length > 12 ? "…"+r.key.slice(-12) : r.key;
    var usedStr = "$"+r.used_usd.toFixed(4);
    var quotaStr = x.quota !== null ? "$"+x.quota.toFixed(2) : '<span style="color:var(--text-muted)">未设置</span>';
    var remStr = x.remaining !== null ? "$"+x.remaining.toFixed(4) : '<span style="color:var(--text-muted)">—</span>';
    var pctBar = x.pct !== null ? x.pct.toFixed(1)+"%" + barHTML(x.pct) : '<span style="color:var(--text-muted)">—</span>';
    var lhStr = r.last_hour_usd > 0 ? "$"+r.last_hour_usd.toFixed(4) : '<span style="color:var(--text-muted)">0</span>';
    var etaStr = x.eta === Infinity ? '<span class="eta-ok">无限</span>' : fmtETA(x.eta);
    return "<tr><td>"+r.id+"</td><td>"+r.name+"</td><td class='key-text'>"+keyTail+"</td><td class='num'>"+usedStr+"</td><td class='num'>"+quotaStr+"</td><td class='num'>"+remStr+"</td><td>"+pctBar+"</td><td class='num'>"+lhStr+"</td><td>"+etaStr+"</td></tr>";
  }).join("");
  document.getElementById("tableBody").innerHTML = html;
}

function loadData() {
  fetch("/api/keys/data").then(function(r){return r.json()}).then(function(data) {
    rawData = data;
    document.getElementById("refreshedAt").textContent = "最后更新：" + new Date().toLocaleTimeString("zh-CN");
    fillTextareaFromData();
    render();
  }).catch(function(e){alert("加载失败: "+e)});
}

loadData();
setInterval(loadData, 60000);
</script></body></html>`
}
