package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A studio override replaces the type default for that studio only; other
// studios fall back to the default, and unconfigured types stay unrestricted.
func TestAllowedBatchGroups(t *testing.T) {
	limits := map[int]batchGroupRule{
		1: {
			Default: []string{"openai", "openai-alice"},
			Studios: map[string][]string{"alice": {"openai-alice"}},
		},
		24: {Studios: map[string][]string{"alice": {"gemini-alice"}}},
	}
	cases := []struct {
		name        string
		channelType int
		studio      string
		want        []string
	}{
		{"studio override", 1, "alice", []string{"openai-alice"}},
		{"other studio uses default", 1, "bob", []string{"openai", "openai-alice"}},
		{"override without default", 24, "alice", []string{"gemini-alice"}},
		{"no default means unrestricted", 24, "bob", nil},
		{"unconfigured type", 14, "alice", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, allowedBatchGroups(limits, tc.channelType, tc.studio))
		})
	}
}
