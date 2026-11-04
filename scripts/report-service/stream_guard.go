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
	"slices"
	"strings"
	"time"
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
