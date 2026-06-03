package main

// clampLimit parses a `limit` query string and clamps it into [1, max].
// Empty / non-numeric / zero / negative inputs return def.
// Values exceeding max are clamped to max.
//
// This is the uniform helper for list-endpoint `limit` parameters; prefer it
// over inline `if limit > N { limit = N }` patterns so the absolute caps stay
// consistent across handlers (#audit-input-vulns-20260603, MEDIUM).
func clampLimit(raw string, def, max int) int {
	// Stub for TDD red commit — real impl lands in the green commit.
	return def
}
