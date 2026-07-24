package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHandleChannelMessages_EntryPointArea covers dborup's request: a
// channel message needs BOTH the scope it was sent with (already shown as
// "Scope: #dk (Danmark (alle))") AND the area the sender was actually
// physically in, resolved from the message's own path[0] entry-point
// repeater -- distinct because someone in Aarhus can still send with the
// broad #dk scope. Also confirms the internal "entryPrefix" field used to
// carry path[0] through to the resolution step never reaches the client.
func TestHandleChannelMessages_EntryPointArea(t *testing.T) {
	srv, router := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`DELETE FROM transmissions`); err != nil {
		t.Fatalf("clear transmissions: %v", err)
	}
	if _, err := srv.db.conn.Exec(`DELETE FROM observations`); err != nil {
		t.Fatalf("clear observations: %v", err)
	}
	if !srv.db.hasScopeName {
		if _, err := srv.db.conn.Exec(`ALTER TABLE transmissions ADD COLUMN scope_name TEXT DEFAULT NULL`); err != nil {
			t.Fatalf("add scope_name column: %v", err)
		}
		srv.db.hasScopeName = true
	}

	f := func(v float64) *float64 { return &v }
	srv.cfg.Areas = map[string]AreaEntry{
		"AAR": {Label: "Aarhus by", LatMin: f(56.05), LatMax: f(56.25), LonMin: f(9.95), LonMax: f(10.35)},
	}

	// A repeater physically in Aarhus, resolvable only via a unique
	// full-length prefix match (mirrors TestResolveHopsAPI_UniquePrefix).
	if _, err := srv.db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, lat, lon, role) VALUES (?, ?, ?, ?, ?)",
		"aar0011223344", "AarhusRepeater", 56.1503, 10.1965, "repeater"); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	srv.store.InvalidateNodeCache()

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := srv.db.conn.Exec(
		`INSERT INTO transmissions (raw_hex,hash,first_seen,route_type,payload_type,channel_hash,decoded_json,scope_name) VALUES (?,?,?,0,5,'#dk',?,'#dk')`,
		"aa", "chmsg1", now, `{"sender":"AarhusSender","text":"AarhusSender: hey from #dk"}`,
	)
	if err != nil {
		t.Fatalf("insert tx: %v", err)
	}
	txID, _ := res.LastInsertId()

	obsRes, err := srv.db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES (?,?,?)`, "obsAAR", "AarhusObs", "AAR")
	if err != nil {
		t.Fatalf("insert observer: %v", err)
	}
	obsIdx, _ := obsRes.LastInsertId()
	if _, err := srv.db.conn.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp) VALUES (?,?,?,?,?,?)`,
		txID, obsIdx, 1.0, -90.0, `["aar0011223344"]`, time.Now().Unix(),
	); err != nil {
		t.Fatalf("insert observation: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/channels/%23dk/messages?limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	messages, _ := body["messages"].([]interface{})
	if len(messages) != 1 {
		t.Fatalf("messages = %+v, want 1", messages)
	}
	msg, _ := messages[0].(map[string]interface{})

	if msg["scope"] != "#dk" {
		t.Errorf("scope = %v, want #dk", msg["scope"])
	}
	if msg["area"] != "Aarhus by" {
		t.Errorf("area = %v, want \"Aarhus by\" (resolved from path[0], not the #dk scope)", msg["area"])
	}
	if _, present := msg["entryPrefix"]; present {
		t.Error("entryPrefix must never reach the client -- it's an internal-only intermediate field")
	}
}

// TestHandleChannelMessages_EntryPointArea_Unresolved confirms "area" is
// simply omitted (never guessed) when the entry-point prefix doesn't
// resolve unambiguously, or when no areas are configured at all.
func TestHandleChannelMessages_EntryPointArea_Unresolved(t *testing.T) {
	srv, router := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`DELETE FROM transmissions`); err != nil {
		t.Fatalf("clear transmissions: %v", err)
	}
	if _, err := srv.db.conn.Exec(`DELETE FROM observations`); err != nil {
		t.Fatalf("clear observations: %v", err)
	}
	if !srv.db.hasScopeName {
		if _, err := srv.db.conn.Exec(`ALTER TABLE transmissions ADD COLUMN scope_name TEXT DEFAULT NULL`); err != nil {
			t.Fatalf("add scope_name column: %v", err)
		}
		srv.db.hasScopeName = true
	}
	// No areas configured at all.
	srv.cfg.Areas = nil

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := srv.db.conn.Exec(
		`INSERT INTO transmissions (raw_hex,hash,first_seen,route_type,payload_type,channel_hash,decoded_json,scope_name) VALUES (?,?,?,0,5,'#dk',?,'#dk')`,
		"aa", "chmsg2", now, `{"sender":"Someone","text":"Someone: hi"}`,
	)
	if err != nil {
		t.Fatalf("insert tx: %v", err)
	}
	txID, _ := res.LastInsertId()
	obsRes, err := srv.db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES (?,?,?)`, "obsX", "ObsX", "XXX")
	if err != nil {
		t.Fatalf("insert observer: %v", err)
	}
	obsIdx, _ := obsRes.LastInsertId()
	if _, err := srv.db.conn.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp) VALUES (?,?,?,?,?,?)`,
		txID, obsIdx, 1.0, -90.0, `["deadbeef99"]`, time.Now().Unix(),
	); err != nil {
		t.Fatalf("insert observation: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/channels/%23dk/messages?limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	messages, _ := body["messages"].([]interface{})
	if len(messages) != 1 {
		t.Fatalf("messages = %+v, want 1", messages)
	}
	msg, _ := messages[0].(map[string]interface{})
	if _, present := msg["area"]; present {
		t.Errorf("area = %v, want absent (no areas configured)", msg["area"])
	}
	if _, present := msg["entryPrefix"]; present {
		t.Error("entryPrefix must never reach the client")
	}
}
