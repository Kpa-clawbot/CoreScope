package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Issue #842 — selectable analytics timeframes.
// Backend must accept ?window=1h|24h|7d|30d and ?from=/?to= and yield a
// TimeWindow that correctly bounds analytics queries.

func TestParseTimeWindow_Window24h(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/analytics/rf?window=24h", nil)
	w := ParseTimeWindow(r)
	if w.Since == "" {
		t.Fatalf("window=24h: expected non-empty Since, got %q", w.Since)
	}
	since, err := time.Parse(time.RFC3339, w.Since)
	if err != nil {
		t.Fatalf("window=24h: Since %q is not RFC3339: %v", w.Since, err)
	}
	delta := time.Since(since)
	if delta < 23*time.Hour || delta > 25*time.Hour {
		t.Fatalf("window=24h: Since should be ~24h ago, got delta=%v", delta)
	}
}

func TestParseTimeWindow_WindowAliases(t *testing.T) {
	cases := map[string]time.Duration{
		"1h":  1 * time.Hour,
		"24h": 24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
		"30d": 30 * 24 * time.Hour,
	}
	for q, want := range cases {
		r := httptest.NewRequest("GET", "/api/analytics/rf?window="+q, nil)
		got := ParseTimeWindow(r)
		if got.Since == "" {
			t.Errorf("window=%s: empty Since", q)
			continue
		}
		since, err := time.Parse(time.RFC3339, got.Since)
		if err != nil {
			t.Errorf("window=%s: bad RFC3339 %q", q, got.Since)
			continue
		}
		delta := time.Since(since)
		// allow 5 minutes of slack
		if delta < want-5*time.Minute || delta > want+5*time.Minute {
			t.Errorf("window=%s: expected ~%v, got %v", q, want, delta)
		}
	}
}

func TestParseTimeWindow_FromTo(t *testing.T) {
	from := "2026-04-01T00:00:00Z"
	to := "2026-04-08T00:00:00Z"
	r := httptest.NewRequest("GET", "/api/analytics/rf?from="+from+"&to="+to, nil)
	w := ParseTimeWindow(r)
	if w.Since != from {
		t.Errorf("expected Since=%q, got %q", from, w.Since)
	}
	if w.Until != to {
		t.Errorf("expected Until=%q, got %q", to, w.Until)
	}
}

func TestParseTimeWindow_NoParams_BackwardsCompatible(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/analytics/rf", nil)
	w := ParseTimeWindow(r)
	if !w.IsZero() {
		t.Errorf("no params should yield zero window, got %+v", w)
	}
}

func TestTimeWindow_Includes(t *testing.T) {
	w := TimeWindow{Since: "2026-04-01T00:00:00Z", Until: "2026-04-08T00:00:00Z"}
	if !w.Includes("2026-04-05T12:00:00Z") {
		t.Error("mid-range ts should be included")
	}
	if w.Includes("2026-03-31T23:59:59Z") {
		t.Error("ts before Since should be excluded")
	}
	if w.Includes("2026-04-08T00:00:01Z") {
		t.Error("ts after Until should be excluded")
	}
	// Empty ts always included (some observations lack timestamps)
	if !w.Includes("") {
		t.Error("empty ts should be included")
	}
}

func TestTimeWindow_CacheKey_DistinctPerWindow(t *testing.T) {
	a := TimeWindow{Since: "2026-04-01T00:00:00Z"}
	b := TimeWindow{Since: "2026-04-02T00:00:00Z"}
	z := TimeWindow{}
	if a.CacheKey() == b.CacheKey() {
		t.Error("different windows must produce different cache keys")
	}
	if z.CacheKey() != "" {
		t.Errorf("zero window cache key must be empty, got %q", z.CacheKey())
	}
	if !strings.Contains(a.CacheKey(), "2026-04-01") {
		t.Errorf("cache key should encode Since, got %q", a.CacheKey())
	}
}
