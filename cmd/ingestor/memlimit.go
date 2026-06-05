package main

// applyMemoryLimit configures Go's soft memory limit (GOMEMLIMIT) for the
// ingestor process. See #1010.
//
// Behavior:
//   - If envSet is true (GOMEMLIMIT env var present), the runtime has already
//     parsed it; we leave it alone and report source="env" with limit=0.
//   - Otherwise, if runtimeMaxMB > 0, we set a limit of runtimeMaxMB MiB via
//     debug.SetMemoryLimit and report source="config".
//   - Otherwise, no limit is applied; source="none".
//
// Stub for the red commit — returns (0, "none") so the
// runtime-config-set test fails on an assertion (not a compile error).
func applyMemoryLimit(runtimeMaxMB int, envSet bool) (int64, string) {
	return 0, "none"
}
