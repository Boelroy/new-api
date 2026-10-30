package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRateLimit(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		ok   bool
		want rateLimitInfo
	}{
		{
			name: "TPM with seconds retry",
			msg:  "status_code=429, Rate limit reached for gpt-6-astra in organization org-ed0Lf9qBb7pMj3bZu1Wd14wI on tokens per min (TPM): Limit 1000000, Used 992591, Requested 33004. Please try again in 1.535s. Visit https://platform.openai.com/account/rate-limits to learn more.",
			ok:   true,
			want: rateLimitInfo{Model: "gpt-6-astra", Unit: "TPM", Window: time.Minute, Limit: 1000000, Used: 992591, Requested: 33004, RetryAfter: 1535 * time.Millisecond},
		},
		{
			name: "TPM with millisecond retry",
			msg:  "Rate limit reached for gpt-6-astra in organization org-X on tokens per min (TPM): Limit 1000000, Used 1000000, Requested 748. Please try again in 44ms. Visit https://platform.openai.com/account/rate-limits to learn more.",
			ok:   true,
			want: rateLimitInfo{Model: "gpt-6-astra", Unit: "TPM", Window: time.Minute, Limit: 1000000, Used: 1000000, Requested: 748, RetryAfter: 44 * time.Millisecond},
		},
		{
			name: "RPM without retry hint",
			msg:  "Rate limit reached for gpt-6.1-sol in organization org-X on requests per min (RPM): Limit 500, Used 500, Requested 1.",
			ok:   true,
			want: rateLimitInfo{Model: "gpt-6.1-sol", Unit: "RPM", Window: time.Minute, Limit: 500, Used: 500, Requested: 1},
		},
		{
			name: "per-day limit with compound retry",
			msg:  "Rate limit reached for gpt-image-2 in organization org-X on tokens per day (TPD): Limit 90000, Used 89000, Requested 2000. Please try again in 1h6m40s.",
			ok:   true,
			want: rateLimitInfo{Model: "gpt-image-2", Unit: "TPD", Window: 24 * time.Hour, Limit: 90000, Used: 89000, Requested: 2000, RetryAfter: time.Hour + 6*time.Minute + 40*time.Second},
		},
		{
			name: "unit derived when parenthesis missing",
			msg:  "Rate limit reached for gpt-6-astra in organization org-X on tokens per min: Limit 10, Used 10, Requested 5.",
			ok:   true,
			want: rateLimitInfo{Model: "gpt-6-astra", Unit: "TPM", Window: time.Minute, Limit: 10, Used: 10, Requested: 5},
		},
		{name: "request too large is not a rate limit", msg: "status_code=429, Request too large for gpt-6-astra in organization org-X on tokens per min (TPM): Limit 1000000, Requested 1200000."},
		{name: "no credits", msg: "status_code=429, You have no credits remaining. Add credits to continue using the API."},
		{name: "terminated", msg: "status_code=429, Your access was terminated due to violation of our policies."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRateLimit(tc.msg)
			require.Equal(t, tc.ok, ok)
			if tc.ok {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestRateLimitCooldown(t *testing.T) {
	minCD, maxCD := time.Minute, 15*time.Minute
	tpm := rateLimitInfo{Unit: "TPM", Window: time.Minute, Limit: 1000000, Used: 1000000, Requested: 30000, RetryAfter: 1800 * time.Millisecond}
	cases := []struct {
		name  string
		info  rateLimitInfo
		trips int
		minCD time.Duration
		want  time.Duration
	}{
		// 30k over a 1M/min limit drains in 1.8s on top of the 60s window.
		{name: "first trip is window plus overflow drain", info: tpm, trips: 1, minCD: minCD, want: 61800 * time.Millisecond},
		{name: "second trip doubles", info: tpm, trips: 2, minCD: minCD, want: 123600 * time.Millisecond},
		{name: "escalation is capped", info: tpm, trips: 10, minCD: minCD, want: maxCD},
		{name: "min cooldown floor wins", info: tpm, trips: 1, minCD: 5 * time.Minute, want: 5 * time.Minute},
		{name: "unparsed message falls back to one window", info: rateLimitInfo{}, trips: 1, minCD: 30 * time.Second, want: time.Minute},
		{
			name:  "long retry-after beats the window",
			info:  rateLimitInfo{Window: time.Minute, Limit: 100, Used: 100, Requested: 1, RetryAfter: 3 * time.Minute},
			trips: 1, minCD: minCD, want: 3 * time.Minute,
		},
		{
			name:  "per-day limit waits at least an hour and may exceed max cooldown",
			info:  rateLimitInfo{Window: 24 * time.Hour, Limit: 90000, Used: 90000, Requested: 10, RetryAfter: 20 * time.Minute},
			trips: 1, minCD: minCD, want: time.Hour,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, rateLimitCooldown(tc.info, tc.trips, tc.minCD, maxCD))
		})
	}
}

func TestNextTrips(t *testing.T) {
	const now, escalate = int64(10_000), 1800
	assert.Equal(t, 1, nextTrips(0, 0, now, escalate), "first trip ever")
	assert.Equal(t, 3, nextTrips(2, now-600, now, escalate), "re-trip inside the escalate window")
	assert.Equal(t, 1, nextTrips(4, now-3600, now, escalate), "streak resets after the window")
	assert.Equal(t, 1, nextTrips(4, now-10, now, 0), "escalation disabled")
}
