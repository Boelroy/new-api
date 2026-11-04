package main

// Stream-failure guard for local channels.
//
// An OpenAI key that is about to run out of credit does not start answering
// 429 right away: for its last ~10 minutes it accepts streams and then ends
// them with response.failed, so clients see empty or half-finished output
// (2026-11-04: 0.18% failed normally, 24% in the 10 minutes before
// credit_balance_exhausted). A leader-only loop watches the recent
// stream_status of every enabled channel and, when one (channel, model) pair
// crosses the failure threshold, strips that model from the channel and drops
// the channel's priority so traffic moves to healthy keys before the key dies.
//
// Changes go straight to the shared database (channels.models, abilities,
// priority); new-api's 60s channel-cache sync picks them up. Every action is
// recorded in rs_stream_guard_event with the previous priority so an operator
// can restore it by hand.

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	streamGuardTick        = 2 * time.Minute
	streamGuardWindow      = 10 * time.Minute
	streamGuardMinStreams  = 10
	streamGuardMinFailed   = 3
	streamGuardMinFailRate = 0.10
	// A model failing on at least this share of the channels that served it
	// (and on at least streamGuardWideMinChans of them) is an upstream-wide
	// problem; stripping it everywhere would take the model offline.
	streamGuardWideShare    = 0.5
	streamGuardWideMinChans = 3
	// Demoted channels drop to this priority (new-api routes higher first).
	streamGuardDemotePriority = 0
)

type streamGuardPair struct {
	channelID int64
	model     string
	total     int
	failed    int
}

// streamGuardVerdict picks the (channel, model) pairs to act on. Pure so the
// thresholds and the upstream-wide exemption can be table-tested.
func streamGuardVerdict(pairs []streamGuardPair) []streamGuardPair {
	served := map[string]int{}
	failing := map[string][]streamGuardPair{}
	for _, p := range pairs {
		served[p.model]++
		if p.total >= streamGuardMinStreams && p.failed >= streamGuardMinFailed &&
			float64(p.failed) >= streamGuardMinFailRate*float64(p.total) {
			failing[p.model] = append(failing[p.model], p)
		}
	}
	var out []streamGuardPair
	for model, bad := range failing {
		if len(bad) >= streamGuardWideMinChans && float64(len(bad)) >= streamGuardWideShare*float64(served[model]) {
			log.Printf("[stream-guard] %s failing on %d/%d channels — upstream-wide, not stripping", model, len(bad), served[model])
			continue
		}
		out = append(out, bad...)
	}
	return out
}

func startStreamGuardLoop() {
	log.Printf("[stream-guard] loop starting")
	go func() {
		time.Sleep(3 * time.Minute) // stagger past the other schedulers
		for {
			if IsLeader() {
				if err := runStreamGuardTick(context.Background()); err != nil {
					log.Printf("[stream-guard] tick: %v", err)
				}
			}
			time.Sleep(streamGuardTick)
		}
	}()
}

func runStreamGuardTick(ctx context.Context) error {
	since := time.Now().Add(-streamGuardWindow).Unix()
	rows, err := db.QueryContext(ctx, `
		SELECT l.channel_id, l.model_name, COUNT(*),
		       COUNT(*) FILTER (WHERE l.other LIKE '%"response_status":"failed"%')
		FROM logs l
		JOIN channels c ON c.id = l.channel_id AND c.status = 1
		WHERE l.created_at >= $1 AND l.type = 2 AND l.is_stream
		GROUP BY 1, 2`, since)
	if err != nil {
		return err
	}
	var pairs []streamGuardPair
	for rows.Next() {
		var p streamGuardPair
		if err := rows.Scan(&p.channelID, &p.model, &p.total, &p.failed); err != nil {
			rows.Close()
			return err
		}
		pairs = append(pairs, p)
	}
	rows.Close()

	for _, p := range streamGuardVerdict(pairs) {
		if err := applyStreamGuard(ctx, p); err != nil {
			log.Printf("[stream-guard] channel %d model %s: %v", p.channelID, p.model, err)
		}
	}
	return nil
}

// applyStreamGuard strips p.model from the channel (unless it is the last
// model) and demotes the channel, in one transaction.
func applyStreamGuard(ctx context.Context, p streamGuardPair) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var models string
	var priority sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(models,''), priority FROM channels WHERE id = $1 AND status = 1 FOR UPDATE`,
		p.channelID).Scan(&models, &priority); err != nil {
		return err
	}
	current := csvSplitModels(models)
	if !slices.Contains(current, p.model) {
		// Already stripped on an earlier tick; its failures are still inside the window.
		return nil
	}
	remaining := slices.DeleteFunc(slices.Clone(current), func(m string) bool { return m == p.model })
	stripped := len(remaining) > 0
	oldPriority := priority.Int64
	demoted := !priority.Valid || oldPriority > streamGuardDemotePriority
	if !stripped && !demoted {
		return nil
	}

	if stripped {
		if _, err := tx.ExecContext(ctx, `UPDATE channels SET models = $1 WHERE id = $2`,
			csvJoinModels(remaining), p.channelID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM abilities WHERE channel_id = $1 AND model = $2`,
			p.channelID, p.model); err != nil {
			return err
		}
	}
	if demoted {
		if _, err := tx.ExecContext(ctx, `UPDATE channels SET priority = $1 WHERE id = $2`,
			streamGuardDemotePriority, p.channelID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE abilities SET priority = $1 WHERE channel_id = $2`,
			streamGuardDemotePriority, p.channelID); err != nil {
			return err
		}
	}
	var actions []string
	if stripped {
		actions = append(actions, "strip_model")
	}
	if demoted {
		actions = append(actions, "demote")
	}
	action := strings.Join(actions, "+")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO rs_stream_guard_event
		  (channel_id, model, action, streams, failed, old_priority, new_priority, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		p.channelID, p.model, action, p.total, p.failed, oldPriority, streamGuardDemotePriority, time.Now().Unix()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("[stream-guard] channel %d: %s (model %s, %d/%d streams failed in %s, priority %d -> %d)",
		p.channelID, action, p.model, p.failed, p.total, streamGuardWindow, oldPriority, streamGuardDemotePriority)
	return nil
}

// handleStreamGuardEvents lists guard actions newest first, with the channel's
// current state so the page can tell which strips are still in effect, plus
// the active thresholds.
func handleStreamGuardEvents(c *gin.Context) {
	rows, err := db.Query(`
		SELECT e.id, e.channel_id, COALESCE(ch.name,''), COALESCE(ch.status,0), COALESCE(ch.priority,0),
		       e.model, e.action, e.streams, e.failed, e.old_priority, e.new_priority, e.created_at, e.restored_at,
		       COALESCE(','||ch.models||',' LIKE '%,'||e.model||',%', false)
		FROM rs_stream_guard_event e
		LEFT JOIN channels ch ON ch.id = e.channel_id
		ORDER BY e.id DESC LIMIT 500`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	events := make([]gin.H, 0)
	for rows.Next() {
		var (
			id, channelID, chPriority, oldPriority, newPriority, createdAt, restoredAt int64
			chStatus, streams, failed                                                  int
			name, model, action                                                        string
			modelPresent                                                               bool
		)
		if err := rows.Scan(&id, &channelID, &name, &chStatus, &chPriority, &model, &action, &streams, &failed,
			&oldPriority, &newPriority, &createdAt, &restoredAt, &modelPresent); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		events = append(events, gin.H{
			"id": id, "channel_id": channelID, "channel_name": name, "channel_status": chStatus,
			"channel_priority": chPriority, "model": model, "action": action, "streams": streams,
			"failed": failed, "old_priority": oldPriority, "new_priority": newPriority,
			"created_at": createdAt, "restored_at": restoredAt, "model_present": modelPresent,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"events": events,
		"rules": gin.H{
			"tick_sec": int(streamGuardTick.Seconds()), "window_sec": int(streamGuardWindow.Seconds()),
			"min_streams": streamGuardMinStreams, "min_failed": streamGuardMinFailed,
			"min_fail_rate": streamGuardMinFailRate, "wide_share": streamGuardWideShare,
			"wide_min_chans": streamGuardWideMinChans, "demote_priority": streamGuardDemotePriority,
		},
	})
}

// handleStreamGuardRestore undoes one guard action: puts the model back on the
// channel (channels.models + one ability row per channel group) and raises the
// priority back to what it was before the event, if it is still lower.
func handleStreamGuardRestore(c *gin.Context) {
	var payload struct {
		ID int64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || payload.ID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id required"})
		return
	}
	ctx := c.Request.Context()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()

	var channelID, oldPriority, restoredAt int64
	var model, action string
	if err := tx.QueryRowContext(ctx,
		`SELECT channel_id, model, action, old_priority, restored_at FROM rs_stream_guard_event WHERE id = $1 FOR UPDATE`,
		payload.ID).Scan(&channelID, &model, &action, &oldPriority, &restoredAt); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "event not found"})
		return
	}
	if restoredAt > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "already restored"})
		return
	}
	var models, groups string
	var status int
	var priority, weight sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(models,''), COALESCE("group",''), COALESCE(status,0), priority, weight FROM channels WHERE id = $1 FOR UPDATE`,
		channelID).Scan(&models, &groups, &status, &priority, &weight); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
		return
	}
	newPriority := max(priority.Int64, oldPriority)
	if strings.Contains(action, "strip_model") && !slices.Contains(csvSplitModels(models), model) {
		if _, err := tx.ExecContext(ctx, `UPDATE channels SET models = $1 WHERE id = $2`,
			csvJoinModels(append(csvSplitModels(models), model)), channelID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		for _, g := range csvSplitModels(groups) {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO abilities ("group", model, channel_id, enabled, priority, weight)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT DO NOTHING`,
				g, model, channelID, status == 1, newPriority, weight.Int64); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
	}
	if newPriority > priority.Int64 {
		if _, err := tx.ExecContext(ctx, `UPDATE channels SET priority = $1 WHERE id = $2`, newPriority, channelID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if _, err := tx.ExecContext(ctx, `UPDATE abilities SET priority = $1 WHERE channel_id = $2`, newPriority, channelID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rs_stream_guard_event SET restored_at = $1 WHERE id = $2`,
		time.Now().Unix(), payload.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[stream-guard] restored event %d: channel %d model %s priority -> %d", payload.ID, channelID, model, newPriority)
	c.JSON(http.StatusOK, gin.H{"ok": true, "priority": newPriority})
}
