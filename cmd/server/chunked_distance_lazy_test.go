package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoadChunkedDistanceIndexRemainsLazy(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	// Two known, located relay nodes make an eager distance build observable.
	mustExecDB(t, db, `UPDATE observations SET path_json = '["aa","11"]' WHERE id = 1`)
	store := NewPacketStore(db, nil)
	if err := store.RunStartupLoad(2); err != nil {
		t.Fatal(err)
	}
	if len(store.distHops) != 0 || len(store.distPaths) != 0 || store.DistanceIndexBuilt() {
		t.Fatalf("chunked startup built the lazy distance index: %d hops, %d paths", len(store.distHops), len(store.distPaths))
	}
	if !store.LoadComplete() || !store.SubpathIndexReady() || !store.PathHopIndexReady() {
		t.Fatal("chunked load must still publish its required indexes and completion")
	}
	stopRecomputers := store.StartAnalyticsRecomputers(time.Hour)
	defer stopRecomputers()
	// Drain the initial and post-load passes before requesting distance data,
	// so a coincidental post-load refresh cannot hide a missing lazy refresh.
	waitUntil(t, "post-load distance snapshot", func() bool { return store.recompDistance.ComputeRuns() >= 2 })
	runsBeforeBuild := store.recompDistance.ComputeRuns()

	entered, release := make(chan struct{}, 2), make(chan struct{})
	var releaseOnce sync.Once
	var builds atomic.Int32
	store.distanceBuildHook = func() { builds.Add(1); entered <- struct{}{}; <-release }
	defer func() {
		releaseOnce.Do(func() { close(release) })
		waitUntil(t, "lazy distance build", store.DistanceIndexBuilt)
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
	case <-time.After(time.Second):
		t.Fatal("first distance request did not start the lazy build")
	}
	if w := request(); w.Code != http.StatusAccepted {
		t.Fatalf("distance response during build = %d, want 202", w.Code)
	}
	releaseOnce.Do(func() { close(release) })
	waitUntil(t, "lazy distance build", store.DistanceIndexBuilt)
	if builds.Load() != 1 || len(store.distHops) == 0 {
		t.Fatalf("want one nonempty lazy build, got %d builds and %d hops", builds.Load(), len(store.distHops))
	}
	w := request()
	if w.Code != http.StatusOK {
		t.Fatalf("distance response after build = %d, want 200", w.Code)
	}
	var body struct {
		TopHops  []json.RawMessage `json:"topHops"`
		TopPaths []json.RawMessage `json:"topPaths"`
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
}

func TestDistanceRequestDuringChunkedLoadWaitsForLaterPackets(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	mustExecDB(t, db, `UPDATE observations SET path_json = '["aa","11"]' WHERE transmission_id = 1`)
	mustExecDB(t, db, `UPDATE observations SET path_json = '["11","aa"]' WHERE transmission_id = 3`)
	store := NewPacketStore(db, &PacketStoreConfig{HotStartupHours: 0})
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
		case <-time.After(5 * time.Second):
			t.Error("chunked load did not finish")
		}
	})
	select {
	case <-firstChunk:
	case <-time.After(5 * time.Second):
		t.Fatal("first chunk did not load")
	}
	if store.LoadComplete() || store.LoadProgress() != 1 {
		t.Fatal("fixture must hold the loader after its first packet")
	}
	stopRecomputers := store.StartAnalyticsRecomputers(time.Hour)
	defer stopRecomputers()
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
		// Keep the later chunks held until the erroneous early build finishes,
		// so the regression cannot pass by racing the build against the load.
		waitUntil(t, "premature distance build", store.DistanceIndexBuilt)
		t.Error("distance build started before the initial chunks finished")
	case <-time.After(100 * time.Millisecond):
	}
	if w := request(); w.Code != http.StatusAccepted || w.Header().Get("Retry-After") == "" {
		t.Errorf("distance poll while loading = %d, want 202 with Retry-After", w.Code)
	}
	releaseOnce.Do(func() { close(releaseLoad) })
	select {
	case <-loadDone:
	case <-time.After(5 * time.Second):
		t.Fatal("chunked load did not finish")
	}
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	// Let the real post-load recompute settle before the lazy build, so it
	// cannot hide a missing refresh of the newly built distance index.
	waitUntil(t, "post-load distance snapshot", func() bool { return store.recompDistance.ComputeRuns() >= 2 })
	runsBeforeBuild := store.recompDistance.ComputeRuns()
	if w := request(); w.Code != http.StatusAccepted {
		t.Errorf("first distance request after loading = %d, want 202", w.Code)
	}
	waitUntil(t, "full distance build", store.DistanceIndexBuilt)
	w := request()
	if w.Code != http.StatusOK {
		t.Fatalf("distance response after build = %d, want 200", w.Code)
	}
	var body DistanceAnalyticsResponse
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
