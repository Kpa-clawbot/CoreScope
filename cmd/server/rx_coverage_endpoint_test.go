package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

func seedCoverageDB(t *testing.T) *DB {
	db := setupTestDBv2(t)
	mustExecDB(t, db, `CREATE TABLE client_receptions (
		id INTEGER PRIMARY KEY AUTOINCREMENT, rx_pubkey TEXT, heard_key TEXT, heard_keylen INTEGER,
		rssi INTEGER, snr REAL, lat REAL, lon REAL, pos_acc_m REAL, rx_at TEXT, ingested_at TEXT, src TEXT)`)
	mustExecDB(t, db, `CREATE TABLE client_observers (pubkey TEXT PRIMARY KEY, name TEXT, last_seen TEXT)`)
	return db
}

func TestQueryCoverageRowsByPrefixAndBBox(t *testing.T) {
	db := seedCoverageDB(t)
	mustExecDB(t, db, `INSERT INTO client_receptions (rx_pubkey,heard_key,heard_keylen,snr,lat,lon,rx_at,ingested_at,src)
		VALUES ('comp','aabbcc',3,-6,51.05,3.72,'t','t','rxlog')`)
	srv := &Server{db: db, cfg: &Config{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true}}}

	rows, err := srv.queryCoverageRows("aabbccddeeff00112233", bbox{MinLat: 50, MinLon: 3, MaxLat: 52, MaxLon: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row by prefix, got %d", len(rows))
	}
	rows, _ = srv.queryCoverageRows("aabbccddeeff00112233", bbox{MinLat: 0, MinLon: 0, MaxLat: 1, MaxLon: 1})
	if len(rows) != 0 {
		t.Fatalf("bbox filter failed, got %d", len(rows))
	}
}

func TestMobileRxStats(t *testing.T) {
	db := seedCoverageDB(t)
	mustExecDB(t, db, `INSERT INTO client_receptions (rx_pubkey,heard_key,heard_keylen,snr,lat,lon,rx_at,ingested_at,src) VALUES ('compA','aabbcc',3,-6,51.05,3.72,'t1','t','rxlog')`)
	mustExecDB(t, db, `INSERT INTO client_receptions (rx_pubkey,heard_key,heard_keylen,snr,lat,lon,rx_at,ingested_at,src) VALUES ('compB','aabbcc',3,-8,51.06,3.73,'t2','t','rxlog')`)
	mustExecDB(t, db, `INSERT INTO client_receptions (rx_pubkey,heard_key,heard_keylen,snr,lat,lon,rx_at,ingested_at,src) VALUES ('compA','ffeedd',3,-5,51.07,3.74,'t3','t','rxlog')`)
	srv := &Server{db: db, cfg: &Config{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true}}}
	c, cl := srv.mobileRxStats("aabbccddeeff00112233")
	if c != 2 || cl != 2 {
		t.Fatalf("got count=%d clients=%d, want 2/2", c, cl)
	}
}

func serveRxCoverage(srv *Server, path string) *httptest.ResponseRecorder {
	router := mux.NewRouter()
	router.HandleFunc("/api/nodes/{pubkey}/rx-coverage", srv.handleNodeRxCoverage).Methods("GET")
	req := httptest.NewRequest("GET", path, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

// nodePK is a full 64-hex pubkey whose 3-byte prefix is the seeded heard_key.
const nodePK = "aabbcc0000000000000000000000000000000000000000000000000000000000"

func TestRxCoverageEndpointGeoJSON(t *testing.T) {
	db := seedCoverageDB(t)
	mustExecDB(t, db, `INSERT INTO client_receptions (rx_pubkey,heard_key,heard_keylen,snr,lat,lon,rx_at,ingested_at,src)
		VALUES ('comp','aabbcc',3,-6,51.05,3.72,'t','t','rxlog')`)
	srv := &Server{db: db, cfg: &Config{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true}}}

	rr := serveRxCoverage(srv, "/api/nodes/"+nodePK+"/rx-coverage?bbox=50,3,52,4&z=12")
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var fc CoverageFeatureCollection
	if err := json.Unmarshal(rr.Body.Bytes(), &fc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if fc.Type != "FeatureCollection" || len(fc.Features) != 1 {
		t.Fatalf("unexpected fc: %+v", fc)
	}
	// #3: the per-node response carries the node-wide mobile totals (wired in
	// from mobileRxStats). One reception from one companion → 1/1.
	if fc.MobileReceptions != 1 || fc.MobileClients != 1 {
		t.Fatalf("want mobile_receptions=1 mobile_clients=1, got %d/%d", fc.MobileReceptions, fc.MobileClients)
	}
	if serveRxCoverage(srv, "/api/nodes/"+nodePK+"/rx-coverage").Code != 400 {
		t.Fatal("missing bbox should be 400")
	}
	// Non-hex pubkey is rejected up front (parity with handleNodeReach).
	if serveRxCoverage(srv, "/api/nodes/nothex/rx-coverage?bbox=50,3,52,4").Code != 400 {
		t.Fatal("non-hex pubkey should be 400")
	}
}

// TestNodeRxCoverageHidesBlacklistedAndHidden verifies #1727 r2 must-fix #1: the
// per-node coverage endpoint must 404 for blacklisted or hidden-prefix nodes, so
// their GPS hex bins / counts aren't retrievable at a pubkey the rest of the API
// hides — not just the node name.
func TestNodeRxCoverageHidesBlacklistedAndHidden(t *testing.T) {
	const hidPK = "ddee110000000000000000000000000000000000000000000000000000000000"
	db := seedCoverageDB(t)
	mustExecDB(t, db, `INSERT INTO client_receptions (rx_pubkey,heard_key,heard_keylen,snr,lat,lon,rx_at,ingested_at,src)
		VALUES ('comp','aabbcc',3,-6,51.05,3.72,'t','t','rxlog')`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key,name,role,last_seen,first_seen,advert_count) VALUES ('`+hidPK+`','🚫Secret','repeater','t','t',1)`)
	srv := &Server{db: db, cfg: &Config{
		ClientRxCoverage:   &ClientRxCoverageConfig{Enabled: true},
		NodeBlacklist:      []string{nodePK},
		HiddenNamePrefixes: []string{"🚫"},
	}}

	if code := serveRxCoverage(srv, "/api/nodes/"+nodePK+"/rx-coverage?bbox=50,3,52,4").Code; code != 404 {
		t.Fatalf("blacklisted node coverage should be 404, got %d", code)
	}
	if code := serveRxCoverage(srv, "/api/nodes/"+hidPK+"/rx-coverage?bbox=50,3,52,4").Code; code != 404 {
		t.Fatalf("hidden-prefix node coverage should be 404, got %d", code)
	}
}
