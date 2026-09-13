package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// #1851: the Channels view shows the region scope a message was sent under.
// transmissions.scope_name encodes three states (see StoreTx.ScopeName):
// SQL NULL = not transport-scoped, "" = transport-scoped but no configured
// region key matched, "#name" = matched region. /api/channels/{hash}/messages
// is served by the DB query when a DB is attached and by the in-memory store
// otherwise, and live messages arrive over the WebSocket broadcast, so all
// three paths must carry the same three states.

const (
	chScopeHashUnscoped = "cccccccccccccc01"
	chScopeHashUnknown  = "cccccccccccccc02"
	chScopeHashMatched  = "cccccccccccccc03"
)

var chScopeWant = map[string]interface{}{
	chScopeHashUnscoped: nil,
	chScopeHashUnknown:  "",
	chScopeHashMatched:  "#belgium",
}

// setupChannelScopeDB seeds one channel with three messages, one per scope
// state, each heard by one observation.
func setupChannelScopeDB(t *testing.T) *DB {
	t.Helper()
	db := setupTestDB(t)
	if _, err := db.conn.Exec(`ALTER TABLE transmissions ADD COLUMN scope_name TEXT DEFAULT NULL`); err != nil {
		t.Fatalf("add scope_name column: %v", err)
	}
	db.hasScopeName = true
	if _, err := db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES ('obs1', 'Observer One', 'BRU')`); err != nil {
		t.Fatalf("insert observer: %v", err)
	}
	now := time.Now().UTC()
	rows := []struct {
		hash      string
		routeType int
		scope     interface{}
		text      string
	}{
		{chScopeHashUnscoped, 1, nil, "Alice: plain flood"},
		{chScopeHashUnknown, 0, "", "Bob: unknown region"},
		{chScopeHashMatched, 0, "#belgium", "Carol: matched region"},
	}
	for i, r := range rows {
		ts := now.Add(time.Duration(i-3) * time.Minute)
		res, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash, scope_name)
			VALUES ('AABB', ?, ?, ?, 5, ?, '#scopetest', ?)`,
			r.hash, ts.Format(time.RFC3339), r.routeType,
			`{"type":"CHAN","channel":"#scopetest","text":"`+r.text+`"}`, r.scope)
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

func assertChannelMessageScopes(t *testing.T, messages []map[string]interface{}) {
	t.Helper()
	if len(messages) != len(chScopeWant) {
		t.Fatalf("expected %d messages, got %d", len(chScopeWant), len(messages))
	}
	for _, m := range messages {
		h, _ := m["packetHash"].(string)
		want, known := chScopeWant[h]
		if !known {
			t.Errorf("unexpected packetHash %q", h)
			continue
		}
		got, present := m["scope_name"]
		if !present {
			t.Errorf("%s: scope_name key missing", h)
			continue
		}
		if got != want {
			t.Errorf("%s: scope_name = %#v, want %#v", h, got, want)
		}
	}
}

func TestDBGetChannelMessagesCarriesScopeName(t *testing.T) {
	db := setupChannelScopeDB(t)
	defer db.Close()
	messages, _, err := db.GetChannelMessages("#scopetest", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertChannelMessageScopes(t, messages)
}

func TestStoreGetChannelMessagesCarriesScopeName(t *testing.T) {
	db := setupChannelScopeDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	messages, _ := store.GetChannelMessages("#scopetest", 100, 0)
	for i := range messages {
		// The store keeps the value as a *string; normalise through JSON so
		// the comparison is on what the browser receives.
		b, err := json.Marshal(messages[i])
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded map[string]interface{}
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		messages[i] = decoded
	}
	assertChannelMessageScopes(t, messages)
}

// A database the ingestor has not migrated yet has no scope_name column. The
// DB query must not select it (the query would fail) and every message reports
// null, the same as a non-transport-scoped message.
func TestDBGetChannelMessagesWithoutScopeNameColumn(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	messages, _, err := db.GetChannelMessages("#test", 100, 0)
	if err != nil {
		t.Fatalf("GetChannelMessages on a schema without scope_name: %v", err)
	}
	if len(messages) == 0 {
		t.Fatal("expected messages for #test")
	}
	for _, m := range messages {
		if got := m["scope_name"]; got != nil {
			t.Errorf("scope_name = %#v, want nil without the column", got)
		}
	}
}

func TestChannelMessagesEndpointExposesScopeName(t *testing.T) {
	db := setupChannelScopeDB(t)
	defer db.Close()
	for _, withDB := range []bool{true, false} {
		name := "db"
		if !withDB {
			name = "store"
		}
		t.Run(name, func(t *testing.T) {
			store := NewPacketStore(db, nil)
			if err := store.Load(); err != nil {
				t.Fatalf("store.Load: %v", err)
			}
			srv := NewServer(db, &Config{Port: 3000}, NewHub())
			srv.store = store
			if !withDB {
				srv.db = nil
			}
			router := mux.NewRouter()
			srv.RegisterRoutes(router)

			req := httptest.NewRequest("GET", "/api/channels/%23scopetest/messages?limit=50", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
			}
			var body struct {
				Messages []map[string]interface{} `json:"messages"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			assertChannelMessageScopes(t, body.Messages)
		})
	}
}

// Live messages in the Channels view are built from the WebSocket broadcast,
// not from the REST response, so both broadcast builders must carry scope_name
// or a new message shows no region until the next REST refresh replaces it.
func TestBroadcastMapsCarryScopeName(t *testing.T) {
	db := setupChannelScopeDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}

	assertBroadcast := func(t *testing.T, maps []map[string]interface{}, hash string, want interface{}) {
		t.Helper()
		found := false
		for _, bm := range maps {
			if bm["hash"] != hash {
				continue
			}
			found = true
			b, err := json.Marshal(bm)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var decoded map[string]interface{}
			if err := json.Unmarshal(b, &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got, ok := decoded["scope_name"]; !ok || got != want {
				t.Errorf("top-level scope_name = %#v (present=%v), want %#v", got, ok, want)
			}
			pkt, _ := decoded["packet"].(map[string]interface{})
			if got, ok := pkt["scope_name"]; !ok || got != want {
				t.Errorf("packet.scope_name = %#v (present=%v), want %#v", got, ok, want)
			}
		}
		if !found {
			t.Fatalf("no broadcast map for %s", hash)
		}
	}

	t.Run("IngestNewFromDB", func(t *testing.T) {
		txMax := store.MaxTransmissionID()
		now := time.Now().UTC()
		res, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash, scope_name)
			VALUES ('AABB', 'cccccccccccccc04', ?, 0, 5, '{"type":"CHAN","channel":"#scopetest","text":"Dave: live"}', '#scopetest', '#belgium')`,
			now.Format(time.RFC3339))
		if err != nil {
			t.Fatalf("insert tx: %v", err)
		}
		txID, _ := res.LastInsertId()
		if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
			VALUES (?, 1, 7.0, -95, '[]', ?)`, txID, now.Unix()); err != nil {
			t.Fatalf("insert obs: %v", err)
		}
		maps, _ := store.IngestNewFromDB(txMax, 100)
		assertBroadcast(t, maps, "cccccccccccccc04", "#belgium")
	})

	t.Run("IngestNewObservations", func(t *testing.T) {
		obsMax := db.GetMaxObservationID()
		var txID int
		if err := db.conn.QueryRow(`SELECT id FROM transmissions WHERE hash = ?`, chScopeHashUnknown).Scan(&txID); err != nil {
			t.Fatalf("lookup tx: %v", err)
		}
		if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
			VALUES (?, 1, 3.0, -110, '["aa"]', ?)`, txID, time.Now().Unix()); err != nil {
			t.Fatalf("insert obs: %v", err)
		}
		maps := store.IngestNewObservations(obsMax, 100)
		assertBroadcast(t, maps, chScopeHashUnknown, "")
	})
}
