package main

// Local pool: same drip model as Remote Channels' pool, but the "upload
// target" is the local channels table (same insert path as
// handleBatchCreateChannels). Kept in a separate file so the local vs.
// remote surfaces don't interleave — they share nothing but the design.
//
// Storage:
//   local_pending_key   — one row per staged key (mirrors remote_pending_key)
//   report_config       — global pool config lives here as key/value pairs
//
// Scheduler:
//   startLocalPendingScheduler — 20s heartbeat, per-tick pool refill
//   gated on profile-level pool_interval_sec. Priority accumulates from
//   MAX(priority) of currently-enabled channels; failed rows don't block
//   the next batch.
//
// Auto mode reads recent /logs to compute a live RPM (5-min average) and
// sizes each tick's batch as ceil(rpm / rpm_base), capped at pool_batch_size.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ---- Config: stored as report_config key/value ----

const (
	cfgLocalPoolIntervalSec = "local_pool_interval_sec"
	cfgLocalPoolBatchSize   = "local_pool_batch_size"
	cfgLocalPoolAutoMode    = "local_pool_auto_mode"
	cfgLocalPoolRPMBase     = "local_pool_rpm_base"
	cfgLocalPoolRPMMin      = "local_pool_rpm_min"

	localPoolIntervalDef  = 60
	localPoolBatchDef     = 2
	localPoolRPMBaseDef   = 150
	localPoolRPMMinDef    = 50
	localPoolIntervalMin  = 5
	localPoolIntervalMax  = 3600
	localPoolBatchSizeMin = 1
	localPoolBatchSizeMax = 50
	localPoolMaxAttempts  = 3

	// Legacy default that handleBatchCreateChannels uses when no priority
	// override is supplied. We treat it as our fallback P starting point
	// when the channels table happens to be empty.
	localPoolFallbackPriority = 1001
)

type localPoolConfig struct {
	IntervalSec int  `json:"pool_interval_sec"`
	BatchSize   int  `json:"pool_batch_size"`
	AutoMode    bool `json:"auto_mode"`
	RPMBase     int  `json:"rpm_base"`
	RPMMin      int  `json:"rpm_min"`
}

func loadLocalPoolConfig() localPoolConfig {
	out := localPoolConfig{
		IntervalSec: localPoolIntervalDef,
		BatchSize:   localPoolBatchDef,
		AutoMode:    false,
		RPMBase:     localPoolRPMBaseDef,
		RPMMin:      localPoolRPMMinDef,
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
	readBool := func(key string, dst *bool) {
		var v string
		if err := db.QueryRow(`SELECT value FROM report_config WHERE key=$1`, key).Scan(&v); err != nil {
			return
		}
		*dst = strings.TrimSpace(v) == "true" || strings.TrimSpace(v) == "1"
	}
	readInt(cfgLocalPoolIntervalSec, &out.IntervalSec)
	readInt(cfgLocalPoolBatchSize, &out.BatchSize)
	readBool(cfgLocalPoolAutoMode, &out.AutoMode)
	readInt(cfgLocalPoolRPMBase, &out.RPMBase)
	readInt(cfgLocalPoolRPMMin, &out.RPMMin)
	// Reuse remote-pool clamps for the local values — same [min, max]
	// safety bounds apply to both since they feed the same scheduler
	// shape. Cheaper than duplicating five constants.
	out.IntervalSec = clampPoolInterval(out.IntervalSec)
	out.BatchSize = clampPoolBatchSize(out.BatchSize)
	if out.RPMBase <= 0 {
		out.RPMBase = localPoolRPMBaseDef
	}
	if out.RPMMin < 0 {
		out.RPMMin = 0
	}
	return out
}

func writeLocalPoolConfig(k, v string) error {
	now := time.Now().Unix()
	_, err := db.Exec(
		`INSERT INTO report_config (key, value, updated_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (key) DO UPDATE SET value=$2, updated_at=$3`,
		k, v, now,
	)
	return err
}

// ---- Handlers ----

func handleLocalPoolConfigGet(c *gin.Context) {
	c.JSON(http.StatusOK, loadLocalPoolConfig())
}

func handleLocalPoolConfigSet(c *gin.Context) {
	var body struct {
		IntervalSec *int  `json:"pool_interval_sec,omitempty"`
		BatchSize   *int  `json:"pool_batch_size,omitempty"`
		AutoMode    *bool `json:"auto_mode,omitempty"`
		RPMBase     *int  `json:"rpm_base,omitempty"`
		RPMMin      *int  `json:"rpm_min,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.IntervalSec != nil {
		if err := writeLocalPoolConfig(cfgLocalPoolIntervalSec, strconv.Itoa(clampPoolInterval(*body.IntervalSec))); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.BatchSize != nil {
		if err := writeLocalPoolConfig(cfgLocalPoolBatchSize, strconv.Itoa(clampPoolBatchSize(*body.BatchSize))); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.AutoMode != nil {
		if err := writeLocalPoolConfig(cfgLocalPoolAutoMode, strconv.FormatBool(*body.AutoMode)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.RPMBase != nil {
		v := *body.RPMBase
		if v <= 0 {
			v = localPoolRPMBaseDef
		}
		if err := writeLocalPoolConfig(cfgLocalPoolRPMBase, strconv.Itoa(v)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.RPMMin != nil {
		v := *body.RPMMin
		if v < 0 {
			v = 0
		}
		if err := writeLocalPoolConfig(cfgLocalPoolRPMMin, strconv.Itoa(v)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, loadLocalPoolConfig())
}

// handleLocalRPM returns the 5-minute moving average of successful log
// rows per minute across the whole logs table. Uncached — caller (the
// scheduler + admin UI) both benefit from freshness. `type=2` = normal
// completed request in new-api's logs schema.
func handleLocalRPM(c *gin.Context) {
	rpm, err := computeLocalRPM()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"rpm": rpm})
}

func computeLocalRPM() (int64, error) {
	since := time.Now().Add(-5 * time.Minute).Unix()
	var count int64
	err := db.QueryRow(
		`SELECT COUNT(*) FROM logs WHERE type=2 AND created_at >= $1`,
		since,
	).Scan(&count)
	if err != nil {
		return 0, err
	}
	// 5-minute window → per-minute average. Integer floor is fine —
	// the scheduler applies ceil(rpm / rpm_base) anyway.
	return count / 5, nil
}

// handleLocalPoolEnqueue mirrors handleBatchCreateChannels' input shape
// (studio + suffix + channels[]) so operator UX is the same. Difference:
// keys land in local_pending_key instead of being inserted into channels
// synchronously. The scheduler will pick them up on its next tick.
func handleLocalPoolEnqueue(c *gin.Context) {
	var body struct {
		Studio       string  `json:"studio"`
		Suffix       string  `json:"suffix"`
		UnitPriceCNY float64 `json:"unit_price_cny"`
		Channels     []struct {
			Key          string   `json:"key"`
			QuotaUSD     float64  `json:"quota_usd"`
			UnitPriceCNY *float64 `json:"unit_price_cny,omitempty"`
		} `json:"channels"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	studio := strings.TrimSpace(body.Studio)
	suffix := strings.TrimSpace(body.Suffix)
	// Studio operator: lock tag to their studio, ignore payload. Same
	// invariant as handleBatchCreateChannels — the JWT claim is the
	// source of truth.
	if callerIsStudioOperator(c) {
		userStudio := callerStudio(c)
		if userStudio == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "your account has no studio binding; ask an admin to bind one before uploading keys"})
			return
		}
		studio = userStudio
	}
	if studio == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "studio is required"})
		return
	}
	if suffix == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "suffix is required"})
		return
	}
	if len(body.Channels) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no channels provided"})
		return
	}
	now := time.Now().Unix()
	inserted, skipped := 0, 0
	for _, ch := range body.Channels {
		key := strings.TrimSpace(ch.Key)
		if key == "" || ch.QuotaUSD <= 0 {
			skipped++
			continue
		}
		enc, err := encryptRemoteToken(key)
		if err != nil {
			skipped++
			continue
		}
		hashBytes := sha256.Sum256([]byte(key))
		hash := hex.EncodeToString(hashBytes[:])
		unit := body.UnitPriceCNY
		if ch.UnitPriceCNY != nil {
			unit = *ch.UnitPriceCNY
		}
		var unitPtr *float64
		if unit > 0 {
			unitPtr = &unit
		}
		res, err := db.Exec(
			`INSERT INTO local_pending_key
			 (studio, suffix, key_hash, key_encrypted, quota_usd, unit_price_cny,
			  status, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,'pending',$7,$7)
			 ON CONFLICT (studio, key_hash) DO NOTHING`,
			studio, suffix, hash, enc, ch.QuotaUSD, unitPtr, now,
		)
		if err != nil {
			skipped++
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		} else {
			skipped++
		}
	}
	// Nudge the scheduler so a fresh batch starts uploading without
	// waiting up to interval_sec for the next tick.
	select {
	case localPoolNudge <- struct{}{}:
	default:
	}
	c.JSON(http.StatusOK, gin.H{"inserted": inserted, "skipped": skipped, "total": len(body.Channels)})
}

type localPendingView struct {
	ID           int64    `json:"id"`
	Studio       string   `json:"studio"`
	Suffix       string   `json:"suffix"`
	KeyMasked    string   `json:"key_masked"`
	QuotaUSD     float64  `json:"quota_usd"`
	UnitPriceCNY *float64 `json:"unit_price_cny,omitempty"`
	Status       string   `json:"status"`
	Priority     int64    `json:"priority"`
	ChannelID    int64    `json:"channel_id"`
	Attempts     int      `json:"attempts"`
	FailedReason string   `json:"failed_reason,omitempty"`
	CreatedAt    int64    `json:"created_at"`
	UpdatedAt    int64    `json:"updated_at"`
}

func handleLocalPoolList(c *gin.Context) {
	studioFilter := strings.TrimSpace(c.Query("studio"))
	statusFilter := strings.TrimSpace(c.Query("status"))
	q := `SELECT id, studio, suffix, key_encrypted, quota_usd, unit_price_cny,
	             status, priority, channel_id, attempts, failed_reason,
	             created_at, updated_at
	        FROM local_pending_key WHERE 1=1`
	args := []any{}
	if callerIsStudioOperator(c) {
		s := callerStudio(c)
		if s == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "your account has no studio binding; ask an admin to bind one before viewing the queue"})
			return
		}
		q += " AND studio=$" + strconv.Itoa(len(args)+1)
		args = append(args, s)
	} else if studioFilter != "" {
		q += " AND studio=$" + strconv.Itoa(len(args)+1)
		args = append(args, studioFilter)
	}
	if statusFilter != "" {
		q += " AND status=$" + strconv.Itoa(len(args)+1)
		args = append(args, statusFilter)
	}
	q += " ORDER BY id DESC LIMIT 2000"
	rows, err := db.Query(q, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := make([]localPendingView, 0)
	for rows.Next() {
		var (
			r    localPendingView
			enc  string
			unit sql.NullFloat64
		)
		if err := rows.Scan(&r.ID, &r.Studio, &r.Suffix, &enc, &r.QuotaUSD, &unit,
			&r.Status, &r.Priority, &r.ChannelID, &r.Attempts, &r.FailedReason,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if unit.Valid {
			v := unit.Float64
			r.UnitPriceCNY = &v
		}
		masked := "***"
		if k, err := decryptRemoteToken(enc); err == nil {
			masked = maskKey(k)
		}
		r.KeyMasked = masked
		out = append(out, r)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func handleLocalPoolDelete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	// Studio operator can only cancel their own studio's rows. Super
	// admin sees everything.
	if callerIsStudioOperator(c) {
		s := callerStudio(c)
		if s == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "your account has no studio binding; ask an admin to bind one before canceling queue entries"})
			return
		}
		res, err := db.Exec(
			`DELETE FROM local_pending_key
			  WHERE id=$1 AND studio=$2 AND status IN ('pending','failed')`,
			id, s,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		n, _ := res.RowsAffected()
		c.JSON(http.StatusOK, gin.H{"deleted": n})
		return
	}
	res, err := db.Exec(
		`DELETE FROM local_pending_key WHERE id=$1 AND status IN ('pending','failed')`,
		id,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	c.JSON(http.StatusOK, gin.H{"deleted": n})
}

// ---- Scheduler ----

var localPoolNudge = make(chan struct{}, 1)
var (
	localPoolNextMu sync.Mutex
	localPoolNext   time.Time
)

func startLocalPendingScheduler() {
	log.Printf("[local-pool] starting, tick=20s, retries=3")
	go func() {
		time.Sleep(15 * time.Second)
		runLocalPoolTick()
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				runLocalPoolTick()
			case <-localPoolNudge:
				runLocalPoolTick()
			}
		}
	}()
}

func runLocalPoolTick() {
	// Step 1: mark 'active' rows as 'used' when their channel was disabled
	// (channels.status != 1). Mirrors the remote-pool step-1 exactly, so
	// a poisoned key gets swept out and the "wait for full drain" gate
	// on step 3 can advance.
	reconcileLocalActive()

	// Step 2: pool refill, honouring config.interval + auto mode.
	cfg := loadLocalPoolConfig()
	nowT := time.Now()
	localPoolNextMu.Lock()
	due := localPoolNext.IsZero() || !nowT.Before(localPoolNext)
	localPoolNextMu.Unlock()
	if !due {
		return
	}
	// Reserve the next tick before doing any work.
	localPoolNextMu.Lock()
	localPoolNext = nowT.Add(time.Duration(cfg.IntervalSec) * time.Second)
	localPoolNextMu.Unlock()

	// Any still-active row means the previous batch hasn't fully died
	// yet — wait for it to drain. failed rows are excluded so a bad key
	// doesn't lock the pool.
	var activeCount int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM local_pending_key WHERE status='active'`,
	).Scan(&activeCount); err != nil {
		log.Printf("[local-pool] count active: %v", err)
		return
	}
	if activeCount > 0 {
		return
	}

	effective := cfg.BatchSize
	if cfg.AutoMode {
		rpm, err := computeLocalRPM()
		if err != nil {
			log.Printf("[local-pool] auto rpm fetch: %v — skipping tick", err)
			return
		}
		effective = autoBatchSize(int(rpm), cfg.RPMBase, cfg.RPMMin, cfg.BatchSize)
		log.Printf("[local-pool] auto rpm=%d base=%d min=%d cap=%d → n=%d",
			rpm, cfg.RPMBase, cfg.RPMMin, cfg.BatchSize, effective)
		if effective == 0 {
			return
		}
	}
	uploadLocalPoolBatch(effective)
}

func reconcileLocalActive() {
	now := time.Now().Unix()
	rows, err := db.Query(
		`SELECT p.id, p.channel_id, COALESCE(c.status, 0)
		   FROM local_pending_key p
		   LEFT JOIN channels c ON c.id = p.channel_id
		  WHERE p.status = 'active'`,
	)
	if err != nil {
		log.Printf("[local-pool] scan active: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var pID, chID int64
		var chStatus int
		if err := rows.Scan(&pID, &chID, &chStatus); err != nil {
			continue
		}
		// channels.status = 1 → enabled, anything else = disabled/banned.
		// If channels row is missing entirely (status=0 fallback via COALESCE
		// but chID=0 means we never populated it), skip.
		if chID > 0 && chStatus != 0 && chStatus != 1 {
			if _, err := db.Exec(
				`UPDATE local_pending_key SET status='used', used_at=$1, updated_at=$1 WHERE id=$2`,
				now, pID,
			); err != nil {
				log.Printf("[local-pool] mark used %d: %v", pID, err)
			}
		}
	}
}

// uploadLocalPoolBatch picks up to N oldest pending rows (FIFO by
// created_at, id) and inserts each into channels + abilities +
// report_key_quotas in a single tx, then marks the local_pending_key row
// 'active'. Priority is the running MAX(channels.priority) + 1 counter,
// falling back to localPoolFallbackPriority if the table is empty (this
// is what handleBatchCreateChannels' defaultChannelPriority uses too).
func uploadLocalPoolBatch(n int) {
	if n <= 0 {
		return
	}
	rows, err := db.Query(
		`SELECT id, studio, suffix, key_encrypted, quota_usd, unit_price_cny
		   FROM local_pending_key WHERE status='pending'
		  ORDER BY created_at ASC, id ASC
		  LIMIT $1`,
		n,
	)
	if err != nil {
		log.Printf("[local-pool] pick pending: %v", err)
		return
	}
	type job struct {
		id       int64
		studio   string
		suffix   string
		enc      string
		quota    float64
		unitPtr  *float64
	}
	jobs := make([]job, 0, n)
	for rows.Next() {
		var j job
		var unit sql.NullFloat64
		if err := rows.Scan(&j.id, &j.studio, &j.suffix, &j.enc, &j.quota, &unit); err != nil {
			continue
		}
		if unit.Valid {
			v := unit.Float64
			j.unitPtr = &v
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if len(jobs) == 0 {
		return
	}

	var pMax int64
	if err := db.QueryRow(
		`SELECT COALESCE(MAX(priority), 0) FROM channels WHERE status=1`,
	).Scan(&pMax); err != nil {
		log.Printf("[local-pool] pMax: %v", err)
		return
	}
	if pMax == 0 {
		pMax = localPoolFallbackPriority
	}

	activeModels := getBatchCreateModels()
	models := strings.Split(activeModels, ",")
	dateStr := time.Now().UTC().Format("0102")

	for _, j := range jobs {
		key, err := decryptRemoteToken(j.enc)
		if err != nil {
			recordLocalFailure(j.id, "decrypt: "+err.Error())
			continue
		}
		pMax++
		if err := insertLocalChannelForPending(j.id, j.studio, j.suffix, key, j.quota, j.unitPtr,
			pMax, dateStr, activeModels, models); err != nil {
			recordLocalFailure(j.id, err.Error())
			continue
		}
	}
}

// insertLocalChannelForPending performs the same INSERT sequence as
// handleBatchCreateChannels (channels + abilities + report_key_quotas)
// for a single pending row and flips it to 'active' on success. All in
// one tx so a mid-flight failure doesn't leave orphan abilities rows.
func insertLocalChannelForPending(pendingID int64, studio, suffix, key string, quotaUSD float64,
	unitPtr *float64, priority int64, dateStr, activeModels string, models []string) error {
	quotaInt := int(quotaUSD)
	name := fmt.Sprintf("%s-%s-%s-%d", dateStr, studio, suffix, quotaInt)
	now := time.Now().Unix()

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %v", err)
	}
	defer tx.Rollback()

	var channelID int64
	if err := tx.QueryRow(`
		INSERT INTO channels
		(type, key, status, name, weight, created_time, base_url, "group", models,
		 model_mapping, status_code_mapping, priority, auto_ban, used_quota, channel_info, tag)
		VALUES (14, $1, 1, $2, 0, $3, '', 'default', $4,
		        '', '', $7, 1, 0, $5::json, $6)
		RETURNING id`,
		key, name, now, activeModels, channelInfoDefault, studio, priority,
	).Scan(&channelID); err != nil {
		return fmt.Errorf("insert channel: %v", err)
	}
	for _, m := range models {
		if _, err := tx.Exec(`
			INSERT INTO abilities ("group", model, channel_id, enabled, priority, weight)
			VALUES ('default', $1, $2, true, $3, 0)
			ON CONFLICT DO NOTHING`,
			strings.TrimSpace(m), channelID, priority,
		); err != nil {
			return fmt.Errorf("insert ability: %v", err)
		}
	}
	if unitPtr != nil && *unitPtr > 0 {
		if _, err := tx.Exec(`
			INSERT INTO report_key_quotas (channel_id, quota_usd, unit_price_cny, updated_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (channel_id)
			DO UPDATE SET quota_usd=$2, unit_price_cny=$3, updated_at=$4`,
			channelID, quotaUSD, *unitPtr, now,
		); err != nil {
			return fmt.Errorf("insert quota: %v", err)
		}
	} else {
		if _, err := tx.Exec(`
			INSERT INTO report_key_quotas (channel_id, quota_usd, updated_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (channel_id) DO UPDATE SET quota_usd=$2, updated_at=$3`,
			channelID, quotaUSD, now,
		); err != nil {
			return fmt.Errorf("insert quota: %v", err)
		}
	}
	if _, err := tx.Exec(
		`UPDATE local_pending_key
		    SET status='active', channel_id=$1, priority=$2, activated_at=$3,
		        updated_at=$3, failed_reason=''
		  WHERE id=$4`,
		channelID, priority, now, pendingID,
	); err != nil {
		return fmt.Errorf("mark active: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %v", err)
	}
	return nil
}

func recordLocalFailure(id int64, reason string) {
	now := time.Now().Unix()
	if _, err := db.Exec(
		`UPDATE local_pending_key
		    SET attempts = attempts + 1,
		        status = CASE WHEN attempts + 1 >= $1 THEN 'failed' ELSE 'pending' END,
		        failed_reason = $2,
		        updated_at = $3
		  WHERE id = $4`,
		localPoolMaxAttempts, reason, now, id,
	); err != nil {
		log.Printf("[local-pool] record failure %d: %v", id, err)
	}
}

