package main

// OpenAI key tier estimation for the Key Capacity page.
//
// OpenAI does not expose an account's usage tier through the API, but every
// completion response carries x-ratelimit-limit-requests / -tokens for the
// model that was called, and those limits are fixed per tier. Probing each key
// with a one-token gpt-4o-mini request (the model whose limits differ on every
// tier boundary) and matching the token limit against the published table gives
// the tier. Results are stored in rs_key_tier and joined into /api/keys/data.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
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

// handleKeyTierProbe probes the requested enabled OpenAI channels (all of them
// when ids is empty) and stores the estimated tier per channel.
func handleKeyTierProbe(c *gin.Context) {
	var payload struct {
		IDs []int `json:"ids"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	query := `SELECT id, key, COALESCE(base_url,''), COALESCE(setting,'') FROM channels WHERE status = 1 AND type = 1`
	args := []any{}
	if len(payload.IDs) > 0 {
		query += ` AND id = ANY($1::bigint[])`
		ids := make([]string, len(payload.IDs))
		for i, id := range payload.IDs {
			ids[i] = strconv.Itoa(id)
		}
		args = append(args, "{"+strings.Join(ids, ",")+"}")
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
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
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
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
			r := probeOpenAIKeyTier(c.Request.Context(), key, t.baseURL, strings.TrimSpace(setting.Proxy))
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
		if _, err := db.Exec(`
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
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"probed": len(results), "results": results})
}
