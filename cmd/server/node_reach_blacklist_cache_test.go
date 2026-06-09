package main

import (
	"net/http"
	"testing"
)

// TestNodeReach_BlacklistMutationBustsCache reproduces #1629.
//
// Scenario:
//  1. Warm the reach response cache with a non-blacklisted pubkey (200 OK).
//  2. Operator blacklists that pubkey via SetNodeBlacklist (the legitimate
//     mutation entry point — config reload, admin call, etc.).
//  3. The very next /reach request for that pubkey MUST return 404 (the
//     blacklist response), not the cached 200 payload.
//
// Pre-fix the blacklist set is locked in by sync.Once at first read, so
// IsBlacklisted keeps returning false after the mutation; the cache then
// re-serves the prior reach body and the assertion fails.
func TestNodeReach_BlacklistMutationBustsCache(t *testing.T) {
	resetReachState(t)
	db, n := newReachIntegrationDB(t, `["AABB","01FA","CCDD"]`)
	defer db.conn.Close()

	// Start with a non-empty blacklist (some unrelated decoy pubkey) so the
	// blacklist set is materialised (sync.Once fires) on the first
	// IsBlacklisted call below. This is the realistic state: a deployment
	// running with a populated blacklist where the operator later ADDS a
	// new entry.
	decoy := pk64("dec0")
	cfg := &Config{NodeBlacklist: []string{decoy}}
	srv := &Server{store: newTestStoreWithDB(t, db, cfg), db: db, cfg: cfg, perfStats: NewPerfStats()}

	// 1. Warm cache (must 200 and populate cache). This call also primes the
	// blacklistSet sync.Once with {decoy}.
	rr := serveReach(srv, "/api/nodes/"+n+"/reach?days=30")
	if rr.Code != http.StatusOK {
		t.Fatalf("warm-up: status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	// Sanity: response was cached under the old key shape.
	srv.reach.cacheMu.RLock()
	cacheLen := len(srv.reach.cache)
	srv.reach.cacheMu.RUnlock()
	if cacheLen == 0 {
		t.Fatalf("warm-up did not populate reach cache")
	}

	// 2. Operator adds the target node to the blacklist via the public setter.
	cfg.SetNodeBlacklist([]string{decoy, n})

	// 3. Next request MUST return 404. With the bug, the sync.Once-cached
	// empty blacklist set makes IsBlacklisted return false, the response
	// cache hits, and the prior 200 body is re-served.
	rr2 := serveReach(srv, "/api/nodes/"+n+"/reach?days=30")
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("post-blacklist mutation: status=%d want 404 (cached payload was served — #1629)", rr2.Code)
	}
}
