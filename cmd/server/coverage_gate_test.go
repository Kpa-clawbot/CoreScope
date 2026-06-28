package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

// gateTestServer builds a minimal *Server with the given client-RX coverage
// config and registers all routes, mirroring routes_test.go's setup but
// skipping the packet store (none of the gated routes need it for the 404 /
// registration assertions below).
func gateTestServer(t *testing.T, cov *ClientRxCoverageConfig) *mux.Router {
	t.Helper()
	db := setupTestDB(t)
	cfg := &Config{Port: 3000, ClientRxCoverage: cov}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	return router
}

func TestCoverageRoutesGatedOff(t *testing.T) {
	router := gateTestServer(t, nil)

	req := httptest.NewRequest("GET", "/api/rx-coverage", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for /api/rx-coverage when disabled, got %d", rr.Code)
	}

	creq := httptest.NewRequest("GET", "/api/config/client", nil)
	crr := httptest.NewRecorder()
	router.ServeHTTP(crr, creq)
	if crr.Code != http.StatusOK {
		t.Fatalf("config/client status %d body %s", crr.Code, crr.Body.String())
	}
	if !strings.Contains(crr.Body.String(), `"clientRxCoverage":false`) {
		t.Fatalf("expected clientRxCoverage:false in config body, got %s", crr.Body.String())
	}
}

func TestCoverageRoutesGatedOn(t *testing.T) {
	router := gateTestServer(t, &ClientRxCoverageConfig{Enabled: true})

	req := httptest.NewRequest("GET", "/api/rx-coverage", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code == http.StatusNotFound {
		t.Fatalf("expected /api/rx-coverage to be registered when enabled, got 404")
	}

	creq := httptest.NewRequest("GET", "/api/config/client", nil)
	crr := httptest.NewRecorder()
	router.ServeHTTP(crr, creq)
	if crr.Code != http.StatusOK {
		t.Fatalf("config/client status %d body %s", crr.Code, crr.Body.String())
	}
	if !strings.Contains(crr.Body.String(), `"clientRxCoverage":true`) {
		t.Fatalf("expected clientRxCoverage:true in config body, got %s", crr.Body.String())
	}
}
