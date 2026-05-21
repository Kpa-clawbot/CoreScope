package main

import (
	"testing"
	"time"
)

func TestParseEnvelopeTime(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"rfc3339 utc", "2026-05-16T10:00:00Z", true},
		{"rfc3339 offset", "2026-05-16T12:00:00+02:00", true},
		{"naive iso", "2026-05-16T10:00:00", true},
		{"naive iso micros", "2026-05-16T10:00:00.123456", true},
		{"garbage", "not-a-time", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseEnvelopeTime(c.in)
			if (err == nil) != c.ok {
				t.Fatalf("parseEnvelopeTime(%q): want ok=%v, got err=%v", c.in, c.ok, err)
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
	if got := resolveRxTime(map[string]interface{}{"timestamp": rx}, "test"); got != rx {
		t.Errorf("plausible past timestamp: got %q want %q", got, rx)
	}
	if got := resolveRxTime(map[string]interface{}{}, "test"); !nearNow(got) {
		t.Errorf("missing timestamp: got %q, expected ~now", got)
	}
	if got := resolveRxTime(map[string]interface{}{"timestamp": "garbage"}, "test"); !nearNow(got) {
		t.Errorf("garbage timestamp: got %q, expected ~now", got)
	}
	future := now.Add(48 * time.Hour).Format(time.RFC3339)
	if got := resolveRxTime(map[string]interface{}{"timestamp": future}, "test"); !nearNow(got) {
		t.Errorf("future timestamp: got %q, expected ~now (rejected)", got)
	}

	// RTC-reset node reporting a factory date — must not drag first_seen back.
	factory := "2020-01-01T00:00:00Z"
	if got := resolveRxTime(map[string]interface{}{"timestamp": factory}, "test"); !nearNow(got) {
		t.Errorf("stale factory timestamp: got %q, expected ~now (rejected)", got)
	}
	// Just past the 30-day floor → rejected.
	stale := now.Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	if got := resolveRxTime(map[string]interface{}{"timestamp": stale}, "test"); !nearNow(got) {
		t.Errorf("stale timestamp >30d: got %q, expected ~now (rejected)", got)
	}
	// Just inside the 30-day floor → used verbatim.
	recent := now.Add(-29 * 24 * time.Hour).Format(time.RFC3339)
	if got := resolveRxTime(map[string]interface{}{"timestamp": recent}, "test"); got != recent {
		t.Errorf("recent timestamp <30d: got %q want %q", got, recent)
	}
}
