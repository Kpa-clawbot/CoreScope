package main

// retransmission_pressure.go: issue #1699.
//
// Network-wide time series of how many distinct repeaters took part in
// relaying each flood, as a proxy for collision pressure. Definition agreed
// in the #1699 thread: for one flood, take the union of the paths of ALL its
// observations and count the distinct repeaters in it (paths [A], [A,B,C],
// [A,D] -> 4). Per time bucket we report the average of that count over the
// floods that started in the bucket.
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
//   - The duplicate filter (wasSeen/markSeen, e.g. src/Mesh.cpp:121-126) is a
//     cyclic buffer of 160 packet hashes (src/helpers/SimpleMeshTables.h:9,
//     52-57). Once a hash is overwritten, the node forwards that packet again
//     if it comes back, so the same node can appear twice in one path.
//   - A node holds a received flood for at most 32 s before handling it
//     (MAX_RX_DELAY_MILLIS, src/Dispatcher.cpp:11,243-251), plus a random
//     retransmit delay of a few airtimes (examples/simple_repeater/
//     MyMesh.cpp:547-550).
//
// Flood events. transmissions.hash is UNIQUE and the packet hash excludes the
// path (src/Packet.cpp:41-50), so when the same bytes are flooded again later
// the new observations are appended to the existing transmission. The union
// over all of them would merge separate floods. We sort a transmission's
// observations by time and start a new event when the gap to the previous
// observation exceeds retransmissionEventGap; each event is counted on its
// own, in the bucket of its first observation.
//
// Retention floor. Events that start before now - retentionHours are dropped
// for every request shape: the store keeps observations from before the
// floor only for hashes that were heard again recently, so they are not the
// traffic of that period.
//
// Prefixes are not resolved. A 1-byte prefix is shared by many repeaters, and
// we do NOT resolve hops to public keys here: the resolved-pubkey index is
// empty for observations whose resolved_path is NULL (on live nearly every
// 1-byte observation), and context-based resolution of history is refused on
// purpose elsewhere (resolvePathForObsColdLoad, PR #1643). Counting rule: a
// prefix counts once per event, whether it repeats inside one path or across
// paths. A repeated 2- or 3-byte prefix is one node forwarding twice (see the
// duplicate filter above). A repeated 1-byte prefix can also be two nodes;
// counting it once keeps the value a lower bound, as does merging repeaters
// that share a prefix across paths. The share of 1-byte packets is reported
// so the undercount can be judged.
//
// Region. region filters on the observers of that region, like
// /api/analytics/rf. Events are split on all observations, so the filter
// does not change where an event starts. A region with no known observers is
// not filtered, the same as the other analytics endpoints.
//
// Complexity: one pass over s.packets under s.mu.RLock, O(T + O log k + H)
// for T transmissions, O observations of flood packets (k per transmission,
// sorted by time) and H hop entries. Timestamps are parsed once per
// observation and cached (StoreObs.ParsedTime). Memory: one entry per
// non-empty bucket, a per-pass observer index and scratch reused across
// transmissions. Served from the analytics recomputer for the default shape
// and from a TTL cache otherwise, never computed per request on a warm
// cache; concurrent misses on one key share a single compute.

import (
	"cmp"
	"math/bits"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
)

// RetransmissionBucket is one time bucket of the series.
type RetransmissionBucket struct {
	Start        string  `json:"start"`         // bucket start, RFC3339 UTC
	Packets      int     `json:"packets"`       // flood events that started in the bucket
	RepeaterSum  int     `json:"repeater_sum"`  // sum of distinct repeaters over those events
	AvgRepeaters float64 `json:"avg_repeaters"` // repeater_sum / packets
	Observers    int     `json:"observers"`     // distinct observers that heard those events
}

// RetransmissionSummary aggregates the whole response window.
type RetransmissionSummary struct {
	Packets           int     `json:"packets"`
	AvgRepeaters      float64 `json:"avg_repeaters"`
	Observers         int     `json:"observers"`
	OneBytePackets    int     `json:"one_byte_packets"`    // events whose hops are 1-byte hashes (most ambiguous)
	NoRepeaterPackets int     `json:"no_repeater_packets"` // events heard with an empty path only
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

// retransmissionEventGap is the settle time that separates two flood events
// of one transmission. It is well above the longest per-hop hold in firmware
// (32 s plus a retransmit delay, see the file header), so a flood still
// spreading is not cut. Gaps between re-floods of the same bytes range from
// minutes to weeks; a re-flood within 5 minutes merges into one event.
const retransmissionEventGap = 5 * time.Minute

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

// repeaterUnion counts the distinct hop prefixes across the observed paths of
// one flood event (see the counting rule in the file header). Hops are keyed
// as (byte width, value) so case does not matter and a 1-byte "AB" differs
// from a 2-byte "AB00". Reuse one value across events via reset().
//
// The set is an open-addressing hash table whose slots are stamped with a
// generation, so reset() is O(1) instead of clearing the table.
type repeaterUnion struct {
	slotKey []uint64
	slotGen []uint32 // slot is live when slotGen[i] == gen
	gen     uint32
	used    int
	width   int // byte width of the first hop seen, 0 if none
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
	u.width = 0
}

func (u *repeaterUnion) allocSlots(n int) {
	u.slotKey = make([]uint64, n)
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
	oldKey, oldGen, gen := u.slotKey, u.slotGen, u.gen
	u.allocSlots(2 * len(oldKey))
	u.gen = 1
	for i := range oldKey {
		if oldGen[i] == gen {
			j := u.slot(oldKey[i])
			u.slotKey[j], u.slotGen[j] = oldKey[i], u.gen
		}
	}
}

func (u *repeaterUnion) add(k uint64) {
	if u.width == 0 {
		u.width = int(k >> 32)
	}
	i := u.slot(k)
	if u.slotGen[i] == u.gen {
		return
	}
	if 2*(u.used+1) > len(u.slotKey) {
		u.grow()
		i = u.slot(k)
	}
	u.slotKey[i], u.slotGen[i] = k, u.gen
	u.used++
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
	for i := 0; i < len(pathJSON); i++ {
		if pathJSON[i] != '"' {
			continue
		}
		end := strings.IndexByte(pathJSON[i+1:], '"')
		if end < 0 {
			break
		}
		if k, ok := hopKey(pathJSON[i+1 : i+1+end]); ok {
			u.add(k)
		}
		i += end + 1
	}
}

func (u *repeaterUnion) count() int {
	return u.used
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

// timedObs is an observation with its parsed time, for sorting one
// transmission's observations into flood events.
type timedObs struct {
	at  int64 // unix nanoseconds
	obs *StoreObs
}

// retransmissionPass accumulates one computeRetransmissionPressure pass.
type retransmissionPass struct {
	regionObs      map[string]bool
	floor          time.Time // events starting before it are dropped; zero = none
	since, until   time.Time
	bucketSec      int64
	aggs           map[int64]*retransmissionBucketAgg
	obsIndex       map[string]int
	allObservers   []uint64
	union          repeaterUnion
	summary        RetransmissionSummary
	totalRepeaters int
}

// addEvent counts one flood event: ev holds its observations in time order.
func (p *retransmissionPass) addEvent(ev []timedObs) {
	t := time.Unix(0, ev[0].at)
	if (!p.floor.IsZero() && t.Before(p.floor)) ||
		(!p.since.IsZero() && t.Before(p.since)) || (!p.until.IsZero() && t.After(p.until)) {
		return
	}
	start := t.Unix() - t.Unix()%p.bucketSec
	agg := p.aggs[start]
	u := &p.union
	u.reset()
	heard := false
	for _, e := range ev {
		obs := e.obs
		if p.regionObs != nil && !p.regionObs[obs.ObserverID] {
			continue
		}
		if agg == nil {
			agg = &retransmissionBucketAgg{}
			p.aggs[start] = agg
		}
		heard = true
		u.addPath(obs.PathJSON)
		idx, ok := p.obsIndex[obs.ObserverID]
		if !ok {
			idx = len(p.obsIndex)
			p.obsIndex[obs.ObserverID] = idx
		}
		agg.observers = setBit(agg.observers, idx)
		p.allObservers = setBit(p.allObservers, idx)
	}
	if !heard {
		return
	}
	n := u.count()
	agg.packets++
	agg.repeaterSum += n
	p.summary.Packets++
	p.totalRepeaters += n
	if n == 0 {
		p.summary.NoRepeaterPackets++
	}
	if u.width == 1 {
		p.summary.OneBytePackets++
	}
}

// computeRetransmissionPressure builds the series (see the file header for
// flood events, the retention floor, the counting rule and region).
func (s *PacketStore) computeRetransmissionPressure(region string, window TimeWindow, bucket time.Duration) RetransmissionResponse {
	if bucket <= 0 {
		bucket = retransmissionDefaultBucket
	}
	p := retransmissionPass{
		bucketSec: int64(bucket / time.Second),
		aggs:      make(map[int64]*retransmissionBucketAgg),
		obsIndex:  make(map[string]int),
	}
	if region != "" {
		p.regionObs = s.resolveRegionObservers(region)
	}
	if window.Since != "" {
		p.since, _ = parseAnyRFC3339(window.Since)
	}
	if window.Until != "" {
		p.until, _ = parseAnyRFC3339(window.Until)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.retentionHours > 0 {
		p.floor = time.Now().Add(-time.Duration(s.retentionHours * float64(time.Hour)))
	}
	gap := int64(retransmissionEventGap)
	var timed []timedObs

	for _, tx := range s.packets {
		if tx.RouteType == nil || (*tx.RouteType != RouteFlood && *tx.RouteType != RouteTransportFlood) {
			continue
		}
		if tx.PayloadType != nil && *tx.PayloadType == PayloadTRACE {
			continue
		}
		timed = timed[:0]
		for _, obs := range tx.Observations {
			if at, ok := obs.ParsedTime(); ok {
				timed = append(timed, timedObs{at: at.UnixNano(), obs: obs})
			}
		}
		slices.SortFunc(timed, func(a, b timedObs) int { return cmp.Compare(a.at, b.at) })
		for start := 0; start < len(timed); {
			end := start + 1
			for end < len(timed) && timed[end].at-timed[end-1].at <= gap {
				end++
			}
			p.addEvent(timed[start:end])
			start = end
		}
	}

	starts := make([]int64, 0, len(p.aggs))
	for st := range p.aggs {
		starts = append(starts, st)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	buckets := make([]RetransmissionBucket, 0, len(starts))
	for _, st := range starts {
		a := p.aggs[st]
		buckets = append(buckets, RetransmissionBucket{
			Start:        time.Unix(st, 0).UTC().Format(time.RFC3339),
			Packets:      a.packets,
			RepeaterSum:  a.repeaterSum,
			AvgRepeaters: float64(a.repeaterSum) / float64(a.packets),
			Observers:    popCount(a.observers),
		})
	}
	summary := p.summary
	if summary.Packets > 0 {
		summary.AvgRepeaters = float64(p.totalRepeaters) / float64(summary.Packets)
	}
	summary.Observers = popCount(p.allObservers)

	label := window.Label
	if label == "" && !window.IsZero() {
		label = window.Since + "/" + window.Until
	}
	return RetransmissionResponse{
		BucketSeconds: int(p.bucketSec),
		Window:        label,
		Region:        region,
		Summary:       summary,
		Buckets:       buckets,
	}
}

func isDefaultRetransmissionShape(region string, window TimeWindow, bucket time.Duration) bool {
	return region == "" && window.IsZero() && bucket == retransmissionDefaultBucket
}

// retransCacheGet returns a fresh cached result for key. Caller must hold
// s.cacheMu.
func (s *PacketStore) retransCacheGet(key string) (RetransmissionResponse, bool) {
	if e, ok := s.retransCache[key]; ok && time.Now().Before(e.expiresAt) {
		return e.data, true
	}
	return RetransmissionResponse{}, false
}

// GetRetransmissionPressure serves the default shape from the recomputer
// snapshot and every other shape from the TTL cache (compute on miss,
// concurrent misses on one key share the compute).
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
	if r, ok := s.retransCacheGet(key); ok {
		s.cacheHits++
		s.cacheMu.Unlock()
		return r
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	v, _, _ := s.retransSF.Do(key, func() (interface{}, error) {
		// A caller that joins right after a winner stored its result must
		// not start a second pass.
		s.cacheMu.Lock()
		r, ok := s.retransCacheGet(key)
		s.cacheMu.Unlock()
		if ok {
			return r, nil
		}
		result := s.computeRetransmissionPressure(region, window, bucket)
		s.cacheMu.Lock()
		if s.retransCache == nil || len(s.retransCache) >= retransmissionCacheMax {
			s.retransCache = make(map[string]*retransmissionCacheEntry)
		}
		s.retransCache[key] = &retransmissionCacheEntry{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
		s.cacheMu.Unlock()
		return result, nil
	})
	return v.(RetransmissionResponse)
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
