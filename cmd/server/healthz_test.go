package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthzNotReady(t *testing.T) {
	// Ensure readiness is 0 (not ready)
	readiness.Store(0)
	defer readiness.Store(0)

	srv := &Server{store: &PacketStore{}}
	req := httptest.NewRequest("GET", "/api/healthz", nil)
	w := httptest.NewRecorder()

	srv.handleHealthz(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["ready"] != false {
		t.Fatalf("expected ready=false, got %v", resp["ready"])
	}
	if resp["reason"] != "loading" {
		t.Fatalf("expected reason=loading, got %v", resp["reason"])
	}
}

func TestHealthzReady(t *testing.T) {
	readiness.Store(1)
	defer readiness.Store(0)

	srv := &Server{store: &PacketStore{}}
	req := httptest.NewRequest("GET", "/api/healthz", nil)
	w := httptest.NewRecorder()

	srv.handleHealthz(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["ready"] != true {
		t.Fatalf("expected ready=true, got %v", resp["ready"])
	}
	if _, ok := resp["loadedTx"]; !ok {
		t.Fatal("missing loadedTx field")
	}
	if _, ok := resp["loadedObs"]; !ok {
		t.Fatal("missing loadedObs field")
	}
}

func TestHealthzAntiTautology(t *testing.T) {
	// When readiness is 0, must NOT return 200
	readiness.Store(0)
	defer readiness.Store(0)

	srv := &Server{store: &PacketStore{}}
	req := httptest.NewRequest("GET", "/api/healthz", nil)
	w := httptest.NewRecorder()

	srv.handleHealthz(w, req)

	if w.Code == http.StatusOK {
		t.Fatal("anti-tautology: handler returned 200 when readiness=0; gating is broken")
	}
}

// TestHealthzExposesFromPubkeyBackfill verifies the from_pubkey backfill
// progress (#1143, M2) is observable via /api/healthz. The atomics are
// updated by backfillFromPubkeyAsync; without exposure here they were dead
// code. Asserts the response includes a from_pubkey_backfill object with
// total/processed/done fields.
func TestHealthzExposesFromPubkeyBackfill(t *testing.T) {
	readiness.Store(1)
	defer readiness.Store(0)

	// Set known atomic values so we can assert wiring (not just presence).
	fromPubkeyBackfillTotal.Store(7)
	fromPubkeyBackfillProcessed.Store(3)
	fromPubkeyBackfillDone.Store(false)
	defer func() {
		fromPubkeyBackfillTotal.Store(0)
		fromPubkeyBackfillProcessed.Store(0)
		fromPubkeyBackfillDone.Store(false)
	}()

	srv := &Server{store: &PacketStore{}}
	req := httptest.NewRequest("GET", "/api/healthz", nil)
	w := httptest.NewRecorder()
	srv.handleHealthz(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	bf, ok := resp["from_pubkey_backfill"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing from_pubkey_backfill object in healthz response: %v", resp)
	}
	if got, want := bf["total"], float64(7); got != want {
		t.Errorf("from_pubkey_backfill.total = %v, want %v", got, want)
	}
	if got, want := bf["processed"], float64(3); got != want {
		t.Errorf("from_pubkey_backfill.processed = %v, want %v", got, want)
	}
	if got, want := bf["done"], false; got != want {
		t.Errorf("from_pubkey_backfill.done = %v, want %v", got, want)
	}
}
