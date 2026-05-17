package main

import (
	"sort"
	"strings"
	"testing"
	"time"
)

// TestQueryGroupedPacketsReturnsDistinctIATAs (#1189 R2):
// The default collapsed grouped view must already expose the DISTINCT set
// of observer IATA codes for each transmission — frontend can't compute it
// because p._children is empty until the user expands the row (or applies a
// non-default sort). Previously the cell showed a single IATA + "+N" of
// observer count, which conflates SAME-region redundancy with CROSS-region
// reception. R1 added a frontend helper but it only fired on the expanded
// view; this test gates the server-side fix.
//
// Seeds one transmission with observations from two IATAs (SJC, SFO) and
// asserts the grouped row carries distinct_iatas containing both codes.
func TestQueryGroupedPacketsReturnsDistinctIATAs(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	recentEpoch := now.Add(-1 * time.Hour).Unix()

	// Observers: SJC + SFO + a third with no IATA (should be excluded).
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obsA', 'A', 'SJC', ?, '2026-01-01T00:00:00Z', 10)`, now.Format(time.RFC3339))
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obsB', 'B', 'SFO', ?, '2026-01-01T00:00:00Z', 10)`, now.Format(time.RFC3339))
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obsC', 'C', '',    ?, '2026-01-01T00:00:00Z', 10)`, now.Format(time.RFC3339))

	// One transmission with 3 observations (SJC, SFO, no-IATA).
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('AABB', 'deadbeefcafef00d', ?, 1, 4, '{}')`, now.Format(time.RFC3339))
	// v3 schema: observer_idx = observers.rowid (auto-assigned 1,2,3 in insert order).
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 12.0, -80, '["aa"]', ?)`, recentEpoch)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 2,  8.0, -90, '["aa"]', ?)`, recentEpoch-30)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 3,  5.0, -95, '["aa"]', ?)`, recentEpoch-60)

	result, err := db.QueryGroupedPackets(PacketQuery{Limit: 50})
	if err != nil {
		t.Fatalf("QueryGroupedPackets: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected 1 grouped tx, got %d", result.Total)
	}

	row := result.Packets[0]
	raw, ok := row["distinct_iatas"]
	if !ok {
		t.Fatalf("expected distinct_iatas key in grouped row, got: %#v", row)
	}
	iatas, ok := raw.([]string)
	if !ok {
		t.Fatalf("expected distinct_iatas to be []string, got %T (%v)", raw, raw)
	}
	sort.Strings(iatas)
	want := []string{"SFO", "SJC"}
	if strings.Join(iatas, ",") != strings.Join(want, ",") {
		t.Fatalf("distinct_iatas = %v, want %v (must exclude empty-IATA observers, dedupe)", iatas, want)
	}
}

// TestQueryGroupedPacketsDistinctIATAsEmptyWhenNoIATA (#1189 R2):
// Group whose observers all have no IATA → distinct_iatas should be empty
// (or absent / empty slice) — must NOT carry stale data from another group.
func TestQueryGroupedPacketsDistinctIATAsEmptyWhenNoIATA(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	recentEpoch := now.Add(-1 * time.Hour).Unix()

	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obsX', 'X', '', ?, '2026-01-01T00:00:00Z', 1)`, now.Format(time.RFC3339))
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('AA', '1111222233334444', ?, 1, 4, '{}')`, now.Format(time.RFC3339))
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10.0, -85, '[]', ?)`, recentEpoch)

	result, err := db.QueryGroupedPackets(PacketQuery{Limit: 50})
	if err != nil {
		t.Fatalf("QueryGroupedPackets: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected 1 grouped tx, got %d", result.Total)
	}
	row := result.Packets[0]
	raw, ok := row["distinct_iatas"]
	if !ok {
		// absent key acceptable — treat as empty
		return
	}
	iatas, ok := raw.([]string)
	if !ok {
		t.Fatalf("distinct_iatas should be []string, got %T", raw)
	}
	if len(iatas) != 0 {
		t.Fatalf("distinct_iatas should be empty for no-IATA group, got %v", iatas)
	}
}
