package main

import (
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyProbe(t *testing.T) {
	cases := []struct {
		name       string
		httpStatus int
		success    bool
		message    string
		errorCode  string
		transport  error
		want       probeClass
	}{
		{
			name:      "transport failure never blames upstream",
			transport: errors.New("dial tcp: connection refused"),
			want:      probeNeutral,
		},
		{
			name:       "non-200 from our own gateway is neutral",
			httpStatus: 401,
			message:    "无权进行此操作，未登录且未提供 access token",
			want:       probeNeutral,
		},
		{
			name:       "success",
			httpStatus: 200,
			success:    true,
			want:       probeOK,
		},
		{
			name:       "channel type new-api cannot test",
			httpStatus: 200,
			message:    "Midjourney channel test is not supported",
			want:       probeUnsupported,
		},
		{
			name:       "invalid api type is not a model verdict",
			httpStatus: 200,
			message:    "invalid api type: 43, adaptor is nil",
			errorCode:  "invalid_api_type",
			want:       probeUnsupported,
		},
		{
			name:       "our own test user is out of quota",
			httpStatus: 200,
			message:    "insufficient_user_quota",
			errorCode:  "insufficient_user_quota",
			want:       probeNeutral,
		},
		{
			name:       "upstream 429 embedded in the relay message",
			httpStatus: 200,
			message:    "bad response status code 429, message: rate_limit_error, body: {...}",
			errorCode:  "bad_response",
			want:       probeThrottle,
		},
		{
			name:       "overloaded",
			httpStatus: 200,
			message:    "Overloaded",
			want:       probeThrottle,
		},
		{
			name:       "revoked key is channel-wide",
			httpStatus: 200,
			message:    "bad response status code 401, message: invalid x-api-key, body: {...}",
			errorCode:  "bad_response",
			want:       probeChannelDown,
		},
		{
			name:       "no available key is channel-wide",
			httpStatus: 200,
			message:    "channel has no available key",
			errorCode:  "channel:no_available_key",
			want:       probeChannelDown,
		},
		{
			name:       "credit exhaustion is channel-wide",
			httpStatus: 200,
			message:    "Your credit balance is too low to access the Anthropic API",
			want:       probeChannelDown,
		},
		{
			name:       "model not found is model-scope",
			httpStatus: 200,
			message:    "bad response status code 404, message: model_not_found, body: {...}",
			errorCode:  "model_not_found",
			want:       probeModelDown,
		},
		{
			// The catch-all matters: an unrecognized error must still be able
			// to demote, or a broken model would sit unknown forever.
			name:       "unrecognized failure falls through to model down",
			httpStatus: 200,
			message:    "something nobody has seen before",
			want:       probeModelDown,
		},
		{
			name:       "empty failure message still counts",
			httpStatus: 200,
			success:    false,
			want:       probeModelDown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyProbe(tc.httpStatus, tc.success, tc.message, tc.errorCode, tc.transport)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestApplyProbe(t *testing.T) {
	th := healthThresholds{DownWindowSec: 1800, DownFailMin: 3, RecoverOKMin: 2}
	now := time.Unix(1_700_000_000, 0)

	t.Run("freshly seeded row does not demote on its first failure", func(t *testing.T) {
		// Rows are seeded with last_ok_at = now precisely so the window
		// clause can't be satisfied immediately. A zero here would demote the
		// entire fleet on the first tick after a rule is enabled.
		h := healthRow{State: healthStateUnknown, LastOKAt: now.Unix()}
		got := applyProbe(h, probeResult{Class: probeModelDown}, th, now)
		assert.Equal(t, healthStateUnknown, got.State)
		assert.Equal(t, 1, got.ConsecutiveFail)
	})

	t.Run("window elapsed but streak too short stays up", func(t *testing.T) {
		h := healthRow{State: healthStateUp, LastOKAt: now.Unix() - 3600, ConsecutiveFail: 1}
		got := applyProbe(h, probeResult{Class: probeModelDown}, th, now)
		assert.Equal(t, healthStateUp, got.State)
		assert.Equal(t, 2, got.ConsecutiveFail)
	})

	t.Run("streak reached but window not elapsed stays up", func(t *testing.T) {
		h := healthRow{State: healthStateUp, LastOKAt: now.Unix() - 60, ConsecutiveFail: 5}
		got := applyProbe(h, probeResult{Class: probeModelDown}, th, now)
		assert.Equal(t, healthStateUp, got.State)
	})

	t.Run("both conditions met demotes", func(t *testing.T) {
		h := healthRow{State: healthStateUp, LastOKAt: now.Unix() - 1900, ConsecutiveFail: 2}
		got := applyProbe(h, probeResult{Class: probeModelDown}, th, now)
		assert.Equal(t, healthStateDown, got.State)
		assert.Equal(t, 3, got.ConsecutiveFail)
	})

	t.Run("throttle leaves both streaks alone", func(t *testing.T) {
		h := healthRow{State: healthStateUp, LastOKAt: now.Unix() - 100, ConsecutiveFail: 2, ConsecutiveOK: 4}
		got := applyProbe(h, probeResult{Class: probeThrottle}, th, now)
		assert.Equal(t, healthStateUp, got.State)
		assert.Equal(t, 2, got.ConsecutiveFail)
		assert.Equal(t, 4, got.ConsecutiveOK)
	})

	t.Run("permanent throttle still converges via the stale backstop", func(t *testing.T) {
		// Without this rule a model that 429s forever accumulates no failures
		// and would never demote, however long it stays unusable.
		h := healthRow{State: healthStateUp, LastOKAt: now.Unix() - 3*1800 - 1}
		got := applyProbe(h, probeResult{Class: probeThrottle}, th, now)
		assert.Equal(t, healthStateDown, got.State)
	})

	t.Run("neutral moves nothing", func(t *testing.T) {
		h := healthRow{
			State: healthStateUp, LastOKAt: now.Unix() - 5000,
			ConsecutiveFail: 9, ConsecutiveOK: 1, LastClass: string(probeOK),
		}
		got := applyProbe(h, probeResult{Class: probeNeutral}, th, now)
		assert.Equal(t, healthStateUp, got.State)
		assert.Equal(t, int64(now.Unix()-5000), got.LastOKAt)
		assert.Equal(t, 9, got.ConsecutiveFail)
		assert.Equal(t, 1, got.ConsecutiveOK)
	})

	t.Run("one success is not enough to recover", func(t *testing.T) {
		h := healthRow{State: healthStateDown, ConsecutiveFail: 5}
		got := applyProbe(h, probeResult{Class: probeOK}, th, now)
		assert.Equal(t, healthStateDown, got.State)
		assert.Equal(t, 1, got.ConsecutiveOK)
		assert.Equal(t, 0, got.ConsecutiveFail)
		assert.Equal(t, now.Unix(), got.LastOKAt)

		got = applyProbe(got, probeResult{Class: probeOK}, th, now)
		assert.Equal(t, healthStateUp, got.State)
	})

	t.Run("unsupported is sticky and never demotes", func(t *testing.T) {
		h := healthRow{State: healthStateUp, LastOKAt: now.Unix() - 100000}
		got := applyProbe(h, probeResult{Class: probeUnsupported}, th, now)
		require.Equal(t, healthStateUnsupported, got.State)

		got = applyProbe(got, probeResult{Class: probeModelDown}, th, now)
		assert.Equal(t, healthStateUnsupported, got.State)
	})
}

func TestDesiredChannelModels(t *testing.T) {
	candidates := []string{"claude-sonnet-5", "claude-opus-4-8"}

	t.Run("models outside the candidate set are never removed", func(t *testing.T) {
		// A rule only owns the models it was configured to manage. Stripping
		// anything else would silently break traffic no rule opted into.
		got := desiredChannelModels(
			[]string{"claude-sonnet-5", "gpt-4o", "claude-opus-4-8"},
			candidates,
			map[string]string{
				"claude-sonnet-5": healthStateDown,
				"claude-opus-4-8": healthStateDown,
				"gpt-4o":          healthStateDown,
			},
		)
		assert.Equal(t, []string{"gpt-4o"}, got)
	})

	t.Run("down candidate is dropped, others keep their position", func(t *testing.T) {
		got := desiredChannelModels(
			[]string{"claude-sonnet-5", "claude-opus-4-8"},
			candidates,
			map[string]string{
				"claude-sonnet-5": healthStateUp,
				"claude-opus-4-8": healthStateDown,
			},
		)
		assert.Equal(t, []string{"claude-sonnet-5"}, got)
	})

	t.Run("recovered candidate is appended", func(t *testing.T) {
		got := desiredChannelModels(
			[]string{"gpt-4o"},
			candidates,
			map[string]string{
				"claude-sonnet-5": healthStateUp,
				"claude-opus-4-8": healthStateDown,
			},
		)
		assert.Equal(t, []string{"gpt-4o", "claude-sonnet-5"}, got)
	})

	t.Run("unknown state is kept, not stripped", func(t *testing.T) {
		got := desiredChannelModels([]string{"claude-sonnet-5"}, candidates, map[string]string{})
		assert.Equal(t, []string{"claude-sonnet-5"}, got)
	})

	t.Run("no change produces an identical list so the caller skips the PUT", func(t *testing.T) {
		current := []string{"claude-sonnet-5", "claude-opus-4-8"}
		got := desiredChannelModels(current, candidates, map[string]string{
			"claude-sonnet-5": healthStateUp,
			"claude-opus-4-8": healthStateUp,
		})
		assert.Equal(t, csvJoinModels(current), csvJoinModels(got))
	})

	t.Run("duplicates in current collapse", func(t *testing.T) {
		got := desiredChannelModels(
			[]string{"claude-sonnet-5", "claude-sonnet-5"},
			candidates,
			map[string]string{"claude-sonnet-5": healthStateUp},
		)
		assert.Equal(t, []string{"claude-sonnet-5"}, got)
	})

	t.Run("all candidates down leaves an empty list when nothing else is configured", func(t *testing.T) {
		got := desiredChannelModels(candidates, candidates, map[string]string{
			"claude-sonnet-5": healthStateDown,
			"claude-opus-4-8": healthStateDown,
		})
		assert.Empty(t, got)
	})
}

func TestCSVModelsRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "empty yields an empty slice, not one empty entry", input: "", want: []string{}},
		{name: "only separators", input: ",,,", want: []string{}},
		{name: "trailing comma", input: "a,b,", want: []string{"a", "b"}},
		{name: "surrounding whitespace", input: " a , b ,, c ", want: []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := csvSplitModels(tc.input)
			assert.Equal(t, tc.want, got)
			// Round-trip must be stable: the reconciler compares joined CSVs
			// to decide whether a PUT is needed at all.
			assert.Equal(t, got, csvSplitModels(csvJoinModels(got)))
		})
	}
}

func TestNextCheckAt(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	t.Run("never schedules into the past", func(t *testing.T) {
		// A past timestamp would make the due-queue re-select the row on the
		// very next tick, spinning billed probes.
		r := rand.New(rand.NewSource(1))
		for i := 0; i < 1000; i++ {
			got := nextCheckAt(now, time.Second, 20, r)
			assert.Greater(t, got, now.Unix())
		}
	})

	t.Run("stays within the jitter band", func(t *testing.T) {
		r := rand.New(rand.NewSource(7))
		for i := 0; i < 1000; i++ {
			got := nextCheckAt(now, 3600*time.Second, 20, r) - now.Unix()
			assert.GreaterOrEqual(t, got, int64(2880))
			assert.LessOrEqual(t, got, int64(4320))
		}
	})

	t.Run("no jitter source yields the exact interval", func(t *testing.T) {
		assert.Equal(t, now.Unix()+1800, nextCheckAt(now, 1800*time.Second, 20, nil))
	})

	t.Run("deterministic for a fixed seed", func(t *testing.T) {
		a := nextCheckAt(now, 600*time.Second, 20, rand.New(rand.NewSource(42)))
		b := nextCheckAt(now, 600*time.Second, 20, rand.New(rand.NewSource(42)))
		assert.Equal(t, a, b)
	})
}
