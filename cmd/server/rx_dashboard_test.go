package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRequireClientRxCoverageNilSafe verifies the #4 fix: coverage routes are
// registered unconditionally, so a nil server cfg (or nil *Config receiver)
// must 404 rather than panic.
func TestRequireClientRxCoverageNilSafe(t *testing.T) {
	var nilCfg *Config
	if nilCfg.ClientRxCoverageEnabled() {
		t.Fatal("nil *Config must report disabled")
	}
	req := func(srv *Server) int {
		rr := httptest.NewRecorder()
		srv.handleRxCoverage(rr, httptest.NewRequest("GET", "/api/rx-coverage?bbox=50,3,52,4", nil))
		return rr.Code
	}
	if code := req(&Server{}); code != http.StatusNotFound { // cfg nil → would panic without the guard
		t.Fatalf("nil cfg: want 404, got %d", code)
	}
	if code := req(&Server{cfg: &Config{}}); code != http.StatusNotFound { // feature disabled
		t.Fatalf("disabled: want 404, got %d", code)
	}
}

func insRx(t *testing.T, db *DB, rx, hk, at string, lat, lon float64) {
	mustExecDB(t, db, fmt.Sprintf(
		`INSERT INTO client_receptions (rx_pubkey,heard_key,heard_keylen,snr,lat,lon,rx_at,ingested_at,src) VALUES ('%s','%s',3,-6,%f,%f,'%s','x','rxlog')`,
		rx, hk, lat, lon, at))
}

func TestQueryCoverageFiltered(t *testing.T) {
	db := seedCoverageDB(t)
	now := time.Now().UTC()
	recent := now.Format(time.RFC3339)
	old := now.AddDate(0, 0, -40).Format(time.RFC3339)
	insRx(t, db, "compa", "aabbcc", recent, 51.05, 3.72)
	insRx(t, db, "compb", "ffeedd", recent, 51.06, 3.73)
	insRx(t, db, "compa", "aabbcc", old, 51.05, 3.72)
	srv := &Server{db: db}
	bb := bbox{MinLat: 50, MinLon: 3, MaxLat: 52, MaxLon: 4}

	if rows, _ := srv.queryCoverageFiltered("", "", 7, bb); len(rows) != 2 {
		t.Fatalf("global 7d: want 2, got %d", len(rows))
	}
	if rows, _ := srv.queryCoverageFiltered("", "compa", 7, bb); len(rows) != 1 {
		t.Fatalf("observer compa 7d: want 1, got %d", len(rows))
	}
	if rows, _ := srv.queryCoverageFiltered("", "", 0, bb); len(rows) != 3 {
		t.Fatalf("global all-time: want 3, got %d", len(rows))
	}
}

func TestRxLeaderboard(t *testing.T) {
	db := seedCoverageDB(t)
	recent := time.Now().UTC().Format(time.RFC3339)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count) VALUES ('compa','MyCompanion','companion','t','t',1)`)
	// compc is NOT in nodes, but reported its name via client_observers (fallback).
	mustExecDB(t, db, `INSERT INTO client_observers (pubkey, name, last_seen) VALUES ('compc','MobOnly','t')`)
	for i := 0; i < 3; i++ {
		insRx(t, db, "compa", fmt.Sprintf("aabb%02d", i), recent, 51.05, 3.72)
	}
	insRx(t, db, "compc", "ddee00", recent, 51.05, 3.72)
	insRx(t, db, "compc", "ddee01", recent, 51.05, 3.72)
	insRx(t, db, "compb", "aabbcc", recent, 51.05, 3.72) // no name anywhere
	srv := &Server{db: db}

	obs, err := srv.rxLeaderboard(7, 10)
	if err != nil {
		t.Fatal(err)
	}
	byPk := map[string]LeaderObserver{}
	for _, o := range obs {
		byPk[o.Pubkey] = o
	}
	if byPk["compa"].Name != "MyCompanion" || byPk["compa"].Receptions != 3 {
		t.Fatalf("compa (nodes name): %+v", byPk["compa"])
	}
	if byPk["compc"].Name != "MobOnly" || byPk["compc"].Receptions != 2 {
		t.Fatalf("compc (client_observers fallback): %+v", byPk["compc"])
	}
	if byPk["compb"].Name != "" {
		t.Fatalf("compb should have no name: %+v", byPk["compb"])
	}
}

// TestBatchResolveHeardKeys verifies the N+1 fix: many heard_keys resolve in one
// batched call with the same unique/ambiguous/unknown/hidden semantics as the
// single-key path.
func TestBatchResolveHeardKeys(t *testing.T) {
	db := setupTestDBv2(t)
	mustExecDB(t, db, `INSERT INTO nodes (public_key,name,role) VALUES ('aabbccdd11223344','Alice','repeater')`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key,name,role) VALUES ('aabbcc99887766aa','Bob','repeater')`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key,name,role) VALUES ('ddee110000000000','🚫Hidden','repeater')`)
	srv := &Server{db: db, cfg: &Config{HiddenNamePrefixes: []string{"🚫"}}}

	got := srv.batchResolveHeardKeys([]string{"aabbccdd", "aabbcc", "ffff", "ddee11", "aabbccdd"})
	cases := map[string][2]string{
		"aabbccdd": {"aabbccdd11223344", "Alice"}, // unique
		"aabbcc":   {"aabbcc", ""},                // ambiguous (Alice + Bob)
		"ffff":     {"ffff", ""},                  // unknown
		"ddee11":   {"ddee11", ""},                // unique but hidden-prefix → not surfaced
	}
	for k, want := range cases {
		if got[k] != want {
			t.Errorf("batchResolveHeardKeys[%q] = %v, want %v", k, got[k], want)
		}
	}
}

// TestRxLeaderboardHidesBlacklistedAndHidden verifies #1727 r2 must-fix #2: the
// leaderboard must drop observer-blacklisted contributors and blank the name of
// node-blacklisted or hidden-prefix identities (pre-PR / post-blacklist rows).
func TestRxLeaderboardHidesBlacklistedAndHidden(t *testing.T) {
	db := seedCoverageDB(t)
	recent := time.Now().UTC().Format(time.RFC3339)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count) VALUES ('aa01','GoodGuy','companion','t','t',1)`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count) VALUES ('cc03','BadNode','companion','t','t',1)`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count) VALUES ('dd04','🚫Hidden','companion','t','t',1)`)
	insRx(t, db, "aa01", "aabb01", recent, 51.05, 3.72) // normal → kept with name
	insRx(t, db, "bb02", "aabb02", recent, 51.05, 3.72) // observer-blacklisted → dropped
	insRx(t, db, "cc03", "aabb03", recent, 51.05, 3.72) // node-blacklisted → name blanked
	insRx(t, db, "dd04", "aabb04", recent, 51.05, 3.72) // hidden prefix → name blanked
	srv := &Server{db: db, cfg: &Config{
		ObserverBlacklist:  []string{"bb02"},
		NodeBlacklist:      []string{"cc03"},
		HiddenNamePrefixes: []string{"🚫"},
	}}

	obs, err := srv.rxLeaderboard(7, 100)
	if err != nil {
		t.Fatal(err)
	}
	byPk := map[string]LeaderObserver{}
	for _, o := range obs {
		byPk[o.Pubkey] = o
	}
	if _, ok := byPk["bb02"]; ok {
		t.Fatalf("observer-blacklisted contributor must be dropped, got %+v", byPk["bb02"])
	}
	if byPk["aa01"].Name != "GoodGuy" {
		t.Fatalf("normal contributor name should be kept: %+v", byPk["aa01"])
	}
	if _, ok := byPk["cc03"]; !ok || byPk["cc03"].Name != "" {
		t.Fatalf("node-blacklisted contributor should remain with a blanked name: %+v", byPk["cc03"])
	}
	if _, ok := byPk["dd04"]; !ok || byPk["dd04"].Name != "" {
		t.Fatalf("hidden-prefix contributor should remain with a blanked name: %+v", byPk["dd04"])
	}
}

// TestRxLeaderboardLimitSurvivesBlacklistDrop verifies #1727 r2 must-fix #2: the
// SQL LIMIT runs before the Go-side observer-blacklist drop, so the leaderboard
// must over-fetch and still return `limit` non-blacklisted rows even when the
// top contributors are blacklisted (not limit-minus-dropped).
func TestRxLeaderboardLimitSurvivesBlacklistDrop(t *testing.T) {
	db := seedCoverageDB(t)
	recent := time.Now().UTC().Format(time.RFC3339)
	// Reception counts strictly descending so ORDER BY COUNT(*) DESC is deterministic:
	// the two blacklisted observers are the top two, then five good ones.
	counts := []struct {
		pk string
		n  int
	}{
		{"bk1", 10}, {"bk2", 9}, // observer-blacklisted (top of the board)
		{"g1", 8}, {"g2", 7}, {"g3", 6}, {"g4", 5}, {"g5", 4},
	}
	for _, c := range counts {
		for i := 0; i < c.n; i++ {
			insRx(t, db, c.pk, fmt.Sprintf("%s%04d", c.pk, i), recent, 51.05, 3.72)
		}
	}
	srv := &Server{db: db, cfg: &Config{ObserverBlacklist: []string{"bk1", "bk2"}}}

	obs, err := srv.rxLeaderboard(7, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 3 {
		t.Fatalf("expected exactly 3 rows after dropping 2 blacklisted from the top, got %d: %+v", len(obs), obs)
	}
	want := []string{"g1", "g2", "g3"}
	for i, o := range obs {
		if o.Pubkey == "bk1" || o.Pubkey == "bk2" {
			t.Fatalf("blacklisted observer %q leaked into the leaderboard", o.Pubkey)
		}
		if o.Pubkey != want[i] {
			t.Fatalf("row %d = %q, want %q (top-3 non-blacklisted by count)", i, o.Pubkey, want[i])
		}
	}
}

// TestRxLeaderboardFrontierScore verifies the leaderboard ranks by frontier-
// weighted cell coverage, not raw reception count: a roaming observer with FEWER
// receptions but MORE distinct cells outranks a stationary spammer, a cell only
// one observer covers weighs 1.0, and a cell shared by N observers weighs 1/N.
func TestRxLeaderboardFrontierScore(t *testing.T) {
	db := seedCoverageDB(t)
	recent := time.Now().UTC().Format(time.RFC3339)
	// "park": 5 receptions all at ONE spot → 1 cell (stationary spammer).
	for i := 0; i < 5; i++ {
		insRx(t, db, "park", fmt.Sprintf("pk%04d", i), recent, 51.05, 3.72)
	}
	// "roam": 3 receptions at 3 far-apart spots → 3 distinct cells. The first
	// coincides with park's cell, so that cell is shared by 2 observers.
	insRx(t, db, "roam", "rm0001", recent, 51.05, 3.72) // shared with park
	insRx(t, db, "roam", "rm0002", recent, 51.06, 3.72) // unique to roam
	insRx(t, db, "roam", "rm0003", recent, 51.07, 3.72) // unique to roam
	srv := &Server{db: db}

	obs, err := srv.rxLeaderboard(7, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 2 {
		t.Fatalf("want 2 observers, got %d: %+v", len(obs), obs)
	}
	// Roamer outranks the parked spammer despite fewer receptions.
	if obs[0].Pubkey != "roam" || obs[1].Pubkey != "park" {
		t.Fatalf("ranking: want [roam park], got [%s %s]", obs[0].Pubkey, obs[1].Pubkey)
	}
	byPk := map[string]LeaderObserver{}
	for _, o := range obs {
		byPk[o.Pubkey] = o
	}
	// park: 1 cell shared with roam → score 0.5; 5 receptions retained.
	if byPk["park"].Cells != 1 || byPk["park"].Receptions != 5 {
		t.Fatalf("park: %+v", byPk["park"])
	}
	if d := byPk["park"].Score - 0.5; d > 1e-9 || d < -1e-9 {
		t.Fatalf("park score: want 0.5, got %v", byPk["park"].Score)
	}
	// roam: shared cell (0.5) + 2 unique cells (1.0 each) = 2.5; 3 cells.
	if byPk["roam"].Cells != 3 {
		t.Fatalf("roam cells: want 3, got %d", byPk["roam"].Cells)
	}
	if d := byPk["roam"].Score - 2.5; d > 1e-9 || d < -1e-9 {
		t.Fatalf("roam score: want 2.5, got %v", byPk["roam"].Score)
	}
}
