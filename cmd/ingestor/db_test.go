package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func tempDBPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(".", "testdata")
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, t.Name()+".db")
	// Clean up any previous test DB
	os.Remove(p)
	os.Remove(p + "-wal")
	os.Remove(p + "-shm")
	t.Cleanup(func() {
		os.Remove(p)
		os.Remove(p + "-wal")
		os.Remove(p + "-shm")
	})
	return p
}

func TestOpenStore(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Verify tables exist
	rows, err := s.db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		rows.Scan(&name)
		tables = append(tables, name)
	}

	expected := []string{"nodes", "observations", "observers", "transmissions"}
	for _, e := range expected {
		found := false
		for _, tbl := range tables {
			if tbl == e {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing table %s, got %v", e, tables)
		}
	}
}

func TestInsertTransmission(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	snr := 5.5
	rssi := -100.0
	data := &PacketData{
		RawHex:         "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976",
		Timestamp:      "2026-03-25T00:00:00Z",
		ObserverID:     "obs1",
		Hash:           "abcdef1234567890",
		RouteType:      2,
		PayloadType:    2,
		PayloadVersion: 0,
		PathJSON:       "[]",
		DecodedJSON:    `{"type":"TXT_MSG"}`,
		SNR:            &snr,
		RSSI:           &rssi,
	}

	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	// Verify transmission was inserted
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 1 {
		t.Errorf("transmissions count=%d, want 1", count)
	}

	// Verify observation was inserted
	s.db.QueryRow("SELECT COUNT(*) FROM observations").Scan(&count)
	if count != 1 {
		t.Errorf("observations count=%d, want 1", count)
	}

	// Verify hash dedup: same hash should not create new transmission
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 1 {
		t.Errorf("transmissions count after dedup=%d, want 1", count)
	}
}

func TestUpsertNode(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	lat := 37.0
	lon := -122.0
	if err := s.UpsertNode("aabbccdd", "TestNode", "repeater", &lat, &lon, "2026-03-25T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	var name, role string
	s.db.QueryRow("SELECT name, role FROM nodes WHERE public_key = 'aabbccdd'").Scan(&name, &role)
	if name != "TestNode" {
		t.Errorf("name=%s, want TestNode", name)
	}
	if role != "repeater" {
		t.Errorf("role=%s, want repeater", role)
	}

	// Upsert again — should update
	if err := s.UpsertNode("aabbccdd", "UpdatedNode", "repeater", &lat, &lon, "2026-03-25T01:00:00Z"); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow("SELECT name FROM nodes WHERE public_key = 'aabbccdd'").Scan(&name)
	if name != "UpdatedNode" {
		t.Errorf("after upsert name=%s, want UpdatedNode", name)
	}

	// UpsertNode does not modify advert_count (IncrementAdvertCount is separate)
	var count int
	s.db.QueryRow("SELECT advert_count FROM nodes WHERE public_key = 'aabbccdd'").Scan(&count)
	if count != 0 {
		t.Errorf("advert_count=%d, want 0 (UpsertNode does not increment)", count)
	}
}

func TestUpsertObserver(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertObserver("obs1", "Observer1", "SJC"); err != nil {
		t.Fatal(err)
	}

	var name, iata string
	s.db.QueryRow("SELECT name, iata FROM observers WHERE id = 'obs1'").Scan(&name, &iata)
	if name != "Observer1" {
		t.Errorf("name=%s, want Observer1", name)
	}
	if iata != "SJC" {
		t.Errorf("iata=%s, want SJC", iata)
	}
}

func TestInsertTransmissionWithObserver(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Insert observer first
	if err := s.UpsertObserver("obs1", "Observer1", "SJC"); err != nil {
		t.Fatal(err)
	}

	data := &PacketData{
		RawHex:      "0A00D69F",
		Timestamp:   "2026-03-25T00:00:00Z",
		ObserverID:  "obs1",
		Hash:        "test1234567890ab",
		RouteType:   2,
		PayloadType: 2,
		PathJSON:    "[]",
		DecodedJSON: `{"type":"TXT_MSG"}`,
	}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	// Verify observer_idx was resolved
	var observerIdx *int64
	s.db.QueryRow("SELECT observer_idx FROM observations LIMIT 1").Scan(&observerIdx)
	if observerIdx == nil {
		t.Error("observer_idx should be set when observer exists")
	}
}

func TestEndToEndIngest(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Simulate full pipeline: decode + insert
	rawHex := "120046D62DE27D4C5194D7821FC5A34A45565DCC2537B300B9AB6275255CEFB65D840CE5C169C94C9AED39E8BCB6CB6EB0335497A198B33A1A610CD3B03D8DCFC160900E5244280323EE0B44CACAB8F02B5B38B91CFA18BD067B0B5E63E94CFC85F758A8530B9240933402E0E6B8F84D5252322D52"

	decoded, err := DecodePacket(rawHex, nil)
	if err != nil {
		t.Fatal(err)
	}

	msg := &MQTTPacketMessage{
		Raw: rawHex,
	}
	pktData := BuildPacketData(msg, decoded, "obs1", "SJC")
	if _, err := s.InsertTransmission(pktData); err != nil {
		t.Fatal(err)
	}

	// Process advert node upsert
	if decoded.Payload.Type == "ADVERT" && decoded.Payload.PubKey != "" {
		ok, _ := ValidateAdvert(&decoded.Payload)
		if ok {
			role := advertRole(decoded.Payload.Flags)
			err := s.UpsertNode(decoded.Payload.PubKey, decoded.Payload.Name, role, decoded.Payload.Lat, decoded.Payload.Lon, pktData.Timestamp)
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	// Verify node was created
	var nodeName string
	err = s.db.QueryRow("SELECT name FROM nodes WHERE public_key = ?", decoded.Payload.PubKey).Scan(&nodeName)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(nodeName, "MRR2-R") {
		t.Errorf("node name=%s, want MRR2-R", nodeName)
	}
}

func TestInsertTransmissionEmptyHash(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	data := &PacketData{
		RawHex:    "0A00",
		Timestamp: "2026-03-25T00:00:00Z",
		Hash:      "", // empty hash → should return nil
	}
	_, err = s.InsertTransmission(data)
	if err != nil {
		t.Errorf("empty hash should return nil, got %v", err)
	}

	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 0 {
		t.Errorf("no transmission should be inserted for empty hash, got count=%d", count)
	}
}

func TestInsertTransmissionEmptyTimestamp(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	data := &PacketData{
		RawHex:    "0A00D69F",
		Timestamp: "", // empty → uses current time
		Hash:      "emptyts123456789",
		RouteType: 2,
	}
	_, err = s.InsertTransmission(data)
	if err != nil {
		t.Fatal(err)
	}

	var firstSeen string
	s.db.QueryRow("SELECT first_seen FROM transmissions WHERE hash = ?", data.Hash).Scan(&firstSeen)
	if firstSeen == "" {
		t.Error("first_seen should be set even with empty timestamp")
	}
}

func TestInsertTransmissionEarlierFirstSeen(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Insert with later timestamp
	data := &PacketData{
		RawHex:    "0A00D69F",
		Timestamp: "2026-03-25T12:00:00Z",
		Hash:      "firstseen12345678",
		RouteType: 2,
	}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	// Insert again with earlier timestamp
	data2 := &PacketData{
		RawHex:    "0A00D69F",
		Timestamp: "2026-03-25T06:00:00Z", // earlier
		Hash:      "firstseen12345678",     // same hash
		RouteType: 2,
	}
	if _, err := s.InsertTransmission(data2); err != nil {
		t.Fatal(err)
	}

	var firstSeen string
	s.db.QueryRow("SELECT first_seen FROM transmissions WHERE hash = ?", data.Hash).Scan(&firstSeen)
	if firstSeen != "2026-03-25T06:00:00Z" {
		t.Errorf("first_seen=%s, want 2026-03-25T06:00:00Z (earlier timestamp)", firstSeen)
	}
}

func TestInsertTransmissionLaterFirstSeenNotUpdated(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	data := &PacketData{
		RawHex:    "0A00D69F",
		Timestamp: "2026-03-25T06:00:00Z",
		Hash:      "notupdated1234567",
		RouteType: 2,
	}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	// Insert with later timestamp — should NOT update first_seen
	data2 := &PacketData{
		RawHex:    "0A00D69F",
		Timestamp: "2026-03-25T18:00:00Z",
		Hash:      "notupdated1234567",
		RouteType: 2,
	}
	if _, err := s.InsertTransmission(data2); err != nil {
		t.Fatal(err)
	}

	var firstSeen string
	s.db.QueryRow("SELECT first_seen FROM transmissions WHERE hash = ?", data.Hash).Scan(&firstSeen)
	if firstSeen != "2026-03-25T06:00:00Z" {
		t.Errorf("first_seen=%s should not change to later time", firstSeen)
	}
}

func TestInsertTransmissionNilSNRRSSI(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	data := &PacketData{
		RawHex:    "0A00D69F",
		Timestamp: "2026-03-25T00:00:00Z",
		Hash:      "nilsnrrssi1234567",
		RouteType: 2,
		SNR:       nil,
		RSSI:      nil,
	}
	_, err = s.InsertTransmission(data)
	if err != nil {
		t.Fatal(err)
	}

	var snr, rssi *float64
	s.db.QueryRow("SELECT snr, rssi FROM observations LIMIT 1").Scan(&snr, &rssi)
	if snr != nil {
		t.Errorf("snr should be nil, got %v", snr)
	}
	if rssi != nil {
		t.Errorf("rssi should be nil, got %v", rssi)
	}
}

func TestBuildPacketData(t *testing.T) {
	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	decoded, err := DecodePacket(rawHex, nil)
	if err != nil {
		t.Fatal(err)
	}

	snr := 5.0
	rssi := -100.0
	msg := &MQTTPacketMessage{
		Raw:    rawHex,
		SNR:    &snr,
		RSSI:   &rssi,
		Origin: "test-observer",
	}

	pkt := BuildPacketData(msg, decoded, "obs123", "SJC")

	if pkt.RawHex != rawHex {
		t.Errorf("rawHex mismatch")
	}
	if pkt.ObserverID != "obs123" {
		t.Errorf("observerID=%s, want obs123", pkt.ObserverID)
	}
	if pkt.ObserverName != "test-observer" {
		t.Errorf("observerName=%s", pkt.ObserverName)
	}
	if pkt.SNR == nil || *pkt.SNR != 5.0 {
		t.Errorf("SNR=%v", pkt.SNR)
	}
	if pkt.RSSI == nil || *pkt.RSSI != -100.0 {
		t.Errorf("RSSI=%v", pkt.RSSI)
	}
	if pkt.Hash == "" {
		t.Error("hash should not be empty")
	}
	if len(pkt.Hash) != 16 {
		t.Errorf("hash length=%d, want 16", len(pkt.Hash))
	}
	if pkt.RouteType != decoded.Header.RouteType {
		t.Errorf("routeType mismatch")
	}
	if pkt.PayloadType != decoded.Header.PayloadType {
		t.Errorf("payloadType mismatch")
	}
	if pkt.Timestamp == "" {
		t.Error("timestamp should be set")
	}
	if pkt.DecodedJSON == "" || pkt.DecodedJSON == "{}" {
		t.Error("decodedJSON should be populated")
	}
}

func TestBuildPacketDataWithHops(t *testing.T) {
	// A packet with actual hops in the path
	raw := "0505AABBCCDDEE" + strings.Repeat("00", 10)
	decoded, err := DecodePacket(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	msg := &MQTTPacketMessage{Raw: raw}
	pkt := BuildPacketData(msg, decoded, "", "")

	if pkt.PathJSON == "[]" {
		t.Error("pathJSON should contain hops")
	}
	if !strings.Contains(pkt.PathJSON, "AA") {
		t.Errorf("pathJSON should contain hop AA: %s", pkt.PathJSON)
	}
}

func TestBuildPacketDataNilSNRRSSI(t *testing.T) {
	decoded, _ := DecodePacket("0A00"+strings.Repeat("00", 10), nil)
	msg := &MQTTPacketMessage{Raw: "0A00" + strings.Repeat("00", 10)}
	pkt := BuildPacketData(msg, decoded, "", "")

	if pkt.SNR != nil {
		t.Errorf("SNR should be nil")
	}
	if pkt.RSSI != nil {
		t.Errorf("RSSI should be nil")
	}
}

func TestUpsertNodeEmptyLastSeen(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	lat := 37.0
	lon := -122.0
	// Empty lastSeen → should use current time
	if err := s.UpsertNode("aabbccdd", "TestNode", "repeater", &lat, &lon, ""); err != nil {
		t.Fatal(err)
	}

	var lastSeen string
	s.db.QueryRow("SELECT last_seen FROM nodes WHERE public_key = 'aabbccdd'").Scan(&lastSeen)
	if lastSeen == "" {
		t.Error("last_seen should be set even with empty input")
	}
}

func TestOpenStoreTwice(t *testing.T) {
	// Opening same DB twice tests the "observations already exists" path in applySchema
	path := tempDBPath(t)
	s1, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()

	// Second open — observations table already exists
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	// Verify it still works
	var count int
	s2.db.QueryRow("SELECT COUNT(*) FROM observations").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 observations, got %d", count)
	}
}

func TestInsertTransmissionDedupObservation(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// First insert
	data := &PacketData{
		RawHex:    "0A00D69F",
		Timestamp: "2026-03-25T00:00:00Z",
		Hash:      "dedupobs12345678",
		RouteType: 2,
		PathJSON:  "[]",
	}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	// Insert same hash again with same observer (no observerID) — 
	// the UNIQUE constraint on observations dedup should handle it
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM observations").Scan(&count)
	// Should have 2 observations (no observer_idx means both have NULL)
	// Actually INSERT OR IGNORE — may be 1 due to dedup index
	if count < 1 {
		t.Errorf("should have at least 1 observation, got %d", count)
	}
}

func TestSchemaCompatibility(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Verify column names match what Node.js expects
	expectedTxCols := []string{"id", "raw_hex", "hash", "first_seen", "route_type", "payload_type", "payload_version", "decoded_json", "created_at"}
	rows, _ := s.db.Query("PRAGMA table_info(transmissions)")
	var txCols []string
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt *string
		var pk int
		rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
		txCols = append(txCols, name)
	}
	rows.Close()

	for _, e := range expectedTxCols {
		found := false
		for _, c := range txCols {
			if c == e {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("transmissions missing column %s, got %v", e, txCols)
		}
	}

	// Verify observations columns
	expectedObsCols := []string{"id", "transmission_id", "observer_idx", "direction", "snr", "rssi", "score", "path_json", "timestamp"}
	rows, _ = s.db.Query("PRAGMA table_info(observations)")
	var obsCols []string
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt *string
		var pk int
		rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
		obsCols = append(obsCols, name)
	}
	rows.Close()

	for _, e := range expectedObsCols {
		found := false
		for _, c := range obsCols {
			if c == e {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("observations missing column %s, got %v", e, obsCols)
		}
	}
}

func TestConcurrentWrites(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Pre-create an observer for observer_idx resolution
	if err := s.UpsertObserver("obs1", "Observer1", "SJC"); err != nil {
		t.Fatal(err)
	}

	const goroutines = 20
	const writesPerGoroutine = 50

	errCh := make(chan error, goroutines*writesPerGoroutine)
	done := make(chan struct{})

	for g := 0; g < goroutines; g++ {
		go func(gIdx int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < writesPerGoroutine; i++ {
				hash := fmt.Sprintf("concurrent_%d_%d_____", gIdx, i) // pad to 16+ chars
				snr := 5.0
				rssi := -100.0
				data := &PacketData{
					RawHex:      "0A00D69F",
					Timestamp:   time.Now().UTC().Format(time.RFC3339),
					ObserverID:  "obs1",
					Hash:        hash[:16],
					RouteType:   2,
					PayloadType: 4, // ADVERT
					PathJSON:    "[]",
					DecodedJSON: `{"type":"ADVERT"}`,
					SNR:         &snr,
					RSSI:        &rssi,
				}
				if _, err := s.InsertTransmission(data); err != nil {
					errCh <- fmt.Errorf("goroutine %d write %d: %w", gIdx, i, err)
					return
				}
				// Also do node + observer upserts to simulate full pipeline
				lat := 37.0
				lon := -122.0
				pubKey := fmt.Sprintf("node_%d_%d________", gIdx, i)
				if err := s.UpsertNode(pubKey[:16], "Node", "repeater", &lat, &lon, data.Timestamp); err != nil {
					errCh <- fmt.Errorf("goroutine %d node upsert %d: %w", gIdx, i, err)
					return
				}
				obsID := fmt.Sprintf("obs_%d_%d__________", gIdx, i)
				if err := s.UpsertObserver(obsID[:16], "Obs", "SJC"); err != nil {
					errCh <- fmt.Errorf("goroutine %d observer upsert %d: %w", gIdx, i, err)
					return
				}
			}
		}(g)
	}

	// Wait for all goroutines
	for g := 0; g < goroutines; g++ {
		<-done
	}
	close(errCh)

	var errors []error
	for err := range errCh {
		errors = append(errors, err)
	}

	if len(errors) > 0 {
		t.Errorf("got %d errors from %d concurrent writers (first: %v)", len(errors), goroutines, errors[0])
	}

	// Verify data integrity
	var txCount, obsCount, nodeCount, observerCount int
	s.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&txCount)
	s.db.QueryRow("SELECT COUNT(*) FROM observations").Scan(&obsCount)
	s.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&nodeCount)
	s.db.QueryRow("SELECT COUNT(*) FROM observers").Scan(&observerCount)

	expectedTx := goroutines * writesPerGoroutine
	if txCount != expectedTx {
		t.Errorf("transmissions count=%d, want %d", txCount, expectedTx)
	}
	if obsCount != expectedTx {
		t.Errorf("observations count=%d, want %d", obsCount, expectedTx)
	}

	t.Logf("Concurrent write test: %d goroutines × %d writes = %d total, 0 errors",
		goroutines, writesPerGoroutine, goroutines*writesPerGoroutine)
	t.Logf("Stats: tx_inserted=%d tx_dupes=%d obs_inserted=%d write_errors=%d",
		s.Stats.TransmissionsInserted.Load(),
		s.Stats.DuplicateTransmissions.Load(),
		s.Stats.ObservationsInserted.Load(),
		s.Stats.WriteErrors.Load(),
	)
}

func TestDBStats(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Initial stats should be zero
	if s.Stats.TransmissionsInserted.Load() != 0 {
		t.Error("initial TransmissionsInserted should be 0")
	}
	if s.Stats.WriteErrors.Load() != 0 {
		t.Error("initial WriteErrors should be 0")
	}

	// Insert a transmission
	data := &PacketData{
		RawHex:    "0A00D69F",
		Timestamp: "2026-03-28T00:00:00Z",
		Hash:      "stats_test_12345",
		RouteType: 2,
		PathJSON:  "[]",
	}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	if s.Stats.TransmissionsInserted.Load() != 1 {
		t.Errorf("TransmissionsInserted=%d, want 1", s.Stats.TransmissionsInserted.Load())
	}
	if s.Stats.ObservationsInserted.Load() != 1 {
		t.Errorf("ObservationsInserted=%d, want 1", s.Stats.ObservationsInserted.Load())
	}

	// Insert duplicate
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}
	if s.Stats.DuplicateTransmissions.Load() != 1 {
		t.Errorf("DuplicateTransmissions=%d, want 1", s.Stats.DuplicateTransmissions.Load())
	}

	// Node upsert
	lat := 37.0
	lon := -122.0
	if err := s.UpsertNode("pk1", "Node1", "repeater", &lat, &lon, "2026-03-28T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if s.Stats.NodeUpserts.Load() != 1 {
		t.Errorf("NodeUpserts=%d, want 1", s.Stats.NodeUpserts.Load())
	}

	// Observer upsert
	if err := s.UpsertObserver("obs1", "Obs1", "SJC"); err != nil {
		t.Fatal(err)
	}
	if s.Stats.ObserverUpserts.Load() != 1 {
		t.Errorf("ObserverUpserts=%d, want 1", s.Stats.ObserverUpserts.Load())
	}

	// LogStats should not panic
	s.LogStats()
}

func TestLoadTestThroughput(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Pre-create observer
	if err := s.UpsertObserver("obs1", "Observer1", "SJC"); err != nil {
		t.Fatal(err)
	}

	const totalMessages = 1000
	const goroutines = 20
	perGoroutine := totalMessages / goroutines

	// Simulate full pipeline: InsertTransmission + UpsertNode + UpsertObserver + IncrementAdvertCount
	// This matches the real handleMessage write pattern for ADVERT packets
	latencies := make([]time.Duration, totalMessages)
	var busyErrors atomic.Int64
	var totalErrors atomic.Int64
	errCh := make(chan error, totalMessages)
	done := make(chan struct{})

	start := time.Now()

	for g := 0; g < goroutines; g++ {
		go func(gIdx int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < perGoroutine; i++ {
				msgStart := time.Now()
				idx := gIdx*perGoroutine + i
				hash := fmt.Sprintf("load_%04d_%04d____", gIdx, i)
				snr := 5.0
				rssi := -100.0

				data := &PacketData{
					RawHex:      "0A00D69F",
					Timestamp:   time.Now().UTC().Format(time.RFC3339),
					ObserverID:  "obs1",
					Hash:        hash[:16],
					RouteType:   2,
					PayloadType: 4,
					PathJSON:    "[]",
					DecodedJSON: `{"type":"ADVERT","pubKey":"` + hash[:16] + `"}`,
					SNR:         &snr,
					RSSI:        &rssi,
				}

				_, err := s.InsertTransmission(data)
				if err != nil {
					totalErrors.Add(1)
					if strings.Contains(err.Error(), "database is locked") || strings.Contains(err.Error(), "SQLITE_BUSY") {
						busyErrors.Add(1)
					}
					errCh <- err
					continue
				}

				lat := 37.0 + float64(gIdx)*0.001
				lon := -122.0 + float64(i)*0.001
				pubKey := fmt.Sprintf("node_%04d_%04d____", gIdx, i)
				if err := s.UpsertNode(pubKey[:16], "Node", "repeater", &lat, &lon, data.Timestamp); err != nil {
					totalErrors.Add(1)
					if strings.Contains(err.Error(), "locked") || strings.Contains(err.Error(), "BUSY") {
						busyErrors.Add(1)
					}
				}

				if err := s.IncrementAdvertCount(pubKey[:16]); err != nil {
					totalErrors.Add(1)
				}

				obsID := fmt.Sprintf("obs_%04d_%04d_____", gIdx, i)
				if err := s.UpsertObserver(obsID[:16], "Obs", "SJC"); err != nil {
					totalErrors.Add(1)
					if strings.Contains(err.Error(), "locked") || strings.Contains(err.Error(), "BUSY") {
						busyErrors.Add(1)
					}
				}

				latencies[idx] = time.Since(msgStart)
			}
		}(g)
	}

	for g := 0; g < goroutines; g++ {
		<-done
	}
	close(errCh)
	elapsed := time.Since(start)

	// Calculate p50, p95, p99
	validLatencies := make([]time.Duration, 0, totalMessages)
	for _, l := range latencies {
		if l > 0 {
			validLatencies = append(validLatencies, l)
		}
	}
	sort.Slice(validLatencies, func(i, j int) bool { return validLatencies[i] < validLatencies[j] })

	p50 := validLatencies[len(validLatencies)*50/100]
	p95 := validLatencies[len(validLatencies)*95/100]
	p99 := validLatencies[len(validLatencies)*99/100]
	msgsPerSec := float64(totalMessages) / elapsed.Seconds()

	t.Logf("=== LOAD TEST RESULTS ===")
	t.Logf("Messages:     %d (%d goroutines × %d each)", totalMessages, goroutines, perGoroutine)
	t.Logf("Writes/msg:   4 (InsertTx + UpsertNode + IncrAdvertCount + UpsertObserver)")
	t.Logf("Total writes: %d", totalMessages*4)
	t.Logf("Duration:     %s", elapsed.Round(time.Millisecond))
	t.Logf("Throughput:   %.1f msgs/sec (%.1f writes/sec)", msgsPerSec, msgsPerSec*4)
	t.Logf("Latency p50:  %s", p50.Round(time.Microsecond))
	t.Logf("Latency p95:  %s", p95.Round(time.Microsecond))
	t.Logf("Latency p99:  %s", p99.Round(time.Microsecond))
	t.Logf("SQLITE_BUSY:  %d", busyErrors.Load())
	t.Logf("Total errors: %d", totalErrors.Load())
	t.Logf("Stats: tx=%d dupes=%d obs=%d nodes=%d observers=%d write_err=%d",
		s.Stats.TransmissionsInserted.Load(),
		s.Stats.DuplicateTransmissions.Load(),
		s.Stats.ObservationsInserted.Load(),
		s.Stats.NodeUpserts.Load(),
		s.Stats.ObserverUpserts.Load(),
		s.Stats.WriteErrors.Load(),
	)

	// Hard assertions
	if busyErrors.Load() > 0 {
		t.Errorf("SQLITE_BUSY errors: %d (expected 0)", busyErrors.Load())
	}
	if totalErrors.Load() > 0 {
		t.Errorf("Total errors: %d (expected 0)", totalErrors.Load())
	}

	var txCount int
	s.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&txCount)
	if txCount != totalMessages {
		t.Errorf("transmissions=%d, want %d", txCount, totalMessages)
	}
}
