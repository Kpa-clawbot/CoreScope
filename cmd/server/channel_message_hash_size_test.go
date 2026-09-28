package main

import (
	"encoding/json"
	"testing"
	"time"
)

// The Channels view shows the path hash size (1-3 bytes) each message was
// sent with. /api/channels/{hash}/messages is served by the DB query when a DB
// is attached and by the in-memory store otherwise, so both must carry it.
// 0 = the packet does not encode one (see packetpath.HashSize).

var chHashSizeWant = map[string]float64{
	"dddddddddddddd01": 1, // flood, 1-byte, 2 hops
	"dddddddddddddd02": 2, // flood, 2-byte, heard direct (0 hops)
	"dddddddddddddd03": 3, // transport flood, 3-byte, 1 hop
	"dddddddddddddd04": 0, // direct zero-hop: no size encoded
}

func setupChannelHashSizeDB(t *testing.T) *DB {
	t.Helper()
	db := setupTestDB(t)
	if _, err := db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES ('obs1', 'Observer One', 'BRU')`); err != nil {
		t.Fatalf("insert observer: %v", err)
	}
	now := time.Now().UTC()
	rows := []struct {
		hash, rawHex string
		routeType    int
	}{
		{"dddddddddddddd01", "1502AABBDEADBEEF", 1},
		{"dddddddddddddd02", "1540DEADBEEF", 1},
		{"dddddddddddddd03", "141122334481AABBCCDEADBEEF", 0},
		{"dddddddddddddd04", "1600DEADBEEF", 2},
	}
	for i, r := range rows {
		ts := now.Add(time.Duration(i-len(rows)) * time.Minute)
		res, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
			VALUES (?, ?, ?, ?, 5, '{"type":"CHAN","channel":"#hashsize","text":"Alice: msg"}', '#hashsize')`,
			r.rawHex, r.hash, ts.Format(time.RFC3339), r.routeType)
		if err != nil {
			t.Fatalf("insert tx %s: %v", r.hash, err)
		}
		txID, _ := res.LastInsertId()
		if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
			VALUES (?, 1, 9.5, -90, '[]', ?)`, txID, ts.Unix()); err != nil {
			t.Fatalf("insert obs %s: %v", r.hash, err)
		}
	}
	return db
}

func assertChannelMessageHashSizes(t *testing.T, messages []map[string]interface{}) {
	t.Helper()
	if len(messages) != len(chHashSizeWant) {
		t.Fatalf("expected %d messages, got %d", len(chHashSizeWant), len(messages))
	}
	for _, m := range messages {
		// Compare on what the browser receives, not on the Go value type.
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded map[string]interface{}
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		h, _ := decoded["packetHash"].(string)
		want, known := chHashSizeWant[h]
		if !known {
			t.Errorf("unexpected packetHash %q", h)
			continue
		}
		got, present := decoded["hash_size"]
		if !present {
			t.Errorf("%s: hash_size key missing", h)
			continue
		}
		if got != want {
			t.Errorf("%s: hash_size = %#v, want %v", h, got, want)
		}
	}
}

func TestDBGetChannelMessagesCarriesHashSize(t *testing.T) {
	db := setupChannelHashSizeDB(t)
	defer db.Close()
	messages, _, err := db.GetChannelMessages("#hashsize", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertChannelMessageHashSizes(t, messages)
}

func TestStoreGetChannelMessagesCarriesHashSize(t *testing.T) {
	db := setupChannelHashSizeDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	messages, _ := store.GetChannelMessages("#hashsize", 100, 0)
	assertChannelMessageHashSizes(t, messages)
}
