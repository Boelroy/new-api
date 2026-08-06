package main

// HTTP surface and configuration for the local model health scheduler.
//
// Config lives in report_config as key/value pairs, same as the local pool
// (local_pool.go). Every value is clamped on read AND on write, and the loop
// re-reads on every tick so changes take effect without a restart.

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	cfgLocalHealthEnabled           = "local_health_enabled"
	cfgLocalHealthTickSec           = "local_health_tick_sec"
	cfgLocalHealthProbeBatch        = "local_health_probe_batch"
	cfgLocalHealthBootstrapBatch    = "local_health_bootstrap_batch"
	cfgLocalHealthProbeTimeoutSec   = "local_health_probe_timeout_sec"
	cfgLocalHealthConcurrency       = "local_health_concurrency"
	cfgLocalHealthMaxActionsPerTick = "local_health_max_actions_per_tick"

	localHealthTickDef, localHealthTickMin, localHealthTickMax                      = 30, 10, 600
	localHealthProbeBatchDef, localHealthProbeBatchMin, localHealthProbeBatchMax    = 40, 1, 500
	localHealthBootstrapDef, localHealthBootstrapMin, localHealthBootstrapMax       = 200, 1, 2000
	localHealthTimeoutDef, localHealthTimeoutMin, localHealthTimeoutMax             = 90, 15, 300
	localHealthConcurrencyDef, localHealthConcurrencyMin, localHealthConcurrencyMax = 4, 1, 32
	localHealthMaxActionsDef, localHealthMaxActionsMin, localHealthMaxActionsMax    = 10, 1, 200

	// Rule-level clamps, applied wherever a rule is written or read.
	localHealthIntervalMin, localHealthIntervalMax     = 60, 86400
	localHealthDownWindowMin, localHealthDownWindowMax = 60, 86400
	localHealthFailMin, localHealthFailMax             = 1, 20

	// Jitter spread on next_check_at, so a fleet restarted together doesn't
	// re-synchronize into one thundering probe wave.
	localHealthJitterPct = 20
	// Floor on the re-probe interval for anything not steady-state up.
	localHealthFastIntervalMin = 120 * time.Second
	// A row new-api can't test at all is parked this long before we bother
	// asking again (channel type could change, in theory).
	localHealthUnsupportedInterval = 24 * time.Hour
	// Retention for the event log, pruned hourly.
	localHealthEventRetention = 14 * 24 * time.Hour
	// The token_name new-api stamps on channel-test consume logs. Report
	// aggregations exclude it so probes don't inflate revenue or RPM.
	channelTestTokenName = "模型测试"
)

// excludeChannelTestLogs filters out the type=2 consume rows new-api writes
// for every channel test (controller/channel-test.go RecordConsumeLog). These
// are not customer traffic: counting them inflates the revenue report, the
// per-key hourly spend, the realtime RPM cards, and — worst — the RPM that
// sizes the local pool's auto batch. new-api's own periodic channel test has
// always produced a trickle of these; the health scheduler makes the volume
// matter. Append to any `logs WHERE type=2` predicate.
//
// excludeChannelTestLogsQualified is the same filter for queries that alias
// the logs table.
const (
	excludeChannelTestLogs          = ` AND COALESCE(token_name,'') <> '` + channelTestTokenName + `'`
	excludeChannelTestLogsQualified = ` AND COALESCE(l.token_name,'') <> '` + channelTestTokenName + `'`
)

type localHealthConfig struct {
	Enabled           bool `json:"enabled"`
	TickSec           int  `json:"tick_sec"`
	ProbeBatch        int  `json:"probe_batch"`
	BootstrapBatch    int  `json:"bootstrap_batch"`
	ProbeTimeoutSec   int  `json:"probe_timeout_sec"`
	Concurrency       int  `json:"concurrency"`
	MaxActionsPerTick int  `json:"max_actions_per_tick"`
	// TokenConfigured reports whether MAIN_SERVICE_URL + MAIN_SERVICE_TOKEN
	// are set. Without them the loop never runs, and the UI needs to say why
	// rather than showing a silently idle scheduler.
	TokenConfigured bool `json:"token_configured"`
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func loadLocalHealthConfig() localHealthConfig {
	out := localHealthConfig{
		TickSec:           localHealthTickDef,
		ProbeBatch:        localHealthProbeBatchDef,
		BootstrapBatch:    localHealthBootstrapDef,
		ProbeTimeoutSec:   localHealthTimeoutDef,
		Concurrency:       localHealthConcurrencyDef,
		MaxActionsPerTick: localHealthMaxActionsDef,
		TokenConfigured:   localNewAPIConfigured(),
	}
	readInt := func(key string, dst *int) {
		var v string
		if err := db.QueryRow(`SELECT value FROM report_config WHERE key=$1`, key).Scan(&v); err != nil {
			return
		}
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			*dst = n
		}
	}
	var enabled string
	if err := db.QueryRow(`SELECT value FROM report_config WHERE key=$1`, cfgLocalHealthEnabled).Scan(&enabled); err == nil {
		s := strings.TrimSpace(enabled)
		out.Enabled = s == "true" || s == "1"
	}
	readInt(cfgLocalHealthTickSec, &out.TickSec)
	readInt(cfgLocalHealthProbeBatch, &out.ProbeBatch)
	readInt(cfgLocalHealthBootstrapBatch, &out.BootstrapBatch)
	readInt(cfgLocalHealthProbeTimeoutSec, &out.ProbeTimeoutSec)
	readInt(cfgLocalHealthConcurrency, &out.Concurrency)
	readInt(cfgLocalHealthMaxActionsPerTick, &out.MaxActionsPerTick)

	out.TickSec = clampInt(out.TickSec, localHealthTickMin, localHealthTickMax)
	out.ProbeBatch = clampInt(out.ProbeBatch, localHealthProbeBatchMin, localHealthProbeBatchMax)
	out.BootstrapBatch = clampInt(out.BootstrapBatch, localHealthBootstrapMin, localHealthBootstrapMax)
	out.ProbeTimeoutSec = clampInt(out.ProbeTimeoutSec, localHealthTimeoutMin, localHealthTimeoutMax)
	out.Concurrency = clampInt(out.Concurrency, localHealthConcurrencyMin, localHealthConcurrencyMax)
	out.MaxActionsPerTick = clampInt(out.MaxActionsPerTick, localHealthMaxActionsMin, localHealthMaxActionsMax)
	return out
}

func writeLocalHealthConfig(key, value string) error {
	now := time.Now().Unix()
	_, err := db.Exec(
		`INSERT INTO report_config (key, value, updated_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (key) DO UPDATE SET value=$2, updated_at=$3`,
		key, value, now,
	)
	return err
}

func handleLocalHealthConfigGet(c *gin.Context) {
	c.JSON(http.StatusOK, loadLocalHealthConfig())
}

func handleLocalHealthConfigSet(c *gin.Context) {
	var body struct {
		Enabled           *bool `json:"enabled,omitempty"`
		TickSec           *int  `json:"tick_sec,omitempty"`
		ProbeBatch        *int  `json:"probe_batch,omitempty"`
		BootstrapBatch    *int  `json:"bootstrap_batch,omitempty"`
		ProbeTimeoutSec   *int  `json:"probe_timeout_sec,omitempty"`
		Concurrency       *int  `json:"concurrency,omitempty"`
		MaxActionsPerTick *int  `json:"max_actions_per_tick,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.Enabled != nil {
		if *body.Enabled && !localNewAPIConfigured() {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "MAIN_SERVICE_URL / MAIN_SERVICE_TOKEN not configured; the scheduler cannot reach the local new-api",
			})
			return
		}
		v := "false"
		if *body.Enabled {
			v = "true"
		}
		if err := writeLocalHealthConfig(cfgLocalHealthEnabled, v); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	writes := []struct {
		val      *int
		key      string
		min, max int
	}{
		{body.TickSec, cfgLocalHealthTickSec, localHealthTickMin, localHealthTickMax},
		{body.ProbeBatch, cfgLocalHealthProbeBatch, localHealthProbeBatchMin, localHealthProbeBatchMax},
		{body.BootstrapBatch, cfgLocalHealthBootstrapBatch, localHealthBootstrapMin, localHealthBootstrapMax},
		{body.ProbeTimeoutSec, cfgLocalHealthProbeTimeoutSec, localHealthTimeoutMin, localHealthTimeoutMax},
		{body.Concurrency, cfgLocalHealthConcurrency, localHealthConcurrencyMin, localHealthConcurrencyMax},
		{body.MaxActionsPerTick, cfgLocalHealthMaxActionsPerTick, localHealthMaxActionsMin, localHealthMaxActionsMax},
	}
	for _, w := range writes {
		if w.val == nil {
			continue
		}
		if err := writeLocalHealthConfig(w.key, strconv.Itoa(clampInt(*w.val, w.min, w.max))); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	nudgeLocalHealth()
	c.JSON(http.StatusOK, loadLocalHealthConfig())
}

// ---- Rules ----

type localHealthRule struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	MatchTag         string `json:"match_tag"`
	MatchType        int    `json:"match_type"`
	MatchGroup       string `json:"match_group"`
	MatchChannelIDs  string `json:"match_channel_ids"`
	CandidateModels  string `json:"candidate_models"`
	Enabled          bool   `json:"enabled"`
	Enforce          bool   `json:"enforce"`
	ProbeIntervalSec int    `json:"probe_interval_sec"`
	DownWindowSec    int    `json:"down_window_sec"`
	DownFailMin      int    `json:"down_fail_min"`
	RecoverOKMin     int    `json:"recover_ok_min"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
}

func (r *localHealthRule) clamp() {
	r.ProbeIntervalSec = clampInt(r.ProbeIntervalSec, localHealthIntervalMin, localHealthIntervalMax)
	r.DownWindowSec = clampInt(r.DownWindowSec, localHealthDownWindowMin, localHealthDownWindowMax)
	r.DownFailMin = clampInt(r.DownFailMin, localHealthFailMin, localHealthFailMax)
	r.RecoverOKMin = clampInt(r.RecoverOKMin, localHealthFailMin, localHealthFailMax)
	if r.MatchType < -1 {
		r.MatchType = -1
	}
	r.MatchTag = strings.TrimSpace(r.MatchTag)
	r.MatchGroup = strings.TrimSpace(r.MatchGroup)
	r.MatchChannelIDs = csvJoinModels(csvSplitModels(r.MatchChannelIDs))
	r.CandidateModels = csvJoinModels(csvSplitModels(r.CandidateModels))
}

const localHealthRuleColumns = `id, name, match_tag, match_type, match_group, match_channel_ids,
	candidate_models, enabled, enforce, probe_interval_sec, down_window_sec,
	down_fail_min, recover_ok_min, created_at, updated_at`

func scanLocalHealthRule(scan func(dest ...any) error) (localHealthRule, error) {
	var r localHealthRule
	err := scan(&r.ID, &r.Name, &r.MatchTag, &r.MatchType, &r.MatchGroup, &r.MatchChannelIDs,
		&r.CandidateModels, &r.Enabled, &r.Enforce, &r.ProbeIntervalSec, &r.DownWindowSec,
		&r.DownFailMin, &r.RecoverOKMin, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func loadLocalHealthRules(enabledOnly bool) ([]localHealthRule, error) {
	q := `SELECT ` + localHealthRuleColumns + ` FROM local_model_health_rule`
	if enabledOnly {
		q += ` WHERE enabled = TRUE`
	}
	q += ` ORDER BY id`
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]localHealthRule, 0, 8)
	for rows.Next() {
		r, err := scanLocalHealthRule(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func handleLocalHealthRuleList(c *gin.Context) {
	rules, err := loadLocalHealthRules(false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"rules": rules})
}

func handleLocalHealthRuleCreate(c *gin.Context) {
	var r localHealthRule
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if r.ProbeIntervalSec == 0 {
		r.ProbeIntervalSec = 3600
	}
	if r.DownWindowSec == 0 {
		r.DownWindowSec = 1800
	}
	if r.DownFailMin == 0 {
		r.DownFailMin = 3
	}
	if r.RecoverOKMin == 0 {
		r.RecoverOKMin = 2
	}
	r.clamp()
	now := time.Now().Unix()
	if err := db.QueryRow(`
		INSERT INTO local_model_health_rule
		(name, match_tag, match_type, match_group, match_channel_ids, candidate_models,
		 enabled, enforce, probe_interval_sec, down_window_sec, down_fail_min,
		 recover_ok_min, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)
		RETURNING id`,
		r.Name, r.MatchTag, r.MatchType, r.MatchGroup, r.MatchChannelIDs, r.CandidateModels,
		r.Enabled, r.Enforce, r.ProbeIntervalSec, r.DownWindowSec, r.DownFailMin,
		r.RecoverOKMin, now,
	).Scan(&r.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	r.CreatedAt, r.UpdatedAt = now, now
	nudgeLocalHealth()
	c.JSON(http.StatusOK, r)
}

func handleLocalHealthRuleUpdate(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	existing, err := scanLocalHealthRule(db.QueryRow(
		`SELECT `+localHealthRuleColumns+` FROM local_model_health_rule WHERE id=$1`, id).Scan)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "rule not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var body struct {
		Name             *string `json:"name,omitempty"`
		MatchTag         *string `json:"match_tag,omitempty"`
		MatchType        *int    `json:"match_type,omitempty"`
		MatchGroup       *string `json:"match_group,omitempty"`
		MatchChannelIDs  *string `json:"match_channel_ids,omitempty"`
		CandidateModels  *string `json:"candidate_models,omitempty"`
		Enabled          *bool   `json:"enabled,omitempty"`
		Enforce          *bool   `json:"enforce,omitempty"`
		ProbeIntervalSec *int    `json:"probe_interval_sec,omitempty"`
		DownWindowSec    *int    `json:"down_window_sec,omitempty"`
		DownFailMin      *int    `json:"down_fail_min,omitempty"`
		RecoverOKMin     *int    `json:"recover_ok_min,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.Name != nil {
		existing.Name = *body.Name
	}
	if body.MatchTag != nil {
		existing.MatchTag = *body.MatchTag
	}
	if body.MatchType != nil {
		existing.MatchType = *body.MatchType
	}
	if body.MatchGroup != nil {
		existing.MatchGroup = *body.MatchGroup
	}
	if body.MatchChannelIDs != nil {
		existing.MatchChannelIDs = *body.MatchChannelIDs
	}
	if body.CandidateModels != nil {
		existing.CandidateModels = *body.CandidateModels
	}
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}
	if body.Enforce != nil {
		existing.Enforce = *body.Enforce
	}
	if body.ProbeIntervalSec != nil {
		existing.ProbeIntervalSec = *body.ProbeIntervalSec
	}
	if body.DownWindowSec != nil {
		existing.DownWindowSec = *body.DownWindowSec
	}
	if body.DownFailMin != nil {
		existing.DownFailMin = *body.DownFailMin
	}
	if body.RecoverOKMin != nil {
		existing.RecoverOKMin = *body.RecoverOKMin
	}
	existing.clamp()
	existing.UpdatedAt = time.Now().Unix()

	if _, err := db.Exec(`
		UPDATE local_model_health_rule
		   SET name=$1, match_tag=$2, match_type=$3, match_group=$4, match_channel_ids=$5,
		       candidate_models=$6, enabled=$7, enforce=$8, probe_interval_sec=$9,
		       down_window_sec=$10, down_fail_min=$11, recover_ok_min=$12, updated_at=$13
		 WHERE id=$14`,
		existing.Name, existing.MatchTag, existing.MatchType, existing.MatchGroup,
		existing.MatchChannelIDs, existing.CandidateModels, existing.Enabled, existing.Enforce,
		existing.ProbeIntervalSec, existing.DownWindowSec, existing.DownFailMin,
		existing.RecoverOKMin, existing.UpdatedAt, id,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	nudgeLocalHealth()
	c.JSON(http.StatusOK, existing)
}

func handleLocalHealthRuleDelete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, err := db.Exec(`DELETE FROM local_model_health_rule WHERE id=$1`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Coverage rows for the deleted rule are cleaned up by the next expand
	// pass, which recomputes the covered set from scratch.
	nudgeLocalHealth()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleLocalHealthRulePreview answers the question an operator must be able
// to ask before flipping `enabled`: how many channels does this touch, how
// many billed probes per day does that imply, and — if enforce were on right
// now — what would change.
func handleLocalHealthRulePreview(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	rule, err := scanLocalHealthRule(db.QueryRow(
		`SELECT `+localHealthRuleColumns+` FROM local_model_health_rule WHERE id=$1`, id).Scan)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "rule not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	channels, err := matchLocalHealthChannels(rule)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	pairs := 0
	samples := make([]gin.H, 0, 20)
	for _, ch := range channels {
		models := ruleCandidateModels(rule, ch)
		pairs += len(models)
		if len(samples) < 20 {
			samples = append(samples, gin.H{
				"channel_id": ch.ID, "name": ch.Name, "type": ch.Type,
				"status": ch.Status, "group": ch.Group, "tag": ch.Tag,
				"current_models": ch.Models, "candidate_models": csvJoinModels(models),
			})
		}
	}
	probesPerDay := 0
	if rule.ProbeIntervalSec > 0 {
		probesPerDay = pairs * 86400 / rule.ProbeIntervalSec
	}
	c.JSON(http.StatusOK, gin.H{
		"rule":             rule,
		"channel_count":    len(channels),
		"pair_count":       pairs,
		"probes_per_day":   probesPerDay,
		"sample_channels":  samples,
		"token_configured": localNewAPIConfigured(),
	})
}

// handleLocalHealthStatus is the page's main read: the health matrix plus the
// signals that tell an operator whether the loop is actually keeping up.
func handleLocalHealthStatus(c *gin.Context) {
	cfg := loadLocalHealthConfig()

	filters := []string{}
	args := []any{}
	if state := strings.TrimSpace(c.Query("state")); state != "" {
		args = append(args, state)
		filters = append(filters, fmt.Sprintf(`h.state = $%d`, len(args)))
	}
	if tag := strings.TrimSpace(c.Query("tag")); tag != "" {
		args = append(args, tag)
		filters = append(filters, fmt.Sprintf(`c.tag = $%d`, len(args)))
	}
	if raw := strings.TrimSpace(c.Query("channel_id")); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			args = append(args, id)
			filters = append(filters, fmt.Sprintf(`h.channel_id = $%d`, len(args)))
		}
	}
	where := ""
	if len(filters) > 0 {
		where = " WHERE " + strings.Join(filters, " AND ")
	}
	limit := 500
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = clampInt(n, 1, 5000)
		}
	}
	args = append(args, limit)

	rows, err := db.Query(`
		SELECT h.channel_id, h.model, h.rule_id, h.state, h.consecutive_ok, h.consecutive_fail,
		       h.last_ok_at, h.last_checked_at, h.next_check_at, h.last_class, h.last_error,
		       h.last_latency_ms,
		       COALESCE(c.name,''), COALESCE(c.tag,''), COALESCE(c."group",''),
		       COALESCE(c.status,0), COALESCE(c.type,0), COALESCE(c.models,''),
		       COALESCE(l.disabled_by_us, FALSE)
		  FROM local_model_health h
		  LEFT JOIN channels c ON c.id = h.channel_id
		  LEFT JOIN local_model_health_channel l ON l.channel_id = h.channel_id`+
		where+` ORDER BY h.channel_id, h.model LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	items := make([]gin.H, 0, 128)
	for rows.Next() {
		var (
			channelID, ruleID                         int64
			model, state, lastClass, lastError        string
			okStreak, failStreak                      int
			lastOK, lastChecked, nextCheck, latencyMS int64
			name, tag, group, models                  string
			chStatus, chType                          int
			disabledByUs                              bool
		)
		if err := rows.Scan(&channelID, &model, &ruleID, &state, &okStreak, &failStreak,
			&lastOK, &lastChecked, &nextCheck, &lastClass, &lastError, &latencyMS,
			&name, &tag, &group, &chStatus, &chType, &models, &disabledByUs); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		items = append(items, gin.H{
			"channel_id": channelID, "model": model, "rule_id": ruleID, "state": state,
			"consecutive_ok": okStreak, "consecutive_fail": failStreak,
			"last_ok_at": lastOK, "last_checked_at": lastChecked, "next_check_at": nextCheck,
			"last_class": lastClass, "last_error": lastError, "last_latency_ms": latencyMS,
			"channel_name": name, "tag": tag, "group": group,
			"channel_status": chStatus, "channel_type": chType,
			"in_models":      containsModel(models, model),
			"disabled_by_us": disabledByUs,
		})
	}

	counts := map[string]int{}
	if stateRows, err := db.Query(`SELECT state, COUNT(*) FROM local_model_health GROUP BY state`); err == nil {
		defer stateRows.Close()
		for stateRows.Next() {
			var s string
			var n int
			if err := stateRows.Scan(&s, &n); err == nil {
				counts[s] = n
			}
		}
	}

	// Queue lag: how far behind the due-queue is. A backlog that exceeds
	// throughput silently stretches the 30-minute window into hours, so this
	// number belongs on screen.
	var lagSec int64
	_ = db.QueryRow(`
		SELECT COALESCE(MAX($1::bigint - next_check_at), 0)
		  FROM local_model_health
		 WHERE next_check_at <= $1 AND state <> $2`,
		time.Now().Unix(), healthStateUnsupported).Scan(&lagSec)

	var disabledCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM local_model_health_channel WHERE disabled_by_us = TRUE`).Scan(&disabledCount)

	c.JSON(http.StatusOK, gin.H{
		"config":            cfg,
		"items":             items,
		"state_counts":      counts,
		"queue_lag_sec":     lagSec,
		"channels_disabled": disabledCount,
		"last_tick":         lastLocalHealthTick(),
	})
}

func containsModel(csv, model string) bool {
	for _, m := range csvSplitModels(csv) {
		if m == model {
			return true
		}
	}
	return false
}

func handleLocalHealthEvents(c *gin.Context) {
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
		SELECT id, channel_id, model, rule_id, kind, detail, dry_run, created_at
		  FROM local_model_health_event`+where+
		` ORDER BY id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, limit)
	for rows.Next() {
		var (
			id, channelID, ruleID, createdAt int64
			model, kind, detail              string
			dryRun                           bool
		)
		if err := rows.Scan(&id, &channelID, &model, &ruleID, &kind, &detail, &dryRun, &createdAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		items = append(items, gin.H{
			"id": id, "channel_id": channelID, "model": model, "rule_id": ruleID,
			"kind": kind, "detail": detail, "dry_run": dryRun, "created_at": createdAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"events": items})
}

// handleLocalHealthProbeNow runs one probe synchronously. Used both as a
// manual "check this now" button and, on a rule the operator is about to
// enable, as a way to confirm MAIN_SERVICE_TOKEN actually carries
// ChannelOperate — otherwise every probe would classify as neutral and the
// scheduler would look mysteriously idle.
func handleLocalHealthProbeNow(c *gin.Context) {
	var body struct {
		ChannelID int64  `json:"channel_id"`
		Model     string `json:"model"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.ChannelID <= 0 || strings.TrimSpace(body.Model) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id and model are required"})
		return
	}
	if !localNewAPIConfigured() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "MAIN_SERVICE_URL / MAIN_SERVICE_TOKEN not configured"})
		return
	}
	cfg := loadLocalHealthConfig()
	res := testLocalChannelModel(c.Request.Context(), body.ChannelID, strings.TrimSpace(body.Model),
		time.Duration(cfg.ProbeTimeoutSec)*time.Second)

	// Fold the result into the stored state when the pair is managed, so a
	// manual probe counts toward recovery rather than being purely advisory.
	if _, err := db.Exec(
		`UPDATE local_model_health SET next_check_at = 0 WHERE channel_id=$1 AND model=$2`,
		body.ChannelID, strings.TrimSpace(body.Model),
	); err != nil {
		log.Printf("[local-health] probe-now reschedule: %v", err)
	}
	nudgeLocalHealth()

	c.JSON(http.StatusOK, gin.H{
		"class":      res.Class,
		"message":    res.Message,
		"error_code": res.ErrorCode,
		"seconds":    res.Seconds,
		"http_code":  res.HTTPCode,
	})
}
