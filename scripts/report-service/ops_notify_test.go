package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestFmtUSD(t *testing.T) {
	assert.Equal(t, "$0.00", fmtUSD(0))
	assert.Equal(t, "$999.50", fmtUSD(999.5))
	assert.Equal(t, "$1,234,567.89", fmtUSD(1234567.891))
	assert.Equal(t, "-$1,000.00", fmtUSD(-1000))
}

// Muted remote profiles drop out of the 上号 digest — lines and title totals —
// while the other sources keep reporting; a window with only muted uploads
// yields no message. The mute list is merged, normalized and cleared through
// the config endpoint, and a config stored before the field existed loads
// with nothing muted.
func TestOpsUploadMuteProfiles(t *testing.T) {
	tencentTestDB(t)
	for _, ddl := range []string{
		`CREATE TABLE rs_notify_event (id BIGSERIAL PRIMARY KEY, kind TEXT NOT NULL, actor_id BIGINT NOT NULL DEFAULT 0,
		   actor TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '', created_at BIGINT NOT NULL)`,
		`CREATE TABLE remote_pending_key (id BIGSERIAL PRIMARY KEY, profile_id BIGINT NOT NULL, channel_type INT NOT NULL DEFAULT 14,
		   uploaded_by BIGINT NOT NULL DEFAULT 0, created_at BIGINT NOT NULL)`,
		`CREATE TABLE rs_supplier_account (id BIGSERIAL PRIMARY KEY, uploaded_by BIGINT NOT NULL, provider TEXT NOT NULL, created_at BIGINT NOT NULL)`,
		`CREATE TABLE rs_gynh_account (id BIGSERIAL PRIMARY KEY, uploaded_by BIGINT NOT NULL, provider TEXT NOT NULL, created_at BIGINT NOT NULL)`,
		`CREATE TABLE rs_ccmax_account (id BIGSERIAL PRIMARY KEY, uploaded_by BIGINT NOT NULL, plan TEXT NOT NULL DEFAULT '', created_at BIGINT NOT NULL)`,
		`INSERT INTO rs_auth_user (id, username) VALUES (1, 'alice'), (2, 'bob'), (3, 'carol')`,
		`INSERT INTO remote_newapi_profile (id, name) VALUES (1, 'aaaasl'), (2, 'p2')`,
		// Window (100, 200]: alice → aaaasl ×3, bob → p2 ×2, carol CC Max ×1.
		`INSERT INTO remote_pending_key (profile_id, uploaded_by, created_at) VALUES (1, 1, 150), (1, 1, 150), (1, 1, 150), (2, 2, 150), (2, 2, 150)`,
		`INSERT INTO rs_ccmax_account (uploaded_by, plan, created_at) VALUES (3, 'max', 150)`,
		// Window (300, 400]: only aaaasl.
		`INSERT INTO remote_pending_key (profile_id, uploaded_by, created_at) VALUES (1, 1, 350)`,
	} {
		_, err := db.Exec(ddl)
		require.NoError(t, err)
	}
	ctx := context.Background()

	lines, totals, err := opsUploadLines(ctx, 100, 200, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"· alice 远程 aaaasl Anthropic 3 个", "· bob 远程 p2 Anthropic 2 个", "· carol CC Max max 1 个"}, lines)
	assert.Equal(t, opsTotals{"aaaasl": 3, "p2": 2, "CC Max": 1}, totals)

	lines, totals, err = opsUploadLines(ctx, 100, 200, []int64{1})
	require.NoError(t, err)
	assert.Equal(t, []string{"· bob 远程 p2 Anthropic 2 个", "· carol CC Max max 1 个"}, lines)
	assert.Equal(t, opsTotals{"p2": 2, "CC Max": 1}, totals)

	lines, totals, err = opsUploadLines(ctx, 300, 400, []int64{1})
	require.NoError(t, err)
	assert.Empty(t, lines)
	assert.Empty(t, totals)

	require.NoError(t, supplierConfigSet(cfgOpsNotify, `{"enabled":true,"uploads":true,"window_min":10}`))
	legacy := loadOpsNotifyConfig()
	assert.True(t, legacy.Uploads)
	assert.Equal(t, []int64{}, legacy.UploadMuteProfiles)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/cfg", handleOpsNotifyConfigSet)
	post := func(body string) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/cfg", strings.NewReader(body)))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}
	post(`{"upload_mute_profiles":[2,1,1,0]}`)
	assert.Equal(t, []int64{1, 2}, loadOpsNotifyConfig().UploadMuteProfiles)
	post(`{"priority":false}`)
	assert.Equal(t, []int64{1, 2}, loadOpsNotifyConfig().UploadMuteProfiles, "other fields leave the list alone")
	post(`{"upload_mute_profiles":[]}`)
	assert.Equal(t, []int64{}, loadOpsNotifyConfig().UploadMuteProfiles)
}
