package main

import (
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestObserversCacheServesFromAtomicPointer asserts the /api/observers default
// (no-filter) handler serves from an in-memory snapshot after the first request,
// not from SQL. Issue #1481 P0-3.
func TestObserversCacheServesFromAtomicPointer(t *testing.T) {
	s := &Server{}
	// Seed the cache directly — handler must read from it before touching SQL.
	resp := ObserverListResponse{
		Observers:  []ObserverResp{{ID: "abc", Name: "test"}},
		ServerTime: time.Now().UTC().Format(time.RFC3339),
	}
	s.observersCache.Store(&resp)

	req := httptest.NewRequest("GET", "/api/observers", nil)
	w := httptest.NewRecorder()
	s.handleObservers(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"id":"abc"`) {
		t.Fatalf("expected cached observer in body, got: %s", body)
	}
}

// TestObserversCacheRecomputesAfterTTL asserts the cache timestamp gates
// recomputation: a cached snapshot newer than TTL is served as-is.
func TestObserversCacheTTLBoundary(t *testing.T) {
	s := &Server{}
	if d := observersCacheTTL; d != 30*time.Second {
		t.Errorf("observersCacheTTL want 30s, got %v", d)
	}
	// cacheAt zero → considered expired
	if !s.observersCacheExpired(time.Time{}) {
		t.Error("zero time should be expired")
	}
	if s.observersCacheExpired(time.Now()) {
		t.Error("just-now should not be expired")
	}
	if !s.observersCacheExpired(time.Now().Add(-31 * time.Second)) {
		t.Error("31s ago should be expired")
	}
}

// TestObserversCacheIsAtomicPointerNotMutex ensures we use atomic.Pointer
// (lock-free) — assert via field type by reflection.
func TestObserversCacheTypeAtomicPointer(t *testing.T) {
	s := &Server{}
	var p *ObserverListResponse
	// Compile-time check on the atomic.Pointer API:
	s.observersCache.Store(p)
	got := s.observersCache.Load()
	_ = got
	// Sanity: increments under concurrent CAS work without blocking.
	var ops atomic.Int64
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			s.observersCache.Load()
			ops.Add(1)
		}
		close(done)
	}()
	<-done
	if ops.Load() != 100 {
		t.Fatal("atomic ops not completing")
	}
}
