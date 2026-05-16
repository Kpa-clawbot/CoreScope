package main

import (
	"fmt"
	"net/http"
	"time"
)

// TimeWindow bounds an analytics query to a specific time range.
// Zero value means unbounded (all data).
type TimeWindow struct {
	From    time.Time
	To      time.Time
	fromStr string // pre-formatted RFC3339 for fast string comparison
	toStr   string
}

func newTimeWindow(from, to time.Time) TimeWindow {
	tw := TimeWindow{From: from, To: to}
	if !from.IsZero() {
		tw.fromStr = from.UTC().Format(time.RFC3339)
	}
	if !to.IsZero() {
		tw.toStr = to.UTC().Format(time.RFC3339)
	}
	return tw
}

// IsZero reports whether the window is unbounded.
func (w TimeWindow) IsZero() bool { return w.From.IsZero() && w.To.IsZero() }

// Includes returns true if the RFC3339 timestamp string falls within the window.
// Uses string comparison — valid because stored timestamps are UTC RFC3339.
func (w TimeWindow) Includes(timestamp string) bool {
	if w.IsZero() {
		return true
	}
	if w.fromStr != "" && timestamp < w.fromStr {
		return false
	}
	if w.toStr != "" && timestamp >= w.toStr {
		return false
	}
	return true
}

// CacheKey returns a stable string suffix for use in cache keys.
func (w TimeWindow) CacheKey() string {
	if w.IsZero() {
		return ""
	}
	return fmt.Sprintf(":%s:%s", w.fromStr, w.toStr)
}

// ParseTimeWindow reads ?window=1h|24h|7d|30d or ?from=<RFC3339>&to=<RFC3339>.
// Returns a zero TimeWindow if no window parameters are present.
func ParseTimeWindow(r *http.Request) TimeWindow {
	now := time.Now().UTC()
	switch r.URL.Query().Get("window") {
	case "1h":
		return newTimeWindow(now.Add(-1*time.Hour), now)
	case "24h":
		return newTimeWindow(now.Add(-24*time.Hour), now)
	case "7d":
		return newTimeWindow(now.Add(-7*24*time.Hour), now)
	case "30d":
		return newTimeWindow(now.Add(-30*24*time.Hour), now)
	}

	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" && to == "" {
		return TimeWindow{}
	}

	var fromT, toT time.Time
	if from != "" {
		if t, err := time.Parse(time.RFC3339Nano, from); err == nil {
			fromT = t.UTC()
		} else if t, err := time.Parse(time.RFC3339, from); err == nil {
			fromT = t.UTC()
		}
	}
	if to != "" {
		if t, err := time.Parse(time.RFC3339Nano, to); err == nil {
			toT = t.UTC()
		} else if t, err := time.Parse(time.RFC3339, to); err == nil {
			toT = t.UTC()
		}
	}
	if fromT.IsZero() && toT.IsZero() {
		return TimeWindow{}
	}
	return newTimeWindow(fromT, toT)
}
