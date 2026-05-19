package main

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"
)

// Issue #1265: /api/observers/clock-skew (3.3s) and /api/nodes/clock-skew (8.9s)
// must be wired into the steady-state analytics recomputer so reads serve
// from an atomic-pointer snapshot in <100ms p99 under concurrent load.

func TestClockSkewRecomputersRegistered(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)

	stop := store.StartAnalyticsRecomputers(50 * time.Millisecond)
	defer stop()
	time.Sleep(100 * time.Millisecond)

	store.analyticsRecomputerMu.RLock()
	rcObs := store.recompObserversClockSkew
	rcNodes := store.recompNodesClockSkew
	store.analyticsRecomputerMu.RUnlock()

	if rcObs == nil {
		t.Fatalf("recompObserversClockSkew not registered after StartAnalyticsRecomputers (issue #1265 not fixed)")
	}
	if rcNodes == nil {
		t.Fatalf("recompNodesClockSkew not registered after StartAnalyticsRecomputers (issue #1265 not fixed)")
	}
	if rcObs.Load() == nil {
		t.Fatalf("recompObserversClockSkew snapshot is nil after initial compute")
	}
	if rcNodes.Load() == nil {
		t.Fatalf("recompNodesClockSkew snapshot is nil after initial compute")
	}
}

func TestClockSkewHandlersSteadyStateLatency(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	stop := store.StartAnalyticsRecomputers(50 * time.Millisecond)
	defer stop()
	time.Sleep(100 * time.Millisecond)

	s := &Server{store: store}

	endpoints := []struct {
		name    string
		path    string
		handler http.HandlerFunc
	}{
		{"observers", "/api/observers/clock-skew", s.handleObserverClockSkew},
		{"nodes", "/api/nodes/clock-skew", s.handleFleetClockSkew},
	}

	for _, ep := range endpoints {
		ep := ep
		t.Run(ep.name, func(t *testing.T) {
			const readers = 8
			const perReader = 25
			var (
				mu      sync.Mutex
				samples []time.Duration
				wg      sync.WaitGroup
			)
			wg.Add(readers)
			for r := 0; r < readers; r++ {
				go func() {
					defer wg.Done()
					for i := 0; i < perReader; i++ {
						rr := httptest.NewRecorder()
						req := httptest.NewRequest(http.MethodGet, ep.path, nil)
						t0 := time.Now()
						ep.handler(rr, req)
						dt := time.Since(t0)
						if rr.Code != http.StatusOK {
							t.Errorf("%s status = %d, want 200", ep.path, rr.Code)
						}
						mu.Lock()
						samples = append(samples, dt)
						mu.Unlock()
					}
				}()
			}
			wg.Wait()

			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			p99 := samples[int(float64(len(samples))*0.99)]
			if p99 > 100*time.Millisecond {
				t.Fatalf("%s p99 latency = %v over %d reqs, want <100ms (recomputer snapshot)", ep.path, p99, len(samples))
			}
		})
	}
}
