package main

import (
	"testing"
	"time"
)

func TestHealthRateLimiter(t *testing.T) {
	rl := &HealthRateLimiter{
		entries:       make(map[string]*healthRateLimitEntry),
		windowSeconds: 60,
		maxRequests:   3,
	}

	ip := "1.2.3.4"

	// First 3 should be allowed
	for i := 0; i < 3; i++ {
		if !rl.Allow(ip) {
			t.Errorf("request %d should be allowed", i+1)
		}
	}
	// 4th should be blocked
	if rl.Allow(ip) {
		t.Error("4th request should be rate-limited")
	}
}

func TestHealthRateLimiterWindowReset(t *testing.T) {
	rl := &HealthRateLimiter{
		entries:       make(map[string]*healthRateLimitEntry),
		windowSeconds: 1,
		maxRequests:   2,
	}

	ip := "5.6.7.8"
	rl.Allow(ip)
	rl.Allow(ip)
	if rl.Allow(ip) {
		t.Error("3rd in window should be blocked")
	}

	// Advance past window
	time.Sleep(1100 * time.Millisecond)
	if !rl.Allow(ip) {
		t.Error("first request in new window should be allowed")
	}
}

func TestHealthRateLimiterDifferentIPs(t *testing.T) {
	rl := &HealthRateLimiter{
		entries:       make(map[string]*healthRateLimitEntry),
		windowSeconds: 60,
		maxRequests:   1,
	}
	if !rl.Allow("1.1.1.1") {
		t.Error("first ip should be allowed")
	}
	if !rl.Allow("2.2.2.2") {
		t.Error("different ip should be allowed")
	}
	if rl.Allow("1.1.1.1") {
		t.Error("second request from first ip should be blocked")
	}
}
