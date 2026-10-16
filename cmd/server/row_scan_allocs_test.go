package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The bulk loaders declared their ~20 scan targets and built the Scan
// argument slice inside the row loop. Each target escapes (its address goes
// into the []interface{}), so every row allocated them all again, plus the
// slice and its growth: about 330MB of the allocations loadChunk made over a
// startup on a week of a busy network. Scan overwrites every target on each
// row, so one set serves the whole loop. On this fixture that took each
// loader from about 89 allocations per row to about 67; the limits sit
// between.

func rowScanStore(t *testing.T, n int) *PacketStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rows.db")
	createTestDBAt(t, dbPath, n)
	t.Cleanup(func() { os.RemoveAll(dir) })
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewPacketStore(db, &PacketStoreConfig{})
}

func mallocsPerRow(t *testing.T, n int, load func(*PacketStore) error) float64 {
	t.Helper()
	s := rowScanStore(t, n)
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	if err := load(s); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&b)
	if len(s.packets) != n {
		t.Fatalf("loaded %d packets, want %d", len(s.packets), n)
	}
	return float64(b.Mallocs-a.Mallocs) / float64(n)
}

func TestBulkLoadersReuseScanTargets(t *testing.T) {
	const n = 2000
	cases := []struct {
		name  string
		limit float64
		load  func(*PacketStore) error
	}{
		{"Load", 78, func(s *PacketStore) error { return s.Load() }},
		{"LoadChunked", 83, func(s *PacketStore) error { return s.LoadChunked(500) }},
		{"loadChunk", 78, func(s *PacketStore) error {
			from := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
			return s.loadChunk(from, from.Add(72*time.Hour))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mallocsPerRow(t, n, c.load)
			t.Logf("%s: %.1f allocations per row", c.name, got)
			if got > c.limit {
				t.Errorf("%s makes %.1f allocations per row, limit %.0f: are the scan targets rebuilt per row?", c.name, got, c.limit)
			}
		})
	}
}
