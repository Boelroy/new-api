package main

// 腾讯一站 · 月对账源数据缓存. Reading a month's sources live (portal bill,
// CC Max bills, the upstream New API's logs channel by channel) takes about a
// minute, so the preview reads rs_tencent_source_cache instead and prices it
// with the current config. The leader refreshes last month and this month
// every day at tencent_source_cache_time (Beijing, default 04:00) and fills any
// missing month once after startup; super admins can recompute a month by
// hand. A computation is claimed in the table first, so a month is computed
// once at a time across nodes. Snapshots and pushes still read live.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	// cfgTencentSourceCacheTime is the daily refresh time, "HH:MM" Beijing;
	// "off" disables it.
	cfgTencentSourceCacheTime = "tencent_source_cache_time"
	// cfgTencentSourceCacheDay is the last Beijing day (YYYY-MM-DD) the daily
	// refresh claimed, so it runs once a day across nodes and restarts.
	cfgTencentSourceCacheDay = "tencent_source_cache_day"

	tencentSourceCacheDefaultTime = "04:00"
	// tencentSourceCacheStale is how long a claimed computation may run before
	// another one may take over (the node computing it died).
	tencentSourceCacheStale = 30 * time.Minute
	// tencentSourceSettleDay: last month is refreshed daily up to this day of
	// the month; afterwards only once more if its cache predates the day, as
	// the portal and CC Max bills of a month settle in its first days.
	tencentSourceSettleDay = 6
)

// tencentSourceCache is a month's cached source data.
type tencentSourceCache struct {
	Rows              []tencentSourceRow
	Warnings          []string
	CCMaxFirstAccount int64
	ComputedAt        int64
	DurationMS        int64
	ComputingSince    int64
	Error             string
}

func (c tencentSourceCache) computing(now time.Time) bool {
	return c.ComputingSince > 0 && now.Sub(time.Unix(c.ComputingSince, 0)) < tencentSourceCacheStale
}

// tencentLoadSourceCache reads a month's cache; a month never computed reads
// as the zero value.
func tencentLoadSourceCache(month time.Time) (tencentSourceCache, error) {
	var c tencentSourceCache
	var rows, warnings string
	err := db.QueryRow(`
		SELECT rows, warnings, ccmax_first_account, computed_at, duration_ms, computing_since, error
		  FROM rs_tencent_source_cache WHERE month = $1`, month.Format("2006-01")).
		Scan(&rows, &warnings, &c.CCMaxFirstAccount, &c.ComputedAt, &c.DurationMS, &c.ComputingSince, &c.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal([]byte(rows), &c.Rows); err != nil {
		return c, fmt.Errorf("source cache %s: %w", month.Format("2006-01"), err)
	}
	_ = json.Unmarshal([]byte(warnings), &c.Warnings)
	return c, nil
}

// tencentClaimSourceCompute marks a month as being computed unless another
// computation of it is running. Returns whether this call claimed it.
func tencentClaimSourceCompute(month time.Time) (bool, error) {
	now := time.Now()
	res, err := db.Exec(`
		INSERT INTO rs_tencent_source_cache (month, computing_since, updated_at) VALUES ($1, $2, $2)
		ON CONFLICT (month) DO UPDATE SET computing_since = $2, updated_at = $2
		 WHERE rs_tencent_source_cache.computing_since = 0 OR rs_tencent_source_cache.computing_since < $3`,
		month.Format("2006-01"), now.Unix(), now.Add(-tencentSourceCacheStale).Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// tencentComputeSourceCache reads a claimed month's sources live and stores
// them; on failure the previous rows stay and the error is kept.
func tencentComputeSourceCache(month time.Time) error {
	key := month.Format("2006-01")
	began := time.Now()
	rows, firstAccount, warnings, err := tencentLoadSourcesFn(month, month.AddDate(0, 1, 0))
	now := time.Now()
	if err != nil {
		if _, dbErr := db.Exec(`UPDATE rs_tencent_source_cache SET computing_since = 0, error = $2, updated_at = $3 WHERE month = $1`,
			key, err.Error(), now.Unix()); dbErr != nil {
			log.Printf("[tencent-cache] record failure of %s: %v", key, dbErr)
		}
		return err
	}
	rowsJSON, _ := json.Marshal(rows)
	if warnings == nil {
		warnings = []string{}
	}
	warningsJSON, _ := json.Marshal(warnings)
	_, err = db.Exec(`
		UPDATE rs_tencent_source_cache SET rows = $2, warnings = $3, ccmax_first_account = $4, computed_at = $5,
		       duration_ms = $6, computing_since = 0, error = '', updated_at = $5
		 WHERE month = $1`,
		key, string(rowsJSON), string(warningsJSON), firstAccount, now.Unix(), now.Sub(began).Milliseconds())
	if err == nil {
		log.Printf("[tencent-cache] %s: %d source rows in %s", key, len(rows), now.Sub(began).Round(time.Millisecond))
	}
	return err
}

// tencentRunSourceCompute claims a month and computes it in this goroutine.
// Returns false, nil when another computation of the month is running.
func tencentRunSourceCompute(month time.Time) (bool, error) {
	claimed, err := tencentClaimSourceCompute(month)
	if err != nil || !claimed {
		return false, err
	}
	return true, tencentComputeSourceCache(month)
}

// tencentStartSourceCompute claims a month and computes it in the background.
// Returns whether this call started it; false with no error means another
// computation of the month is running.
func tencentStartSourceCompute(month time.Time) (bool, error) {
	claimed, err := tencentClaimSourceCompute(month)
	if err != nil || !claimed {
		return false, err
	}
	go func() {
		if err := tencentComputeSourceCache(month); err != nil {
			log.Printf("[tencent-cache] compute %s: %v", month.Format("2006-01"), err)
		}
	}()
	return true, nil
}

// tencentSourceCacheTime parses the daily refresh time; ok is false when the
// refresh is off.
func tencentSourceCacheTime() (hour, minute int, ok bool) {
	raw := strings.TrimSpace(supplierConfigGet(cfgTencentSourceCacheTime))
	if raw == "" {
		raw = tencentSourceCacheDefaultTime
	}
	if strings.EqualFold(raw, "off") {
		return 0, 0, false
	}
	t, err := time.Parse("15:04", raw)
	if err != nil {
		t, _ = time.Parse("15:04", tencentSourceCacheDefaultTime)
	}
	return t.Hour(), t.Minute(), true
}

// tencentSourceCacheMonthsDue returns the months the daily refresh computes
// at now: this month, and last month on its settling days or when its cache
// predates them.
func tencentSourceCacheMonthsDue(now time.Time) ([]time.Time, error) {
	now = now.In(billingExportZone)
	this := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, billingExportZone)
	last := this.AddDate(0, -1, 0)
	months := []time.Time{this}
	if now.Day() <= tencentSourceSettleDay {
		return append([]time.Time{last}, months...), nil
	}
	cache, err := tencentLoadSourceCache(last)
	if err != nil {
		return nil, err
	}
	settled := time.Date(now.Year(), now.Month(), tencentSourceSettleDay, 0, 0, 0, 0, billingExportZone)
	if cache.ComputedAt < settled.Unix() {
		months = append([]time.Time{last}, months...)
	}
	return months, nil
}

// tencentSourceCacheTick runs the daily refresh once its time has come today
// (Beijing), claiming the day first. Returns whether this call ran it.
func tencentSourceCacheTick(now time.Time) (bool, error) {
	hour, minute, ok := tencentSourceCacheTime()
	if !ok {
		return false, nil
	}
	now = now.In(billingExportZone)
	due := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, billingExportZone)
	if now.Before(due) {
		return false, nil
	}
	day := now.Format("2006-01-02")
	res, err := db.Exec(`
		INSERT INTO report_config (key, value, updated_at) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = $3 WHERE report_config.value < $2`,
		cfgTencentSourceCacheDay, day, now.Unix())
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	months, err := tencentSourceCacheMonthsDue(now)
	if err != nil {
		return true, err
	}
	var errs []error
	for _, m := range months {
		if _, err := tencentRunSourceCompute(m); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.Format("2006-01"), err))
		}
	}
	return true, errors.Join(errs...)
}

// tencentSourceCacheBackfill computes last month and this month in the
// background when they have never been computed.
func tencentSourceCacheBackfill(now time.Time) {
	now = now.In(billingExportZone)
	this := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, billingExportZone)
	for _, m := range []time.Time{this.AddDate(0, -1, 0), this} {
		cache, err := tencentLoadSourceCache(m)
		if err != nil {
			log.Printf("[tencent-cache] backfill %s: %v", m.Format("2006-01"), err)
			continue
		}
		if cache.ComputedAt == 0 && !cache.computing(now) {
			if _, err := tencentRunSourceCompute(m); err != nil {
				log.Printf("[tencent-cache] backfill %s: %v", m.Format("2006-01"), err)
			}
		}
	}
}

// startTencentSourceCacheLoop checks the daily refresh every minute on the
// leader, and fills missing months the first time this node leads.
func startTencentSourceCacheLoop() {
	go func() {
		backfilled := false
		for {
			time.Sleep(time.Until(time.Now().Truncate(time.Minute).Add(time.Minute)))
			if !IsLeader() {
				continue
			}
			if !backfilled {
				backfilled = true
				tencentSourceCacheBackfill(time.Now())
			}
			if ran, err := tencentSourceCacheTick(time.Now()); err != nil {
				log.Printf("[tencent-cache] daily refresh (ran=%v): %v", ran, err)
			}
		}
	}()
}

// handleTencentStatementRecompute recomputes a month's source cache in the
// background (?month, default last month); a month already being computed
// answers its state without starting another.
func handleTencentStatementRecompute(c *gin.Context) {
	month := c.Query("month")
	if month == "" {
		now := time.Now().In(billingExportZone)
		month = time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, billingExportZone).Format("2006-01")
	}
	start, _, err := tencentMonthRange(month)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	cache, err := tencentLoadSourceCache(start)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	started := false
	if !cache.computing(time.Now()) {
		if started, err = tencentStartSourceCompute(start); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusAccepted, gin.H{
		"month":       start.Format("2006-01"),
		"computing":   true,
		"started":     started,
		"computed_at": cache.ComputedAt,
		"duration_ms": cache.DurationMS,
		"error":       cache.Error,
	})
}

// handleTencentSourceCacheSettingsGet / Set expose the daily refresh time.
func handleTencentSourceCacheSettingsGet(c *gin.Context) {
	hour, minute, ok := tencentSourceCacheTime()
	value := "off"
	if ok {
		value = fmt.Sprintf("%02d:%02d", hour, minute)
	}
	c.JSON(http.StatusOK, gin.H{"time": value, "last_day": supplierConfigGet(cfgTencentSourceCacheDay)})
}

func handleTencentSourceCacheSettingsSet(c *gin.Context) {
	var body struct {
		Time string `json:"time"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	value := strings.TrimSpace(body.Time)
	if !strings.EqualFold(value, "off") {
		t, err := time.Parse("15:04", value)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "时间须为 HH:MM（北京时间）或 off"})
			return
		}
		value = t.Format("15:04")
	} else {
		value = "off"
	}
	if err := supplierConfigSet(cfgTencentSourceCacheTime, value); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	handleTencentSourceCacheSettingsGet(c)
}
