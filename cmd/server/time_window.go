package main

import (
	"net/http"
	"time"
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
	q := r.URL.Query()

	// Absolute range takes precedence if either bound is set.
	from := q.Get("from")
	to := q.Get("to")
	if from != "" || to != "" {
		w := TimeWindow{}
		if from != "" {
			if t, err := time.Parse(time.RFC3339, from); err == nil {
				w.Since = t.UTC().Format(time.RFC3339)
			}
		}
		if to != "" {
			if t, err := time.Parse(time.RFC3339, to); err == nil {
				w.Until = t.UTC().Format(time.RFC3339)
			}
		}
		return w
	}

	// Relative window.
	if win := q.Get("window"); win != "" {
		var d time.Duration
		switch win {
		case "1h":
			d = 1 * time.Hour
		case "24h", "1d":
			d = 24 * time.Hour
		case "3d":
			d = 3 * 24 * time.Hour
		case "7d", "1w":
			d = 7 * 24 * time.Hour
		case "30d":
			d = 30 * 24 * time.Hour
		default:
			// Unknown values are silently ignored — backwards compatible.
			return TimeWindow{}
		}
		since := time.Now().UTC().Add(-d).Format(time.RFC3339)
		return TimeWindow{Since: since}
	}

	return TimeWindow{}
}
