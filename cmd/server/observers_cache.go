package main

// Stub for observers cache. Replaced in the follow-up commit with real
// atomic-pointer-backed cache (issue #1481 P0-3).

import (
	"sync/atomic"
	"time"
)

// observersCacheTTL is the freshness window for the cached default
// (no-filter) /api/observers response.
const observersCacheTTL = 30 * time.Second

// observersCache holds the latest cached /api/observers response.
type observersCacheField = atomic.Pointer[ObserverListResponse]

// helper: returns true if the timestamp is older than TTL or zero.
func (s *Server) observersCacheExpired(t time.Time) bool {
	if t.IsZero() {
		return true
	}
	return time.Since(t) >= observersCacheTTL
}
