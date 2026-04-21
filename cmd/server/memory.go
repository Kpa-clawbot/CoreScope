package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MemorySnapshot is a point-in-time view of process memory across several
// vantage points. Values are in MB (1024*1024 bytes), rounded to one decimal.
//
// Field invariants (typical, not guaranteed under exotic conditions):
//
//	processRSSMB  >=  goSysMB  >=  goHeapInuseMB  >=  storeDataMB
//
//   - processRSSMB is what the kernel charges the process (resident set).
//     Read from /proc/self/status `VmRSS:` on Linux; falls back to goSysMB
//     on other platforms or when /proc is unavailable.
//   - goSysMB is the total memory obtained from the OS by the Go runtime
//     (heap, stacks, GC metadata, mspans, mcache, etc.). Includes
//     fragmentation and unused-but-mapped span overhead.
//   - goHeapInuseMB is the live, in-use Go heap (HeapInuse). Excludes
//     idle spans and runtime overhead.
//   - storeDataMB is the in-store packet byte estimate (transmissions +
//     observations). Subset of HeapInuse. Does not include index maps,
//     analytics caches, broadcast queues, or runtime overhead. Used as
//     the input to the eviction watermark.
//
// processRSSMB and storeDataMB are monotonic only relative to ingest +
// eviction; both can shrink when packets age out. goHeapInuseMB and goSysMB
// fluctuate with GC.
type MemorySnapshot struct {
	ProcessRSSMB  float64 `json:"processRSSMB"`
	GoHeapInuseMB float64 `json:"goHeapInuseMB"`
	GoSysMB       float64 `json:"goSysMB"`
	StoreDataMB   float64 `json:"storeDataMB"`
}

// memSnapshotCache rate-limits both ReadMemStats (stop-the-world) and the
// /proc/self/status read so that hot endpoints (/api/stats, /api/health)
// don't pay the cost on every request.
var (
	memSnapshotMu       sync.Mutex
	memSnapshotCache    MemorySnapshot
	memSnapshotCachedAt time.Time
)

const memSnapshotTTL = 1 * time.Second

// getMemorySnapshot returns a cached memory snapshot. storeDataMB is filled
// in by the caller (passed in) since the packet store is the source of
// truth and we don't want to import a package cycle here.
//
// The 1s TTL is a compromise: stats endpoints are typically polled every
// 5–30s, but operators sometimes hit /api/stats in tight loops while
// debugging. ReadMemStats stops the world; readProcRSSMB does a small
// /proc read. Both are cheap individually but add up under burst load.
func getMemorySnapshot(storeDataMB float64) MemorySnapshot {
	memSnapshotMu.Lock()
	defer memSnapshotMu.Unlock()

	if time.Since(memSnapshotCachedAt) < memSnapshotTTL {
		snap := memSnapshotCache
		snap.StoreDataMB = roundMB(storeDataMB)
		return snap
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	rssMB := readProcRSSMB()
	if rssMB <= 0 {
		// Fallback: Go runtime "Sys" is a reasonable upper bound on what
		// the kernel actually has resident, ignoring cgo. For pure-Go
		// builds (modernc.org/sqlite) this is close.
		rssMB = float64(ms.Sys) / 1048576.0
	}

	memSnapshotCache = MemorySnapshot{
		ProcessRSSMB:  roundMB(rssMB),
		GoHeapInuseMB: roundMB(float64(ms.HeapInuse) / 1048576.0),
		GoSysMB:       roundMB(float64(ms.Sys) / 1048576.0),
	}
	memSnapshotCachedAt = time.Now()

	snap := memSnapshotCache
	snap.StoreDataMB = roundMB(storeDataMB)
	return snap
}

// readProcRSSMB parses /proc/self/status for the VmRSS line. Returns 0 on
// any failure (file missing, malformed line, parse error) — the caller
// then uses a runtime fallback. Linux only; macOS/Windows return 0.
//
// Safety notes (djb): the file path is hard-coded, no untrusted input is
// concatenated. We bound the read at 8 KiB (the whole status file is
// well under 4 KiB on modern kernels) so a corrupt /proc can't OOM us.
// We only parse digits with strconv; no shell, no exec.
func readProcRSSMB() float64 {
	const maxStatusBytes = 8 * 1024
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer f.Close()

	buf := make([]byte, maxStatusBytes)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return 0
	}
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		// Format: "VmRSS:\t   123456 kB"
		fields := strings.Fields(line[len("VmRSS:"):])
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return 0
		}
		// Unit is kB per kernel convention; convert to MB.
		return kb / 1024.0
	}
	return 0
}

func roundMB(v float64) float64 {
	if v < 0 {
		return 0
	}
	// One decimal place.
	return float64(int64(v*10+0.5)) / 10.0
}
