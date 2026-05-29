package main

// observers cache for /api/observers default (no-filter) response.
// Issue #1481 P0-3 + #1483 follow-up.
//
// Design:
//   - Atomic pointer holds the immutable cached response.
//   - Wall-clock TTL replaced with monotonic time.Time (#1483: NTP
//     step-backward must not extend the cache).
//   - singleflight collapses TTL-boundary thundering herd into one
//     SQL fill, regardless of incoming concurrency.

import (
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// observersCacheTTL is the default freshness window for the cached
// default (no-filter) /api/observers response when no per-server
// override is configured. Configurable via ObserversCache.TTLSeconds
// (#1483).
const observersCacheTTL = 30 * time.Second

// effectiveObserversCacheTTL returns the cfg-overridden TTL or the
// default. Falls back to the default on nil cfg / non-positive value.
func (s *Server) effectiveObserversCacheTTL() time.Duration {
	if s.cfg != nil && s.cfg.ObserversCache != nil && s.cfg.ObserversCache.TTLSeconds > 0 {
		return time.Duration(s.cfg.ObserversCache.TTLSeconds) * time.Second
	}
	return observersCacheTTL
}

// singleflight key for the default-shape cache fill.
const observersCacheFlightKey = "observers:default"

// observersCacheEntry pairs the response with the monotonic timestamp
// of when it was built. atomic.Pointer guarantees the read is a single
// load; the entry is immutable once stored.
type observersCacheEntry struct {
	resp ObserverListResponse
	at   time.Time
}

// observersCacheField bundles the atomic pointer with the singleflight
// group that gates concurrent refills.
type observersCacheField struct {
	ptr atomic.Pointer[observersCacheEntry]
	sf  singleflight.Group

	// fillCount increments once per actual SQL fill (i.e., per
	// singleflight winner). Tests use this to assert the herd was
	// collapsed; production code never reads it.
	fillCount atomic.Int64
}

// observersCacheExpired reports whether the cached entry at `t` is
// older than observersCacheTTL or absent (zero time).
func (s *Server) observersCacheExpired(t time.Time) bool {
	if t.IsZero() {
		return true
	}
	return time.Since(t) >= s.effectiveObserversCacheTTL()
}

// loadObserversCache returns the cached entry and its age, or nil.
func (s *Server) loadObserversCache() (*observersCacheEntry, bool) {
	e := s.observersCacheV2.ptr.Load()
	if e == nil {
		return nil, false
	}
	return e, true
}
