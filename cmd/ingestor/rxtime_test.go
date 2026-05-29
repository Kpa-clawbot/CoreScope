package main

import (
	"testing"
	"time"
)

func TestParseEnvelopeTime(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		ok        bool
		wantNaive bool
	}{
		{"rfc3339 utc", "2026-05-16T10:00:00Z", true, false},
		{"rfc3339 offset", "2026-05-16T12:00:00+02:00", true, false},
		{"naive iso", "2026-05-16T10:00:00", true, true},
		{"naive iso micros", "2026-05-16T10:00:00.123456", true, true},
		{"garbage", "not-a-time", false, false},
		{"empty", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, naive, err := parseEnvelopeTime(c.in)
			if (err == nil) != c.ok {
				t.Fatalf("parseEnvelopeTime(%q): want ok=%v, got err=%v", c.in, c.ok, err)
			}
			if err == nil && naive != c.wantNaive {
				t.Fatalf("parseEnvelopeTime(%q): want naive=%v, got %v", c.in, c.wantNaive, naive)
			}
		})
	}
}

func TestResolveRxTime(t *testing.T) {
	now := time.Now().UTC()

	mustParse := func(s string) time.Time {
		t.Helper()
		parsed, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("result %q is not RFC3339: %v", s, err)
		}
		return parsed
	}
	nearNow := func(s string) bool {
		d := mustParse(s).Sub(now)
		if d < 0 {
			d = -d
		}
		return d <= time.Minute
	}

	rx := now.Add(-5 * time.Hour).Format(time.RFC3339)
	if got, _ := resolveRxTime(map[string]interface{}{"timestamp": rx}, "test"); got != rx {
		t.Errorf("plausible past timestamp: got %q want %q", got, rx)
	}
	if got, _ := resolveRxTime(map[string]interface{}{}, "test"); !nearNow(got) {
		t.Errorf("missing timestamp: got %q, expected ~now", got)
	}
	if got, _ := resolveRxTime(map[string]interface{}{"timestamp": "garbage"}, "test"); !nearNow(got) {
		t.Errorf("garbage timestamp: got %q, expected ~now", got)
	}
	future := now.Add(48 * time.Hour).Format(time.RFC3339)
	if got, _ := resolveRxTime(map[string]interface{}{"timestamp": future}, "test"); !nearNow(got) {
		t.Errorf("future timestamp: got %q, expected ~now (rejected)", got)
	}

	// RTC-reset node reporting a factory date — must not drag first_seen back.
	factory := "2020-01-01T00:00:00Z"
	if got, _ := resolveRxTime(map[string]interface{}{"timestamp": factory}, "test"); !nearNow(got) {
		t.Errorf("stale factory timestamp: got %q, expected ~now (rejected)", got)
	}
	// Just past the 30-day floor → rejected.
	stale := now.Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	if got, _ := resolveRxTime(map[string]interface{}{"timestamp": stale}, "test"); !nearNow(got) {
		t.Errorf("stale timestamp >30d: got %q, expected ~now (rejected)", got)
	}
	// Just inside the 30-day floor → used verbatim.
	recent := now.Add(-29 * 24 * time.Hour).Format(time.RFC3339)
	if got, _ := resolveRxTime(map[string]interface{}{"timestamp": recent}, "test"); got != recent {
		t.Errorf("recent timestamp <30d: got %q want %q", got, recent)
	}
}

// Regression: issue #1463 — naive (zone-less) ISO timestamps from observers
// in negative-UTC-offset zones (e.g. California PDT, UTC−7) were interpreted
// as UTC, producing rxTime values 7h in the past that poisoned `last_seen`
// and rendered the observer perpetually "Stale" in the UI. The symmetric
// clamp now collapses any naive timestamp more than 15 min off server-now to
// `now()`, while zone-aware timestamps (RFC3339 with Z or offset) are still
// honored verbatim regardless of skew (those are well-behaved observers).
func TestResolveRxTimeNaiveTimestampClamp(t *testing.T) {
	now := time.Now().UTC()

	mustParse := func(s string) time.Time {
		t.Helper()
		parsed, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("result %q is not RFC3339: %v", s, err)
		}
		return parsed
	}
	nearNow := func(s string) bool {
		d := mustParse(s).Sub(now)
		if d < 0 {
			d = -d
		}
		return d <= time.Minute
	}

	// California observer (UTC-7) emitting a naive local-clock timestamp:
	// must NOT be stored verbatim 7h in the past — clamp to ~now.
	naivePast := now.Add(-7 * time.Hour).Format("2006-01-02T15:04:05")
	if got, _ := resolveRxTime(map[string]interface{}{"timestamp": naivePast}, "test"); !nearNow(got) {
		t.Errorf("naive past timestamp (UTC-7 observer): got %q, expected ~now (clamped)", got)
	}

	// Naive future just minutes ahead (UTC+N observer, existing soft-clamp
	// behavior): still clamped to now.
	naiveFuture := now.Add(5 * time.Minute).Format("2006-01-02T15:04:05")
	if got, _ := resolveRxTime(map[string]interface{}{"timestamp": naiveFuture}, "test"); !nearNow(got) {
		t.Errorf("naive future timestamp: got %q, expected ~now (clamped)", got)
	}

	// Naive microsecond layout (python isoformat without tz) — same clamp.
	naivePastMicros := now.Add(-7 * time.Hour).Format("2006-01-02T15:04:05.000000")
	if got, _ := resolveRxTime(map[string]interface{}{"timestamp": naivePastMicros}, "test"); !nearNow(got) {
		t.Errorf("naive past timestamp w/ micros: got %q, expected ~now (clamped)", got)
	}

	// Well-behaved observer: Z-suffixed past timestamp passes through verbatim
	// even if it's hours old (legitimate buffered uploads must be preserved).
	zPast := now.Add(-7 * time.Hour).Format(time.RFC3339)
	if got, _ := resolveRxTime(map[string]interface{}{"timestamp": zPast}, "test"); got != zPast {
		t.Errorf("Z-suffixed past timestamp must pass through: got %q want %q", got, zPast)
	}

	// Well-behaved observer with explicit offset (UTC-7) — canonicalize to UTC
	// but preserve the moment in time. Must equal the same moment in UTC.
	offsetLoc := time.FixedZone("PDT", -7*3600)
	offsetMoment := now.Add(-7 * time.Hour).In(offsetLoc)
	offsetStr := offsetMoment.Format(time.RFC3339)
	wantUTC := offsetMoment.UTC().Format(time.RFC3339)
	if got, _ := resolveRxTime(map[string]interface{}{"timestamp": offsetStr}, "test"); got != wantUTC {
		t.Errorf("offset-suffixed timestamp: got %q want %q", got, wantUTC)
	}

	// Naive timestamp within tolerance window (2 min in past, observer that
	// happens to be in UTC) — within tolerance, passes through verbatim.
	naiveCloseStr := now.Add(-2 * time.Minute).Format("2006-01-02T15:04:05")
	naiveCloseWant := now.Add(-2 * time.Minute).Format(time.RFC3339)
	if got, _ := resolveRxTime(map[string]interface{}{"timestamp": naiveCloseStr}, "test"); got != naiveCloseWant {
		t.Errorf("naive timestamp within tolerance: got %q, expected %q (verbatim)", got, naiveCloseWant)
	}
}
