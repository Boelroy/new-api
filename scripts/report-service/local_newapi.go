package main

// Client for the LOCAL new-api's admin HTTP API.
//
// report-service already shares a Postgres with new-api and inserts channels
// straight into the table (local_pool.go). Health reconciliation deliberately
// does NOT do that: changing channels.models requires rebuilding the
// (group, model, channel_id) rows in `abilities` — which channels."group"
// makes non-trivial because it can be a comma-separated list — and then
// invalidating new-api's in-memory routing cache. Going through
// PUT /api/channel/ gets Channel.Update -> UpdateAbilities and
// InitChannelCache() for free and correct. Probing likewise reuses new-api's
// full adaptor stack, so Azure / Gemini / AWS SigV4 / Vertex JWT request
// construction is not our problem.
//
// Requires MAIN_SERVICE_TOKEN — an access token on the local new-api holding
// ChannelRead / ChannelWrite / ChannelOperate — alongside the existing
// MAIN_SERVICE_URL and MAIN_SERVICE_USER_ID.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// mainServiceToken is read in main() alongside mainServiceURL/mainServiceUID.
var mainServiceToken string

// localStatusEndpointMissing caches the one-time discovery that this new-api
// predates POST /api/channel/:id/status (added in 4aee5f7d5). 0 = unknown,
// 1 = missing, so we go straight to the legacy PUT path.
var localStatusEndpointMissing atomic.Int32

// localNewAPIClient is shared: probes are long-lived and concurrent, and a
// fresh client per call would leak connections against the local gateway.
var localNewAPIClient = &http.Client{}

func localNewAPIConfigured() bool {
	return mainServiceURL != "" && mainServiceToken != ""
}

// localNewAPIDo performs one authenticated call against the local new-api.
// Returns the raw status and body: the channel-test endpoint signals failure
// with HTTP 200 + success:false and carries no `data` key on success, so the
// standard {success,message,data} envelope unwrapping is unusable here.
func localNewAPIDo(ctx context.Context, method, path string, query url.Values, body any,
	timeout time.Duration) (int, []byte, error) {
	if !localNewAPIConfigured() {
		return 0, nil, fmt.Errorf("MAIN_SERVICE_URL / MAIN_SERVICE_TOKEN not configured")
	}
	endpoint := mainServiceURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal body: %v", err)
		}
		reader = bytes.NewReader(buf)
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, method, endpoint, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", mainServiceToken)
	req.Header.Set("New-Api-User", mainServiceUID)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := localNewAPIClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("http: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read body: %v", err)
	}
	return resp.StatusCode, raw, nil
}

// localEnvelope is new-api's standard admin response shape.
type localEnvelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// patchLocalChannelModels rewrites one channel's models CSV.
//
// The body carries only id + models on purpose. new-api's UpdateChannel
// rejects any request containing a `status` key outright (controller/channel.go
// isManageableChannelStatus guards status changes to a separate endpoint), so
// the GET-modify-PUT-whole-object pattern used elsewhere in this service is a
// guaranteed 400 against current new-api. Both `id` and `models` are in
// channelNonSensitiveFields, so this needs only ChannelWrite and doesn't trip
// the fail-closed unknown-field scan. GORM's struct Updates skips zero values,
// which is why every other column survives untouched — and also why models can
// never be cleared to "" this way (channel disable is a status flip instead).
func patchLocalChannelModels(ctx context.Context, id int64, models string) error {
	if strings.TrimSpace(models) == "" {
		return fmt.Errorf("refusing to write an empty models list (disable the channel instead)")
	}
	status, raw, err := localNewAPIDo(ctx, http.MethodPut, "/api/channel/", nil,
		map[string]any{"id": id, "models": models}, 30*time.Second)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("new-api returned %d: %s", status, snippet(raw))
	}
	var env localEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decode envelope: %v", err)
	}
	if !env.Success {
		return fmt.Errorf("new-api: %s", env.Message)
	}
	return nil
}

// setLocalChannelStatus enables (1) or disables (2) a channel.
//
// We never write status=3: it has no HTTP path, and new-api's own
// ShouldEnableChannel re-enables status=3 channels on its next automatic test
// run, which would fight this loop forever. status=2 is inert to that
// automation. The ledger table records which disables were ours.
func setLocalChannelStatus(ctx context.Context, id int64, chStatus int) error {
	if chStatus != 1 && chStatus != 2 {
		return fmt.Errorf("unsupported channel status %d", chStatus)
	}
	if localStatusEndpointMissing.Load() == 0 {
		httpStatus, raw, err := localNewAPIDo(ctx, http.MethodPost,
			"/api/channel/"+strconv.FormatInt(id, 10)+"/status", nil,
			map[string]any{"status": chStatus}, 20*time.Second)
		if err != nil {
			return err
		}
		if httpStatus != http.StatusNotFound {
			if httpStatus != http.StatusOK {
				return fmt.Errorf("new-api returned %d: %s", httpStatus, snippet(raw))
			}
			var env localEnvelope
			if err := json.Unmarshal(raw, &env); err != nil {
				return fmt.Errorf("decode envelope: %v", err)
			}
			if !env.Success {
				return fmt.Errorf("new-api: %s", env.Message)
			}
			return nil
		}
		// Pre-4aee5f7d5 new-api: no dedicated status endpoint, and no guard
		// against status in the PUT body either.
		localStatusEndpointMissing.Store(1)
	}

	httpStatus, raw, err := localNewAPIDo(ctx, http.MethodPut, "/api/channel/", nil,
		map[string]any{"id": id, "status": chStatus}, 20*time.Second)
	if err != nil {
		return err
	}
	if httpStatus != http.StatusOK {
		return fmt.Errorf("new-api returned %d: %s", httpStatus, snippet(raw))
	}
	var env localEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decode envelope: %v", err)
	}
	if !env.Success {
		return fmt.Errorf("new-api: %s", env.Message)
	}
	return nil
}

// channelTestResponse is what GET /api/channel/test/:id answers. Always HTTP
// 200; `success` carries the verdict and error_code is only present on failure.
type channelTestResponse struct {
	Success   bool    `json:"success"`
	Message   string  `json:"message"`
	Time      float64 `json:"time"`
	ErrorCode string  `json:"error_code"`
}

// testLocalChannelModel probes one model on one channel.
//
// new-api tests the channel regardless of its status and regardless of whether
// the model is in its models list, which is what makes automatic recovery
// possible: a model we already stripped, on a channel we already disabled, can
// still be re-probed and brought back.
//
// Each call is a real billed upstream request and writes a logs row with
// token_name='模型测试'. The report aggregations exclude that token name; keep
// them in sync if a new aggregation is added.
func testLocalChannelModel(ctx context.Context, id int64, model string, timeout time.Duration) probeResult {
	q := url.Values{}
	q.Set("model", model)
	started := time.Now()
	httpStatus, raw, err := localNewAPIDo(ctx, http.MethodGet,
		"/api/channel/test/"+strconv.FormatInt(id, 10), q, nil, timeout)
	elapsed := time.Since(started).Seconds()

	if err != nil {
		return probeResult{
			Class:    probeNeutral,
			Message:  sanitizeUpstreamMessage(err.Error()),
			Seconds:  elapsed,
			HTTPCode: httpStatus,
		}
	}
	var body channelTestResponse
	if httpStatus == http.StatusOK {
		if jsonErr := json.Unmarshal(raw, &body); jsonErr != nil {
			return probeResult{
				Class:    probeNeutral,
				Message:  sanitizeUpstreamMessage("decode test response: " + jsonErr.Error()),
				Seconds:  elapsed,
				HTTPCode: httpStatus,
			}
		}
	} else {
		body.Message = snippet(raw)
	}

	class := classifyProbe(httpStatus, body.Success, body.Message, body.ErrorCode, nil)
	seconds := body.Time
	if seconds <= 0 {
		seconds = elapsed
	}
	return probeResult{
		Class:     class,
		Message:   sanitizeUpstreamMessage(body.Message),
		ErrorCode: body.ErrorCode,
		Seconds:   seconds,
		HTTPCode:  httpStatus,
	}
}

func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
