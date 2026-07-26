package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestHandlePacketPath_TouchedAreas covers dborup's follow-up to the
// ping-bot reply's capped "touched" list: View Path has room to show every
// area the packet's points and observers fall in, not just the first few.
// Deduped and alphabetized, but uncapped -- unlike
// annotateBotReplyTouchedAreas's pong-reply version.
func TestHandlePacketPath_TouchedAreas(t *testing.T) {
	srv, router := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`DELETE FROM transmissions`); err != nil {
		t.Fatalf("clear transmissions: %v", err)
	}
	if _, err := srv.db.conn.Exec(`DELETE FROM observations`); err != nil {
		t.Fatalf("clear observations: %v", err)
	}

	f := func(v float64) *float64 { return &v }
	srv.cfg.Areas = map[string]AreaEntry{
		"AAR": {Label: "Aarhus by", LatMin: f(56.05), LatMax: f(56.25), LonMin: f(9.95), LonMax: f(10.35)},
		"ODE": {Label: "Odense by", LatMin: f(55.30), LatMax: f(55.50), LonMin: f(10.25), LonMax: f(10.45)},
	}

	// A relay hop positioned in Aarhus, an observer positioned in Odense --
	// touchedAreas must cover both, from a single 1-hop branch.
	if _, err := srv.db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, lat, lon, role) VALUES (?, ?, ?, ?, ?)",
		"pkaarhusrepeater", "AarhusRepeater", 56.15, 10.20, "repeater"); err != nil {
		t.Fatalf("insert repeater node: %v", err)
	}
	if _, err := srv.db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, lat, lon, role) VALUES (?, ?, ?, ?, ?)",
		"pkodenseobserver", "OdenseObserver", 55.40, 10.38, "client"); err != nil {
		t.Fatalf("insert observer node: %v", err)
	}
	srv.store.InvalidateNodeCache()

	txRes, err := srv.db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('AA', 'touchedpath00001', '2026-01-15T10:00:00Z', 1, 5,
		'{"type":"CHAN","channel":"#ping","text":"ping","sender":"Eve"}', '#ping')`)
	if err != nil {
		t.Fatalf("insert tx: %v", err)
	}
	txID, _ := txRes.LastInsertId()
	obsRes, err := srv.db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES (?,?,?)`,
		"pkodenseobserver", "OdenseObserver", "")
	if err != nil {
		t.Fatalf("insert observer: %v", err)
	}
	obsIdx, _ := obsRes.LastInsertId()
	if _, err := srv.db.conn.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, resolved_path, timestamp) VALUES (?,?,?,?,?,?,?)`,
		txID, obsIdx, 8.0, -85.0, `["aa"]`, `["pkaarhusrepeater"]`, 1736935200,
	); err != nil {
		t.Fatalf("insert observation: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/packets/touchedpath00001/path", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp PacketPathResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.TouchedAreas) != 2 || resp.TouchedAreas[0] != "Aarhus by" || resp.TouchedAreas[1] != "Odense by" {
		t.Errorf("TouchedAreas = %v, want [\"Aarhus by\" \"Odense by\"] (alphabetical, one from the relay hop, one from the observer)", resp.TouchedAreas)
	}
}

// TestHandlePacketPath_TouchedAreas_NoAreasConfigured confirms the field is
// simply omitted (never guessed) when no areas are configured.
func TestHandlePacketPath_TouchedAreas_NoAreasConfigured(t *testing.T) {
	srv, router := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`DELETE FROM transmissions`); err != nil {
		t.Fatalf("clear transmissions: %v", err)
	}
	if _, err := srv.db.conn.Exec(`DELETE FROM observations`); err != nil {
		t.Fatalf("clear observations: %v", err)
	}
	srv.cfg.Areas = nil

	if _, err := srv.db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, lat, lon, role) VALUES (?, ?, ?, ?, ?)",
		"pkaarhusrepeater", "AarhusRepeater", 56.15, 10.20, "repeater"); err != nil {
		t.Fatalf("insert repeater node: %v", err)
	}
	srv.store.InvalidateNodeCache()

	txRes, err := srv.db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('AA', 'touchedpath00002', '2026-01-15T10:00:00Z', 1, 5,
		'{"type":"CHAN","channel":"#ping","text":"ping","sender":"Eve"}', '#ping')`)
	if err != nil {
		t.Fatalf("insert tx: %v", err)
	}
	txID, _ := txRes.LastInsertId()
	obsRes, err := srv.db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES (?,?,?)`, "obsX", "ObsX", "")
	if err != nil {
		t.Fatalf("insert observer: %v", err)
	}
	obsIdx, _ := obsRes.LastInsertId()
	if _, err := srv.db.conn.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, resolved_path, timestamp) VALUES (?,?,?,?,?,?,?)`,
		txID, obsIdx, 8.0, -85.0, `["aa"]`, `["pkaarhusrepeater"]`, 1736935200,
	); err != nil {
		t.Fatalf("insert observation: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/packets/touchedpath00002/path", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := body["touchedAreas"]; present {
		t.Errorf("touchedAreas = %v, want absent (no areas configured)", body["touchedAreas"])
	}
}
