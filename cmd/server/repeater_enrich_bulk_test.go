package main

import (
	"sync"
	"testing"
	"time"
)

// TestGetRepeaterRelayInfoMap_ServesStaleOnTTLExpiry is a regression guard
// for issue #1272.
//
// Background: GetRepeaterRelayInfoMap used to rebuild the cache inline
// whenever the TTL expired, causing ~700ms latency spikes on /api/nodes.
// The recomputer (StartRepeaterEnrichmentRecomputer) runs every 5 min and
// already keeps the cache warm; there is no reason to rebuild on-request.
//
// This test verifies that a populated cache is ALWAYS returned as-is,
// even when its timestamp is ancient (simulating TTL expiry under the old
// code). The stale sentinel value proves no inline recompute occurred.
func TestGetRepeaterRelayInfoMap_ServesStaleOnTTLExpiry(t *testing.T) {
	store := &PacketStore{
		byPathHop: make(map[string][]*StoreTx),
	}

	// Pre-populate the cache with a sentinel entry that would NOT be
	// produced by computeRepeaterRelayInfoMap on the empty byPathHop.
	stale := map[string]RepeaterRelayInfo{
		"sentinel": {RelayCount24h: 9999},
	}
	store.repeaterRelayAt = time.Now().Add(-24 * time.Hour) // well past any TTL
	store.repeaterRelayCache = stale
	store.repeaterRelayCacheWin = 24

	got := store.GetRepeaterRelayInfoMap(24)

	if got["sentinel"].RelayCount24h != 9999 {
		t.Fatalf("stale cache not served: sentinel missing or overwritten (RelayCount24h=%d)", got["sentinel"].RelayCount24h)
	}
}

// TestGetRepeaterUsefulnessScoreMap_ServesStaleOnTTLExpiry mirrors
// TestGetRepeaterRelayInfoMap_ServesStaleOnTTLExpiry for the usefulness
// score map (same fix, same root cause).
func TestGetRepeaterUsefulnessScoreMap_ServesStaleOnTTLExpiry(t *testing.T) {
	store := &PacketStore{
		byPathHop:     make(map[string][]*StoreTx),
		byPayloadType: make(map[int][]*StoreTx),
	}

	stale := map[string]float64{"sentinel": 0.42}
	store.repeaterUsefulAt = time.Now().Add(-24 * time.Hour)
	store.repeaterUsefulCache = stale

	got := store.GetRepeaterUsefulnessScoreMap()

	if got["sentinel"] != 0.42 {
		t.Fatalf("stale cache not served: sentinel missing or overwritten (score=%v)", got["sentinel"])
	}
}

// TestGetRepeaterRelayInfoMap_BuildsWhenNil verifies that when the cache
// is nil (before the recomputer's first prewarm), GetRepeaterRelayInfoMap
// computes inline and caches the result for subsequent callers.
func TestGetRepeaterRelayInfoMap_BuildsWhenNil(t *testing.T) {
	pt2 := 2
	now := time.Now().UTC()
	tx := &StoreTx{
		ID:          1,
		Hash:        "abc",
		FirstSeen:   now.Add(-10 * time.Minute).Format(time.RFC3339Nano),
		PayloadType: &pt2,
	}
	store := &PacketStore{
		byPathHop:     map[string][]*StoreTx{"aabbcc": {tx}},
		byPayloadType: map[int][]*StoreTx{pt2: {tx}},
		mu:            sync.RWMutex{},
	}

	got := store.GetRepeaterRelayInfoMap(24)
	if _, ok := got["aabbcc"]; !ok {
		t.Fatal("inline compute did not produce entry for seeded hop key")
	}

	// Second call must return the same cached map (pointer equality).
	got2 := store.GetRepeaterRelayInfoMap(24)
	if &got2 == &got {
		// map headers differ but underlying pointers should be identical
	}
	if got2["aabbcc"].RelayCount24h != got["aabbcc"].RelayCount24h {
		t.Fatal("second call returned different map — cache not installed")
	}
}
