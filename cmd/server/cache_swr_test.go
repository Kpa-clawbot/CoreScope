package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// #2146: the node, region-observer and area-node caches are read by callers
// that hold s.mu. A cache past its TTL must be served as-is while a single
// background refresh runs the SQL, so no caller holding s.mu waits on the
// 4-connection read pool.

// blockingLoadHook returns a cacheLoadHook that counts calls per kind and
// blocks every call for kind until release is closed.
func blockingLoadHook(kind string) (hook func(string), calls *int64, entered chan struct{}, release chan struct{}) {
	calls = new(int64)
	entered = make(chan struct{}, 64)
	release = make(chan struct{})
	hook = func(k string) {
		if k != kind {
			return
		}
		atomic.AddInt64(calls, 1)
		entered <- struct{}{}
		<-release
	}
	return hook, calls, entered, release
}

// returnsWithin runs fn and fails the test if it does not return within d.
func returnsWithin(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s blocked for more than %v", what, d)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNodeCacheServesStaleWhileRefreshing(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	s := &PacketStore{db: db}

	stale := []nodeInfo{{PublicKey: "stale0001", Name: "Stale"}}
	s.nodeCache, s.nodePM = stale, buildPrefixMap(stale)
	s.nodeCacheTime = time.Now().Add(-time.Hour)

	hook, calls, entered, release := blockingLoadHook("nodes")
	s.cacheLoadHook = hook

	var got []nodeInfo
	returnsWithin(t, time.Second, "stale getCachedNodesAndPM", func() {
		got, _ = s.getCachedNodesAndPM()
	})
	if len(got) != 1 || got[0].PublicKey != "stale0001" {
		t.Fatalf("want the stale cache served, got %v", got)
	}
	<-entered // the background refresh reached the SQL

	// Further stale callers while the refresh is in flight: served from the
	// stale cache, and they do not start a second refresh.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.getCachedNodesAndPM() }()
	}
	returnsWithin(t, time.Second, "concurrent stale callers", wg.Wait)
	if n := atomic.LoadInt64(calls); n != 1 {
		t.Fatalf("want exactly 1 background refresh, got %d", n)
	}

	close(release)
	waitUntil(t, "refreshed node cache", func() bool {
		nodes, _ := s.getCachedNodesAndPM()
		return len(nodes) > 0 && nodes[0].PublicKey != "stale0001"
	})
}

func TestNodeCacheInvalidatedBuildsInline(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	s := &PacketStore{db: db}

	stale := []nodeInfo{{PublicKey: "stale0001", Name: "Stale"}}
	s.nodeCache, s.nodePM = stale, buildPrefixMap(stale)
	s.nodeCacheTime = time.Now()
	s.InvalidateNodeCache()

	var calls int64
	s.cacheLoadHook = func(k string) {
		if k == "nodes" {
			atomic.AddInt64(&calls, 1)
		}
	}
	nodes, _ := s.getCachedNodesAndPM()
	if atomic.LoadInt64(&calls) != 1 {
		t.Fatalf("an invalidated cache must rebuild inline, hook calls = %d", calls)
	}
	for _, n := range nodes {
		if n.PublicKey == "stale0001" {
			t.Fatal("an invalidated cache must not serve the old node list")
		}
	}
	if len(nodes) == 0 {
		t.Fatal("want the seeded nodes after an inline rebuild")
	}
}

// A refresh that started before InvalidateNodeCache read the DB before the
// change the invalidation announces, so it must not mark the cache fresh.
func TestNodeCacheRefreshDoesNotOverrideInvalidation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	s := &PacketStore{db: db}

	stale := []nodeInfo{{PublicKey: "stale0001", Name: "Stale"}}
	s.nodeCache, s.nodePM = stale, buildPrefixMap(stale)
	s.nodeCacheTime = time.Now().Add(-time.Hour)

	hook, _, entered, release := blockingLoadHook("nodes")
	s.cacheLoadHook = hook
	s.getCachedNodesAndPM()
	<-entered

	s.InvalidateNodeCache()
	close(release)
	waitUntil(t, "background refresh to finish", func() bool { return !s.cacheRefresh.running("nodes") })

	s.cacheMu.Lock()
	builtAt := s.nodeCacheTime
	s.cacheMu.Unlock()
	if !builtAt.IsZero() {
		t.Fatal("a refresh that began before InvalidateNodeCache marked the cache fresh")
	}
}

func TestRegionObsServesStaleWhileRefreshing(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	s := &PacketStore{db: db}

	s.regionObsCache = map[string]map[string]bool{"SJC": {"stale-obs": true}}
	s.regionObsCacheTime = time.Now().Add(-time.Hour)

	hook, calls, entered, release := blockingLoadHook("regionObs")
	s.cacheLoadHook = hook

	var got map[string]bool
	returnsWithin(t, time.Second, "stale resolveRegionObservers", func() {
		got = s.resolveRegionObservers("SJC")
	})
	if !got["stale-obs"] {
		t.Fatalf("want the stale region set served, got %v", got)
	}
	<-entered
	returnsWithin(t, time.Second, "second stale caller", func() { s.resolveRegionObservers("SJC") })
	if n := atomic.LoadInt64(calls); n != 1 {
		t.Fatalf("want exactly 1 background refresh, got %d", n)
	}

	close(release)
	waitUntil(t, "refreshed region cache", func() bool {
		m := s.resolveRegionObservers("SJC")
		return len(m) > 0 && !m["stale-obs"]
	})
}

// A cold region still queries inline, but it must not hold regionObsMu while
// it does: other regions are served from the cache meanwhile.
func TestRegionObsColdMissDoesNotBlockCachedRegion(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	s := &PacketStore{db: db}

	s.regionObsCache = map[string]map[string]bool{"SJC": {"obs-sjc": true}}
	s.regionObsCacheTime = time.Now()

	hook, _, entered, release := blockingLoadHook("regionObs")
	s.cacheLoadHook = hook
	go s.resolveRegionObservers("LAX")
	<-entered

	returnsWithin(t, time.Second, "cached region during a cold miss", func() {
		if !s.resolveRegionObservers("SJC")["obs-sjc"] {
			t.Error("want the cached SJC set")
		}
	})
	close(release)
}

func TestAreaNodesServesStaleWhileRefreshing(t *testing.T) {
	db := setupTestDBv2(t)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, lat, lon) VALUES ('pk-fresh', 50.85, 4.35)`)
	cfg := &Config{Areas: map[string]AreaEntry{
		"BEL": {Label: "Belgium", Polygon: [][2]float64{{50.0, 2.5}, {51.5, 2.5}, {51.5, 6.4}, {50.0, 6.4}}},
	}}
	s := newTestStoreWithDB(t, db, cfg)
	s.areaNodeCache["BEL"] = map[string]bool{"pk-stale": true}
	s.areaNodeCacheTimes["BEL"] = time.Now().Add(-time.Hour)

	hook, calls, entered, release := blockingLoadHook("areaNodes")
	s.cacheLoadHook = hook

	var got map[string]bool
	returnsWithin(t, time.Second, "stale resolveAreaNodes", func() {
		got = s.resolveAreaNodes("BEL")
	})
	if !got["pk-stale"] {
		t.Fatalf("want the stale area set served, got %v", got)
	}
	<-entered
	returnsWithin(t, time.Second, "second stale caller", func() { s.resolveAreaNodes("BEL") })
	if n := atomic.LoadInt64(calls); n != 1 {
		t.Fatalf("want exactly 1 background refresh, got %d", n)
	}

	close(release)
	waitUntil(t, "refreshed area cache", func() bool { return s.resolveAreaNodes("BEL")["pk-fresh"] })
}
