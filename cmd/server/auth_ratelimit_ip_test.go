package main

import (
	"context"
	"net"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimitIPKey(t *testing.T) {
	key := func(s string) string {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP %q", s)
		}
		return rateLimitIPKey(ip)
	}
	if got := key("198.51.100.7"); got != "ip:198.51.100.7" {
		t.Errorf("IPv4 key = %q", got)
	}
	if key("198.51.100.7") == key("198.51.100.8") {
		t.Error("distinct IPv4 addresses share a key")
	}
	if key("::ffff:198.51.100.7") != key("198.51.100.7") {
		t.Error("v4-mapped address keyed differently from its IPv4 form")
	}
	if key("2001:db8:1:2::1") != key("2001:db8:1:2:ffff:ffff:ffff:ffff") {
		t.Error("two addresses in one /64 got different keys")
	}
	if key("2001:db8:1:2::1") == key("2001:db8:1:3::1") {
		t.Error("different /64s share a key")
	}
}

// The limiter itself must count a whole /64 as one client, on the in-memory
// path.
func TestAllowSharesBucketPerIPv6Prefix(t *testing.T) {
	a, _ := newTestAuthService(t)
	a.ipr.trustedProxies = parseCIDRList([]string{"10.0.0.0/8"}, "test")
	l := newRateLimiter(1, time.Hour)
	try := func(xff string) bool {
		r := httptest.NewRequest("POST", "/x", nil)
		r.RemoteAddr = "10.0.0.1:4000"
		r.Header.Set("X-Forwarded-For", xff)
		return a.allow(httptest.NewRecorder(), r, l)
	}
	if !try("2001:db8:1:2::1") {
		t.Fatal("first request refused")
	}
	if try("2001:db8:1:2:aaaa:bbbb:cccc:dddd") {
		t.Fatal("second address in the same /64 not limited")
	}
	if !try("2001:db8:1:3::1") {
		t.Fatal("a different /64 shares the bucket")
	}
	if !try("198.51.100.7") {
		t.Fatal("first request refused")
	}
	if try("::ffff:198.51.100.7") {
		t.Fatal("v4-mapped address not counted as its IPv4 form")
	}
	if n := len(l.buckets); n != 3 {
		t.Errorf("buckets = %d, want 3", n)
	}
}

// On the shared path the key reaching the store is the normalised one.
type recordingShared struct{ keys []string }

func (r *recordingShared) RateLimitHit(_ context.Context, key string, _ time.Duration, _ int, _ time.Time) (bool, error) {
	r.keys = append(r.keys, key)
	return true, nil
}

func TestAllowSharedPathUsesNormalisedKey(t *testing.T) {
	a, _ := newTestAuthService(t)
	a.ipr.trustedProxies = parseCIDRList([]string{"10.0.0.0/8"}, "test")
	l := newRateLimiter(5, time.Hour)
	rec := &recordingShared{}
	l.share(rec, "login")
	for _, xff := range []string{"2001:db8:1:2::1", "2001:db8:1:2::2", "::ffff:198.51.100.7"} {
		r := httptest.NewRequest("POST", "/x", nil)
		r.RemoteAddr = "10.0.0.1:4000"
		r.Header.Set("X-Forwarded-For", xff)
		if !a.allow(httptest.NewRecorder(), r, l, "email:x") {
			t.Fatal("refused")
		}
	}
	if rec.keys[0] != rec.keys[2] || rec.keys[2] == rec.keys[4] {
		t.Errorf("shared keys not normalised: %v", rec.keys)
	}
	if rec.keys[4] != "login:ip:198.51.100.7" || rec.keys[1] != "login:email:x" {
		t.Errorf("unexpected keys: %v", rec.keys)
	}
}
