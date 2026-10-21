// Package main: zero-hop home regions for the /api/nodes region filter (#1879).
//
// The region filter used to put a node in every region where any observer
// heard any of its adverts. Adverts are flooded, so a node leaked into each
// region its adverts were relayed into. A zero-hop DIRECT advert is not
// relayed, so an observer that hears one is within RF range of the node (#804).
// This recomputer turns those hearings into a set of home regions per node.
//
// A node never heard zero-hop goes in the regions that heard it in the fewest
// hops, and in no region when even that is over regionFallback.maxHops (it still
// shows unfiltered). Every region that heard it at all, the old fallback, put
// most nodes in every region on a statewide deployment: adverts flood the mesh,
// so observers in each metro hear far-off nodes many hops out. The observer-
// based check now decides only for nodes newer than the last recompute.
//
// Each node's outcome is a NodeRegionPlacement, which RegionNodePubkeys reads
// and /api/nodes returns as region_placement, so an operator can see which of
// the four rules placed a node.
//
// A set rather than #804's plurality winner: a node on a region boundary,
// heard zero-hop in two regions, belongs to both, so per-region node counts
// no longer sum to the node total.
//
// Same shape as bridge_recomputer.go: background ticker, atomic snapshot,
// synchronous prewarm at start, plus one recompute when the startup load is
// done. Each recompute makes two passes over the in-memory adverts (zero-hop
// counts, then fewest hops; decoded JSON cached on the tx) under s.mu read,
// off the request path: BenchmarkRecomputeNodeHomeRegionSets. RegionNodePubkeys
// and handleNodes read the snapshot with a single atomic load.
package main

import (
	"sort"
	"strings"
	"sync"
	"time"
)

const nodeHomeRegionsRecomputerDefaultInterval = 5 * time.Minute

// regionFallbackMaxHopsDefault bounds the fallback for nodes never heard
// zero-hop: a node whose closest hearing took more hops than this is in no
// region. Config regionFallback.maxHops overrides it (Config.RegionFallbackMaxHops).
const regionFallbackMaxHopsDefault = 3

var (
	nodeHomeRegionsRecompStartedMu sync.Mutex
	nodeHomeRegionsRecompStarted   bool
)

// StartNodeHomeRegionsRecomputer computes the region placements once, again
// when the startup load is done, then every interval (default 5min if <= 0). Idempotent: later calls are no-ops
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

	// main.go starts this at the first load chunk, so the prewarm saw only
	// part of the store. Recompute once the whole startup load (hot window
	// and background fill) is done, as the analytics recomputers do.
	loaded := s.StartupLoadDone()

	var stopOnce sync.Once
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-loaded:
				loaded = nil // once
				s.recomputeNodeHomeRegionSets()
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
	placements := regionPlacements(counts, hops, s.config.RegionFallbackMaxHops())
	s.nodeRegionPlacements.Store(&placements)
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
			h, known := pathHopCount(obs.PathJSON)
			if !known {
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
// json.Unmarshal would allocate per observation. known is false for a missing
// path (legacy rows with NULL path_json): "[]" is a direct hearing, "" is not.
func pathHopCount(pathJSON string) (hops int, known bool) {
	p := strings.TrimSpace(pathJSON)
	switch p {
	case "", "null":
		return 0, false
	case "[]":
		return 0, true
	}
	return strings.Count(p, ",") + 1, true
}

// RegionBasis names the rule that decided a node's regions for ?region=.
type RegionBasis string

const (
	// RegionBasisZeroHop: observers heard the node zero-hop; Regions are
	// theirs.
	RegionBasisZeroHop RegionBasis = "zero_hop"
	// RegionBasisFewestHops: never heard zero-hop; Regions heard it in the
	// fewest hops (Hops), at most regionFallback.maxHops.
	RegionBasisFewestHops RegionBasis = "fewest_hops"
	// RegionBasisNone: never heard zero-hop, and its closest hearing (Hops)
	// is over regionFallback.maxHops, so it is in no region.
	RegionBasisNone RegionBasis = "none"
	// RegionBasisObserverScan: not in the last recompute (its first advert
	// is newer, the store holds none, or every hearing lacks a path); the
	// filter puts it in each region whose observers heard any of its adverts.
	RegionBasisObserverScan RegionBasis = "observer_scan"
)

// NodeRegionPlacement is which rule placed a node for the /api/nodes region
// filter, and where (#1879). Returned on each node as region_placement.
type NodeRegionPlacement struct {
	Basis RegionBasis `json:"basis"`
	// Regions is sorted; absent for none and observer_scan.
	Regions []string `json:"regions,omitempty"`
	// Hops is the fewest hops any region heard the node over; set for
	// fewest_hops and none.
	Hops *int `json:"hops,omitempty"`
}

// regionPlacements places every node in zeroHop (per-region zero-hop counts)
// by those regions, and every other node in fewest (per-region fewest hops)
// by its fewest-hop regions, or in none past maxHops.
func regionPlacements(zeroHop, fewest map[string]map[string]int, maxHops int) map[string]NodeRegionPlacement {
	out := make(map[string]NodeRegionPlacement, len(fewest))
	for pk, perIATA := range zeroHop {
		regions := make([]string, 0, len(perIATA))
		for iata := range perIATA {
			regions = append(regions, iata)
		}
		sort.Strings(regions)
		out[pk] = NodeRegionPlacement{Basis: RegionBasisZeroHop, Regions: regions}
	}
	for pk, perIATA := range fewest {
		if _, placed := out[pk]; placed || len(perIATA) == 0 {
			continue
		}
		hops := -1
		for _, h := range perIATA {
			if hops < 0 || h < hops {
				hops = h
			}
		}
		if hops > maxHops {
			out[pk] = NodeRegionPlacement{Basis: RegionBasisNone, Hops: &hops}
			continue
		}
		regions := []string{}
		for iata, h := range perIATA {
			if h == hops {
				regions = append(regions, iata)
			}
		}
		sort.Strings(regions)
		out[pk] = NodeRegionPlacement{Basis: RegionBasisFewestHops, Regions: regions, Hops: &hops}
	}
	return out
}

// GetNodeRegionPlacements returns the current snapshot (pubkey → placement),
// or an empty map before the first compute. A node absent from it is placed by
// the observer scan. Read-only by convention: the map is shared by all readers.
func (s *PacketStore) GetNodeRegionPlacements() map[string]NodeRegionPlacement {
	snap := s.nodeRegionPlacements.Load()
	if snap == nil {
		return map[string]NodeRegionPlacement{}
	}
	return *snap
}

// RegionPlacementFor returns a node's placement from a snapshot, observer_scan
// when the snapshot does not cover it.
func RegionPlacementFor(placements map[string]NodeRegionPlacement, pubkey string) NodeRegionPlacement {
	if p, ok := placements[pubkey]; ok {
		return p
	}
	return NodeRegionPlacement{Basis: RegionBasisObserverScan}
}
