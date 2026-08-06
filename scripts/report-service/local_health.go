package main

// Local model health: pure decision logic.
//
// Nothing in this file touches the database, the network, or the clock —
// every input is an argument. That's deliberate: classifying a probe wrong
// silently deletes models from production channels, and the 30-minute
// demotion rule is trivially easy to get off by one. Both live here so they
// can be table-tested directly (local_health_test.go).
//
// The scheduler that calls into this file is local_health_loop.go; the
// new-api admin API client is local_newapi.go.

import (
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// probeClass is the verdict for one (channel, model) test call.
type probeClass string

const (
	// probeOK — the model answered.
	probeOK probeClass = "ok"
	// probeModelDown — this specific model is unusable on this channel:
	// not found, not entitled, blocked by content policy. Also the catch-all
	// for anything we can't recognize, so an unknown upstream error still
	// eventually demotes rather than being silently ignored.
	probeModelDown probeClass = "model_down"
	// probeChannelDown — the credential or endpoint is broken, so every model
	// on the channel is equally dead. Fanned out by the caller to spare N-1
	// billed probes on a channel whose key is simply revoked.
	probeChannelDown probeClass = "channel_down"
	// probeThrottle — rate limited / overloaded. Neutral for the streak
	// counters, but the last_ok_at clock keeps running so a permanently
	// throttled model still converges to down via the backstop rule.
	probeThrottle probeClass = "throttle"
	// probeUnsupported — new-api itself refuses to test this channel type.
	// Parks the row forever; must never demote, or every Midjourney/Suno
	// channel gets stripped 30 minutes after a rule is enabled.
	probeUnsupported probeClass = "unsupported"
	// probeNeutral — our own call to the local new-api failed (transport
	// error, non-200, bad token). Says nothing about the upstream model, so
	// it must not move any counter.
	probeNeutral probeClass = "neutral"
)

// probeResult is one test call's outcome, already sanitized for storage.
type probeResult struct {
	Class     probeClass
	Message   string
	ErrorCode string
	Seconds   float64
	HTTPCode  int
}

// healthState values for local_model_health.state.
const (
	healthStateUnknown     = "unknown"
	healthStateUp          = "up"
	healthStateDown        = "down"
	healthStateUnsupported = "unsupported"
)

// healthThresholds are the per-rule knobs that drive applyProbe.
type healthThresholds struct {
	DownWindowSec int
	DownFailMin   int
	RecoverOKMin  int
}

// healthRow mirrors one local_model_health row.
type healthRow struct {
	ChannelID       int64
	Model           string
	RuleID          int64
	State           string
	ConsecutiveOK   int
	ConsecutiveFail int
	LastOKAt        int64
	LastCheckedAt   int64
	NextCheckAt     int64
	LastClass       string
	LastError       string
	LastLatencyMS   int64
}

// upstreamStatusRe pulls the real upstream status code out of the message
// new-api builds in service.RelayErrorHandler ("bad response status code 429,
// message: ..., body: ..."). The channel-test endpoint always answers HTTP 200
// and reports failures through error_code=bad_response, so this embedded code
// is the only place the upstream's own status survives.
var upstreamStatusRe = regexp.MustCompile(`status code (\d{3})`)

// classifyProbe maps one channel-test response onto a probeClass.
//
// httpStatus/transportErr describe our hop to the local new-api; success,
// message and errorCode come from its JSON body. Ordering matters: the
// "unsupported" phrases are substrings of broader patterns checked later.
func classifyProbe(httpStatus int, success bool, message, errorCode string, transportErr error) probeClass {
	if transportErr != nil {
		return probeNeutral
	}
	// Anything but 200 means new-api didn't even get to run the test — a bad
	// MAIN_SERVICE_TOKEN, a proxy in the way, a restart mid-flight.
	if httpStatus != 200 {
		return probeNeutral
	}
	if success {
		return probeOK
	}

	msg := strings.ToLower(message)
	code := strings.ToLower(strings.TrimSpace(errorCode))

	// new-api can't test this channel type at all (controller/channel-test.go
	// unsupportedTestChannelTypes) or can't build a request for it.
	if code == "invalid_api_type" ||
		strings.Contains(msg, "channel test is not supported") ||
		strings.Contains(msg, "invalid api type") ||
		strings.Contains(msg, "invalid general request type") {
		return probeUnsupported
	}

	// Our own side ran out of quota for the test user — says nothing about
	// the channel. Keep it neutral so a misconfigured root user can't demote
	// the whole fleet.
	if code == "insufficient_user_quota" || code == "pre_consume_token_quota_failed" ||
		strings.Contains(msg, "insufficient_user_quota") {
		return probeNeutral
	}

	upstreamStatus := 0
	if m := upstreamStatusRe.FindStringSubmatch(msg); len(m) == 2 {
		upstreamStatus, _ = strconv.Atoi(m[1])
	}

	if upstreamStatus == 429 ||
		strings.Contains(msg, "rate limit") || strings.Contains(msg, "rate_limit") ||
		strings.Contains(msg, "too many requests") || strings.Contains(msg, "overloaded") ||
		strings.Contains(msg, "resource_exhausted") || strings.Contains(msg, "resource has been exhausted") {
		return probeThrottle
	}

	if upstreamStatus == 401 || upstreamStatus == 403 ||
		code == "channel:no_available_key" || code == "channel:invalid_key" ||
		code == "get_channel_failed" || code == "channel:aws_client_error" ||
		strings.Contains(msg, "invalid api key") || strings.Contains(msg, "invalid_api_key") ||
		strings.Contains(msg, "invalid x-api-key") ||
		strings.Contains(msg, "authentication") || strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "permission denied") || strings.Contains(msg, "permission_denied") ||
		strings.Contains(msg, "no available key") ||
		strings.Contains(msg, "insufficient_quota") || strings.Contains(msg, "insufficient quota") ||
		strings.Contains(msg, "credit balance") || strings.Contains(msg, "billing") ||
		strings.Contains(msg, "account is not active") || strings.Contains(msg, "account has been disabled") {
		return probeChannelDown
	}

	// Everything else — including messages we don't recognize — is treated as
	// a model-level failure. Fail loud rather than accumulating a channel that
	// silently never demotes.
	return probeModelDown
}

// applyProbe advances one health row by a single probe result.
//
// The demotion clock is last_ok_at alone. There is deliberately no
// first_fail_at: with throttle neutral for the streak counters, a permanently
// 429ing model would otherwise never accumulate failures and never demote.
func applyProbe(h healthRow, res probeResult, th healthThresholds, now time.Time) healthRow {
	ts := now.Unix()
	h.LastCheckedAt = ts
	h.LastClass = string(res.Class)
	h.LastError = res.Message
	h.LastLatencyMS = int64(res.Seconds * 1000)

	switch res.Class {
	case probeNeutral:
		// Our hop failed; the row learns nothing.
		h.LastError = res.Message
		return h

	case probeUnsupported:
		h.State = healthStateUnsupported
		h.ConsecutiveOK = 0
		h.ConsecutiveFail = 0
		return h

	case probeOK:
		h.LastOKAt = ts
		h.ConsecutiveFail = 0
		h.ConsecutiveOK++
		if h.State != healthStateUp && h.ConsecutiveOK >= th.RecoverOKMin {
			h.State = healthStateUp
		}
		return h

	case probeThrottle:
		// Neither streak moves, but last_ok_at keeps aging — see the backstop
		// below.

	default: // probeModelDown, probeChannelDown
		h.ConsecutiveOK = 0
		h.ConsecutiveFail++
	}

	if h.State == healthStateUnsupported {
		// Sticky: a channel type new-api can't test doesn't become testable
		// because of one odd response.
		return h
	}

	sinceOK := ts - h.LastOKAt
	hardDown := h.ConsecutiveFail >= th.DownFailMin && sinceOK >= int64(th.DownWindowSec)
	// Backstop: no successful probe for 3x the window and the last verdict
	// wasn't ok. Catches the permanent-throttle case that the streak rule
	// alone can never reach.
	staleDown := sinceOK >= 3*int64(th.DownWindowSec) && h.LastClass != string(probeOK)
	if hardDown || staleDown {
		h.State = healthStateDown
	}
	return h
}

// csvSplitModels parses new-api's comma-separated models column. Returns an
// empty slice (never []string{""}) for blank input.
func csvSplitModels(s string) []string {
	out := make([]string, 0, 8)
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// csvJoinModels renders a model list back into the column format.
func csvJoinModels(models []string) string {
	return strings.Join(models, ",")
}

// desiredChannelModels computes the model list a channel should carry.
//
// Two invariants the callers depend on:
//   - a model outside `candidates` is never removed, whatever its state.
//     The rule only owns the models it was configured to manage.
//   - the result is order-stable: existing models keep their position and
//     recovered ones append, so an unchanged channel produces a byte-identical
//     CSV and the caller skips the PUT.
//
// state maps model name to one of the healthState* constants; a model missing
// from the map counts as unknown, which is kept (we only strip on proof).
func desiredChannelModels(current, candidates []string, state map[string]string) []string {
	isCandidate := make(map[string]bool, len(candidates))
	for _, m := range candidates {
		isCandidate[m] = true
	}

	out := make([]string, 0, len(current)+len(candidates))
	seen := make(map[string]bool, len(current)+len(candidates))
	for _, m := range current {
		if seen[m] {
			continue
		}
		if isCandidate[m] && state[m] == healthStateDown {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	for _, m := range candidates {
		if seen[m] || state[m] != healthStateUp {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// nextCheckAt returns the unix timestamp for the next probe of a row, with
// +/- jitterPct spread so a fleet restarted together doesn't re-synchronize.
// Always at least one second in the future — a past timestamp would make the
// due-queue re-select the row immediately and spin.
func nextCheckAt(now time.Time, base time.Duration, jitterPct int, r *rand.Rand) int64 {
	seconds := int64(base / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	if jitterPct > 0 && r != nil {
		spread := seconds * int64(jitterPct) / 100
		if spread > 0 {
			seconds += r.Int63n(2*spread+1) - spread
		}
	}
	if seconds < 1 {
		seconds = 1
	}
	return now.Unix() + seconds
}
