package main

import (
	"sync"
	"time"
)

type healthRateLimitEntry struct {
	count       int
	windowStart time.Time
}

// HealthRateLimiter implements a fixed-window per-IP rate limiter.
type HealthRateLimiter struct {
	mu            sync.Mutex
	entries       map[string]*healthRateLimitEntry
	windowSeconds int
	maxRequests   int
	done          chan struct{}
}

func NewHealthRateLimiter(windowSeconds, maxRequests int) *HealthRateLimiter {
	rl := &HealthRateLimiter{
		entries:       make(map[string]*healthRateLimitEntry),
		windowSeconds: windowSeconds,
		maxRequests:   maxRequests,
		done:          make(chan struct{}),
	}
	go rl.cleanup()
	return rl
}

func (rl *HealthRateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	window := time.Duration(rl.windowSeconds) * time.Second

	e, ok := rl.entries[ip]
	if !ok || now.Sub(e.windowStart) > window {
		rl.entries[ip] = &healthRateLimitEntry{count: 1, windowStart: now}
		return true
	}
	if e.count >= rl.maxRequests {
		return false
	}
	e.count++
	return true
}

func (rl *HealthRateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-rl.done:
			return
		case <-ticker.C:
			rl.mu.Lock()
			cutoff := time.Now().Add(-time.Duration(rl.windowSeconds) * time.Second)
			for ip, e := range rl.entries {
				if e.windowStart.Before(cutoff) {
					delete(rl.entries, ip)
				}
			}
			rl.mu.Unlock()
		}
	}
}

func (rl *HealthRateLimiter) Stop() {
	select {
	case <-rl.done:
	default:
		close(rl.done)
	}
}
