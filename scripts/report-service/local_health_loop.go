package main

// Local model health scheduler.
//
// One leader-gated loop, three phases per tick:
//
//	expand    — materialize the covered (channel, model) set from the rules,
//	            seed new rows, drop rows no longer covered.
//	probe     — claim due rows from the queue and test them against the local
//	            new-api, concurrently but bounded.
//	reconcile — converge each covered channel's models onto the set its health
//	            implies, and flip its status when that set empties or refills.
//
// Only reconcile writes to new-api, only when the matching rule has
// enforce=true, and only under a per-tick action budget. Everything else is
// local SQL.

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"
)

// localHealthNudge wakes the loop early after a config or rule change,
// mirroring localPoolNudge in local_pool.go.
var localHealthNudge = make(chan struct{}, 1)

func nudgeLocalHealth() {
	select {
	case localHealthNudge <- struct{}{}:
	default:
	}
}

// tickStats is one tick's outcome, surfaced on the status endpoint so an
// operator can tell a healthy idle loop from a stuck one.
type tickStats struct {
	At          int64 `json:"at"`
	Probed      int   `json:"probed"`
	OK          int   `json:"ok"`
	ModelDown   int   `json:"model_down"`
	ChannelDown int   `json:"channel_down"`
	Throttled   int   `json:"throttled"`
	Neutral     int   `json:"neutral"`
	Unsupported int   `json:"unsupported"`
	Actions     int   `json:"actions"`
	BreakerOpen bool  `json:"breaker_open"`
	DurationMS  int64 `json:"duration_ms"`
}

var (
	lastTickMu   sync.RWMutex
	lastTickData tickStats
)

func lastLocalHealthTick() tickStats {
	lastTickMu.RLock()
	defer lastTickMu.RUnlock()
	return lastTickData
}

func recordLocalHealthTick(st tickStats) {
	lastTickMu.Lock()
	lastTickData = st
	lastTickMu.Unlock()
}

func startLocalHealthLoop() {
	if !localNewAPIConfigured() {
		log.Printf("[local-health] MAIN_SERVICE_TOKEN not set; scheduler disabled")
		return
	}
	log.Printf("[local-health] loop starting")
	go func() {
		// Stagger past the pool schedulers' first ticks so a cold start
		// doesn't fire everything at once.
		time.Sleep(60 * time.Second)
		for {
			cfg := loadLocalHealthConfig()
			if cfg.Enabled && IsLeader() {
				runLocalHealthTickOnce(cfg)
			}
			select {
			case <-time.After(time.Duration(cfg.TickSec) * time.Second):
			case <-localHealthNudge:
			}
		}
	}()
	go localHealthPruneLoop()
}

// localHealthPruneLoop trims the event log. Node-local timing is fine; the
// DELETE is idempotent and leader-gated anyway.
func localHealthPruneLoop() {
	time.Sleep(5 * time.Minute)
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		if !IsLeader() {
			continue
		}
		cutoff := time.Now().Add(-localHealthEventRetention).Unix()
		if _, err := db.Exec(`DELETE FROM local_model_health_event WHERE created_at < $1`, cutoff); err != nil {
			log.Printf("[local-health] prune events: %v", err)
		}
	}
}

func runLocalHealthTickOnce(cfg localHealthConfig) {
	started := time.Now()
	st := tickStats{At: started.Unix()}

	rules, err := loadLocalHealthRules(true)
	if err != nil {
		log.Printf("[local-health] load rules: %v", err)
		return
	}
	if len(rules) == 0 {
		recordLocalHealthTick(st)
		return
	}

	coverage, err := expandLocalHealthCoverage(rules)
	if err != nil {
		log.Printf("[local-health] expand: %v", err)
		return
	}
	if len(coverage) == 0 {
		st.DurationMS = time.Since(started).Milliseconds()
		recordLocalHealthTick(st)
		return
	}

	changed := runLocalProbeBatch(cfg, coverage, &st)

	// A tick where most probes never reached new-api tells us nothing about
	// upstream health — it tells us our own gateway or token is broken. Acting
	// on that would demote the whole fleet.
	if st.Probed > 0 && st.Neutral*2 > st.Probed {
		st.BreakerOpen = true
		logHealthEvent(0, "", 0, "breaker_tripped",
			fmt.Sprintf("%d/%d probes failed before reaching new-api; skipping reconcile", st.Neutral, st.Probed), false)
		st.DurationMS = time.Since(started).Milliseconds()
		recordLocalHealthTick(st)
		return
	}

	// Reconcile every covered channel, not only the ones that moved this tick.
	// Drift detection is a local map comparison against the `channels` row we
	// already read, so a steady-state channel costs nothing — and an operator
	// who flips a rule from observe to enforce gets convergence on the next
	// tick instead of waiting for the next state transition.
	budget := cfg.MaxActionsPerTick
	for _, cov := range coverage {
		if budget <= 0 {
			break
		}
		// electOnce steps down on any DB error, so leadership can change
		// mid-tick. Re-check before every mutation, not just per tick.
		if !IsLeader() {
			break
		}
		if reconcileLocalChannel(cov, changed[cov.Channel.ID], &st) {
			budget--
		}
	}
	st.DurationMS = time.Since(started).Milliseconds()
	recordLocalHealthTick(st)
}

// ---- Phase A: expand ----

// localChannelRow is the subset of `channels` the scheduler needs.
type localChannelRow struct {
	ID           int64
	Name         string
	Type         int
	Status       int
	Group        string
	Tag          string
	Models       string
	DisabledByUs bool
}

// channelCoverage is one managed channel plus the merged rule state that
// applies to it. Rules union: candidate models merge, thresholds take the
// strictest value, and enforce is on if any matching rule enforces.
type channelCoverage struct {
	Channel     localChannelRow
	RuleID      int64
	Candidates  []string
	Enforce     bool
	Thresholds  healthThresholds
	IntervalSec int
}

// matchLocalHealthChannels returns the channels one rule selects.
//
// status IN (1, 3) plus our own status=2 disables: a channel new-api auto-
// banned (3) is exactly the case the operator wants recovered when a model
// comes back, while a status=2 channel we did not disable is a deliberate
// human decision the scheduler must not override.
//
// Multi-key channels are excluded: a single probe round-robins across keys
// (model/channel.go GetNextEnabledKey), so per-model verdicts would be noise.
func matchLocalHealthChannels(rule localHealthRule) ([]localChannelRow, error) {
	conds := []string{
		`(c.status IN (1, 3) OR (c.status = 2 AND COALESCE(l.disabled_by_us, FALSE)))`,
		`COALESCE(c.channel_info->>'is_multi_key', 'false') <> 'true'`,
	}
	args := []any{}
	if rule.MatchTag != "" {
		args = append(args, rule.MatchTag)
		conds = append(conds, fmt.Sprintf(`c.tag = $%d`, len(args)))
	}
	if rule.MatchType >= 0 {
		args = append(args, rule.MatchType)
		conds = append(conds, fmt.Sprintf(`c.type = $%d`, len(args)))
	}
	if rule.MatchGroup != "" {
		args = append(args, rule.MatchGroup)
		conds = append(conds, fmt.Sprintf(`c."group" = $%d`, len(args)))
	}
	if ids := csvSplitModels(rule.MatchChannelIDs); len(ids) > 0 {
		parsed := make([]int64, 0, len(ids))
		for _, raw := range ids {
			if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
				parsed = append(parsed, n)
			}
		}
		if len(parsed) == 0 {
			return nil, nil
		}
		placeholders := make([]string, 0, len(parsed))
		for _, n := range parsed {
			args = append(args, n)
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		}
		conds = append(conds, `c.id IN (`+strings.Join(placeholders, ",")+`)`)
	}

	rows, err := db.Query(`
		SELECT c.id, COALESCE(c.name,''), c.type, c.status, COALESCE(c."group",''),
		       COALESCE(c.tag,''), COALESCE(c.models,''), COALESCE(l.disabled_by_us, FALSE)
		  FROM channels c
		  LEFT JOIN local_model_health_channel l ON l.channel_id = c.id
		 WHERE `+strings.Join(conds, " AND ")+`
		 ORDER BY c.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]localChannelRow, 0, 64)
	for rows.Next() {
		var r localChannelRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Type, &r.Status, &r.Group, &r.Tag,
			&r.Models, &r.DisabledByUs); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ruleCandidateModels resolves which models a rule manages on one channel.
// An empty candidate_models falls back to the configured batch-create list for
// the channel's type rather than the channel's current models — otherwise the
// scheduler could only ever shrink a channel, never discover a model the
// channel can serve but wasn't configured with.
func ruleCandidateModels(rule localHealthRule, ch localChannelRow) []string {
	if models := csvSplitModels(rule.CandidateModels); len(models) > 0 {
		return models
	}
	return csvSplitModels(getBatchCreateModels(ch.Type))
}

func expandLocalHealthCoverage(rules []localHealthRule) (map[int64]*channelCoverage, error) {
	coverage := make(map[int64]*channelCoverage, 128)
	for _, rule := range rules {
		channels, err := matchLocalHealthChannels(rule)
		if err != nil {
			return nil, fmt.Errorf("match rule %d: %w", rule.ID, err)
		}
		for _, ch := range channels {
			cov, ok := coverage[ch.ID]
			if !ok {
				cov = &channelCoverage{
					Channel: ch,
					RuleID:  rule.ID,
					Thresholds: healthThresholds{
						DownWindowSec: rule.DownWindowSec,
						DownFailMin:   rule.DownFailMin,
						RecoverOKMin:  rule.RecoverOKMin,
					},
					IntervalSec: rule.ProbeIntervalSec,
				}
				coverage[ch.ID] = cov
			}
			// Strictest wins on overlap: shortest window, fewest failures to
			// demote, most successes to recover, shortest probe interval.
			if rule.DownWindowSec < cov.Thresholds.DownWindowSec {
				cov.Thresholds.DownWindowSec = rule.DownWindowSec
			}
			if rule.DownFailMin < cov.Thresholds.DownFailMin {
				cov.Thresholds.DownFailMin = rule.DownFailMin
			}
			if rule.RecoverOKMin > cov.Thresholds.RecoverOKMin {
				cov.Thresholds.RecoverOKMin = rule.RecoverOKMin
			}
			if rule.ProbeIntervalSec < cov.IntervalSec {
				cov.IntervalSec = rule.ProbeIntervalSec
			}
			cov.Enforce = cov.Enforce || rule.Enforce
			cov.Candidates = mergeModels(cov.Candidates, ruleCandidateModels(rule, ch))
		}
	}

	if err := syncHealthRowsToCoverage(coverage); err != nil {
		return nil, err
	}
	if err := pullForwardErroringPairs(coverage); err != nil {
		log.Printf("[local-health] error-log fast path: %v", err)
	}
	return coverage, nil
}

func mergeModels(existing, incoming []string) []string {
	seen := make(map[string]bool, len(existing)+len(incoming))
	out := make([]string, 0, len(existing)+len(incoming))
	for _, list := range [][]string{existing, incoming} {
		for _, m := range list {
			if seen[m] {
				continue
			}
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// healthPair identifies one managed (channel, model).
type healthPair struct {
	channelID int64
	model     string
}

// syncHealthRowsToCoverage makes local_model_health match the covered set:
// insert what's new, delete what no rule owns any more (including pairs whose
// channel disappeared from `channels`).
//
// It reads the existing pairs once and writes only the difference. A per-pair
// UPSERT would be a couple of thousand round trips every tick on a five
// connection pool.
func syncHealthRowsToCoverage(coverage map[int64]*channelCoverage) error {
	rows, err := db.Query(`SELECT channel_id, model FROM local_model_health`)
	if err != nil {
		return err
	}
	existing := make(map[healthPair]bool, 512)
	for rows.Next() {
		var p healthPair
		if err := rows.Scan(&p.channelID, &p.model); err != nil {
			rows.Close()
			return err
		}
		existing[p] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	wanted := make(map[healthPair]int64, len(existing)+16)
	for _, cov := range coverage {
		for _, model := range cov.Candidates {
			wanted[healthPair{cov.Channel.ID, model}] = cov.RuleID
		}
	}

	now := time.Now().Unix()
	// last_ok_at seeded to now is load-bearing: a zero value would satisfy
	// `now - last_ok_at >= down_window_sec` immediately, so the first failed
	// probe of a brand-new row would demote it. next_check_at seeded to now
	// with no jitter so a freshly uploaded key resolves its models within a
	// tick or two rather than after a full interval.
	const insertChunk = 200
	pending := make([]any, 0, insertChunk*5)
	placeholders := make([]string, 0, insertChunk)
	flush := func() error {
		if len(placeholders) == 0 {
			return nil
		}
		_, err := db.Exec(`
			INSERT INTO local_model_health
			(channel_id, model, rule_id, state, last_ok_at, next_check_at, updated_at)
			VALUES `+strings.Join(placeholders, ",")+`
			ON CONFLICT (channel_id, model) DO NOTHING`, pending...)
		pending = pending[:0]
		placeholders = placeholders[:0]
		return err
	}
	for p, ruleID := range wanted {
		if existing[p] {
			continue
		}
		n := len(pending)
		pending = append(pending, p.channelID, p.model, ruleID, healthStateUnknown, now)
		placeholders = append(placeholders, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			n+1, n+2, n+3, n+4, n+5, n+5, n+5))
		if len(placeholders) >= insertChunk {
			if err := flush(); err != nil {
				return fmt.Errorf("seed health rows: %w", err)
			}
		}
	}
	if err := flush(); err != nil {
		return fmt.Errorf("seed health rows: %w", err)
	}

	for p := range existing {
		if _, ok := wanted[p]; ok {
			continue
		}
		if _, err := db.Exec(
			`DELETE FROM local_model_health WHERE channel_id=$1 AND model=$2`,
			p.channelID, p.model,
		); err != nil {
			return err
		}
	}
	return nil
}

// pullForwardErroringPairs is the cost lever. Probes are real billed calls, so
// the steady-state interval is long (an hour by default); real traffic errors
// that new-api already writes to `logs` are a free, zero-latency signal that a
// pair deserves an immediate re-probe.
func pullForwardErroringPairs(coverage map[int64]*channelCoverage) error {
	if len(coverage) == 0 {
		return nil
	}
	now := time.Now().Unix()
	_, err := db.Exec(`
		UPDATE local_model_health h
		   SET next_check_at = $1
		  FROM (
			SELECT channel_id, model_name
			  FROM logs
			 WHERE type = 5 AND created_at >= $2
			 GROUP BY channel_id, model_name
			HAVING COUNT(*) >= 3
		  ) e
		 WHERE h.channel_id = e.channel_id
		   AND h.model = e.model_name
		   AND h.state <> $3
		   AND h.next_check_at > $1`,
		now, now-300, healthStateUnsupported)
	return err
}

// ---- Phase B: probe ----

// claimDueLocalProbes atomically selects due rows and pushes their
// next_check_at forward in the same statement, so a probe still in flight when
// the next tick fires is not dispatched twice. Doing it in the DB rather than
// an in-memory set means restarts and leadership handovers stay correct.
func claimDueLocalProbes(cfg localHealthConfig, coverage map[int64]*channelCoverage, bootstrap bool) ([]healthRow, error) {
	limit := cfg.ProbeBatch
	stateCmp := "<>"
	if bootstrap {
		limit = cfg.BootstrapBatch
		stateCmp = "="
	}
	now := time.Now()
	// Park the claimed rows one timeout-plus-slack ahead; the real interval is
	// written back after the probe completes.
	parkUntil := now.Unix() + int64(cfg.ProbeTimeoutSec) + 30

	rows, err := db.Query(`
		UPDATE local_model_health
		   SET next_check_at = $1
		 WHERE ctid IN (
			SELECT ctid FROM local_model_health
			 WHERE next_check_at <= $2
			   AND state <> $3
			   AND state `+stateCmp+` $4
			 ORDER BY next_check_at
			 LIMIT $5
			 FOR UPDATE SKIP LOCKED
		 )
		RETURNING channel_id, model, rule_id, state, consecutive_ok, consecutive_fail,
		          last_ok_at, last_checked_at, next_check_at, last_class, last_error, last_latency_ms`,
		parkUntil, now.Unix(), healthStateUnsupported, healthStateUnknown, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]healthRow, 0, limit)
	for rows.Next() {
		var h healthRow
		if err := rows.Scan(&h.ChannelID, &h.Model, &h.RuleID, &h.State, &h.ConsecutiveOK,
			&h.ConsecutiveFail, &h.LastOKAt, &h.LastCheckedAt, &h.NextCheckAt,
			&h.LastClass, &h.LastError, &h.LastLatencyMS); err != nil {
			return nil, err
		}
		// A row can outlive its coverage between expand and claim; skip it
		// rather than probing something no rule owns.
		if _, ok := coverage[h.ChannelID]; !ok {
			continue
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// runLocalProbeBatch probes the due rows and returns the set of channels whose
// health changed state this tick.
func runLocalProbeBatch(cfg localHealthConfig, coverage map[int64]*channelCoverage, st *tickStats) map[int64]bool {
	// Two queues, two budgets: rows that have never been probed get a much
	// larger allowance so a 200-channel upload resolves in a few ticks instead
	// of trickling out over an hour.
	due := make([]healthRow, 0, cfg.BootstrapBatch+cfg.ProbeBatch)
	for _, bootstrap := range []bool{true, false} {
		claimed, err := claimDueLocalProbes(cfg, coverage, bootstrap)
		if err != nil {
			log.Printf("[local-health] claim due (bootstrap=%v): %v", bootstrap, err)
			continue
		}
		due = append(due, claimed...)
	}
	if len(due) == 0 {
		return nil
	}

	timeout := time.Duration(cfg.ProbeTimeoutSec) * time.Second
	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup
	results := make([]probeResult, len(due))

	for i := range due {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx] = testLocalChannelModel(context.Background(),
				due[idx].ChannelID, due[idx].Model, timeout)
		}(i)
	}
	wg.Wait()

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	now := time.Now()
	changedSet := make(map[int64]bool, 8)
	// A channel-scope failure means the credential or endpoint is dead, so
	// every model on it is equally unusable. Fanning out spares N-1 billed
	// probes per tick on a channel whose key was simply revoked.
	channelWide := make(map[int64]probeResult, 4)

	for i, h := range due {
		res := results[i]
		st.Probed++
		switch res.Class {
		case probeOK:
			st.OK++
		case probeModelDown:
			st.ModelDown++
		case probeChannelDown:
			st.ChannelDown++
			channelWide[h.ChannelID] = res
		case probeThrottle:
			st.Throttled++
		case probeUnsupported:
			st.Unsupported++
			markChannelProbeUnsupported(h.ChannelID, res.Message)
		default:
			st.Neutral++
		}

		cov := coverage[h.ChannelID]
		if cov == nil {
			continue
		}
		before := h.State
		updated := applyProbe(h, res, cov.Thresholds, now)
		updated.NextCheckAt = nextCheckAt(now, probeInterval(updated.State, cov.IntervalSec),
			localHealthJitterPct, rng)
		if err := persistHealthRow(updated); err != nil {
			log.Printf("[local-health] persist %d/%s: %v", h.ChannelID, h.Model, err)
			continue
		}
		if updated.State != before {
			changedSet[h.ChannelID] = true
			logStateTransition(cov, updated, before, res)
		}
	}

	for channelID, res := range channelWide {
		cov := coverage[channelID]
		if cov == nil {
			continue
		}
		if fanOutChannelFailure(cov, res, now, rng) {
			changedSet[channelID] = true
		}
	}
	return changedSet
}

// probeInterval picks the re-probe cadence for a state. Healthy models are
// checked rarely (probes cost money and the error-log fast path covers
// regressions); everything else is checked several times faster so both
// demotion and recovery land inside the configured window.
func probeInterval(state string, intervalSec int) time.Duration {
	base := time.Duration(intervalSec) * time.Second
	switch state {
	case healthStateUp:
		return base
	case healthStateUnsupported:
		return localHealthUnsupportedInterval
	default:
		fast := base / 6
		if fast < localHealthFastIntervalMin {
			fast = localHealthFastIntervalMin
		}
		return fast
	}
}

func persistHealthRow(h healthRow) error {
	_, err := db.Exec(`
		UPDATE local_model_health
		   SET state=$1, consecutive_ok=$2, consecutive_fail=$3, last_ok_at=$4,
		       last_checked_at=$5, next_check_at=$6, last_class=$7, last_error=$8,
		       last_latency_ms=$9, updated_at=$5
		 WHERE channel_id=$10 AND model=$11`,
		h.State, h.ConsecutiveOK, h.ConsecutiveFail, h.LastOKAt, h.LastCheckedAt,
		h.NextCheckAt, h.LastClass, h.LastError, h.LastLatencyMS, h.ChannelID, h.Model)
	return err
}

// fanOutChannelFailure applies a channel-scope verdict to every other managed
// model on that channel, so one dead key doesn't cost one billed probe per
// model per tick. Returns true if any row changed state.
func fanOutChannelFailure(cov *channelCoverage, res probeResult, now time.Time, rng *rand.Rand) bool {
	rows, err := db.Query(`
		SELECT channel_id, model, rule_id, state, consecutive_ok, consecutive_fail,
		       last_ok_at, last_checked_at, next_check_at, last_class, last_error, last_latency_ms
		  FROM local_model_health
		 WHERE channel_id=$1 AND state <> $2 AND last_checked_at < $3`,
		cov.Channel.ID, healthStateUnsupported, now.Unix())
	if err != nil {
		log.Printf("[local-health] fan-out query %d: %v", cov.Channel.ID, err)
		return false
	}
	defer rows.Close()

	pending := make([]healthRow, 0, 8)
	for rows.Next() {
		var h healthRow
		if err := rows.Scan(&h.ChannelID, &h.Model, &h.RuleID, &h.State, &h.ConsecutiveOK,
			&h.ConsecutiveFail, &h.LastOKAt, &h.LastCheckedAt, &h.NextCheckAt,
			&h.LastClass, &h.LastError, &h.LastLatencyMS); err != nil {
			log.Printf("[local-health] fan-out scan %d: %v", cov.Channel.ID, err)
			return false
		}
		pending = append(pending, h)
	}

	changed := false
	for _, h := range pending {
		before := h.State
		updated := applyProbe(h, res, cov.Thresholds, now)
		updated.NextCheckAt = nextCheckAt(now, probeInterval(updated.State, cov.IntervalSec),
			localHealthJitterPct, rng)
		if err := persistHealthRow(updated); err != nil {
			log.Printf("[local-health] fan-out persist %d/%s: %v", h.ChannelID, h.Model, err)
			continue
		}
		if updated.State != before {
			changed = true
			logStateTransition(cov, updated, before, res)
		}
	}
	return changed
}

func markChannelProbeUnsupported(channelID int64, detail string) {
	now := time.Now().Unix()
	if _, err := db.Exec(`
		INSERT INTO local_model_health_channel (channel_id, probe_supported, last_error, updated_at)
		VALUES ($1, FALSE, $2, $3)
		ON CONFLICT (channel_id) DO UPDATE SET probe_supported = FALSE, last_error = $2, updated_at = $3`,
		channelID, detail, now,
	); err != nil {
		log.Printf("[local-health] mark unsupported %d: %v", channelID, err)
	}
}

func logStateTransition(cov *channelCoverage, h healthRow, before string, res probeResult) {
	kind := ""
	switch h.State {
	case healthStateDown:
		kind = "model_down"
	case healthStateUp:
		kind = "model_up"
	case healthStateUnsupported:
		kind = "unsupported"
	default:
		return
	}
	detail := fmt.Sprintf("%s -> %s (class=%s fail=%d ok=%d): %s",
		before, h.State, res.Class, h.ConsecutiveFail, h.ConsecutiveOK, res.Message)
	logHealthEvent(h.ChannelID, h.Model, cov.RuleID, kind, detail, !cov.Enforce)
}

func logHealthEvent(channelID int64, model string, ruleID int64, kind, detail string, dryRun bool) {
	if _, err := db.Exec(`
		INSERT INTO local_model_health_event
		(channel_id, model, rule_id, kind, detail, dry_run, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		channelID, model, ruleID, kind, sanitizeUpstreamMessage(detail), dryRun, time.Now().Unix(),
	); err != nil {
		log.Printf("[local-health] log event %s: %v", kind, err)
	}
}

// ---- Phase C: reconcile ----

// reconcileLocalChannel converges one channel onto the model set its health
// state implies. Returns true if it spent an action from the tick budget.
//
// It compares against desired state rather than emitting deltas, so a
// concurrent edit or a failed write can never compound: the next tick simply
// recomputes the same target. The comparison reads `channels` directly —
// report-service shares new-api's database, so that row is authoritative and
// never stale, and a steady-state channel costs no HTTP at all. That matters:
// every PUT makes new-api rebuild its entire channel routing cache.
//
// transitioned says whether this channel's health moved this tick. In observe
// mode it gates the "would change" events so a persistent dry-run diff doesn't
// write one event per channel per tick forever.
func reconcileLocalChannel(cov *channelCoverage, transitioned bool, st *tickStats) bool {
	states, err := loadChannelModelStates(cov.Channel.ID)
	if err != nil {
		log.Printf("[local-health] load states %d: %v", cov.Channel.ID, err)
		return false
	}

	current := csvSplitModels(cov.Channel.Models)
	currentCSV := csvJoinModels(current)
	desired := desiredChannelModels(current, cov.Candidates, states)

	// Never let a removal take the last enabled channel serving a model out of
	// rotation: turning a degraded model into a completely unroutable one is
	// the only truly unacceptable failure mode here.
	desired = applyModelFloor(cov, current, desired, cov.Channel.Group)
	desiredCSV := csvJoinModels(desired)

	wantStatus := cov.Channel.Status
	switch {
	case len(desired) == 0 && cov.Channel.Status != 2:
		wantStatus = 2
	case len(desired) > 0 && (cov.Channel.Status == 3 || (cov.Channel.Status == 2 && cov.Channel.DisabledByUs)):
		// Recover: either new-api auto-banned it or we disabled it ourselves.
		wantStatus = 1
	}
	modelsDiffer := len(desired) > 0 && desiredCSV != currentCSV
	if !modelsDiffer && wantStatus == cov.Channel.Status {
		return false
	}

	if !cov.Enforce {
		if transitioned {
			logHealthEvent(cov.Channel.ID, "", cov.RuleID, dryRunKind(modelsDiffer, wantStatus),
				fmt.Sprintf("dry-run: models %q -> %q, status %d -> %d",
					currentCSV, desiredCSV, cov.Channel.Status, wantStatus), true)
		}
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	acted := false

	if modelsDiffer {
		if err := patchLocalChannelModels(ctx, cov.Channel.ID, desiredCSV); err != nil {
			logHealthEvent(cov.Channel.ID, "", cov.RuleID, "action_failed",
				"update models: "+err.Error(), false)
			return false
		}
		cov.Channel.Models = desiredCSV
		logHealthEvent(cov.Channel.ID, "", cov.RuleID, "model_removed",
			"models "+currentCSV+" -> "+desiredCSV, false)
		st.Actions++
		acted = true
	}

	if wantStatus != cov.Channel.Status {
		if err := setLocalChannelStatus(ctx, cov.Channel.ID, wantStatus); err != nil {
			logHealthEvent(cov.Channel.ID, "", cov.RuleID, "action_failed",
				fmt.Sprintf("set status %d: %v", wantStatus, err), false)
			return acted
		}
		if wantStatus == 2 {
			markChannelDisabledByUs(cov.Channel.ID, currentCSV)
			logHealthEvent(cov.Channel.ID, "", cov.RuleID, "channel_disabled",
				"no candidate model is usable; models at disable time: "+currentCSV, false)
		} else {
			clearChannelDisabledByUs(cov.Channel.ID)
			logHealthEvent(cov.Channel.ID, "", cov.RuleID, "channel_enabled",
				"usable models: "+desiredCSV, false)
		}
		cov.Channel.Status = wantStatus
		st.Actions++
		acted = true
	}
	return acted
}

func dryRunKind(modelsDiffer bool, wantStatus int) string {
	if !modelsDiffer {
		if wantStatus == 2 {
			return "channel_disabled"
		}
		return "channel_enabled"
	}
	return "model_removed"
}

func loadChannelModelStates(channelID int64) (map[string]string, error) {
	rows, err := db.Query(
		`SELECT model, state FROM local_model_health WHERE channel_id=$1`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string, 8)
	for rows.Next() {
		var model, state string
		if err := rows.Scan(&model, &state); err != nil {
			return nil, err
		}
		out[model] = state
	}
	return out, rows.Err()
}

// applyModelFloor puts back any model whose removal would leave no enabled
// channel serving it in that group. `abilities` is already the (group, model,
// channel) routing table, so the check is one indexed query per removal.
func applyModelFloor(cov *channelCoverage, current, desired []string, group string) []string {
	// Compare membership, not length: a tick can remove one model and add
	// another back, which leaves the count unchanged but still needs the check.
	keep := make(map[string]bool, len(desired))
	for _, m := range desired {
		keep[m] = true
	}
	removals := false
	for _, m := range current {
		if !keep[m] {
			removals = true
			break
		}
	}
	if !removals {
		return desired
	}
	restored := false
	for _, m := range current {
		if keep[m] {
			continue
		}
		blocked, err := localModelFloorBlocked(group, m, cov.Channel.ID)
		if err != nil {
			// Fail safe: if we can't prove another channel serves this model,
			// don't remove it.
			log.Printf("[local-health] floor check %d/%s: %v", cov.Channel.ID, m, err)
			blocked = true
		}
		if blocked {
			keep[m] = true
			restored = true
			logHealthEvent(cov.Channel.ID, m, cov.RuleID, "floor_blocked",
				"kept: no other enabled channel serves this model in group "+group, !cov.Enforce)
		}
	}
	if !restored {
		return desired
	}
	// Rebuild in the original order so the CSV stays stable.
	out := make([]string, 0, len(current)+len(desired))
	seen := make(map[string]bool, len(current)+len(desired))
	for _, m := range current {
		if keep[m] && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	for _, m := range desired {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// localModelFloorBlocked reports whether removing `model` from `exceptChannel`
// would leave the (group, model) pair with no enabled channel at all. A
// comma-separated channels."group" expands to one abilities row per group, so
// each group is checked independently.
func localModelFloorBlocked(group, model string, exceptChannel int64) (bool, error) {
	groups := csvSplitModels(group)
	if len(groups) == 0 {
		groups = []string{"default"}
	}
	for _, g := range groups {
		var other int
		if err := db.QueryRow(`
			SELECT COUNT(*)
			  FROM abilities a
			  JOIN channels c ON c.id = a.channel_id
			 WHERE a."group" = $1 AND a.model = $2 AND a.enabled
			   AND c.status = 1 AND c.id <> $3`,
			g, model, exceptChannel).Scan(&other); err != nil {
			return false, err
		}
		if other == 0 {
			return true, nil
		}
	}
	return false, nil
}

func markChannelDisabledByUs(channelID int64, removedModels string) {
	now := time.Now().Unix()
	if _, err := db.Exec(`
		INSERT INTO local_model_health_channel
		(channel_id, disabled_by_us, disabled_at, removed_models, last_action_at, updated_at)
		VALUES ($1, TRUE, $2, $3, $2, $2)
		ON CONFLICT (channel_id) DO UPDATE
		   SET disabled_by_us = TRUE, disabled_at = $2, removed_models = $3,
		       last_action_at = $2, updated_at = $2`,
		channelID, now, removedModels,
	); err != nil {
		log.Printf("[local-health] ledger disable %d: %v", channelID, err)
	}
}

func clearChannelDisabledByUs(channelID int64) {
	now := time.Now().Unix()
	if _, err := db.Exec(`
		INSERT INTO local_model_health_channel
		(channel_id, disabled_by_us, removed_models, last_action_at, updated_at)
		VALUES ($1, FALSE, '', $2, $2)
		ON CONFLICT (channel_id) DO UPDATE
		   SET disabled_by_us = FALSE, removed_models = '', last_action_at = $2, updated_at = $2`,
		channelID, now,
	); err != nil {
		log.Printf("[local-health] ledger enable %d: %v", channelID, err)
	}
}
