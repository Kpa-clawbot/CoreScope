package main

// Analytics recomputers after the startup load.
//
// main.go starts the recomputers as soon as the FIRST load chunk is in
// memory. Their initial compute therefore only sees that chunk (the
// oldest ids), and the next compute used to wait a full interval
// (5 min by default). These tests pin the contract:
//
//   - the store exposes a signal that fires only after RunStartupLoad
//     has finished, background fill included;
//   - every recomputer recomputes as soon as that signal fires;
//   - a pass that started before the signal never ends the #1659
//     warm-up, and a snapshot served after the force timeout is
//     replaced as soon as the load is done.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

func isClosedForTest(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func waitForTest(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// The startup-load signal must stay open while the background fill is
// still running (LoadComplete is already true at that point) and close
// once RunStartupLoad returns.
func TestStartupLoadDone_ClosesOnlyAfterBackgroundFill(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	createTestDBSpreadOverDays(t, dbPath, 100, 14, time.Now().UTC().Unix())
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()
	store := NewPacketStore(db, &PacketStoreConfig{RetentionHours: 14 * 24, HotStartupHours: 24})

	var closedAtBgEntry, loadCompleteAtBgEntry atomic.Bool
	store.bgLoaderEntryHook = func() {
		closedAtBgEntry.Store(isClosedForTest(store.StartupLoadDone()))
		loadCompleteAtBgEntry.Store(store.LoadComplete())
	}
	if isClosedForTest(store.StartupLoadDone()) {
		t.Fatal("StartupLoadDone closed before RunStartupLoad ran")
	}
	if err := store.RunStartupLoad(500); err != nil {
		t.Fatalf("RunStartupLoad: %v", err)
	}
	if !loadCompleteAtBgEntry.Load() {
		t.Fatal("fixture precondition: LoadComplete should already be true when the background fill starts")
	}
	if closedAtBgEntry.Load() {
		t.Fatal("StartupLoadDone closed before the background fill ran")
	}
	if !isClosedForTest(store.StartupLoadDone()) {
		t.Fatal("StartupLoadDone not closed after RunStartupLoad returned")
	}
}

// A failed load is terminal too: nothing more will be loaded, so the
// signal must close instead of leaving the recomputers waiting forever.
func TestStartupLoadDone_ClosesWhenLoadFails(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	createTestDBSpreadOverDays(t, dbPath, 10, 1, time.Now().UTC().Unix())
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	store := NewPacketStore(db, &PacketStoreConfig{RetentionHours: 24, HotStartupHours: 1})
	db.conn.Close()
	if err := store.RunStartupLoad(500); err == nil {
		t.Fatal("fixture precondition: RunStartupLoad on a closed conn should fail")
	}
	if !isClosedForTest(store.StartupLoadDone()) {
		t.Fatal("StartupLoadDone not closed after a failed RunStartupLoad")
	}
}

// Derived caches with their own TTL (node hash-size info 15s, clock-skew
// engine 30s) may hold values computed from the partial store. The
// recomputes that follow the signal read them, so they must be dropped.
func TestStartupLoadDone_DropsDerivedCaches(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)

	store.hashSizeInfoMu.Lock()
	store.hashSizeInfoCache = map[string]*hashSizeNodeInfo{"partial": {}}
	store.hashSizeInfoAt = time.Now()
	store.hashSizeInfoMu.Unlock()
	store.clockSkew.mu.Lock()
	store.clockSkew.lastComputed = time.Now()
	store.clockSkew.mu.Unlock()

	store.signalStartupLoadDone()

	store.hashSizeInfoMu.Lock()
	hashInfo := store.hashSizeInfoCache
	store.hashSizeInfoMu.Unlock()
	if hashInfo != nil {
		t.Fatal("hash-size info cache survived the startup-load signal")
	}
	store.clockSkew.mu.RLock()
	last := store.clockSkew.lastComputed
	store.clockSkew.mu.RUnlock()
	if !last.IsZero() {
		t.Fatal("clock-skew engine still considers its pre-load result fresh")
	}
}

// The recomputer must not wait a full interval once the store is loaded.
func TestRecomputeWhenLoaded_RunsImmediately(t *testing.T) {
	var loadedFlag atomic.Bool
	rc := newAnalyticsRecomputer("t", time.Hour, func() interface{} {
		if loadedFlag.Load() {
			return "full"
		}
		return "partial"
	})
	rc.Start()
	defer rc.Stop()
	if got := rc.Load(); got != "partial" {
		t.Fatalf("initial snapshot = %v, want partial", got)
	}

	loaded := make(chan struct{})
	stop := make(chan struct{})
	defer close(stop)
	go recomputeWhenLoaded(loaded, stop, []*analyticsRecomputer{rc})

	loadedFlag.Store(true)
	close(loaded)
	waitForTest(t, "snapshot recomputed after load", func() bool { return rc.Load() == "full" })
	if runs := rc.ComputeRuns(); runs != 2 {
		t.Fatalf("ComputeRuns = %d, want 2 (initial + post-load)", runs)
	}
}

// Recomputers run one after the other, in slice order, so a recomputer
// that reads another one's snapshot (roles reads nodes-clock-skew) sees
// the post-load version.
func TestRecomputeWhenLoaded_RunsInOrder(t *testing.T) {
	var loadedFlag atomic.Bool
	var skewSnapshot atomic.Value
	skew := newAnalyticsRecomputer("skew", time.Hour, func() interface{} {
		v := "partial"
		if loadedFlag.Load() {
			v = "full"
		}
		time.Sleep(20 * time.Millisecond)
		skewSnapshot.Store(v)
		return v
	})
	roles := newAnalyticsRecomputer("roles", time.Hour, func() interface{} {
		return skewSnapshot.Load()
	})
	skew.Start()
	roles.Start()
	defer skew.Stop()
	defer roles.Stop()

	loaded := make(chan struct{})
	stop := make(chan struct{})
	defer close(stop)
	done := make(chan struct{})
	go func() {
		recomputeWhenLoaded(loaded, stop, []*analyticsRecomputer{skew, roles})
		close(done)
	}()
	loadedFlag.Store(true)
	close(loaded)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("recomputeWhenLoaded did not finish")
	}
	if got := roles.Load(); got != "full" {
		t.Fatalf("roles snapshot = %v, want full (it ran before the skew recompute finished)", got)
	}
}

// The post-load order used by StartAnalyticsRecomputers: gated
// recomputers first (their 503 ends soonest), roles after
// nodes-clock-skew (roles reads that snapshot).
func TestAnalyticsRecomputers_PostLoadOrder(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	stop := store.StartAnalyticsRecomputers(time.Hour)
	defer stop()

	store.analyticsRecomputerMu.RLock()
	list := store.analyticsRecomputersLocked()
	store.analyticsRecomputerMu.RUnlock()
	pos := map[string]int{}
	for i, rc := range list {
		pos[rc.name] = i
	}
	if len(list) != 11 || len(pos) != 11 {
		t.Fatalf("want 11 distinct recomputers, got %d (%d distinct)", len(list), len(pos))
	}
	for _, name := range []string{"rf", "topology", "channels"} {
		if pos[name] > 2 {
			t.Errorf("%s at position %d, want among the first three", name, pos[name])
		}
	}
	if pos["nodes-clock-skew"] > pos["roles"] {
		t.Errorf("roles (%d) runs before nodes-clock-skew (%d)", pos["roles"], pos["nodes-clock-skew"])
	}
}

// A pass that STARTED before the load finished must not end the warm-up,
// even when the load finishes while that pass is still running.
func TestWarmup_PassStartedBeforeLoadDoesNotOpenGate(t *testing.T) {
	loaded := make(chan struct{})
	releaseSecond := make(chan struct{})
	var calls atomic.Int32
	rc := newAnalyticsRecomputer("t", time.Hour, func() interface{} {
		switch calls.Add(1) {
		case 1:
			close(loaded) // load finishes while the first pass is running
			return "partial"
		default:
			<-releaseSecond
			return "full"
		}
	})
	rc.setWarmupReadyGate_1659(func() bool { return isClosedForTest(loaded) })
	rc.Start()
	defer rc.Stop()

	if !rc.IsWarmingUp_1659() {
		t.Fatal("gate opened on a pass that started before the store was loaded")
	}

	stop := make(chan struct{})
	defer close(stop)
	go recomputeWhenLoaded(loaded, stop, []*analyticsRecomputer{rc})
	waitForTest(t, "post-load pass started", func() bool { return calls.Load() == 2 })
	if !rc.IsWarmingUp_1659() {
		t.Fatal("gate opened while the only post-load pass is still running")
	}
	close(releaseSecond)
	waitForTest(t, "gate opens after the post-load pass", func() bool { return !rc.IsWarmingUp_1659() })
	if got := rc.Load(); got != "full" {
		t.Fatalf("gate open but snapshot = %v, want full", got)
	}
}

// Force-open timeout followed by the load finishing: the first-chunk
// snapshot that was served after the timeout is replaced right away.
func TestWarmup_ForceOpenedSnapshotReplacedOnLoad(t *testing.T) {
	prev := warmupForceTimeout
	warmupForceTimeout = 30 * time.Millisecond
	defer func() { warmupForceTimeout = prev }()

	loaded := make(chan struct{})
	rc := newAnalyticsRecomputer("t", time.Hour, func() interface{} {
		if isClosedForTest(loaded) {
			return "full"
		}
		return "partial"
	})
	rc.setWarmupReadyGate_1659(func() bool { return isClosedForTest(loaded) })
	rc.Start()
	defer rc.Stop()
	stop := make(chan struct{})
	defer close(stop)
	go recomputeWhenLoaded(loaded, stop, []*analyticsRecomputer{rc})

	waitForTest(t, "force timeout opens the gate", func() bool { return !rc.IsWarmingUp_1659() })
	if got := rc.Load(); got != "partial" {
		t.Fatalf("fixture precondition: forced-open snapshot = %v, want partial", got)
	}
	if !rc.FirstPassDoneAt_1659().IsZero() {
		t.Fatal("a pre-load pass was recorded as the first full pass")
	}

	close(loaded)
	waitForTest(t, "snapshot replaced after load", func() bool { return rc.Load() == "full" })
	waitForTest(t, "first full pass recorded", func() bool { return !rc.FirstPassDoneAt_1659().IsZero() })
}

// The periodic ticker restarts from the post-load compute, so the next
// periodic pass is a full interval after it instead of a redundant pass
// on the original phase a moment later.
func TestRecomputeNow_RestartsTicker(t *testing.T) {
	const interval = 400 * time.Millisecond
	rc := newAnalyticsRecomputer("t", interval, func() interface{} { return 1 })
	rc.Start() // run 1 at t0; unreset ticker would fire at t0+400ms
	defer rc.Stop()

	time.Sleep(250 * time.Millisecond)
	rc.RecomputeNow()                  // run 2 at ~250ms; next tick now ~650ms
	time.Sleep(300 * time.Millisecond) // ~550ms: past the old phase, before the new one
	if runs := rc.ComputeRuns(); runs != 2 {
		t.Fatalf("ComputeRuns = %d at ~550ms, want 2 (ticker not restarted by RecomputeNow)", runs)
	}
}

// RecomputeNow must not hang when the recomputer is stopped.
func TestRecomputeNow_ReturnsWhenStopped(t *testing.T) {
	rc := newAnalyticsRecomputer("t", time.Hour, func() interface{} { return 1 })
	rc.Start()
	rc.Stop()
	done := make(chan struct{})
	go func() {
		rc.RecomputeNow()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RecomputeNow blocked on a stopped recomputer")
	}
}

// After a lazy distance-index build, the distance recomputer snapshot
// must already reflect the new index when the handler stops answering 202.
func TestDistanceIndexBuild_RefreshesRecomputerBeforeReportingBuilt(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("Load(): %v", err)
	}
	stop := store.StartAnalyticsRecomputers(time.Hour)
	defer stop()
	before := store.recompDistance.ComputeRuns()

	store.TriggerDistanceIndexBuild()
	waitForTest(t, "distance index built", store.DistanceIndexBuilt)
	if runs := store.recompDistance.ComputeRuns(); runs <= before {
		t.Fatalf("distance index reported built while recomputer still serves the pre-build snapshot (runs %d -> %d)", before, runs)
	}
}

// End to end: recomputers started at the first chunk, background fill
// still pending. Gated endpoints answer 503 until the load is done and
// then serve data from the whole store without waiting for the interval.
// Ungated recomputers keep answering (no new 503) and are recomputed
// right after the load.
func TestAnalyticsRecomputers_FullDataRightAfterStartupLoad(t *testing.T) {
	const totalRows = 100
	dbPath := filepath.Join(t.TempDir(), "test.db")
	createTestDBSpreadOverDays(t, dbPath, totalRows, 14, time.Now().UTC().Unix())
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()
	store := NewPacketStore(db, &PacketStoreConfig{RetentionHours: 14 * 24, HotStartupHours: 24})

	bgEntered := make(chan struct{})
	releaseBg := make(chan struct{})
	store.bgLoaderEntryHook = func() {
		close(bgEntered)
		<-releaseBg
	}
	loadErr := make(chan error, 1)
	go func() { loadErr <- store.RunStartupLoad(500) }()
	<-bgEntered

	stop := store.StartAnalyticsRecomputers(time.Hour)
	defer stop()

	srv := NewServer(db, &Config{Port: 3000}, NewHub())
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}

	for _, p := range []string{"/api/analytics/rf", "/api/analytics/topology", "/api/analytics/channels"} {
		if w := get(p); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s before full load: got %d, want 503", p, w.Code)
		}
	}
	for _, p := range []string{"/api/analytics/hash-sizes", "/api/analytics/hash-collisions", "/api/analytics/roles"} {
		if w := get(p); w.Code != http.StatusOK {
			t.Fatalf("%s before full load: got %d, want 200 (no new warm-up 503)", p, w.Code)
		}
	}

	store.analyticsRecomputerMu.RLock()
	all := map[string]*analyticsRecomputer{
		"topology": store.recompTopology, "rf": store.recompRF, "distance": store.recompDistance,
		"channels": store.recompChannels, "hash-collisions": store.recompHashCollisions,
		"hash-sizes": store.recompHashSizes, "roles": store.recompRoles,
		"observers-clock-skew": store.recompObserversClockSkew, "nodes-clock-skew": store.recompNodesClockSkew,
	}
	store.analyticsRecomputerMu.RUnlock()
	runsBefore := map[string]int64{}
	for name, rc := range all {
		runsBefore[name] = rc.ComputeRuns()
	}

	close(releaseBg)
	if err := <-loadErr; err != nil {
		t.Fatalf("RunStartupLoad: %v", err)
	}
	store.mu.RLock()
	inMemory := len(store.packets)
	store.mu.RUnlock()
	if inMemory != totalRows {
		t.Fatalf("fixture precondition: %d packets in memory after load, want %d", inMemory, totalRows)
	}

	waitForTest(t, "rf gate opens after full load", func() bool { return get("/api/analytics/rf").Code == http.StatusOK })
	var rf map[string]interface{}
	if err := json.Unmarshal(get("/api/analytics/rf").Body.Bytes(), &rf); err != nil {
		t.Fatalf("rf body: %v", err)
	}
	if got, _ := rf["totalTransmissions"].(float64); int(got) != totalRows {
		t.Fatalf("rf totalTransmissions = %v right after full load, want %d (first-chunk snapshot still served)", rf["totalTransmissions"], totalRows)
	}
	for _, p := range []string{"/api/analytics/topology", "/api/analytics/channels"} {
		if w := get(p); w.Code != http.StatusOK {
			t.Fatalf("%s after full load: got %d, want 200", p, w.Code)
		}
	}
	for name, rc := range all {
		name, rc := name, rc
		waitForTest(t, name+" recomputed after full load", func() bool { return rc.ComputeRuns() > runsBefore[name] })
	}
}
