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
}
