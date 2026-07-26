package main

// Tests for the ping-score highscore/leaderboard feature's detection side:
// isPingTrigger/pingTriggerSenderAndText mirror cmd/server/db.go's copies
// exactly, and InsertTransmission writes exactly one ping_triggers row per
// new ping-triggering CHAN transmission. Also covers backfillPingTriggers,
// the one-time async migration that catches CHAN messages sent before this
// feature existed (which never went through InsertTransmission's isNew
// detection hook).

import (
	"context"
	"testing"
)

func TestIsPingTrigger(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"ping", true},
		{"Ping", true},
		{"PING", true},
		{"/ping", true},
		{"@CoreScopeBot ping", true},
		{"@CoreScopeBot /ping", true},
		{"  ping  ", true},
		{"ping there", false},
		{"pingpong", false},
		{"pong", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isPingTrigger(c.text); got != c.want {
			t.Errorf("isPingTrigger(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestPingTriggerSenderAndText(t *testing.T) {
	sender, text, ok := pingTriggerSenderAndText(`{"type":"CHAN","channel":"#test","text":"Alice: ping"}`)
	if !ok {
		t.Fatal("expected ok=true for valid JSON")
	}
	if sender != "Alice" {
		t.Errorf("sender = %q, want Alice", sender)
	}
	if text != "ping" {
		t.Errorf("text = %q, want ping", text)
	}

	if _, _, ok := pingTriggerSenderAndText("not json"); ok {
		t.Error("expected ok=false for invalid JSON")
	}
}

func insertChanTx(t *testing.T, s *Store, hash, text, channelHash string) {
	t.Helper()
	data := &PacketData{
		RawHex:      "AABB",
		Timestamp:   "2026-03-25T00:00:00Z",
		ObserverID:  "obs1",
		Hash:        hash,
		RouteType:   1,
		PayloadType: 5,
		DecodedJSON: `{"type":"CHAN","channel":"` + channelHash + `","text":"` + text + `"}`,
		ChannelHash: channelHash,
		PathJSON:    "[]",
	}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatalf("InsertTransmission: %v", err)
	}
}

func countPingTriggers(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ping_triggers`).Scan(&n); err != nil {
		t.Fatalf("count ping_triggers: %v", err)
	}
	return n
}

func TestInsertTransmission_PingTriggerRecorded(t *testing.T) {
	s := openNeighborsStore(t) // reuses the OpenStore+t.Cleanup helper from issue1865_test.go

	insertChanTx(t, s, "pinghash00000001", "Alice: ping", "#test")

	if got := countPingTriggers(t, s); got != 1 {
		t.Fatalf("ping_triggers count = %d, want 1", got)
	}
	var hash, channelHash, sender, firstSeen string
	if err := s.db.QueryRow(
		`SELECT hash, channel_hash, sender, first_seen FROM ping_triggers WHERE tx_id = (SELECT id FROM transmissions WHERE hash = ?)`,
		"pinghash00000001",
	).Scan(&hash, &channelHash, &sender, &firstSeen); err != nil {
		t.Fatalf("read ping_triggers row: %v", err)
	}
	if hash != "pinghash00000001" || channelHash != "#test" || sender != "Alice" {
		t.Errorf("ping_triggers row = (hash=%q channel=%q sender=%q), want (pinghash00000001, #test, Alice)", hash, channelHash, sender)
	}
}

func TestInsertTransmission_NonPingNotRecorded(t *testing.T) {
	s := openNeighborsStore(t)

	insertChanTx(t, s, "chatmsg00000001", "Alice: hello everyone", "#test")

	if got := countPingTriggers(t, s); got != 0 {
		t.Errorf("ping_triggers count = %d, want 0 for a non-ping message", got)
	}
}

func TestInsertTransmission_RepeatObservationDoesNotDuplicate(t *testing.T) {
	s := openNeighborsStore(t)

	insertChanTx(t, s, "pinghash00000002", "Bob: /ping", "#test")
	// A second observation of the SAME hash (e.g. heard by another
	// observer) must not add a second ping_triggers row -- the
	// transmissions table's own find-or-create semantics mean
	// InsertTransmission's isNew branch (where the ping check lives)
	// only ever runs once per hash.
	insertChanTx(t, s, "pinghash00000002", "Bob: /ping", "#test")

	if got := countPingTriggers(t, s); got != 1 {
		t.Errorf("ping_triggers count = %d, want 1 (no duplicate on repeat observation)", got)
	}
}

// insertHistoricalChanTxDirect inserts a transmission row directly via SQL,
// bypassing InsertTransmission entirely -- simulating a CHAN message that
// was ingested before the ping-score feature existed, so it never went
// through the isNew detection hook and has no ping_triggers row.
func insertHistoricalChanTxDirect(t *testing.T, s *Store, hash, text, channelHash string) int64 {
	t.Helper()
	res, err := s.db.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, channel_hash, last_seen)
		 VALUES ('AABB', ?, '2026-01-01T00:00:00Z', 1, 5, 1, ?, ?, 0)`,
		hash, `{"type":"CHAN","channel":"`+channelHash+`","text":"`+text+`"}`, channelHash,
	)
	if err != nil {
		t.Fatalf("insert historical tx: %v", err)
	}
	txID, _ := res.LastInsertId()
	return txID
}

func TestBackfillPingTriggers_FindsHistoricalPings(t *testing.T) {
	s := openNeighborsStore(t)
	// OpenStore already schedules this same migration in the background;
	// let its (empty-DB, no-op) first pass finish before inserting test
	// data, or the automatic run could race the inserts below and find
	// them itself before this test's own explicit call does.
	s.WaitForAsyncMigrations()

	insertHistoricalChanTxDirect(t, s, "histping0000001", "Alice: ping", "#test")
	insertHistoricalChanTxDirect(t, s, "histchat0000001", "Alice: just chatting", "#test")

	if got := countPingTriggers(t, s); got != 0 {
		t.Fatalf("ping_triggers count before backfill = %d, want 0 (historical rows bypass InsertTransmission)", got)
	}

	if err := backfillPingTriggers(context.Background(), s.db); err != nil {
		t.Fatalf("backfillPingTriggers: %v", err)
	}

	if got := countPingTriggers(t, s); got != 1 {
		t.Fatalf("ping_triggers count after backfill = %d, want 1 (only the historical ping, not the chat message)", got)
	}
	var hash string
	if err := s.db.QueryRow(`SELECT hash FROM ping_triggers`).Scan(&hash); err != nil {
		t.Fatalf("read backfilled row: %v", err)
	}
	if hash != "histping0000001" {
		t.Errorf("backfilled hash = %q, want histping0000001", hash)
	}
}

func TestBackfillPingTriggers_IdempotentOnRerun(t *testing.T) {
	s := openNeighborsStore(t)
	s.WaitForAsyncMigrations() // let OpenStore's own (empty-DB) pass finish first
	insertHistoricalChanTxDirect(t, s, "histping0000002", "Bob: /ping", "#test")

	if err := backfillPingTriggers(context.Background(), s.db); err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	if err := backfillPingTriggers(context.Background(), s.db); err != nil {
		t.Fatalf("second backfill: %v", err)
	}

	if got := countPingTriggers(t, s); got != 1 {
		t.Errorf("ping_triggers count after two backfill runs = %d, want 1 (tx_id PRIMARY KEY + INSERT OR IGNORE must dedupe)", got)
	}
}

// TestOpenStore_SchedulesPingTriggersBackfill confirms the migration is
// actually wired into OpenStore's boot path (registered + completed),
// not just directly callable in isolation like the tests above.
func TestOpenStore_SchedulesPingTriggersBackfill(t *testing.T) {
	s := openNeighborsStore(t)
	s.WaitForAsyncMigrations()

	status, err := s.AsyncMigrationStatus("ping_triggers_backfill_v1")
	if err != nil {
		t.Fatalf("AsyncMigrationStatus: %v", err)
	}
	if status != "done" {
		t.Errorf("ping_triggers_backfill_v1 status = %q, want %q -- OpenStore must schedule and complete this migration on every boot", status, "done")
	}
}

func TestInsertTransmission_NonChanPayloadNotChecked(t *testing.T) {
	s := openNeighborsStore(t)

	// PayloadType 2 (not 5/CHAN) with "ping"-looking text must never be
	// mistaken for a channel ping trigger.
	data := &PacketData{
		RawHex:      "AABB",
		Timestamp:   "2026-03-25T00:00:00Z",
		ObserverID:  "obs1",
		Hash:        "advhash00000001",
		RouteType:   1,
		PayloadType: 2,
		DecodedJSON: `{"type":"ADVERT","text":"ping"}`,
		PathJSON:    "[]",
	}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatalf("InsertTransmission: %v", err)
	}

	if got := countPingTriggers(t, s); got != 0 {
		t.Errorf("ping_triggers count = %d, want 0 for a non-CHAN payload type", got)
	}
}
