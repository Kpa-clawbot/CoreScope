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
