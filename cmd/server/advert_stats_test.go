package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// insertAdvertTx seeds one advert transmission row - the single place that
// knows the INSERT column list, shared by every test in this file.
func insertAdvertTx(t *testing.T, db *DB, pubkey, hash string, rt int, ts time.Time) {
	t.Helper()
	if _, err := db.conn.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, from_pubkey) VALUES ('00', ?, ?, ?, ?, ?)`,
		hash, ts.Format("2006-01-02T15:04:05.000Z"), rt, payloadTypeAdvert, pubkey); err != nil {
		t.Fatalf("insert transmission: %v", err)
	}
}

func advertTS(hoursAgo float64) string {
	return time.Now().UTC().Add(-time.Duration(hoursAgo * float64(time.Hour))).Format("2006-01-02T15:04:05.000Z")
}

// Flood adverts inside the window count; zero-hop (DIRECT) and out-of-window
// ones do not; duplicate hashes collapse; broken timestamps are skipped.
func TestCountFloodAdverts(t *testing.T) {
	now := time.Now()
	entries := []floodAdvertEntry{
		{ts: advertTS(1), rt: advertRouteTypeFlood, hash: "a1"},
		{ts: advertTS(2), rt: advertRouteTypeFlood, hash: "a1"}, // dup hash: one advert, two rows
		{ts: advertTS(3), rt: advertRouteTypeFlood, hash: "a2"},
		{ts: advertTS(4), rt: 0, hash: "a3"},                         // zero-hop (DIRECT): excluded
		{ts: advertTS(9 * 24), rt: advertRouteTypeFlood, hash: "a4"}, // outside 7d window
		{ts: "not-a-time", rt: advertRouteTypeFlood, hash: "a5"},     // unparseable: skipped
		{ts: advertTS(5), rt: -1, hash: "a6"},                        // route type absent: excluded
	}
	if got := countFloodAdverts(entries, now, 7*24); got != 2 {
		t.Fatalf("want 2 flood adverts in window, got %d", got)
	}
	// Hash-less entries dedup by timestamp instead of collapsing into one.
	hashless := []floodAdvertEntry{
		{ts: advertTS(1), rt: advertRouteTypeFlood},
		{ts: advertTS(2), rt: advertRouteTypeFlood},
	}
	if got := countFloodAdverts(hashless, now, 7*24); got != 2 {
		t.Fatalf("want 2 hash-less flood adverts, got %d", got)
	}
}

// The wire contract: GET /api/nodes/{pubkey} carries flood_advert_count_7d,
// counting recent flood adverts only (zero-hop and out-of-window excluded).
func TestNodeDetailIncludesFloodAdvertCount(t *testing.T) {
	srv, router := setupTestServer(t)
	now := time.Now().UTC()
	ins := func(hash string, rt int, ts time.Time) {
		insertAdvertTx(t, srv.db, "aabbccdd11223344", hash, rt, ts)
	}
	// The shared fixture may already seed adverts for this node, so assert the
	// DELTA our inserts cause rather than an absolute count.
	fetch := func() float64 {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var body map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		node, ok := body["node"].(map[string]interface{})
		if !ok {
			t.Fatal("expected node object")
		}
		got, ok := node["flood_advert_count_7d"].(float64)
		if !ok {
			t.Fatalf("flood_advert_count_7d missing or not a number: %v", node["flood_advert_count_7d"])
		}
		return got
	}
	before := fetch()

	ins("fa1", advertRouteTypeFlood, now.Add(-2*time.Hour))
	ins("fa2", advertRouteTypeFlood, now.Add(-30*time.Hour))
	ins("za1", 0, now.Add(-1*time.Hour))                       // zero-hop: excluded
	ins("fa3", advertRouteTypeFlood, now.Add(-9*24*time.Hour)) // outside the 7d window
	// Inside the SQL date floor (window + 1d slack) but outside the exact 7d
	// window - only the Go-side check rejects this one.
	ins("fa4", advertRouteTypeFlood, now.Add(-time.Duration(7.5*24)*time.Hour))

	if got := fetch(); got != before+2 {
		t.Fatalf("want flood_advert_count_7d = %v+2, got %v", before, got)
	}
}

// The row cap saturates the count instead of failing: with a cap of 2, three
// qualifying flood adverts count as 2 (newest rows win via ORDER BY id DESC).
func TestCountFloodAdvertsForNode_RowCapSaturates(t *testing.T) {
	db := setupTestDB(t)
	now := time.Now().UTC()
	for i, h := range []string{"cap1", "cap2", "cap3"} {
		insertAdvertTx(t, db, "capnode11223344", h, advertRouteTypeFlood, now.Add(-time.Duration(i+1)*time.Hour))
	}
	n, err := db.CountFloodAdvertsForNode("capnode11223344", 7*24, 2)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("want saturated count 2, got %d", n)
	}
}
