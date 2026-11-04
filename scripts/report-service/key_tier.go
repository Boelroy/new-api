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

// probeKeyTiers probes enabled OpenAI channels matching filter (an SQL
// fragment over channels c LEFT JOIN rs_key_tier t, appended after the WHERE)
// and stores the estimated tier per channel.
func probeKeyTiers(ctx context.Context, filter string, args ...any) ([]keyTierProbeResult, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.id, c.key, COALESCE(c.base_url,''), COALESCE(c.setting,'')
		FROM channels c
		LEFT JOIN rs_key_tier t ON t.channel_id = c.id
		WHERE c.status = 1 AND c.type = 1 `+filter, args...)
	if err != nil {
		return nil, err
	}
	type target struct {
		id                    int
		key, baseURL, setting string
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.key, &t.baseURL, &t.setting); err != nil {
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
			INSERT INTO rs_key_tier (channel_id, tier, limit_requests, limit_tokens, probe_model, http_status, error, probed_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (channel_id) DO UPDATE SET
			  -- A failed probe (dead key, network error) keeps the last known tier
			  -- and limits; the error column tells the operator it is stale.
			  tier           = COALESCE(EXCLUDED.tier, rs_key_tier.tier),
			  limit_requests = CASE WHEN EXCLUDED.tier IS NULL THEN rs_key_tier.limit_requests ELSE EXCLUDED.limit_requests END,
			  limit_tokens   = CASE WHEN EXCLUDED.tier IS NULL THEN rs_key_tier.limit_tokens ELSE EXCLUDED.limit_tokens END,
			  probe_model=$5, http_status=$6, error=$7, probed_at=$8`,
			r.ChannelID, tier, r.LimitRequests, r.LimitTokens, keyTierProbeModel, r.HTTPStatus, r.Error, r.ProbedAt); err != nil {
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
			}
			time.Sleep(keyTierTickInterval)
		}
	}()
}
