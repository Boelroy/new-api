package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Suppliers paste whatever the callback page shows; every shape must yield
// the bare code plus any embedded state.
func TestCCMaxParseCode(t *testing.T) {
	cases := []struct {
		name, raw, code, state string
	}{
		{"bare code", "  abc123  ", "abc123", ""},
		{"code#state", "abc123#st1", "abc123", "st1"},
		{"callback query", "http://localhost:54545/callback?code=abc123&state=st1", "abc123", "st1"},
		{"callback fragment", "http://localhost:54545/callback#code=abc123&state=st1", "abc123", "st1"},
		{"query string", "?code=abc123&state=st1", "abc123", "st1"},
		{"code param carrying #state", "https://x/cb?code=abc123%23st2", "abc123", "st2"},
		{"empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, state := ccmaxParseCode(tc.raw)
			assert.Equal(t, tc.code, code)
			assert.Equal(t, tc.state, state)
		})
	}
}
