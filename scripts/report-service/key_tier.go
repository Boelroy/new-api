package main

// OpenAI key tier estimation for the Key Capacity page.
//
// OpenAI does not expose an account's usage tier through the API, but every
// completion response carries x-ratelimit-limit-requests / -tokens for the
// model that was called, and those limits are fixed per tier. Probing each key
// with a one-token gpt-4o-mini request (the model whose limits differ on every
// tier boundary) and matching the token limit against the published table gives
// the tier. A leader-only loop probes new keys within minutes and re-probes
// every key daily; results are stored in rs_key_tier and joined into
// /api/keys/data, where Key Capacity marks each key with its tier.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	keyTierProbeModel       = "gpt-4o-mini"
	keyTierProbeConcurrency = 4
	keyTierProbeTimeout     = 20 * time.Second
	keyTierTickInterval     = 5 * time.Minute
	keyTierRefreshAge       = 24 * time.Hour
	keyTierBatchSize        = 50
)

// keyTierTPM is OpenAI's published gpt-4o-mini tokens-per-minute limit for each
// usage tier (index = tier, 0 = free).
var keyTierTPM = []int64{40_000, 200_000, 2_000_000, 4_000_000, 10_000_000, 150_000_000}

// openAITierFromTPM maps a gpt-4o-mini token limit to the highest tier whose
// published limit it reaches. Orgs with a raised custom limit land on the tier
// they exceed rather than failing the match.
func openAITierFromTPM(tpm int64) int {
	tier := -1
	for t, limit := range keyTierTPM {
		if tpm >= limit {
			tier = t
		}
	}
	return tier
}

type keyTierProbeResult struct {
	ChannelID     int    `json:"channel_id"`
	Tier          *int   `json:"tier"`
	LimitRequests int64  `json:"limit_requests"`
	LimitTokens   int64  `json:"limit_tokens"`
	HTTPStatus    int    `json:"http_status"`
	Error         string `json:"error"`
	ProbedAt      int64  `json:"probed_at"`
	// Rate limit of the model this key served most in the last hour; that is
	// the bucket that actually fills up, unlike the gpt-4o-mini tier probe.
	MainModel    string `json:"main_model,omitempty"`
	MainLimitTPM int64  `json:"main_limit_tpm,omitempty"`
}

func probeOpenAIKeyTier(ctx context.Context, key, baseURL, proxyURL string) keyTierProbeResult {
	res := keyTierProbeResult{ProbedAt: time.Now().Unix()}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://api.openai.com"
	}
	body, _ := json.Marshal(map[string]any{
		"model":      keyTierProbeModel,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 1,
	})
	ctx, cancel := context.WithTimeout(ctx, keyTierProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(key))
	req.Header.Set("Content-Type", "application/json")
	resp, err := remoteHTTPClient(proxyURL).Do(req)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	res.HTTPStatus = resp.StatusCode
	res.LimitRequests, _ = strconv.ParseInt(resp.Header.Get("x-ratelimit-limit-requests"), 10, 64)
	res.LimitTokens, _ = strconv.ParseInt(resp.Header.Get("x-ratelimit-limit-tokens"), 10, 64)
	if res.LimitTokens > 0 {
		if tier := openAITierFromTPM(res.LimitTokens); tier >= 0 {
			res.Tier = &tier
		}
	}
	if resp.StatusCode >= 300 && res.Tier == nil {
		var env struct {
			Error struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &env)
		msg := env.Error.Code
		if msg == "" {
			msg = env.Error.Message
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		res.Error = fmt.Sprintf("HTTP %d %s", resp.StatusCode, msg)
	}
	return res
}

// isResponsesTextModel reports whether model can be probed with a tiny
// /v1/responses call (image and audio models cannot).
func isResponsesTextModel(model string) bool {
	return strings.HasPrefix(model, "gpt-") && !strings.Contains(model, "image") &&
		!strings.Contains(model, "audio") && !strings.Contains(model, "tts")
}

// probeModelTPMLimit reads model's tokens-per-minute limit for this key from a
// 16-token /v1/responses call. 0 when the call fails or carries no header.
func probeModelTPMLimit(ctx context.Context, key, baseURL, proxyURL, model string) int64 {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://api.openai.com"
	}
	body, _ := json.Marshal(map[string]any{"model": model, "input": "hi", "max_output_tokens": 16})
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(key))
	req.Header.Set("Content-Type", "application/json")
	resp, err := remoteHTTPClient(proxyURL).Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	limit, _ := strconv.ParseInt(resp.Header.Get("x-ratelimit-limit-tokens"), 10, 64)
	return limit
}

// probeKeyTiers probes enabled OpenAI channels matching filter (an SQL
// fragment over channels c LEFT JOIN rs_key_tier t, appended after the WHERE)
// and stores the estimated tier per channel.
func probeKeyTiers(ctx context.Context, filter string, args ...any) ([]keyTierProbeResult, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.id, c.key, COALESCE(c.base_url,''), COALESCE(c.setting,''),
		       COALESCE((SELECT l.model_name FROM logs l
		                 WHERE l.channel_id = c.id AND l.type = 2 AND l.created_at >= $`+strconv.Itoa(len(args)+1)+`
		                 GROUP BY l.model_name ORDER BY COUNT(*) DESC LIMIT 1), '')
		FROM channels c
		LEFT JOIN rs_key_tier t ON t.channel_id = c.id
		WHERE c.status = 1 AND c.type = 1 `+filter, append(args, time.Now().Add(-time.Hour).Unix())...)
	if err != nil {
		return nil, err
	}
	type target struct {
		id                               int
		key, baseURL, setting, mainModel string
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.key, &t.baseURL, &t.setting, &t.mainModel); err != nil {
			rows.Close()
			return nil, err
		}
		targets = append(targets, t)
	}
	rows.Close()

	results := make([]keyTierProbeResult, len(targets))
	sem := make(chan struct{}, keyTierProbeConcurrency)
	var wg sync.WaitGroup
	for i, t := range targets {
		var setting struct {
			Proxy string `json:"proxy"`
		}
		_ = json.Unmarshal([]byte(t.setting), &setting)
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// Multi-key channels store one key per line; the first is representative.
			key, _, _ := strings.Cut(t.key, "\n")
			r := probeOpenAIKeyTier(ctx, key, t.baseURL, strings.TrimSpace(setting.Proxy))
			r.ChannelID = t.id
			if r.Tier != nil && isResponsesTextModel(t.mainModel) {
				r.MainModel = t.mainModel
				r.MainLimitTPM = probeModelTPMLimit(ctx, key, t.baseURL, strings.TrimSpace(setting.Proxy), t.mainModel)
			}
			results[i] = r
		}()
	}
	wg.Wait()

	for _, r := range results {
		var tier sql.NullInt64
		if r.Tier != nil {
			tier = sql.NullInt64{Int64: int64(*r.Tier), Valid: true}
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO rs_key_tier (channel_id, tier, limit_requests, limit_tokens, probe_model, http_status, error, probed_at, main_model, main_limit_tpm)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (channel_id) DO UPDATE SET
			  -- A failed probe (dead key, network error) keeps the last known tier
			  -- and limits; the error column tells the operator it is stale.
			  tier           = COALESCE(EXCLUDED.tier, rs_key_tier.tier),
			  limit_requests = CASE WHEN EXCLUDED.tier IS NULL THEN rs_key_tier.limit_requests ELSE EXCLUDED.limit_requests END,
			  limit_tokens   = CASE WHEN EXCLUDED.tier IS NULL THEN rs_key_tier.limit_tokens ELSE EXCLUDED.limit_tokens END,
			  probe_model=$5, http_status=$6, error=$7, probed_at=$8,
			  main_model     = CASE WHEN EXCLUDED.main_limit_tpm > 0 THEN EXCLUDED.main_model ELSE rs_key_tier.main_model END,
			  main_limit_tpm = CASE WHEN EXCLUDED.main_limit_tpm > 0 THEN EXCLUDED.main_limit_tpm ELSE rs_key_tier.main_limit_tpm END`,
			r.ChannelID, tier, r.LimitRequests, r.LimitTokens, keyTierProbeModel, r.HTTPStatus, r.Error, r.ProbedAt,
			r.MainModel, r.MainLimitTPM); err != nil {
			return nil, err
		}
	}
	return results, nil
}

// handleKeyTierProbe re-probes the requested enabled OpenAI channels (all of
// them when ids is empty) on demand.
func handleKeyTierProbe(c *gin.Context) {
	var payload struct {
		IDs []int `json:"ids"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	filter, args := "", []any{}
	if len(payload.IDs) > 0 {
		ids := make([]string, len(payload.IDs))
		for i, id := range payload.IDs {
			ids[i] = strconv.Itoa(id)
		}
		filter, args = `AND c.id = ANY($1::bigint[])`, []any{"{" + strings.Join(ids, ",") + "}"}
	}
	results, err := probeKeyTiers(c.Request.Context(), filter, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"probed": len(results), "results": results})
}

// startKeyTierLoop keeps tiers current without operator action: every tick the
// leader probes enabled OpenAI channels that were never probed (fresh uploads)
// or whose last probe is older than keyTierRefreshAge, oldest first.
func startKeyTierLoop() {
	log.Printf("[key-tier] loop starting")
	go func() {
		time.Sleep(2 * time.Minute) // stagger past the other schedulers
		for {
			if IsLeader() {
				cutoff := time.Now().Add(-keyTierRefreshAge).Unix()
				results, err := probeKeyTiers(context.Background(), `
					AND (t.channel_id IS NULL OR t.probed_at < $1)
					ORDER BY COALESCE(t.probed_at, 0), c.id
					LIMIT $2`, cutoff, keyTierBatchSize)
				if err != nil {
					log.Printf("[key-tier] probe: %v", err)
				} else if len(results) > 0 {
					log.Printf("[key-tier] probed %d OpenAI keys", len(results))
				}
				if guard := loadKeyTierGuardConfig(); guard.Enabled {
					if err := disableLowTierKeys(context.Background(), guard); err != nil {
						log.Printf("[key-tier] disable low tier: %v", err)
					}
				}
			}
			time.Sleep(keyTierTickInterval)
		}
	}()
}

// keyTierGuardConfig: the tier patrol disables enabled OpenAI channels whose
// probed tier is at or below MaxTier (low tiers hit rate limits under pool
// load). Stored as JSON in report_config, edited from the 调度 page.
type keyTierGuardConfig struct {
	Enabled bool `json:"enabled"`
	MaxTier int  `json:"max_tier"`
}

const cfgKeyTierGuard = "key_tier_guard_config"

func loadKeyTierGuardConfig() keyTierGuardConfig {
	cfg := keyTierGuardConfig{Enabled: true, MaxTier: 3}
	var raw string
	if err := db.QueryRow(`SELECT value FROM report_config WHERE key = $1`, cfgKeyTierGuard).Scan(&raw); err == nil {
		_ = json.Unmarshal([]byte(raw), &cfg)
	}
	cfg.MaxTier = clampInt(cfg.MaxTier, 0, 4)
	return cfg
}

// disableLowTierKeys sets status=2 (manual disable, which new-api's automatic
// re-enable leaves alone) on every enabled OpenAI channel whose last probe put
// it at or below cfg.MaxTier, and stamps rs_key_tier.disabled_at.
func disableLowTierKeys(ctx context.Context, cfg keyTierGuardConfig) error {
	rows, err := db.QueryContext(ctx, `
		SELECT c.id, t.tier FROM channels c
		JOIN rs_key_tier t ON t.channel_id = c.id
		WHERE c.status = 1 AND c.type = 1 AND t.tier IS NOT NULL AND t.tier <= $1`, cfg.MaxTier)
	if err != nil {
		return err
	}
	type hit struct{ id, tier int }
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.id, &h.tier); err != nil {
			rows.Close()
			return err
		}
		hits = append(hits, h)
	}
	rows.Close()
	for _, h := range hits {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		_, err1 := tx.ExecContext(ctx, `UPDATE channels SET status = 2 WHERE id = $1 AND status = 1`, h.id)
		_, err2 := tx.ExecContext(ctx, `UPDATE abilities SET enabled = false WHERE channel_id = $1`, h.id)
		_, err3 := tx.ExecContext(ctx, `UPDATE rs_key_tier SET disabled_at = $1 WHERE channel_id = $2`, time.Now().Unix(), h.id)
		if err := errors.Join(err1, err2, err3); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		log.Printf("[key-tier] disabled channel %d: tier %d <= T%d", h.id, h.tier, cfg.MaxTier)
	}
	return nil
}

func handleKeyTierGuardConfigGet(c *gin.Context) {
	c.JSON(http.StatusOK, loadKeyTierGuardConfig())
}

func handleKeyTierGuardConfigSet(c *gin.Context) {
	cfg := loadKeyTierGuardConfig()
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	cfg.MaxTier = clampInt(cfg.MaxTier, 0, 4)
	raw, _ := json.Marshal(cfg)
	if _, err := db.Exec(`
		INSERT INTO report_config (key, value, updated_at) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = $3`,
		cfgKeyTierGuard, string(raw), time.Now().Unix()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, cfg)
}

// handleKeyTierGuardDisabled lists channels the patrol disabled, newest first.
func handleKeyTierGuardDisabled(c *gin.Context) {
	rows, err := db.Query(`
		SELECT t.channel_id, COALESCE(ch.name,''), COALESCE(ch.status,0), t.tier, t.limit_requests, t.limit_tokens, t.disabled_at
		FROM rs_key_tier t LEFT JOIN channels ch ON ch.id = t.channel_id
		WHERE t.disabled_at > 0 ORDER BY t.disabled_at DESC LIMIT 500`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id, rpm, tpm, at int64
		var status int
		var tier sql.NullInt64
		var name string
		if err := rows.Scan(&id, &name, &status, &tier, &rpm, &tpm, &at); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		items = append(items, gin.H{"channel_id": id, "channel_name": name, "channel_status": status,
			"tier": tier.Int64, "limit_rpm": rpm, "limit_tpm": tpm, "disabled_at": at})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}
