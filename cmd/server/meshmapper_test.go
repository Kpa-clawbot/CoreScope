package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleMeshMapperCoverage_NotConfigured(t *testing.T) {
	s := &Server{cfg: &Config{}} // no MeshMapper config → key is empty
	req := httptest.NewRequest("GET", "/api/coverage/meshmapper", nil)
	rr := httptest.NewRecorder()
	s.handleMeshMapperCoverage(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503, got %d", rr.Code)
	}
	var body map[string]string
	json.NewDecoder(rr.Body).Decode(&body)
	if body["error"] != "meshmapper_not_configured" {
		t.Errorf("want meshmapper_not_configured error, got %q", body["error"])
	}
}

func TestHandleMeshMapperCoverage_FetchSuccess(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer testkey" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"coverage_type":"BIDIR","bounds":{"south":52.0,"west":4.0,"north":52.01,"east":4.01},"snr":5,"timestamp":1700000000,"grid_id":"A1"}]`))
	}))
	defer upstream.Close()

	s := &Server{cfg: &Config{MeshMapper: &MeshMapperConfig{APIKey: "testkey", APIUrl: upstream.URL}}}
	s.meshMapperCache = meshMapperCacheState{}

	req := httptest.NewRequest("GET", "/api/coverage/meshmapper", nil)
	rr := httptest.NewRecorder()
	s.handleMeshMapperCoverage(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "BIDIR") {
		t.Errorf("response should contain BIDIR, got: %s", rr.Body.String())
	}
}

func TestHandleMeshMapperCoverage_CacheHit(t *testing.T) {
	callCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Write([]byte(`[]`))
	}))
	defer upstream.Close()

	s := &Server{cfg: &Config{MeshMapper: &MeshMapperConfig{
		APIKey: "k", APIUrl: upstream.URL, CacheTTLSecs: 60,
	}}}
	s.meshMapperCache = meshMapperCacheState{}

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/api/coverage/meshmapper", nil)
		rr := httptest.NewRecorder()
		s.handleMeshMapperCoverage(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("call %d: want 200, got %d", i, rr.Code)
		}
	}
	if callCount != 1 {
		t.Errorf("upstream should be called once (cache hit on calls 2+), got %d", callCount)
	}
}

func TestHandleMeshMapperCoverage_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	s := &Server{cfg: &Config{MeshMapper: &MeshMapperConfig{
		APIKey: "k", APIUrl: upstream.URL,
	}}}
	s.meshMapperCache = meshMapperCacheState{}

	req := httptest.NewRequest("GET", "/api/coverage/meshmapper", nil)
	rr := httptest.NewRecorder()
	s.handleMeshMapperCoverage(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("want 502, got %d", rr.Code)
	}
}
