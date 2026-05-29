package main

import (
	"sync/atomic"
	"time"
)

// #1481 P0-1: cached default-filter neighbor-graph response.
//
// The /api/analytics/neighbor-graph handler does graph build + per-edge
// score + filter + ~900KB JSON marshal on every request. The default
// (no-region, no-role, minCount=5, minScore=0.1) shape covers the
// overwhelming majority of organic traffic; cache it as a fully-built
// response served from an atomic pointer. Recomputed every 5 minutes
// in the background — never on the hot path.

const neighborGraphCacheInterval = 5 * time.Minute

// neighborGraphCacheEntry is the cached response for a fixed filter shape.
type neighborGraphCacheEntry struct {
	resp NeighborGraphResponse
	at   time.Time
}

// Server cache fields are declared on Server below via embedded helpers
// (we don't add directly to the struct definition to keep the perf hot
// path obvious in routes.go).

type neighborGraphCacheField struct {
	ptr atomic.Pointer[neighborGraphCacheEntry]
}

// startNeighborGraphRecomputer launches a background goroutine that
// rebuilds the default-shape response every interval. Returns a stop
// closure. Safe to call once per Server.
func (s *Server) startNeighborGraphRecomputer(interval time.Duration, stop <-chan struct{}) {
	if interval <= 0 {
		interval = neighborGraphCacheInterval
	}
	go func() {
		s.recomputeNeighborGraphCache()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.recomputeNeighborGraphCache()
			case <-stop:
				return
			}
		}
	}()
}

// recomputeNeighborGraphCache builds the default-shape NeighborGraphResponse
// and atomically stores it. Defensive against panics so a single bad
// rebuild doesn't kill the background goroutine.
func (s *Server) recomputeNeighborGraphCache() {
	defer func() { _ = recover() }()
	resp := s.buildDefaultNeighborGraphResponse()
	s.neighborGraphCache.ptr.Store(&neighborGraphCacheEntry{
		resp: resp,
		at:   time.Now(),
	})
}

// loadNeighborGraphCache returns the cached default response if present
// and non-empty, else (zero, false).
func (s *Server) loadNeighborGraphCache() (NeighborGraphResponse, bool) {
	e := s.neighborGraphCache.ptr.Load()
	if e == nil {
		return NeighborGraphResponse{}, false
	}
	return e.resp, true
}
