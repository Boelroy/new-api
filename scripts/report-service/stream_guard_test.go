package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Only pairs with enough streams and a failure rate at or above the threshold
// are acted on, and a model failing on most of its channels is left alone
// because stripping it would take the model offline.
func TestStreamGuardVerdict(t *testing.T) {
	cases := []struct {
		name  string
		pairs []streamGuardPair
		want  []int64
	}{
		{"healthy", []streamGuardPair{{1, "m", 100, 1}}, nil},
		{"too few streams", []streamGuardPair{{1, "m", 9, 9}}, nil},
		{"too few failures", []streamGuardPair{{1, "m", 10, 2}}, nil},
		{"dying key", []streamGuardPair{{1, "m", 40, 10}, {2, "m", 40, 0}, {3, "m", 40, 0}}, []int64{1}},
		{"exactly at threshold", []streamGuardPair{{1, "m", 30, 3}}, []int64{1}},
		{"small dying key", []streamGuardPair{{1, "m", 10, 4}}, []int64{1}},
		{"two of many channels", []streamGuardPair{{1, "m", 40, 10}, {2, "m", 40, 10}, {3, "m", 40, 0}, {4, "m", 40, 0}, {5, "m", 40, 0}, {6, "m", 40, 0}}, []int64{1, 2}},
		{"upstream-wide", []streamGuardPair{{1, "m", 40, 10}, {2, "m", 40, 10}, {3, "m", 40, 10}, {4, "m", 40, 0}}, nil},
	}
	for _, tc := range cases {
		var got []int64
		for _, p := range streamGuardVerdict(streamGuardDefaults, tc.pairs) {
			got = append(got, p.channelID)
		}
		assert.ElementsMatch(t, tc.want, got, tc.name)
	}
}
