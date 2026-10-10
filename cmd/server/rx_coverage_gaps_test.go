package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// --- aggregateGaps: pure ---

func TestAggregateGapsTrackWithoutReception(t *testing.T) {
	res := zoomToHexRes(12)
	track := []trackPoint{{Lat: 51.05, Lon: 3.72}, {Lat: 51.05, Lon: 3.72}, {Lat: 51.30, Lon: 3.90}}
	rx := []coverageRow{{Lat: 51.30, Lon: 3.90}}
	gaps, truncated := aggregateGaps(track, rx, res)
	if truncated {
		t.Fatal("two cells must not be truncated")
	}
	if len(gaps) != 1 {
		t.Fatalf("want 1 gap (the cell without a reception), got %d: %+v", len(gaps), gaps)
	}
	g := gaps[0]
	if g.Type != "Feature" || g.Geometry.Type != "Polygon" || len(g.Geometry.Coordinates[0]) == 0 {
		t.Fatalf("gap must be a GeoJSON polygon feature, got %+v", g)
	}
	if g.Properties.Cell != hexCellAt(51.05, 3.72, res) || g.Properties.Samples != 2 {
		t.Fatalf("want the 51.05/3.72 cell with 2 samples, got %+v", g.Properties)
	}
}

// A parked companion is not a drive through the cell: stationary samples never make a gap.
func TestAggregateGapsIgnoresStationary(t *testing.T) {
	gaps, _ := aggregateGaps([]trackPoint{{Lat: 51.05, Lon: 3.72, Stationary: true}}, nil, zoomToHexRes(12))
	if len(gaps) != 0 {
		t.Fatalf("stationary sample must not produce a gap, got %+v", gaps)
	}
}

// One moving sample is enough (threshold measured: more samples do not make a gap more reliable).
func TestAggregateGapsSingleSample(t *testing.T) {
	gaps, _ := aggregateGaps([]trackPoint{{Lat: 51.05, Lon: 3.72}}, nil, zoomToHexRes(12))
	if len(gaps) != 1 || gaps[0].Properties.Samples != 1 {
		t.Fatalf("one moving sample without reception must be a gap, got %+v", gaps)
	}
}

func TestAggregateGapsCapsKeepsMostSampled(t *testing.T) {
	res := zoomToHexRes(14)
	var track []trackPoint
	seen := map[string]bool{}
	for i := 0; len(seen) < coverageGapCap+5; i++ {
		la, lo := 51.0+float64(i%200)*0.002, 3.5+float64(i/200)*0.003
		c := hexCellAt(la, lo, res)
		if seen[c] {
			continue
		}
		seen[c] = true
		track = append(track, trackPoint{Lat: la, Lon: lo})
		if len(seen) == 1 { // one cell gets many samples: it must survive the cap
			for k := 0; k < 9; k++ {
				track = append(track, trackPoint{Lat: la, Lon: lo})
			}
		}
	}
	gaps, truncated := aggregateGaps(track, nil, res)
	if !truncated || len(gaps) != coverageGapCap {
		t.Fatalf("want %d gaps and truncated, got %d truncated=%v", coverageGapCap, len(gaps), truncated)
	}
	found := false
	for _, g := range gaps {
		if g.Properties.Samples == 10 {
			found = true
		}
	}
	if !found {
		t.Fatal("the most-sampled cell must be kept when capping")
	}
	for i := 1; i < len(gaps); i++ {
		if gaps[i-1].Properties.Cell > gaps[i].Properties.Cell {
			t.Fatal("gaps must be sorted by cell for a deterministic payload")
		}
	}
}

// --- endpoint ---

func seedGapsDB(t *testing.T) *DB {
	db := seedCoverageDB(t)
	mustExecDB(t, db, `CREATE TABLE client_rf_samples (
		id INTEGER PRIMARY KEY AUTOINCREMENT, rx_pubkey TEXT, sampled_at TEXT, ingested_at TEXT,
		lat REAL, lon REAL, pos_acc_m REAL, stationary INTEGER NOT NULL DEFAULT 0,
		uptime_secs INTEGER, battery_mv INTEGER, queue_len INTEGER, errors INTEGER,
		noise_floor INTEGER, last_rssi INTEGER, last_snr REAL, tx_air_secs INTEGER,
		rx_air_secs INTEGER, recv INTEGER, sent INTEGER, flood_rx INTEGER, direct_rx INTEGER,
		flood_tx INTEGER, direct_tx INTEGER, recv_errors INTEGER)`)
	return db
}

func insTrack(t *testing.T, db *DB, rx, at string, lat, lon float64, stationary int) {
	mustExecDB(t, db, fmt.Sprintf(
		`INSERT INTO client_rf_samples (rx_pubkey,sampled_at,ingested_at,lat,lon,stationary,uptime_secs) VALUES ('%s','%s','t',%f,%f,%d,1)`,
		rx, at, lat, lon, stationary))
}

func serveCoverage(srv *Server, path string) *httptest.ResponseRecorder {
	router := mux.NewRouter()
	router.HandleFunc("/api/rx-coverage", srv.handleRxCoverage).Methods("GET")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
	return rr
}

func gapsServer(t *testing.T, rfOn bool) *Server {
	db := seedGapsDB(t)
	// seedCoverageDB already holds receptions; start from a known state.
	mustExecDB(t, db, `DELETE FROM client_receptions`)
	now := time.Now().UTC().Format(time.RFC3339)
	old := time.Now().UTC().AddDate(0, 0, -40).Format(time.RFC3339)
	insRx(t, db, "compa", "aabbcc", now, 51.30, 3.90) // heard here
	insTrack(t, db, "compa", now, 51.30, 3.90, 0)     // driven and heard: no gap
	insTrack(t, db, "compa", now, 51.05, 3.72, 0)     // driven, nothing: gap
	insTrack(t, db, "compb", now, 51.10, 3.60, 0)     // other companion, nothing: gap unless ?rx=compa
	insTrack(t, db, "compa", now, 51.20, 3.50, 1)     // parked: no gap
	insTrack(t, db, "compa", old, 51.40, 3.40, 0)     // outside the 7-day window: no gap
	cfg := &Config{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true}}
	if rfOn {
		cfg.ClientRfSamples = &ClientRfSamplesConfig{Enabled: true}
	}
	return &Server{db: db, cfg: cfg}
}

func decodeCoverage(t *testing.T, rr *httptest.ResponseRecorder) CoverageFeatureCollection {
	t.Helper()
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var fc CoverageFeatureCollection
	if err := json.Unmarshal(rr.Body.Bytes(), &fc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return fc
}

func TestRxCoverageGapsEndpoint(t *testing.T) {
	srv := gapsServer(t, true)
	z := 12
	fc := decodeCoverage(t, serveCoverage(srv, fmt.Sprintf("/api/rx-coverage?bbox=50,3,52,4&z=%d&days=7&gaps=1", z)))
	if len(fc.Features) != 1 {
		t.Fatalf("the reception layer must be unchanged: want 1 feature, got %d", len(fc.Features))
	}
	res := zoomToHexRes(z)
	want := map[string]bool{hexCellAt(51.05, 3.72, res): true, hexCellAt(51.10, 3.60, res): true}
	if len(fc.Gaps) != len(want) {
		t.Fatalf("want %d gaps, got %+v", len(want), fc.Gaps)
	}
	for _, g := range fc.Gaps {
		if !want[g.Properties.Cell] {
			t.Fatalf("unexpected gap cell %s", g.Properties.Cell)
		}
	}
}

func TestRxCoverageGapsFollowObserverFilter(t *testing.T) {
	srv := gapsServer(t, true)
	fc := decodeCoverage(t, serveCoverage(srv, "/api/rx-coverage?bbox=50,3,52,4&z=12&days=7&gaps=1&rx=compa"))
	if len(fc.Gaps) != 1 || fc.Gaps[0].Properties.Cell != hexCellAt(51.05, 3.72, zoomToHexRes(12)) {
		t.Fatalf("?rx=compa must only show compa's own drive, got %+v", fc.Gaps)
	}
}

// Without ?gaps=1, with a node filter, or with the RF sample stream off, the payload carries no gaps member at all.
func TestRxCoverageGapsOnlyWhenAskedAndAvailable(t *testing.T) {
	cases := []struct {
		name string
		rfOn bool
		path string
	}{
		{"not asked", true, "/api/rx-coverage?bbox=50,3,52,4&z=12&days=7"},
		{"node filter", true, "/api/rx-coverage?bbox=50,3,52,4&z=12&days=7&gaps=1&node=aabbcc"},
		{"rf samples off", false, "/api/rx-coverage?bbox=50,3,52,4&z=12&days=7&gaps=1"},
	}
	for _, c := range cases {
		rr := serveCoverage(gapsServer(t, c.rfOn), c.path)
		if rr.Code != 200 {
			t.Fatalf("%s: status %d", c.name, rr.Code)
		}
		if strings.Contains(rr.Body.String(), `"gaps"`) {
			t.Fatalf("%s: payload must not carry gaps, got %s", c.name, rr.Body.String())
		}
	}
}
