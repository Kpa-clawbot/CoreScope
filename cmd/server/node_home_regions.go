// Package main: zero-hop home regions for the /api/nodes region filter (#1879).
//
// The region filter used to put a node in every region where any observer
// heard any of its adverts. Adverts are flooded, so a node leaked into each
// region its adverts were relayed into. The #804 home-region rule uses
// receptions of DIRECT adverts with an empty remaining path. This recomputer
// turns those observations into a set of home regions per node.
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

	"github.com/meshcore-analyzer/packetpath"
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
			if iata == "" || !obs.receivedForRegion() {
				continue
			}
			h, known := obs.regionHopCount()
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

// The stored direction still describes the first observer/path row. A legacy
// duplicate can independently prove a reception without changing that row.
type advertReception struct {
	kind            uint8
	wireHopsPlusOne uint8 // zero when the frame supplied no hop count
}

const (
	advertMissingFrame uint8 = iota
	advertTX
	advertReceived
	advertReceivedDirect
)

// Read the two observation-level facts while raw_hex is in the scan window.
// Validated relay paths already establish the flood hop count.
func observedFrameEvidence(raw, path, direction string) (uint8, advertReception) {
	width := observedRelayHopHexLen(raw, path)
	if direction == "tx" {
		return width, advertReception{kind: advertTX}
	}
	if raw == "" {
		return width, advertReception{kind: advertMissingFrame}
	}
	if width != 0 {
		_, pathByte, _ := packetpath.PathByte(raw)
		return width, advertReception{kind: advertReceived, wireHopsPlusOne: (pathByte & 63) + 1}
	}
	// A nonempty observation path rules out direct-empty, even if the
	// frame disagrees. Ordinary relayed reports need no extra packet decode.
	if !emptyOrMissingPath(path) {
		return width, advertReception{kind: advertReceived}
	}
	switch packetpath.AdvertRouteEvidence(raw) {
	case packetpath.AdvertDirectEmptyPath:
		return width, advertReception{kind: advertReceivedDirect, wireHopsPlusOne: 1}
	case packetpath.AdvertFlood:
		_, pathByte, _ := packetpath.PathByte(raw)
		return width, advertReception{kind: advertReceived, wireHopsPlusOne: (pathByte & 63) + 1}
	}
	return width, advertReception{kind: advertReceived}
}

func emptyOrMissingPath(path string) bool {
	if strings.HasPrefix(path, `["`) {
		return false
	}
	p := strings.TrimSpace(path)
	if p == "" || p == "null" || p == "[]" {
		return true
	}
	n, ok := countStringArray(p)
	return ok && n == 0
}

func (o *StoreObs) receivedForRegion() bool {
	return o.reception.kind != advertTX && (o.reception.kind != advertMissingFrame || !o.isOwnTransmission())
}

func (o *StoreObs) regionHopCount() (int, bool) {
	if n := o.reception.wireHopsPlusOne; n > 0 {
		return int(n) - 1, true
	}
	n, ok := countStringArray(o.PathJSON)
	if !ok || n > 63 {
		return 0, false
	}
	for pos, i := 0, 0; i < n; i++ {
		hop, next, _ := nextPathHop(o.PathJSON, pos)
		if _, valid := hopKey(hop); !valid || len(hop) > 6 {
			return 0, false
		}
		pos = next
	}
	return n, true
}

func (o *StoreObs) receivedDirectAdvert(tx *StoreTx) bool {
	if o.reception.kind == advertReceivedDirect {
		return true
	}
	// Without observation frames, the representative is attributable only
	// to a sole reception whose path does not contradict an empty one.
	// A recorded flood for this content makes even a single retained row
	// ambiguous. The transmission-wide union cannot identify who heard it.
	return o.reception.kind == advertMissingFrame && len(tx.Observations) == 1 &&
		tx.AdvertRouteEvidence&packetpath.AdvertFlood == 0 &&
		!o.isOwnTransmission() && emptyOrMissingPath(o.PathJSON) &&
		packetpath.AdvertRouteEvidence(tx.RawHex) == packetpath.AdvertDirectEmptyPath
}

// A later TX cannot supply direct evidence for a reception. Folding two
// reports also makes borrowing the canonical frame for a legacy row unsafe.
func (o *StoreObs) mergeAdvertReception(id int, b advertReception) {
	// A poll retry can read the very same legacy row again.
	if id == o.ID && b.kind == advertMissingFrame {
		return
	}
	a := &o.reception
	if a.kind == advertMissingFrame {
		a.kind = advertReceived
	}
	if b.kind == advertMissingFrame {
		b.kind = advertReceived
	}
	a.kind = max(a.kind, b.kind)
	if n := b.wireHopsPlusOne; n > 0 && (a.wireHopsPlusOne == 0 || n < a.wireHopsPlusOne) {
		a.wireHopsPlusOne = n
	}
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
