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
	// blacklist set is materialised on the first IsBlacklisted call below.
	// This is the realistic state: a deployment running with a populated
	// blacklist where the operator later ADDS a new entry.
	decoy := pk64("dec0")
	cfg := &Config{NodeBlacklist: []string{decoy}}
	srv := &Server{store: newTestStoreWithDB(t, db, cfg), db: db, cfg: cfg, perfStats: NewPerfStats()}

	// 1. Warm cache (must 200 and populate cache).
	rr := serveReach(srv, "/api/nodes/"+n+"/reach?days=30")
	if rr.Code != http.StatusOK {
		t.Fatalf("warm-up: status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	if srv.reachCacheLen() == 0 {
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

// TestConfig_BlacklistGenerationIncrements asserts that every SetNodeBlacklist
// call bumps the generation counter by exactly 1, regardless of whether the
// content changed. The /reach cache key embeds this generation, so the
// monotonic-bump contract is part of the public API of the package
// (adversarial #4 from round-1 polish).
func TestConfig_BlacklistGenerationIncrements(t *testing.T) {
	cfg := &Config{}
	g0 := cfg.BlacklistGeneration()
	cfg.SetNodeBlacklist([]string{"aa"})
	g1 := cfg.BlacklistGeneration()
	if g1 != g0+1 {
		t.Fatalf("first SetNodeBlacklist: gen %d -> %d (want +1)", g0, g1)
	}
	// Identical content — generation MUST still bump. Callers rely on
	// "any call invalidates" rather than "content-diff invalidates."
	cfg.SetNodeBlacklist([]string{"aa"})
	g2 := cfg.BlacklistGeneration()
	if g2 != g1+1 {
		t.Fatalf("second SetNodeBlacklist (same content): gen %d -> %d (want +1)", g1, g2)
	}
	// Empty mutation also bumps.
	cfg.SetNodeBlacklist(nil)
	g3 := cfg.BlacklistGeneration()
	if g3 != g2+1 {
		t.Fatalf("nil SetNodeBlacklist: gen %d -> %d (want +1)", g2, g3)
	}
}

// TestNodeReach_BlacklistMutationPurgesCache asserts that a blacklist
// mutation evicts ALL prior reach cache entries (not just the affected
// pubkey) on the next /reach request. Per adversarial #5, the previous
// gen-suffix-only design left every prior cached entry stranded until TTL,
// growing the cache by N entries per operator edit. The current design
// purges on generation bump (detected on the next handler invocation) so a
// steady stream of edits cannot leak entries unboundedly.
func TestNodeReach_BlacklistMutationPurgesCache(t *testing.T) {
	resetReachState(t)
	db, n := newReachIntegrationDB(t, `["AABB","01FA","CCDD"]`)
	defer db.conn.Close()

	cfg := &Config{}
	srv := &Server{store: newTestStoreWithDB(t, db, cfg), db: db, cfg: cfg, perfStats: NewPerfStats()}

	// Warm cache with two distinct keys (different days param).
	for _, days := range []string{"30", "7"} {
		rr := serveReach(srv, "/api/nodes/"+n+"/reach?days="+days)
		if rr.Code != http.StatusOK {
			t.Fatalf("warm-up days=%s: status=%d want 200", days, rr.Code)
		}
	}
	before := srv.reachCacheLen()
	if before < 2 {
		t.Fatalf("warm-up populated %d entries, want >=2", before)
	}

	// Unrelated blacklist mutation. The cached pubkey is not in the
	// blacklist, but prior entries are now keyed under a stale generation
	// and would otherwise sit until TTL.
	cfg.SetNodeBlacklist([]string{pk64("dead")})

	// Next /reach request triggers the purge inside the reach path.
	rr := serveReach(srv, "/api/nodes/"+n+"/reach?days=30")
	if rr.Code != http.StatusOK {
		t.Fatalf("post-mutation request: status=%d want 200", rr.Code)
	}
	// After the purge + this single re-populate we expect exactly 1 entry,
	// not the 2 stale + 1 new = 3 that the leaky design would leave behind.
	if got := srv.reachCacheLen(); got != 1 {
		t.Fatalf("post-mutation cache len = %d, want 1 (prior entries leaked — adv #5)", got)
	}
}
