package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseTimeWindow_Relative(t *testing.T) {
	cases := []struct {
		param string
		hours float64 // expected From offset in hours (negative = past)
	}{
		{"1h", 1},
		{"24h", 24},
		{"7d", 168},
		{"30d", 720},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/?window="+tc.param, nil)
		w := ParseTimeWindow(r)
		if w.IsZero() {
			t.Errorf("window=%s: expected non-zero", tc.param)
		}
		expected := time.Duration(tc.hours * float64(time.Hour))
		diff := time.Since(w.From) - expected
		if diff < -5*time.Second || diff > 5*time.Second {
			t.Errorf("window=%s: From offset %v, want ~%v", tc.param, time.Since(w.From), expected)
		}
		if w.To.IsZero() {
			t.Errorf("window=%s: To should be set", tc.param)
		}
	}
}

func TestParseTimeWindow_Absolute(t *testing.T) {
	from := "2024-01-01T00:00:00Z"
	to := "2024-01-02T00:00:00Z"
	r := httptest.NewRequest("GET", "/?from="+from+"&to="+to, nil)
	w := ParseTimeWindow(r)
	if w.IsZero() {
		t.Fatal("expected non-zero window")
	}
	if w.From.Year() != 2024 || w.From.Month() != 1 || w.From.Day() != 1 {
		t.Errorf("unexpected From: %v", w.From)
	}
	if w.To.Day() != 2 {
		t.Errorf("unexpected To: %v", w.To)
	}
}

func TestParseTimeWindow_Empty(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	w := ParseTimeWindow(r)
	if !w.IsZero() {
		t.Fatal("expected zero window for no params")
	}
}

func TestTimeWindow_Includes(t *testing.T) {
	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	w := newTimeWindow(from, to)

	if !w.Includes("2024-01-01T12:00:00Z") {
		t.Error("expected midday to be included")
	}
	if w.Includes("2023-12-31T23:59:59Z") {
		t.Error("expected before-from to be excluded")
	}
	if w.Includes("2024-01-02T00:00:00Z") {
		t.Error("expected to-boundary to be excluded (half-open)")
	}
	if w.Includes("2024-01-03T00:00:00Z") {
		t.Error("expected after-to to be excluded")
	}
}

func TestTimeWindow_ZeroIncludes(t *testing.T) {
	w := TimeWindow{}
	if !w.Includes("2024-01-01T00:00:00Z") {
		t.Error("zero window should include everything")
	}
	if !w.Includes("") {
		t.Error("zero window should include empty timestamp")
	}
}

func TestTimeWindow_CacheKey(t *testing.T) {
	w := TimeWindow{}
	if w.CacheKey() != "" {
		t.Errorf("zero window CacheKey should be empty, got %q", w.CacheKey())
	}

	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	w2 := newTimeWindow(from, to)
	if w2.CacheKey() == "" {
		t.Error("non-zero window CacheKey should not be empty")
	}
	// Different windows should have different cache keys
	w3 := newTimeWindow(from.Add(time.Hour), to)
	if w2.CacheKey() == w3.CacheKey() {
		t.Error("different windows should have different cache keys")
	}
}
