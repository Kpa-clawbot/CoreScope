package main

import (
	"net/http"
	"strconv"
)

// clampLimit parses a `limit`-shaped string and clamps it into [1, max].
// Empty / non-numeric / zero / negative inputs return def.
// Values exceeding max are clamped to max.
//
// This is the uniform helper for list-endpoint `limit` parameters; prefer it
// over inline `if limit > N { limit = N }` patterns so the absolute caps stay
// consistent across handlers. See audit-input-vulns-20260603 (MEDIUM —
// unbounded `limit` on list endpoints).
func clampLimit(raw string, def, max int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// queryLimit reads the `limit` query parameter from r and clamps it through
// clampLimit. Convenience wrapper used by HTTP handlers so existing
// queryInt(r, "limit", def) call sites can become queryLimit(r, def, max).
func queryLimit(r *http.Request, def, max int) int {
	return clampLimit(r.URL.Query().Get("limit"), def, max)
}

