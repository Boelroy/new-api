package main

// Remote model cooldown on upstream rate limits.
//
// A leader-gated loop over the remote_error_log mirror (filled every 60s by
// startRemoteErrorLogSync). When a remote channel keeps failing a model with
// an OpenAI-style "Rate limit reached for <model> ... (TPM): Limit L, Used U,
// Requested R" 429, the model is stripped from that channel's models list so
// the remote stops routing it there, and re-added once a cooldown elapses.
//
// The cooldown follows the reported limit: one limit window (60s for per-min
// limits) plus the time the overflow needs to drain at the limit's rate, never
// below min_cooldown nor below the upstream's own "try again in". A channel
// that trips again within escalate_window of its last restore doubles its
// cooldown, capped at max_cooldown (per-day limits cap at 24h instead).
//
// Safety: a model is never stripped from the last min_keep enabled channels
// that serve it in the channel's groups, nor when it is the channel's only
// model. Restores run even while the loop is disabled so nothing stays
// stripped. Every action is audited in rs_remote_model_cooldown_event; the
// ledger rs_remote_model_cooldown remembers what to restore and the trip
// count used for escalation.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

const (
	cfgRMCEnabled        = "rmc_enabled"
	cfgRMCProfiles       = "rmc_profiles" // CSV of profile ids
	cfgRMCModels         = "rmc_models"   // CSV, empty = all models
	cfgRMCTickSec        = "rmc_tick_sec"
	cfgRMCWindowSec      = "rmc_window_sec"
	cfgRMCMinHits        = "rmc_min_hits"
	cfgRMCMinCooldownSec = "rmc_min_cooldown_sec"
	cfgRMCMaxCooldownSec = "rmc_max_cooldown_sec"
	cfgRMCEscalateSec    = "rmc_escalate_window_sec"
	cfgRMCMinKeep        = "rmc_min_keep"
	cfgRMCMaxActions     = "rmc_max_actions"
	cfgRMCDryRun         = "rmc_dry_run"

	rmcTickDef, rmcTickMin, rmcTickMax                   = 20, 10, 600
	rmcWindowDef, rmcWindowMin, rmcWindowMax             = 120, 30, 3600
	rmcMinHitsDef, rmcMinHitsMin, rmcMinHitsMax          = 3, 1, 1000
	rmcMinCDDef, rmcMinCDMin, rmcMinCDMax                = 60, 10, 86400
	rmcMaxCDDef, rmcMaxCDMin, rmcMaxCDMax                = 900, 60, 86400
	rmcEscalateDef, rmcEscalateMin, rmcEscalateMax       = 1800, 0, 86400
	rmcMinKeepDef, rmcMinKeepMin, rmcMinKeepMax          = 1, 0, 100
	rmcMaxActionsDef, rmcMaxActionsMin, rmcMaxActionsMax = 50, 1, 500

	rmcEventRetention = 14 * 24 * time.Hour
)

type rmcConfig struct {
	Enabled           bool     `json:"enabled"`
	ProfileIDs        []int64  `json:"profile_ids"`
	Models            []string `json:"models"`
	TickSec           int      `json:"tick_sec"`
	WindowSec         int      `json:"window_sec"`
	MinHits           int      `json:"min_hits"`
	MinCooldownSec    int      `json:"min_cooldown_sec"`
	MaxCooldownSec    int      `json:"max_cooldown_sec"`
	EscalateWindowSec int      `json:"escalate_window_sec"`
	MinKeep           int      `json:"min_keep"`
	MaxActions        int      `json:"max_actions"`
	DryRun            bool     `json:"dry_run"`
}

var (
	rmcNudge  = make(chan struct{}, 1)
	rmcTickMu sync.Mutex
)

func nudgeRemoteModelCooldown() {
	select {
	case rmcNudge <- struct{}{}:
	default:
	}
}

func loadRMCConfig() rmcConfig {
	out := rmcConfig{
		ProfileIDs:        []int64{},
		Models:            []string{},
		TickSec:           rmcTickDef,
		WindowSec:         rmcWindowDef,
		MinHits:           rmcMinHitsDef,
		MinCooldownSec:    rmcMinCDDef,
		MaxCooldownSec:    rmcMaxCDDef,
		EscalateWindowSec: rmcEscalateDef,
		MinKeep:           rmcMinKeepDef,
		MaxActions:        rmcMaxActionsDef,
		DryRun:            true, // a fresh deployment only observes
	}
	get := func(key string) (string, bool) {
		var v string
		if err := db.QueryRow(`SELECT value FROM report_config WHERE key=$1`, key).Scan(&v); err != nil {
			return "", false
		}
		return v, true
	}
	readInt := func(key string, dst *int, min, max int) {
		if v, ok := get(key); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				*dst = n
			}
		}
		*dst = clampInt(*dst, min, max)
	}
	readBool := func(key string, dst *bool) {
		if v, ok := get(key); ok {
			s := strings.TrimSpace(v)
			*dst = s == "true" || s == "1"
		}
	}

	readBool(cfgRMCEnabled, &out.Enabled)
	readBool(cfgRMCDryRun, &out.DryRun)
	if v, ok := get(cfgRMCProfiles); ok {
		for _, s := range splitCSV(v) {
			if id, err := strconv.ParseInt(s, 10, 64); err == nil && id > 0 {
				out.ProfileIDs = append(out.ProfileIDs, id)
			}
		}
	}
	if v, ok := get(cfgRMCModels); ok {
		out.Models = splitCSV(v)
	}
	readInt(cfgRMCTickSec, &out.TickSec, rmcTickMin, rmcTickMax)
	readInt(cfgRMCWindowSec, &out.WindowSec, rmcWindowMin, rmcWindowMax)
	readInt(cfgRMCMinHits, &out.MinHits, rmcMinHitsMin, rmcMinHitsMax)
	readInt(cfgRMCMinCooldownSec, &out.MinCooldownSec, rmcMinCDMin, rmcMinCDMax)
	readInt(cfgRMCMaxCooldownSec, &out.MaxCooldownSec, rmcMaxCDMin, rmcMaxCDMax)
	readInt(cfgRMCEscalateSec, &out.EscalateWindowSec, rmcEscalateMin, rmcEscalateMax)
	readInt(cfgRMCMinKeep, &out.MinKeep, rmcMinKeepMin, rmcMinKeepMax)
	readInt(cfgRMCMaxActions, &out.MaxActions, rmcMaxActionsMin, rmcMaxActionsMax)
	return out
}

// ---- Rate-limit parsing + cooldown math (pure) ----

var (
	rateLimitHeadRe  = regexp.MustCompile(`Rate limit reached for (\S+) in organization \S+ on ([a-z ]+?) per (min|minute|hour|day)\b(?:\s*\((\w+)\))?`)
	rateLimitNumsRe  = regexp.MustCompile(`Limit (\d+), Used (\d+), Requested (\d+)`)
	rateLimitRetryRe = regexp.MustCompile(`try again in ((?:\d+(?:\.\d+)?(?:ms|h|m|s))+)`)
)

// rateLimitInfo is what an OpenAI-style rate-limit message tells us.
type rateLimitInfo struct {
	Model      string
	Unit       string // e.g. TPM, RPM, TPD
	Window     time.Duration
	Limit      int64
	Used       int64
	Requested  int64
	RetryAfter time.Duration
}

// parseRateLimit extracts the limit details from an upstream 429 message.
// ok is false when the text is not a "Rate limit reached for ..." error —
// "Request too large", quota exhaustion and account bans all fall out here.
func parseRateLimit(msg string) (info rateLimitInfo, ok bool) {
	m := rateLimitHeadRe.FindStringSubmatch(msg)
	if m == nil {
		return info, false
	}
	info.Model = m[1]
	info.Unit = m[4]
	switch m[3] {
	case "hour":
		info.Window = time.Hour
	case "day":
		info.Window = 24 * time.Hour
	default:
		info.Window = time.Minute
	}
	if what := strings.TrimSpace(m[2]); info.Unit == "" && what != "" {
		info.Unit = strings.ToUpper(what[:1]) + "P" + strings.ToUpper(m[3][:1]) // "tokens per min" → TPM
	}
	if n := rateLimitNumsRe.FindStringSubmatch(msg); n != nil {
		info.Limit, _ = strconv.ParseInt(n[1], 10, 64)
		info.Used, _ = strconv.ParseInt(n[2], 10, 64)
		info.Requested, _ = strconv.ParseInt(n[3], 10, 64)
	}
	if r := rateLimitRetryRe.FindStringSubmatch(msg); r != nil {
		if d, err := time.ParseDuration(r[1]); err == nil {
			info.RetryAfter = d
		}
	}
	return info, true
}

// rateLimitCooldown is how long to keep a model off a channel after its
// trips-th consecutive rate-limit trip (trips >= 1).
func rateLimitCooldown(info rateLimitInfo, trips int, minCD, maxCD time.Duration) time.Duration {
	window := info.Window
	if window <= 0 {
		window = time.Minute
	}
	limitCap := maxCD
	var base time.Duration
	if window >= 24*time.Hour {
		// Daily quotas reset on their own schedule; trust "try again in",
		// but never re-probe more often than hourly.
		base = max(info.RetryAfter, time.Hour)
		limitCap = max(maxCD, 24*time.Hour)
	} else {
		base = window
		if excess := info.Used + info.Requested - info.Limit; excess > 0 && info.Limit > 0 {
			base += time.Duration(float64(excess) / float64(info.Limit) * float64(window))
		}
	}
	base = max(base, info.RetryAfter, minCD)
	cd := base
	for i := 1; i < trips && cd < limitCap; i++ {
		cd *= 2
	}
	return min(cd, limitCap)
}

// nextTrips continues the escalation streak when the previous cooldown for
// the same (channel, model) ended within the escalate window.
func nextTrips(prevTrips int, prevRestoredAt, now int64, escalateWindowSec int) int {
	if prevTrips > 0 && prevRestoredAt > 0 && now-prevRestoredAt <= int64(escalateWindowSec) {
		return prevTrips + 1
	}
	return 1
}

func csvHas(csv, item string) bool {
	for _, p := range strings.Split(csv, ",") {
		if strings.TrimSpace(p) == item {
			return true
		}
	}
	return false
}

// ---- Loop ----

func startRemoteModelCooldownLoop() {
	log.Printf("[remote-cooldown] loop starting")
	go func() {
		time.Sleep(60 * time.Second) // stagger past the other schedulers
		for {
			cfg := loadRMCConfig()
			if IsLeader() {
				runRMCTick(context.Background(), cfg, cfg.Enabled)
			}
			select {
			case <-time.After(time.Duration(cfg.TickSec) * time.Second):
			case <-rmcNudge:
			}
		}
	}()
	go func() {
		time.Sleep(8 * time.Minute)
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			if !IsLeader() {
				continue
			}
			cutoff := time.Now().Add(-rmcEventRetention).Unix()
			if _, err := db.Exec(`DELETE FROM rs_remote_model_cooldown_event WHERE created_at < $1`, cutoff); err != nil {
				log.Printf("[remote-cooldown] prune events: %v", err)
			}
		}
	}()
}

type rmcSummary struct {
	Candidates int `json:"candidates"`
	Stripped   int `json:"stripped"`
	Restored   int `json:"restored"`
	Skipped    int `json:"skipped"`
	Errors     int `json:"errors"`
}

type rmcCreds struct {
	host   string
	userID int64
	token  string
	err    error
}

// rmcCredCache loads each profile's credentials at most once per tick.
type rmcCredCache map[int64]rmcCreds

func (cc rmcCredCache) get(profileID int64) rmcCreds {
	if c, ok := cc[profileID]; ok {
		return c
	}
	var c rmcCreds
	c.host, c.userID, c.token, c.err = loadRemoteProfileByID(profileID)
	cc[profileID] = c
	return c
}

// runRMCTick restores due models, then (when strip is set) strips newly
// rate-limited ones. Restores always run so a disabled loop never leaves a
// model stripped.
func runRMCTick(ctx context.Context, cfg rmcConfig, strip bool) rmcSummary {
	rmcTickMu.Lock()
	defer rmcTickMu.Unlock()
	var sum rmcSummary
	creds := rmcCredCache{}
	actionsLeft := cfg.MaxActions
	rmcRestoreDue(ctx, creds, &sum, &actionsLeft)
	if !strip {
		return sum
	}
	for _, pid := range cfg.ProfileIDs {
		if actionsLeft <= 0 || !IsLeader() {
			break
		}
		rmcStripProfile(ctx, cfg, pid, creds, &sum, &actionsLeft)
	}
	if sum.Stripped+sum.Restored+sum.Errors > 0 {
		log.Printf("[remote-cooldown] tick: stripped=%d restored=%d skipped=%d errors=%d dry_run=%v",
			sum.Stripped, sum.Restored, sum.Skipped, sum.Errors, cfg.DryRun)
	}
	return sum
}

type rmcLedgerRow struct {
	profileID   int64
	channelID   int64
	model       string
	channelName string
	trips       int
	cooldownSec int
	dryRun      bool
}

func rmcRestoreDue(ctx context.Context, creds rmcCredCache, sum *rmcSummary, actionsLeft *int) {
	rows, err := db.Query(`
		SELECT profile_id, channel_id, model, channel_name, trips, cooldown_sec, dry_run
		  FROM rs_remote_model_cooldown
		 WHERE state='cooling' AND restore_at <= $1
		 ORDER BY restore_at
		 LIMIT $2`, time.Now().Unix(), *actionsLeft)
	if err != nil {
		log.Printf("[remote-cooldown] query due restores: %v", err)
		return
	}
	var due []rmcLedgerRow
	for rows.Next() {
		var r rmcLedgerRow
		if err := rows.Scan(&r.profileID, &r.channelID, &r.model, &r.channelName, &r.trips, &r.cooldownSec, &r.dryRun); err != nil {
			log.Printf("[remote-cooldown] scan due restore: %v", err)
			continue
		}
		due = append(due, r)
	}
	rows.Close()
	for _, r := range due {
		if *actionsLeft <= 0 || !IsLeader() {
			return
		}
		*actionsLeft--
		if err := rmcRestore(ctx, creds, r, "restore"); err != nil {
			sum.Errors++
			continue
		}
		sum.Restored++
	}
}

// rmcRestore puts a cooled-down model back on its channel and closes the
// ledger row. A deleted channel closes the row as 'gone'.
func rmcRestore(ctx context.Context, creds rmcCredCache, r rmcLedgerRow, action string) error {
	now := time.Now().Unix()
	finish := func(state, detail string) {
		if _, err := db.Exec(`
			UPDATE rs_remote_model_cooldown SET state=$1, restored_at=$2
			 WHERE profile_id=$3 AND channel_id=$4 AND model=$5`,
			state, now, r.profileID, r.channelID, r.model); err != nil {
			log.Printf("[remote-cooldown] close ledger %d/%d/%s: %v", r.profileID, r.channelID, r.model, err)
		}
		logRMCEvent(r.profileID, r.channelID, r.channelName, r.model, action, r.cooldownSec, r.trips, 0, r.dryRun, detail)
	}
	if r.dryRun {
		finish("restored", "dry-run: nothing was stripped")
		return nil
	}
	c := creds.get(r.profileID)
	if c.err != nil {
		logRMCEvent(r.profileID, r.channelID, r.channelName, r.model, "error", r.cooldownSec, r.trips, 0, false, "load profile: "+c.err.Error())
		return c.err
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	data, err := remoteDoJSON(cctx, http.MethodGet, c.host, "/api/channel/"+strconv.FormatInt(r.channelID, 10), c.token, c.userID, nil, nil)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			finish("gone", "channel no longer exists on the remote")
			return nil
		}
		logRMCEvent(r.profileID, r.channelID, r.channelName, r.model, "error", r.cooldownSec, r.trips, 0, false, "restore fetch: "+err.Error())
		return err
	}
	var current map[string]any
	if err := json.Unmarshal(data, &current); err != nil {
		logRMCEvent(r.profileID, r.channelID, r.channelName, r.model, "error", r.cooldownSec, r.trips, 0, false, "restore decode: "+err.Error())
		return err
	}
	models, _ := current["models"].(string)
	if csvHas(models, r.model) {
		finish("restored", "model already present")
		return nil
	}
	if strings.TrimSpace(models) == "" {
		models = r.model
	} else {
		models = models + "," + r.model
	}
	current["models"] = models
	if err := remotePutChannel(cctx, c.host, c.token, c.userID, current); err != nil {
		logRMCEvent(r.profileID, r.channelID, r.channelName, r.model, "error", r.cooldownSec, r.trips, 0, false, "restore put: "+err.Error())
		return err
	}
	rmcPatchMirrorModels(r.profileID, r.channelID, models)
	finish("restored", fmt.Sprintf("re-added after %ds cooldown", r.cooldownSec))
	return nil
}

func rmcPatchMirrorModels(profileID, channelID int64, models string) {
	if _, err := db.Exec(`
		UPDATE remote_channel_current SET models=$1, updated_at=$2
		 WHERE profile_id=$3 AND remote_channel_id=$4`,
		models, time.Now().Unix(), profileID, channelID); err != nil {
		log.Printf("[remote-cooldown] mirror update %d/%d: %v", profileID, channelID, err)
	}
}

// rmcCandidate is one (channel, model) pair that crossed min_hits rate-limit
// errors since its last cooldown ended.
type rmcCandidate struct {
	channelID      int64
	model          string
	hits           int
	snippet        string
	prevTrips      int
	prevRestoredAt int64
	name           string
	group          string
	status         int
	models         string
}

func rmcStripProfile(ctx context.Context, cfg rmcConfig, profileID int64, creds rmcCredCache, sum *rmcSummary, actionsLeft *int) {
	now := time.Now()
	rows, err := db.QueryContext(ctx, `
		SELECT e.channel_id, e.model_name, count(*) AS hits,
		       (array_agg(e.content_snippet ORDER BY e.created_at DESC))[1],
		       COALESCE(max(l.trips), 0), COALESCE(max(l.restored_at), 0),
		       COALESCE(max(c.name), ''), COALESCE(max(c."group"), ''),
		       COALESCE(max(c.status), 0), COALESCE(max(c.models), '')
		  FROM remote_error_log e
		  LEFT JOIN rs_remote_model_cooldown l
		    ON l.profile_id = e.profile_id AND l.channel_id = e.channel_id AND l.model = e.model_name
		  LEFT JOIN remote_channel_current c
		    ON c.profile_id = e.profile_id AND c.remote_channel_id = e.channel_id
		 WHERE e.profile_id = $1 AND e.status_code = 429 AND e.channel_id > 0
		   AND e.created_at >= $2
		   AND e.content_snippet LIKE '%Rate limit reached for %'
		   AND (l.profile_id IS NULL OR (
		        l.state <> 'cooling'
		        AND NOT (l.state = 'skipped' AND l.restore_at > $3)
		        AND e.created_at > l.restored_at))
		 GROUP BY e.channel_id, e.model_name
		HAVING count(*) >= $4
		 ORDER BY hits DESC
		 LIMIT $5`,
		profileID, now.Unix()-int64(cfg.WindowSec), now.Unix(), cfg.MinHits, *actionsLeft)
	if err != nil {
		log.Printf("[remote-cooldown] profile=%d candidates: %v", profileID, err)
		sum.Errors++
		return
	}
	var cands []rmcCandidate
	for rows.Next() {
		var c rmcCandidate
		if err := rows.Scan(&c.channelID, &c.model, &c.hits, &c.snippet, &c.prevTrips, &c.prevRestoredAt,
			&c.name, &c.group, &c.status, &c.models); err != nil {
			log.Printf("[remote-cooldown] scan candidate: %v", err)
			continue
		}
		cands = append(cands, c)
	}
	rows.Close()

	for _, cand := range cands {
		if *actionsLeft <= 0 || !IsLeader() {
			return
		}
		if len(cfg.Models) > 0 && !slices.Contains(cfg.Models, cand.model) {
			continue
		}
		sum.Candidates++
		*actionsLeft--
		if err := rmcStripOne(ctx, cfg, profileID, cand, creds, sum); err != nil {
			sum.Errors++
			logRMCEvent(profileID, cand.channelID, cand.name, cand.model, "error", 0, 0, cand.hits, cfg.DryRun, "strip: "+err.Error())
			log.Printf("[remote-cooldown] profile=%d channel=%d model=%s: %v", profileID, cand.channelID, cand.model, err)
		}
	}
}

func rmcStripOne(ctx context.Context, cfg rmcConfig, profileID int64, cand rmcCandidate, creds rmcCredCache, sum *rmcSummary) error {
	now := time.Now().Unix()
	info, parsed := parseRateLimit(cand.snippet)
	trips := nextTrips(cand.prevTrips, cand.prevRestoredAt, now, cfg.EscalateWindowSec)
	cd := rateLimitCooldown(info, trips,
		time.Duration(cfg.MinCooldownSec)*time.Second, time.Duration(cfg.MaxCooldownSec)*time.Second)
	cdSec := int(cd / time.Second)
	limitNote := "limit details not parsed"
	if parsed {
		limitNote = fmt.Sprintf("%s limit %d, used %d, requested %d, retry-after %s",
			info.Unit, info.Limit, info.Used, info.Requested, info.RetryAfter)
	}

	// skip parks the pair until min_cooldown passes so a persistent
	// condition doesn't re-log every tick; it never counts as a trip.
	skip := func(action, why string) {
		sum.Skipped++
		if _, err := db.Exec(`
			INSERT INTO rs_remote_model_cooldown
			  (profile_id, channel_id, model, channel_name, grp, state, trips, cooldown_sec,
			   removed_at, restore_at, restored_at, limit_unit, limit_value, hits, detail, dry_run)
			VALUES ($1,$2,$3,$4,$5,'skipped',0,0,0,$6,0,$7,$8,$9,$10,$11)
			ON CONFLICT (profile_id, channel_id, model) DO UPDATE
			   SET state='skipped', restore_at=EXCLUDED.restore_at, hits=EXCLUDED.hits,
			       detail=EXCLUDED.detail, channel_name=EXCLUDED.channel_name, grp=EXCLUDED.grp`,
			profileID, cand.channelID, cand.model, cand.name, cand.group,
			now+int64(cfg.MinCooldownSec), info.Unit, info.Limit, cand.hits, why, cfg.DryRun); err != nil {
			log.Printf("[remote-cooldown] park skipped pair: %v", err)
		}
		logRMCEvent(profileID, cand.channelID, cand.name, cand.model, action, 0, trips, cand.hits, cfg.DryRun, why)
	}

	// Current channel state: the mirror in dry-run, the live remote otherwise.
	status, models, group, name := cand.status, cand.models, cand.group, cand.name
	var current map[string]any
	c := creds.get(profileID)
	if !cfg.DryRun {
		if c.err != nil {
			return fmt.Errorf("load profile: %w", c.err)
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		data, err := remoteDoJSON(cctx, http.MethodGet, c.host, "/api/channel/"+strconv.FormatInt(cand.channelID, 10), c.token, c.userID, nil, nil)
		if err != nil {
			return fmt.Errorf("fetch channel: %w", err)
		}
		if err := json.Unmarshal(data, &current); err != nil {
			return fmt.Errorf("decode channel: %w", err)
		}
		if v, ok := current["status"].(float64); ok {
			status = int(v)
		}
		models, _ = current["models"].(string)
		group, _ = current["group"].(string)
		if v, ok := current["name"].(string); ok && v != "" {
			name = v
		}
		cand.name, cand.group = name, group
	}
	// Disabled channels or a model already gone: the remote no longer routes
	// it here, so there is nothing to cool down.
	if status != 1 || !csvHas(models, cand.model) {
		return nil
	}
	remaining := removeModelFromCSV(models, cand.model)
	if remaining == "" {
		skip("skip_last_model", "it is the channel's only model; "+limitNote)
		return nil
	}

	var others int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM remote_channel_current c
		 WHERE c.profile_id = $1 AND c.remote_channel_id <> $2 AND c.status = 1
		   AND $3 = ANY(regexp_split_to_array(c.models, '\s*,\s*'))
		   AND regexp_split_to_array(c."group", '\s*,\s*') && $4::text[]
		   AND NOT EXISTS (
		       SELECT 1 FROM rs_remote_model_cooldown l
		        WHERE l.profile_id = c.profile_id AND l.channel_id = c.remote_channel_id
		          AND l.model = $3 AND l.state = 'cooling')`,
		profileID, cand.channelID, cand.model, pq.Array(splitCSV(group))).Scan(&others); err != nil {
		return fmt.Errorf("count serving channels: %w", err)
	}
	if others < cfg.MinKeep {
		skip("skip_floor", fmt.Sprintf("only %d other enabled channel(s) serve it (min_keep %d); %s", others, cfg.MinKeep, limitNote))
		return nil
	}

	if !cfg.DryRun {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		current["models"] = remaining
		if err := remotePutChannel(cctx, c.host, c.token, c.userID, current); err != nil {
			return fmt.Errorf("put channel: %w", err)
		}
		rmcPatchMirrorModels(profileID, cand.channelID, remaining)
	}
	detail := fmt.Sprintf("%d rate-limit 429s in %ds; %s; cooldown %ds (trip %d)",
		cand.hits, cfg.WindowSec, limitNote, cdSec, trips)
	if _, err := db.Exec(`
		INSERT INTO rs_remote_model_cooldown
		  (profile_id, channel_id, model, channel_name, grp, state, trips, cooldown_sec,
		   removed_at, restore_at, restored_at, limit_unit, limit_value, hits, detail, dry_run)
		VALUES ($1,$2,$3,$4,$5,'cooling',$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (profile_id, channel_id, model) DO UPDATE
		   SET state='cooling', trips=EXCLUDED.trips, cooldown_sec=EXCLUDED.cooldown_sec,
		       removed_at=EXCLUDED.removed_at, restore_at=EXCLUDED.restore_at,
		       limit_unit=EXCLUDED.limit_unit, limit_value=EXCLUDED.limit_value,
		       hits=EXCLUDED.hits, detail=EXCLUDED.detail, dry_run=EXCLUDED.dry_run,
		       channel_name=EXCLUDED.channel_name, grp=EXCLUDED.grp`,
		profileID, cand.channelID, cand.model, cand.name, cand.group, trips, cdSec,
		now, now+int64(cdSec), cand.prevRestoredAt, info.Unit, info.Limit, cand.hits, detail, cfg.DryRun); err != nil {
		// The remote already lost the model; without a ledger row nothing
		// would restore it, so put it straight back.
		if !cfg.DryRun {
			_ = rmcRestore(ctx, creds, rmcLedgerRow{profileID: profileID, channelID: cand.channelID, model: cand.model,
				channelName: cand.name, trips: trips, cooldownSec: 0}, "restore")
		}
		return fmt.Errorf("write ledger: %w", err)
	}
	sum.Stripped++
	logRMCEvent(profileID, cand.channelID, cand.name, cand.model, "strip", cdSec, trips, cand.hits, cfg.DryRun, detail)
	log.Printf("[remote-cooldown] strip profile=%d channel=%d (%s) model=%s: %s dry_run=%v",
		profileID, cand.channelID, cand.name, cand.model, detail, cfg.DryRun)
	return nil
}

func logRMCEvent(profileID, channelID int64, channelName, model, action string, cooldownSec, trips, hits int, dryRun bool, detail string) {
	if _, err := db.Exec(`
		INSERT INTO rs_remote_model_cooldown_event
		(profile_id, channel_id, channel_name, model, action, cooldown_sec, trips, hits, detail, dry_run, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		profileID, channelID, channelName, model, action, cooldownSec, trips, hits,
		sanitizeUpstreamMessage(detail), dryRun, time.Now().Unix(),
	); err != nil {
		log.Printf("[remote-cooldown] log event %s: %v", action, err)
	}
}

// ---- HTTP handlers (V1, admin-gated) ----

func handleRMCConfigGet(c *gin.Context) {
	c.JSON(http.StatusOK, loadRMCConfig())
}

func handleRMCConfigSet(c *gin.Context) {
	var body struct {
		Enabled           *bool     `json:"enabled,omitempty"`
		ProfileIDs        *[]int64  `json:"profile_ids,omitempty"`
		Models            *[]string `json:"models,omitempty"`
		TickSec           *int      `json:"tick_sec,omitempty"`
		WindowSec         *int      `json:"window_sec,omitempty"`
		MinHits           *int      `json:"min_hits,omitempty"`
		MinCooldownSec    *int      `json:"min_cooldown_sec,omitempty"`
		MaxCooldownSec    *int      `json:"max_cooldown_sec,omitempty"`
		EscalateWindowSec *int      `json:"escalate_window_sec,omitempty"`
		MinKeep           *int      `json:"min_keep,omitempty"`
		MaxActions        *int      `json:"max_actions,omitempty"`
		DryRun            *bool     `json:"dry_run,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	setBool := func(key string, v *bool) error {
		if v == nil {
			return nil
		}
		return writeModelCleanupConfig(key, strconv.FormatBool(*v))
	}
	setInt := func(key string, v *int, min, max int) error {
		if v == nil {
			return nil
		}
		return writeModelCleanupConfig(key, strconv.Itoa(clampInt(*v, min, max)))
	}
	fail := func(err error) bool {
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return true
		}
		return false
	}
	if body.ProfileIDs != nil {
		ids := make([]string, 0, len(*body.ProfileIDs))
		for _, id := range *body.ProfileIDs {
			if id > 0 {
				ids = append(ids, strconv.FormatInt(id, 10))
			}
		}
		if fail(writeModelCleanupConfig(cfgRMCProfiles, strings.Join(cleanCSVList(ids), ","))) {
			return
		}
	}
	if body.Models != nil {
		if fail(writeModelCleanupConfig(cfgRMCModels, strings.Join(cleanCSVList(*body.Models), ","))) {
			return
		}
	}
	if fail(setBool(cfgRMCEnabled, body.Enabled)) ||
		fail(setBool(cfgRMCDryRun, body.DryRun)) ||
		fail(setInt(cfgRMCTickSec, body.TickSec, rmcTickMin, rmcTickMax)) ||
		fail(setInt(cfgRMCWindowSec, body.WindowSec, rmcWindowMin, rmcWindowMax)) ||
		fail(setInt(cfgRMCMinHits, body.MinHits, rmcMinHitsMin, rmcMinHitsMax)) ||
		fail(setInt(cfgRMCMinCooldownSec, body.MinCooldownSec, rmcMinCDMin, rmcMinCDMax)) ||
		fail(setInt(cfgRMCMaxCooldownSec, body.MaxCooldownSec, rmcMaxCDMin, rmcMaxCDMax)) ||
		fail(setInt(cfgRMCEscalateSec, body.EscalateWindowSec, rmcEscalateMin, rmcEscalateMax)) ||
		fail(setInt(cfgRMCMinKeep, body.MinKeep, rmcMinKeepMin, rmcMinKeepMax)) ||
		fail(setInt(cfgRMCMaxActions, body.MaxActions, rmcMaxActionsMin, rmcMaxActionsMax)) {
		return
	}
	nudgeRemoteModelCooldown()
	c.JSON(http.StatusOK, loadRMCConfig())
}

// handleRMCActive lists the (channel, model) pairs currently cooling down.
func handleRMCActive(c *gin.Context) {
	rows, err := db.Query(`
		SELECT l.profile_id, COALESCE(p.name, ''), l.channel_id, l.channel_name, l.model, l.grp,
		       l.trips, l.cooldown_sec, l.removed_at, l.restore_at, l.limit_unit, l.limit_value,
		       l.hits, l.detail, l.dry_run
		  FROM rs_remote_model_cooldown l
		  LEFT JOIN remote_newapi_profile p ON p.id = l.profile_id
		 WHERE l.state = 'cooling'
		 ORDER BY l.restore_at`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, 32)
	for rows.Next() {
		var (
			profileID, channelID, removedAt, restoreAt, limitValue int64
			trips, cooldownSec, hits                               int
			profileName, name, model, group, unit, detail          string
			dryRun                                                 bool
		)
		if err := rows.Scan(&profileID, &profileName, &channelID, &name, &model, &group,
			&trips, &cooldownSec, &removedAt, &restoreAt, &unit, &limitValue, &hits, &detail, &dryRun); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		items = append(items, gin.H{
			"profile_id": profileID, "profile_name": profileName, "channel_id": channelID,
			"channel_name": name, "model": model, "group": group, "trips": trips,
			"cooldown_sec": cooldownSec, "removed_at": removedAt, "restore_at": restoreAt,
			"limit_unit": unit, "limit_value": limitValue, "hits": hits, "detail": detail, "dry_run": dryRun,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "now": time.Now().Unix()})
}

func handleRMCEvents(c *gin.Context) {
	limit := 200
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = clampInt(n, 1, 2000)
		}
	}
	filters := []string{}
	args := []any{}
	if raw := strings.TrimSpace(c.Query("profile_id")); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			args = append(args, id)
			filters = append(filters, fmt.Sprintf(`profile_id = $%d`, len(args)))
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
		SELECT id, profile_id, channel_id, channel_name, model, action, cooldown_sec, trips, hits, detail, dry_run, created_at
		  FROM rs_remote_model_cooldown_event`+where+
		` ORDER BY id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, limit)
	for rows.Next() {
		var (
			id, profileID, channelID, createdAt int64
			cooldownSec, trips, hits            int
			name, model, action, detail         string
			dryRun                              bool
		)
		if err := rows.Scan(&id, &profileID, &channelID, &name, &model, &action, &cooldownSec, &trips, &hits, &detail, &dryRun, &createdAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		items = append(items, gin.H{
			"id": id, "profile_id": profileID, "channel_id": channelID, "channel_name": name,
			"model": model, "action": action, "cooldown_sec": cooldownSec, "trips": trips,
			"hits": hits, "detail": detail, "dry_run": dryRun, "created_at": createdAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"events": items})
}

// handleRMCRunNow runs one tick synchronously, stripping even while the loop
// is disabled (dry_run still applies) so an operator can observe a pass first.
func handleRMCRunNow(c *gin.Context) {
	if !IsLeader() {
		c.JSON(http.StatusConflict, gin.H{"error": "this node is not the scheduler leader; try again"})
		return
	}
	cfg := loadRMCConfig()
	sum := runRMCTick(c.Request.Context(), cfg, true)
	c.JSON(http.StatusOK, gin.H{"summary": sum, "dry_run": cfg.DryRun})
}

// handleRMCRestore ends one cooldown early.
func handleRMCRestore(c *gin.Context) {
	var body struct {
		ProfileID int64  `json:"profile_id"`
		ChannelID int64  `json:"channel_id"`
		Model     string `json:"model"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !IsLeader() {
		c.JSON(http.StatusConflict, gin.H{"error": "this node is not the scheduler leader; try again"})
		return
	}
	rmcTickMu.Lock()
	defer rmcTickMu.Unlock()
	r := rmcLedgerRow{profileID: body.ProfileID, channelID: body.ChannelID, model: body.Model}
	err := db.QueryRow(`
		SELECT channel_name, trips, cooldown_sec, dry_run FROM rs_remote_model_cooldown
		 WHERE profile_id=$1 AND channel_id=$2 AND model=$3 AND state='cooling'`,
		body.ProfileID, body.ChannelID, body.Model).Scan(&r.channelName, &r.trips, &r.cooldownSec, &r.dryRun)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no active cooldown for that channel and model"})
		return
	}
	if err := rmcRestore(c.Request.Context(), rmcCredCache{}, r, "manual_restore"); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
