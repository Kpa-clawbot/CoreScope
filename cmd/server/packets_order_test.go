package main

import (
	"testing"
	"time"
)

// TestQueryPacketsOrdersByIngestID is the regression test for issue #1345.
//
// PR #1233 changed `first_seen` to be the observer's receive time (rxTime),
// not the moment the server ingested the row. When an observer buffers
// offline and uploads hours later, its packets land with old first_seen
// values. The /api/packets handler previously ordered by
// `first_seen DESC`, so buffered uploads with old rxTime appeared at the
// bottom while older-ingested packets with newer rxTime took the top —
// users on the packets page saw "no recent activity" even though MQTT
// ingest was active.
//
// Fix: default ordering for /api/packets is `t.id DESC` (ingest order).
// This test inserts two rows where row order by id and order by
// first_seen DISAGREE, then asserts the result is ordered by id DESC.
func TestQueryPacketsOrdersByIngestID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	// Row A: ingested FIRST (lower id), rxTime "newer" (fresher first_seen)
	freshFirstSeen := now.Add(-1 * time.Hour).Format(time.RFC3339)
	// Row B: ingested SECOND (higher id), rxTime "older" — simulating a
	// buffered observer upload that arrived after row A but contains a
	// packet the radio received hours earlier.
	bufferedFirstSeen := now.Add(-6 * time.Hour).Format(time.RFC3339)

	if _, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, payload_type)
		VALUES ('AA', 'hashfresh00000001', ?, 4)`, freshFirstSeen); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, payload_type)
		VALUES ('BB', 'hashbuffered00002', ?, 4)`, bufferedFirstSeen); err != nil {
		t.Fatal(err)
	}

	result, err := db.QueryPackets(PacketQuery{Limit: 50, Order: "DESC"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Packets) != 2 {
		t.Fatalf("expected 2 packets, got %d", len(result.Packets))
	}
	// With first_seen DESC (the bug), the order would be [fresh, buffered]
	// because the fresh row has the newer rxTime. With the fix (id DESC),
	// order is [buffered, fresh] because the buffered row was ingested
	// second and has the higher id.
	first, _ := result.Packets[0]["hash"].(string)
	second, _ := result.Packets[1]["hash"].(string)
	if first != "hashbuffered00002" || second != "hashfresh00000001" {
		t.Errorf("expected order [buffered, fresh] by ingest id DESC, got [%s, %s]",
			first, second)
	}
}

// TestQueryPacketsSinceFilterUsesFirstSeen documents the chosen semantic for
// the `since=` query param: it still filters by `first_seen` (radio receive
// time), NOT by ingest time. Rationale: callers using `since=` expect
// "packets the network received since X" — buffered uploads of older
// packets should still be EXCLUDED from a `since=15min` view even if
// they were ingested in the last 15 minutes. Display order is by ingest
// id (issue #1345 fix); filter semantic is unchanged.
func TestQueryPacketsSinceFilterUsesFirstSeen(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	recent := now.Add(-30 * time.Minute).Format(time.RFC3339)
	old := now.Add(-6 * time.Hour).Format(time.RFC3339)
	sinceCutoff := now.Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-30 * time.Minute).Unix()
	oldEpoch := now.Add(-6 * time.Hour).Unix()

	if _, err := db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen, packet_count)
		VALUES ('obs1', 'Obs1', ?, ?, 1)`, recent, recent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, payload_type)
		VALUES ('AA', 'recentrx00000001', ?, 4)`, recent); err != nil {
		t.Fatal(err)
	}
	// Buffered upload — ingested SECOND, but rxTime is 6h ago.
	if _, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, payload_type)
		VALUES ('BB', 'oldrxbuffered001', ?, 4)`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10, -90, '[]', ?)`, recentEpoch); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 1, 10, -90, '[]', ?)`, oldEpoch); err != nil {
		t.Fatal(err)
	}

	result, err := db.QueryPackets(PacketQuery{Limit: 50, Order: "DESC", Since: sinceCutoff})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Packets) != 1 {
		t.Fatalf("since= should filter by first_seen (rxTime); expected 1 packet, got %d",
			len(result.Packets))
	}
	h, _ := result.Packets[0]["hash"].(string)
	if h != "recentrx00000001" {
		t.Errorf("expected the rxTime-recent packet, got %s", h)
	}
}
