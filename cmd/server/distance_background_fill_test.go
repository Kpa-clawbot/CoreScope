package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Run in a child because resetting an in-use sync.Once can fatally unlock an
// unlocked mutex. The parent still fails on an assertion instead of crashing
// the entire server test suite when this regression is reintroduced.
func TestDistanceBackgroundFillInvalidatesInFlightBuild(t *testing.T) {
	const childEnv = "CORESCOPE_TEST_DISTANCE_FILL_CHILD"
	if os.Getenv(childEnv) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDistanceBackgroundFillInvalidatesInFlightBuild$", "-test.v")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("background fill must preserve invalidation and allow a complete rebuild: %v\n%s", err, output)
		}
		return
	}

	db := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedTestData(t, db)
	mustExecDB(t, db, `UPDATE observations SET path_json='["aa","11"]' WHERE transmission_id IN (1,3)`)
	// Keep the two cold observations below the 5% debounce threshold. A
	// growth-triggered rebuild must not hide a lost background-fill invalidation.
	for i := 0; i < 100; i++ {
		mustExecDB(t, db, fmt.Sprintf(`INSERT INTO observations (transmission_id,observer_idx,path_json,timestamp)
			SELECT 1,1,'["%04x"]',timestamp FROM observations WHERE id=1`, i))
	}
	store := NewPacketStore(db, &PacketStoreConfig{HotStartupHours: 2, RetentionHours: 48})
	if err := store.LoadChunked(100); err != nil {
		t.Fatal(err)
	}
	if !store.LoadComplete() || len(store.packets) != 1 || store.totalObs != 102 {
		t.Fatalf("want a completed hot window with one packet and 102 observations, got %d / %d", len(store.packets), store.totalObs)
	}

	computed, releaseCompute := make(chan struct{}), make(chan struct{})
	rebuildEntered, releaseRebuild := make(chan struct{}), make(chan struct{})
	var releaseComputeOnce, releaseRebuildOnce sync.Once
	var builds, computes atomic.Int32
	store.distanceBuildHook = func() {
		if builds.Add(1) == 2 {
			close(rebuildEntered)
			<-releaseRebuild
		}
	}
	rc := newAnalyticsRecomputer("distance", time.Hour, func() interface{} {
		result := store.computeAnalyticsDistance("", "")
		if computes.Add(1) == 2 {
			// The first build has released the store lock and captured its
			// hot-window snapshot, but has not yet published readiness.
			close(computed)
			<-releaseCompute
		}
		return result
	})
	store.recompDistance = rc
	rc.Start()
	t.Cleanup(func() {
		releaseComputeOnce.Do(func() { close(releaseCompute) })
		releaseRebuildOnce.Do(func() { close(releaseRebuild) })
		waitForTest(t, "distance build cleanup", func() bool { return !store.DistanceIndexBuilding() })
		rc.Stop()
	})
	srv := &Server{store: store}
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.handleAnalyticsDistance(w, httptest.NewRequest(http.MethodGet, "/api/analytics/distance", nil))
		return w
	}
	if w := request(); w.Code != http.StatusAccepted {
		t.Fatalf("first request = %d, want 202", w.Code)
	}
	select {
	case <-computed:
	case <-time.After(3 * time.Second):
		t.Fatal("lazy build did not reach snapshot publication")
	}
	store.loadBackgroundChunks()
	if !store.backgroundLoadDone.Load() || len(store.packets) != 3 {
		t.Fatal("background fill must finish while the old snapshot is paused")
	}
	if w := request(); w.Code != http.StatusAccepted || builds.Load() != 1 {
		t.Fatalf("invalidated in-flight build must remain single-flight: status=%d builds=%d", w.Code, builds.Load())
	}
	releaseComputeOnce.Do(func() { close(releaseCompute) })
	waitForTest(t, "invalidated distance build completion", func() bool { return !store.DistanceIndexBuilding() })
	if store.DistanceIndexBuilt() {
		t.Fatal("the hot-window build overwrote the background-fill invalidation")
	}

	var callers sync.WaitGroup
	for i := 0; i < 10; i++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			if w := request(); w.Code != http.StatusAccepted || w.Header().Get("Retry-After") == "" {
				t.Errorf("rebuild request = %d, want 202 with Retry-After", w.Code)
			}
		}()
	}
	callers.Wait()
	select {
	case <-rebuildEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("next request did not rebuild after background fill")
	}
	if builds.Load() != 2 {
		t.Fatalf("want one initial build and one coalesced rebuild, got %d", builds.Load())
	}
	releaseRebuildOnce.Do(func() { close(releaseRebuild) })
	waitForTest(t, "complete distance rebuild", store.DistanceIndexBuilt)
	w := request()
	if w.Code != http.StatusOK {
		t.Fatalf("rebuilt distance response = %d, want 200", w.Code)
	}
	var body struct {
		TopPaths []DistancePath `json:"topPaths"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, path := range body.TopPaths {
		if path.Hash == "def456abc1230099" && path.TotalDist > 0 {
			return
		}
	}
	t.Fatalf("ready response omitted the background-loaded path: %s", w.Body.String())
}
