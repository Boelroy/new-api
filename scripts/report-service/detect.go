package main

// Provider detection: send the same 6 probes as
// llm-api-bench/tools/provider-detection/probe.mjs against a target
// Claude/Anthropic Messages endpoint, capture the full trace, and
// apply the §2 signal panel from PIPELINE.md to label router/backend.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	detectProbeTimeout    = 30 * time.Second
	detectMaxBodyBytes    = 256 * 1024 // 256 KiB per probe — generous, but bounded
	detectAnthropicVerHdr = "2023-06-01"

	detectDefaultIntervalMs = 500 // pause between probes to avoid bursting the upstream
	detectDefaultMaxRetries = 2   // retry on 429 / 5xx, respecting Retry-After
	detectMaxBackoffSeconds = 30  // cap whatever the upstream asks for
)

type detectProbe struct {
	Label            string            `json:"label"`
	Intent           string            `json:"intent"`
	Status           int               `json:"status"`
	Headers          map[string]string `json:"headers"`
	Body             string            `json:"body"`
	ElapsedMs        int64             `json:"elapsed_ms"`
	Retries          int               `json:"retries,omitempty"`
	RetryHistory    []int             `json:"retry_history,omitempty"` // sequence of status codes that triggered each retry
	StreamEventCount int               `json:"stream_event_count,omitempty"`
	StreamMaxGapMs   int64             `json:"stream_max_gap_ms,omitempty"`
}

type detectSignal struct {
	Code    string `json:"code"`    // e.g. "A1", "A2", "B3"
	Tier    int    `json:"tier"`    // 1 or 2
	Label   string `json:"label"`   // short signal name
	Detail  string `json:"detail"`  // what we observed
	Layer   string `json:"layer"`   // "router" or "backend"
	Implies string `json:"implies"` // resolved verdict
}

type detectClassification struct {
	RouterLabel      string         `json:"router_label"`
	RouterConfidence string         `json:"router_confidence"` // high / medium / low / unknown
	BackendLabel    string         `json:"backend_label"`
	BackendConfidence string       `json:"backend_confidence"`
	Signals         []detectSignal `json:"signals"`
	Notes           []string       `json:"notes,omitempty"`
}

type detectResult struct {
	URL            string               `json:"url"`
	Model          string               `json:"model"`
	StartedAt      string               `json:"started_at"`
	Probes         []detectProbe        `json:"probes"`
	Classification detectClassification `json:"classification"`
}

// ---- HTTP probes ----

func detectHTTPClient() *http.Client {
	return &http.Client{Timeout: detectProbeTimeout}
}

func detectReadBody(r *http.Response) string {
	limited := io.LimitReader(r.Body, detectMaxBodyBytes)
	buf, _ := io.ReadAll(limited)
	return string(buf)
}

func detectHeadersMap(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	return out
}

// detectExecute runs the build → fetch loop with retry on 429 / 5xx. Returns
// the final response (caller closes), the count of retries performed, and the
// sequence of status codes that triggered each retry (for surfacing in UI).
func detectExecute(ctx context.Context, build func() (*http.Request, error), maxRetries int) (*http.Response, int, []int, error) {
	if maxRetries < 0 {
		maxRetries = 0
	}
	var history []int
	for attempt := 0; ; attempt++ {
		req, err := build()
		if err != nil {
			return nil, len(history), history, err
		}
		req = req.WithContext(ctx)
		resp, doErr := detectHTTPClient().Do(req)
		if doErr != nil {
			return nil, len(history), history, doErr
		}
		// Don't retry on success or client errors (other than 429).
		if (resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500) || attempt >= maxRetries {
			return resp, len(history), history, nil
		}
		// Decide backoff: prefer Retry-After header, else exponential 1s/3s.
		wait := time.Duration(1+2*attempt) * time.Second
		if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
			if n, perr := strconv.Atoi(ra); perr == nil && n > 0 {
				if n > detectMaxBackoffSeconds {
					n = detectMaxBackoffSeconds
				}
				wait = time.Duration(n) * time.Second
			}
		}
		history = append(history, resp.StatusCode)
		// Drain + close the throttled response before sleeping.
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		select {
		case <-ctx.Done():
			return nil, len(history), history, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// detectProbeGET fires a single GET probe. Used by Step 0 (/v1/models).
func detectProbeGET(ctx context.Context, base, key, path, label, intent string, maxRetries int) detectProbe {
	p := detectProbe{Label: label, Intent: intent}
	start := time.Now()
	resp, retries, history, err := detectExecute(ctx, func() (*http.Request, error) {
		req, err := http.NewRequest("GET", strings.TrimRight(base, "/")+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		return req, nil
	}, maxRetries)
	p.Retries = retries
	p.RetryHistory = history
	if err != nil {
		p.Body = "<network error: " + err.Error() + ">"
		p.ElapsedMs = time.Since(start).Milliseconds()
		return p
	}
	defer resp.Body.Close()
	p.Status = resp.StatusCode
	p.Headers = detectHeadersMap(resp.Header)
	p.Body = detectReadBody(resp)
	p.ElapsedMs = time.Since(start).Milliseconds()
	return p
}

// detectProbePOST fires a non-streaming POST to /v1/messages.
func detectProbePOST(ctx context.Context, base, key string, body any, label, intent string, maxRetries int) detectProbe {
	p := detectProbe{Label: label, Intent: intent}
	start := time.Now()
	buf, _ := json.Marshal(body)
	resp, retries, history, err := detectExecute(ctx, func() (*http.Request, error) {
		req, err := http.NewRequest("POST", strings.TrimRight(base, "/")+"/v1/messages", bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", detectAnthropicVerHdr)
		req.Header.Set("content-type", "application/json")
		return req, nil
	}, maxRetries)
	p.Retries = retries
	p.RetryHistory = history
	if err != nil {
		p.Body = "<network error: " + err.Error() + ">"
		p.ElapsedMs = time.Since(start).Milliseconds()
		return p
	}
	defer resp.Body.Close()
	p.Status = resp.StatusCode
	p.Headers = detectHeadersMap(resp.Header)
	p.Body = detectReadBody(resp)
	p.ElapsedMs = time.Since(start).Milliseconds()
	return p
}

// detectProbeStream POSTs stream=true and tallies event count + max gap.
func detectProbeStream(ctx context.Context, base, key string, body map[string]any, label, intent string, maxRetries int) detectProbe {
	p := detectProbe{Label: label, Intent: intent}
	bodyCopy := make(map[string]any, len(body)+1)
	for k, v := range body {
		bodyCopy[k] = v
	}
	bodyCopy["stream"] = true
	start := time.Now()
	buf, _ := json.Marshal(bodyCopy)
	resp, retries, history, err := detectExecute(ctx, func() (*http.Request, error) {
		req, err := http.NewRequest("POST", strings.TrimRight(base, "/")+"/v1/messages", bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", detectAnthropicVerHdr)
		req.Header.Set("content-type", "application/json")
		return req, nil
	}, maxRetries)
	p.Retries = retries
	p.RetryHistory = history
	if err != nil {
		p.Body = "<network error: " + err.Error() + ">"
		p.ElapsedMs = time.Since(start).Milliseconds()
		return p
	}
	defer resp.Body.Close()
	p.Status = resp.StatusCode
	p.Headers = detectHeadersMap(resp.Header)

	var sb strings.Builder
	tmp := make([]byte, 4096)
	lastByte := time.Now()
	var maxGap time.Duration
	read := 0
	for {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			now := time.Now()
			gap := now.Sub(lastByte)
			if gap > maxGap {
				maxGap = gap
			}
			lastByte = now
			room := detectMaxBodyBytes - read
			if room <= 0 {
				break
			}
			if n > room {
				n = room
			}
			sb.Write(tmp[:n])
			read += n
		}
		if err != nil {
			break
		}
	}
	p.Body = sb.String()
	p.StreamMaxGapMs = maxGap.Milliseconds()
	events := strings.Count(p.Body, "\nevent: ")
	if strings.HasPrefix(p.Body, "event: ") {
		events++
	}
	p.StreamEventCount = events
	p.ElapsedMs = time.Since(start).Milliseconds()
	return p
}

type detectOptions struct {
	IntervalMs int
	MaxRetries int
}

// runDetect executes the full 6-probe sequence + classification.
func runDetect(rawURL, key, model string, opts detectOptions) (detectResult, error) {
	if _, err := url.Parse(rawURL); err != nil {
		return detectResult{}, fmt.Errorf("invalid url: %w", err)
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return detectResult{}, fmt.Errorf("url must be http:// or https://")
	}
	if key == "" {
		return detectResult{}, fmt.Errorf("missing key")
	}
	if model == "" {
		return detectResult{}, fmt.Errorf("missing model")
	}
	if opts.IntervalMs < 0 {
		opts.IntervalMs = 0
	}
	if opts.IntervalMs > 60_000 {
		opts.IntervalMs = 60_000
	}
	if opts.MaxRetries < 0 {
		opts.MaxRetries = 0
	}
	if opts.MaxRetries > 5 {
		opts.MaxRetries = 5
	}

	ctx := context.Background()
	res := detectResult{
		URL:       rawURL,
		Model:     model,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}

	emit := func(p detectProbe) {
		if len(res.Probes) > 0 && opts.IntervalMs > 0 {
			time.Sleep(time.Duration(opts.IntervalMs) * time.Millisecond)
		}
		res.Probes = append(res.Probes, p)
	}

	emit(detectProbeGET(ctx, rawURL, key, "/v1/models",
		"Step 0: GET /v1/models",
		"Router-layer fingerprint via response headers (x-new-api-version / x-amzn-RequestId / openrouter-* / cf-ray) and the model catalog itself (owned_by hint, supported_endpoint_types breadth).",
		opts.MaxRetries,
	))

	// NOTE: emit() applies the inter-probe delay BEFORE appending; the GET above
	// is index 0 so no delay fires for it.

	emit(detectProbePOST(ctx, rawURL, key,
		map[string]any{
			"model":      model,
			"max_tokens": 10,
			"messages":   []map[string]any{{"role": "user", "content": "reply pong"}},
		},
		"Step 1: Plain probe",
		"Baseline 200 response. Look at: id prefix (msg_bdrk_/msg_vrtx_/msg_<bare>/chatcmpl-/gen-), usage.inference_geo (A2 — non-null = Anthropic 1P), top-level `provider` field if present, HTTP headers.",
		opts.MaxRetries,
	))

	emit(detectProbePOST(ctx, rawURL, key,
		map[string]any{
			"model":      model,
			"max_tokens": 80,
			"tools": []map[string]any{{
				"name":        "get_weather",
				"description": "weather for a city",
				"input_schema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"city": map[string]any{"type": "string"}},
					"required":   []string{"city"},
				},
			}},
			"tool_choice": map[string]any{"type": "tool", "name": "get_weather"},
			"messages":    []map[string]any{{"role": "user", "content": "what is the weather in tokyo? call the get_weather tool now"}},
		},
		"Step 2: Tools probe",
		"Forces a tool_use response via tool_choice. Decisive Tier 1 A1: tool_use[].id prefix — toolu_bdrk_ = AWS Bedrock, toolu_vrtx_ = GCP Vertex, toolu_<bare> = Anthropic 1P.",
		opts.MaxRetries,
	))

	emit(detectProbeStream(ctx, rawURL, key,
		map[string]any{
			"model":      model,
			"max_tokens": 15,
			"messages":   []map[string]any{{"role": "user", "content": "count 1 to 3"}},
		},
		"Step 3: Streaming probe",
		"SSE shape. Look at: message_start id prefix, amazon-bedrock-invocationMetrics in message_stop (Tier 1 A3 = Bedrock decisive), final `data: [DONE]` line (OpenRouter / OpenAI-compat normalizer), inter-byte gap.",
		opts.MaxRetries,
	))

	emit(detectProbePOST(ctx, rawURL, key,
		map[string]any{
			"model":      model,
			"max_tokens": 99999999,
			"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		},
		"Step 4a: huge max_tokens probe",
		"Triggers upstream context-overflow. Search body for 'context-compression plugin' (OpenRouter-only wording).",
		opts.MaxRetries,
	))

	emit(detectProbePOST(ctx, rawURL, key,
		map[string]any{
			"model":      model,
			"max_tokens": 10,
			"messages":   []map[string]any{{"role": "alien", "content": "hi"}},
		},
		"Step 4b: invalid role probe",
		"Triggers a validation error. {error.type:'new_api_error'} = new-api fork; {error.code:'ERR_PROVIDER_NNN'} = service-inference style; Anthropic-native {type:'error', error:{type,message}} = direct/passthrough.",
		opts.MaxRetries,
	))

	res.Classification = classifyDetect(res.Probes)
	return res, nil
}

// ---- Classifier: §2 signal panel ----

var (
	reTooluBdrk    = regexp.MustCompile(`"id"\s*:\s*"toolu_bdrk_`)
	reTooluVrtx    = regexp.MustCompile(`"id"\s*:\s*"toolu_vrtx_`)
	reTooluBare    = regexp.MustCompile(`"id"\s*:\s*"toolu_(?:01|0[2-9A-Za-z])`)
	reMsgBdrk      = regexp.MustCompile(`"id"\s*:\s*"msg_bdrk_`)
	reMsgVrtx      = regexp.MustCompile(`"id"\s*:\s*"msg_vrtx_`)
	reMsgBare      = regexp.MustCompile(`"id"\s*:\s*"msg_01`)
	reGenID        = regexp.MustCompile(`"id"\s*:\s*"gen-`)
	reChatCmpl     = regexp.MustCompile(`"id"\s*:\s*"chatcmpl-`)
	reInferenceGeo = regexp.MustCompile(`"inference_geo"\s*:\s*"(?:global|us|eu|asia)"`)
	reNewAPIErr    = regexp.MustCompile(`"type"\s*:\s*"new_api_error"`)
	reProviderCode = regexp.MustCompile(`"code"\s*:\s*"ERR_PROVIDER_`)
	reSSEDone      = regexp.MustCompile(`(?m)^data:\s*\[DONE\]\s*$`)
)

func classifyDetect(probes []detectProbe) detectClassification {
	cls := detectClassification{
		RouterLabel:       "unknown",
		RouterConfidence:  "unknown",
		BackendLabel:      "unknown",
		BackendConfidence: "unknown",
	}

	// Aggregate text for searches
	var modelsBody, plainBody, toolsBody, streamBody, hugeBody, invalidBody string
	for i, p := range probes {
		switch i {
		case 0:
			modelsBody = p.Body
		case 1:
			plainBody = p.Body
		case 2:
			toolsBody = p.Body
		case 3:
			streamBody = p.Body
		case 4:
			hugeBody = p.Body
		case 5:
			invalidBody = p.Body
		}
	}
	_ = modelsBody
	_ = hugeBody

	// All non-2xx — likely auth/permission issue. Surface that explicitly
	// rather than guessing.
	any2xx := false
	for _, p := range probes {
		if p.Status >= 200 && p.Status < 300 {
			any2xx = true
			break
		}
	}
	if !any2xx {
		cls.Notes = append(cls.Notes, "All probes returned non-2xx — check URL / key / model before drawing conclusions.")
	}

	// ---- Backend (Tier 1) ----

	setBackend := func(label, conf, code, layer, detail string) {
		if cls.BackendLabel == "unknown" || conf == "high" {
			cls.BackendLabel = label
			cls.BackendConfidence = conf
		}
		cls.Signals = append(cls.Signals, detectSignal{
			Code: code, Tier: 1, Label: "backend signal", Detail: detail, Layer: layer, Implies: label,
		})
	}

	if reTooluBdrk.MatchString(toolsBody) {
		setBackend("AWS Bedrock", "high", "A1", "backend", "tool_use[].id prefix toolu_bdrk_ in Step 2")
	} else if reTooluVrtx.MatchString(toolsBody) {
		setBackend("GCP Vertex", "high", "A1", "backend", "tool_use[].id prefix toolu_vrtx_ in Step 2")
	} else if reTooluBare.MatchString(toolsBody) {
		setBackend("Anthropic 1P (or unbranded passthrough)", "medium", "A1", "backend", "tool_use[].id has no platform infix in Step 2")
	}

	if reInferenceGeo.MatchString(plainBody) {
		// A2 is decisive for Anthropic 1P.
		cls.BackendLabel = "Anthropic 1P"
		cls.BackendConfidence = "high"
		cls.Signals = append(cls.Signals, detectSignal{
			Code: "A2", Tier: 1, Label: "inference_geo present", Detail: "usage.inference_geo set in Step 1 — only Anthropic 1P returns this", Layer: "backend", Implies: "Anthropic 1P",
		})
	}

	if strings.Contains(streamBody, "amazon-bedrock-invocationMetrics") {
		setBackend("AWS Bedrock", "high", "A3", "backend", "amazon-bedrock-invocationMetrics present in SSE message_stop (Step 3)")
	}

	if reMsgBdrk.MatchString(plainBody) || reMsgBdrk.MatchString(streamBody) {
		setBackend("AWS Bedrock", "medium", "A4", "backend", "response id prefixed msg_bdrk_ (router may have rewritten)")
	}
	if reMsgVrtx.MatchString(plainBody) || reMsgVrtx.MatchString(streamBody) {
		setBackend("GCP Vertex", "medium", "A4", "backend", "response id prefixed msg_vrtx_")
	}
	if cls.BackendLabel == "unknown" && reMsgBare.MatchString(plainBody) {
		cls.BackendLabel = "Anthropic 1P (likely)"
		cls.BackendConfidence = "low"
		cls.Signals = append(cls.Signals, detectSignal{
			Code: "A4", Tier: 1, Label: "msg id bare", Detail: "id has no platform infix; could also be a router that strips prefixes", Layer: "backend", Implies: "Anthropic 1P",
		})
	}

	// ---- Router (Tier 2) ----

	addRouter := func(label, conf, code, detail string) {
		if cls.RouterLabel == "unknown" || conf == "high" {
			cls.RouterLabel = label
			cls.RouterConfidence = conf
		}
		cls.Signals = append(cls.Signals, detectSignal{
			Code: code, Tier: 2, Label: "router signal", Detail: detail, Layer: "router", Implies: label,
		})
	}

	if reNewAPIErr.MatchString(invalidBody) {
		addRouter("new-api / one-api fork", "high", "B1", "error envelope {type:'new_api_error'} in Step 4b")
	} else if reProviderCode.MatchString(invalidBody) {
		addRouter("service-inference (OpenRouter resale)", "high", "B1", "error code ERR_PROVIDER_* in Step 4b")
	}

	if strings.Contains(strings.ToLower(hugeBody), "context-compression plugin") {
		addRouter("OpenRouter (or downstream resale)", "high", "B2", "'context-compression plugin' wording in Step 4a body (OpenRouter-only)")
	}

	if reGenID.MatchString(plainBody) || reGenID.MatchString(streamBody) {
		addRouter("OpenRouter family", "medium", "B3", "id prefixed gen-")
	}
	if reChatCmpl.MatchString(plainBody) {
		addRouter("OpenAI-compat normalizer", "medium", "B3", "id prefixed chatcmpl-")
	}

	if reSSEDone.MatchString(streamBody) {
		cls.Signals = append(cls.Signals, detectSignal{
			Code: "B4", Tier: 2, Label: "SSE [DONE]", Detail: "stream terminates with `data: [DONE]` — OpenAI-compat normalizer, not Anthropic-native", Layer: "router", Implies: "non-Anthropic-native router",
		})
		if cls.RouterLabel == "unknown" {
			cls.RouterLabel = "OpenAI-compat normalizer"
			cls.RouterConfidence = "medium"
		}
	}

	// Router header hints (B5)
	for _, p := range probes {
		for k, v := range p.Headers {
			lk := strings.ToLower(k)
			lv := strings.ToLower(v)
			switch {
			case strings.HasPrefix(lk, "openrouter-") || strings.Contains(lv, "openrouter"):
				addRouter("OpenRouter", "medium", "B5", "header "+k+": "+v)
			case lk == "x-new-api-version":
				addRouter("new-api fork", "high", "B5", "header x-new-api-version: "+v)
			case lk == "x-amzn-requestid" || lk == "x-amz-cf-id":
				cls.Signals = append(cls.Signals, detectSignal{
					Code: "B5", Tier: 2, Label: "AWS edge header", Detail: "header " + k + ": " + v + " — Bedrock or AWS-fronted", Layer: "infra", Implies: "AWS edge",
				})
			}
		}
	}

	return cls
}

// ---- HTTP handlers ----

type detectRequest struct {
	URL        string `json:"url"`
	Key        string `json:"key"`
	Model      string `json:"model"`
	IntervalMs *int   `json:"interval_ms,omitempty"`
	MaxRetries *int   `json:"max_retries,omitempty"`
}

func handleDetectModels(c *gin.Context) {
	base := strings.TrimSpace(c.Query("url"))
	key := strings.TrimSpace(c.Query("key"))
	if base == "" || key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url and key are required"})
		return
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url must be http:// or https://"})
		return
	}
	probe := detectProbeGET(c.Request.Context(), base, key, "/v1/models",
		"GET /v1/models", "List models for the target endpoint", detectDefaultMaxRetries)
	c.JSON(http.StatusOK, gin.H{
		"status":     probe.Status,
		"headers":    probe.Headers,
		"body":       probe.Body,
		"elapsed_ms": probe.ElapsedMs,
		"retries":    probe.Retries,
	})
}

func handleDetectRun(c *gin.Context) {
	var req detectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	req.Key = strings.TrimSpace(req.Key)
	req.Model = strings.TrimSpace(req.Model)

	opts := detectOptions{
		IntervalMs: detectDefaultIntervalMs,
		MaxRetries: detectDefaultMaxRetries,
	}
	if req.IntervalMs != nil {
		opts.IntervalMs = *req.IntervalMs
	}
	if req.MaxRetries != nil {
		opts.MaxRetries = *req.MaxRetries
	}

	res, err := runDetect(req.URL, req.Key, req.Model, opts)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}
