package main

import (
	"encoding/json"
	"net/http"
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

// #2227: production uses RunStartupLoad/LoadChunked, not the legacy Load.
// Keep startup's required indexes, but do no distance-index work until asked.
func TestLoadChunkedDistanceIndexRemainsLazy(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	// The existing fixture has two located nodes; this path makes an eager
	// distance build observable instead of passing with empty distance data.
	mustExecDB(t, db, `UPDATE observations SET path_json = '["aa","11"]' WHERE id = 1`)
	store := NewPacketStore(db, nil)
	if err := store.RunStartupLoad(2); err != nil {
		t.Fatal(err)
	}
	if len(store.distHops) != 0 || len(store.distPaths) != 0 || store.DistanceIndexBuilt() {
		t.Fatalf("chunked startup built the lazy distance index: %d hops, %d paths", len(store.distHops), len(store.distPaths))
	}
	if !store.LoadComplete() || !store.SubpathIndexReady() || !store.PathHopIndexReady() || len(store.byPathHop) == 0 {
		t.Fatal("chunked startup must retain path-hop entries and publish its required indexes")
	}
	stop := store.StartAnalyticsRecomputers(time.Hour)
	defer stop()
	// Drain startup's refresh so it cannot hide a missing lazy-build refresh.
	waitForTest(t, "post-load distance snapshot", func() bool { return store.recompDistance.ComputeRuns() >= 2 })
	runsBeforeBuild := store.recompDistance.ComputeRuns()

	entered, release := make(chan struct{}, 1), make(chan struct{})
	var releaseOnce sync.Once
	var builds atomic.Int32
	store.distanceBuildHook = func() { builds.Add(1); entered <- struct{}{}; <-release }
	defer func() {
		releaseOnce.Do(func() { close(release) })
		waitForTest(t, "lazy distance build", store.DistanceIndexBuilt)
	}()
	srv := NewServer(db, &Config{}, nil)
	srv.store = store
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.handleAnalyticsDistance(w, httptest.NewRequest(http.MethodGet, "/api/analytics/distance", nil))
		return w
	}
	if w := request(); w.Code != http.StatusAccepted || w.Header().Get("Retry-After") == "" {
		t.Fatalf("first distance response = %d, want 202 with Retry-After", w.Code)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first distance request did not start the lazy build")
	}
	if w := request(); w.Code != http.StatusAccepted {
		t.Fatalf("distance response during build = %d, want 202", w.Code)
	}
	releaseOnce.Do(func() { close(release) })
	waitForTest(t, "lazy distance build", store.DistanceIndexBuilt)
	w := request()
	if w.Code != http.StatusOK {
		t.Fatalf("distance response after build = %d, want 200", w.Code)
	}
	var body struct {
		TopHops  []DistanceHop  `json:"topHops"`
		TopPaths []DistancePath `json:"topPaths"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.TopHops) == 0 || len(body.TopPaths) == 0 {
		t.Fatalf("ready response served an empty distance snapshot: %s", w.Body.String())
	}
	if store.recompDistance.ComputeRuns() <= runsBeforeBuild {
		t.Fatal("lazy build did not refresh the real distance recomputer before returning 200")
	}
	// A completed build must not be repeated by requests or an explicit trigger.
	store.TriggerDistanceIndexBuild()
	if w := request(); w.Code != http.StatusOK || !store.DistanceIndexBuilt() || builds.Load() != 1 {
		t.Fatalf("ready distance index rebuilt: status=%d, builds=%d", w.Code, builds.Load())
	}
}

func TestDistanceRequestDuringChunkedLoadWaitsForLaterPackets(t *testing.T) {
	db := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedTestData(t, db)
	mustExecDB(t, db, `UPDATE observations SET path_json = '["aa","11"]' WHERE transmission_id = 1`)
	mustExecDB(t, db, `UPDATE observations SET path_json = '["11","aa"]' WHERE transmission_id = 3`)
	store := NewPacketStore(db, nil)
	firstChunk, releaseLoad, loadDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	store.OnChunkLoaded(func(_, total int) {
		if total == 1 {
			close(firstChunk)
			<-releaseLoad
		}
	})
	var loadErr error
	go func() {
		defer close(loadDone)
		loadErr = store.RunStartupLoad(1)
	}()
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseLoad) })
		select {
		case <-loadDone:
		case <-time.After(3 * time.Second):
			t.Error("chunked load did not finish")
		}
	})
	select {
	case <-firstChunk:
	case <-time.After(3 * time.Second):
		t.Fatal("first chunk did not load")
	}
	if store.LoadComplete() || store.LoadProgress() != 1 {
		t.Fatal("fixture must hold the loader after its first packet")
	}
	stop := store.StartAnalyticsRecomputers(time.Hour)
	t.Cleanup(stop)
	buildEntered := make(chan struct{}, 1)
	var builds atomic.Int32
	store.distanceBuildHook = func() { builds.Add(1); buildEntered <- struct{}{} }
	srv := NewServer(db, &Config{}, nil)
	srv.store = store
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.handleAnalyticsDistance(w, httptest.NewRequest(http.MethodGet, "/api/analytics/distance", nil))
		return w
	}
	if w := request(); w.Code != http.StatusAccepted || w.Header().Get("Retry-After") == "" {
		t.Fatalf("distance response while loading = %d, want 202 with Retry-After", w.Code)
	}
	select {
	case <-buildEntered:
		// Finish an erroneous early build while later chunks remain blocked,
		// so it cannot race the loader and accidentally include the full data.
		waitForTest(t, "premature distance build", store.DistanceIndexBuilt)
		t.Error("distance build started before the initial chunks finished")
	case <-time.After(100 * time.Millisecond):
	}
	if w := request(); w.Code != http.StatusAccepted {
		t.Errorf("distance poll while loading = %d, want 202", w.Code)
	}
	releaseOnce.Do(func() { close(releaseLoad) })
	select {
	case <-loadDone:
	case <-time.After(3 * time.Second):
		t.Fatal("chunked load did not finish")
	}
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	waitForTest(t, "post-load distance snapshot", func() bool { return store.recompDistance.ComputeRuns() >= 2 })
	runsBeforeBuild := store.recompDistance.ComputeRuns()
	if w := request(); w.Code != http.StatusAccepted {
		t.Errorf("first distance request after loading = %d, want 202", w.Code)
	}
	waitForTest(t, "full distance build", store.DistanceIndexBuilt)
	w := request()
	if w.Code != http.StatusOK {
		t.Fatalf("distance response after build = %d, want 200", w.Code)
	}
	var body struct {
		TopPaths []DistancePath `json:"topPaths"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	paths := make(map[string]bool, len(body.TopPaths))
	for _, path := range body.TopPaths {
		if path.TotalDist > 0 {
			paths[path.Hash] = true
		}
	}
	if !paths["abc123def4567890"] || !paths["def456abc1230099"] {
		t.Errorf("distance paths must include both first and later chunks: %v", paths)
	}
	if builds.Load() != 1 || store.recompDistance.ComputeRuns() <= runsBeforeBuild {
		t.Errorf("want one full build followed by a fresh recompute; builds=%d, runs=%d -> %d",
			builds.Load(), runsBeforeBuild, store.recompDistance.ComputeRuns())
	}
}

func TestDistanceAfterCompletedHotWindowRemainsAvailable(t *testing.T) {
	for _, tc := range []struct {
		name         string
		done, failed bool
	}{
		{"background pending", false, false},
		{"background failed", false, true},
		{"background failed and terminal", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			defer db.Close()
			seedTestData(t, db)
			mustExecDB(t, db, `UPDATE observations SET path_json = '["aa","11"]' WHERE id = 1`)
			store := NewPacketStore(db, &PacketStoreConfig{HotStartupHours: 2, RetentionHours: 168})
			if err := store.LoadChunked(1); err != nil {
				t.Fatal(err)
			}
			if !store.LoadComplete() {
				t.Fatal("fixture must have a complete initial hot window")
			}
			// These background-fill states must not invalidate the loaded hot window.
			store.backgroundLoadFailed.Store(tc.failed)
			store.backgroundLoadDone.Store(tc.done)
			srv := &Server{store: store}
			request := func() *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				srv.handleAnalyticsDistance(w, httptest.NewRequest(http.MethodGet, "/api/analytics/distance", nil))
				return w
			}
			if w := request(); w.Code != http.StatusAccepted {
				t.Fatalf("first distance request for a complete hot window = %d, want 202", w.Code)
			}
			waitForTest(t, "hot-window distance build", store.DistanceIndexBuilt)
			w := request()
			if w.Code != http.StatusOK {
				t.Fatalf("distance response for a complete hot window = %d, want 200", w.Code)
			}
			var body struct {
				TopPaths []DistancePath `json:"topPaths"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.TopPaths) == 0 {
				t.Fatal("distance response must retain the loaded hot-window data")
			}
		})
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
	// Hold the lazy build open until every concurrent request has been served,
	// so the "requests during the build window -> 202" guarantee is exercised
	// deterministically. Without this the build of the tiny test DB finishes
	// almost instantly, so on a fast/idle machine some requests race past it and
	// get 200 — a flake, not a regression (the code only ever promised 202 for
	// requests that arrive WHILE the build is in flight).
	release := make(chan struct{})
	store.distanceBuildHook = func() { <-release }
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
	close(release) // let the gated build finish
	if got202.Load() != N {
		t.Fatalf("expected all %d concurrent first-window requests to get 202; only %d did", N, got202.Load())
	}
}
