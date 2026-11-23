package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRemoteChannelList serves /api/channel/ like new-api: newest channel
// first, page_size clamped to 100. failPage answers 502; page rateLimitPage
// answers 429 for its first rateLimitHits requests.
type fakeRemoteChannelList struct {
	total         int
	failPage      int
	rateLimitPage int
	rateLimitHits int32
	requests      atomic.Int32
}

func (f *fakeRemoteChannelList) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	page, _ := strconv.Atoi(r.URL.Query().Get("p"))
	size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	size = min(size, 100)
	if page == f.failPage {
		http.Error(w, "upstream down", http.StatusBadGateway)
		return
	}
	if page == f.rateLimitPage && f.rateLimitHits > 0 {
		f.rateLimitHits--
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	items := make([]map[string]any, 0, size)
	for i := (page - 1) * size; i < min(page*size, f.total); i++ {
		items = append(items, map[string]any{"id": f.total - i, "name": "ch-" + strconv.Itoa(f.total-i)})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"data":    map[string]any{"items": items, "total": f.total, "page": page, "page_size": size},
	})
}

// remoteChannelMirrorFixture swaps in a sqlmock DB and drops the page gap and
// 429 backoff so a 60-page walk runs instantly.
func remoteChannelMirrorFixture(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	prevDB, prevGap, prevBackoff := db, remoteChannelPageGap, remoteRateLimitBackoff
	pool, mock, err := sqlmock.New()
	require.NoError(t, err)
	db = pool
	remoteChannelPageGap = 0
	remoteRateLimitBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() {
		db, remoteChannelPageGap, remoteRateLimitBackoff = prevDB, prevGap, prevBackoff
		pool.Close()
	})
	return mock
}

// expectProfileLookups covers remoteTransportForHost, which reads the
// profile row once per remote request; no row means a direct admin call.
func expectProfileLookups(mock sqlmock.Sqlmock, n int) {
	for range n {
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT proxy, api_mode FROM remote_newapi_profile`)).
			WillReturnError(sql.ErrNoRows)
	}
}

func TestIterateRemoteChannelsCollectsRemoteTotal(t *testing.T) {
	cases := []struct {
		name          string
		total         int
		rateLimitPage int
		rateLimitHits int32
		wantRequests  int
	}{
		// 6000 channels is 60 pages: past the old 50-page / 5000-row cap.
		{name: "more than fifty pages", total: 6000, wantRequests: 60},
		{name: "rate limited page is retried", total: 250, rateLimitPage: 2, rateLimitHits: 2, wantRequests: 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := remoteChannelMirrorFixture(t)
			remote := &fakeRemoteChannelList{total: tc.total, rateLimitPage: tc.rateLimitPage, rateLimitHits: tc.rateLimitHits}
			srv := httptest.NewServer(remote)
			t.Cleanup(srv.Close)
			expectProfileLookups(mock, tc.wantRequests)

			channels, total, err := iterateRemoteChannels(context.Background(), srv.URL, "token", 1, 100, nil)

			require.NoError(t, err)
			assert.Equal(t, int64(tc.total), total)
			require.Len(t, channels, tc.total)
			assert.Equal(t, int64(tc.total), channels[0].ID)
			assert.Equal(t, int64(1), channels[len(channels)-1].ID, "oldest channel is reached")
			assert.Equal(t, int32(tc.wantRequests), remote.requests.Load())
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// The mirror only deletes channels missing from the list after a complete
// walk; a walk cut short by a remote error upserts what it got and leaves
// every other existing row in remote_channel_current alone.
func TestMirrorRemoteChannelsDeletesOnlyAfterCompleteWalk(t *testing.T) {
	cases := []struct {
		name         string
		failPage     int
		wantRows     int
		wantRequests int
		wantDelete   bool
	}{
		{name: "complete walk reconciles", wantRows: 250, wantRequests: 3, wantDelete: true},
		{name: "remote error mid walk keeps old rows", failPage: 2, wantRows: 100, wantRequests: 2, wantDelete: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := remoteChannelMirrorFixture(t)
			srv := httptest.NewServer(&fakeRemoteChannelList{total: 250, failPage: tc.failPage})
			t.Cleanup(srv.Close)
			expectProfileLookups(mock, tc.wantRequests)

			channels, total, walkErr := iterateRemoteChannels(context.Background(), srv.URL, "token", 1, 100, nil)
			require.Len(t, channels, tc.wantRows)
			assert.Equal(t, tc.failPage != 0, walkErr != nil)

			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO remote_channel_snapshot`)).
				WillReturnResult(sqlmock.NewResult(0, int64(tc.wantRows)))
			mock.ExpectCommit()
			mock.ExpectBegin()
			// upsertRemoteCurrent writes 200 rows per statement.
			for range (tc.wantRows + 199) / 200 {
				mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO remote_channel_current`)).
					WillReturnResult(sqlmock.NewResult(0, 1))
			}
			if tc.wantDelete {
				mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM remote_channel_current`)).
					WithArgs(int64(7), sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(0, 3))
			}
			mock.ExpectCommit()

			// An unexpected DELETE fails the exec, so NoError also proves no
			// reconcile ran on the partial walk.
			require.NoError(t, mirrorRemoteChannels(7, channels, total, walkErr))
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// usage-range retries a rate-limited /api/log/stat, reports channels whose
// stat call still fails in failed_channel_ids instead of as 0 usage, and
// does not cache the failure: the next refresh asks the remote again.
func TestRemoteChannelUsageRangeReportsFailedChannels(t *testing.T) {
	mock := remoteChannelMirrorFixture(t)
	mock.MatchExpectationsInOrder(false)
	var rateLimited atomic.Int32
	statCalls := map[string]*atomic.Int32{"1": {}, "2": {}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch := r.URL.Query().Get("channel")
		statCalls[ch].Add(1)
		switch {
		case ch == "2":
			http.Error(w, "upstream down", http.StatusBadGateway)
		case rateLimited.Add(1) <= 2:
			http.Error(w, "too many requests", http.StatusTooManyRequests)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"quota": 1_000_000}})
		}
	}))
	t.Cleanup(srv.Close)

	const profileID, start, end = int64(9), int64(1_700_000_000), int64(1_700_003_600)
	t.Cleanup(func() {
		usageRangeCacheMu.Lock()
		for _, ch := range []int64{1, 2} {
			delete(usageRangeCache, fmt.Sprintf("%d:%d:%d:%d", profileID, ch, start, end))
		}
		usageRangeCacheMu.Unlock()
	})
	call := func() map[string]any {
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT host, user_id, access_token_enc FROM remote_newapi_profile`)).
			WithArgs(profileID).
			WillReturnRows(sqlmock.NewRows([]string{"host", "user_id", "access_token_enc"}).AddRow(srv.URL, 1, ""))
		payload, err := json.Marshal(map[string]any{
			"profile_id": profileID, "start_timestamp": start, "end_timestamp": end, "channel_ids": []int64{2, 1},
		})
		require.NoError(t, err)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/remote-newapi/channels/usage-range", bytes.NewReader(payload))
		c.Request.Header.Set("Content-Type", "application/json")
		handleRemoteChannelUsageRange(c)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		return out
	}

	// First call: channel 1 → two 429s then success, channel 2 → one 502.
	expectProfileLookups(mock, 4)
	first := call()
	assert.Equal(t, map[string]any{"1": float64(1_000_000)}, first["data"])
	assert.Equal(t, []any{float64(2)}, first["failed_channel_ids"])
	assert.InDelta(t, 2.0, first["total_used_usd"], 1e-9)

	// Second call: channel 1 comes from cache, channel 2 is asked again.
	expectProfileLookups(mock, 1)
	second := call()
	assert.Equal(t, first["data"], second["data"])
	assert.Equal(t, []any{float64(2)}, second["failed_channel_ids"])
	assert.Equal(t, int32(3), statCalls["1"].Load())
	assert.Equal(t, int32(2), statCalls["2"].Load())
	assert.NoError(t, mock.ExpectationsWereMet())
}
