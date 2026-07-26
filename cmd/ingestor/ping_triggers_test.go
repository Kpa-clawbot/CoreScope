package main

// Tests for the ping-score highscore/leaderboard feature's detection side:
// isPingTrigger/pingTriggerSenderAndText mirror cmd/server/db.go's copies
// exactly, and InsertTransmission writes exactly one ping_triggers row per
// new ping-triggering CHAN transmission.

import "testing"

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
