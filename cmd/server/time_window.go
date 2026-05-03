package main

import (
	"net/http"
)

// TimeWindow is a half-open time range used to bound analytics queries.
// Empty Since/Until means unbounded on that end (backwards compatible).
type TimeWindow struct {
	Since string // RFC3339, empty = unbounded
	Until string // RFC3339, empty = unbounded
}

// IsZero reports whether the window imposes no bounds at all.
func (w TimeWindow) IsZero() bool {
	return w.Since == "" && w.Until == ""
}

// CacheKey returns a deterministic key suitable for analytics caches.
func (w TimeWindow) CacheKey() string {
	if w.IsZero() {
		return ""
	}
	return w.Since + "|" + w.Until
}

// Includes reports whether ts (an RFC3339-style string) falls within the
// window. Empty ts is treated as included (for callers that don't have a
// timestamp on every observation).
func (w TimeWindow) Includes(ts string) bool {
	if ts == "" {
		return true
	}
	if w.Since != "" && ts < w.Since {
		return false
	}
	if w.Until != "" && ts > w.Until {
		return false
	}
	return true
}

// ParseTimeWindow extracts a TimeWindow from query params.
//
// Supported parameters:
//
//	?window=1h | 24h | 7d | 30d   — relative window ending "now"
//	?from=<RFC3339>&to=<RFC3339>  — absolute custom range (either bound optional)
//
// When neither is set, returns the zero TimeWindow (unbounded; original behavior).
// Invalid values are silently ignored to preserve backwards compatibility.
func ParseTimeWindow(r *http.Request) TimeWindow {
	// STUB — implementation in green commit. Returns zero window so any
	// assertion-level test that expects a populated Since/Until fails on
	// the assertion (not on a build error).
	_ = r
	return TimeWindow{}
}
