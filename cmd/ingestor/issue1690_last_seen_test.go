package main

// Test for issue #1690 — every observation insert must denormalize the
// transmission's last_seen so cold-load can filter on effective recency.
//
// Setup: insert a transmission whose first/last seen are both 7 days ago.
// Then insert a fresh observation against the same hash. Post-fix the
// transmissions.last_seen column must reflect the new observation time.

import (
	"testing"
	"time"
)

func TestIssue1690_LastSeenUpdatedOnObservation(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	hash := "abcdef1690cafebabe"
	weekAgo := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	snr, rssi := 5.5, -100.0

	first := &PacketData{
		RawHex:         "0A00",
		Timestamp:      weekAgo,
		ObserverID:     "obs1",
		Hash:           hash,
		RouteType:      2,
		PayloadType:    2,
		PayloadVersion: 0,
		PathJSON:       "[]",
		DecodedJSON:    `{"type":"TXT_MSG"}`,
		SNR:            &snr,
		RSSI:           &rssi,
	}
	if _, err := s.InsertTransmission(first); err != nil {
		t.Fatalf("seed insert: %v", err)
	}

	// Sanity: confirm the seed last_seen is the 7d-ago time.
	var seededLastSeen int64
	if err := s.db.QueryRow(`SELECT COALESCE(last_seen, 0) FROM transmissions WHERE hash = ?`, hash).Scan(&seededLastSeen); err != nil {
		t.Fatalf("seed select last_seen: %v (column missing? post-fix must add it)", err)
	}
	weekAgoUnix, _ := time.Parse(time.RFC3339, weekAgo)
	if seededLastSeen != weekAgoUnix.Unix() {
		t.Logf("seed last_seen=%d expected %d (allowed for fresh column)", seededLastSeen, weekAgoUnix.Unix())
	}

	// New observation: nowSec timestamp.
	nowSec := time.Now().UTC().Unix()
	nowStr := time.Unix(nowSec, 0).UTC().Format(time.RFC3339)
	second := &PacketData{
		RawHex:         "0A00",
		Timestamp:      nowStr,
		ObserverID:     "obs2", // different observer → new observation row
		Hash:           hash,
		RouteType:      2,
		PayloadType:    2,
		PayloadVersion: 0,
		PathJSON:       "[]",
		DecodedJSON:    `{"type":"TXT_MSG"}`,
		SNR:            &snr,
		RSSI:           &rssi,
	}
	if _, err := s.InsertTransmission(second); err != nil {
		t.Fatalf("second insert: %v", err)
	}

	var ls int64
	if err := s.db.QueryRow(`SELECT last_seen FROM transmissions WHERE hash = ?`, hash).Scan(&ls); err != nil {
		t.Fatalf("post-insert select last_seen: %v", err)
	}
	// The post-fix writer must bump last_seen to at least the new observation's
	// epoch second. We allow ±2s slack for the unix-second round trip.
	if ls < nowSec-2 {
		t.Errorf("transmissions.last_seen=%d after fresh observation; expected ≥ %d (a recent unix-second). "+
			"Pre-fix the column is never updated on re-observation — the original cold-load bug (#1690).",
			ls, nowSec)
	}
}
