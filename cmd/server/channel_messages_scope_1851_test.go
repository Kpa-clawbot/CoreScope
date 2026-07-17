// Regression test for issue #1851: channel messages must round-trip
// transport scope_name so the UI can render a region label per message.
package main

import (
	"testing"
	"time"
)

// TestGetChannelMessagesScopeName_1851 asserts that GetChannelMessages emits
// a `scope_name` key on every returned row, populated from
// transmissions.scope_name — non-empty for transport-scoped packets, empty
// string for unscoped (NULL) rows. Table-driven with two representative
// cases.
func TestGetChannelMessagesScopeName_1851(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// setupTestDB's schema doesn't include scope_name — this ALTER mirrors
	// what the real dbschema/migrations do in prod, matching the
	// TestHandleScopeStats and TestGetScopeStats patterns already in the
	// test suite.
	if _, err := db.conn.Exec(`ALTER TABLE transmissions ADD COLUMN scope_name TEXT DEFAULT NULL`); err != nil {
		t.Fatalf("add scope_name column: %v", err)
	}
	db.hasScopeName = true

	now := time.Now().UTC()
	tsA := now.Add(-2 * time.Minute).Format(time.RFC3339)
	tsB := now.Add(-1 * time.Minute).Format(time.RFC3339)
	epochA := now.Add(-2 * time.Minute).Unix()
	epochB := now.Add(-1 * time.Minute).Unix()

	db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES ('obs1', 'Obs One', 'SJC')`)

	// Row A: transport-scoped, scope_name="EU".
	if _, err := db.conn.Exec(`INSERT INTO transmissions
		(raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash, scope_name)
		VALUES ('AA', 'chan1851scope01', ?, 0, 5,
		'{"type":"CHAN","channel":"#s","text":"A: hello EU","sender":"A"}', '#s', 'EU')`, tsA); err != nil {
		t.Fatalf("insert tx A: %v", err)
	}
	// Row B: unscoped, scope_name NULL.
	if _, err := db.conn.Exec(`INSERT INTO transmissions
		(raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash, scope_name)
		VALUES ('BB', 'chan1851scope02', ?, 1, 5,
		'{"type":"CHAN","channel":"#s","text":"B: hello world","sender":"B"}', '#s', NULL)`, tsB); err != nil {
		t.Fatalf("insert tx B: %v", err)
	}
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10.0, -90, '[]', ?)`, epochA)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 1, 9.0, -91, '[]', ?)`, epochB)

	messages, total, err := db.GetChannelMessages("#s", 100, 0)
	if err != nil {
		t.Fatalf("GetChannelMessages: %v", err)
	}
	if total != 2 || len(messages) != 2 {
		t.Fatalf("expected 2 messages, total=%d len=%d", total, len(messages))
	}

	// Emission order is LatestEpoch ASC → tx1 (epochA) first, tx2 second.
	cases := []struct {
		idx      int
		sender   string
		wantScope string // "" means expect empty/absent (unscoped)
	}{
		{0, "A", "EU"},
		{1, "B", ""},
	}
	for _, c := range cases {
		m := messages[c.idx]
		if got, _ := m["sender"].(string); got != c.sender {
			t.Errorf("messages[%d].sender = %q, want %q", c.idx, got, c.sender)
		}
		got, ok := m["scope_name"]
		if !ok {
			t.Errorf("messages[%d] missing key scope_name (all rows must carry it for stable API contract)", c.idx)
			continue
		}
		gotStr, _ := got.(string)
		if gotStr != c.wantScope {
			t.Errorf("messages[%d].scope_name = %q, want %q", c.idx, gotStr, c.wantScope)
		}
	}
}
