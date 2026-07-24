package main

import (
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Issue #1751: repeater/room nodes should expose the deduplicated, sorted
// set of region scope names (transmissions.scope_name) across every
// non-advert packet in which they appear as a path hop. Advert packets must
// be excluded (a self-advert is not transported traffic).
//
// These tests exercise BOTH computation paths that feed RepeaterRelayInfo:
//   - computeRepeaterRelayInfoMap (bulk, repeater_enrich_bulk.go)
//   - GetRepeaterRelayInfo        (per-node, repeater_liveness.go)
// so the field stays in parity for /api/nodes (bulk) and the single-node
// detail endpoint (per-node).

const scope1751Key = "aabbccdd11223344"

// scopeTx builds a path-hop StoreTx with the given payload type, scope name,
// and an in-window FirstSeen.
func scopeTx(id int, payloadType int, scope string) *StoreTx {
	pt := payloadType
	return &StoreTx{
		ID:          id,
		Hash:        "scope-tx-" + scope + "-" + strconv.Itoa(id),
		PayloadType: &pt,
		ScopeName:   scope,
		FirstSeen:   time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano),
	}
}

func TestTransportedScopes_BulkDedupSortAndAdvertExcluded(t *testing.T) {
	// Three non-advert packets across two distinct scopes (one repeated to
	// prove dedup) PLUS one advert carrying a scope that must NOT appear.
	txMsgWest := scopeTx(1, 2, "region-west")  // TXT_MSG
	txGrpEast := scopeTx(2, 5, "region-east")  // GRP_TXT
	txMsgWest2 := scopeTx(3, 1, "region-west") // REQ, duplicate scope
	advertSecret := scopeTx(4, payloadTypeAdvert, "advert-only-scope")

	store := &PacketStore{
		byPathHop: map[string][]*StoreTx{
			scope1751Key: {txMsgWest, txGrpEast, txMsgWest2, advertSecret},
		},
		mu: sync.RWMutex{},
	}

	out := store.computeRepeaterRelayInfoMap(24)
	got := out[scope1751Key].TransportedScopes

	want := []string{"region-east", "region-west"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bulk TransportedScopes = %v, want %v (deduped, sorted, advert-excluded)", got, want)
	}
}

func TestTransportedScopes_PerNodeMatchesBulk(t *testing.T) {
	txMsgWest := scopeTx(1, 2, "region-west")
	txGrpEast := scopeTx(2, 5, "region-east")
	advertSecret := scopeTx(3, payloadTypeAdvert, "advert-only-scope")

	store := &PacketStore{
		byPathHop: map[string][]*StoreTx{
			scope1751Key: {txMsgWest, txGrpEast, advertSecret},
		},
		mu: sync.RWMutex{},
	}

	info := store.GetRepeaterRelayInfo(scope1751Key, 24)
	got := info.TransportedScopes

	want := []string{"region-east", "region-west"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("per-node TransportedScopes = %v, want %v (deduped, sorted, advert-excluded)", got, want)
	}
}

// TestTransportedScopes_EmptyWhenNoScope guards the "field absent" contract:
// a repeater whose path-hop packets carry no scope_name (older schema /
// hasScopeName=false → ScopeName always "") must yield a nil/empty slice so
// routes.go omits the JSON field entirely.
func TestTransportedScopes_EmptyWhenNoScope(t *testing.T) {
	noScope := scopeTx(1, 2, "") // non-advert but ScopeName==""

	store := &PacketStore{
		byPathHop: map[string][]*StoreTx{scope1751Key: {noScope}},
		mu:        sync.RWMutex{},
	}

	if got := store.computeRepeaterRelayInfoMap(24)[scope1751Key].TransportedScopes; len(got) != 0 {
		t.Fatalf("bulk: expected no scopes when ScopeName empty, got %v", got)
	}
	if got := store.GetRepeaterRelayInfo(scope1751Key, 24).TransportedScopes; len(got) != 0 {
		t.Fatalf("per-node: expected no scopes when ScopeName empty, got %v", got)
	}
}

// TestTransportedScopes_PrefixBucketExcludedFromScope covers the fix for
// the false-attribution bug found via a real stg.meshview.dk report: the
// 1-byte raw-prefix bucket (ambiguous hop hash — could be ANY node sharing
// that first byte) still folds into RelayCount/LastRelayed as before, but
// must NOT contribute to TransportedScopes. MeshCore firmware only relays a
// TRANSPORT_FLOOD/DIRECT packet when the repeater's own configured region
// matches the packet's (examples/simple_repeater/MyMesh.cpp
// allowPacketForward + RegionMap::findMatch), so crediting a scope from an
// unresolved hop would assert something the protocol itself wouldn't allow.
func TestTransportedScopes_PrefixBucketExcludedFromScope(t *testing.T) {
	full := scopeTx(1, 2, "region-direct")           // only in the full-key bucket — must count
	prefixOnly := scopeTx(2, 2, "region-via-prefix") // only in the 1-byte bucket — must NOT count
	shared := scopeTx(3, 2, "region-shared")         // in BOTH buckets — must count (present in the full-key list)

	store := &PacketStore{
		byPathHop: map[string][]*StoreTx{
			scope1751Key:     {full, shared},
			scope1751Key[:2]: {prefixOnly, shared},
		},
		mu: sync.RWMutex{},
	}

	got := store.computeRepeaterRelayInfoMap(24)[scope1751Key].TransportedScopes
	want := []string{"region-direct", "region-shared"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TransportedScopes = %v, want %v (region-via-prefix must be excluded — it only appeared in the ambiguous prefix bucket)", got, want)
	}
}

// TestTransportedScopes_PerNodePrefixBucketExcludedFromScope is the
// per-node-path (GetRepeaterRelayInfo) counterpart to
// TestTransportedScopes_PrefixBucketExcludedFromScope, keeping the single-
// node detail endpoint (Nodes page "Transported scopes" badges) in parity
// with the bulk /api/nodes path.
func TestTransportedScopes_PerNodePrefixBucketExcludedFromScope(t *testing.T) {
	full := scopeTx(1, 2, "region-direct")
	prefixOnly := scopeTx(2, 2, "region-via-prefix")
	shared := scopeTx(3, 2, "region-shared")

	store := &PacketStore{
		byPathHop: map[string][]*StoreTx{
			scope1751Key:     {full, shared},
			scope1751Key[:2]: {prefixOnly, shared},
		},
		mu: sync.RWMutex{},
	}

	got := store.GetRepeaterRelayInfo(scope1751Key, 24).TransportedScopes
	want := []string{"region-direct", "region-shared"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("per-node TransportedScopes = %v, want %v (region-via-prefix must be excluded)", got, want)
	}
}

// TestTransportedScopes_Capped pins the soft cap (#1751 review follow-up):
// more distinct scopes than maxTransportedScopes are bounded to the cap,
// keeping the lexicographically-first names so the output stays deterministic.
func TestTransportedScopes_Capped(t *testing.T) {
	var txs []*StoreTx
	for i := 0; i < maxTransportedScopes+10; i++ {
		txs = append(txs, scopeTx(i+1, 2, fmt.Sprintf("scope-%03d", i)))
	}
	store := &PacketStore{
		byPathHop: map[string][]*StoreTx{scope1751Key: txs},
		mu:        sync.RWMutex{},
	}

	got := store.computeRepeaterRelayInfoMap(24)[scope1751Key].TransportedScopes
	if len(got) != maxTransportedScopes {
		t.Fatalf("expected cap at %d scopes, got %d", maxTransportedScopes, len(got))
	}
	if got[0] != "scope-000" || got[len(got)-1] != fmt.Sprintf("scope-%03d", maxTransportedScopes-1) {
		t.Fatalf("cap should keep lexicographically-first names: first=%q last=%q", got[0], got[len(got)-1])
	}
}
