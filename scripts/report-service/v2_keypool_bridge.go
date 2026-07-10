package main

// Scheduler bridge: mirror remote_pending_key state changes back to
// rs_key_pool.
//
// V1's remote pending scheduler in remote_newapi.go owns the actual upload
// lifecycle. Rather than instrument that path (which would spread V2
// concerns across V1 code), we run a poll loop that reconciles rs_key_pool
// against remote_pending_key on a short cadence.
//
// State machine:
//   rs_key_pool.status = 'pending'  →  when the matching remote_pending_key
//                                       row (by key_hash + profile_id) is
//                                       'active' → mirror to 'active' and
//                                       copy remote_channel_id.
//                                    →  when it is 'failed' → mirror to
//                                       'failed' with sanitized reason.
//   rs_key_pool.status = 'active'   →  when the matching remote_pending_key
//                                       row transitions to 'used' → mirror
//                                       to 'used' (typically after a manual
//                                       remote channel deletion).
//
// This is safe to run concurrently with V1's own scheduler; the UPDATEs
// are conditional on the current rs_key_pool.status so we never fight it.

import (
	"log"
	"time"
)

const keyPoolBridgeInterval = 10 * time.Second

// startAwaitingAssignmentTTL is the M3 TTL cron — declared here so it can
// live alongside the bridge goroutine start (both are long-lived
// background workers with the same pattern). Implemented in v2_ttl.go.
func startAwaitingAssignmentTTL() {
	go awaitingAssignmentTTLLoop()
	go keyPoolBridgeLoop()
}

func keyPoolBridgeLoop() {
	// Give V1 schedulers a beat to warm up before we start querying.
	time.Sleep(2 * time.Second)
	tick := time.NewTicker(keyPoolBridgeInterval)
	defer tick.Stop()
	for range tick.C {
		if err := reconcileKeyPool(); err != nil {
			log.Printf("[v2 keypool bridge] reconcile: %v", sanitizeUpstreamMessage(err.Error()))
		}
	}
}

// reconcileKeyPool runs one pass of the state-mirror queries. Returns the
// first error encountered but does not stop mid-run.
func reconcileKeyPool() error {
	now := time.Now().Unix()

	// pending → active
	_, err := db.Exec(
		`UPDATE rs_key_pool k
		    SET status = 'active',
		        remote_channel_id = rp.remote_channel_id,
		        failed_reason = '',
		        updated_at = $1
		   FROM remote_pending_key rp
		  WHERE k.status = 'pending'
		    AND rp.profile_id = k.assigned_profile_id
		    AND rp.key_hash   = k.key_hash
		    AND rp.status = 'active'`,
		now,
	)
	if err != nil {
		return err
	}

	// pending → failed. Do this in two steps so we can sanitize the
	// failed_reason in Go (V1 may write raw upstream body to
	// remote_pending_key.failed_reason; §3.6.5 wants a redacted mirror).
	type failedTransition struct {
		poolID int64
		reason string
	}
	rowsDB, err := db.Query(
		`SELECT k.id, COALESCE(rp.failed_reason, '')
		   FROM rs_key_pool k
		   JOIN remote_pending_key rp
		     ON rp.profile_id = k.assigned_profile_id AND rp.key_hash = k.key_hash
		  WHERE k.status = 'pending' AND rp.status = 'failed'`,
	)
	if err != nil {
		return err
	}
	pending := make([]failedTransition, 0)
	for rowsDB.Next() {
		var t failedTransition
		if err := rowsDB.Scan(&t.poolID, &t.reason); err != nil {
			rowsDB.Close()
			return err
		}
		pending = append(pending, t)
	}
	rowsDB.Close()
	for _, t := range pending {
		clean := sanitizeUpstreamMessage(t.reason)
		if _, err := db.Exec(
			`UPDATE rs_key_pool SET status='failed', failed_reason=$1, updated_at=$2 WHERE id=$3 AND status='pending'`,
			clean, now, t.poolID,
		); err != nil {
			return err
		}
	}

	// active → used (channel removed on remote)
	_, err = db.Exec(
		`UPDATE rs_key_pool k
		    SET status = 'used',
		        updated_at = $1
		   FROM remote_pending_key rp
		  WHERE k.status = 'active'
		    AND rp.profile_id = k.assigned_profile_id
		    AND rp.key_hash   = k.key_hash
		    AND rp.status = 'used'`,
		now,
	)
	if err != nil {
		return err
	}
	return nil
}
