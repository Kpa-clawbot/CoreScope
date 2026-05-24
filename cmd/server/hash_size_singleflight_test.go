package main

import (
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestGetNodeHashSizeInfo_Singleflight asserts that N concurrent callers
// with a cold cache trigger at most ONE computeNodeHashSizeInfo invocation.
// Anti-tautology: remove the in-flight channel logic and this test fails.
func TestGetNodeHashSizeInfo_Singleflight(t *testing.T) {
	store := newTestPacketStore()

	var computeCount int32

	orig := computeHashSizeInfoFn
	defer func() { computeHashSizeInfoFn = orig }()
	computeHashSizeInfoFn = func(s *PacketStore) map[string]*hashSizeNodeInfo {
		atomic.AddInt32(&computeCount, 1)
		time.Sleep(50 * time.Millisecond) // ensure goroutines overlap
		return map[string]*hashSizeNodeInfo{}
	}

	const N = 10
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			store.GetNodeHashSizeInfo()
		}()
	}
	wg.Wait()

	got := atomic.LoadInt32(&computeCount)
	if got != 1 {
		t.Fatalf("expected exactly 1 computeNodeHashSizeInfo call under singleflight, got %d", got)
	}
}

// TestGetNodeHashSizeInfo_TTL_30s asserts that a warm cache is reused within
// 30s and that a second call does not recompute.
func TestGetNodeHashSizeInfo_TTL_30s(t *testing.T) {
	store := newTestPacketStore()

	var computeCount int32
	orig := computeHashSizeInfoFn
	defer func() { computeHashSizeInfoFn = orig }()
	computeHashSizeInfoFn = func(s *PacketStore) map[string]*hashSizeNodeInfo {
		atomic.AddInt32(&computeCount, 1)
		return map[string]*hashSizeNodeInfo{"pk": {}}
	}

	store.GetNodeHashSizeInfo() // prime the cache
	store.GetNodeHashSizeInfo() // should hit cache

	got := atomic.LoadInt32(&computeCount)
	if got != 1 {
		t.Fatalf("expected 1 compute call (second should be cached), got %d", got)
	}
}

// TestGetMultiByteCapMap_Singleflight asserts that N concurrent callers
// with a cold cache trigger at most ONE computeMultiByteCapability call.
// Anti-tautology: remove the in-flight channel logic and this test fails.
func TestGetMultiByteCapMap_Singleflight(t *testing.T) {
	// Set up an in-memory database for analytics queries
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)

	// Create minimal schema needed for GetAnalyticsHashSizes
	schema := `
		CREATE TABLE nodes (
			public_key TEXT PRIMARY KEY,
			name TEXT,
			role TEXT,
			lat REAL,
			lon REAL
		);
	`
	if _, err := conn.Exec(schema); err != nil {
		t.Fatal(err)
	}

	store := &PacketStore{
		db:            &DB{conn: conn},
		packets:       []*StoreTx{},
		byNode:        make(map[string][]*StoreTx),
		nodeCache:     []nodeInfo{},
		nodePM:        &prefixMap{},
		rfCache:       make(map[string]*cachedResult),
		hashCache:     make(map[string]*cachedResult),
		areaNodeCache: make(map[string]map[string]bool),
	}

	var computeCount int32
	origComputeFn := computeMultiByteCapFn
	defer func() { computeMultiByteCapFn = origComputeFn }()
	computeMultiByteCapFn = func(s *PacketStore, adopterSizes map[string]int) []MultiByteCapEntry {
		atomic.AddInt32(&computeCount, 1)
		time.Sleep(50 * time.Millisecond)
		return nil
	}

	const N = 10
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			store.GetMultiByteCapMap()
		}()
	}
	wg.Wait()

	got := atomic.LoadInt32(&computeCount)
	if got != 1 {
		t.Fatalf("expected exactly 1 computeMultiByteCapability call under singleflight, got %d", got)
	}
}
