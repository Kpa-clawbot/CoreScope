package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// seedPingTrigger inserts a CHAN transmission plus its ping_triggers row --
// what the ingestor would have written for a real "ping" message.
func seedPingTrigger(t *testing.T, srv *Server, hash, channelHash, sender, firstSeen string) int64 {
	t.Helper()
	res, err := srv.db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('AABBCCDDEE', ?, ?, 1, 5, '{"type":"CHAN","channel":"'||?||'","text":"'||?||': ping","sender":"'||?||'"}', ?)`,
		hash, firstSeen, channelHash, sender, sender, channelHash)
	if err != nil {
		t.Fatalf("insert tx: %v", err)
	}
	txID, _ := res.LastInsertId()
	if _, err := srv.db.conn.Exec(`INSERT INTO ping_triggers (tx_id, hash, channel_hash, sender, first_seen) VALUES (?, ?, ?, ?, ?)`,
		txID, hash, channelHash, sender, firstSeen); err != nil {
		t.Fatalf("insert ping_trigger: %v", err)
	}
	return txID
}

func seedPingObservation(t *testing.T, srv *Server, txID int64, observerID string, snr float64, pathJSON, resolvedPath string, tsUnix int64) {
	t.Helper()
	var observerIdx int64
	if err := srv.db.conn.QueryRow(`SELECT rowid FROM observers WHERE id = ?`, observerID).Scan(&observerIdx); err != nil {
		t.Fatalf("lookup observer %s: %v", observerID, err)
	}
	if _, err := srv.db.conn.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, resolved_path, timestamp) VALUES (?,?,?,?,?,?,?)`,
		txID, observerIdx, snr, -90.0, pathJSON, resolvedPath, tsUnix,
	); err != nil {
		t.Fatalf("insert observation: %v", err)
	}
}

// setupPingScoresFixture seeds observers with known positions (via nodes
// row for lat/lon lookups the same way GetPacketPath resolves them) and
// returns the server (+ router, for handler-level tests) ready for
// computePingScore/computeAllPingScores.
func setupPingScoresFixture(t *testing.T) (*Server, *mux.Router) {
	t.Helper()
	srv, router := setupTestServer(t)
	// setupTestDB's shared schema predates the ping-score feature; create
	// ping_triggers locally here rather than touching the schema shared by
	// every other test in the package.
	if _, err := srv.db.conn.Exec(`CREATE TABLE IF NOT EXISTS ping_triggers (
		tx_id INTEGER PRIMARY KEY,
		hash TEXT NOT NULL,
		channel_hash TEXT,
		sender TEXT,
		first_seen TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create ping_triggers: %v", err)
	}
	obs := []struct {
		id, name, iata string
	}{
		{"pingobsa", "ObsA", "AAA"},
		{"pingobsb", "ObsB", "BBB"},
		{"pingobsc", "ObsC", "CCC"},
	}
	for _, o := range obs {
		if _, err := srv.db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES (?,?,?)`, o.id, o.name, o.iata); err != nil {
			t.Fatalf("insert observer %s: %v", o.id, err)
		}
	}
	// Distinct positions so DistanceFromFirstKm is real and non-trivial.
	positions := []struct {
		pubkey   string
		lat, lon float64
	}{
		{"pingobsa", 56.0, 10.0},   // first-hearer landmark for most scenarios
		{"pingobsb", 57.8, 12.6},   // ~230km from A
		{"pingobsc", 56.05, 10.05}, // ~6km from A
	}
	for _, p := range positions {
		if _, err := srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES (?,?,?,?)`, p.pubkey, p.pubkey, p.lat, p.lon); err != nil {
			t.Fatalf("insert node position for %s: %v", p.pubkey, err)
		}
	}
	return srv, router
}

// TestComputePingScore_Basic covers the core per-ping derivation: deepest
// (most hops) vs farthest (most distanceFromFirstKm) can be DIFFERENT
// branches, spread seconds is the max across all branches, and the relay
// pubkeys feeding the leaderboard are collected from every branch's hop
// chain, not just the deepest/farthest one.
func TestComputePingScore_Basic(t *testing.T) {
	srv, _ := setupPingScoresFixture(t)
	txID := seedPingTrigger(t, srv, "scorebasic00001", "#test", "Alice", "2026-01-15T10:00:00Z")

	// obs A: first hearer, 0 hops.
	seedPingObservation(t, srv, txID, "pingobsa", 9.0, `[]`, `[]`, 1736935200)
	// obs B: farther away (230km) but fewer hops (1) than C.
	seedPingObservation(t, srv, txID, "pingobsb", 6.0, `["aa"]`, `["pkrelay1"]`, 1736935210)
	// obs C: closer (6km) but MORE hops (2) -- the deepest branch.
	seedPingObservation(t, srv, txID, "pingobsc", 4.0, `["aa","bb"]`, `["pkrelay1","pkrelay2"]`, 1736935260)

	score := srv.computePingScore(pingTriggerRow{txID: txID, hash: "scorebasic00001", channelHash: "#test", sender: "Alice", firstSeen: "2026-01-15T10:00:00Z"})
	if score == nil {
		t.Fatal("computePingScore returned nil")
	}
	if score.StationCount != 3 {
		t.Errorf("StationCount = %d, want 3", score.StationCount)
	}
	if score.DeepestHops != 2 {
		t.Errorf("DeepestHops = %d, want 2 (obs C's branch)", score.DeepestHops)
	}
	if score.FarthestKm == nil {
		t.Fatal("FarthestKm is nil, want a value")
	}
	if *score.FarthestKm < 200 || *score.FarthestKm > 260 {
		t.Errorf("FarthestKm = %v, want ~230 (obs B's branch)", *score.FarthestKm)
	}
	if score.FarthestPubkey != "pingobsb" {
		t.Errorf("FarthestPubkey = %q, want pingobsb -- farthest and deepest must be independently attributed", score.FarthestPubkey)
	}
	if score.DeepestPubkey != "pingobsc" {
		t.Errorf("DeepestPubkey = %q, want pingobsc", score.DeepestPubkey)
	}
	if score.SpreadSeconds == nil || *score.SpreadSeconds < 59 || *score.SpreadSeconds > 61 {
		t.Errorf("SpreadSeconds = %v, want ~60 (obs C arrived 60s after obs A)", score.SpreadSeconds)
	}
	relaySet := map[string]bool{}
	for _, pk := range score.relayPubkeys {
		relaySet[pk] = true
	}
	if !relaySet["pkrelay1"] || !relaySet["pkrelay2"] {
		t.Errorf("relayPubkeys = %v, want both pkrelay1 and pkrelay2 collected across all branches", score.relayPubkeys)
	}
	if score.firstPubkey != "pingobsa" {
		t.Errorf("firstPubkey = %q, want pingobsa (earliest-arriving observation)", score.firstPubkey)
	}
}

// TestComputeAllPingScores_RecordSelection seeds several pings with
// deliberately distinct winning stats and asserts each record picks the
// right one -- including the fastest-full-spread record correctly
// excluding a lone-station ping (StationCount<2) that would otherwise
// trivially "win" with a 0-second spread.
func TestComputeAllPingScores_RecordSelection(t *testing.T) {
	srv, _ := setupPingScoresFixture(t)

	// Ping FAR: farthest (230km) and deepest (2 hops).
	txFar := seedPingTrigger(t, srv, "recordfar000001", "#test", "Alice", "2026-01-15T10:00:00Z")
	seedPingObservation(t, srv, txFar, "pingobsa", 9.0, `[]`, `[]`, 1736935200)
	seedPingObservation(t, srv, txFar, "pingobsb", 6.0, `["aa","bb"]`, `["pkrelay1","pkrelay2"]`, 1736935210)

	// Ping WIDE: 3 stations (more than any other ping here) -- wins widest spread.
	txWide := seedPingTrigger(t, srv, "recordwide00001", "#test", "Bob", "2026-01-15T11:00:00Z")
	seedPingObservation(t, srv, txWide, "pingobsa", 9.0, `[]`, `[]`, 1736938800)
	seedPingObservation(t, srv, txWide, "pingobsb", 6.0, `["aa"]`, `["pkrelay3"]`, 1736938850)
	seedPingObservation(t, srv, txWide, "pingobsc", 4.0, `["aa"]`, `["pkrelay3"]`, 1736938900)

	// Ping FAST: 2 stations, arrives within 1 second -- wins fastest spread.
	txFast := seedPingTrigger(t, srv, "recordfast00001", "#test", "Carol", "2026-01-15T12:00:00Z")
	seedPingObservation(t, srv, txFast, "pingobsa", 9.0, `[]`, `[]`, 1736942400)
	seedPingObservation(t, srv, txFast, "pingobsc", 6.0, `["aa"]`, `["pkrelay4"]`, 1736942401)

	// Ping LONE: single station, 0-second "spread" -- must NOT win fastest
	// spread despite the smallest possible SpreadSeconds, since there's
	// nothing to spread to.
	txLone := seedPingTrigger(t, srv, "recordlone00001", "#test", "Dave", "2026-01-15T13:00:00Z")
	seedPingObservation(t, srv, txLone, "pingobsa", 9.0, `[]`, `[]`, 1736946000)

	snap := srv.computeAllPingScores()
	if snap == nil {
		t.Fatal("computeAllPingScores returned nil")
	}
	if snap.TotalPings != 4 {
		t.Errorf("TotalPings = %d, want 4", snap.TotalPings)
	}
	if snap.FarthestPing == nil || snap.FarthestPing.Hash != "recordfar000001" {
		t.Errorf("FarthestPing = %+v, want recordfar000001", snap.FarthestPing)
	}
	if snap.MostHopsPing == nil || snap.MostHopsPing.Hash != "recordfar000001" {
		t.Errorf("MostHopsPing = %+v, want recordfar000001", snap.MostHopsPing)
	}
	if snap.WidestSpreadPing == nil || snap.WidestSpreadPing.Hash != "recordwide00001" {
		t.Errorf("WidestSpreadPing = %+v, want recordwide00001 (3 stations)", snap.WidestSpreadPing)
	}
	if snap.FastestSpreadPing == nil || snap.FastestSpreadPing.Hash != "recordfast00001" {
		t.Errorf("FastestSpreadPing = %+v, want recordfast00001, NOT the lone-station ping", snap.FastestSpreadPing)
	}
}

// TestComputeAllPingScores_Leaderboards verifies relay credit is deduped
// per-ping (a relay appearing in several branches of the SAME ping earns
// only one point for it) while still tallying correctly ACROSS pings, and
// that the observer leaderboard credits whoever was first each time.
func TestComputeAllPingScores_Leaderboards(t *testing.T) {
	srv, _ := setupPingScoresFixture(t)

	// Ping 1: pkrelay1 appears in BOTH branches -- must count once for this ping.
	tx1 := seedPingTrigger(t, srv, "lbtest0000001", "#test", "Alice", "2026-01-15T10:00:00Z")
	seedPingObservation(t, srv, tx1, "pingobsa", 9.0, `[]`, `[]`, 1736935200)
	seedPingObservation(t, srv, tx1, "pingobsb", 6.0, `["aa"]`, `["pkrelay1"]`, 1736935210)
	seedPingObservation(t, srv, tx1, "pingobsc", 4.0, `["aa"]`, `["pkrelay1"]`, 1736935220)

	// Ping 2: pkrelay1 appears again -- second distinct ping, should now be 2.
	tx2 := seedPingTrigger(t, srv, "lbtest0000002", "#test", "Bob", "2026-01-15T11:00:00Z")
	seedPingObservation(t, srv, tx2, "pingobsa", 9.0, `[]`, `[]`, 1736938800)
	seedPingObservation(t, srv, tx2, "pingobsb", 6.0, `["aa"]`, `["pkrelay1"]`, 1736938810)

	snap := srv.computeAllPingScores()
	if snap == nil {
		t.Fatal("computeAllPingScores returned nil")
	}

	var relay1Count int
	found := false
	for _, e := range snap.RelayLeaderboard {
		if e.Pubkey == "pkrelay1" {
			relay1Count = e.Count
			found = true
		}
	}
	if !found {
		t.Fatalf("pkrelay1 not found in RelayLeaderboard: %+v", snap.RelayLeaderboard)
	}
	if relay1Count != 2 {
		t.Errorf("pkrelay1 count = %d, want 2 (one per DISTINCT ping, not 3 for appearing in 3 branches total)", relay1Count)
	}

	var obsACount int
	for _, e := range snap.ObserverLeaderboard {
		if e.Pubkey == "pingobsa" {
			obsACount = e.Count
		}
	}
	if obsACount != 2 {
		t.Errorf("pingobsa first-hearer count = %d, want 2 (first on both pings)", obsACount)
	}
}

// TestHandlePingScores_EmptyState confirms the endpoint returns a
// well-formed 200 with zero-valued/omitted fields rather than an error
// when no ping has ever been recorded -- an ordinary state, not a failure.
func TestHandlePingScores_EmptyState(t *testing.T) {
	srv, router := setupTestServer(t)
	srv.pingScores.Store(&PingScoresSnapshot{GeneratedAt: time.Now().UTC().Format(time.RFC3339)})

	req := httptest.NewRequest("GET", "/api/ping-scores", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp PingScoresSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TotalPings != 0 {
		t.Errorf("TotalPings = %d, want 0", resp.TotalPings)
	}
	if resp.FarthestPing != nil {
		t.Errorf("FarthestPing = %+v, want nil/omitted with no ping history", resp.FarthestPing)
	}
}

// TestHandlePingScores_NilCache confirms the handler tolerates the cache
// never having been populated at all (e.g. a request racing the
// recomputer's first tick) rather than panicking on a nil pointer.
func TestHandlePingScores_NilCache(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/ping-scores", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp PingScoresSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.GeneratedAt == "" {
		t.Error("GeneratedAt should still be populated even when the cache was never warmed")
	}
}

// TestHandlePingScores_Populated exercises the full HTTP path end-to-end
// against real fixture data, confirming the JSON shape matches openapi.go
// (nested PingScore objects, leaderboards present).
func TestHandlePingScores_Populated(t *testing.T) {
	srv, router := setupPingScoresFixture(t)
	txID := seedPingTrigger(t, srv, "handlerpop00001", "#test", "Alice", "2026-01-15T10:00:00Z")
	seedPingObservation(t, srv, txID, "pingobsa", 9.0, `[]`, `[]`, 1736935200)
	seedPingObservation(t, srv, txID, "pingobsb", 6.0, `["aa"]`, `["pkrelay1"]`, 1736935210)

	srv.pingScores.Store(srv.computeAllPingScores())

	req := httptest.NewRequest("GET", "/api/ping-scores", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, present := raw["farthestPing"]; !present {
		t.Error("expected farthestPing in the JSON response")
	}
	if _, present := raw["relayLeaderboard"]; !present {
		t.Error("expected relayLeaderboard in the JSON response")
	}
}
