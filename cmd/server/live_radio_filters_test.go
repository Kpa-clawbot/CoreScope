package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

// /api/config/client exposes liveRadioFilters so the /live "Radio" picker can
// narrow the feed to packets heard only on one frequency (e.g. a second mesh
// heard by multi-radio observers). Unset → [], invalid entries dropped.
func TestConfigClientExposesLiveRadioFilters(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{
		Port: 3000,
		LiveRadioFilters: []LiveRadioFilter{
			{Label: " MediaMesh ", FreqMHz: 911.15},
			{Label: "", FreqMHz: 910.5},   // no label
			{Label: "Zero", FreqMHz: 0},   // no frequency
			{Label: "Neg", FreqMHz: -1.0}, // nonsense
		},
	}
	srv := NewServer(db, cfg, NewHub())
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/config/client", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	var body struct {
		LiveRadioFilters []LiveRadioFilter `json:"liveRadioFilters"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.LiveRadioFilters) != 1 {
		t.Fatalf("expected 1 valid filter, got %+v", body.LiveRadioFilters)
	}
	if got := body.LiveRadioFilters[0]; got.Label != "MediaMesh" || got.FreqMHz != 911.15 {
		t.Errorf("unexpected filter %+v", got)
	}
}

func TestConfigClientLiveRadioFiltersDefaultsEmpty(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/config/client", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	v, ok := body["liveRadioFilters"].([]interface{})
	if !ok {
		t.Fatalf("expected liveRadioFilters to be [], got %T %v", body["liveRadioFilters"], body["liveRadioFilters"])
	}
	if len(v) != 0 {
		t.Errorf("expected no filters by default, got %v", v)
	}
}
