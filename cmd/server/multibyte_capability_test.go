package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// recentTS returns a timestamp string N hours ago, ensuring test data
// stays within the 7-day advert window used by computeNodeHashSizeInfo.
func recentTS(hoursAgo int) string {
	return time.Now().UTC().Add(-time.Duration(hoursAgo) * time.Hour).Format("2006-01-02T15:04:05.000Z")
}

// setupCapabilityTestDB creates a minimal in-memory DB with nodes table.
func setupCapabilityTestDB(t *testing.T) *DB {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	conn.Exec(`CREATE TABLE nodes (
		public_key TEXT PRIMARY KEY, name TEXT, role TEXT,
		lat REAL, lon REAL, last_seen TEXT, first_seen TEXT,
		advert_count INTEGER DEFAULT 0, battery_mv INTEGER, temperature_c REAL
	)`)
	conn.Exec(`CREATE TABLE observers (
		id TEXT PRIMARY KEY, name TEXT, iata TEXT, last_seen TEXT,
		first_seen TEXT, packet_count INTEGER DEFAULT 0, model TEXT,
		firmware TEXT, client_version TEXT, radio TEXT, battery_mv INTEGER,
		uptime_secs INTEGER
	)`)
	return &DB{conn: conn}
}

// addTestPacket adds a StoreTx to the store's internal structures including
// the byPathHop index and byPayloadType index.
func addTestPacket(store *PacketStore, tx *StoreTx) {
	store.mu.Lock()
	defer store.mu.Unlock()
	tx.ID = len(store.packets) + 1
	if tx.Hash == "" {
		tx.Hash = fmt.Sprintf("test-hash-%d", tx.ID)
	}
	store.packets = append(store.packets, tx)
	store.byHash[tx.Hash] = tx
	store.byTxID[tx.ID] = tx
	if tx.PayloadType != nil {
		store.byPayloadType[*tx.PayloadType] = append(store.byPayloadType[*tx.PayloadType], tx)
	}
	addTxToPathHopIndex(store.byPathHop, tx)
}

// buildPathByte returns a 2-char hex string for the path byte with given
// hashSize (1-3) and hopCount.
func buildPathByte(hashSize, hopCount int) string {
	b := byte(((hashSize - 1) & 0x3) << 6) | byte(hopCount&0x3F)
	return fmt.Sprintf("%02x", b)
}

// makeTestAdvert creates a StoreTx representing a flood advert packet.
func makeTestAdvert(pubkey string, hashSize int) *StoreTx {
	decoded, _ := json.Marshal(map[string]interface{}{"pubKey": pubkey, "name": pubkey[:8]})
	pt := 4
	pathByte := buildPathByte(hashSize, 1)
	prefix := strings.ToLower(pubkey[:hashSize*2])
	rawHex := "01" + pathByte + prefix // flood header + path byte + hop prefix
	return &StoreTx{
		RawHex:      rawHex,
		PayloadType: &pt,
		DecodedJSON: string(decoded),
		PathJSON:    `["` + prefix + `"]`,
		FirstSeen:   recentTS(24),
	}
}

// TestMultiByteCapability_Confirmed tests that a repeater advertising
// with hash_size >= 2 is classified as "confirmed".
func TestMultiByteCapability_Confirmed(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepA", "repeater", recentTS(24))

	store := NewPacketStore(db, nil)
	addTestPacket(store, makeTestAdvert("aabbccdd11223344", 2))

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(caps))
	}
	if caps[0].Status != "confirmed" {
		t.Errorf("expected confirmed, got %s", caps[0].Status)
	}
	if caps[0].Evidence != "advert" {
		t.Errorf("expected advert evidence, got %s", caps[0].Evidence)
	}
	if caps[0].MaxHashSize != 2 {
		t.Errorf("expected maxHashSize 2, got %d", caps[0].MaxHashSize)
	}
}

// TestMultiByteCapability_Suspected tests that a repeater whose prefix
// appears in a multi-byte path is classified as "suspected".
func TestMultiByteCapability_Suspected(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepB", "repeater", recentTS(48))

	store := NewPacketStore(db, nil)

	// Non-advert packet with 2-byte hash in path, hop prefix matching node
	pathByte := buildPathByte(2, 1)
	rawHex := "01" + pathByte + "aabb"
	pt := 1
	pkt := &StoreTx{
		RawHex:      rawHex,
		PayloadType: &pt,
		PathJSON:    `["aabb"]`,
		FirstSeen:   recentTS(48),
	}
	addTestPacket(store, pkt)

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(caps))
	}
	if caps[0].Status != "suspected" {
		t.Errorf("expected suspected, got %s", caps[0].Status)
	}
	if caps[0].Evidence != "path" {
		t.Errorf("expected path evidence, got %s", caps[0].Evidence)
	}
	if caps[0].MaxHashSize != 2 {
		t.Errorf("expected maxHashSize 2, got %d", caps[0].MaxHashSize)
	}
}

// TestMultiByteCapability_Unknown tests that a repeater with only 1-byte
// adverts and no multi-byte path appearances is classified as "unknown".
func TestMultiByteCapability_Unknown(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepC", "repeater", recentTS(72))

	store := NewPacketStore(db, nil)

	// Advert with 1-byte hash only
	addTestPacket(store, makeTestAdvert("aabbccdd11223344", 1))

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(caps))
	}
	if caps[0].Status != "unknown" {
		t.Errorf("expected unknown, got %s", caps[0].Status)
	}
	if caps[0].MaxHashSize != 1 {
		t.Errorf("expected maxHashSize 1, got %d", caps[0].MaxHashSize)
	}
}

// TestMultiByteCapability_SuspectedFromPath tests that a repeater whose
// 2-byte prefix appears as a hop in a hs=2 packet is classified as "suspected"
// when it has no confirming advert, while another node confirmed via advert
// remains "confirmed" even though the two share no prefix overlap.
func TestMultiByteCapability_SuspectedFromPath(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	// Two repeaters sharing 2-byte prefix "aacc"
	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabb000000000001", "RepConfirmed", "repeater", recentTS(24))
	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aacc000000000002", "RepOther", "repeater", recentTS(24))

	store := NewPacketStore(db, nil)

	// RepConfirmed has a 2-byte advert
	addTestPacket(store, makeTestAdvert("aabb000000000001", 2))

	// A packet with hs=2 path containing 2-byte hop "aacc" — matches RepOther's
	// 2-byte prefix. Hop length (2 bytes) correctly matches hash_size=2.
	pathByte := buildPathByte(2, 1)
	rawHex := "01" + pathByte + "aacc"
	pt := 1
	pkt := &StoreTx{
		RawHex:      rawHex,
		PayloadType: &pt,
		PathJSON:    `["aacc"]`,
		FirstSeen:   recentTS(48),
	}
	addTestPacket(store, pkt)

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(caps))
	}

	capByName := map[string]MultiByteCapEntry{}
	for _, c := range caps {
		capByName[c.Name] = c
	}

	if capByName["RepConfirmed"].Status != "confirmed" {
		t.Errorf("RepConfirmed expected confirmed, got %s", capByName["RepConfirmed"].Status)
	}
	if capByName["RepOther"].Status != "suspected" {
		t.Errorf("RepOther expected suspected, got %s", capByName["RepOther"].Status)
	}
}

// TestMultiByteCapability_TraceExcluded tests that TRACE packets (payload_type 8)
// do NOT contribute to "suspected" multi-byte capability. TRACE packets carry
// hash size in their own flags, so pre-1.14 repeaters can forward multi-byte
// TRACEs without actually supporting multi-byte hashes. See #714.
func TestMultiByteCapability_TraceExcluded(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepTrace", "repeater", recentTS(48))

	store := NewPacketStore(db, nil)

	// TRACE packet (payload_type 8) with 2-byte hash in path
	pathByte := buildPathByte(2, 1)
	rawHex := "01" + pathByte + "aabb"
	pt := 8
	pkt := &StoreTx{
		RawHex:      rawHex,
		PayloadType: &pt,
		PathJSON:    `["aabb"]`,
		FirstSeen:   recentTS(48),
	}
	addTestPacket(store, pkt)

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(caps))
	}
	if caps[0].Status != "unknown" {
		t.Errorf("expected unknown (TRACE excluded), got %s", caps[0].Status)
	}
}

// TestMultiByteCapability_NonTraceStillSuspected verifies that non-TRACE packets
// with 2-byte paths still correctly mark a repeater as "suspected".
func TestMultiByteCapability_NonTraceStillSuspected(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepNonTrace", "repeater", recentTS(48))

	store := NewPacketStore(db, nil)

	// GRP_TXT packet (payload_type 1) with 2-byte hash in path
	pathByte := buildPathByte(2, 1)
	rawHex := "01" + pathByte + "aabb"
	pt := 1
	pkt := &StoreTx{
		RawHex:      rawHex,
		PayloadType: &pt,
		PathJSON:    `["aabb"]`,
		FirstSeen:   recentTS(48),
	}
	addTestPacket(store, pkt)

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(caps))
	}
	if caps[0].Status != "suspected" {
		t.Errorf("expected suspected, got %s", caps[0].Status)
	}
}

// TestMultiByteCapability_ConfirmedUnaffectedByTraceExclusion verifies that
// "confirmed" status from adverts is not affected by the TRACE exclusion.
func TestMultiByteCapability_ConfirmedUnaffectedByTraceExclusion(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepConfirmedTrace", "repeater", recentTS(24))

	store := NewPacketStore(db, nil)

	// Advert with 2-byte hash (confirms capability)
	addTestPacket(store, makeTestAdvert("aabbccdd11223344", 2))

	// TRACE packet also present — should not downgrade confirmed status
	pathByte := buildPathByte(2, 1)
	rawHex := "01" + pathByte + "aabb"
	pt := 8
	pkt := &StoreTx{
		RawHex:      rawHex,
		PayloadType: &pt,
		PathJSON:    `["aabb"]`,
		FirstSeen:   recentTS(48),
	}
	addTestPacket(store, pkt)

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(caps))
	}
	if caps[0].Status != "confirmed" {
		t.Errorf("expected confirmed (unaffected by TRACE), got %s", caps[0].Status)
	}
}

// TestMultiByteCapability_CompanionConfirmed tests that a companion with
// multi-byte advert is classified as "confirmed", not "unknown" (Bug 1, #754).
func TestMultiByteCapability_CompanionConfirmed(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "CompA", "companion", recentTS(24))

	store := NewPacketStore(db, nil)
	addTestPacket(store, makeTestAdvert("aabbccdd11223344", 2))

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(caps))
	}
	if caps[0].Status != "confirmed" {
		t.Errorf("expected confirmed for companion, got %s", caps[0].Status)
	}
	if caps[0].Role != "companion" {
		t.Errorf("expected role companion, got %s", caps[0].Role)
	}
	if caps[0].Evidence != "advert" {
		t.Errorf("expected advert evidence, got %s", caps[0].Evidence)
	}
}

// TestMultiByteCapability_RoleColumnPopulated tests that the Role field is
// populated for all node types (Bug 2, #754).
func TestMultiByteCapability_RoleColumnPopulated(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabb000000000001", "Rep1", "repeater", recentTS(24))
	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"ccdd000000000002", "Comp1", "companion", recentTS(24))
	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"eeff000000000003", "Room1", "room_server", recentTS(24))

	store := NewPacketStore(db, nil)
	addTestPacket(store, makeTestAdvert("aabb000000000001", 2))
	addTestPacket(store, makeTestAdvert("ccdd000000000002", 2))
	addTestPacket(store, makeTestAdvert("eeff000000000003", 1))

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(caps))
	}

	roleByName := map[string]string{}
	for _, c := range caps {
		roleByName[c.Name] = c.Role
	}
	if roleByName["Rep1"] != "repeater" {
		t.Errorf("Rep1 role: expected repeater, got %s", roleByName["Rep1"])
	}
	if roleByName["Comp1"] != "companion" {
		t.Errorf("Comp1 role: expected companion, got %s", roleByName["Comp1"])
	}
	if roleByName["Room1"] != "room_server" {
		t.Errorf("Room1 role: expected room_server, got %s", roleByName["Room1"])
	}
}

// TestGetMultibyteCapMap_PopulatedByAnalyticsCycle verifies that calling
// GetAnalyticsHashSizes populates mbCapSnapshot and that GetMultibyteCapMap
// then returns the expected entries — exercising the full analytics → publish path.
func TestGetMultibyteCapMap_PopulatedByAnalyticsCycle(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()

	store := NewPacketStore(db, nil)
	store.Load()

	// GetAnalyticsHashSizes triggers computeMultiByteCapability and publishes mbCapSnapshot.
	store.GetAnalyticsHashSizes("")

	m := store.GetMultibyteCapMap()
	// Rich test DB has a repeater (aabbccdd11223344) with hash_size=2 adverts,
	// so it must appear as "confirmed" after the analytics cycle.
	e, ok := m["aabbccdd11223344"]
	if !ok {
		t.Fatal("expected aabbccdd11223344 in snapshot after analytics cycle")
	}
	if e.Status != "confirmed" {
		t.Errorf("status = %q, want confirmed", e.Status)
	}
}

func TestEnrichNodeWithMultibyte_Confirmed(t *testing.T) {
	node := map[string]interface{}{"public_key": "aabb", "multibyte_sup": 0}
	enrichNodeWithMultibyte(node, MultiByteCapEntry{Status: "confirmed", Evidence: "advert"})
	if node["multibyte_sup"] != 2 {
		t.Errorf("multibyte_sup = %v, want 2", node["multibyte_sup"])
	}
	if node["multibyte_evidence"] != "advert" {
		t.Errorf("multibyte_evidence = %v, want advert", node["multibyte_evidence"])
	}
}

func TestEnrichNodeWithMultibyte_Suspected(t *testing.T) {
	node := map[string]interface{}{"public_key": "aabb", "multibyte_sup": 0}
	enrichNodeWithMultibyte(node, MultiByteCapEntry{Status: "suspected", Evidence: "path"})
	if node["multibyte_sup"] != 1 {
		t.Errorf("multibyte_sup = %v, want 1", node["multibyte_sup"])
	}
}

func TestEnrichNodeWithMultibyte_ZeroEntryNoChange(t *testing.T) {
	// Contract: a zero-status entry (Status=="") must be a no-op so that confirmed
	// values written by the DB layer are not clobbered when the in-memory snapshot
	// is missing the pubkey (e.g. immediately after cold start before the first cycle).
	node := map[string]interface{}{"public_key": "aabb", "multibyte_sup": 0}
	enrichNodeWithMultibyte(node, MultiByteCapEntry{}) // zero-value = unknown, no pubkey
	if node["multibyte_sup"] != 0 {
		t.Errorf("multibyte_sup = %v, want 0 (unchanged for unknown)", node["multibyte_sup"])
	}
	if _, ok := node["multibyte_evidence"]; ok {
		t.Error("multibyte_evidence should not be set for unknown entry")
	}
}

// TestMultiByteCapability_HopLengthMismatch tests that a 1-byte hop stored
// in a hs=2 packet (pre-#886 ingestor data) does NOT trigger suspected.
// The guard at store.go (len(pfx)/2 != hs) rejects hops whose byte length
// disagrees with the path_byte hash_size — without it this test would produce "suspected".
func TestMultiByteCapability_HopLengthMismatch(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"daabccdd11223344", "LegacyNode", "repeater", recentTS(24))

	store := NewPacketStore(db, nil)

	// Malformed packet: path_json has 1-byte hops but path_byte in raw_hex
	// encodes hash_size=2 (pre-#886 ingestor stored path bytes individually).
	// buildPathByte(2,1) gives a path byte with hs=2, hop_count=1.
	pathByte := buildPathByte(2, 1)
	// path_json has 1-byte hop "da" — matches 1-byte prefix of node "daab..."
	// raw_hex says hash_size=2.
	rawHex := "01" + pathByte + "da"
	pt := 1
	pkt := &StoreTx{
		RawHex:      rawHex,
		PayloadType: &pt,
		PathJSON:    `["da"]`,
		FirstSeen:   recentTS(48),
	}
	addTestPacket(store, pkt)

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(caps))
	}
	if caps[0].Status != "unknown" {
		t.Errorf("expected unknown (hop length mismatch should be filtered), got %s", caps[0].Status)
	}
}

// TestMultiByteCapability_AdopterEvidenceTakesPrecedence tests that when
// adopter data shows hashSize >= 2 but path evidence says "suspected",
// the node is upgraded to "confirmed" (Bug 3, #754).
func TestMultiByteCapability_AdopterEvidenceTakesPrecedence(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepAdopter", "repeater", recentTS(24))

	store := NewPacketStore(db, nil)

	// Only a path-based packet (no advert) — would normally be "suspected"
	pathByte := buildPathByte(2, 1)
	rawHex := "01" + pathByte + "aabb"
	pt := 1
	pkt := &StoreTx{
		RawHex:      rawHex,
		PayloadType: &pt,
		PathJSON:    `["aabb"]`,
		FirstSeen:   recentTS(48),
	}
	addTestPacket(store, pkt)

	// Without adopter data: should be suspected
	caps := store.computeMultiByteCapability(nil)
	capByName := map[string]MultiByteCapEntry{}
	for _, c := range caps {
		capByName[c.Name] = c
	}
	if capByName["RepAdopter"].Status != "suspected" {
		t.Errorf("without adopter data: expected suspected, got %s", capByName["RepAdopter"].Status)
	}

	// With adopter data showing hashSize 2: should be confirmed
	adopterHS := map[string]int{"aabbccdd11223344": 2}
	caps = store.computeMultiByteCapability(adopterHS)
	capByName = map[string]MultiByteCapEntry{}
	for _, c := range caps {
		capByName[c.Name] = c
	}
	if capByName["RepAdopter"].Status != "confirmed" {
		t.Errorf("with adopter data: expected confirmed, got %s", capByName["RepAdopter"].Status)
	}
	if capByName["RepAdopter"].Evidence != "advert" {
		t.Errorf("with adopter data: expected advert evidence, got %s", capByName["RepAdopter"].Evidence)
	}
}


// TestGetMultibyteCapFor_O1Lookup verifies O(1) index lookup for GetMultibyteCapFor.
func TestGetMultibyteCapFor_O1Lookup(t *testing.T) {
	s := &PacketStore{}
	entries := []MultiByteCapEntry{
		{PublicKey: "pk1", Status: "confirmed", Evidence: "advert"},
		{PublicKey: "pk2", Status: "suspected"},
	}
	s.cacheMu.Lock()
	s.mbCapSnapshot = entries
	idx := make(map[string]MultiByteCapEntry, len(entries))
	for _, e := range entries {
		idx[e.PublicKey] = e
	}
	s.mbCapIndex = idx
	s.cacheMu.Unlock()

	e1, ok1 := s.GetMultibyteCapFor("pk1")
	if !ok1 || e1.Status != "confirmed" {
		t.Errorf("pk1: want confirmed, got %v ok=%v", e1.Status, ok1)
	}
	_, ok2 := s.GetMultibyteCapFor("missing")
	if ok2 {
		t.Error("missing key should return ok=false")
	}
}

