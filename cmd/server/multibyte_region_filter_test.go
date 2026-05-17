package main

import (
	"testing"
	"time"
)

// TestMultiByteCapability_RegionFiltered_PreservesConfirmedStatus verifies
// that GetAnalyticsHashSizes returns a populated multiByteCapability list
// even when a region filter is applied. The frontend (analytics.js) merges
// this into the adopter table to render per-node "confirmed/suspected/unknown"
// badges. When the field is missing or empty under a region filter, every
// row falls back to "unknown" — see meshcore.meshat.se/#/analytics filtered
// by JKG showing 14 "unknown" while the unfiltered view shows 0.
//
// Multi-byte capability is a property of the NODE (advertised hash_size from
// its own adverts), not the observing region. Region filter should affect
// which nodes appear in the result list (multiByteNodes), not their cap status.
//
// Pre-fix behavior: multiByteCapability is only populated when region == "".
// This test fails because result["multiByteCapability"] is absent under
// region="JKG", so the lookup returns nil/false.
func TestMultiByteCapability_RegionFiltered_PreservesConfirmedStatus(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-1 * time.Hour).Unix()

	// Two observers in different regions.
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs-sjc', 'Obs SJC', 'SJC', ?, '2026-01-01T00:00:00Z', 100)`, recent)
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs-jkg', 'Obs JKG', 'JKG', ?, '2026-01-01T00:00:00Z', 100)`, recent)

	// Node A: a JKG-region repeater that advertises multi-byte (hash_size=2).
	// Its zero-hop direct advert is only heard by obs-SJC (e.g. an out-of-region
	// listener that happens to pick it up). Under the JKG region filter, the
	// computeAnalyticsHashSizes() pass will see a smaller advert dataset, but
	// the node's multi-byte capability is intrinsic and should still resolve
	// to "confirmed" via the global advert evidence.
	pkA := "aaa0000000000001"
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role)
		VALUES (?, 'Node-A', 'repeater')`, pkA)

	decodedA := `{"pubKey":"` + pkA + `","name":"Node-A","type":"ADVERT","flags":{"isRepeater":true}}`

	// Zero-hop direct advert (route_type=2, payload_type=4),
	// pathByte 0x40 → hash_size bits 01 → 2 bytes.
	// Heard by obs-SJC ONLY.
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('1240aabbccdd', 'a_zh_direct', ?, 2, 4, ?)`, recent, decodedA)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 12.0, -85, '[]', ?)`, recentEpoch)

	// Node A also appears as a path hop in a JKG-observed packet, so it
	// shows up in the JKG region's node list.
	// route_type=1 (flood), payload_type=4, pathByte 0x41 (hs=2, hops=1)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('1141aabbccdd', 'a_jkg_relay', ?, 1, 4, ?)`, recent, decodedA)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 2, 8.0, -95, '["aa"]', ?)`, recentEpoch)

	store := NewPacketStore(db, nil)
	store.Load()

	// Sanity: unfiltered view exposes the field.
	unfiltered := store.GetAnalyticsHashSizes("")
	if _, ok := unfiltered["multiByteCapability"]; !ok {
		t.Fatal("unfiltered result missing multiByteCapability — test setup is wrong")
	}

	// The actual assertion: region-filtered view MUST also expose the field
	// AND must report Node A as "confirmed", not "unknown".
	result := store.GetAnalyticsHashSizes("JKG")
	capsRaw, ok := result["multiByteCapability"]
	if !ok {
		t.Fatalf("expected multiByteCapability in region=JKG result, got keys: %v", keysOf(result))
	}
	caps, ok := capsRaw.([]MultiByteCapEntry)
	if !ok {
		t.Fatalf("expected []MultiByteCapEntry, got %T", capsRaw)
	}

	var foundA *MultiByteCapEntry
	for i := range caps {
		if caps[i].PublicKey == pkA {
			foundA = &caps[i]
			break
		}
	}
	if foundA == nil {
		t.Fatalf("Node A missing from region=JKG multiByteCapability (have %d entries)", len(caps))
	}
	if foundA.Status != "confirmed" {
		t.Errorf("Node A status under region=JKG = %q, want %q (region filter wrongly downgraded multi-byte capability evidence)", foundA.Status, "confirmed")
	}
}

func keysOf(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
