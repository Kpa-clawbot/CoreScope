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

func TestHaversineKm_KnownDistance(t *testing.T) {
	// Amsterdam to Rotterdam ≈ 57 km
	km := haversineKm(52.3676, 4.9041, 51.9244, 4.4777)
	if math.Abs(km-57.0) > 2.0 {
		t.Errorf("expected ~57 km, got %.2f", km)
	}
}

func TestHaversineKm_SamePoint(t *testing.T) {
	km := haversineKm(52.0, 4.0, 52.0, 4.0)
	if km != 0 {
		t.Errorf("expected 0 for same point, got %f", km)
	}
}

func TestInterpolatePoint_Midpoint(t *testing.T) {
	lat, lon := interpolatePoint(0, 0, 0, 10, 0.5)
	if math.Abs(lat-0) > 0.001 || math.Abs(lon-5) > 0.001 {
		t.Errorf("expected (0, 5), got (%.4f, %.4f)", lat, lon)
	}
}

func TestInterpolatePoint_StartEnd(t *testing.T) {
	lat0, lon0 := interpolatePoint(10, 20, 30, 40, 0)
	if math.Abs(lat0-10) > 0.001 || math.Abs(lon0-20) > 0.001 {
		t.Errorf("t=0 should return start, got (%.4f, %.4f)", lat0, lon0)
	}
	lat1, lon1 := interpolatePoint(10, 20, 30, 40, 1)
	if math.Abs(lat1-30) > 0.001 || math.Abs(lon1-40) > 0.001 {
		t.Errorf("t=1 should return end, got (%.4f, %.4f)", lat1, lon1)
	}
}

func TestEarthBulgeM_Edges(t *testing.T) {
	if b := earthBulgeM(0, 10000); b != 0 {
		t.Errorf("expected 0 bulge at t=0, got %f", b)
	}
	if b := earthBulgeM(1, 10000); b != 0 {
		t.Errorf("expected 0 bulge at t=1, got %f", b)
	}
}

func TestEarthBulgeM_Midpoint(t *testing.T) {
	dist := 10000.0
	R := 6371000.0 * 1.33
	expected := 0.25 * dist * dist / (2 * R)
	got := earthBulgeM(0.5, dist)
	if math.Abs(got-expected) > 0.001 {
		t.Errorf("expected %.4f, got %.4f", expected, got)
	}
}

func TestLOSAnalyze_ClearPath(t *testing.T) {
	profile := []losProfilePoint{
		{TerrainElev: 0, LOSElev: 10, Bulge: 0},
		{TerrainElev: 0, LOSElev: 9, Bulge: 0.1},
		{TerrainElev: 0, LOSElev: 8, Bulge: 0},
	}
	result := losAnalyze(profile)
	if !result.LOSClear {
		t.Errorf("expected LOS clear on flat terrain")
	}
	if result.MaxViolationM != 0 {
		t.Errorf("expected 0 violation, got %f", result.MaxViolationM)
	}
}

func TestLOSAnalyze_BlockedPath(t *testing.T) {
	// Mountain at index 1 exceeds LOS line
	profile := []losProfilePoint{
		{TerrainElev: 10, LOSElev: 50, Bulge: 0},
		{TerrainElev: 100, LOSElev: 50, Bulge: 2}, // blocked: 100 > 52
		{TerrainElev: 10, LOSElev: 50, Bulge: 0},
	}
	result := losAnalyze(profile)
	if result.LOSClear {
		t.Errorf("expected LOS blocked")
	}
	if math.Abs(result.MaxViolationM-48) > 1 {
		t.Errorf("expected ~48m violation, got %.2f", result.MaxViolationM)
	}
	if result.Relay == nil {
		t.Errorf("expected relay suggestion when blocked")
	}
}

func TestHandleLOS_BadRequest(t *testing.T) {
	srv := &Server{cfg: &Config{}, perfStats: NewPerfStats()}
	r := mux.NewRouter()
	r.HandleFunc("/api/los", srv.handleLOS).Methods("POST")

	body := strings.NewReader(`{"lat_a": 999, "lon_a": 4.0, "lat_b": 52.0, "lon_b": 4.5}`)
	req := httptest.NewRequest(http.MethodPost, "/api/los", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] == "" {
		t.Errorf("expected error field in response")
	}
}

func TestHandleLOS_InvalidJSON(t *testing.T) {
	srv := &Server{cfg: &Config{}, perfStats: NewPerfStats()}
	r := mux.NewRouter()
	r.HandleFunc("/api/los", srv.handleLOS).Methods("POST")

	body := strings.NewReader(`not json`)
	req := httptest.NewRequest(http.MethodPost, "/api/los", body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}
