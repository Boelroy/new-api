package main

// Key-material scrubber for error messages and log lines.
//
// Upstream new-api / provider error bodies routinely echo the request key
// or credential header. If we persist those into rs_key_pool.failed_reason
// or send them to log.Printf, we leak plaintext through GET endpoints and
// operator terminals. sanitizeUpstreamMessage is the choke point.
//
// The regex list intentionally covers common providers we integrate with:
//   sk-<hex-ish>              — OpenAI / Anthropic style
//   Bearer <token>            — any bearer-authenticated upstream
//   AKIA[0-9A-Z]{16}          — AWS access key ID
//   AIza[0-9A-Za-z\-_]{35}    — Google API key
//   ya29\.[0-9A-Za-z\-_]+     — Google OAuth
// Add more patterns as we onboard new upstreams; every addition should
// come with a test-data fixture.

import (
	"regexp"
	"strings"
)

const sanitizeMaxLen = 512
const redactPlaceholder = "[REDACTED]"

// keyPatterns is the ordered list of regexes tried against every message
// passed through sanitizeUpstreamMessage. Longer/more-specific patterns
// come first so we don't over-match.
var keyPatterns = []*regexp.Regexp{
	// Bearer <opaque>
	regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9\-_\.]{20,}`),
	// OpenAI-style
	regexp.MustCompile(`sk-[A-Za-z0-9\-_]{20,}`),
	// Anthropic legacy (sk-ant-xxx)
	regexp.MustCompile(`sk-ant-[A-Za-z0-9\-_]{20,}`),
	// Google API key
	regexp.MustCompile(`AIza[0-9A-Za-z\-_]{35}`),
	// Google OAuth access token
	regexp.MustCompile(`ya29\.[0-9A-Za-z\-_]+`),
	// AWS Access Key ID
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	// Generic "Authorization: <opaque>"
	regexp.MustCompile(`(?i)Authorization:\s*[^\s\r\n]{20,}`),
	// x-api-key / x-goog-api-key style headers echoed in the body
	regexp.MustCompile(`(?i)(?:x-api-key|x-goog-api-key)[^\r\n]{0,10}[A-Za-z0-9\-_]{20,}`),
}

// sanitizeErr is the error-aware convenience wrapper. Nil returns "".
// Any error whose message could plausibly carry upstream body text (upload
// paths, encrypt/decrypt failures, scheduler bridge diagnostics) MUST go
// through this before it reaches the wire or an audit log.
func sanitizeErr(err error) string {
	if err == nil {
		return ""
	}
	return sanitizeUpstreamMessage(err.Error())
}

// sanitizeUpstreamMessage redacts any recognizable key material from msg
// and truncates the result to sanitizeMaxLen. Safe to call on any string
// that could hit rs_key_pool.failed_reason or the process log.
//
// The function is idempotent (running it twice yields the same output).
func sanitizeUpstreamMessage(msg string) string {
	if msg == "" {
		return ""
	}
	out := msg
	for _, re := range keyPatterns {
		out = re.ReplaceAllString(out, redactPlaceholder)
	}
	// Collapse redundant [REDACTED] runs a caller might have already added.
	for strings.Contains(out, redactPlaceholder+" "+redactPlaceholder) {
		out = strings.ReplaceAll(out, redactPlaceholder+" "+redactPlaceholder, redactPlaceholder)
	}
	if len(out) > sanitizeMaxLen {
		out = out[:sanitizeMaxLen-1] + "…"
	}
	return out
}
