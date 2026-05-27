package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestProcIORate_ZeroValuePrevSuppressesRate guards against the phantom-delta
// regression from #1169: when os.Open("/proc/self/io") fails, readProcSelfIO
// now returns a zero-value procIOSnapshot (ok=false, zero time.Time). This
// asserts procIORate returns nil so no inflated rate spike appears for the
// next successful read.
func TestProcIORate_ZeroValuePrevSuppressesRate(t *testing.T) {
	prev := procIOSnapshot{} // zero-value: ok=false, at=zero
	cur := procIOSnapshot{
		at:        time.Now(),
		readBytes: 1024 * 1024 * 100,
		ok:        true,
	}
	if got := procIORate(prev, cur, "2026-01-01T00:00:00Z"); got != nil {
		t.Fatalf("expected nil rate when prev is zero-value (os.Open failed), got %+v", got)
	}
}

// TestProcIORate_NormalPath asserts two valid snapshots produce a non-nil rate.
func TestProcIORate_NormalPath(t *testing.T) {
	base := time.Now()
	prev := procIOSnapshot{at: base, readBytes: 0, ok: true}
	cur := procIOSnapshot{at: base.Add(time.Second), readBytes: 1024, ok: true}
	got := procIORate(prev, cur, "2026-01-01T00:00:01Z")
	if got == nil {
		t.Fatal("expected non-nil rate for valid prev/cur pair")
	}
	if got.ReadBytesPerSec != 1024.0 {
		t.Errorf("ReadBytesPerSec: want 1024.0, got %v", got.ReadBytesPerSec)
	}
}

// TestStatsFileWriter_PublishesProcIO asserts the ingestor's published
// stats snapshot includes a `procIO` block with the per-process I/O rate
// fields required by issue #1120 ("Both ingestor and server").
func TestStatsFileWriter_PublishesProcIO(t *testing.T) {
	if _, err := os.Stat("/proc/self/io"); err != nil {
		t.Skip("skip: /proc/self/io unavailable on this host")
	}
	dir := t.TempDir()
	statsPath := filepath.Join(dir, "ingestor-stats.json")
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)

	store, err := OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	StartStatsFileWriter(store, 50*time.Millisecond)

	// Wait for at least 2 ticks so the writer has had a chance to populate
	// procIO rates from a delta.
	deadline := time.Now().Add(3 * time.Second)
	var snap map[string]interface{}
	for time.Now().Before(deadline) {
		time.Sleep(75 * time.Millisecond)
		b, err := os.ReadFile(statsPath)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(b, &snap); err != nil {
			continue
		}
		if _, ok := snap["procIO"]; ok {
			break
		}
	}

	pio, ok := snap["procIO"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected procIO block in stats snapshot, got: %v", snap)
	}
	for _, field := range []string{"readBytesPerSec", "writeBytesPerSec", "cancelledWriteBytesPerSec", "syscallsRead", "syscallsWrite"} {
		v, present := pio[field]
		if !present {
			t.Errorf("procIO missing field %q", field)
			continue
		}
		// #1167 must-fix #5: assert the field actually decodes as a JSON
		// number, not just that the key exists. An empty PerfIOSample{}
		// substruct would still serialise the keys since the inner numeric
		// fields lack omitempty — without this Kind check the test would
		// silently pass on an empty struct regression.
		if _, isFloat := v.(float64); !isFloat {
			t.Errorf("procIO[%q] expected JSON number (float64), got %T (%v)", field, v, v)
		}
	}
}
