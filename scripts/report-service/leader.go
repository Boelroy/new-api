package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"
)

// Leader election for multi-node report-service deployments.
//
// A single-row lease table (report_leader, id=1) is the source of truth.
// Every node heartbeats every leaderHeartbeat; the row carries the current
// leader_id and an expires_at leaderTTL in the future. A node becomes leader
// by atomically claiming the row when it is either already the leader
// (renewal) or expired (takeover) — see the UPSERT in electOnce. Postgres
// now() is the clock for both nodes, so there is no cross-host skew.
//
// Only the singleton background schedulers (Lark notify, daily aggregation,
// remote snapshots, key uploads, auto-disable, etc.) are gated on IsLeader().
// Node-local work (HTTP, in-memory test/eval jobs, the test-job reaper) runs
// on every node regardless.

const (
	leaderTTL       = 30 * time.Second
	leaderHeartbeat = 10 * time.Second
)

var (
	isLeaderFlag atomic.Bool
	reportNodeID string
)

// IsLeader reports whether this node currently holds the leader lease. Cheap
// (atomic load) — safe to call on every scheduler tick.
func IsLeader() bool { return isLeaderFlag.Load() }

// NodeID returns this process's stable node identifier used in the lease.
func NodeID() string { return reportNodeID }

func computeNodeID() string {
	if v := os.Getenv("REPORT_NODE_ID"); v != "" {
		return v
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

// electOnce runs a single claim/renew attempt and updates isLeaderFlag.
// Returns the current leader_id (this node's id when we hold it, the other
// node's id when we don't) for logging/observability.
func electOnce() (string, error) {
	var winner string
	err := db.QueryRow(`
		INSERT INTO report_leader (id, leader_id, expires_at, updated_at)
		VALUES (1, $1, now() + ($2 || ' seconds')::interval, now())
		ON CONFLICT (id) DO UPDATE
		    SET leader_id  = EXCLUDED.leader_id,
		        expires_at = EXCLUDED.expires_at,
		        updated_at = now()
		    WHERE report_leader.leader_id = EXCLUDED.leader_id
		       OR report_leader.expires_at < now()
		RETURNING leader_id`,
		reportNodeID, fmt.Sprintf("%d", int(leaderTTL.Seconds())),
	).Scan(&winner)

	if err == sql.ErrNoRows {
		// Another node holds an unexpired lease — we are a follower.
		if isLeaderFlag.Swap(false) {
			log.Printf("[leader] lost leadership (node=%s)", reportNodeID)
		}
		return "", nil
	}
	if err != nil {
		// On DB error, do not assume leadership. Keep prior state on a
		// transient blip? Safer to step down so two nodes never both act.
		if isLeaderFlag.Swap(false) {
			log.Printf("[leader] stepping down after election error (node=%s): %v", reportNodeID, err)
		}
		return "", err
	}

	nowLeader := winner == reportNodeID
	if nowLeader && !isLeaderFlag.Swap(true) {
		log.Printf("[leader] became leader (node=%s)", reportNodeID)
	} else if !nowLeader && isLeaderFlag.Swap(false) {
		log.Printf("[leader] lost leadership to %s (node=%s)", winner, reportNodeID)
	}
	return winner, nil
}

// startLeaderElection computes this node's id, runs one synchronous election
// (so IsLeader() is meaningful before the schedulers start — avoids both
// nodes firing an initial run), then heartbeats in the background.
func startLeaderElection() {
	reportNodeID = computeNodeID()
	log.Printf("[leader] node id = %s (ttl=%s, heartbeat=%s)", reportNodeID, leaderTTL, leaderHeartbeat)

	if _, err := electOnce(); err != nil {
		log.Printf("[leader] initial election error (node=%s): %v", reportNodeID, err)
	}
	log.Printf("[leader] initial state: isLeader=%v", IsLeader())

	go func() {
		t := time.NewTicker(leaderHeartbeat)
		defer t.Stop()
		for range t.C {
			if _, err := electOnce(); err != nil {
				log.Printf("[leader] heartbeat error (node=%s): %v", reportNodeID, err)
			}
		}
	}()
}

// currentLeaderID returns the leader_id recorded in the lease row (empty if
// none / expired). For the /api/leader observability endpoint.
func currentLeaderID() (leaderID string, expired bool) {
	var expiresAt time.Time
	err := db.QueryRow(`SELECT leader_id, expires_at FROM report_leader WHERE id = 1`).Scan(&leaderID, &expiresAt)
	if err != nil {
		return "", true
	}
	return leaderID, time.Now().After(expiresAt)
}
