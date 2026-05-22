package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHandleNodesLimit2000ColdMiss is a regression guard for issue #1262.
//
// Background: PR #1260 added a 15s-TTL bulk-cache for repeater
// enrichment in handleNodes (GetRepeaterRelayInfoMap /
// GetRepeaterUsefulnessScoreMap). On warm hits the request is ~40ms.
// On the very first request after server startup (or after the 15s TTL
// expires) the cache rebuild runs on the request-serving goroutine and
// is O(byPathHop + parsed timestamps). On staging (75k tx, 600 nodes)
// the cold rebuild took 15.7s.
//
// /api/nodes?limit=2000 is the SPA's hop-resolver bootstrap call (see
// public/live.js) so EVERY cold SPA load eats the cold-rebuild cost.
//
// Acceptance: /api/nodes?limit=2000 must return in <2s on a
// realistic-shape fleet WITHOUT a prior warmup request — i.e. once the
// store has been initialized and the steady-state repeater-enrichment
// recomputer prewarm has run.
func TestHandleNodesLimit2000ColdMiss(t *testing.T) {
	if testing.Short() {
		t.Skip("perf test")
	}
	srv, router := setupTestServer(t)
	conn := srv.db.conn

	// Seed 600 nodes — 50 repeaters/rooms with most-recent last_seen so
	// they sit at the top of the limit=2000 page, plus 550 stale
	// companions.
	tx, err := conn.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count, foreign_advert)
		VALUES (?, ?, ?, 0, 0, ?, '2026-01-01T00:00:00Z', 1, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 0; i < 50; i++ {
		pk := fmt.Sprintf("pkrepeat%056x", i)
		ts := now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
		if _, err := stmt.Exec(pk, fmt.Sprintf("rep%d", i), "repeater", ts); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 550; i++ {
		pk := fmt.Sprintf("pkcompan%056x", i)
		ts := now.Add(-time.Duration(60+i) * time.Minute).Format(time.RFC3339Nano)
		if _, err := stmt.Exec(pk, fmt.Sprintf("comp%d", i), "companion", ts); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Seed the in-memory packet store: a non-trivial body of non-advert
	// traffic where each repeater appears as a path hop on many txs.
	// This is what makes the bulk-cache rebuild expensive.
	const numTx = 150000
	const hopsPerTx = 6
	pt2 := 2
	store := srv.store
	for i := 0; i < numTx; i++ {
		txID := 100000 + i
		ts := now.Add(-time.Duration(i) * time.Second).Format(time.RFC3339Nano)
		stx := &StoreTx{
			ID:          txID,
			Hash:        fmt.Sprintf("h%d", txID),
			FirstSeen:   ts,
			PayloadType: &pt2,
		}
		store.byPayloadType[pt2] = append(store.byPayloadType[pt2], stx)
		// Shared 1-byte prefix bucket to mirror production hop-prefix
		// collisions.
		store.byPathHop["pk"] = append(store.byPathHop["pk"], stx)
		for h := 0; h < hopsPerTx; h++ {
			repIdx := (i + h) % 50
			pk := fmt.Sprintf("pkrepeat%056x", repIdx)
			store.byPathHop[pk] = append(store.byPathHop[pk], stx)
		}
	}

	// Steady-state repeater-enrichment recomputer (the fix for #1262)
	// prewarms the bulk caches at startup so the first handler request
	// — which is /api/nodes?limit=2000 from live.js on every cold SPA
	// load — hits the cache instead of rebuilding it on-thread.
	stop := store.StartRepeaterEnrichmentRecomputer(24, 5*time.Minute)
	defer stop()

	// NO HTTP warmup — we are explicitly measuring the first
	// limit=2000 request, the way live.js sees it.

	start := time.Now()
	req := httptest.NewRequest("GET", "/api/nodes?limit=2000", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	elapsed := time.Since(start)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	const budget = 2 * time.Second
	t.Logf("/api/nodes?limit=2000 elapsed=%v on %d nodes, %d tx", elapsed, 600, numTx)
	if elapsed > budget {
		t.Fatalf("/api/nodes?limit=2000 cold-miss too slow for #1262: %v (budget %v) on %d nodes, %d tx",
			elapsed, budget, 600, numTx)
	}
}
