package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A model alerts only when the last window clears the floors and its rate is
// at least RisePct% of the baseline rate; no baseline hits counts as a rise.
func TestRiseVerdict(t *testing.T) {
	rule := riseRule{MinTotal: 50, MinHits: 10, MinRatePct: 3, RisePct: 200}
	cases := []struct {
		name string
		row  riseRow
		want bool
	}{
		{"doubled", riseRow{"m", 500, 50, 3000, 150}, true},      // 10% vs 5%
		{"not doubled", riseRow{"m", 500, 40, 3000, 150}, false}, // 8% vs 5%
		{"no baseline hits", riseRow{"m", 200, 10, 3000, 0}, true},
		{"no baseline traffic", riseRow{"m", 200, 10, 0, 0}, true},
		{"too few hits", riseRow{"m", 100, 9, 3000, 0}, false},
		{"too little traffic", riseRow{"m", 49, 49, 3000, 0}, false},
		{"rate under floor", riseRow{"m", 1000, 20, 3000, 1}, false}, // 2% < 3%
	}
	for _, tc := range cases {
		got := riseVerdict(rule, []riseRow{tc.row})
		assert.Equal(t, tc.want, len(got) == 1, tc.name)
	}
}

// Many uploads or priority edits by one actor collapse into one line, with
// the breakdown largest first.
func TestOpsLineAggCompresses(t *testing.T) {
	var agg opsLineAgg
	agg.add("alice 本地 OpenAI", 1, "T4", 1)
	agg.add("bob 远程 p1 Anthropic", 5, "", 0)
	agg.add("alice 本地 OpenAI", 1, "T5", 1)
	agg.add("alice 本地 OpenAI", 1, "T4", 1)
	agg.add("alice 本地 OpenAI", 1, "未测", 1)
	assert.Equal(t, []string{
		"· alice 本地 OpenAI 4 个：T4×2 T5×1 未测×1",
		"· bob 远程 p1 Anthropic 5 个",
	}, agg.render("个"))
}

// RPM alerts on a drop to half or a doubling, and only when the busier side
// carries at least RPMMinBase requests per minute.
func TestRPMVerdict(t *testing.T) {
	cfg := opsNotifyDefaults // 10 min window + 10 min baseline, -50% / 200%, min 30 rpm
	cases := []struct {
		name string
		row  rpmRow
		want string
	}{
		{"steady", rpmRow{Model: "m", CurReq: 1000, BaseReq: 1100}, ""},
		{"halved", rpmRow{Model: "m", CurReq: 500, BaseReq: 1000}, "down"},
		{"stopped", rpmRow{Model: "m", CurReq: 0, BaseReq: 1000}, "down"},
		{"small model halved", rpmRow{Model: "m", CurReq: 100, BaseReq: 290}, ""},
		{"doubled", rpmRow{Model: "m", CurReq: 2000, BaseReq: 1000}, "up"},
		{"new model", rpmRow{Model: "m", CurReq: 400, BaseReq: 0}, "up"},
		{"small new model", rpmRow{Model: "m", CurReq: 200, BaseReq: 0}, ""},
	}
	for _, tc := range cases {
		hits, up := rpmVerdict(cfg, []rpmRow{tc.row})
		got := ""
		if len(hits) == 1 {
			got = map[bool]string{true: "up", false: "down"}[up[0]]
		}
		assert.Equal(t, tc.want, got, tc.name)
	}
}
