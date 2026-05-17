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
	// Label is a stable identifier for the user-requested window
	// (e.g. "24h"). For relative windows it is the original alias; for
	// absolute ranges it is empty (Since/Until are already stable).
	// Used only for cache keying so that "?window=24h" produces a single
	// cache entry instead of one per second.
	Label string
}

// IsZero reports whether the window imposes no bounds at all.
func (w TimeWindow) IsZero() bool {
	return w.Since == "" && w.Until == ""
}

// CacheKey returns a deterministic key suitable for analytics caches.
// For relative windows the key is the alias label so that the cache
// remains stable across the wall-clock advancing.
func (w TimeWindow) CacheKey() string {
	if w.IsZero() {
		return ""
	}
	if w.Label != "" {
		return "rel:" + w.Label
	}
	return w.Since + "|" + w.Until
}

// Includes reports whether ts (an RFC3339-style string) falls within the
// window. Empty ts is treated as included (for callers that don't have a
// timestamp on every observation).
//
// Comparison is done by parsing both sides into time.Time. Lex compare is
// unsafe here because stored timestamps carry millisecond precision
// ("...HH:MM:SS.000Z") while bounds emitted by ParseTimeWindow do not
// ("...HH:MM:SSZ"), and '.' (0x2e) sorts before 'Z' (0x5a). If a timestamp
// fails to parse we fall back to lex compare to preserve old behavior.
func (w TimeWindow) Includes(ts string) bool {
	if ts == "" {
		return true
	}
	tt, terr := parseAnyRFC3339(ts)
	if w.Since != "" {
		if s, err := parseAnyRFC3339(w.Since); err == nil && terr == nil {
			if tt.Before(s) {
				return false
			}
		} else if ts < w.Since {
			return false
		}
	}
	if w.Until != "" {
		if u, err := parseAnyRFC3339(w.Until); err == nil && terr == nil {
			if tt.After(u) {
				return false
			}
		} else if ts > w.Until {
			return false
		}
	}
	return true
}

// parseAnyRFC3339 accepts both fractional-second ("...000Z") and second-
// precision ("...Z") RFC3339 timestamps. time.RFC3339Nano handles both.
func parseAnyRFC3339(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
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
		return TimeWindow{Since: since, Label: win}
	}

	return TimeWindow{}
}
