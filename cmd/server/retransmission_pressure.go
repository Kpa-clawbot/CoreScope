package main

// retransmission_pressure.go: issue #1699.
//
// Network-wide time series of how many distinct repeaters took part in
// relaying each flood packet, as a proxy for collision pressure. Definition
// agreed in the #1699 thread: for one transmission, take the union of the
// paths of ALL its observations and count the distinct repeaters in it
// (paths [A], [A,B,C], [A,D] -> 4). Per time bucket we report the average of
// that count over the flood packets first seen in the bucket.
//
// It is a proxy, not a measured collision rate: a repeater that forwarded a
// packet no observer heard is invisible, so the number moves with observer
// coverage too. The response carries the per-bucket observer count so the UI
// can show that.
//
// Protocol facts (MeshCore firmware, commit 0679dbef):
//   - Only flood routes build a path of forwarders: routeRecvPacket appends
//     the forwarder's own hash to the end of the path (src/Mesh.cpp:344-356).
//     ROUTE_TYPE_TRANSPORT_FLOOD (0) and ROUTE_TYPE_FLOOD (1) are both flood
//     (src/Packet.h:14-15,64).
//   - Direct routes carry the route still to travel; each hop removes itself
//     (removeSelfFromPath, src/Mesh.cpp:78-106,334-342), so the observed path
//     is not the forwarders. Zero-hop sends are ROUTE_TYPE_DIRECT /
//     TRANSPORT_DIRECT with path_len 0 (src/Mesh.cpp:717-737). Both are
//     excluded by the route filter.
//   - TRACE never floods (sendFlood refuses it, src/Mesh.cpp:637-641) and its
//     path bytes are SNR values, not hashes (src/Mesh.cpp:59-61). Excluded
//     explicitly as well.
//   - A path entry is the first 1-3 bytes of the forwarder's public key; the
//     width is chosen by the originator and is the same for every hop of one
//     packet (src/Mesh.cpp:649, src/Packet.h:79-83, src/Identity.h:23-25).
//   - A node forwards a given flood once: routeRecvPacket runs only after
//     wasSeen()/markSeen() on the packet hash (e.g. src/Mesh.cpp:121-126), and
//     that hash excludes the path (src/Packet.cpp:41-50).
//
// Ambiguous prefixes. A 1-byte prefix is shared by many repeaters, and we do
// NOT resolve hops to public keys here: the resolved-pubkey index is empty
// for observations whose resolved_path is NULL (on live nearly every 1-byte
// observation), and context-based resolution of history is refused on
// purpose elsewhere (resolvePathForObsColdLoad, PR #1643). Counting rule:
//   - the same prefix in different observations is the same repeater, so
//     colliding repeaters merge and the count is a lower bound;
//   - the same prefix k times inside ONE path is k repeaters (a node forwards
//     a flood once), so a prefix counts as its highest multiplicity in any
//     single observed path.
// The share of packets on 1-byte hashes is reported so the undercount can be
// judged.
//
// Bucketing is by the transmission's first_seen; later observations of the
// same packet land in the bucket where it was first heard.
//
// Complexity: one pass over s.packets under s.mu.RLock, O(T + O + H) for T
// transmissions, O observations of flood packets and H hop entries, with no
// allocation per observation. Memory: one entry per non-empty bucket plus a
// per-pass observer index; the union set is reused across transmissions.
// Served from the analytics recomputer for the default shape and from a
// TTL cache otherwise, never computed per request on a warm cache.

import (
	"math/bits"
	"net/http"
	"sort"
	"strings"
	"time"
)

// RetransmissionBucket is one time bucket of the series.
type RetransmissionBucket struct {
	Start        string  `json:"start"`         // bucket start, RFC3339 UTC
	Packets      int     `json:"packets"`       // flood packets first seen in the bucket
	RepeaterSum  int     `json:"repeater_sum"`  // sum of distinct repeaters over those packets
	AvgRepeaters float64 `json:"avg_repeaters"` // repeater_sum / packets
	Observers    int     `json:"observers"`     // distinct observers that heard those packets
}

// RetransmissionSummary aggregates the whole response window.
type RetransmissionSummary struct {
	Packets           int     `json:"packets"`
	AvgRepeaters      float64 `json:"avg_repeaters"`
	Observers         int     `json:"observers"`
	OneBytePackets    int     `json:"one_byte_packets"`    // packets whose hops are 1-byte hashes (most ambiguous)
	NoRepeaterPackets int     `json:"no_repeater_packets"` // flood packets heard with an empty path only
}

// RetransmissionResponse is the /api/analytics/retransmissions body.
type RetransmissionResponse struct {
	BucketSeconds int                    `json:"bucket_seconds"`
	Window        string                 `json:"window"`
	Region        string                 `json:"region"`
	Summary       RetransmissionSummary  `json:"summary"`
	Buckets       []RetransmissionBucket `json:"buckets"`
}

type retransmissionCacheEntry struct {
	data      RetransmissionResponse
	expiresAt time.Time
}

// retransmissionCacheMax bounds the TTL cache: ?from=&to= makes the key space
// open-ended, and invalidation only runs when new paths arrive.
const retransmissionCacheMax = 64

const retransmissionDefaultBucket = time.Hour

// parseRetransmissionBucket maps the ?bucket= value to a duration. Unknown
// values fall back to the default, matching how ParseTimeWindow ignores
// invalid input.
func parseRetransmissionBucket(v string) time.Duration {
	switch v {
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "6h":
		return 6 * time.Hour
	case "1d":
		return 24 * time.Hour
	}
	return retransmissionDefaultBucket
}

// repeaterUnion counts distinct repeaters across the observed paths of one
// transmission (see the counting rule in the file header). Hops are keyed as
// (byte width, value) so case does not matter and a 1-byte "AB" differs from
// a 2-byte "AB00". Reuse one value across transmissions via reset().
//
// The set is an open-addressing hash table whose slots are stamped with a
// generation, so reset() is O(1) instead of clearing the table.
type repeaterUnion struct {
	slotKey []uint64
	slotCnt []int    // highest multiplicity of slotKey[i] within a single path
	slotGen []uint32 // slot is live when slotGen[i] == gen
	gen     uint32
	used    int
	total   int      // sum of slotCnt over live slots
	path    []uint64 // scratch: hop keys of the path being added
	width   int      // byte width of the first hop seen, 0 if none
}

const repeaterUnionMinSlots = 256

func (u *repeaterUnion) reset() {
	if u.slotKey == nil {
		u.allocSlots(repeaterUnionMinSlots)
	}
	u.gen++
	if u.gen == 0 {
		clear(u.slotGen)
		u.gen = 1
	}
	u.used = 0
	u.total = 0
	u.width = 0
}

func (u *repeaterUnion) allocSlots(n int) {
	u.slotKey = make([]uint64, n)
	u.slotCnt = make([]int, n)
	u.slotGen = make([]uint32, n)
}

// slot returns the index holding k, or the free index where k belongs.
func (u *repeaterUnion) slot(k uint64) int {
	mask := len(u.slotKey) - 1
	i := int((k * 0x9E3779B97F4A7C15) >> 40 & uint64(mask))
	for u.slotGen[i] == u.gen && u.slotKey[i] != k {
		i = (i + 1) & mask
	}
	return i
}

// grow doubles the table, keeping the live entries. Load stays below 1/2,
// so slot() always finds a free index.
func (u *repeaterUnion) grow() {
	oldKey, oldCnt, oldGen, gen := u.slotKey, u.slotCnt, u.slotGen, u.gen
	u.allocSlots(2 * len(oldKey))
	u.gen = 1
	for i := range oldKey {
		if oldGen[i] == gen {
			j := u.slot(oldKey[i])
			u.slotKey[j], u.slotCnt[j], u.slotGen[j] = oldKey[i], oldCnt[i], u.gen
		}
	}
}

// hopKey parses a hex hop into (width<<32 | value). ok is false for anything
// that is not 1-4 bytes of hex.
func hopKey(hop string) (uint64, bool) {
	if len(hop) == 0 || len(hop) > 8 || len(hop)%2 != 0 {
		return 0, false
	}
	var v uint64
	for i := 0; i < len(hop); i++ {
		c := hop[i]
		switch {
		case c >= '0' && c <= '9':
			c -= '0'
		case c >= 'a' && c <= 'f':
			c = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			c = c - 'A' + 10
		default:
			return 0, false
		}
		v = v<<4 | uint64(c)
	}
	return uint64(len(hop)/2)<<32 | v, true
}

// addPath folds one observation's path_json (a JSON array of hex strings)
// into the union. Scans the string directly: hop tokens are plain hex, so no
// JSON decoder and no allocation are needed.
func (u *repeaterUnion) addPath(pathJSON string) {
	u.path = u.path[:0]
	for i := 0; i < len(pathJSON); i++ {
		if pathJSON[i] != '"' {
			continue
		}
		end := strings.IndexByte(pathJSON[i+1:], '"')
		if end < 0 {
			break
		}
		if k, ok := hopKey(pathJSON[i+1 : i+1+end]); ok {
			u.path = append(u.path, k)
		}
		i += end + 1
	}
	if len(u.path) > 0 && u.width == 0 {
		u.width = int(u.path[0] >> 32)
	}
	for p, k := range u.path {
		occ := 1
		for _, prev := range u.path[:p] {
			if prev == k {
				occ++
			}
		}
		i := u.slot(k)
		if u.slotGen[i] != u.gen {
			if 2*(u.used+1) > len(u.slotKey) {
				u.grow()
				i = u.slot(k)
			}
			u.slotKey[i], u.slotCnt[i], u.slotGen[i] = k, occ, u.gen
			u.used++
			u.total += occ
		} else if occ > u.slotCnt[i] {
			u.total += occ - u.slotCnt[i]
			u.slotCnt[i] = occ
		}
	}
}

func (u *repeaterUnion) count() int {
	return u.total
}

type retransmissionBucketAgg struct {
	packets     int
	repeaterSum int
	observers   []uint64 // bitset over the pass-local observer index
}

func setBit(set []uint64, i int) []uint64 {
	for len(set) <= i/64 {
		set = append(set, 0)
	}
	set[i/64] |= 1 << uint(i%64)
	return set
}

func popCount(set []uint64) int {
	n := 0
	for _, w := range set {
		n += bits.OnesCount64(w)
	}
	return n
}

// computeRetransmissionPressure builds the series. region filters on the
// observers of that region, like /api/analytics/rf: only their observations
// feed the union, and a packet none of them heard is skipped.
func (s *PacketStore) computeRetransmissionPressure(region string, window TimeWindow, bucket time.Duration) RetransmissionResponse {
	if bucket <= 0 {
		bucket = retransmissionDefaultBucket
	}
	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}
	var since, until time.Time
	if window.Since != "" {
		since, _ = parseAnyRFC3339(window.Since)
	}
	if window.Until != "" {
		until, _ = parseAnyRFC3339(window.Until)
	}
	bucketSec := int64(bucket / time.Second)

	s.mu.RLock()
	defer s.mu.RUnlock()

	aggs := make(map[int64]*retransmissionBucketAgg)
	obsIndex := make(map[string]int)
	var allObservers []uint64
	var u repeaterUnion
	var summary RetransmissionSummary
	totalRepeaters := 0

	for _, tx := range s.packets {
		if tx.RouteType == nil || (*tx.RouteType != RouteFlood && *tx.RouteType != RouteTransportFlood) {
			continue
		}
		if tx.PayloadType != nil && *tx.PayloadType == PayloadTRACE {
			continue
		}
		t, err := parseAnyRFC3339(tx.FirstSeen)
		if err != nil {
			continue
		}
		if (!since.IsZero() && t.Before(since)) || (!until.IsZero() && t.After(until)) {
			continue
		}
		start := t.Unix() - t.Unix()%bucketSec
		agg := aggs[start]
		u.reset()
		heard := false
		for _, obs := range tx.Observations {
			if regionObs != nil && !regionObs[obs.ObserverID] {
				continue
			}
			if agg == nil {
				agg = &retransmissionBucketAgg{}
				aggs[start] = agg
			}
			heard = true
			u.addPath(obs.PathJSON)
			idx, ok := obsIndex[obs.ObserverID]
			if !ok {
				idx = len(obsIndex)
				obsIndex[obs.ObserverID] = idx
			}
			agg.observers = setBit(agg.observers, idx)
			allObservers = setBit(allObservers, idx)
		}
		if !heard {
			continue
		}
		n := u.count()
		agg.packets++
		agg.repeaterSum += n
		summary.Packets++
		totalRepeaters += n
		if n == 0 {
			summary.NoRepeaterPackets++
		}
		if u.width == 1 {
			summary.OneBytePackets++
		}
	}

	starts := make([]int64, 0, len(aggs))
	for st := range aggs {
		starts = append(starts, st)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	buckets := make([]RetransmissionBucket, 0, len(starts))
	for _, st := range starts {
		a := aggs[st]
		buckets = append(buckets, RetransmissionBucket{
			Start:        time.Unix(st, 0).UTC().Format(time.RFC3339),
			Packets:      a.packets,
			RepeaterSum:  a.repeaterSum,
			AvgRepeaters: float64(a.repeaterSum) / float64(a.packets),
			Observers:    popCount(a.observers),
		})
	}
	if summary.Packets > 0 {
		summary.AvgRepeaters = float64(totalRepeaters) / float64(summary.Packets)
	}
	summary.Observers = popCount(allObservers)

	label := window.Label
	if label == "" && !window.IsZero() {
		label = window.Since + "/" + window.Until
	}
	return RetransmissionResponse{
		BucketSeconds: int(bucketSec),
		Window:        label,
		Region:        region,
		Summary:       summary,
		Buckets:       buckets,
	}
}

func isDefaultRetransmissionShape(region string, window TimeWindow, bucket time.Duration) bool {
	return region == "" && window.IsZero() && bucket == retransmissionDefaultBucket
}

// GetRetransmissionPressure serves the default shape from the recomputer
// snapshot and every other shape from the TTL cache (compute on miss).
func (s *PacketStore) GetRetransmissionPressure(region string, window TimeWindow, bucket time.Duration) RetransmissionResponse {
	if isDefaultRetransmissionShape(region, window, bucket) {
		s.analyticsRecomputerMu.RLock()
		rc := s.recompRetransmissions
		s.analyticsRecomputerMu.RUnlock()
		if rc != nil {
			if r, ok := rc.Load().(RetransmissionResponse); ok {
				s.cacheMu.Lock()
				s.cacheHits++
				s.cacheMu.Unlock()
				return r
			}
		}
	}
	key := region + "|" + window.CacheKey() + "|" + bucket.String()
	s.cacheMu.Lock()
	if e, ok := s.retransCache[key]; ok && time.Now().Before(e.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return e.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeRetransmissionPressure(region, window, bucket)

	s.cacheMu.Lock()
	if s.retransCache == nil || len(s.retransCache) >= retransmissionCacheMax {
		s.retransCache = make(map[string]*retransmissionCacheEntry)
	}
	s.retransCache[key] = &retransmissionCacheEntry{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()
	return result
}

func (s *Server) handleAnalyticsRetransmissions(w http.ResponseWriter, r *http.Request) {
	region := r.URL.Query().Get("region")
	window := ParseTimeWindow(r)
	bucket := parseRetransmissionBucket(r.URL.Query().Get("bucket"))
	if s.store == nil {
		writeJSON(w, RetransmissionResponse{BucketSeconds: int(bucket / time.Second), Buckets: []RetransmissionBucket{}})
		return
	}
	// #1659 warmup gate (see handleAnalyticsRF for rationale).
	if isDefaultRetransmissionShape(region, window, bucket) {
		s.store.analyticsRecomputerMu.RLock()
		rc := s.store.recompRetransmissions
		s.store.analyticsRecomputerMu.RUnlock()
		if rc != nil && rc.IsWarmingUp_1659() {
			writeAnalyticsWarmup503(w)
			return
		}
	}
	writeJSON(w, s.store.GetRetransmissionPressure(region, window, bucket))
}
