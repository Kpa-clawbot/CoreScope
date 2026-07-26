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
	if len(resp.TouchedAreas) != 2 || resp.TouchedAreas[0].Label != "Aarhus by" || resp.TouchedAreas[1].Label != "Odense by" {
		t.Errorf("TouchedAreas = %+v, want [\"Aarhus by\" \"Odense by\"] (alphabetical, one from the relay hop, one from the observer)", resp.TouchedAreas)
	}
	aar := resp.TouchedAreas[0]
	if aar.LatMin == nil || *aar.LatMin != 56.05 || aar.LatMax == nil || *aar.LatMax != 56.25 {
		t.Errorf("Aarhus by bbox = %+v, want the configured LatMin/LatMax (56.05/56.25) -- this area was configured with a bounding box, not a polygon", aar)
	}
	if len(aar.Polygon) != 0 {
		t.Errorf("Aarhus by Polygon = %v, want empty -- this area has no polygon configured", aar.Polygon)
	}
}

// TestHandlePacketPath_TouchedAreas_Polygon covers an area configured with
// a real drawn polygon (geofilter-draft.js) rather than a bounding box --
// TouchedAreaShape must carry the polygon through so the map can shade the
// actual drawn shape, not fall back to a rectangle.
func TestHandlePacketPath_TouchedAreas_Polygon(t *testing.T) {
	srv, router := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`DELETE FROM transmissions`); err != nil {
		t.Fatalf("clear transmissions: %v", err)
	}
	if _, err := srv.db.conn.Exec(`DELETE FROM observations`); err != nil {
		t.Fatalf("clear observations: %v", err)
	}

	poly := [][2]float64{{56.05, 9.95}, {56.05, 10.35}, {56.25, 10.35}, {56.25, 9.95}}
	srv.cfg.Areas = map[string]AreaEntry{
		"AAR": {Label: "Aarhus by", Polygon: poly},
	}

	if _, err := srv.db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, lat, lon, role) VALUES (?, ?, ?, ?, ?)",
		"pkaarhusrepeater", "AarhusRepeater", 56.15, 10.20, "repeater"); err != nil {
		t.Fatalf("insert repeater node: %v", err)
	}
	srv.store.InvalidateNodeCache()

	txRes, err := srv.db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('AA', 'touchedpath00003', '2026-01-15T10:00:00Z', 1, 5,
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

	req := httptest.NewRequest("GET", "/api/packets/touchedpath00003/path", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp PacketPathResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.TouchedAreas) != 1 || resp.TouchedAreas[0].Label != "Aarhus by" {
		t.Fatalf("TouchedAreas = %+v, want [\"Aarhus by\"]", resp.TouchedAreas)
	}
	aar := resp.TouchedAreas[0]
	if len(aar.Polygon) != len(poly) {
		t.Fatalf("Aarhus by Polygon = %v, want %v", aar.Polygon, poly)
	}
	for i, pt := range poly {
		if aar.Polygon[i][0] != pt[0] || aar.Polygon[i][1] != pt[1] {
			t.Errorf("Aarhus by Polygon[%d] = %v, want %v", i, aar.Polygon[i], pt)
		}
	}
	if aar.LatMin != nil || aar.LatMax != nil {
		t.Errorf("Aarhus by LatMin/LatMax = %v/%v, want nil -- this area was configured with a polygon, not a bounding box", aar.LatMin, aar.LatMax)
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
