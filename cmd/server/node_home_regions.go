// Package main: zero-hop home regions for the /api/nodes region filter (#1879).
//
// The region filter used to put a node in every region where any observer
// heard any of its adverts. Adverts are flooded, so a node leaked into each
// region its adverts were relayed into. A zero-hop DIRECT advert is not
// relayed, so an observer that hears one is within RF range of the node (#804).
// This recomputer turns those hearings into a set of home regions per node;
// RegionNodePubkeys uses the set for nodes that have one, and keeps the
// observer-based check only for nodes never heard zero-hop.
//
// A set rather than #804's plurality winner: a node on a region boundary,
// heard zero-hop in two regions, belongs to both, so per-region node counts
// no longer sum to the node total.
//
// Same shape as bridge_recomputer.go: background ticker, atomic snapshot,
// synchronous prewarm at start. The cost is the computeNodeHomeRegions scan
// (one pass over the in-memory packets, decoded JSON cached on the tx) once
// per interval, off the request path; RegionNodePubkeys and handleNodes read
// the snapshot with a single atomic load.
package main

import (
	"sort"
	"sync"
	"time"
)

const nodeHomeRegionsRecomputerDefaultInterval = 5 * time.Minute

var (
	nodeHomeRegionsRecompStartedMu sync.Mutex
	nodeHomeRegionsRecompStarted   bool
)

// StartNodeHomeRegionsRecomputer computes the home region sets once, then
// every interval (default 5min if <= 0). Idempotent: later calls are no-ops
// and return a no-op stop closure.
func (s *PacketStore) StartNodeHomeRegionsRecomputer(interval time.Duration) func() {
	if interval <= 0 {
		interval = nodeHomeRegionsRecomputerDefaultInterval
	}

	nodeHomeRegionsRecompStartedMu.Lock()
	if nodeHomeRegionsRecompStarted {
		nodeHomeRegionsRecompStartedMu.Unlock()
		return func() {}
	}
	nodeHomeRegionsRecompStarted = true
	stop := make(chan struct{})
	done := make(chan struct{})
	nodeHomeRegionsRecompStartedMu.Unlock()

	s.recomputeNodeHomeRegionSets()

	var stopOnce sync.Once
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.recomputeNodeHomeRegionSets()
			case <-stop:
				return
			}
		}
	}()

	return func() {
		stopOnce.Do(func() {
			close(stop)
		})
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

// recomputeNodeHomeRegionSets rebuilds and installs the snapshot. A panic in
// compute is swallowed so the goroutine never dies; the previous snapshot
// stays valid.
func (s *PacketStore) recomputeNodeHomeRegionSets() {
	defer func() { _ = recover() }()
	// The observers table is read before s.mu, as for computeNodeHomeRegions (#2146).
	obsIATA := s.observerIATAs()
	counts := func() map[string]map[string]int {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.nodeZeroHopRegionCounts(obsIATA)
	}()
	sets := homeRegionSets(counts)
	s.nodeHomeRegionSets.Store(&sets)
}

// homeRegionSets turns per-region zero-hop counts into each node's sorted set
// of home regions.
func homeRegionSets(counts map[string]map[string]int) map[string][]string {
	out := make(map[string][]string, len(counts))
	for pk, perIATA := range counts {
		regions := make([]string, 0, len(perIATA))
		for iata := range perIATA {
			regions = append(regions, iata)
		}
		sort.Strings(regions)
		out[pk] = regions
	}
	return out
}

// GetNodeHomeRegionSets returns the current snapshot (pubkey → sorted home
// regions), or an empty map before the first compute. Read-only by convention:
// the map is shared by all readers.
func (s *PacketStore) GetNodeHomeRegionSets() map[string][]string {
	snap := s.nodeHomeRegionSets.Load()
	if snap == nil {
		return map[string][]string{}
	}
	return *snap
}

// enrichNodeHomeRegions sets home_regions on a node row when zero-hop data
// placed it, and leaves the field absent otherwise, so an operator can tell
// which rule put a node in a region (#1879).
func enrichNodeHomeRegions(node map[string]interface{}, regions []string) {
	if node == nil || len(regions) == 0 {
		return
	}
	node["home_regions"] = regions
}
