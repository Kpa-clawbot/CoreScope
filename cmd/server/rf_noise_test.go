package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestAggregateRfNoiseExcludesStationary verifies the stationary-sample
// decision documented on aggregateRfNoise: a parked companion can log
// hundreds of samples at one GPS point, and those must not enter the
// aggregate at all (not even down-weighted) — otherwise the parked run
// would dominate the cell's reported noise floor.
func TestAggregateRfNoiseExcludesStationary(t *testing.T) {
	rows := []rfNoiseRow{
		// One real drive-by sample.
		{Lat: 51.05, Lon: 3.72, NoiseFloor: -110, Stationary: false},
	}
	// A parked companion logging 300 samples at a much noisier value — if
	// these leaked into the aggregate, the median would swing hard toward -90.
	for i := 0; i < 300; i++ {
		rows = append(rows, rfNoiseRow{Lat: 51.05, Lon: 3.72, NoiseFloor: -90, Stationary: true})
	}
	fc := aggregateRfNoise(rows, 9)
	if len(fc.Features) != 1 {
		t.Fatalf("expected 1 cell, got %d", len(fc.Features))
	}
	p := fc.Features[0].Properties
	if p.Count != 1 {
		t.Fatalf("stationary rows leaked into the aggregate: count=%d, want 1", p.Count)
	}
	if p.MedianNoiseFloor != -110 || p.QuietestNoiseFloor != -110 || p.NoisiestNoiseFloor != -110 {
		t.Fatalf("stationary rows skewed the stats: %+v", p)
	}
}

// TestAggregateRfNoiseMedianAndExtremes verifies the per-cell summary stats
// on a mix of mobile samples: count, median (average of the two middle
// values for an even count), quietest (min) and noisiest (max).
func TestAggregateRfNoiseMedianAndExtremes(t *testing.T) {
	rows := []rfNoiseRow{
		{Lat: 51.05, Lon: 3.72, NoiseFloor: -116},
		{Lat: 51.05, Lon: 3.72, NoiseFloor: -110},
		{Lat: 51.05, Lon: 3.72, NoiseFloor: -108},
		{Lat: 51.05, Lon: 3.72, NoiseFloor: -103},
	}
	fc := aggregateRfNoise(rows, 9)
	if len(fc.Features) != 1 {
		t.Fatalf("expected 1 cell, got %d", len(fc.Features))
	}
	p := fc.Features[0].Properties
	if p.Count != 4 {
		t.Fatalf("count = %d, want 4", p.Count)
	}
	if p.MedianNoiseFloor != -109 { // avg(-110, -108)
		t.Fatalf("median = %v, want -109", p.MedianNoiseFloor)
	}
	if p.QuietestNoiseFloor != -116 {
		t.Fatalf("quietest = %d, want -116", p.QuietestNoiseFloor)
	}
	if p.NoisiestNoiseFloor != -103 {
		t.Fatalf("noisiest = %d, want -103", p.NoisiestNoiseFloor)
	}
}

// TestAggregateRfNoiseCapsFeatures verifies the rfNoiseFeatureCap truncation
// (mirrors #12 on the coverage aggregator): a query spanning more than
// rfNoiseFeatureCap cells is bounded, with Truncated set, and a smaller
// query is not truncated.
func TestAggregateRfNoiseCapsFeatures(t *testing.T) {
	rows := make([]rfNoiseRow, 0, rfNoiseFeatureCap+200)
	side := 75 // 75*75 = 5625 > 5000
	for i := 0; i < side*side; i++ {
		lat := 10.0 + float64(i/side)*0.1
		lon := 10.0 + float64(i%side)*0.1
		rows = append(rows, rfNoiseRow{Lat: lat, Lon: lon, NoiseFloor: -110})
	}
	// Seed one cell with a much higher sample count than the rest (#5): every
	// other cell above has count == 1, so a cap comparator that only exercises
	// the cell-id tie-break (or is inverted to keep the sparsest cells) would
	// still pass without this. This cell — the busiest by far — must be the
	// one the cap keeps, not the one it cuts.
	hotLat, hotLon := 10.0, 10.0
	for i := 0; i < 50; i++ {
		rows = append(rows, rfNoiseRow{Lat: hotLat, Lon: hotLon, NoiseFloor: -110})
	}
	fc := aggregateRfNoise(rows, 9)
	if len(fc.Features) != rfNoiseFeatureCap || !fc.Truncated {
		t.Fatalf("want %d features + truncated, got %d truncated=%v", rfNoiseFeatureCap, len(fc.Features), fc.Truncated)
	}
	for i := 1; i < len(fc.Features); i++ {
		if fc.Features[i-1].Properties.Cell > fc.Features[i].Properties.Cell {
			t.Fatalf("truncated features not sorted by cell at %d", i)
		}
	}
	hotCell := hexCellAt(hotLat, hotLon, 9)
	found := false
	for _, f := range fc.Features {
		if f.Properties.Cell == hotCell {
			found = true
			if f.Properties.Count < 51 {
				t.Fatalf("hot cell count = %d, want >= 51", f.Properties.Count)
			}
		}
	}
	if !found {
		t.Fatalf("the densest cell was dropped by the cap; want the densest cells kept")
	}
	small := aggregateRfNoise(rows[:10], 9)
	if small.Truncated {
		t.Fatalf("small query should not be truncated")
	}
}

// TestAggregateRfNoiseEmpty verifies an empty input produces an empty (not
// nil) feature collection, matching aggregateCoverage's shape.
func TestAggregateRfNoiseEmpty(t *testing.T) {
	fc := aggregateRfNoise(nil, 9)
	if fc.Type != "FeatureCollection" || fc.Features == nil || len(fc.Features) != 0 {
		t.Fatalf("unexpected empty aggregate: %+v", fc)
	}
	if fc.Truncated {
		t.Fatalf("empty input should not be truncated")
	}
}

// --- Gate: /api/rf-noise and clientRfSamples in /api/config/client ---

func rfNoiseGateTestServer(t *testing.T, rf *ClientRfSamplesConfig) *mux.Router {
	t.Helper()
	db := setupTestDB(t)
	cfg := &Config{Port: 3000, ClientRfSamples: rf}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	return router
}

func TestRfNoiseRouteGatedOff(t *testing.T) {
	router := rfNoiseGateTestServer(t, nil)

	req := httptest.NewRequest("GET", "/api/rf-noise", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != 404 {
		t.Fatalf("expected 404 for /api/rf-noise when disabled, got %d", rr.Code)
	}

	creq := httptest.NewRequest("GET", "/api/config/client", nil)
	crr := httptest.NewRecorder()
	router.ServeHTTP(crr, creq)
	if crr.Code != 200 {
		t.Fatalf("config/client status %d body %s", crr.Code, crr.Body.String())
	}
	if !strings.Contains(crr.Body.String(), `"clientRfSamples":false`) {
		t.Fatalf("expected clientRfSamples:false in config body, got %s", crr.Body.String())
	}
}

func TestRfNoiseRouteGatedOn(t *testing.T) {
	router := rfNoiseGateTestServer(t, &ClientRfSamplesConfig{Enabled: true})

	req := httptest.NewRequest("GET", "/api/rf-noise", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code == 404 {
		t.Fatalf("expected /api/rf-noise to be registered when enabled, got 404")
	}

	creq := httptest.NewRequest("GET", "/api/config/client", nil)
	crr := httptest.NewRecorder()
	router.ServeHTTP(crr, creq)
	if crr.Code != 200 {
		t.Fatalf("config/client status %d body %s", crr.Code, crr.Body.String())
	}
	if !strings.Contains(crr.Body.String(), `"clientRfSamples":true`) {
		t.Fatalf("expected clientRfSamples:true in config body, got %s", crr.Body.String())
	}
}

// --- End-to-end: query + aggregation through the handler ---

func seedRfNoiseDB(t *testing.T) *DB {
	db := setupTestDBv2(t)
	mustExecDB(t, db, `CREATE TABLE client_rf_samples (
		id INTEGER PRIMARY KEY AUTOINCREMENT, rx_pubkey TEXT, sampled_at TEXT, ingested_at TEXT,
		lat REAL, lon REAL, pos_acc_m REAL, stationary INTEGER NOT NULL DEFAULT 0,
		uptime_secs INTEGER, battery_mv INTEGER, queue_len INTEGER, errors INTEGER,
		noise_floor INTEGER, last_rssi INTEGER, last_snr REAL, tx_air_secs INTEGER,
		rx_air_secs INTEGER, recv INTEGER, sent INTEGER, flood_rx INTEGER, direct_rx INTEGER,
		flood_tx INTEGER, direct_tx INTEGER, recv_errors INTEGER)`)
	return db
}

func serveRfNoise(srv *Server, path string) *httptest.ResponseRecorder {
	router := mux.NewRouter()
	router.HandleFunc("/api/rf-noise", srv.handleRfNoise).Methods("GET")
	req := httptest.NewRequest("GET", path, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

func TestRfNoiseEndpointGeoJSON(t *testing.T) {
	db := seedRfNoiseDB(t)
	now := time.Now().UTC().Format(time.RFC3339)
	outsideWindow := time.Now().UTC().AddDate(0, 0, -40).Format(time.RFC3339)
	mustExecDB(t, db, `INSERT INTO client_rf_samples (rx_pubkey,sampled_at,ingested_at,lat,lon,stationary,uptime_secs,noise_floor)
		VALUES ('comp','`+now+`','t',51.05,3.72,0,100,-112)`)
	// A parked sample in the same cell/window must not affect the response.
	mustExecDB(t, db, `INSERT INTO client_rf_samples (rx_pubkey,sampled_at,ingested_at,lat,lon,stationary,uptime_secs,noise_floor)
		VALUES ('comp','`+now+`','t',51.05,3.72,1,100,-90)`)
	// A row with no noise_floor reading must be skipped, not counted/crash.
	mustExecDB(t, db, `INSERT INTO client_rf_samples (rx_pubkey,sampled_at,ingested_at,lat,lon,stationary,uptime_secs,noise_floor)
		VALUES ('comp','`+now+`','t',51.05,3.72,0,100,NULL)`)
	// A row outside the days=30 window (#3) must not be counted.
	mustExecDB(t, db, `INSERT INTO client_rf_samples (rx_pubkey,sampled_at,ingested_at,lat,lon,stationary,uptime_secs,noise_floor)
		VALUES ('comp','`+outsideWindow+`','t',51.05,3.72,0,100,-105)`)
	// A row outside the bbox=50,3,52,4 window (#3) must not be counted.
	mustExecDB(t, db, `INSERT INTO client_rf_samples (rx_pubkey,sampled_at,ingested_at,lat,lon,stationary,uptime_secs,noise_floor)
		VALUES ('comp','`+now+`','t',10.0,10.0,0,100,-105)`)
	srv := &Server{db: db, cfg: &Config{ClientRfSamples: &ClientRfSamplesConfig{Enabled: true}}}

	rr := serveRfNoise(srv, "/api/rf-noise?bbox=50,3,52,4&z=12&days=30")
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var fc RfNoiseFeatureCollection
	if err := json.Unmarshal(rr.Body.Bytes(), &fc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if fc.Type != "FeatureCollection" || len(fc.Features) != 1 {
		t.Fatalf("unexpected fc: %+v", fc)
	}
	p := fc.Features[0].Properties
	if p.Count != 1 || p.MedianNoiseFloor != -112 {
		t.Fatalf("expected the single mobile sample to define the cell, got %+v", p)
	}

	if serveRfNoise(srv, "/api/rf-noise").Code != 400 {
		t.Fatal("missing bbox should be 400")
	}
}

// TestRfNoiseEndpointGatedOff404sEvenWithData verifies the endpoint 404s
// when the feature flag is off even though matching rows exist in the DB.
// It does not verify requireClientRfSamples runs before the DB query (a gate
// checked after the query would also 404 here) — that ordering is correct
// by inspection of handleRfNoise, not exercised by this test.
func TestRfNoiseEndpointGatedOff404sEvenWithData(t *testing.T) {
	db := seedRfNoiseDB(t)
	mustExecDB(t, db, `INSERT INTO client_rf_samples (rx_pubkey,sampled_at,ingested_at,lat,lon,stationary,uptime_secs,noise_floor)
		VALUES ('comp','2026-06-01T10:00:00Z','t',51.05,3.72,0,100,-112)`)
	srv := &Server{db: db, cfg: &Config{}}

	if code := serveRfNoise(srv, "/api/rf-noise?bbox=50,3,52,4"); code.Code != 404 {
		t.Fatalf("expected 404 when clientRfSamples disabled, got %d", code.Code)
	}
}
