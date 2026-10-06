package main

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

func TestRateLimiterBurstAndRefill(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	l := newRateLimiter(3, time.Minute)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if ok, _ := l.take("k"); !ok {
			t.Fatalf("take %d refused within burst", i)
		}
	}
	ok, wait := l.take("k")
	if ok || wait <= 0 || wait > 20*time.Second {
		t.Fatalf("4th take = %v, wait %v", ok, wait)
	}
	if ok, _ := l.take("other"); !ok {
		t.Fatal("keys are not independent")
	}
	now = now.Add(21 * time.Second) // one token per 20s
	if ok, _ := l.take("k"); !ok {
		t.Fatal("no refill after 21s")
	}
	now = now.Add(time.Hour)
	l.gc()
	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n != 0 {
		t.Fatalf("gc kept %d full buckets", n)
	}
}

func TestRateLimiterBucketCap(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	l := newRateLimiter(3, time.Minute)
	l.now = func() time.Time { return now }
	l.maxBuckets = 2
	l.take("a")
	l.take("b")
	// both buckets are partly drained, so the inline sweep frees nothing
	ok, wait := l.take("c")
	if ok || wait <= 0 {
		t.Fatalf("new key at the cap = %v, wait %v; want refused", ok, wait)
	}
	if ok, _ := l.take("a"); !ok {
		t.Fatal("existing key refused at the cap")
	}
	// once the old buckets have refilled, the sweep makes room
	now = now.Add(time.Hour)
	if ok, _ := l.take("c"); !ok {
		t.Fatal("new key refused after buckets refilled")
	}
	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n > 2 {
		t.Fatalf("buckets = %d, cap 2", n)
	}
}

func TestAllowKeysOnForwardedIP(t *testing.T) {
	a, _ := newTestAuthService(t)
	a.ipr.trustedProxies = parseCIDRList([]string{"10.0.0.0/8"}, "test")
	l := newRateLimiter(1, time.Hour)
	try := func(xff string) bool {
		r := httptest.NewRequest("POST", "/x", nil)
		r.RemoteAddr = "10.0.0.1:4000"
		r.Header.Set("X-Forwarded-For", xff)
		return a.allow(httptest.NewRecorder(), r, l)
	}
	if !try("198.51.100.7") {
		t.Fatal("first request refused")
	}
	if try("198.51.100.7") {
		t.Fatal("same forwarded IP not limited")
	}
	if !try("198.51.100.8") {
		t.Fatal("different forwarded IP shares a bucket")
	}
}

func TestCloseUserManagementWaitsForJanitor(t *testing.T) {
	a, _ := newTestAuthService(t)
	srv := &Server{auth: a}
	release := make(chan struct{})
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		<-a.stop
		<-release // janitor still busy after stop was signalled
	}()
	done := make(chan struct{})
	go func() { srv.closeUserManagement(); close(done) }()
	select {
	case <-done:
		t.Fatal("close returned while the janitor was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("close did not return after the janitor exited")
	}
}

func TestAdminRowConfigAdminNeedsAdminRole(t *testing.T) {
	a, _ := newTestAuthService(t, "boss@example.org")
	pending := users.User{Email: "boss@example.org", Role: users.RoleUser, Status: users.StatusPending}
	if a.adminRow(pending, nil).ConfigAdmin {
		t.Fatal("pending squatter on a config address reported as config admin")
	}
	admin := users.User{Email: "boss@example.org", Role: users.RoleAdmin, Status: users.StatusActive}
	if !a.adminRow(admin, nil).ConfigAdmin {
		t.Fatal("real config admin not reported")
	}
}
