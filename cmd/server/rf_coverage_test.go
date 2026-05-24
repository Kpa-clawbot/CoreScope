package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

// ─── Math unit tests ───────────────────────────────────────────────────────────

func TestRFSensitivityDBm(t *testing.T) {
	cases := []struct{ sf int; want float64 }{
		{7, -123.0}, {8, -126.0}, {9, -129.0},
		{10, -132.0}, {11, -134.5}, {12, -137.0},
	}
	for _, c := range cases {
		if got := rfSensitivityDBm(c.sf); got != c.want {
			t.Errorf("SF%d: want %f, got %f", c.sf, c.want, got)
		}
	}
}

func TestRFSensitivityDBm_UnknownSF_DefaultsSF7(t *testing.T) {
	if got := rfSensitivityDBm(6); got != -123.0 {
		t.Errorf("unknown SF should default to SF7 (-123), got %f", got)
	}
}

func TestRFPathLossExponent(t *testing.T) {
	if got := rfPathLossExponent("free"); got != 2.0 {
		t.Errorf("free: want 2.0, got %f", got)
	}
	if got := rfPathLossExponent("suburban"); got != 2.2 {
		t.Errorf("suburban: want 2.2, got %f", got)
	}
	if got := rfPathLossExponent("urban"); got != 2.3 {
		t.Errorf("urban: want 2.3, got %f", got)
	}
	if got := rfPathLossExponent("indoor"); got != 2.7 {
		t.Errorf("indoor: want 2.7, got %f", got)
	}
	if got := rfPathLossExponent("unknown"); got != 2.0 {
		t.Errorf("unknown model: want free-space 2.0, got %f", got)
	}
}

func TestRFPathLossDB_ZeroDistance(t *testing.T) {
	pl := rfPathLossDB(0, 869.618, 2.0)
	if pl != 0 {
		t.Errorf("zero distance: want 0, got %f", pl)
	}
}

func TestRFPathLossDB_IncreaseWithDistance(t *testing.T) {
	pl1 := rfPathLossDB(1000, 869.618, 2.0)
	pl2 := rfPathLossDB(5000, 869.618, 2.0)
	if pl2 <= pl1 {
		t.Errorf("path loss should increase with distance: pl(1km)=%f pl(5km)=%f", pl1, pl2)
	}
}

func TestRFPathLossDB_AtOneMeter(t *testing.T) {
	// At 1 m, 869.618 MHz, n=2: FSPL_1m = 20*log10(4*pi*869.618e6/299792458)
	freqHz := 869.618e6
	fspl1m := 20 * math.Log10(4*math.Pi*freqHz/299_792_458)
	got := rfPathLossDB(1, 869.618, 2.0)
	if math.Abs(got-fspl1m) > 0.01 {
		t.Errorf("at 1m: want %.4f dB, got %.4f dB", fspl1m, got)
	}
}

func TestDestCoordFromBearing_North(t *testing.T) {
	// Moving 1 km north from (52.0, 5.0) should increase lat, keep lon near 5.0
	lat, lon := destCoordFromBearing(52.0, 5.0, 1.0, 0)
	if lat <= 52.0 {
		t.Errorf("moving north should increase lat: got %f", lat)
	}
	if math.Abs(lon-5.0) > 0.01 {
		t.Errorf("moving north should not change lon significantly: got %f", lon)
	}
}

func TestDestCoordFromBearing_RoundTrip(t *testing.T) {
	// Go 10 km north, then 10 km south — should return close to origin
	lat1, lon1 := destCoordFromBearing(52.0, 5.0, 10.0, 0)   // north
	lat2, lon2 := destCoordFromBearing(lat1, lon1, 10.0, 180) // south
	if math.Abs(lat2-52.0) > 0.01 {
		t.Errorf("round-trip lat: want ~52.0, got %f", lat2)
	}
	if math.Abs(lon2-5.0) > 0.01 {
		t.Errorf("round-trip lon: want ~5.0, got %f", lon2)
	}
}

// ─── HTTP handler tests ────────────────────────────────────────────────────────

func TestHandleRFCoverage_InvalidJSON(t *testing.T) {
	s := &Server{cfg: &Config{}}
	r := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/rf-coverage", strings.NewReader("not-json"))
	s.handleRFCoverage(r, req)
	if r.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", r.Code)
	}
}

func TestHandleRFCoverage_LatOutOfRange(t *testing.T) {
	s := &Server{cfg: &Config{}}
	body := `{"lat":999,"lon":5.0}`
	r := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/rf-coverage", strings.NewReader(body))
	s.handleRFCoverage(r, req)
	if r.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", r.Code)
	}
}

func TestHandleRFCoverage_LonOutOfRange(t *testing.T) {
	s := &Server{cfg: &Config{}}
	body := `{"lat":52.0,"lon":999.0}`
	r := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/rf-coverage", strings.NewReader(body))
	s.handleRFCoverage(r, req)
	if r.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", r.Code)
	}
}

// ─── Integration test with mock elevation server ───────────────────────────────

// mockRFElevServer returns a test server that responds with constant elevation 0
// for all requested points. The response shape matches open-topo-data.
func mockRFElevServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		locs := r.URL.Query().Get("locations")
		points := strings.Split(locs, "|")
		type dataset struct {
			Elevation *float64 `json:"elevation"`
		}
		type point struct {
			Datasets []dataset `json:"datasets"`
		}
		type resp struct {
			Results []point `json:"results"`
		}
		zero := 0.0
		results := make([]point, len(points))
		for i := range results {
			results[i] = point{Datasets: []dataset{{Elevation: &zero}}}
		}
		json.NewEncoder(w).Encode(resp{Results: results})
	}))
}

func TestHandleRFCoverage_MockElevation(t *testing.T) {
	srv := mockRFElevServer(t)
	defer srv.Close()

	cfg := &Config{
		LOS: &LOSConfig{ElevationURL: srv.URL, CacheTTLHours: 0},
	}
	s := &Server{cfg: cfg}

	router := mux.NewRouter()
	router.HandleFunc("/api/rf-coverage", s.handleRFCoverage).Methods("POST")

	body := `{"lat":52.0,"lon":5.0,"tx_power_dbm":20,"freq_mhz":869.618,"sf":7,"antenna_height":2,"model":"free"}`
	req := httptest.NewRequest("POST", "/api/rf-coverage", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d — body: %s", rr.Code, rr.Body.String())
	}

	var resp rfCoverageResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(resp.Coverage) != 36 {
		t.Errorf("want 36 coverage points, got %d", len(resp.Coverage))
	}
	if resp.SensitivityDBm != -123.0 {
		t.Errorf("want sensitivity -123, got %f", resp.SensitivityDBm)
	}
	// On flat terrain, every bearing should reach > 0 km
	for i, pt := range resp.Coverage {
		if pt.RangeKm <= 0 {
			t.Errorf("bearing %d: expected range > 0 on flat terrain, got %f km", i, pt.RangeKm)
		}
	}
}
