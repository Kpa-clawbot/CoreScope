package main

import "runtime/debug"

// applyMemoryLimit configures Go's soft memory limit (GOMEMLIMIT) for the
// ingestor process. See #1010.
//
// Precedence:
//  1. GOMEMLIMIT env var (parsed by the runtime at startup) — we do not
//     override; report source="env" with limit=0.
//  2. runtimeMaxMB > 0 (from config runtime.maxMemoryMB) — set limit of
//     runtimeMaxMB MiB via debug.SetMemoryLimit; source="config".
//  3. Otherwise no limit applied; source="none" (default behavior).
//
// Returns the limit (bytes) we set, or 0 if we did not set one.
func applyMemoryLimit(runtimeMaxMB int, envSet bool) (int64, string) {
	if envSet {
		return 0, "env"
	}
	if runtimeMaxMB <= 0 {
		return 0, "none"
	}
	limit := int64(runtimeMaxMB) * 1024 * 1024
	debug.SetMemoryLimit(limit)
	return limit, "config"
}
