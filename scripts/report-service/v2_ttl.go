package main

// TTL cron for awaiting_assignment key pool rows.
//
// Studio operators can upload keys into the pool that never get assigned
// to a profile. Without a TTL the queue grows without bound. Default: 30
// days; overridable via env RS_POOL_AWAITING_TTL_DAYS.

import (
	"log"
	"os"
	"strconv"
	"time"
)

const awaitingAssignmentTTLDefaultDays = 30
const awaitingAssignmentTTLInterval = time.Hour

func awaitingAssignmentTTLLoop() {
	// Read TTL from env; default 30d.
	ttlDays := awaitingAssignmentTTLDefaultDays
	if v := os.Getenv("RS_POOL_AWAITING_TTL_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ttlDays = n
		}
	}
	tick := time.NewTicker(awaitingAssignmentTTLInterval)
	defer tick.Stop()
	for range tick.C {
		cutoff := time.Now().Add(-time.Duration(ttlDays) * 24 * time.Hour).Unix()
		res, err := db.Exec(
			`DELETE FROM rs_key_pool WHERE status='awaiting_assignment' AND created_at < $1`,
			cutoff,
		)
		if err != nil {
			log.Printf("[v2 ttl] awaiting_assignment purge error: %v", sanitizeUpstreamMessage(err.Error()))
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("[v2 ttl] purged %d awaiting_assignment rows older than %d days", n, ttlDays)
		}
	}
}
