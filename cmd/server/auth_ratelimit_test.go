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

// A capped limiter charges both buckets, all or nothing: the burst refills
// in seconds, the cap only over the hour.
func TestRateLimiterCapped(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	l := newCappedRateLimiter(4, 10, time.Minute, 6, time.Hour)
	l.now = func() time.Time { return now }
	if ok, _ := l.takeN("k", 4); !ok {
		t.Fatal("the burst refused")
	}
	if ok, wait := l.takeN("k", 1); ok || wait != 6*time.Second {
		t.Fatalf("empty burst: ok=%v wait=%v, want one token in 6 s", ok, wait)
	}
	now = now.Add(time.Minute) // burst full again (4); cap 2 + 0.1
	// 0.9 cap tokens short at 6 an hour: 9 minutes.
	if ok, wait := l.takeN("k", 3); ok || (wait-9*time.Minute).Abs() > time.Millisecond {
		t.Fatalf("over the cap: ok=%v wait=%v, want 9m", ok, wait)
	}
	if ok, _ := l.takeN("k", 2); !ok {
		t.Fatal("within both refused: the refusal took tokens")
	}
	l.refundN("k", 2) // 4 and 2.1 again
	if ok, _ := l.takeN("k", 3); ok {
		t.Fatal("refund gave the cap bucket more than was taken")
	}
	if ok, _ := l.takeN("k", 2); !ok {
		t.Fatal("refund lost tokens")
	}
	l.refundN("k", 100) // never above either size: 4 and 6
	if ok, _ := l.takeN("k", 5); ok {
		t.Fatal("refund raised the burst above its size")
	}
	if ok, _ := l.takeN("k", 4); !ok {
		t.Fatal("refund did not fill the burst")
	}
	// A bucket is swept only once both have refilled.
	now = now.Add(30 * time.Minute)
	l.gc()
	if l.buckets["k"] == nil {
		t.Fatal("swept while the cap bucket was still refilling")
	}
	now = now.Add(time.Hour)
	l.gc()
	if len(l.buckets) != 0 {
		t.Fatal("full buckets kept")
	}
	// An uncapped limiter is unchanged by the cap fields.
	u := newRateLimiter(2, time.Minute)
	u.now = l.now
	u.refundN("x", 1)
	if ok, _ := u.takeN("x", 2); !ok {
		t.Fatal("uncapped limiter refused its burst")
	}
	if ok, _ := u.takeN("x", -3); !ok || u.buckets["x"].tokens != 0 {
		t.Fatalf("a negative take added tokens: %v", u.buckets["x"].tokens)
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
