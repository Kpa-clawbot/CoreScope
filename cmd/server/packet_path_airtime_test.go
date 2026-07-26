package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/meshcore-analyzer/lora"
)

// TestHandlePacketPath_Airtime covers dborup's "real airtime" follow-up:
// View Path's estimated LoRa Time-on-Air x distinct-relay-count for the
// packet's whole flood, sourced from the in-memory PacketStore (same
// formula as the Relay Airtime Share analytics metric, issue #1768) via
// the transmission ID GetPacketPath captures. Two observations record
// PARTIALLY overlapping resolved_path relay sets -- the union (3 distinct
// pubkeys) is what should feed the estimate, not either path alone.
func TestHandlePacketPath_Airtime(t *testing.T) {
	srv, router := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`DELETE FROM transmissions`); err != nil {
		t.Fatalf("clear transmissions: %v", err)
	}
	if _, err := srv.db.conn.Exec(`DELETE FROM observations`); err != nil {
		t.Fatalf("clear observations: %v", err)
	}

	txRes, err := srv.db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('AABBCCDDEE', 'airtimepath00001', '2026-01-15T10:00:00Z', 1, 5,
		'{"type":"CHAN","channel":"#ping","text":"ping","sender":"Eve"}', '#ping')`)
	if err != nil {
		t.Fatalf("insert tx: %v", err)
	}
	txID, _ := txRes.LastInsertId()

	obs1Res, err := srv.db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES (?,?,?)`, "airtimeobs1", "ObsOne", "SJC")
	if err != nil {
		t.Fatalf("insert observer 1: %v", err)
	}
	obs1Idx, _ := obs1Res.LastInsertId()
	obs2Res, err := srv.db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES (?,?,?)`, "airtimeobs2", "ObsTwo", "SFO")
	if err != nil {
		t.Fatalf("insert observer 2: %v", err)
	}
	obs2Idx, _ := obs2Res.LastInsertId()

	// obs1 heard it via repeaterA -> repeaterB; obs2 via repeaterB ->
	// repeaterC. Union across both observations of this ONE transmission
	// is {A, B, C} = 3 distinct relays -- B must not be double-counted.
	if _, err := srv.db.conn.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, resolved_path, timestamp) VALUES (?,?,?,?,?,?,?)`,
		txID, obs1Idx, 9.0, -88.0, `["aa","bb"]`, `["pkrepeaterA","pkrepeaterB"]`, 1736935200,
	); err != nil {
		t.Fatalf("insert observation 1: %v", err)
	}
	if _, err := srv.db.conn.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, resolved_path, timestamp) VALUES (?,?,?,?,?,?,?)`,
		txID, obs2Idx, 6.0, -95.0, `["aa","bb","cc"]`, `["pkrepeaterB","pkrepeaterC"]`, 1736935260,
	); err != nil {
		t.Fatalf("insert observation 2: %v", err)
	}

	// The rows above were inserted directly via SQL after setupTestServer
	// already called store.Load() once -- the in-memory store (which
	// AirtimeAndRelayCountForTransmission reads from) won't see them
	// until reloaded, same as a real cold restart picking up DB state.
	if err := srv.store.Load(); err != nil {
		t.Fatalf("reload store: %v", err)
	}
	if !srv.store.WaitIndexesReady(5 * time.Second) {
		t.Fatal("background indexes never became ready after reload")
	}

	req := httptest.NewRequest("GET", "/api/packets/airtimepath00001/path", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp PacketPathResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.AirtimeRelayCount != 3 {
		t.Errorf("AirtimeRelayCount = %d, want 3 (union of repeaterA/B/C across both observations, no double-count)", resp.AirtimeRelayCount)
	}
	if resp.EstimatedAirtimeMs == nil {
		t.Fatal("EstimatedAirtimeMs = nil, want a value -- the store has this transmission and its resolved-path relays")
	}
	preset := srv.store.resolveLoRaPreset()
	wantMs := float64(lora.TimeOnAir(5, preset).Microseconds()) / 1000.0 * 3 // raw_hex is 10 hex chars = 5 bytes, x3 relays
	if diff := *resp.EstimatedAirtimeMs - wantMs; diff > 0.01 || diff < -0.01 {
		t.Errorf("EstimatedAirtimeMs = %v, want %v (5-byte payload x 3 relays' ToA)", *resp.EstimatedAirtimeMs, wantMs)
	}

	// touchedpath00001-style raw JSON round-trip check: the internal-only
	// TxID field must never leak, and estimatedAirtimeMs/airtimeRelayCount
	// must use the documented JSON names.
	var raw map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, present := raw["TxID"]; present {
		t.Error("TxID must never reach the client -- it's an internal-only field")
	}
	if _, present := raw["estimatedAirtimeMs"]; !present {
		t.Error("expected estimatedAirtimeMs in the JSON response")
	}
	if _, present := raw["airtimeRelayCount"]; !present {
		t.Error("expected airtimeRelayCount in the JSON response")
	}
}

// TestHandlePacketPath_Airtime_ZeroRelays confirms the fields are omitted
// -- not a bare "estimatedAirtimeMs":0 with no accompanying relay count --
// for a directly-received packet (no resolved_path relays at all). This
// caught a real bug on stg: the *float64 EstimatedAirtimeMs survived JSON
// encoding as 0 (a non-nil pointer isn't "empty"), while the plain int
// AirtimeRelayCount's omitempty dropped it at exactly 0, leaving the
// frontend a number with nothing to pair it with.
func TestHandlePacketPath_Airtime_ZeroRelays(t *testing.T) {
	srv, router := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`DELETE FROM transmissions`); err != nil {
		t.Fatalf("clear transmissions: %v", err)
	}
	if _, err := srv.db.conn.Exec(`DELETE FROM observations`); err != nil {
		t.Fatalf("clear observations: %v", err)
	}

	txRes, err := srv.db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('AABBCCDDEE', 'airtimepath00003', '2026-01-15T10:00:00Z', 1, 5,
		'{"type":"CHAN","channel":"#ping","text":"ping","sender":"Eve"}', '#ping')`)
	if err != nil {
		t.Fatalf("insert tx: %v", err)
	}
	txID, _ := txRes.LastInsertId()
	obsRes, err := srv.db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES (?,?,?)`, "airtimeobs1", "ObsOne", "SJC")
	if err != nil {
		t.Fatalf("insert observer: %v", err)
	}
	obsIdx, _ := obsRes.LastInsertId()
	if _, err := srv.db.conn.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, resolved_path, timestamp) VALUES (?,?,?,?,?,?,?)`,
		txID, obsIdx, 9.0, -88.0, `[]`, `[]`, 1736935200,
	); err != nil {
		t.Fatalf("insert observation: %v", err)
	}
	if err := srv.store.Load(); err != nil {
		t.Fatalf("reload store: %v", err)
	}
	if !srv.store.WaitIndexesReady(5 * time.Second) {
		t.Fatal("background indexes never became ready after reload")
	}

	req := httptest.NewRequest("GET", "/api/packets/airtimepath00003/path", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := raw["estimatedAirtimeMs"]; present {
		t.Errorf("estimatedAirtimeMs = %v, want absent for a direct reception with 0 relays", raw["estimatedAirtimeMs"])
	}
	if _, present := raw["airtimeRelayCount"]; present {
		t.Errorf("airtimeRelayCount = %v, want absent for a direct reception with 0 relays", raw["airtimeRelayCount"])
	}
}

// TestHandlePacketPath_Airtime_StoreUnavailable confirms the field is
// simply omitted -- not a guessed zero -- when the in-memory store has no
// record of this transmission's ID (e.g. DB-only mode, or an old packet
// evicted from the memory-bounded store).
func TestHandlePacketPath_Airtime_StoreUnavailable(t *testing.T) {
	srv, router := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`DELETE FROM transmissions`); err != nil {
		t.Fatalf("clear transmissions: %v", err)
	}
	if _, err := srv.db.conn.Exec(`DELETE FROM observations`); err != nil {
		t.Fatalf("clear observations: %v", err)
	}

	txRes, err := srv.db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('AABB', 'airtimepath00002', '2026-01-15T10:00:00Z', 1, 5,
		'{"type":"CHAN","channel":"#ping","text":"ping","sender":"Eve"}', '#ping')`)
	if err != nil {
		t.Fatalf("insert tx: %v", err)
	}
	txID, _ := txRes.LastInsertId()
	obsRes, err := srv.db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES (?,?,?)`, "airtimeobs1", "ObsOne", "SJC")
	if err != nil {
		t.Fatalf("insert observer: %v", err)
	}
	obsIdx, _ := obsRes.LastInsertId()
	if _, err := srv.db.conn.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, resolved_path, timestamp) VALUES (?,?,?,?,?,?,?)`,
		txID, obsIdx, 9.0, -88.0, `["aa"]`, `["pkrepeaterA"]`, 1736935200,
	); err != nil {
		t.Fatalf("insert observation: %v", err)
	}
	// Deliberately no srv.store.Load() reload -- the store never learns
	// about this transmission, matching an evicted/never-loaded packet.

	req := httptest.NewRequest("GET", "/api/packets/airtimepath00002/path", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := raw["estimatedAirtimeMs"]; present {
		t.Errorf("estimatedAirtimeMs = %v, want absent (transmission not in the in-memory store)", raw["estimatedAirtimeMs"])
	}
	if _, present := raw["airtimeRelayCount"]; present {
		t.Errorf("airtimeRelayCount = %v, want absent", raw["airtimeRelayCount"])
	}
}
