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
	l.refundN("ip", 1)
	if ok, _ := l.take("ip"); !ok {
		t.Fatal("refund must give the token back")
	}
	l.refundN("ip", 1)
	l.refundN("ip", 1) // never above burst
	l.take("ip")
	if ok, _ := l.take("ip"); ok {
		t.Fatal("refund must not raise the bucket above its burst")
	}
	l.refundN("unknown", 1) // no bucket: no-op, no panic
}
