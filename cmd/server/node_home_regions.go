// Package main: zero-hop home regions for the /api/nodes region filter (#1879).
//
// The region filter used to put a node in every region where any observer
// heard any of its adverts. Adverts are flooded, so a node leaked into each
// region its adverts were relayed into. A zero-hop DIRECT advert is not
// relayed, so an observer that hears one is within RF range of the node (#804).
// This recomputer turns those hearings into a set of home regions per node;
// RegionNodePubkeys uses the set for nodes that have one.
//
// A node never heard zero-hop goes in the regions that heard it in the fewest
// hops, and in no region when even that is over regionFallbackMaxHops (it still
// shows unfiltered). Every region that heard it at all, the old fallback, put
// most nodes in every region on a statewide deployment: adverts flood the mesh,
// so observers in each metro hear far-off nodes many hops out. The observer-
// based check now decides only for nodes newer than the last recompute.
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
	"strings"
	"sync"
	"time"
)

const nodeHomeRegionsRecomputerDefaultInterval = 5 * time.Minute

// regionFallbackMaxHops bounds the fallback for nodes never heard zero-hop: a
// node whose closest hearing took more hops than this is in no region.
const regionFallbackMaxHops = 3

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
	var counts, hops map[string]map[string]int
	func() {
		s.mu.RLock()
		defer s.mu.RUnlock()
		counts = s.nodeZeroHopRegionCounts(obsIATA)
		hops = s.nodeFewestHopsByRegion(obsIATA)
	}()
	sets := homeRegionSets(counts)
	fallback := fallbackRegionSets(hops, sets)
	s.nodeHomeRegionSets.Store(&sets)
	s.nodeFallbackRegionSets.Store(&fallback)
}

// nodeFewestHopsByRegion returns, per advert pubkey, the fewest hops any
// observer in each region heard it over. Caller must hold s.mu (read).
func (s *PacketStore) nodeFewestHopsByRegion(obsIATA map[string]string) map[string]map[string]int {
	per := make(map[string]map[string]int, 256)
	if len(obsIATA) == 0 {
		return per
	}
	for _, tx := range s.byPayloadType[PayloadADVERT] {
		var m map[string]int
		for _, obs := range tx.Observations {
			iata := obsIATA[obs.ObserverID]
			if iata == "" {
				continue
			}
			if m == nil {
				// Parsed only for adverts heard in some region, and cached on the tx.
				pk := advertPubkey(tx)
				if pk == "" {
					break
				}
				if m = per[pk]; m == nil {
					m = map[string]int{}
					per[pk] = m
				}
			}
			h := pathHopCount(obs.PathJSON)
			if cur, ok := m[iata]; !ok || h < cur {
				m[iata] = h
			}
		}
	}
	return per
}

// pathHopCount counts the hops in an observation's path_json without decoding
// it: the elements are hop-hash hex strings, so they hold no commas. Run over
// every advert observation per recompute, under s.mu, where pathLen's
// json.Unmarshal would allocate per observation.
func pathHopCount(pathJSON string) int {
	p := strings.TrimSpace(pathJSON)
	if p == "" || p == "[]" || p == "null" {
		return 0
	}
	return strings.Count(p, ",") + 1
}

// fallbackRegionSets places each node without home regions in the regions
// that heard it in the fewest hops, or none (an empty set, so RegionNodePubkeys
// knows not to fall back further) when that is over regionFallbackMaxHops.
func fallbackRegionSets(hops map[string]map[string]int, homes map[string][]string) map[string][]string {
	out := make(map[string][]string, len(hops))
	for pk, perIATA := range hops {
		if _, placed := homes[pk]; placed {
			continue
		}
		fewest := -1
		for _, h := range perIATA {
			if fewest < 0 || h < fewest {
				fewest = h
			}
		}
		regions := []string{}
		if fewest <= regionFallbackMaxHops {
			for iata, h := range perIATA {
				if h == fewest {
					regions = append(regions, iata)
				}
			}
			sort.Strings(regions)
		}
		out[pk] = regions
	}
	return out
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

// GetNodeFallbackRegionSets returns the current fallback snapshot (pubkey →
// sorted regions, empty for a node in no region), or an empty map before the
// first compute. Read-only by convention.
func (s *PacketStore) GetNodeFallbackRegionSets() map[string][]string {
	snap := s.nodeFallbackRegionSets.Load()
	if snap == nil {
		return map[string][]string{}
	}
	return *snap
}

// RegionBasis names the rule that decided a node's regions for ?region=.
type RegionBasis string

const (
	RegionBasisZeroHop      RegionBasis = "zero_hop"
	RegionBasisFewestHops   RegionBasis = "fewest_hops"
	RegionBasisNone         RegionBasis = "none"
	RegionBasisObserverScan RegionBasis = "observer_scan"
)

// NodeRegionPlacement is a node's region_placement on /api/nodes.
type NodeRegionPlacement struct {
	Basis   RegionBasis `json:"basis"`
	Regions []string    `json:"regions,omitempty"`
	Hops    *int        `json:"hops,omitempty"`
}

// GetNodeRegionPlacements is a stub.
func (s *PacketStore) GetNodeRegionPlacements() map[string]NodeRegionPlacement {
	return map[string]NodeRegionPlacement{}
}
