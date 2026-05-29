package main

import (
	"bytes"
	"encoding/json"
	"sync/atomic"
	"time"
)

// #1481 P0-1: cached default-filter neighbor-graph response.
//
// The /api/analytics/neighbor-graph handler does graph build + per-edge
// score + filter + ~900KB JSON marshal on every request. The default
// (no-region, no-role, minCount=5, minScore=0.1) shape covers the
// overwhelming majority of organic traffic; cache the fully-built AND
// pre-marshaled response so warm reads are a single Write. Recomputed
// every 5 minutes in the background — never on the hot path.

const neighborGraphCacheInterval = 5 * time.Minute

// neighborGraphCacheEntry holds both the response struct (kept for
// tests / structured access) and the pre-marshaled bytes that the
// handler writes verbatim.
type neighborGraphCacheEntry struct {
	resp NeighborGraphResponse
	json []byte
	at   time.Time
}

type neighborGraphCacheField struct {
	ptr atomic.Pointer[neighborGraphCacheEntry]
}

// startNeighborGraphRecomputer launches a background goroutine that
// rebuilds the default-shape response every interval. Returns when
// the stop channel is closed.
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

// recomputeNeighborGraphCache builds and pre-marshals the default-shape
// response and atomically swaps it in. Panic-defensive so a single bad
// rebuild doesn't kill the background goroutine.
func (s *Server) recomputeNeighborGraphCache() {
	defer func() { _ = recover() }()
	resp := s.buildDefaultNeighborGraphResponse()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(resp); err != nil {
		return
	}
	s.neighborGraphCache.ptr.Store(&neighborGraphCacheEntry{
		resp: resp,
		json: buf.Bytes(),
		at:   time.Now(),
	})
}

// loadNeighborGraphCache returns the cached default response if present.
func (s *Server) loadNeighborGraphCache() (NeighborGraphResponse, bool) {
	e := s.neighborGraphCache.ptr.Load()
	if e == nil {
		return NeighborGraphResponse{}, false
	}
	return e.resp, true
}

// loadNeighborGraphCacheBytes returns the pre-marshaled JSON for the
// cached default response if present.
func (s *Server) loadNeighborGraphCacheBytes() ([]byte, bool) {
	e := s.neighborGraphCache.ptr.Load()
	if e == nil || len(e.json) == 0 {
		return nil, false
	}
	return e.json, true
}
