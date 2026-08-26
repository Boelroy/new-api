package main

// Log-driven model auto-cleanup.
//
// A leader-gated loop that, for each configured model, finds ENABLED channels
// (optionally restricted to configured groups) that in the last window_sec
// produced ONLY errors — zero successes and at least one error whose message
// contains a configured substring (default "Operation not allowed") — and
// strips that model from the channel's models list. The channel stays enabled;
// only the dead model is removed, so a Bedrock account that never had the model
// enabled stops being routed to for it while everything else it serves keeps
// working.
//
// This is the passive, real-traffic counterpart to the probe-based scheduler in
// local_health*.go: no billed test requests, it just reads new-api's own logs.
// State is read from the shared new-api database (channels, logs); the mutation
// goes through patchLocalChannelModels (local_newapi.go) so new-api rebuilds its
// abilities routing table and invalidates its in-memory cache for free.

import (
	"context"
	"encoding/json"
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
	cfgModelCleanupEnabled     = "model_cleanup_enabled"
	cfgModelCleanupModels      = "model_cleanup_models"    // CSV
	cfgModelCleanupGroups      = "model_cleanup_groups"    // CSV, empty = all groups
	cfgModelCleanupWindowSec   = "model_cleanup_window_sec"
	cfgModelCleanupTickSec     = "model_cleanup_tick_sec"
	cfgModelCleanupMaxActions  = "model_cleanup_max_actions"
	cfgModelCleanupDryRun       = "model_cleanup_dry_run"
	cfgModelCleanupErrorSubstr  = "model_cleanup_error_substr"  // legacy single-keyword key (read-only migration)
	cfgModelCleanupErrorSubstrs = "model_cleanup_error_substrs" // JSON array of keywords

	modelCleanupWindowDef, modelCleanupWindowMin, modelCleanupWindowMax = 180, 30, 3600
	modelCleanupTickDef, modelCleanupTickMin, modelCleanupTickMax       = 60, 15, 600
	modelCleanupActionsDef, modelCleanupActionsMin, modelCleanupActionsMax = 50, 1, 500

	modelCleanupErrorSubstrDef = "Operation not allowed"
	// Retention for the audit log, pruned hourly.
	modelCleanupEventRetention = 14 * 24 * time.Hour
)

// modelCleanupDefaultModels seeds a fresh deployment with the four Bedrock
// models this loop was built to police. Operators edit the list in the UI.
var modelCleanupDefaultModels = []string{
	"claude-opus-5", "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-4-6",
}

type modelCleanupConfig struct {
	Enabled     bool     `json:"enabled"`
	Models      []string `json:"models"`
	Groups      []string `json:"groups"`
	WindowSec    int      `json:"window_sec"`
	TickSec      int      `json:"tick_sec"`
	MaxActions   int      `json:"max_actions"`
	DryRun       bool     `json:"dry_run"`
	ErrorSubstrs []string `json:"error_substrs"` // match if an error contains ANY of these
	// TokenConfigured reports whether MAIN_SERVICE_URL + MAIN_SERVICE_TOKEN
	// are set. Without them the loop cannot mutate channels, and the UI needs
	// to explain a silently idle scheduler.
	TokenConfigured bool `json:"token_configured"`
}

// modelCleanupNudge wakes the loop early after a config change or a manual run
// request, mirroring localHealthNudge.
var modelCleanupNudge = make(chan struct{}, 1)

func nudgeModelCleanup() {
	select {
	case modelCleanupNudge <- struct{}{}:
	default:
	}
}

func loadModelCleanupConfig() modelCleanupConfig {
	out := modelCleanupConfig{
		Models:          append([]string(nil), modelCleanupDefaultModels...),
		Groups:          []string{},
		WindowSec:       modelCleanupWindowDef,
		TickSec:         modelCleanupTickDef,
		MaxActions:      modelCleanupActionsDef,
		ErrorSubstrs:    []string{modelCleanupErrorSubstrDef},
		TokenConfigured: localNewAPIConfigured(),
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

	readBool(cfgModelCleanupEnabled, &out.Enabled)
	readBool(cfgModelCleanupDryRun, &out.DryRun)
	// A stored models key (even empty) overrides the seed default, so an
	// operator can intentionally clear the list.
	if v, ok := get(cfgModelCleanupModels); ok {
		out.Models = splitCSV(v)
	}
	if v, ok := get(cfgModelCleanupGroups); ok {
		out.Groups = splitCSV(v)
	}
	// Keywords: prefer the JSON list key; fall back to the legacy single-string
	// key so deployments written before multi-keyword support keep working.
	if v, ok := get(cfgModelCleanupErrorSubstrs); ok {
		var arr []string
		if err := json.Unmarshal([]byte(v), &arr); err == nil {
			if cleaned := cleanCSVList(arr); len(cleaned) > 0 {
				out.ErrorSubstrs = cleaned
			}
		}
	} else if v, ok := get(cfgModelCleanupErrorSubstr); ok {
		if s := strings.TrimSpace(v); s != "" {
			out.ErrorSubstrs = []string{s}
		}
	}
	readInt(cfgModelCleanupWindowSec, &out.WindowSec)
	readInt(cfgModelCleanupTickSec, &out.TickSec)
	readInt(cfgModelCleanupMaxActions, &out.MaxActions)

	out.WindowSec = clampInt(out.WindowSec, modelCleanupWindowMin, modelCleanupWindowMax)
	out.TickSec = clampInt(out.TickSec, modelCleanupTickMin, modelCleanupTickMax)
	out.MaxActions = clampInt(out.MaxActions, modelCleanupActionsMin, modelCleanupActionsMax)
	return out
}

func writeModelCleanupConfig(key, value string) error {
	now := time.Now().Unix()
	_, err := db.Exec(
		`INSERT INTO report_config (key, value, updated_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (key) DO UPDATE SET value=$2, updated_at=$3`,
		key, value, now,
	)
	return err
}

// ---- Loop ----

func startModelCleanupLoop() {
	if !localNewAPIConfigured() {
		log.Printf("[model-cleanup] MAIN_SERVICE_TOKEN not set; scheduler disabled")
		return
	}
	log.Printf("[model-cleanup] loop starting")
	go func() {
		// Stagger past the other schedulers' first ticks.
		time.Sleep(75 * time.Second)
		for {
			cfg := loadModelCleanupConfig()
			if cfg.Enabled && IsLeader() {
				runModelCleanupTick(context.Background(), cfg)
			}
			select {
			case <-time.After(time.Duration(cfg.TickSec) * time.Second):
			case <-modelCleanupNudge:
			}
		}
	}()
	go modelCleanupPruneLoop()
}

func modelCleanupPruneLoop() {
	time.Sleep(6 * time.Minute)
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		if !IsLeader() {
			continue
		}
		cutoff := time.Now().Add(-modelCleanupEventRetention).Unix()
		if _, err := db.Exec(`DELETE FROM rs_model_cleanup_event WHERE created_at < $1`, cutoff); err != nil {
			log.Printf("[model-cleanup] prune events: %v", err)
		}
	}
}

// modelCleanupSummary is one tick's outcome, returned by the manual-run handler.
type modelCleanupSummary struct {
	Scanned     int `json:"scanned"`      // (channel,model) pairs matched
	Removed     int `json:"removed"`      // models actually stripped
	WouldRemove int `json:"would_remove"` // dry-run matches
	Skipped     int `json:"skipped"`      // would have emptied models list
	Errors      int `json:"errors"`       // patch failures
}

// modelCleanupTarget is one channel that currently fails a model outright.
type modelCleanupTarget struct {
	channelID int64
	name      string
	models    string
	group     string
	errCount  int
}

func runModelCleanupTick(ctx context.Context, cfg modelCleanupConfig) modelCleanupSummary {
	var sum modelCleanupSummary
	keywords := cleanCSVList(cfg.ErrorSubstrs)
	if len(cfg.Models) == 0 || len(keywords) == 0 {
		return sum
	}
	actionsLeft := cfg.MaxActions
	cutoff := time.Now().Add(-time.Duration(cfg.WindowSec) * time.Second).Unix()

	for _, model := range cfg.Models {
		if actionsLeft <= 0 {
			break
		}
		// Re-check leadership before each model: electOnce can step down on a
		// DB blip mid-tick, and two nodes must never both mutate.
		if !IsLeader() {
			break
		}
		targets, err := findModelCleanupTargets(ctx, model, cfg.Groups, cutoff, keywords, actionsLeft)
		if err != nil {
			log.Printf("[model-cleanup] query %s: %v", model, err)
			continue
		}
		for _, t := range targets {
			if actionsLeft <= 0 {
				break
			}
			if !IsLeader() {
				return sum
			}
			sum.Scanned++
			newModels := removeModelFromCSV(t.models, model)
			if strings.TrimSpace(newModels) == "" {
				// patchLocalChannelModels refuses an empty list (a disable is a
				// separate action we deliberately don't take here). Record it so
				// a channel that only ever served this one dead model is visible.
				sum.Skipped++
				logModelCleanupEvent(t, model, cfg.WindowSec, "skip_last_model", false,
					fmt.Sprintf("%s is the channel's only model; left as-is (0 success, %d [%s] in %ds)",
						model, t.errCount, strings.Join(keywords, " | "), cfg.WindowSec))
				continue
			}
			detail := fmt.Sprintf("0 success, %d errors matching [%s] in last %ds; group=%s",
				t.errCount, strings.Join(keywords, " | "), cfg.WindowSec, t.group)
			if cfg.DryRun {
				sum.WouldRemove++
				actionsLeft--
				logModelCleanupEvent(t, model, cfg.WindowSec, "would_remove", true, detail)
				continue
			}
			if err := patchLocalChannelModels(ctx, t.channelID, newModels); err != nil {
				sum.Errors++
				actionsLeft--
				logModelCleanupEvent(t, model, cfg.WindowSec, "error", false,
					"patch failed: "+err.Error())
				log.Printf("[model-cleanup] patch channel %d (remove %s): %v", t.channelID, model, err)
				continue
			}
			sum.Removed++
			actionsLeft--
			logModelCleanupEvent(t, model, cfg.WindowSec, "removed", false, detail)
			log.Printf("[model-cleanup] removed %s from channel %d (%s): %s", model, t.channelID, t.name, detail)
		}
	}
	return sum
}

// findModelCleanupTargets returns enabled channels that currently list `model`,
// optionally within `groups`, whose real traffic for that model in the window
// is all failure: zero type=2 successes and at least one type=5 error whose
// content contains ANY of `keywords` (case-insensitive). Bounded by `limit` so
// a tick never fetches more than its remaining action budget.
func findModelCleanupTargets(ctx context.Context, model string, groups []string,
	cutoff int64, keywords []string, limit int) ([]modelCleanupTarget, error) {
	if len(keywords) == 0 {
		return nil, nil
	}
	args := []any{model, cutoff}
	// Build "(l.content ILIKE $3 OR l.content ILIKE $4 ...)" once; both the
	// count subquery and the EXISTS reference the same placeholders.
	likes := make([]string, 0, len(keywords))
	for _, kw := range keywords {
		args = append(args, "%"+kw+"%")
		likes = append(likes, fmt.Sprintf("l.content ILIKE $%d", len(args)))
	}
	likeClause := "(" + strings.Join(likes, " OR ") + ")"

	groupClause := ""
	if len(groups) > 0 {
		args = append(args, pq.Array(groups))
		groupClause = fmt.Sprintf(` AND string_to_array(c."group", ',') && $%d`, len(args))
	}
	args = append(args, limit)
	limitPos := len(args)

	q := `
		SELECT c.id, COALESCE(c.name,''), COALESCE(c.models,''), COALESCE(c."group",''),
		       (SELECT count(*) FROM logs l
		          WHERE l.channel_id=c.id AND l.model_name=$1 AND l.created_at>=$2
		            AND l.type=5 AND ` + likeClause + `) AS err_count
		  FROM channels c
		 WHERE c.status=1
		   AND $1 = ANY(string_to_array(c.models, ','))` + groupClause + `
		   AND NOT EXISTS (SELECT 1 FROM logs l
		          WHERE l.channel_id=c.id AND l.model_name=$1 AND l.created_at>=$2 AND l.type=2)
		   AND EXISTS (SELECT 1 FROM logs l
		          WHERE l.channel_id=c.id AND l.model_name=$1 AND l.created_at>=$2
		            AND l.type=5 AND ` + likeClause + `)
		 ORDER BY err_count DESC
		 LIMIT $` + strconv.Itoa(limitPos)

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]modelCleanupTarget, 0, limit)
	for rows.Next() {
		var t modelCleanupTarget
		if err := rows.Scan(&t.channelID, &t.name, &t.models, &t.group, &t.errCount); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// removeModelFromCSV drops one exact model token from a comma-separated list,
// preserving order and never matching a substring (claude-opus-4-6 must not
// touch claude-opus-4-5-20251101).
func removeModelFromCSV(models, drop string) string {
	parts := strings.Split(models, ",")
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) == drop {
			continue
		}
		if strings.TrimSpace(p) == "" {
			continue
		}
		kept = append(kept, strings.TrimSpace(p))
	}
	return strings.Join(kept, ",")
}

func logModelCleanupEvent(t modelCleanupTarget, model string, windowSec int, kind string, dryRun bool, detail string) {
	if _, err := db.Exec(`
		INSERT INTO rs_model_cleanup_event
		(channel_id, channel_name, model, grp, err_count, window_sec, kind, detail, dry_run, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		t.channelID, t.name, model, t.group, t.errCount, windowSec, kind,
		sanitizeUpstreamMessage(detail), dryRun, time.Now().Unix(),
	); err != nil {
		log.Printf("[model-cleanup] log event %s: %v", kind, err)
	}
}

// ---- HTTP handlers (V1, admin-gated) ----

func handleModelCleanupConfigGet(c *gin.Context) {
	c.JSON(http.StatusOK, loadModelCleanupConfig())
}

func handleModelCleanupConfigSet(c *gin.Context) {
	var body struct {
		Enabled     *bool     `json:"enabled,omitempty"`
		Models      *[]string `json:"models,omitempty"`
		Groups      *[]string `json:"groups,omitempty"`
		WindowSec   *int      `json:"window_sec,omitempty"`
		TickSec     *int      `json:"tick_sec,omitempty"`
		MaxActions  *int      `json:"max_actions,omitempty"`
		DryRun       *bool     `json:"dry_run,omitempty"`
		ErrorSubstrs *[]string `json:"error_substrs,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.Enabled != nil && *body.Enabled && !localNewAPIConfigured() {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "MAIN_SERVICE_URL / MAIN_SERVICE_TOKEN not configured; the scheduler cannot reach the local new-api",
		})
		return
	}
	setBool := func(key string, v *bool) error {
		if v == nil {
			return nil
		}
		s := "false"
		if *v {
			s = "true"
		}
		return writeModelCleanupConfig(key, s)
	}
	setCSV := func(key string, v *[]string) error {
		if v == nil {
			return nil
		}
		return writeModelCleanupConfig(key, strings.Join(cleanCSVList(*v), ","))
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

	if fail(setBool(cfgModelCleanupEnabled, body.Enabled)) ||
		fail(setBool(cfgModelCleanupDryRun, body.DryRun)) ||
		fail(setCSV(cfgModelCleanupModels, body.Models)) ||
		fail(setCSV(cfgModelCleanupGroups, body.Groups)) ||
		fail(setInt(cfgModelCleanupWindowSec, body.WindowSec, modelCleanupWindowMin, modelCleanupWindowMax)) ||
		fail(setInt(cfgModelCleanupTickSec, body.TickSec, modelCleanupTickMin, modelCleanupTickMax)) ||
		fail(setInt(cfgModelCleanupMaxActions, body.MaxActions, modelCleanupActionsMin, modelCleanupActionsMax)) {
		return
	}
	if body.ErrorSubstrs != nil {
		if cleaned := cleanCSVList(*body.ErrorSubstrs); len(cleaned) > 0 {
			blob, err := json.Marshal(cleaned)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if fail(writeModelCleanupConfig(cfgModelCleanupErrorSubstrs, string(blob))) {
				return
			}
		}
	}
	nudgeModelCleanup()
	c.JSON(http.StatusOK, loadModelCleanupConfig())
}

// cleanCSVList trims, drops blanks, and dedups while preserving order.
func cleanCSVList(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		v := strings.TrimSpace(s)
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func handleModelCleanupEvents(c *gin.Context) {
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
	if kind := strings.TrimSpace(c.Query("kind")); kind != "" {
		args = append(args, kind)
		filters = append(filters, fmt.Sprintf(`kind = $%d`, len(args)))
	}
	where := ""
	if len(filters) > 0 {
		where = " WHERE " + strings.Join(filters, " AND ")
	}
	args = append(args, limit)
	rows, err := db.Query(`
		SELECT id, channel_id, channel_name, model, grp, err_count, window_sec, kind, detail, dry_run, created_at
		  FROM rs_model_cleanup_event`+where+
		` ORDER BY id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, limit)
	for rows.Next() {
		var (
			id, channelID, createdAt      int64
			errCount, windowSec           int
			name, model, group, kind, det string
			dryRun                        bool
		)
		if err := rows.Scan(&id, &channelID, &name, &model, &group, &errCount, &windowSec, &kind, &det, &dryRun, &createdAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		items = append(items, gin.H{
			"id": id, "channel_id": channelID, "channel_name": name, "model": model,
			"group": group, "err_count": errCount, "window_sec": windowSec,
			"kind": kind, "detail": det, "dry_run": dryRun, "created_at": createdAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"events": items})
}

// handleModelCleanupStats reports, for every model currently listed on an
// enabled channel, how many enabled channels carry it. A channel listing N
// models contributes to N counts — this is the per-model live channel fan-out,
// which is what an operator wants to see before/after a cleanup pass.
func handleModelCleanupStats(c *gin.Context) {
	rows, err := db.Query(`
		SELECT m, count(*) AS n FROM (
			SELECT unnest(string_to_array(models, ',')) AS m
			  FROM channels WHERE status=1
		) t
		WHERE trim(m) <> ''
		GROUP BY m
		ORDER BY n DESC, m ASC`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, 64)
	for rows.Next() {
		var model string
		var n int
		if err := rows.Scan(&model, &n); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		items = append(items, gin.H{"model": strings.TrimSpace(model), "enabled_channels": n})
	}
	c.JSON(http.StatusOK, gin.H{"stats": items})
}

// handleModelCleanupRunNow runs one tick synchronously against the current
// config, ignoring the Enabled gate so an operator can trigger and observe a
// pass (dry-run or not) before turning the loop on. Leader-gated: a follower
// must not mutate.
func handleModelCleanupRunNow(c *gin.Context) {
	if !localNewAPIConfigured() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "MAIN_SERVICE_URL / MAIN_SERVICE_TOKEN not configured"})
		return
	}
	if !IsLeader() {
		c.JSON(http.StatusConflict, gin.H{"error": "this node is not the scheduler leader; try again"})
		return
	}
	cfg := loadModelCleanupConfig()
	sum := runModelCleanupTick(c.Request.Context(), cfg)
	c.JSON(http.StatusOK, gin.H{"summary": sum, "dry_run": cfg.DryRun})
}
