package main

import (
	"testing"
	"time"
)

func TestRateLimiterRefundReturnsOneToken(t *testing.T) {
	l := newRateLimiter(1, time.Hour)
	if ok, _ := l.take("ip"); !ok {
		t.Fatal("first take must succeed")
	}
	if ok, _ := l.take("ip"); ok {
		t.Fatal("bucket of 1 must be empty after one take")
	}
	l.refund("ip")
	if ok, _ := l.take("ip"); !ok {
		t.Fatal("refund must give the token back")
	}
	l.refund("ip")
	l.refund("ip") // never above burst
	l.take("ip")
	if ok, _ := l.take("ip"); ok {
		t.Fatal("refund must not raise the bucket above its burst")
	}
	l.refund("unknown") // no bucket: no-op, no panic
}
