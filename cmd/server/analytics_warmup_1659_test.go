// Package main: issue #1659 — analytics warmup gating.
//
// After a server restart, the analytics recomputer caches the FIRST
// computation (a small in-RAM slice) and serves it via the default
// region="", zero-window shortcut in GetAnalyticsRFWithWindow until the
// next periodic recompute fires. The client-side CLIENT_TTL.analyticsRF
// then pins that small slice on the page even after the server flips
// to steady-state.
//
// Fix: each recomputer carries a firstPassDoneAt timestamp set ONLY
// after a full-range compute completes. While firstPassDoneAt is zero
// AND the request is the default-shape (region="" && area="" &&
// window.IsZero()), the handler returns 503 + Retry-After: 5 with a
// JSON body the client recognizes and retries with backoff.
//
// These tests are the RED contract: they must FAIL on the assertion
// (not a build error) when the warmup gate is absent, and PASS once
// the fix lands.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestAnalyticsRF_WarmupReturns503 asserts that immediately after the
// server starts — before any analytics recomputer has finished its
// first full-range pass — GET /api/analytics/rf returns 503 with
// Retry-After: 5 and a JSON body shaped as
// {"error":"analytics warming up","retry_after_s":5}.
//
// This is the core acceptance criterion (c) from #1659.
func TestAnalyticsRF_WarmupReturns503(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	// Register recomputers but DO NOT let them complete a first pass.
	// We install a compute func that blocks until we release it, so the
	// recomputer's firstPassDoneAt stays zero.
	block := make(chan struct{})
	defer close(block)
	store.installWarmupBlocker_1659(block) // helper added in GREEN

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/analytics/rf", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 during warmup, got %d (body=%s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("expected Retry-After: 5, got %q", got)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON body: %v (raw=%s)", err, w.Body.String())
	}
	if resp["error"] != "analytics warming up" {
		t.Fatalf("expected error='analytics warming up', got %v", resp["error"])
	}
	if v, ok := resp["retry_after_s"].(float64); !ok || v != 5 {
		t.Fatalf("expected retry_after_s=5, got %v", resp["retry_after_s"])
	}
}

// TestAnalyticsRF_AfterFirstPassReturns200 asserts the post-warmup
// happy path: once the recomputer's first full-range compute completes,
// the handler serves the cached snapshot as 200.
func TestAnalyticsRF_AfterFirstPassReturns200(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)

	stop := store.StartAnalyticsRecomputers(50 * time.Millisecond)
	defer stop()

	// Wait for the synchronous first-pass to complete. Start() runs
	// the initial compute synchronously, so by the time it returns
	// firstPassDoneAt should be set. We poll a brief moment to keep
	// the test robust to scheduling.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if store.recompRF != nil && !store.recompRF.FirstPassDoneAt_1659().IsZero() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if store.recompRF == nil || store.recompRF.FirstPassDoneAt_1659().IsZero() {
		t.Fatal("recompRF.firstPassDoneAt never flipped after Start()")
	}

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/analytics/rf", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after first pass, got %d (body=%s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Retry-After"); got != "" {
		t.Fatalf("expected no Retry-After header on 200, got %q", got)
	}
	// Body should be a valid JSON object (the RF analytics map).
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if len(resp) == 0 {
		t.Fatal("expected non-empty RF analytics response after first pass")
	}
}

// TestAnalyticsRF_WindowedRequestNotGated asserts that even during
// warmup, a request with an explicit time window (?since=/?until=) or
// region/area filter is NOT gated by the warmup flag — those queries
// bypass the recomputer entirely and hit the legacy compute-then-cache
// path, which is unaffected by the first-pass bug.
func TestAnalyticsRF_WindowedRequestNotGated(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	block := make(chan struct{})
	defer close(block)
	store.installWarmupBlocker_1659(block)

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	// Explicit window — should bypass warmup gate.
	req := httptest.NewRequest("GET", "/api/analytics/rf?window=1h", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code == http.StatusServiceUnavailable {
		t.Fatalf("windowed request must NOT be gated by warmup (got 503)")
	}
}
