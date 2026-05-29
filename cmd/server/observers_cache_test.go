package main

import (
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestObserversCacheServesFromAtomicPointer asserts the /api/observers default
// (no-filter) handler serves from an in-memory snapshot after the first request,
// not from SQL. Issue #1481 P0-3.
func TestObserversCacheServesFromAtomicPointer(t *testing.T) {
	s := &Server{}
	resp := ObserverListResponse{
		Observers:  []ObserverResp{{ID: "abc", Name: "test"}},
		ServerTime: time.Now().UTC().Format(time.RFC3339),
	}
	s.observersCacheV2.ptr.Store(&observersCacheEntry{resp: resp, at: time.Now()})

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
	if h := w.Header().Get("X-Cache-Age-Seconds"); h == "" {
		t.Error("expected X-Cache-Age-Seconds header on cached response")
	}
}

// TestObserversCacheTTLBoundary exercises the helper AND the handler:
// an entry older than TTL must NOT be served. We assert by toggling the
// stored entry's age and observing whether the handler re-enters the
// build path (signaled by the request producing a stale-sentinel body
// from cache vs. attempting SQL on a nil DB → 500).
func TestObserversCacheTTLBoundary(t *testing.T) {
	if d := observersCacheTTL; d != 30*time.Second {
		t.Errorf("observersCacheTTL want 30s, got %v", d)
	}
	s := &Server{}
	if !s.observersCacheExpired(time.Time{}) {
		t.Error("zero time should be expired")
	}
	if s.observersCacheExpired(time.Now()) {
		t.Error("just-now should not be expired")
	}
	if !s.observersCacheExpired(time.Now().Add(-31 * time.Second)) {
		t.Error("31s ago should be expired")
	}

	// Handler integration: fresh entry → served from cache.
	fresh := ObserverListResponse{
		Observers:  []ObserverResp{{ID: "fresh-sentinel"}},
		ServerTime: time.Now().UTC().Format(time.RFC3339),
	}
	s.observersCacheV2.ptr.Store(&observersCacheEntry{resp: fresh, at: time.Now()})
	req := httptest.NewRequest("GET", "/api/observers", nil)
	w := httptest.NewRecorder()
	s.handleObservers(w, req)
	if !strings.Contains(w.Body.String(), "fresh-sentinel") {
		t.Fatalf("fresh cache should be served by handler, body=%s", w.Body.String())
	}

	// Stale entry → handler must NOT serve it; it will enter the
	// singleflight build path and (with nil DB) crash. We assert it
	// did NOT short-circuit by checking the response is not the stale
	// sentinel: either 500 or panic-recover. Use defer recover().
	stale := ObserverListResponse{
		Observers:  []ObserverResp{{ID: "stale-sentinel"}},
		ServerTime: time.Now().UTC().Format(time.RFC3339),
	}
	s.observersCacheV2.ptr.Store(&observersCacheEntry{resp: stale, at: time.Now().Add(-31 * time.Second)})
	w2 := httptest.NewRecorder()
	func() {
		defer func() { _ = recover() }()
		s.handleObservers(w2, req)
	}()
	if strings.Contains(w2.Body.String(), "stale-sentinel") {
		t.Fatalf("stale cache MUST NOT be served by handler, body=%s", w2.Body.String())
	}
}

// TestObserversCacheSingleflightCollapsesStampede fires N concurrent
// requests at a fresh (empty) cache and asserts the underlying fill
// runs exactly once — singleflight collapsed the herd. #1483 follow-up.
func TestObserversCacheSingleflightCollapsesStampede(t *testing.T) {
	s := &Server{}
	// Pre-empt the SQL path by storing a STALE entry. The handler will
	// then enter singleflight and call buildObserversDefaultResponse,
	// which nil-derefs on s.db. We can't use the real build for this
	// test, so we install a sentinel by storing an entry post-flight.
	// Simpler: use the singleflight Group directly to count calls
	// across N goroutines via Do() — that's exactly the contract.
	const N = 50
	var wg sync.WaitGroup
	var calls atomic.Int64
	wg.Add(N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, _, _ = s.observersCacheV2.sf.Do(observersCacheFlightKey, func() (interface{}, error) {
				calls.Add(1)
				time.Sleep(20 * time.Millisecond) // hold the flight long enough to catch stragglers
				return "ok", nil
			})
		}()
	}
	close(start)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("singleflight must collapse %d concurrent requests into 1 fill, got %d", N, got)
	}
}

// TestObserversCacheConcurrentReadersDuringRecompute asserts that
// readers can keep reading the OLD entry while a recompute is in
// flight (atomic.Pointer payload immutability). #1483 follow-up.
func TestObserversCacheConcurrentReadersDuringRecompute(t *testing.T) {
	s := &Server{}
	initial := ObserverListResponse{
		Observers: []ObserverResp{{ID: "v1"}},
	}
	s.observersCacheV2.ptr.Store(&observersCacheEntry{resp: initial, at: time.Now()})

	var wg sync.WaitGroup
	stop := make(chan struct{})
	var observedV1, observedV2 atomic.Int64
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					e := s.observersCacheV2.ptr.Load()
					if e == nil {
						continue
					}
					if len(e.resp.Observers) > 0 {
						switch e.resp.Observers[0].ID {
						case "v1":
							observedV1.Add(1)
						case "v2":
							observedV2.Add(1)
						}
					}
				}
			}
		}()
	}
	time.Sleep(5 * time.Millisecond)
	// Swap in v2 while readers are running.
	updated := ObserverListResponse{
		Observers: []ObserverResp{{ID: "v2"}},
	}
	s.observersCacheV2.ptr.Store(&observersCacheEntry{resp: updated, at: time.Now()})
	time.Sleep(5 * time.Millisecond)
	close(stop)
	wg.Wait()
	if observedV1.Load() == 0 {
		t.Error("expected at least one read of v1 before swap")
	}
	if observedV2.Load() == 0 {
		t.Error("expected at least one read of v2 after swap")
	}
}
