package main

// Regression tests for issue #1366: Channel view shows stale timestamps
// because GetChannelMessages emits tx.FirstSeen (first-observation time)
// when the operator-visible expectation is the latest observation time
// (tx.LatestSeen). For repeated heartbeat-style messages whose tx.Hash is
// stable, FirstSeen stays pinned to the very first observation while the
// real-world transmission keeps repeating, producing a multi-hour gap
// between the channel view and the operator's live MeshCore client.
//
// Server-side UTC clocks are trusted; client-reported sender_timestamp
// is NOT (firmware lacks reliable wall-clock on many builds). Therefore
// the fix uses tx.LatestSeen (== max observation timestamp), NOT
// sender_timestamp. sender_timestamp remains exposed in the response
// for debug surfaces but MUST NOT be the rendered field.

import (
	"strconv"
	"testing"
	"time"
)

// TestChannelMessages_TimestampUsesLatestSeen: a CHAN tx with multiple
// observations spanning hours must render with the LATEST observation
// timestamp, not the first-seen ingest time.
func TestChannelMessages_TimestampUsesLatestSeen(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	firstSeen := now.Add(-7 * time.Hour).Format(time.RFC3339)
	firstSeenEpoch := now.Add(-7 * time.Hour).Unix()
	laterEpoch := now.Add(-5 * time.Minute).Unix()
	_ = laterEpoch

	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obsA', 'ObsA', 'SJC', ?, '2026-01-01T00:00:00Z', 10)`, firstSeen)
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obsB', 'ObsB', 'LAX', ?, '2026-01-01T00:00:00Z', 10)`, firstSeen)

	// One transmission with two observations: T0 (7h ago) and T1 (5m ago).
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('AA01', 'hash_repeated_msg', ?, 1, 5,
			'{"type":"CHAN","channel":"#test","text":"Heartbeat: ping","sender":"Heartbeat","sender_timestamp":` +
		strconv.FormatInt(firstSeenEpoch, 10) + `}',
		'#test')`, firstSeen)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10.0, -90, '["aa"]', ?)`, firstSeenEpoch)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 2, 11.0, -88, '["bb"]', ?)`, laterEpoch)

	store := NewPacketStore(db, nil)
	store.Load()

	msgs, total := store.GetChannelMessages("#test", 10, 0)
	if total != 1 {
		t.Fatalf("want 1 msg, got %d (msgs=%+v)", total, msgs)
	}
	got, _ := msgs[0]["timestamp"].(string)
	gotParsed, err := time.Parse(time.RFC3339, got)
	if err != nil {
		// Try the milli-second precision form that SQLite strftime emits.
		gotParsed, err = time.Parse("2006-01-02T15:04:05.000Z", got)
		if err != nil {
			gotParsed, err = time.Parse("2006-01-02T15:04:05.000Z07:00", got)
		}
	}
	if err != nil {
		t.Fatalf("timestamp not parseable: %q (%v)", got, err)
	}
	// LatestSeen should equal the laterEpoch observation (±1s).
	if delta := gotParsed.Unix() - laterEpoch; delta < -1 || delta > 1 {
		t.Errorf("timestamp: want ~%s (LatestSeen, observation at T-5m), got %q (Δ=%ds — likely FirstSeen, issue #1366)",
			time.Unix(laterEpoch, 0).UTC().Format(time.RFC3339), got, delta)
	}

	// first_seen MUST also be exposed separately so the UI/debug can see
	// when the analyzer first heard the packet (older than `timestamp`).
	fs, _ := msgs[0]["first_seen"].(string)
	if fs == "" {
		t.Errorf("first_seen field must be exposed alongside timestamp; got empty")
	}
	if fs == got {
		t.Errorf("first_seen should differ from latest-seen timestamp (both = %q)", got)
	}
}

// TestChannelMessages_TimestampNotSenderTimestamp: a CHAN tx whose
// decoded sender_timestamp is wildly off (e.g. client with bad RTC)
// must NOT cause the rendered timestamp to drift. Rendered timestamp
// must remain server UTC (LatestSeen/FirstSeen), regardless of what
// the client claimed.
func TestChannelMessages_TimestampNotSenderTimestamp(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	firstSeen := now.Add(-10 * time.Minute).Format(time.RFC3339)
	firstSeenEpoch := now.Add(-10 * time.Minute).Unix()

	// Client claims it sent the message in year 2000 (bad RTC).
	badSenderTs := int64(946684800) // 2000-01-01 UTC

	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obsX', 'ObsX', 'SJC', ?, '2026-01-01T00:00:00Z', 1)`, firstSeen)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('BB01', 'hash_bad_clock', ?, 1, 5,
			'{"type":"CHAN","channel":"#bad","text":"Alice: ping","sender":"Alice","sender_timestamp":` +
		strconv.FormatInt(badSenderTs, 10) + `}',
		'#bad')`, firstSeen)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10.0, -90, '["aa"]', ?)`, firstSeenEpoch)

	store := NewPacketStore(db, nil)
	store.Load()

	msgs, total := store.GetChannelMessages("#bad", 10, 0)
	if total != 1 {
		t.Fatalf("want 1 msg, got %d", total)
	}
	got, _ := msgs[0]["timestamp"].(string)
	// MUST be the server-side observation time, parseable as RFC3339, and
	// within ~1h of now — NOT the year-2000 client value.
	parsed, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("timestamp not RFC3339: %q (%v)", got, err)
	}
	if parsed.Year() < now.Year() {
		t.Errorf("rendered timestamp %q took on the client's bad sender_timestamp (year %d) instead of server UTC",
			got, parsed.Year())
	}
}

// TestChannelMessages_TimestampIsUTCZ: rendered timestamp MUST end with
// 'Z' (or +00:00) so the browser does NOT interpret it as a local-zone
// string and shift by the operator's TZ offset.
func TestChannelMessages_TimestampIsUTCZ(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	fs := now.Add(-30 * time.Minute).Format(time.RFC3339)
	ep := now.Add(-30 * time.Minute).Unix()

	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obsZ', 'ObsZ', 'SJC', ?, '2026-01-01T00:00:00Z', 1)`, fs)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('ZZ01', 'hash_zone_check', ?, 1, 5,
			'{"type":"CHAN","channel":"#zone","text":"Carol: ping","sender":"Carol"}',
		'#zone')`, fs)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 11.0, -89, '["zz"]', ?)`, ep)

	store := NewPacketStore(db, nil)
	store.Load()

	msgs, _ := store.GetChannelMessages("#zone", 10, 0)
	if len(msgs) != 1 {
		t.Fatalf("want 1 msg, got %d", len(msgs))
	}
	ts, _ := msgs[0]["timestamp"].(string)
	if ts == "" {
		t.Fatal("empty timestamp")
	}
	n := len(ts)
	if !(ts[n-1] == 'Z' || (n >= 6 && ts[n-6:] == "+00:00")) {
		t.Errorf("timestamp not UTC-suffixed (Z/+00:00): %q", ts)
	}
}

// TestChannelMessages_OrderedByLatestSeen: adversarial follow-up to #1366
// (PR #1368). The earlier fix only adjusted the rendered `timestamp`
// field; page SELECTION and SORT ORDER on both the in-memory and DB
// paths still used FirstSeen. This test pins the contract:
//
//   - tx-A: FirstSeen 24h ago, LatestSeen NOW (via a fresh observation).
//   - tx-B: FirstSeen 1h ago, LatestSeen 1h ago (single observation).
//
// Both paths MUST:
//  1. Return BOTH transmissions in a small (limit=10) page — tx-A must
//     not be excluded because its FirstSeen is old.
//  2. Return tx-A AFTER tx-B (newest-LatestSeen-LAST), matching the
//     tail-of-msgOrder convention used by the rest of the API and
//     the frontend's scrollToBottom().
func TestChannelMessages_OrderedByLatestSeen_InMemory(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	tOld := now.Add(-24 * time.Hour)
	tMid := now.Add(-1 * time.Hour)
	tFresh := now.Add(-1 * time.Minute)

	tOldStr := tOld.Format(time.RFC3339)
	tMidStr := tMid.Format(time.RFC3339)

	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obsO', 'ObsO', 'SJC', ?, '2026-01-01T00:00:00Z', 10)`, tOldStr)

	// tx-A: FirstSeen 24h ago, observations at T-24h and T-1m.
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('AAAA', 'order_hash_a', ?, 1, 5,
			'{"type":"CHAN","channel":"#ord","text":"Alpha: hb","sender":"Alpha"}', '#ord')`, tOldStr)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10.0, -90, '["aa"]', ?)`, tOld.Unix())
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 11.0, -88, '["aa"]', ?)`, tFresh.Unix())

	// tx-B: FirstSeen 1h ago, single observation at T-1h.
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('BBBB', 'order_hash_b', ?, 1, 5,
			'{"type":"CHAN","channel":"#ord","text":"Bravo: msg","sender":"Bravo"}', '#ord')`, tMidStr)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 1, 9.0, -91, '["bb"]', ?)`, tMid.Unix())

	store := NewPacketStore(db, nil)
	store.Load()

	msgs, total := store.GetChannelMessages("#ord", 10, 0)
	if total != 2 {
		t.Fatalf("in-memory: want total=2 (both tx returned), got %d", total)
	}
	if len(msgs) != 2 {
		t.Fatalf("in-memory: want 2 msgs in page, got %d (tx-A may have been excluded by FirstSeen-based selection)", len(msgs))
	}
	// Newest-LatestSeen LAST (tail convention). tx-A.LatestSeen > tx-B.LatestSeen,
	// so tx-A ("Alpha") must come AFTER tx-B ("Bravo").
	sender0, _ := msgs[0]["sender"].(string)
	sender1, _ := msgs[1]["sender"].(string)
	if sender0 != "Bravo" || sender1 != "Alpha" {
		t.Errorf("in-memory: want order [Bravo, Alpha] (by LatestSeen ASC), got [%q, %q]", sender0, sender1)
	}
}

func TestChannelMessages_OrderedByLatestSeen_DB(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	tOld := now.Add(-24 * time.Hour)
	tMid := now.Add(-1 * time.Hour)
	tFresh := now.Add(-1 * time.Minute)

	tOldStr := tOld.Format(time.RFC3339)
	tMidStr := tMid.Format(time.RFC3339)

	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obsD', 'ObsD', 'SJC', ?, '2026-01-01T00:00:00Z', 10)`, tOldStr)

	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('AADB', 'order_db_hash_a', ?, 1, 5,
			'{"type":"CHAN","channel":"#ordb","text":"Alpha: hb","sender":"Alpha"}', '#ordb')`, tOldStr)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10.0, -90, '["aa"]', ?)`, tOld.Unix())
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 11.0, -88, '["aa"]', ?)`, tFresh.Unix())

	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('BBDB', 'order_db_hash_b', ?, 1, 5,
			'{"type":"CHAN","channel":"#ordb","text":"Bravo: msg","sender":"Bravo"}', '#ordb')`, tMidStr)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 1, 9.0, -91, '["bb"]', ?)`, tMid.Unix())

	msgs, total, err := db.GetChannelMessages("#ordb", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("DB: want total=2 (both tx returned), got %d", total)
	}
	if len(msgs) != 2 {
		t.Fatalf("DB: want 2 msgs in page, got %d (tx-A may have been excluded by FirstSeen-based selection)", len(msgs))
	}
	sender0, _ := msgs[0]["sender"].(string)
	sender1, _ := msgs[1]["sender"].(string)
	if sender0 != "Bravo" || sender1 != "Alpha" {
		t.Errorf("DB: want order [Bravo, Alpha] (by LatestSeen ASC), got [%q, %q]", sender0, sender1)
	}
}
