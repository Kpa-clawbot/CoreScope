package main

import (
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// Issue #1011: distance index must NOT be built eagerly at startup.
// It is constructed lazily on first /api/analytics/distance request,
// the first request returns 202 + Retry-After while the build runs,
// and concurrent requests during the build also get 202 (one build
// only, not N parallel builds).
//
// These three assertions encode the acceptance criteria from the
// triage Fix path (sync.Once-style first-request trigger, 202+Retry-After).

// TestDistanceIndexNotBuiltOnLoad: Load() must complete without
// populating distHops / distPaths. Eager build is gone.
func TestDistanceIndexNotBuiltOnLoad(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("Load(): %v", err)
	}
	store.mu.RLock()
	nHops := len(store.distHops)
	nPaths := len(store.distPaths)
	store.mu.RUnlock()
	if nHops != 0 || nPaths != 0 {
		t.Fatalf("expected distance index empty after Load() (lazy build, #1011); got %d hops, %d paths — eager build still firing in Load()", nHops, nPaths)
	}
	if store.DistanceIndexBuilt() {
		t.Fatalf("expected DistanceIndexBuilt() = false directly after Load(); got true")
	}
}

// TestDistanceFirstRequestReturns202: first /api/analytics/distance call
// must trigger async build and return 202 + Retry-After. The handler must
// NOT block for the full build.
func TestDistanceFirstRequestReturns202(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("Load(): %v", err)
	}
	srv.store = store
	r := mux.NewRouter()
	srv.RegisterRoutes(r)

	req := httptest.NewRequest("GET", "/api/analytics/distance", nil)
	w := httptest.NewRecorder()
	t0 := time.Now()
	r.ServeHTTP(w, req)
	elapsed := time.Since(t0)

	if w.Code != 202 {
		t.Fatalf("expected 202 Accepted on first request (lazy build, #1011); got %d (body=%s)", w.Code, w.Body.String())
	}
	if ra := w.Header().Get("Retry-After"); ra == "" {
		t.Fatalf("expected non-empty Retry-After header on 202 response; got none")
	}
	// Handler must return quickly — must not block on the full build.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("first-request handler took %v — must not block on build (#1011)", elapsed)
	}
}

// TestDistanceConcurrentRequestsDuringBuildReturn202: 10 requests fired
// in close succession while the build is in flight must all receive 202;
// exactly one build runs.
func TestDistanceConcurrentRequestsDuringBuildReturn202(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("Load(): %v", err)
	}
	srv.store = store
	r := mux.NewRouter()
	srv.RegisterRoutes(r)

	const N = 10
	var wg sync.WaitGroup
	var got202 atomic.Int32
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/api/analytics/distance", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code == 202 {
				got202.Add(1)
			}
		}()
	}
	wg.Wait()
	if got202.Load() != N {
		t.Fatalf("expected all %d concurrent first-window requests to get 202; only %d did", N, got202.Load())
	}
}
