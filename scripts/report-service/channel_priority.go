package main

// Log-driven channel priority auto-tuning.
//
// new-api routes to the channels holding the MAX(priority) for a model+group,
// weighted-random within that top tier. So dropping a channel's priority below
// the tier baseline pulls it out of rotation, and restoring it puts it back.
//
// This loop uses that: when a channel is being throttled — in the last
// down_window it produced only 429s (>= min_throttle of them, zero successes) —
// its priority is stepped down so live traffic shifts to healthy peers. When a
// channel has gone up_window with no 429 at all, its priority is stepped back
// up toward the baseline it had when we first demoted it. A ledger row
// (rs_channel_priority_state) remembers that baseline and scopes the loop to
// only ever touch channels it demoted; once a channel is fully restored the
// row is deleted.
//
// Priority is written straight to the DB (channels + abilities in one
// transaction, kept in sync exactly like handleBatchUpdateChannelPriority),
// which this deployment reads live — no MAIN_SERVICE token needed.

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

const (
	cfgChanPrioEnabled       = "chan_prio_enabled"
	cfgChanPrioGroups        = "chan_prio_groups" // CSV, empty = all
	cfgChanPrioDownWindowSec = "chan_prio_down_window_sec"
	cfgChanPrioUpWindowSec   = "chan_prio_up_window_sec"
	cfgChanPrioTickSec       = "chan_prio_tick_sec"
	cfgChanPrioStep          = "chan_prio_step"
	cfgChanPrioMaxDrop       = "chan_prio_max_drop"
	cfgChanPrioMinThrottle   = "chan_prio_min_throttle"
	cfgChanPrioThrottleSub   = "chan_prio_throttle_substr"
	cfgChanPrioMaxActions    = "chan_prio_max_actions"
	cfgChanPrioDryRun        = "chan_prio_dry_run"

	chanPrioDownWindowDef, chanPrioDownWindowMin, chanPrioDownWindowMax = 30, 10, 600
	chanPrioUpWindowDef, chanPrioUpWindowMin, chanPrioUpWindowMax       = 60, 10, 3600
	chanPrioTickDef, chanPrioTickMin, chanPrioTickMax                   = 15, 5, 300
	chanPrioStepDef, chanPrioStepMin, chanPrioStepMax                   = 100, 1, 1000000
	chanPrioMaxDropDef, chanPrioMaxDropMin, chanPrioMaxDropMax          = 1000, 1, 100000000
	chanPrioMinThrottleDef, chanPrioMinThrottleMin, chanPrioMinThrottleMax = 3, 1, 10000
	chanPrioMaxActionsDef, chanPrioMaxActionsMin, chanPrioMaxActionsMax = 200, 1, 2000

	chanPrioThrottleSubDef  = "StatusCode: 429"
	chanPrioEventRetention  = 14 * 24 * time.Hour
	chanPrioPriorityFloorMin = 1 // never write a non-positive priority
)

type chanPrioConfig struct {
	Enabled       bool     `json:"enabled"`
	Groups        []string `json:"groups"`
	DownWindowSec int      `json:"down_window_sec"`
	UpWindowSec   int      `json:"up_window_sec"`
	TickSec       int      `json:"tick_sec"`
	Step          int      `json:"step"`
	MaxDrop       int      `json:"max_drop"`
	MinThrottle   int      `json:"min_throttle"`
	ThrottleSubstr string  `json:"throttle_substr"`
	MaxActions    int      `json:"max_actions"`
	DryRun        bool     `json:"dry_run"`
}

var chanPrioNudge = make(chan struct{}, 1)

func nudgeChanPrio() {
	select {
	case chanPrioNudge <- struct{}{}:
	default:
	}
}

func loadChanPrioConfig() chanPrioConfig {
	out := chanPrioConfig{
		Groups:         []string{},
		DownWindowSec:  chanPrioDownWindowDef,
		UpWindowSec:    chanPrioUpWindowDef,
		TickSec:        chanPrioTickDef,
		Step:           chanPrioStepDef,
		MaxDrop:        chanPrioMaxDropDef,
		MinThrottle:    chanPrioMinThrottleDef,
		ThrottleSubstr: chanPrioThrottleSubDef,
		MaxActions:     chanPrioMaxActionsDef,
	}
	get := func(key string) (string, bool) {
		var v string
		if err := db.QueryRow(`SELECT value FROM report_config WHERE key=$1`, key).Scan(&v); err != nil {
			return "", false
		}
		return v, true
	}
	readInt := func(key string, dst *int) {
		if v, ok := get(key); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				*dst = n
			}
		}
	}
	readBool := func(key string, dst *bool) {
		if v, ok := get(key); ok {
			s := strings.TrimSpace(v)
			*dst = s == "true" || s == "1"
		}
	}

	readBool(cfgChanPrioEnabled, &out.Enabled)
	readBool(cfgChanPrioDryRun, &out.DryRun)
	if v, ok := get(cfgChanPrioGroups); ok {
		out.Groups = splitCSV(v)
	}
	if v, ok := get(cfgChanPrioThrottleSub); ok {
		if s := strings.TrimSpace(v); s != "" {
			out.ThrottleSubstr = s
		}
	}
	readInt(cfgChanPrioDownWindowSec, &out.DownWindowSec)
	readInt(cfgChanPrioUpWindowSec, &out.UpWindowSec)
	readInt(cfgChanPrioTickSec, &out.TickSec)
	readInt(cfgChanPrioStep, &out.Step)
	readInt(cfgChanPrioMaxDrop, &out.MaxDrop)
	readInt(cfgChanPrioMinThrottle, &out.MinThrottle)
	readInt(cfgChanPrioMaxActions, &out.MaxActions)

	out.DownWindowSec = clampInt(out.DownWindowSec, chanPrioDownWindowMin, chanPrioDownWindowMax)
	out.UpWindowSec = clampInt(out.UpWindowSec, chanPrioUpWindowMin, chanPrioUpWindowMax)
	out.TickSec = clampInt(out.TickSec, chanPrioTickMin, chanPrioTickMax)
	out.Step = clampInt(out.Step, chanPrioStepMin, chanPrioStepMax)
	out.MaxDrop = clampInt(out.MaxDrop, chanPrioMaxDropMin, chanPrioMaxDropMax)
	out.MinThrottle = clampInt(out.MinThrottle, chanPrioMinThrottleMin, chanPrioMinThrottleMax)
	out.MaxActions = clampInt(out.MaxActions, chanPrioMaxActionsMin, chanPrioMaxActionsMax)
	return out
}

func writeChanPrioConfig(key, value string) error {
	now := time.Now().Unix()
	_, err := db.Exec(
		`INSERT INTO report_config (key, value, updated_at) VALUES ($1, $2, $3)
		 ON CONFLICT (key) DO UPDATE SET value=$2, updated_at=$3`,
		key, value, now,
	)
	return err
}

// ---- Loop ----

func startChannelPriorityLoop() {
	log.Printf("[chan-prio] loop starting")
	go func() {
		time.Sleep(90 * time.Second) // stagger past the other schedulers
		for {
			cfg := loadChanPrioConfig()
			if cfg.Enabled && IsLeader() {
				runChanPrioTick(context.Background(), cfg)
			}
			select {
			case <-time.After(time.Duration(cfg.TickSec) * time.Second):
			case <-chanPrioNudge:
			}
		}
	}()
	go chanPrioPruneLoop()
}

func chanPrioPruneLoop() {
	time.Sleep(7 * time.Minute)
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		if !IsLeader() {
			continue
		}
		cutoff := time.Now().Add(-chanPrioEventRetention).Unix()
		if _, err := db.Exec(`DELETE FROM rs_channel_priority_event WHERE created_at < $1`, cutoff); err != nil {
			log.Printf("[chan-prio] prune events: %v", err)
		}
	}
}

type chanPrioSummary struct {
	Demoted  int `json:"demoted"`
	Promoted int `json:"promoted"`
	Restored int `json:"restored"`
	Errors   int `json:"errors"`
}

func runChanPrioTick(ctx context.Context, cfg chanPrioConfig) chanPrioSummary {
	var sum chanPrioSummary
	actionsLeft := cfg.MaxActions
	throttleLike := "%" + cfg.ThrottleSubstr + "%"
	now := time.Now().Unix()
	downCut := now - int64(cfg.DownWindowSec)
	upCut := now - int64(cfg.UpWindowSec)

	// --- Demote: throttled channels step down ---
	demotes, err := findThrottledChannels(ctx, cfg.Groups, downCut, throttleLike, cfg.MinThrottle, actionsLeft)
	if err != nil {
		log.Printf("[chan-prio] find throttled: %v", err)
	}
	for _, d := range demotes {
		if actionsLeft <= 0 || !IsLeader() {
			break
		}
		base := d.priority
		if d.basePriority > 0 {
			base = d.basePriority
		}
		floor := base - cfg.MaxDrop
		if floor < chanPrioPriorityFloorMin {
			floor = chanPrioPriorityFloorMin
		}
		newPrio := d.priority - cfg.Step
		if newPrio < floor {
			newPrio = floor
		}
		if newPrio >= d.priority {
			continue // already at floor, nothing to do
		}
		detail := fmt.Sprintf("throttled: %d %q, 0 success in %ds; %d -> %d (base %d)",
			d.throttle, cfg.ThrottleSubstr, cfg.DownWindowSec, d.priority, newPrio, base)
		if cfg.DryRun {
			sum.Demoted++
			actionsLeft--
			logChanPrioEvent(d.id, d.name, d.group, "demote", d.priority, newPrio, base, d.throttle, true, detail)
			continue
		}
		if err := applyChannelPriority(ctx, d.id, newPrio, base, false); err != nil {
			sum.Errors++
			actionsLeft--
			logChanPrioEvent(d.id, d.name, d.group, "error", d.priority, newPrio, base, d.throttle, false, "demote failed: "+err.Error())
			log.Printf("[chan-prio] demote channel %d: %v", d.id, err)
			continue
		}
		sum.Demoted++
		actionsLeft--
		logChanPrioEvent(d.id, d.name, d.group, "demote", d.priority, newPrio, base, d.throttle, false, detail)
		log.Printf("[chan-prio] demoted channel %d (%s): %s", d.id, d.name, detail)
	}

	// --- Promote: recovered channels step back up toward baseline ---
	promotes, err := findRecoveredChannels(ctx, cfg.Groups, upCut, throttleLike, actionsLeft)
	if err != nil {
		log.Printf("[chan-prio] find recovered: %v", err)
	}
	for _, p := range promotes {
		if actionsLeft <= 0 || !IsLeader() {
			break
		}
		newPrio := p.priority + cfg.Step
		restore := false
		if newPrio >= p.basePriority {
			newPrio = p.basePriority
			restore = true
		}
		if newPrio <= p.priority {
			continue
		}
		action := "promote"
		if restore {
			action = "restore"
		}
		detail := fmt.Sprintf("no throttle in %ds; %d -> %d (base %d)%s",
			cfg.UpWindowSec, p.priority, newPrio, p.basePriority,
			map[bool]string{true: " [restored]", false: ""}[restore])
		if cfg.DryRun {
			if restore {
				sum.Restored++
			} else {
				sum.Promoted++
			}
			actionsLeft--
			logChanPrioEvent(p.id, p.name, p.group, action, p.priority, newPrio, p.basePriority, 0, true, detail)
			continue
		}
		if err := applyChannelPriority(ctx, p.id, newPrio, p.basePriority, restore); err != nil {
			sum.Errors++
			actionsLeft--
			logChanPrioEvent(p.id, p.name, p.group, "error", p.priority, newPrio, p.basePriority, 0, false, "promote failed: "+err.Error())
			log.Printf("[chan-prio] promote channel %d: %v", p.id, err)
			continue
		}
		if restore {
			sum.Restored++
		} else {
			sum.Promoted++
		}
		actionsLeft--
		logChanPrioEvent(p.id, p.name, p.group, action, p.priority, newPrio, p.basePriority, 0, false, detail)
		log.Printf("[chan-prio] %s channel %d (%s): %s", action, p.id, p.name, detail)
	}
	return sum
}

type prioTarget struct {
	id           int64
	name         string
	group        string
	priority     int
	basePriority int
	throttle     int
}

// findThrottledChannels returns enabled channels (optionally in groups) whose
// traffic in the down-window is all throttle: >= minThrottle matching 429
// errors and zero successes. basePriority is the ledger baseline if one exists.
func findThrottledChannels(ctx context.Context, groups []string, downCut int64,
	throttleLike string, minThrottle, limit int) ([]prioTarget, error) {
	args := []any{throttleLike, downCut, minThrottle}
	groupClause := ""
	if len(groups) > 0 {
		args = append(args, pq.Array(groups))
		groupClause = fmt.Sprintf(` AND string_to_array(c."group", ',') && $%d`, len(args))
	}
	args = append(args, limit)
	limitPos := len(args)

	q := `
		WITH agg AS (
			SELECT channel_id,
			       sum(CASE WHEN type=2 THEN 1 ELSE 0 END) AS succ,
			       sum(CASE WHEN type=5 AND content ILIKE $1 THEN 1 ELSE 0 END) AS thr
			  FROM logs WHERE created_at >= $2
			 GROUP BY channel_id
		)
		SELECT c.id, COALESCE(c.name,''), COALESCE(c."group",''), COALESCE(c.priority,0),
		       COALESCE(s.base_priority,0), a.thr
		  FROM agg a
		  JOIN channels c ON c.id = a.channel_id
		  LEFT JOIN rs_channel_priority_state s ON s.channel_id = c.id
		 WHERE c.status=1 AND a.succ=0 AND a.thr >= $3` + groupClause + `
		 ORDER BY a.thr DESC
		 LIMIT $` + strconv.Itoa(limitPos)

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]prioTarget, 0, limit)
	for rows.Next() {
		var t prioTarget
		if err := rows.Scan(&t.id, &t.name, &t.group, &t.priority, &t.basePriority, &t.throttle); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// findRecoveredChannels returns channels we previously demoted (ledger row
// present, current priority below baseline) that had zero throttle errors in
// the up-window — candidates to step back up.
func findRecoveredChannels(ctx context.Context, groups []string, upCut int64,
	throttleLike string, limit int) ([]prioTarget, error) {
	args := []any{throttleLike, upCut}
	groupClause := ""
	if len(groups) > 0 {
		args = append(args, pq.Array(groups))
		groupClause = fmt.Sprintf(` AND string_to_array(c."group", ',') && $%d`, len(args))
	}
	args = append(args, limit)
	limitPos := len(args)

	q := `
		WITH agg AS (
			SELECT channel_id, sum(CASE WHEN type=5 AND content ILIKE $1 THEN 1 ELSE 0 END) AS thr
			  FROM logs WHERE created_at >= $2
			 GROUP BY channel_id
		)
		SELECT c.id, COALESCE(c.name,''), COALESCE(c."group",''), COALESCE(c.priority,0), s.base_priority
		  FROM rs_channel_priority_state s
		  JOIN channels c ON c.id = s.channel_id
		  LEFT JOIN agg a ON a.channel_id = c.id
		 WHERE c.status=1 AND c.priority < s.base_priority AND COALESCE(a.thr,0) = 0` + groupClause + `
		 ORDER BY (s.base_priority - c.priority) DESC
		 LIMIT $` + strconv.Itoa(limitPos)

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]prioTarget, 0, limit)
	for rows.Next() {
		var t prioTarget
		if err := rows.Scan(&t.id, &t.name, &t.group, &t.priority, &t.basePriority); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// applyChannelPriority writes channels.priority + abilities.priority in one
// transaction (they must stay in sync) and maintains the ledger: on demote it
// records the baseline once; on full restore it deletes the ledger row.
func applyChannelPriority(ctx context.Context, id int64, newPrio, base int, restore bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE channels SET priority=$1 WHERE id=$2`, newPrio, id); err != nil {
		return fmt.Errorf("update channels: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE abilities SET priority=$1 WHERE channel_id=$2`, newPrio, id); err != nil {
		return fmt.Errorf("update abilities: %w", err)
	}
	if restore {
		if _, err := tx.ExecContext(ctx, `DELETE FROM rs_channel_priority_state WHERE channel_id=$1`, id); err != nil {
			return fmt.Errorf("clear ledger: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO rs_channel_priority_state (channel_id, base_priority, updated_at)
			 VALUES ($1,$2,$3) ON CONFLICT (channel_id) DO UPDATE SET updated_at=$3`,
			id, base, time.Now().Unix()); err != nil {
			return fmt.Errorf("upsert ledger: %w", err)
		}
	}
	return tx.Commit()
}

func logChanPrioEvent(channelID int64, name, group, action string, from, to, base, throttle int, dryRun bool, detail string) {
	if _, err := db.Exec(`
		INSERT INTO rs_channel_priority_event
		(channel_id, channel_name, grp, action, from_priority, to_priority, base_priority, throttle_count, detail, dry_run, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		channelID, name, group, action, from, to, base, throttle,
		sanitizeUpstreamMessage(detail), dryRun, time.Now().Unix(),
	); err != nil {
		log.Printf("[chan-prio] log event %s: %v", action, err)
	}
}

// ---- HTTP handlers (V1, admin-gated) ----

func handleChanPrioConfigGet(c *gin.Context) {
	c.JSON(http.StatusOK, loadChanPrioConfig())
}

func handleChanPrioConfigSet(c *gin.Context) {
	var body struct {
		Enabled        *bool     `json:"enabled,omitempty"`
		Groups         *[]string `json:"groups,omitempty"`
		DownWindowSec  *int      `json:"down_window_sec,omitempty"`
		UpWindowSec    *int      `json:"up_window_sec,omitempty"`
		TickSec        *int      `json:"tick_sec,omitempty"`
		Step           *int      `json:"step,omitempty"`
		MaxDrop        *int      `json:"max_drop,omitempty"`
		MinThrottle    *int      `json:"min_throttle,omitempty"`
		ThrottleSubstr *string   `json:"throttle_substr,omitempty"`
		MaxActions     *int      `json:"max_actions,omitempty"`
		DryRun         *bool     `json:"dry_run,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	fail := func(err error) bool {
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return true
		}
		return false
	}
	setBool := func(key string, v *bool) error {
		if v == nil {
			return nil
		}
		s := "false"
		if *v {
			s = "true"
		}
		return writeChanPrioConfig(key, s)
	}
	setInt := func(key string, v *int, min, max int) error {
		if v == nil {
			return nil
		}
		return writeChanPrioConfig(key, strconv.Itoa(clampInt(*v, min, max)))
	}

	if fail(setBool(cfgChanPrioEnabled, body.Enabled)) ||
		fail(setBool(cfgChanPrioDryRun, body.DryRun)) ||
		fail(setInt(cfgChanPrioDownWindowSec, body.DownWindowSec, chanPrioDownWindowMin, chanPrioDownWindowMax)) ||
		fail(setInt(cfgChanPrioUpWindowSec, body.UpWindowSec, chanPrioUpWindowMin, chanPrioUpWindowMax)) ||
		fail(setInt(cfgChanPrioTickSec, body.TickSec, chanPrioTickMin, chanPrioTickMax)) ||
		fail(setInt(cfgChanPrioStep, body.Step, chanPrioStepMin, chanPrioStepMax)) ||
		fail(setInt(cfgChanPrioMaxDrop, body.MaxDrop, chanPrioMaxDropMin, chanPrioMaxDropMax)) ||
		fail(setInt(cfgChanPrioMinThrottle, body.MinThrottle, chanPrioMinThrottleMin, chanPrioMinThrottleMax)) ||
		fail(setInt(cfgChanPrioMaxActions, body.MaxActions, chanPrioMaxActionsMin, chanPrioMaxActionsMax)) {
		return
	}
	if body.Groups != nil {
		if fail(writeChanPrioConfig(cfgChanPrioGroups, strings.Join(cleanCSVList(*body.Groups), ","))) {
			return
		}
	}
	if body.ThrottleSubstr != nil {
		if s := strings.TrimSpace(*body.ThrottleSubstr); s != "" {
			if fail(writeChanPrioConfig(cfgChanPrioThrottleSub, s)) {
				return
			}
		}
	}
	nudgeChanPrio()
	c.JSON(http.StatusOK, loadChanPrioConfig())
}

func handleChanPrioEvents(c *gin.Context) {
	limit := 200
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = clampInt(n, 1, 2000)
		}
	}
	filters := []string{}
	args := []any{}
	if raw := strings.TrimSpace(c.Query("channel_id")); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			args = append(args, id)
			filters = append(filters, fmt.Sprintf(`channel_id = $%d`, len(args)))
		}
	}
	if action := strings.TrimSpace(c.Query("action")); action != "" {
		args = append(args, action)
		filters = append(filters, fmt.Sprintf(`action = $%d`, len(args)))
	}
	where := ""
	if len(filters) > 0 {
		where = " WHERE " + strings.Join(filters, " AND ")
	}
	args = append(args, limit)
	rows, err := db.Query(`
		SELECT id, channel_id, channel_name, grp, action, from_priority, to_priority, base_priority, throttle_count, detail, dry_run, created_at
		  FROM rs_channel_priority_event`+where+
		` ORDER BY id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, limit)
	for rows.Next() {
		var (
			id, channelID, createdAt         int64
			from, to, base, throttle         int
			name, group, action, det         string
			dryRun                           bool
		)
		if err := rows.Scan(&id, &channelID, &name, &group, &action, &from, &to, &base, &throttle, &det, &dryRun, &createdAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		items = append(items, gin.H{
			"id": id, "channel_id": channelID, "channel_name": name, "group": group,
			"action": action, "from_priority": from, "to_priority": to, "base_priority": base,
			"throttle_count": throttle, "detail": det, "dry_run": dryRun, "created_at": createdAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"events": items})
}

// handleChanPrioStatus reports how many channels are currently demoted (below
// their baseline) so the UI can show the live state at a glance.
func handleChanPrioStatus(c *gin.Context) {
	var demoted int
	_ = db.QueryRow(`SELECT count(*) FROM rs_channel_priority_state s
		JOIN channels c ON c.id=s.channel_id WHERE c.priority < s.base_priority`).Scan(&demoted)
	rows, err := db.Query(`
		SELECT c.id, COALESCE(c.name,''), COALESCE(c."group",''), COALESCE(c.priority,0), s.base_priority, s.updated_at
		  FROM rs_channel_priority_state s
		  JOIN channels c ON c.id = s.channel_id
		 WHERE c.priority < s.base_priority
		 ORDER BY (s.base_priority - c.priority) DESC
		 LIMIT 500`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, 64)
	for rows.Next() {
		var (
			id, updated             int64
			name, group             string
			priority, base          int
		)
		if err := rows.Scan(&id, &name, &group, &priority, &base, &updated); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		items = append(items, gin.H{
			"channel_id": id, "channel_name": name, "group": group,
			"priority": priority, "base_priority": base, "updated_at": updated,
		})
	}
	c.JSON(http.StatusOK, gin.H{"demoted_count": demoted, "demoted": items})
}

func handleChanPrioRunNow(c *gin.Context) {
	if !IsLeader() {
		c.JSON(http.StatusConflict, gin.H{"error": "this node is not the scheduler leader; try again"})
		return
	}
	cfg := loadChanPrioConfig()
	sum := runChanPrioTick(c.Request.Context(), cfg)
	c.JSON(http.StatusOK, gin.H{"summary": sum, "dry_run": cfg.DryRun})
}
