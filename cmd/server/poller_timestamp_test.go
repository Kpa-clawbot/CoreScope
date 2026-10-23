package main

import "testing"

func TestGetNewTransmissionsSinceIncludesTimestamp(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	const firstSeen = "2026-01-16T10:00:00Z"
	if _, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen) VALUES ('AABB', 'poller-timestamp', $1)`, firstSeen); err != nil {
		t.Fatal(err)
	}

	packets, err := db.GetNewTransmissionsSince(0, 1)
	if err != nil || len(packets) != 1 {
		t.Fatalf("poller query returned %d packets: %v", len(packets), err)
	}
	if got := packets[0]["timestamp"]; got != firstSeen {
		t.Errorf("fallback packet timestamp=%v, want first_seen %q", got, firstSeen)
	}
	if got := packets[0]["first_seen"]; got != firstSeen {
		t.Errorf("first_seen=%v, want %q", got, firstSeen)
	}
}
