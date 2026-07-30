package main

import (
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Map scope-filter parity follow-up: TransportedScopes (#1751) has no
// per-scope timestamp, so a scope carried weeks ago (but still resident in
// the in-memory byPathHop index) looks identical to one carried a minute
// ago. TransportedScopesRecent adds a per-scope recency gate — same
// windowHours threshold RelayActive already uses — so a live-map "Relayed
// Scope" filter can distinguish "still trustworthy" from "stale but not yet
// evicted".
//
// These tests exercise BOTH computation paths, same discipline as
// transported_scopes_1751_test.go:
//   - computeRepeaterRelayInfoMap (bulk, repeater_enrich_bulk.go)
//   - GetRepeaterRelayInfo        (per-node, repeater_liveness.go)

const scopeRecentKey = "ffeeddcc55667788"

// scopeTxAt builds a path-hop StoreTx with an explicit FirstSeen age.
func scopeTxAt(id int, payloadType int, scope string, age time.Duration) *StoreTx {
	pt := payloadType
	return &StoreTx{
		ID:          id,
		Hash:        "scope-recent-tx-" + scope + "-" + strconv.Itoa(id),
		PayloadType: &pt,
		ScopeName:   scope,
		FirstSeen:   time.Now().UTC().Add(-age).Format(time.RFC3339Nano),
	}
}

func TestTransportedScopesRecent_BulkSplitsFreshFromStale(t *testing.T) {
	fresh := scopeTxAt(1, 2, "region-fresh", 10*time.Minute)
	stale := scopeTxAt(2, 2, "region-stale", 48*time.Hour)

	store := &PacketStore{
		byPathHop: map[string][]*StoreTx{scopeRecentKey: {fresh, stale}},
		mu:        sync.RWMutex{},
	}

	info := store.computeRepeaterRelayInfoMap(24)[scopeRecentKey]

	wantAll := []string{"region-fresh", "region-stale"}
	if !reflect.DeepEqual(info.TransportedScopes, wantAll) {
		t.Fatalf("TransportedScopes = %v, want %v (recency must not affect the ever-seen set)", info.TransportedScopes, wantAll)
	}
	wantRecent := []string{"region-fresh"}
	if !reflect.DeepEqual(info.TransportedScopesRecent, wantRecent) {
		t.Fatalf("bulk TransportedScopesRecent = %v, want %v", info.TransportedScopesRecent, wantRecent)
	}
}

func TestTransportedScopesRecent_PerNodeMatchesBulk(t *testing.T) {
	fresh := scopeTxAt(1, 2, "region-fresh", 10*time.Minute)
	stale := scopeTxAt(2, 2, "region-stale", 48*time.Hour)

	store := &PacketStore{
		byPathHop: map[string][]*StoreTx{scopeRecentKey: {fresh, stale}},
		mu:        sync.RWMutex{},
	}

	info := store.GetRepeaterRelayInfo(scopeRecentKey, 24)
	want := []string{"region-fresh"}
	if !reflect.DeepEqual(info.TransportedScopesRecent, want) {
		t.Fatalf("per-node TransportedScopesRecent = %v, want %v", info.TransportedScopesRecent, want)
	}
}

// TestTransportedScopesRecent_DisabledWhenWindowHoursZero mirrors
// RelayActive's own convention: windowHours <= 0 means "recency
// undetermined", not "everything is recent".
func TestTransportedScopesRecent_DisabledWhenWindowHoursZero(t *testing.T) {
	fresh := scopeTxAt(1, 2, "region-fresh", 10*time.Minute)

	store := &PacketStore{
		byPathHop: map[string][]*StoreTx{scopeRecentKey: {fresh}},
		mu:        sync.RWMutex{},
	}

	bulkInfo := store.computeRepeaterRelayInfoMap(0)[scopeRecentKey]
	if len(bulkInfo.TransportedScopesRecent) != 0 {
		t.Fatalf("bulk: expected no TransportedScopesRecent when windowHours<=0, got %v", bulkInfo.TransportedScopesRecent)
	}
	if len(bulkInfo.TransportedScopes) == 0 {
		t.Fatalf("bulk: TransportedScopes should still be populated regardless of windowHours")
	}

	perNodeInfo := store.GetRepeaterRelayInfo(scopeRecentKey, 0)
	if len(perNodeInfo.TransportedScopesRecent) != 0 {
		t.Fatalf("per-node: expected no TransportedScopesRecent when windowHours<=0, got %v", perNodeInfo.TransportedScopesRecent)
	}
}

// TestTransportedScopesRecent_PrefixBucketExcluded mirrors
// TestTransportedScopes_PrefixBucketExcludedFromScope: the ambiguous 1-byte
// prefix bucket must not contribute to recency either, same rationale (the
// firmware only relays a region-scoped packet when its OWN configured
// region matches, so an unresolved hop can't be credited with any scope).
func TestTransportedScopesRecent_PrefixBucketExcluded(t *testing.T) {
	prefixOnly := scopeTxAt(1, 2, "region-via-prefix", 10*time.Minute)

	store := &PacketStore{
		byPathHop: map[string][]*StoreTx{
			// The full key must be present (even with no entries of its
			// own) for computeRepeaterRelayInfoMap's outer loop to visit
			// it and trigger the prefix-bucket fold-in logic at all.
			scopeRecentKey:     {},
			scopeRecentKey[:2]: {prefixOnly},
		},
		mu: sync.RWMutex{},
	}

	got := store.computeRepeaterRelayInfoMap(24)[scopeRecentKey].TransportedScopesRecent
	if len(got) != 0 {
		t.Fatalf("expected region-via-prefix excluded from TransportedScopesRecent (ambiguous prefix bucket), got %v", got)
	}
}

// TestTransportedScopesRecent_AdvertExcluded mirrors the existing
// advert-exclusion rule for TransportedScopes: a self-advert is not relay
// activity and must not seed recency either.
func TestTransportedScopesRecent_AdvertExcluded(t *testing.T) {
	advertOnly := scopeTxAt(1, payloadTypeAdvert, "advert-only-scope", 1*time.Minute)

	store := &PacketStore{
		byPathHop: map[string][]*StoreTx{scopeRecentKey: {advertOnly}},
		mu:        sync.RWMutex{},
	}

	bulkGot := store.computeRepeaterRelayInfoMap(24)[scopeRecentKey].TransportedScopesRecent
	if len(bulkGot) != 0 {
		t.Fatalf("bulk: expected advert excluded from TransportedScopesRecent, got %v", bulkGot)
	}
	perNodeGot := store.GetRepeaterRelayInfo(scopeRecentKey, 24).TransportedScopesRecent
	if len(perNodeGot) != 0 {
		t.Fatalf("per-node: expected advert excluded from TransportedScopesRecent, got %v", perNodeGot)
	}
}
