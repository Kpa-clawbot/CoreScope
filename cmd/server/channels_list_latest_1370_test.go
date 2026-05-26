package main

// Regression tests for issue #1370: the channel-LIST endpoint
// (/api/channels) currently picks lastActivity / lastMessage / lastSender
// using max(FirstSeen) across the channel's transmissions. This sibling
// bug to #1366 (fixed in PR #1368 for the per-channel detail endpoint)
// causes a long-running heartbeat tx whose FirstSeen is older but whose
// LatestSeen is fresher than a brand-new message to LOSE — the row
// preview shows the genuinely-newest distinct-FirstSeen tx, not the
// truly-most-recently-observed activity.
//
// Operator's screenshot ground truth on staging:
//   - Voodoo3's "tst hmdpt" had FirstSeen 18:42, LatestSeen ~01:42 (just
//     observed via a fresh relay).
//   - Marsh-MQTT's heartbeat had FirstSeen 01:28, LatestSeen 01:28
//     (one-shot, never re-observed).
//   - With max-FirstSeen, Marsh wins (current bug).
//   - With max-LatestSeen, Voodoo3 wins (matches operator's mental
//     model of "the last activity I saw on this channel").
//
// Decision (LOCKED, mirrors #1368): Option A — max-LatestSeen for both
// the in-memory PacketStore.GetChannels path AND the DB.GetChannels
// path. NOT sender_timestamp.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"
)

// findChannel returns the entry with the given hash from a GetChannels
// result, or nil if absent.
func findChannel(list []map[string]interface{}, hash string) map[string]interface{} {
	for _, c := range list {
		if h, _ := c["hash"].(string); h == hash {
			return c
		}
	}
	return nil
}

// TestGetChannels_ListRowUsesMaxLatestSeen_InMemory exercises the
// in-memory PacketStore.GetChannels path. Three GRP_TXT transmissions
// are seeded for #test:
//
//   - tx-OLD:       FirstSeen T-2h,  LatestSeen T-NOW (heartbeat re-relayed).
//   - tx-MID:       FirstSeen T-30m, LatestSeen T-30m (single observation).
//   - tx-RECENT-FS: FirstSeen T-5m,  LatestSeen T-5m  (single observation).
//
// Under the (buggy) max-FirstSeen rule, tx-RECENT-FS wins because its
// FirstSeen is newest. Under the (correct, Option A) max-LatestSeen rule,
// tx-OLD wins because its most-recent observation is freshest of all.
func TestGetChannels_ListRowUsesMaxLatestSeen_InMemory(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	tNow := now.Add(-1 * time.Second)        // freshest re-observation
	tOldFirst := now.Add(-2 * time.Hour)     // tx-OLD FirstSeen
	tMid := now.Add(-30 * time.Minute)       // tx-MID
	tRecentFS := now.Add(-5 * time.Minute)   // tx-RECENT-FS

	tOldFirstStr := tOldFirst.Format(time.RFC3339)
	tMidStr := tMid.Format(time.RFC3339)
	tRecentFSStr := tRecentFS.Format(time.RFC3339)

	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs1370', 'Obs1370', 'SJC', ?, '2026-01-01T00:00:00Z', 10)`, tOldFirstStr)

	// tx-OLD: heartbeat re-observed at T-NOW.
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('AA1370', 'hash_1370_old', ?, 1, 5,
			'{"type":"CHAN","channel":"#test","text":"BotA: old heartbeat re-observed","sender":"BotA"}', '#test')`, tOldFirstStr)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10.0, -90, '["aa"]', ?)`, tOldFirst.Unix())
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 11.0, -88, '["aa"]', ?)`, tNow.Unix())

	// tx-MID: single observation 30m ago.
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('BB1370', 'hash_1370_mid', ?, 1, 5,
			'{"type":"CHAN","channel":"#test","text":"BotB: mid message","sender":"BotB"}', '#test')`, tMidStr)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 1, 9.0, -91, '["bb"]', ?)`, tMid.Unix())

	// tx-RECENT-FS: single observation 5m ago — has the newest FirstSeen,
	// but its LatestSeen is older than tx-OLD's. This is what the buggy
	// max-FirstSeen code returns.
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('CC1370', 'hash_1370_recent_fs', ?, 1, 5,
			'{"type":"CHAN","channel":"#test","text":"UserC: recent first-seen, modest latest","sender":"UserC"}', '#test')`, tRecentFSStr)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (3, 1, 8.0, -92, '["cc"]', ?)`, tRecentFS.Unix())

	store := NewPacketStore(db, nil)
	store.Load()

	channels := store.GetChannels("")
	entry := findChannel(channels, "#test")
	if entry == nil {
		t.Fatalf("in-memory: #test channel missing from GetChannels result; got %+v", channels)
	}

	// messageCount must reflect all 3 transmissions.
	if mc, _ := entry["messageCount"].(int); mc != 3 {
		t.Errorf("in-memory: messageCount want 3, got %v", entry["messageCount"])
	}

	// lastMessage / lastSender MUST come from tx-OLD (max-LatestSeen winner).
	gotMsg, _ := entry["lastMessage"].(string)
	gotSender, _ := entry["lastSender"].(string)
	wantMsg := "old heartbeat re-observed"
	wantSender := "BotA"
	if gotMsg != wantMsg {
		t.Errorf("in-memory: lastMessage want %q, got %q (max-FirstSeen would yield 'recent first-seen, modest latest' — bug #1370)",
			wantMsg, gotMsg)
	}
	if gotSender != wantSender {
		t.Errorf("in-memory: lastSender want %q, got %q", wantSender, gotSender)
	}

	// lastActivity MUST be ~T-NOW (tx-OLD's LatestSeen), NOT T-5m (tx-RECENT-FS's FirstSeen).
	gotActivity, _ := entry["lastActivity"].(string)
	if gotActivity == "" {
		t.Fatalf("in-memory: empty lastActivity")
	}
	parsed, err := time.Parse(time.RFC3339, gotActivity)
	if err != nil {
		parsed, err = time.Parse("2006-01-02T15:04:05.000Z", gotActivity)
	}
	if err != nil {
		t.Fatalf("in-memory: lastActivity not parseable: %q (%v)", gotActivity, err)
	}
	delta := parsed.Unix() - tNow.Unix()
	if delta < -2 || delta > 2 {
		t.Errorf("in-memory: lastActivity want ~%s (tx-OLD LatestSeen), got %q (Δ=%ds — max-FirstSeen bug)",
			tNow.Format(time.RFC3339), gotActivity, delta)
	}

	// Mutation guards: tx-MID and tx-RECENT-FS must NOT be the rendered last message.
	if gotMsg == "recent first-seen, modest latest" {
		t.Errorf("in-memory: lastMessage == tx-RECENT-FS — this is the max-FirstSeen bug, fix #1370 reverted?")
	}
	if gotMsg == "mid message" {
		t.Errorf("in-memory: lastMessage == tx-MID — neither max-FirstSeen nor max-LatestSeen should pick this")
	}
}

// TestGetChannels_ListRowUsesMaxLatestSeen_DB mirrors the above on the
// DB.GetChannels path. Same fixture, same assertions. PR #1368's
// composite index idx_observations_tx_ts already exists in prod schema
// (and the test schema) and is what the fix's MAX(timestamp) subquery
// uses.
func TestGetChannels_ListRowUsesMaxLatestSeen_DB(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	tNow := now.Add(-1 * time.Second)
	tOldFirst := now.Add(-2 * time.Hour)
	tMid := now.Add(-30 * time.Minute)
	tRecentFS := now.Add(-5 * time.Minute)

	tOldFirstStr := tOldFirst.Format(time.RFC3339)
	tMidStr := tMid.Format(time.RFC3339)
	tRecentFSStr := tRecentFS.Format(time.RFC3339)

	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs1370db', 'Obs1370DB', 'SJC', ?, '2026-01-01T00:00:00Z', 10)`, tOldFirstStr)

	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('AA1370DB', 'hash_1370db_old', ?, 1, 5,
			'{"type":"CHAN","channel":"#testdb","text":"BotA: old heartbeat re-observed","sender":"BotA"}', '#testdb')`, tOldFirstStr)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10.0, -90, '["aa"]', ?)`, tOldFirst.Unix())
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 11.0, -88, '["aa"]', ?)`, tNow.Unix())

	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('BB1370DB', 'hash_1370db_mid', ?, 1, 5,
			'{"type":"CHAN","channel":"#testdb","text":"BotB: mid message","sender":"BotB"}', '#testdb')`, tMidStr)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 1, 9.0, -91, '["bb"]', ?)`, tMid.Unix())

	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('CC1370DB', 'hash_1370db_recent_fs', ?, 1, 5,
			'{"type":"CHAN","channel":"#testdb","text":"UserC: recent first-seen, modest latest","sender":"UserC"}', '#testdb')`, tRecentFSStr)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (3, 1, 8.0, -92, '["cc"]', ?)`, tRecentFS.Unix())

	channels, err := db.GetChannels()
	if err != nil {
		t.Fatalf("DB: GetChannels error: %v", err)
	}
	entry := findChannel(channels, "#testdb")
	if entry == nil {
		// Helpful dump for debugging if hash filter changes.
		buf, _ := json.Marshal(channels)
		t.Fatalf("DB: #testdb channel missing from GetChannels result; got %s", string(buf))
	}

	if mc, _ := entry["messageCount"].(int); mc != 3 {
		t.Errorf("DB: messageCount want 3, got %v", entry["messageCount"])
	}

	gotMsg, _ := entry["lastMessage"].(string)
	gotSender, _ := entry["lastSender"].(string)
	wantMsg := "old heartbeat re-observed"
	wantSender := "BotA"
	if gotMsg != wantMsg {
		t.Errorf("DB: lastMessage want %q, got %q (max-FirstSeen bug — #1370)", wantMsg, gotMsg)
	}
	if gotSender != wantSender {
		t.Errorf("DB: lastSender want %q, got %q", wantSender, gotSender)
	}

	gotActivity, _ := entry["lastActivity"].(string)
	if gotActivity == "" {
		t.Fatalf("DB: empty lastActivity")
	}
	// lastActivity from DB is a string max(...) over either first_seen
	// (current buggy behavior — RFC3339-ish string) or a unix epoch
	// derived from MAX(o.timestamp) (post-fix). Accept either, but
	// normalize to a unix epoch for the comparison.
	gotEpoch := parseLooseTimestamp(gotActivity)
	if gotEpoch == 0 {
		t.Fatalf("DB: lastActivity not parseable: %q", gotActivity)
	}
	delta := gotEpoch - tNow.Unix()
	if delta < -2 || delta > 2 {
		t.Errorf("DB: lastActivity want ~%s (tx-OLD LatestSeen, %d), got %q (epoch %d, Δ=%ds — max-FirstSeen bug)",
			tNow.Format(time.RFC3339), tNow.Unix(), gotActivity, gotEpoch, delta)
	}

	if gotMsg == "recent first-seen, modest latest" {
		t.Errorf("DB: lastMessage == tx-RECENT-FS — max-FirstSeen bug, fix #1370 reverted?")
	}
	if gotMsg == "mid message" {
		t.Errorf("DB: lastMessage == tx-MID — unexpected winner under either rule")
	}
}

// parseLooseTimestamp accepts RFC3339 strings, "2006-01-02T15:04:05Z"
// variants, plain unix-second integers, or stringified integers, and
// returns the unix epoch. Returns 0 on failure.
func parseLooseTimestamp(s string) int64 {
	if s == "" {
		return 0
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		// Heuristic: epoch seconds will be 10 digits in current era.
		// epoch ms would be 13. Accept seconds form only here.
		if n > 1_000_000_000 && n < 100_000_000_000 {
			if n > 10_000_000_000 {
				return n / 1000
			}
			return n
		}
	}
	for _, layout := range []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05.000Z07:00",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	// Last-ditch: try to extract a leading number.
	for i := 1; i <= len(s); i++ {
		if _, err := strconv.ParseInt(s[:i], 10, 64); err != nil {
			if i > 1 {
				n, _ := strconv.ParseInt(s[:i-1], 10, 64)
				if n > 1_000_000_000 {
					return n
				}
			}
			break
		}
	}
	return 0
}

// Tiny sanity helper: keeps fmt import live if other code paths drop it.
var _ = fmt.Sprintf
