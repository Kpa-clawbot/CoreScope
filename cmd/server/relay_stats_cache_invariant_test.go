package main

import (
	"testing"
)

// Issue #1164: every code path that mutates byPathHop MUST invalidate
// the batch relay-stats cache. Late-arriving observations that only
// append resolved-pubkey entries (not raw hops) are the regression
// vector — the prior coverage only fired when raw hops changed, so
// stale stats persisted for up to 5 minutes for nodes whose new path
// data arrived via resolved-pubkey indexing.
//
// This test exercises addResolvedPubkeysToPathHopIndex, the helper
// shared by Load / IngestNewFromDB / IngestNewObservations for the
// resolved-pubkey append path. It seeds the relay-stats cache, calls
// the helper, and asserts the cache was cleared.
func TestAddResolvedPubkeysToPathHopIndex_InvalidatesRelayStatsCache(t *testing.T) {
	s := &PacketStore{
		byPathHop:       make(map[string][]*StoreTx),
		relayStatsCache: map[string]RepeaterNodeStats{"sentinel": {}},
	}

	tx := &StoreTx{
		ID:          1,
		parsedPath:  []string{"a3"},
		pathParsed:  true,
		PayloadType: nil,
	}

	hopsSeen := make(map[string]bool, 8)
	mutated := s.addResolvedPubkeysToPathHopIndex(tx, []string{"deadbeefcafef00d"}, hopsSeen)
	if !mutated {
		t.Fatalf("expected mutated=true when adding a new resolved pubkey")
	}

	s.relayStatsCacheMu.Lock()
	cache := s.relayStatsCache
	s.relayStatsCacheMu.Unlock()
	if cache != nil {
		t.Fatalf("addResolvedPubkeysToPathHopIndex MUST invalidate relayStatsCache when byPathHop is mutated; got cache=%v", cache)
	}
}

// Negative case: when every supplied pubkey is already represented as
// a raw hop, no mutation happens and the cache should be preserved
// (no spurious lock-thrash).
func TestAddResolvedPubkeysToPathHopIndex_NoMutation_PreservesCache(t *testing.T) {
	s := &PacketStore{
		byPathHop:       make(map[string][]*StoreTx),
		relayStatsCache: map[string]RepeaterNodeStats{"sentinel": {}},
	}

	tx := &StoreTx{
		ID:         1,
		parsedPath: []string{"deadbeefcafef00d"},
		pathParsed: true,
	}

	hopsSeen := make(map[string]bool, 8)
	mutated := s.addResolvedPubkeysToPathHopIndex(tx, []string{"deadbeefcafef00d"}, hopsSeen)
	if mutated {
		t.Fatalf("expected mutated=false when pubkey already present as raw hop")
	}

	s.relayStatsCacheMu.Lock()
	cache := s.relayStatsCache
	s.relayStatsCacheMu.Unlock()
	if cache == nil {
		t.Fatalf("relayStatsCache must be preserved when no byPathHop mutation occurred")
	}
}
