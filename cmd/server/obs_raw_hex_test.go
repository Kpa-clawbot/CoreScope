package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// Issue #1999: the packet-detail API returned the transmission's canonical
// frame for every observation, so the browser's hex view could not show the
// selected observation's actual bytes and could contradict the path_json shown
// beside it.
//
// The store drops observations.raw_hex on purpose (#881, ~98MB on a 1.7M
// observation store) on the assumption that one content hash means one frame.
// The firmware hashes payload and type independently of the relay path, so
// observations of a single transmission legitimately differ. Measured on a
// production DB: of the 3000 most recent transmissions, 1977 had more than one
// observation and 1844 of those held genuinely different frames, up to 51 for
// one packet.
//
// The fix reads them back on the detail path only, one query per request.

// seedDistinctFrames inserts one transmission and three observations whose
// stored frames differ in their path bytes, plus one with no stored frame at
// all so the canonical fallback stays covered. Returns the hash and the
// observation ids in insertion order.
func seedDistinctFrames(t *testing.T, db *DB, hash string) (string, []int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.conn.Exec(`INSERT INTO transmissions
		(raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('CANON0000', ?, ?, 1, 5, '{"type":"CHAN"}')`, hash, now); err != nil {
		t.Fatalf("insert transmission: %v", err)
	}
	var txID int
	if err := db.conn.QueryRow("SELECT id FROM transmissions WHERE hash = ?", hash).Scan(&txID); err != nil {
		t.Fatalf("lookup tx id: %v", err)
	}

	frames := []struct {
		pathJSON string
		rawHex   interface{} // nil means SQL NULL: no stored frame
	}{
		{`["AA","BB"]`, "FRAME2HOPS"},
		{`["AA","BB","CC"]`, "FRAME3HOPS"},
		{`["AA"]`, "FRAME1HOP"},
		{`["DD"]`, nil},
	}
	var ids []int
	for i, f := range frames {
		res, err := db.conn.Exec(`INSERT INTO observations
			(transmission_id, observer_idx, snr, rssi, path_json, timestamp, raw_hex)
			VALUES (?, 1, 5.0, -90, ?, ?, ?)`,
			txID, f.pathJSON, time.Now().Unix()-int64(i), f.rawHex)
		if err != nil {
			t.Fatalf("insert observation %d: %v", i, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("last insert id: %v", err)
		}
		ids = append(ids, int(id))
	}
	return hash, ids
}

func TestObservationRawHexForHash(t *testing.T) {
	db := setupTestDB(t)
	// Production sets this in detectSchema (db.go) when the column exists; the
	// test schema has the column but the helper does not run detection.
	db.hasObsRawHex = true

	hash, ids := seedDistinctFrames(t, db, "1999aaaabbbbcccc")

	got := db.ObservationRawHexForHash(hash)
	if len(got) != 3 {
		t.Fatalf("got %d frames, want 3 (the fourth observation stores none): %v", len(got), got)
	}
	want := map[int]string{ids[0]: "FRAME2HOPS", ids[1]: "FRAME3HOPS", ids[2]: "FRAME1HOP"}
	for id, wantHex := range want {
		if got[id] != wantHex {
			t.Errorf("observation %d: got %q, want %q", id, got[id], wantHex)
		}
	}
	// The observation with a NULL frame must be absent rather than empty, so
	// the caller falls back to the transmission's canonical bytes.
	if _, present := got[ids[3]]; present {
		t.Errorf("observation %d has no stored frame and must not appear in the map", ids[3])
	}

	// Unknown hash: no rows, no error, no panic.
	if got := db.ObservationRawHexForHash("0000000000000000"); len(got) != 0 {
		t.Errorf("unknown hash returned %d frames, want 0", len(got))
	}
}

// TestObservationRawHexForHashRespectsSchemaFlag pins the guard: on a schema
// without observations.raw_hex the query must not be attempted at all, because
// it would be a SQL error rather than an empty result.
func TestObservationRawHexForHashRespectsSchemaFlag(t *testing.T) {
	db := setupTestDB(t)
	db.hasObsRawHex = false
	hash, _ := seedDistinctFrames(t, db, "1999ddddeeeeffff")
	if got := db.ObservationRawHexForHash(hash); got != nil {
		t.Errorf("hasObsRawHex=false must return nil, got %v", got)
	}
}

// TestPacketDetailExposesPerObservationFrames is the regression the issue asks
// for: the detail response must give each observation its own bytes, and fall
// back to the transmission's only where none are stored.
func TestPacketDetailExposesPerObservationFrames(t *testing.T) {
	srv, router := setupTestServer(t)
	srv.db.hasObsRawHex = true

	// Inserted after store.Load(), so this transmission is DB-only and the
	// handler takes its DB-fallback path. Both paths converge on the same
	// backfill, and the DB path is the one that can be set up deterministically.
	hash, ids := seedDistinctFrames(t, srv.db, "1999112233445566")

	req := httptest.NewRequest("GET", "/api/packets/"+hash, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	obsList, _ := body["observations"].([]interface{})
	if len(obsList) != 4 {
		t.Fatalf("got %d observations, want 4", len(obsList))
	}

	byID := map[int]string{}
	for _, raw := range obsList {
		m, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("observation is not an object: %T", raw)
		}
		idF, ok := m["id"].(float64) // JSON numbers decode as float64
		if !ok {
			t.Fatalf("observation has no numeric id: %v", m["id"])
		}
		hx, _ := m["raw_hex"].(string)
		byID[int(idF)] = hx
	}

	for i, wantHex := range []string{"FRAME2HOPS", "FRAME3HOPS", "FRAME1HOP"} {
		if got := byID[ids[i]]; got != wantHex {
			t.Errorf("observation %d: raw_hex = %q, want its own frame %q", ids[i], got, wantHex)
		}
	}
	// The one without stored bytes gets the transmission's canonical frame. On
	// this path that is new: the DB observation query selects no raw_hex, so
	// before the fix the field was missing from those observations entirely.
	if got := byID[ids[3]]; got != "CANON0000" {
		t.Errorf("observation %d: raw_hex = %q, want the canonical fallback %q", ids[3], got, "CANON0000")
	}

	// The whole point: the frames are not all the same value.
	distinct := map[string]bool{}
	for _, hx := range byID {
		distinct[hx] = true
	}
	if len(distinct) < 3 {
		t.Errorf("only %d distinct frames across 4 observations (%v) — the canonical frame is still being repeated", len(distinct), byID)
	}
}

// TestPacketDetailExposesPerObservationFramesFromStore covers the path almost
// every real request takes: the transmission IS in the in-memory store.
//
// This is the case a DB-fallback-only test misses. enrichObsWithTx already puts
// the transmission's canonical frame into every observation map, so a backfill
// that skipped observations which "already have" raw_hex would skip all of them
// and leave the defect untouched on the main path. A stored frame has to win
// over that placeholder.
func TestPacketDetailExposesPerObservationFramesFromStore(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	db.hasObsRawHex = true

	// Seed BEFORE the store loads, so the store holds this transmission and the
	// handler never reaches its DB fallback.
	hash, ids := seedDistinctFrames(t, db, "1999aabbccdd0011")

	cfg := &Config{Port: 3000}
	srv := NewServer(db, cfg, NewHub())
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if !store.WaitIndexesReady(5 * time.Second) {
		t.Fatal("background indexes never became ready")
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	if got := srv.store.GetPacketByHash(hash); got == nil {
		t.Fatal("precondition failed: the store does not hold the seeded transmission")
	}

	req := httptest.NewRequest("GET", "/api/packets/"+hash, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	obsList, _ := body["observations"].([]interface{})
	if len(obsList) != 4 {
		t.Fatalf("got %d observations, want 4", len(obsList))
	}

	byID := map[int]string{}
	for _, raw := range obsList {
		m, _ := raw.(map[string]interface{})
		idF, ok := m["id"].(float64)
		if !ok {
			t.Fatalf("observation has no numeric id: %v", m["id"])
		}
		hx, _ := m["raw_hex"].(string)
		byID[int(idF)] = hx
	}

	for i, wantHex := range []string{"FRAME2HOPS", "FRAME3HOPS", "FRAME1HOP"} {
		if got := byID[ids[i]]; got != wantHex {
			t.Errorf("store path, observation %d: raw_hex = %q, want its own frame %q (a canonical value here means the backfill was skipped)", ids[i], got, wantHex)
		}
	}
	if got := byID[ids[3]]; got != "CANON0000" {
		t.Errorf("store path, observation %d: raw_hex = %q, want the canonical fallback %q", ids[3], got, "CANON0000")
	}
}
