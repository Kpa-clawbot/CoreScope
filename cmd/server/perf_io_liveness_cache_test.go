package main

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestReadIngestorSourceLiveness_CachesWithinTTL guards the /healthz
// hot-path TTL cache (PR #1623 round-1 finding 4): readIngestorSourceLiveness
// is called per /healthz probe (LB / k8s / uptime monitors), and every
// call re-reads + re-unmarshals the entire IngestorStats JSON. Within
// the TTL window the function MUST hit a cached parse and avoid the
// re-read.
func TestReadIngestorSourceLiveness_CachesWithinTTL(t *testing.T) {
	dir := t.TempDir()
	statsPath := filepath.Join(dir, "ingestor-stats.json")
	stub := `{
		"sampledAt": "2026-06-07T00:00:00Z",
		"source_liveness": {
			"mqtt-broker-a": {"lastReceiptUnix": 1717000000, "lastMessageUnix": 1716999990}
		}
	}`
	if err := os.WriteFile(statsPath, []byte(stub), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)

	// Swap the read function to a counting wrapper.
	var calls atomic.Int64
	prev := sourceLivenessReadFile
	sourceLivenessReadFile = func(p string) ([]byte, error) {
		calls.Add(1)
		return os.ReadFile(p)
	}
	t.Cleanup(func() {
		sourceLivenessReadFile = prev
		resetSourceLivenessCache()
	})
	resetSourceLivenessCache()

	// 5 sequential calls within <1s — the cache TTL window.
	start := time.Now()
	for i := 0; i < 5; i++ {
		got := readIngestorSourceLiveness()
		if _, ok := got["mqtt-broker-a"]; !ok {
			t.Fatalf("call %d: expected mqtt-broker-a in liveness map, got %+v", i, got)
		}
	}
	elapsed := time.Since(start)
	if elapsed > 800*time.Millisecond {
		t.Fatalf("loop took %s — too slow for a TTL-cache assertion (should be sub-second)", elapsed)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 os.ReadFile call across 5 readIngestorSourceLiveness() calls within TTL, got %d", got)
	}
}

// TestReadIngestorSourceLiveness_InvalidatesOnMTimeChange guards the
// other half of the cache contract: when the underlying stats file
// changes (mtime moves), the cache MUST refresh on the next call.
func TestReadIngestorSourceLiveness_InvalidatesOnMTimeChange(t *testing.T) {
	dir := t.TempDir()
	statsPath := filepath.Join(dir, "ingestor-stats.json")
	stubA := `{"source_liveness": {"a": {"lastReceiptUnix": 1, "lastMessageUnix": 1}}}`
	stubB := `{"source_liveness": {"b": {"lastReceiptUnix": 2, "lastMessageUnix": 2}}}`
	if err := os.WriteFile(statsPath, []byte(stubA), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)

	t.Cleanup(resetSourceLivenessCache)
	resetSourceLivenessCache()

	got := readIngestorSourceLiveness()
	if _, ok := got["a"]; !ok {
		t.Fatalf("first call: expected key 'a', got %+v", got)
	}
	// Bump mtime forward to guarantee the cache notices.
	future := time.Now().Add(2 * time.Second)
	if err := os.WriteFile(statsPath, []byte(stubB), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(statsPath, future, future); err != nil {
		t.Fatal(err)
	}
	got = readIngestorSourceLiveness()
	if _, ok := got["b"]; !ok {
		t.Fatalf("after mtime change: expected key 'b', got %+v", got)
	}
}
