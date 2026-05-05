package main

import (
	"testing"
	"time"
)

// TestRepeaterRelayActivity_Active verifies that a repeater whose pubkey
// appears as a relay hop in a recent (non-advert) packet is reported with
// a non-zero lastRelayed timestamp and relayActive=true.
func TestRepeaterRelayActivity_Active(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	pubkey := "aabbccdd11223344"
	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		pubkey, "RepActive", "repeater", recentTS(1))

	store := NewPacketStore(db, nil)

	// A non-advert packet (payload_type=1, TXT_MSG) with the repeater pubkey
	// indexed as a path hop. Index by lowercase pubkey directly to mirror
	// the resolved-path entries that decode-window writes.
	pt := 1
	relayed := &StoreTx{
		RawHex:      "0100",
		PayloadType: &pt,
		PathJSON:    `["aa"]`,
		FirstSeen:   recentTS(2),
	}
	store.mu.Lock()
	relayed.ID = len(store.packets) + 1
	relayed.Hash = "test-relay-1"
	store.packets = append(store.packets, relayed)
	store.byHash[relayed.Hash] = relayed
	store.byTxID[relayed.ID] = relayed
	store.byPathHop[pubkey] = append(store.byPathHop[pubkey], relayed)
	store.mu.Unlock()

	info := store.GetRepeaterRelayInfo(pubkey, 24)
	if info.LastRelayed == "" {
		t.Fatalf("expected non-empty LastRelayed for active relayer, got empty (RelayActive=%v)", info.RelayActive)
	}
	if !info.RelayActive {
		t.Errorf("expected RelayActive=true within 24h window, got false (LastRelayed=%s)", info.LastRelayed)
	}
	if info.RelayCount1h != 0 {
		t.Errorf("expected RelayCount1h=0 (relay was 2h ago, outside 1h window), got %d", info.RelayCount1h)
	}
	if info.RelayCount24h != 1 {
		t.Errorf("expected RelayCount24h=1 (relay was 2h ago, inside 24h window), got %d", info.RelayCount24h)
	}
}

// TestRepeaterRelayActivity_Idle verifies that a repeater whose pubkey
// has not appeared as a relay hop reports an empty LastRelayed and
// relayActive=false.
func TestRepeaterRelayActivity_Idle(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	pubkey := "ccddeeff55667788"
	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		pubkey, "RepIdle", "repeater", recentTS(1))

	store := NewPacketStore(db, nil)

	info := store.GetRepeaterRelayInfo(pubkey, 24)
	if info.LastRelayed != "" {
		t.Errorf("expected empty LastRelayed for idle repeater, got %q", info.LastRelayed)
	}
	if info.RelayActive {
		t.Errorf("expected RelayActive=false for idle repeater, got true")
	}
	if info.RelayCount1h != 0 || info.RelayCount24h != 0 {
		t.Errorf("expected zero relay counts for idle repeater, got 1h=%d 24h=%d", info.RelayCount1h, info.RelayCount24h)
	}
}

// TestRepeaterRelayActivity_Stale verifies that a repeater whose only
// relay-hop appearances are older than the configured window reports
// a non-empty LastRelayed but relayActive=false.
func TestRepeaterRelayActivity_Stale(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	pubkey := "1122334455667788"
	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		pubkey, "RepStale", "repeater", recentTS(1))

	store := NewPacketStore(db, nil)

	pt := 1
	staleTS := time.Now().UTC().Add(-48 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	old := &StoreTx{
		RawHex:      "0100",
		PayloadType: &pt,
		PathJSON:    `["11"]`,
		FirstSeen:   staleTS,
	}
	store.mu.Lock()
	old.ID = len(store.packets) + 1
	old.Hash = "test-relay-stale"
	store.packets = append(store.packets, old)
	store.byHash[old.Hash] = old
	store.byTxID[old.ID] = old
	store.byPathHop[pubkey] = append(store.byPathHop[pubkey], old)
	store.mu.Unlock()

	info := store.GetRepeaterRelayInfo(pubkey, 24)
	if info.LastRelayed != staleTS {
		t.Errorf("expected LastRelayed=%q (stale ts), got %q", staleTS, info.LastRelayed)
	}
	if info.RelayActive {
		t.Errorf("expected RelayActive=false for relay older than window, got true")
	}
	if info.RelayCount1h != 0 || info.RelayCount24h != 0 {
		t.Errorf("expected zero relay counts for stale (>24h) repeater, got 1h=%d 24h=%d", info.RelayCount1h, info.RelayCount24h)
	}
}

// TestRepeaterRelayActivity_IgnoresAdverts verifies that adverts originated
// by the repeater itself (payload_type=4) are NOT counted as relay activity —
// adverts demonstrate liveness, not relaying.
func TestRepeaterRelayActivity_IgnoresAdverts(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	pubkey := "deadbeef00000001"
	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		pubkey, "RepAdvertOnly", "repeater", recentTS(1))

	store := NewPacketStore(db, nil)

	// Self-advert with the repeater as its own first hop. Should NOT count.
	pt := 4
	adv := &StoreTx{
		RawHex:      "0140de",
		PayloadType: &pt,
		PathJSON:    `["de"]`,
		FirstSeen:   recentTS(2),
	}
	store.mu.Lock()
	adv.ID = len(store.packets) + 1
	adv.Hash = "test-advert-1"
	store.packets = append(store.packets, adv)
	store.byHash[adv.Hash] = adv
	store.byTxID[adv.ID] = adv
	store.byPathHop[pubkey] = append(store.byPathHop[pubkey], adv)
	store.mu.Unlock()

	info := store.GetRepeaterRelayInfo(pubkey, 24)
	if info.LastRelayed != "" {
		t.Errorf("expected empty LastRelayed (adverts ignored), got %q", info.LastRelayed)
	}
	if info.RelayActive {
		t.Errorf("expected RelayActive=false (adverts ignored), got true")
	}
	if info.RelayCount1h != 0 || info.RelayCount24h != 0 {
		t.Errorf("expected zero relay counts (adverts ignored), got 1h=%d 24h=%d", info.RelayCount1h, info.RelayCount24h)
	}
}

// TestRepeaterRelayActivity_PrefixHop verifies that GetRepeaterRelayInfo
// counts a non-advert packet whose path contains only the 1-byte raw hop
// prefix matching the target node (not the full resolved pubkey).
//
// Reality on prod/staging: many ingested packets only carry raw 1-byte
// path hops (e.g. ["a3"] from the wire) — resolution to a full pubkey
// happens later via neighbor affinity for the "Paths seen through node"
// view. The byPathHop index is populated under BOTH keys (raw hop AND
// resolved pubkey), but GetRepeaterRelayInfo only looks up the full
// pubkey, missing all raw-hop-only entries. This is the cause of the
// "never observed as relay hop" claim on nodes that clearly have paths
// shown through them. See https://analyzer-stg.00id.net/#/nodes/<pk>.
func TestRepeaterRelayActivity_PrefixHop(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	pubkey := "a36a21290d9c25a158130fe7c489541210d5f09f25fab997db5e942fb7680510"
	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		pubkey, "RepPrefix", "repeater", recentTS(1))

	store := NewPacketStore(db, nil)

	// Non-advert packet with a single raw 1-byte hop matching the target
	// pubkey's first byte ("a3"). Index it the way addTxToPathHopIndex
	// does — under the raw hop key only, not the full pubkey.
	pt := 1
	tx := &StoreTx{
		RawHex:      "0100",
		PayloadType: &pt,
		PathJSON:    `["a3"]`,
		FirstSeen:   recentTS(2),
	}
	store.mu.Lock()
	tx.ID = len(store.packets) + 1
	tx.Hash = "test-relay-prefix-1"
	store.packets = append(store.packets, tx)
	store.byHash[tx.Hash] = tx
	store.byTxID[tx.ID] = tx
	addTxToPathHopIndex(store.byPathHop, tx)
	store.mu.Unlock()

	info := store.GetRepeaterRelayInfo(pubkey, 24)
	if info.RelayCount24h < 1 {
		t.Fatalf("expected RelayCount24h>=1 for node with prefix-matched hop in path, got %d (LastRelayed=%q)",
			info.RelayCount24h, info.LastRelayed)
	}
	if info.LastRelayed == "" {
		t.Errorf("expected non-empty LastRelayed when prefix hop matched, got empty")
	}
	if !info.RelayActive {
		t.Errorf("expected RelayActive=true within 24h window, got false (LastRelayed=%s)", info.LastRelayed)
	}
}

// TestRepeaterRelayActivity_DedupAcrossPrefixAndFullKey verifies that when
// the SAME packet is indexed in byPathHop under BOTH the full pubkey AND
// the raw 1-byte prefix, GetRepeaterRelayInfo counts it exactly once. This
// gates the `seen[tx.ID]` dedup map: without it, hop counts would double
// for any tx that resolved-path indexing recorded under both keys.
func TestRepeaterRelayActivity_DedupAcrossPrefixAndFullKey(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	pubkey := "a36a21290d9c25a158130fe7c489541210d5f09f25fab997db5e942fb7680510"
	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		pubkey, "RepDedup", "repeater", recentTS(1))

	store := NewPacketStore(db, nil)

	pt := 1
	tx := &StoreTx{
		RawHex:      "0100",
		PayloadType: &pt,
		PathJSON:    `["a3"]`,
		FirstSeen:   recentTS(2),
	}
	store.mu.Lock()
	tx.ID = len(store.packets) + 1
	tx.Hash = "test-relay-dedup-1"
	store.packets = append(store.packets, tx)
	store.byHash[tx.Hash] = tx
	store.byTxID[tx.ID] = tx
	// Index under BOTH the full pubkey AND the raw 1-byte prefix — this
	// is the exact double-index case that occurs when wire ingest records
	// the raw hop and a later resolution pass also records the full key.
	store.byPathHop[pubkey] = append(store.byPathHop[pubkey], tx)
	store.byPathHop[pubkey[:2]] = append(store.byPathHop[pubkey[:2]], tx)
	store.mu.Unlock()

	info := store.GetRepeaterRelayInfo(pubkey, 24)
	if info.RelayCount24h != 1 {
		t.Fatalf("expected RelayCount24h=1 (dedup across full+prefix indexing), got %d", info.RelayCount24h)
	}
	if info.RelayCount1h != 0 {
		t.Errorf("expected RelayCount1h=0 (relay was 2h ago, outside 1h window), got %d", info.RelayCount1h)
	}
	if !info.RelayActive {
		t.Errorf("expected RelayActive=true, got false (LastRelayed=%s)", info.LastRelayed)
	}
}
