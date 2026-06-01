package main

import (
	"sync"
	"testing"
	"time"
)

// TestGetStoreStats_CacheHit verifies that a second call within 30s returns
// the cached observation counts without re-querying the database.
func TestGetStoreStats_CacheHit(t *testing.T) {
	srv, _ := setupTestServer(t)
	store := srv.store

	store.statsCacheMu.Lock()
	store.statsCacheTime = time.Now()
	store.statsLastHour = 42
	store.statsLast24h = 777
	store.statsCacheMu.Unlock()

	st, err := store.GetStoreStats()
	if err != nil {
		t.Fatalf("GetStoreStats: %v", err)
	}
	if st.PacketsLastHour != 42 {
		t.Errorf("cache hit: PacketsLastHour want 42 got %d", st.PacketsLastHour)
	}
	if st.PacketsLast24h != 777 {
		t.Errorf("cache hit: PacketsLast24h want 777 got %d", st.PacketsLast24h)
	}
}

// TestGetStoreStats_CacheExpiry verifies that a cache older than 30s is
// discarded and the database query re-runs to refresh the values.
func TestGetStoreStats_CacheExpiry(t *testing.T) {
	srv, _ := setupTestServer(t)
	store := srv.store

	store.statsCacheMu.Lock()
	store.statsCacheTime = time.Now().Add(-35 * time.Second)
	store.statsLastHour = 9999
	store.statsLast24h = 9999
	store.statsCacheMu.Unlock()

	st, err := store.GetStoreStats()
	if err != nil {
		t.Fatalf("GetStoreStats: %v", err)
	}
	if st.PacketsLastHour == 9999 || st.PacketsLast24h == 9999 {
		t.Errorf("stale cache not expired: got PacketsLastHour=%d PacketsLast24h=%d — DB values expected, not sentinel",
			st.PacketsLastHour, st.PacketsLast24h)
	}

	store.statsCacheMu.Lock()
	age := time.Since(store.statsCacheTime)
	store.statsCacheMu.Unlock()
	if age > 5*time.Second {
		t.Errorf("cache not refreshed after expiry: statsCacheTime age=%v", age)
	}
}

// TestGetStoreStats_CacheConcurrentReaders verifies that 100 concurrent
// callers produce no data race on the stats cache fields.
// Run with: go test -race ./... -run TestGetStoreStats_CacheConcurrentReaders
func TestGetStoreStats_CacheConcurrentReaders(t *testing.T) {
	srv, _ := setupTestServer(t)
	store := srv.store

	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.GetStoreStats(); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent GetStoreStats: %v", err)
	}
}
