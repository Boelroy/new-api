package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gpt-4o-mini token limits observed on real keys map onto OpenAI's published
// tiers; raised custom limits land on the highest tier they reach.
func TestOpenAITierFromTPM(t *testing.T) {
	cases := []struct {
		tpm  int64
		want int
	}{
		{0, -1},
		{40_000, 0},
		{200_000, 1},
		{2_000_000, 2},
		{4_000_000, 3},
		{10_000_000, 4},
		{150_000_000, 5},
		{12_000_000, 4},
		{30_000, -1},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, openAITierFromTPM(tc.tpm), "tpm=%d", tc.tpm)
	}
}

// A rejected key that still returns rate-limit headers keeps its tier; one
// without headers reports the upstream error code instead.
func TestProbeOpenAIKeyTier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		switch r.Header.Get("Authorization") {
		case "Bearer t4":
			w.Header().Set("x-ratelimit-limit-requests", "10000")
			w.Header().Set("x-ratelimit-limit-tokens", "10000000")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"bad key","code":"invalid_api_key"}}`))
		}
	}))
	defer srv.Close()

	ok := probeOpenAIKeyTier(t.Context(), "t4", srv.URL, "")
	require.NotNil(t, ok.Tier)
	assert.Equal(t, 4, *ok.Tier)
	assert.Equal(t, int64(10000), ok.LimitRequests)
	assert.Empty(t, ok.Error)

	bad := probeOpenAIKeyTier(t.Context(), "nope", srv.URL, "")
	assert.Nil(t, bad.Tier)
	assert.Equal(t, http.StatusUnauthorized, bad.HTTPStatus)
	assert.Equal(t, "HTTP 401 invalid_api_key", bad.Error)
}
