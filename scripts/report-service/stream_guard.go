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
//
// High-tier keys (rs_key_tier.tier >= CooldownMinTier) rarely die of credit
// exhaustion; their failed streams are mostly upstream timeouts, so for them
// the action is a cooldown: cooldown_until is stamped on the event and the
// loop restores it once that passes. Repeat hits on the same (channel, model)
// within 24h double the cooldown up to CooldownMaxSec.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// streamGuardConfig is operator-tunable from the 调度 page and stored as one
// JSON value in report_config; every tick reloads it, so edits apply on the
// next tick without a restart.
type streamGuardConfig struct {
	Enabled   bool `json:"enabled"`
	TickSec   int  `json:"tick_sec"`
	WindowSec int  `json:"window_sec"`
	// A (channel, model) pair is hit when its streams in the window reach
	// MinStreams, MinFailed of them ended in response.failed, and the failure
	// rate is at least MinFailRatePct.
	MinStreams     int `json:"min_streams"`
	MinFailed      int `json:"min_failed"`
	MinFailRatePct int `json:"min_fail_rate_pct"`
	// A model failing on at least WideSharePct of the channels that served it
	// (and on at least WideMinChans of them) is an upstream-wide problem;
	// stripping it everywhere would take the model offline.
	WideSharePct int  `json:"wide_share_pct"`
	WideMinChans int  `json:"wide_min_chans"`
	StripModel   bool `json:"strip_model"`
	Demote       bool `json:"demote"`
	// Demoted channels drop to this priority (new-api routes higher first).
	DemotePriority int `json:"demote_priority"`
	// Channels whose probed OpenAI tier is at least CooldownMinTier get a
	// timed cooldown instead of a permanent strip; 0 turns cooldowns off.
	CooldownMinTier int `json:"cooldown_min_tier"`
	CooldownSec     int `json:"cooldown_sec"`
	CooldownMaxSec  int `json:"cooldown_max_sec"`
}

const cfgStreamGuard = "stream_guard_config"

var streamGuardDefaults = streamGuardConfig{
	Enabled: true, TickSec: 120, WindowSec: 600,
	MinStreams: 10, MinFailed: 3, MinFailRatePct: 10,
	WideSharePct: 50, WideMinChans: 3,
	StripModel: true, Demote: true, DemotePriority: 0,
	CooldownMinTier: 5, CooldownSec: 1800, CooldownMaxSec: 4 * 3600,
}

// clamp bounds every field so a bad save cannot stall the loop or make it
// fire on every stream.
func (c streamGuardConfig) clamp() streamGuardConfig {
	c.TickSec = clampInt(c.TickSec, 5, 3600)
	c.WindowSec = clampInt(c.WindowSec, 30, 6*3600)
	c.MinStreams = clampInt(c.MinStreams, 1, 100000)
	c.MinFailed = clampInt(c.MinFailed, 1, 100000)
	c.MinFailRatePct = clampInt(c.MinFailRatePct, 1, 100)
	c.WideSharePct = clampInt(c.WideSharePct, 1, 100)
	c.WideMinChans = clampInt(c.WideMinChans, 1, 1000)
	c.DemotePriority = clampInt(c.DemotePriority, -100000, 100000)
	c.CooldownMinTier = clampInt(c.CooldownMinTier, 0, len(keyTierTPM)-1)
	// A cooldown shorter than the window would re-strip on the failures that
	// caused it as soon as the model is back.
	c.CooldownSec = clampInt(c.CooldownSec, max(60, c.WindowSec), 7*86400)
	c.CooldownMaxSec = clampInt(c.CooldownMaxSec, c.CooldownSec, 7*86400)
	return c
}

func loadStreamGuardConfig() streamGuardConfig {
	cfg := streamGuardDefaults
	var raw string
	if err := db.QueryRow(`SELECT value FROM report_config WHERE key = $1`, cfgStreamGuard).Scan(&raw); err == nil {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			log.Printf("[stream-guard] bad %s, using defaults: %v", cfgStreamGuard, err)
			cfg = streamGuardDefaults
		}
	}
	return cfg.clamp()
}

type streamGuardPair struct {
	channelID int64
	model     string
	total     int
	failed    int
}

// streamGuardVerdict picks the (channel, model) pairs to act on. Pure so the
// thresholds and the upstream-wide exemption can be table-tested.
func streamGuardVerdict(cfg streamGuardConfig, pairs []streamGuardPair) []streamGuardPair {
	served := map[string]int{}
	failing := map[string][]streamGuardPair{}
	for _, p := range pairs {
		served[p.model]++
		if p.total >= cfg.MinStreams && p.failed >= cfg.MinFailed &&
			p.failed*100 >= cfg.MinFailRatePct*p.total {
			failing[p.model] = append(failing[p.model], p)
		}
	}
	var out []streamGuardPair
	for model, bad := range failing {
		if len(bad) >= cfg.WideMinChans && len(bad)*100 >= cfg.WideSharePct*served[model] {
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
			cfg := loadStreamGuardConfig()
			if IsLeader() {
				// Due cooldowns are restored even while the guard is switched off.
				restoreDueStreamGuardCooldowns(context.Background())
			}
			if cfg.Enabled && IsLeader() {
				if err := runStreamGuardTick(context.Background(), cfg); err != nil {
					log.Printf("[stream-guard] tick: %v", err)
				}
			}
			time.Sleep(time.Duration(cfg.TickSec) * time.Second)
		}
	}()
}

func runStreamGuardTick(ctx context.Context, cfg streamGuardConfig) error {
	if !cfg.StripModel && !cfg.Demote {
		return nil
	}
	since := time.Now().Add(-time.Duration(cfg.WindowSec) * time.Second).Unix()
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

	for _, p := range streamGuardVerdict(cfg, pairs) {
		if err := applyStreamGuard(ctx, cfg, p); err != nil {
			log.Printf("[stream-guard] channel %d model %s: %v", p.channelID, p.model, err)
		}
	}
	return nil
}

// applyStreamGuard strips p.model from the channel (unless it is the last
// model) and/or demotes the channel, as configured, in one transaction.
func applyStreamGuard(ctx context.Context, cfg streamGuardConfig, p streamGuardPair) error {
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
	stripped := cfg.StripModel && len(remaining) > 0
	oldPriority := priority.Int64
	newPriority := oldPriority
	demoted := cfg.Demote && (!priority.Valid || oldPriority > int64(cfg.DemotePriority))
	if demoted {
		newPriority = int64(cfg.DemotePriority)
	}
	if !stripped && !demoted {
		return nil
	}

	now := time.Now().Unix()
	var cooldownUntil int64
	var tier sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT tier FROM rs_key_tier WHERE channel_id = $1`, p.channelID).Scan(&tier); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if cfg.CooldownMinTier > 0 && tier.Valid && tier.Int64 >= int64(cfg.CooldownMinTier) {
		var repeats int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM rs_stream_guard_event
			WHERE channel_id = $1 AND model = $2 AND cooldown_until > 0 AND created_at >= $3`,
			p.channelID, p.model, now-24*3600).Scan(&repeats); err != nil {
			return err
		}
		cooldownUntil = now + streamGuardCooldown(cfg, repeats)
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
			newPriority, p.channelID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE abilities SET priority = $1 WHERE channel_id = $2`,
			newPriority, p.channelID); err != nil {
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
		  (channel_id, model, action, streams, failed, old_priority, new_priority, created_at, cooldown_until)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		p.channelID, p.model, action, p.total, p.failed, oldPriority, newPriority, now, cooldownUntil); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("[stream-guard] channel %d: %s (model %s, %d/%d streams failed in %ds, priority %d -> %d, tier %v, cooldown %ds)",
		p.channelID, action, p.model, p.failed, p.total, cfg.WindowSec, oldPriority, newPriority,
		tier.Int64, max(0, cooldownUntil-now))
	return nil
}

// streamGuardCooldown is how long a high-tier channel keeps the model off:
// CooldownSec, doubled for each earlier cooldown of the same pair in the last
// 24h, capped at CooldownMaxSec.
func streamGuardCooldown(cfg streamGuardConfig, repeats int) int64 {
	cooldown := int64(cfg.CooldownSec)
	for range min(repeats, 16) {
		cooldown = min(cooldown*2, int64(cfg.CooldownMaxSec))
	}
	return cooldown
}

// restoreDueStreamGuardCooldowns restores every cooldown event whose time is
// up. An event whose channel is gone is closed so it is not retried forever.
func restoreDueStreamGuardCooldowns(ctx context.Context) {
	rows, err := db.QueryContext(ctx, `
		SELECT id FROM rs_stream_guard_event
		WHERE restored_at = 0 AND cooldown_until > 0 AND cooldown_until <= $1
		ORDER BY id`, time.Now().Unix())
	if err != nil {
		log.Printf("[stream-guard] due cooldowns: %v", err)
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		_, err := restoreStreamGuardEvent(ctx, id)
		if errors.Is(err, errStreamGuardChannelGone) {
			_, err = db.ExecContext(ctx, `UPDATE rs_stream_guard_event SET restored_at = $1 WHERE id = $2`, time.Now().Unix(), id)
		}
		if err != nil && !errors.Is(err, errStreamGuardRestored) {
			log.Printf("[stream-guard] cooldown restore event %d: %v", id, err)
		}
	}
}

func handleStreamGuardConfigGet(c *gin.Context) {
	c.JSON(http.StatusOK, loadStreamGuardConfig())
}

// handleStreamGuardConfigSet merges the posted fields over the stored config.
func handleStreamGuardConfigSet(c *gin.Context) {
	cfg := loadStreamGuardConfig()
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	cfg = cfg.clamp()
	raw, _ := json.Marshal(cfg)
	if _, err := db.Exec(`
		INSERT INTO report_config (key, value, updated_at) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = $3`,
		cfgStreamGuard, string(raw), time.Now().Unix()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, cfg)
}

// handleStreamGuardEvents lists guard actions newest first, with the channel's
// current state so the page can tell which strips are still in effect.
func handleStreamGuardEvents(c *gin.Context) {
	rows, err := db.Query(`
		SELECT e.id, e.channel_id, COALESCE(ch.name,''), COALESCE(ch.status,0), COALESCE(ch.priority,0),
		       e.model, e.action, e.streams, e.failed, e.old_priority, e.new_priority, e.created_at, e.restored_at, e.cooldown_until,
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
			id, channelID, chPriority, oldPriority, newPriority, createdAt, restoredAt, cooldownUntil int64
			chStatus, streams, failed                                                                 int
			name, model, action                                                                       string
			modelPresent                                                                              bool
		)
		if err := rows.Scan(&id, &channelID, &name, &chStatus, &chPriority, &model, &action, &streams, &failed,
			&oldPriority, &newPriority, &createdAt, &restoredAt, &cooldownUntil, &modelPresent); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		events = append(events, gin.H{
			"id": id, "channel_id": channelID, "channel_name": name, "channel_status": chStatus,
			"channel_priority": chPriority, "model": model, "action": action, "streams": streams,
			"failed": failed, "old_priority": oldPriority, "new_priority": newPriority,
			"created_at": createdAt, "restored_at": restoredAt, "cooldown_until": cooldownUntil,
			"model_present": modelPresent,
		})
	}
	c.JSON(http.StatusOK, gin.H{"events": events})
}

var (
	errStreamGuardNoEvent     = errors.New("event not found")
	errStreamGuardRestored    = errors.New("already restored")
	errStreamGuardChannelGone = errors.New("channel not found")
)

// restoreStreamGuardEvent undoes one guard action: puts the model back on the
// channel (channels.models + one ability row per channel group) and, for a
// demotion, raises the priority back to what it was before the event, if it
// is still lower. It
// returns the channel's priority afterwards.
func restoreStreamGuardEvent(ctx context.Context, id int64) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var channelID, oldPriority, restoredAt int64
	var model, action string
	if err := tx.QueryRowContext(ctx,
		`SELECT channel_id, model, action, old_priority, restored_at FROM rs_stream_guard_event WHERE id = $1 FOR UPDATE`,
		id).Scan(&channelID, &model, &action, &oldPriority, &restoredAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errStreamGuardNoEvent
		}
		return 0, err
	}
	if restoredAt > 0 {
		return 0, errStreamGuardRestored
	}
	var models, groups string
	var status int
	var priority, weight sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(models,''), COALESCE("group",''), COALESCE(status,0), priority, weight FROM channels WHERE id = $1 FOR UPDATE`,
		channelID).Scan(&models, &groups, &status, &priority, &weight); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errStreamGuardChannelGone
		}
		return 0, err
	}
	// Only a demotion is undone on priority. A strip-only event (tier cooldown)
	// never moved it, and its old_priority may be hours stale by now: raising
	// to it would overwrite whatever an operator set in between.
	newPriority := priority.Int64
	if strings.Contains(action, "demote") {
		newPriority = max(priority.Int64, oldPriority)
	}
	if strings.Contains(action, "strip_model") && !slices.Contains(csvSplitModels(models), model) {
		if _, err := tx.ExecContext(ctx, `UPDATE channels SET models = $1 WHERE id = $2`,
			csvJoinModels(append(csvSplitModels(models), model)), channelID); err != nil {
			return 0, err
		}
		for _, g := range csvSplitModels(groups) {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO abilities ("group", model, channel_id, enabled, priority, weight)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT DO NOTHING`,
				g, model, channelID, status == 1, newPriority, weight.Int64); err != nil {
				return 0, err
			}
		}
	}
	if newPriority > priority.Int64 {
		if _, err := tx.ExecContext(ctx, `UPDATE channels SET priority = $1 WHERE id = $2`, newPriority, channelID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE abilities SET priority = $1 WHERE channel_id = $2`, newPriority, channelID); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rs_stream_guard_event SET restored_at = $1 WHERE id = $2`,
		time.Now().Unix(), id); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	log.Printf("[stream-guard] restored event %d: channel %d model %s priority -> %d", id, channelID, model, newPriority)
	return newPriority, nil
}

func handleStreamGuardRestore(c *gin.Context) {
	var payload struct {
		ID int64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || payload.ID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id required"})
		return
	}
	priority, err := restoreStreamGuardEvent(c.Request.Context(), payload.ID)
	switch {
	case errors.Is(err, errStreamGuardNoEvent), errors.Is(err, errStreamGuardChannelGone):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, errStreamGuardRestored):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusOK, gin.H{"ok": true, "priority": priority})
	}
}
