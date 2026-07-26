package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHandleChannelMessages_BotReplyTouchedAreas covers dborup's follow-up
// request on the ping-bot reply: beyond the numeric "spread up to Nkm", show
// which named areas the packet actually touched, deduped across whichever
// hearing stations have their own GPS fix on file.
func TestHandleChannelMessages_BotReplyTouchedAreas(t *testing.T) {
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

	// Two observers inside Aarhus by (must dedupe to one label), one inside
	// Odense by.
	for _, n := range []struct {
		pk       string
		lat, lon float64
	}{
		{"pkaarhusobs1", 56.15, 10.20},
		{"pkaarhusobs2", 56.16, 10.21},
		{"pkodenseobs1", 55.40, 10.38},
	} {
		if _, err := srv.db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, lat, lon, role) VALUES (?, ?, ?, ?, ?)",
			n.pk, n.pk, n.lat, n.lon, "client"); err != nil {
			t.Fatalf("insert node %s: %v", n.pk, err)
		}
	}
	srv.store.InvalidateNodeCache()

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := srv.db.conn.Exec(
		`INSERT INTO transmissions (raw_hex,hash,first_seen,route_type,payload_type,channel_hash,decoded_json) VALUES (?,?,?,0,5,'#ping',?)`,
		"aa", "chmsgtouch1", now, `{"sender":"Alice","text":"Alice: ping"}`,
	)
	if err != nil {
		t.Fatalf("insert tx: %v", err)
	}
	txID, _ := res.LastInsertId()

	obsPKs := []string{"pkaarhusobs1", "pkaarhusobs2", "pkodenseobs1"}
	for i, pk := range obsPKs {
		obsRes, err := srv.db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES (?,?,?)`, pk, pk, "")
		if err != nil {
			t.Fatalf("insert observer %s: %v", pk, err)
		}
		obsIdx, _ := obsRes.LastInsertId()
		if _, err := srv.db.conn.Exec(
			`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp) VALUES (?,?,?,?,?,?)`,
			txID, obsIdx, 10.0, -80.0, `[]`, time.Now().Unix()+int64(i),
		); err != nil {
			t.Fatalf("insert observation %s: %v", pk, err)
		}
	}

	req := httptest.NewRequest("GET", "/api/channels/%23ping/messages?limit=10", nil)
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
	br, _ := msg["botReply"].(map[string]interface{})
	if br == nil {
		t.Fatal("expected a botReply on the ping message")
	}
	text, _ := br["text"].(string)
	if !strings.Contains(text, "touched Aarhus by, Odense by") {
		t.Errorf("botReply text = %q, want \"touched Aarhus by, Odense by\" (deduped, alphabetical)", text)
	}
	if _, present := br["touchedObserverPubkeys"]; present {
		t.Error("touchedObserverPubkeys must never reach the client -- it's an internal-only intermediate field")
	}
}

// TestHandleChannelMessages_BotReplyTouchedAreas_Capped covers the display
// cap: a packet touching more than botReplyMaxAreasShown distinct areas
// shows only the first few (alphabetically) plus a "+N more" count, rather
// than growing the chat bubble unboundedly.
func TestHandleChannelMessages_BotReplyTouchedAreas_Capped(t *testing.T) {
	srv, router := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`DELETE FROM transmissions`); err != nil {
		t.Fatalf("clear transmissions: %v", err)
	}
	if _, err := srv.db.conn.Exec(`DELETE FROM observations`); err != nil {
		t.Fatalf("clear observations: %v", err)
	}

	f := func(v float64) *float64 { return &v }
	srv.cfg.Areas = map[string]AreaEntry{
		"A1": {Label: "Area One", LatMin: f(56.00), LatMax: f(56.10), LonMin: f(10.00), LonMax: f(10.10)},
		"A2": {Label: "Area Two", LatMin: f(56.20), LatMax: f(56.30), LonMin: f(10.20), LonMax: f(10.30)},
		"A3": {Label: "Area Three", LatMin: f(56.40), LatMax: f(56.50), LonMin: f(10.40), LonMax: f(10.50)},
		"A4": {Label: "Area Four", LatMin: f(56.60), LatMax: f(56.70), LonMin: f(10.60), LonMax: f(10.70)},
	}

	for _, n := range []struct {
		pk       string
		lat, lon float64
	}{
		{"pkarea1", 56.05, 10.05},
		{"pkarea2", 56.25, 10.25},
		{"pkarea3", 56.45, 10.45},
		{"pkarea4", 56.65, 10.65},
	} {
		if _, err := srv.db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, lat, lon, role) VALUES (?, ?, ?, ?, ?)",
			n.pk, n.pk, n.lat, n.lon, "client"); err != nil {
			t.Fatalf("insert node %s: %v", n.pk, err)
		}
	}
	srv.store.InvalidateNodeCache()

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := srv.db.conn.Exec(
		`INSERT INTO transmissions (raw_hex,hash,first_seen,route_type,payload_type,channel_hash,decoded_json) VALUES (?,?,?,0,5,'#ping',?)`,
		"aa", "chmsgtouch2", now, `{"sender":"Bob","text":"Bob: ping"}`,
	)
	if err != nil {
		t.Fatalf("insert tx: %v", err)
	}
	txID, _ := res.LastInsertId()

	obsPKs := []string{"pkarea1", "pkarea2", "pkarea3", "pkarea4"}
	for i, pk := range obsPKs {
		obsRes, err := srv.db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES (?,?,?)`, pk, pk, "")
		if err != nil {
			t.Fatalf("insert observer %s: %v", pk, err)
		}
		obsIdx, _ := obsRes.LastInsertId()
		if _, err := srv.db.conn.Exec(
			`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp) VALUES (?,?,?,?,?,?)`,
			txID, obsIdx, 10.0, -80.0, `[]`, time.Now().Unix()+int64(i),
		); err != nil {
			t.Fatalf("insert observation %s: %v", pk, err)
		}
	}

	req := httptest.NewRequest("GET", "/api/channels/%23ping/messages?limit=10", nil)
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
	br, _ := msg["botReply"].(map[string]interface{})
	if br == nil {
		t.Fatal("expected a botReply on the ping message")
	}
	text, _ := br["text"].(string)
	want := "touched Area Four, Area One, Area Three +1 more"
	if !strings.Contains(text, want) {
		t.Errorf("botReply text = %q, want it to contain %q (capped to 3, alphabetical, remainder counted)", text, want)
	}
}
