package main

import (
	"runtime/debug"
)

// applyMemoryLimit configures Go's soft memory limit (GOMEMLIMIT).
//
// Behavior:
//   - If envSet is true (GOMEMLIMIT env var present), the runtime has already
//     parsed it; we leave it alone and report source="env" with limit=0.
//   - Otherwise, if maxMemoryMB > 0, we derive a limit of maxMemoryMB * 1.5 MiB
//     and set it via debug.SetMemoryLimit. This forces aggressive GC under
//     cgroup pressure so the process self-throttles before SIGKILL. See #836.
//   - Otherwise, no limit is applied; source="none".
//
// Returns the limit (in bytes) we actually set, or 0 if we did not set one,
// plus a short source identifier ("env" | "derived" | "none") for logging.
func applyMemoryLimit(maxMemoryMB int, envSet bool) (int64, string) {
	if envSet {
		return 0, "env"
	}
	if maxMemoryMB <= 0 {
		return 0, "none"
	}
	// 1.5x headroom over the steady-state packet store budget covers
	// transient peaks (cold-load row-scan / decode pipeline, Go's NextGC
	// trigger at ~2x live heap). See issue #836 heap profile.
	limit := int64(maxMemoryMB) * 1024 * 1024 * 3 / 2
	debug.SetMemoryLimit(limit)
	return limit, "derived"
}
