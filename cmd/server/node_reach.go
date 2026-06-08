package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/sync/singleflight"
)

// reachScanRowLimit hard-caps the windowed observation scan so a hot relay node
// with weeks of traffic can't pull an unbounded result set into memory. A node
// with >200k matching observations in the window is far past dashboard scale;
// beyond the cap the counts are a (still representative) truncation. The LIKE
// filter is unavoidably a text scan of path_json over the timestamp-narrowed
// window — an indexed path-token column would need an ingestor-side schema
// migration (the server is read-only by invariant), so it's a follow-up.
const reachScanRowLimit = 200000

// pathRow is one observation fed to attributeDirections. path tokens are
// uppercase hex hop prefixes (as stored in observations.path_json).
type pathRow struct {
	observerPK  string // lowercase pubkey of the observer (may be "")
	fromPubkey  string // lowercase originator pubkey (may be "")
	payloadType int
	path        []string
	snr         *float64
}

type obsAgg struct {
	count  int
	snrSum float64
	snrN   int
}

type dirCounts struct {
	we    map[string]int
	they  map[string]int
	obs   map[string]*obsAgg
	relay int
}

// attributeDirections walks each path and attributes directional evidence for
// the target node (identified by any token in ourTokens). resolve maps a hop
// token → a unique relay pubkey ("" when ambiguous/unknown → skipped). ourPK is
// the target's own pubkey (lowercase) so self-edges are ignored.
func attributeDirections(rows []pathRow, ourTokens map[string]bool, ourPK string, resolve func(string) string) dirCounts {
	// Size hints: neighbour fan-out is a fraction of the row count; a /8 hint
	// avoids most map-growth rehashing without over-allocating for small scans.
	hint := len(rows)/8 + 1
	d := dirCounts{
		we:   make(map[string]int, hint),
		they: make(map[string]int, hint),
		obs:  make(map[string]*obsAgg, hint),
	}
	for _, r := range rows {
		n := len(r.path)
		if n == 0 {
			continue
		}
		hit := false
		for i, tok := range r.path {
			if !ourTokens[tok] {
				continue
			}
			hit = true
			// predecessor → we heard it
			if i > 0 {
				if pk := resolve(r.path[i-1]); pk != "" && pk != ourPK {
					d.we[pk]++
				}
			} else if r.payloadType == PayloadADVERT && r.fromPubkey != "" && r.fromPubkey != ourPK {
				d.we[r.fromPubkey]++
			}
			// successor → it heard us; or if we're the last hop, the observer did
			if i < n-1 {
				if pk := resolve(r.path[i+1]); pk != "" && pk != ourPK {
					d.they[pk]++
				}
			} else if r.observerPK != "" && r.observerPK != ourPK {
				d.they[r.observerPK]++
				a := d.obs[r.observerPK]
				if a == nil {
					a = &obsAgg{}
					d.obs[r.observerPK] = a
				}
				a.count++
				if r.snr != nil {
					a.snrSum += *r.snr
					a.snrN++
				}
			}
		}
		if hit {
			d.relay++
		}
	}
	return d
}

// reliableTokens returns the uppercase hex prefixes (1, 2, 3 byte) of pubkey
// that are UNIQUE among relay-capable nodes in pm. 1-byte prefixes almost always
// collide and are excluded; only unique prefixes can identify a node in a path.
func reliableTokens(pubkey string, pm *prefixMap) map[string]bool {
	out := map[string]bool{}
	lpk := strings.ToLower(pubkey)
	for _, l := range []int{2, 4, 6} { // hex chars = 1,2,3 bytes
		if len(lpk) < l {
			continue
		}
		p := lpk[:l]
		if pm != nil && len(pm.m[p]) == 1 {
			out[strings.ToUpper(p)] = true
		}
	}
	return out
}

// uniqueResolve returns the single relay pubkey (lowercase) for a hop token, or
// "" when the token resolves to zero or multiple candidates (conservative).
// Callers should memoize across a request (see newResolver) so the per-hop
// ToLower + map lookup runs once per distinct token, not once per row.
func uniqueResolve(pm *prefixMap, token string) string {
	if pm == nil {
		return ""
	}
	cands := pm.m[strings.ToLower(token)]
	if len(cands) == 1 {
		return strings.ToLower(cands[0].PublicKey)
	}
	return ""
}

// parsePathTokens extracts the quoted hex hop tokens from a path_json array
// (e.g. `["AA","01FA","BB"]`) in a single pass, uppercased. Avoids the
// json.Unmarshal reflection + per-row interface allocations on the hot scan
// path. Tokens slice into pj (no copy) except where ToUpper must rewrite a
// lowercase hop; path_json holds only hex strings, so there are no escapes to
// worry about. Returns nil for an empty/degenerate array.
func parsePathTokens(pj string) []string {
	out := make([]string, 0, 8) // paths are short (a handful of hops)
	i := 0
	for {
		q1 := strings.IndexByte(pj[i:], '"')
		if q1 < 0 {
			break
		}
		q1 += i
		rel := strings.IndexByte(pj[q1+1:], '"')
		if rel < 0 {
			break
		}
		q2 := q1 + 1 + rel
		out = append(out, strings.ToUpper(pj[q1+1:q2]))
		i = q2 + 1
	}
	return out
}

// newResolver returns a memoized hop-token → pubkey resolver. Paths reuse the
// same hop tokens across thousands of rows, so caching collapses the repeated
// ToLower + prefix-map lookups to once per distinct token.
func newResolver(pm *prefixMap) func(string) string {
	cache := make(map[string]string)
	return func(tok string) string {
		if pk, ok := cache[tok]; ok {
			return pk
		}
		pk := uniqueResolve(pm, tok)
		cache[tok] = pk
		return pk
	}
}

type NodeReachInfo struct {
	Pubkey    string   `json:"pubkey"`
	Name      string   `json:"name"`
	Role      string   `json:"role"`
	Lat       *float64 `json:"lat"`
	Lon       *float64 `json:"lon"`
	FirstSeen string   `json:"first_seen"`
}
type NodeReachWindow struct {
	Days  int    `json:"days"`
	Since string `json:"since"`
}
type NodeReachImportance struct {
	NeighborDegree     int `json:"neighbor_degree"`
	DegreeRank         int `json:"degree_rank"`
	NodesWithEdges     int `json:"nodes_with_edges"`
	RelayObservations  int `json:"relay_observations"`
	BidirectionalLinks int `json:"bidirectional_links"`
	DirectObservers    int `json:"direct_observers"`
}
type NodeReachObserver struct {
	Pubkey     string   `json:"pubkey"`
	Name       string   `json:"name"`
	Count      int      `json:"count"`
	AvgSNR     *float64 `json:"avg_snr"`
	Lat        *float64 `json:"lat"`
	Lon        *float64 `json:"lon"`
	DistanceKm *float64 `json:"distance_km"`
}
type NodeReachLink struct {
	Pubkey     string   `json:"pubkey"`
	Name       string   `json:"name"`
	Role       string   `json:"role"`
	Lat        *float64 `json:"lat"`
	Lon        *float64 `json:"lon"`
	WeHear     int      `json:"we_hear"`
	TheyHear   int      `json:"they_hear"`
	Bottleneck int      `json:"bottleneck"`
	Bidir      bool     `json:"bidir"`
	DistanceKm *float64 `json:"distance_km"`
}
type NodeReachResponse struct {
	Node            NodeReachInfo       `json:"node"`
	Window          NodeReachWindow     `json:"window"`
	ReliableTokens  []string            `json:"reliable_tokens"`
	Importance      NodeReachImportance `json:"importance"`
	DirectObservers []NodeReachObserver `json:"direct_observers"`
	Links           []NodeReachLink     `json:"links"`
}

func fptr(v float64) *float64 { return &v }

// gpsPtrs returns (lat,lon) pointers, nil when the node has no GPS (0,0).
func gpsPtrs(info nodeInfo, ok bool) (*float64, *float64) {
	if !ok || !info.HasGPS {
		return nil, nil
	}
	return fptr(info.Lat), fptr(info.Lon)
}

// clampDays bounds the lookback window to [1,30]; default callers pass 7.
func clampDays(d int) int {
	if d < 1 {
		return 1
	}
	if d > 30 {
		return 30
	}
	return d
}

// --- bounded TTL cache. perf is gated by the time window; this just avoids
// recompute under dashboard polling. Keyed "pubkey|days". ---
//
// reachCacheMax bounds entry count; at ~2KB of marshalled JSON per entry the
// worst case is well under 1MB, so an entry cap (rather than a byte budget)
// keeps the bookkeeping trivial while staying memory-safe.
const (
	reachCacheTTL = 5 * time.Minute
	reachCacheMax = 256
)

type reachCacheEntry struct {
	at  time.Time
	raw []byte
}

var (
	reachCacheMu sync.RWMutex
	reachCache   = map[string]reachCacheEntry{}
	// reachSF dedups concurrent cold-cache requests for the same key so N
	// simultaneous callers run the scan + attribution once, not N times.
	reachSF singleflight.Group
)

// reachCacheGet returns the cached marshalled JSON for key. The returned slice
// is shared (not copied): it is treated as immutable — only ever handed to
// w.Write — so callers MUST NOT mutate it.
func reachCacheGet(key string) ([]byte, bool) {
	reachCacheMu.RLock()
	defer reachCacheMu.RUnlock()
	e, ok := reachCache[key]
	if !ok || time.Since(e.at) > reachCacheTTL {
		return nil, false
	}
	return e.raw, true
}

// isHexPubkey reports whether s is a full 64-char lowercase-hex public key.
// The handler lowercases input first, so we only accept [0-9a-f].
func isHexPubkey(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func reachCachePut(key string, raw []byte) {
	reachCacheMu.Lock()
	defer reachCacheMu.Unlock()
	if _, exists := reachCache[key]; !exists && len(reachCache) >= reachCacheMax {
		evictReachLocked()
	}
	reachCache[key] = reachCacheEntry{at: time.Now(), raw: raw}
}

// evictReachLocked drops expired entries first; if still at the cap it evicts
// the single oldest entry. Avoids the full-map wipe that thrashed every cached
// key once the cap was reached. Caller holds reachCacheMu (write).
func evictReachLocked() {
	now := time.Now()
	for k, e := range reachCache {
		if now.Sub(e.at) > reachCacheTTL {
			delete(reachCache, k)
		}
	}
	if len(reachCache) < reachCacheMax {
		return
	}
	var oldestKey string
	var oldestAt time.Time
	first := true
	for k, e := range reachCache {
		if first || e.at.Before(oldestAt) {
			oldestKey, oldestAt, first = k, e.at, false
		}
	}
	if !first {
		delete(reachCache, oldestKey)
	}
}

func (s *Server) handleNodeReach(w http.ResponseWriter, r *http.Request) {
	pubkey := strings.ToLower(mux.Vars(r)["pubkey"])
	// Reject malformed pubkeys up front (cheap defense against cache-key
	// pollution + wasted work on bogus IDs).
	if !isHexPubkey(pubkey) {
		writeError(w, 400, "invalid pubkey: expected 64 hex chars")
		return
	}
	if s.cfg != nil && s.cfg.IsBlacklisted(pubkey) {
		writeError(w, 404, "Not found")
		return
	}
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			days = n
		}
	}
	days = clampDays(days)

	cacheKey := pubkey + "|" + strconv.Itoa(days)
	if raw, ok := reachCacheGet(cacheKey); ok {
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
		return
	}

	// singleflight: collapse a thundering herd on a cold key to one scan. The
	// shared computation uses the triggering request's context; a disconnect
	// there can cancel the in-flight scan for all waiters (acceptable — the
	// next request recomputes).
	v, err, _ := reachSF.Do(cacheKey, func() (interface{}, error) {
		if raw, ok := reachCacheGet(cacheKey); ok {
			return raw, nil
		}
		resp, ok := s.computeNodeReach(r.Context(), pubkey, days)
		if !ok {
			return []byte(nil), nil
		}
		raw, mErr := json.Marshal(resp)
		if mErr != nil {
			log.Printf("[reach] marshal failed for %s: %v", cacheKey, mErr)
			return nil, mErr
		}
		reachCachePut(cacheKey, raw)
		return raw, nil
	})
	if err != nil {
		writeError(w, 500, "reach computation failed")
		return
	}
	raw, _ := v.([]byte)
	if len(raw) == 0 {
		writeError(w, 404, "Not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

// computeNodeReach does the read-only scan + assembly. ok=false → 404.
func (s *Server) computeNodeReach(ctx context.Context, pubkey string, days int) (NodeReachResponse, bool) {
	if s.store == nil || s.db == nil || s.db.conn == nil {
		return NodeReachResponse{}, false
	}
	nodeMap := s.buildNodeInfoMap()
	self, found := nodeMap[pubkey]
	if !found {
		return NodeReachResponse{}, false
	}
	_, pm := s.store.getCachedNodesAndPM()
	tokens := reliableTokens(pubkey, pm)

	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	sinceEpoch := since.Unix()

	var d dirCounts
	if len(tokens) > 0 {
		rows := s.scanReachRows(ctx, tokens, sinceEpoch)
		d = attributeDirections(rows, tokens, pubkey, newResolver(pm))
	} else {
		d = dirCounts{we: map[string]int{}, they: map[string]int{}, obs: map[string]*obsAgg{}}
	}

	// importance: neighbor_edges degree + rank (all-time). Served from a
	// coarse-TTL snapshot so the full UNION+GROUP-BY aggregate runs at most
	// once per snapshotTTL, not on every cache miss.
	degree, rank, nodesWithEdges := s.reachDegreeRank(ctx, pubkey)

	// node first_seen (nodeInfo only carries last_seen; the contract wants it).
	// pubkey is already lowercased + validated, so match the column directly to
	// keep the public_key index usable. A missing row → empty first_seen (the
	// node may be observer-only); only log unexpected errors.
	var firstSeen sql.NullString
	if err := s.db.conn.QueryRowContext(ctx, `SELECT first_seen FROM nodes WHERE public_key=?`, pubkey).Scan(&firstSeen); err != nil && err != sql.ErrNoRows {
		log.Printf("[reach] first_seen query failed for %s: %v", pubkey, err)
	}

	// assemble links
	links := make([]NodeReachLink, 0, len(d.we)+len(d.they))
	bidir := 0
	seen := make(map[string]bool, len(d.we)+len(d.they))
	for pk := range d.we {
		seen[pk] = true
	}
	for pk := range d.they {
		seen[pk] = true
	}
	for pk := range seen {
		we, they := d.we[pk], d.they[pk]
		info := nodeMap[pk]
		lat, lon := gpsPtrs(info, true)
		var dist *float64
		if self.HasGPS && info.HasGPS {
			dist = fptr(haversineKm(self.Lat, self.Lon, info.Lat, info.Lon))
		}
		b := we > 0 && they > 0
		if b {
			bidir++
		}
		links = append(links, NodeReachLink{
			Pubkey: pk, Name: info.Name, Role: info.Role, Lat: lat, Lon: lon,
			WeHear: we, TheyHear: they, Bottleneck: min(we, they), Bidir: b, DistanceKm: dist,
		})
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].Bidir != links[j].Bidir {
			return links[i].Bidir
		}
		if links[i].Bottleneck != links[j].Bottleneck {
			return links[i].Bottleneck > links[j].Bottleneck
		}
		return links[i].WeHear+links[i].TheyHear > links[j].WeHear+links[j].TheyHear
	})

	// direct observers
	directObs := make([]NodeReachObserver, 0, len(d.obs))
	for pk, a := range d.obs {
		info := nodeMap[pk]
		lat, lon := gpsPtrs(info, true)
		var avg, dist *float64
		if a.snrN > 0 {
			avg = fptr(a.snrSum / float64(a.snrN))
		}
		if self.HasGPS && info.HasGPS {
			dist = fptr(haversineKm(self.Lat, self.Lon, info.Lat, info.Lon))
		}
		directObs = append(directObs, NodeReachObserver{
			Pubkey: pk, Name: info.Name, Count: a.count, AvgSNR: avg, Lat: lat, Lon: lon, DistanceKm: dist,
		})
	}
	sort.Slice(directObs, func(i, j int) bool { return directObs[i].Count > directObs[j].Count })

	toks := make([]string, 0, len(tokens))
	for t := range tokens {
		toks = append(toks, t)
	}
	sort.Strings(toks)

	selfLat, selfLon := gpsPtrs(self, true)
	return NodeReachResponse{
		Node: NodeReachInfo{Pubkey: pubkey, Name: self.Name, Role: self.Role,
			Lat: selfLat, Lon: selfLon, FirstSeen: firstSeen.String},
		Window:         NodeReachWindow{Days: days, Since: since.Format(time.RFC3339)},
		ReliableTokens: toks,
		Importance: NodeReachImportance{
			NeighborDegree: degree, DegreeRank: rank, NodesWithEdges: nodesWithEdges,
			RelayObservations: d.relay, BidirectionalLinks: bidir, DirectObservers: len(directObs),
		},
		DirectObservers: directObs,
		Links:           links,
	}, true
}

// --- neighbor-degree snapshot ---------------------------------------------
// The degree/rank importance is identical across all reach requests except the
// pubkey match, so the full neighbor_edges aggregate is computed once and shared
// behind a coarse TTL. Rank is a binary search over the descending degree list.
const reachDegreeTTL = 60 * time.Second

type degreeSnapshot struct {
	at         time.Time
	total      int            // nodes that have any edge
	deg        map[string]int // lowercase pubkey → neighbour count
	sortedDesc []int          // degrees sorted descending, for rank
}

var (
	reachDegreeMu   sync.Mutex
	reachDegreeSnap *degreeSnapshot
)

func (s *Server) reachDegreeRank(ctx context.Context, pubkey string) (degree, rank, total int) {
	snap := s.getDegreeSnapshot(ctx)
	if snap == nil {
		return 0, 0, 0
	}
	degree = snap.deg[pubkey]
	// rank = 1 + (number of nodes with strictly higher degree). sortedDesc is
	// descending, so the count of entries > degree is the first index whose
	// value is <= degree.
	rank = 1 + sort.Search(len(snap.sortedDesc), func(i int) bool { return snap.sortedDesc[i] <= degree })
	return degree, rank, snap.total
}

func (s *Server) getDegreeSnapshot(ctx context.Context) *degreeSnapshot {
	reachDegreeMu.Lock()
	defer reachDegreeMu.Unlock()
	if reachDegreeSnap != nil && time.Since(reachDegreeSnap.at) < reachDegreeTTL {
		return reachDegreeSnap
	}
	rows, err := s.db.conn.QueryContext(ctx, `
		SELECT pk, COUNT(*) neigh FROM (
			SELECT node_a pk FROM neighbor_edges
			UNION ALL SELECT node_b FROM neighbor_edges
		) GROUP BY pk`)
	if err != nil {
		log.Printf("[reach] degree snapshot query failed: %v (serving stale)", err)
		return reachDegreeSnap // serve stale on error rather than zeroing
	}
	defer rows.Close()
	deg := make(map[string]int)
	var sortedDesc []int
	for rows.Next() {
		var pk string
		var neigh int
		if rows.Scan(&pk, &neigh) != nil {
			continue
		}
		deg[strings.ToLower(pk)] = neigh
		sortedDesc = append(sortedDesc, neigh)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sortedDesc)))
	reachDegreeSnap = &degreeSnapshot{at: time.Now(), total: len(deg), deg: deg, sortedDesc: sortedDesc}
	return reachDegreeSnap
}

// scanReachRows reads windowed observations whose path contains any reliable
// token, with the originator + observer + snr needed for attribution. Observer
// id and originator pubkey are lowercased in SQL (not per row), the path slice
// is uppercased in place (no second allocation), and the result is hard-capped
// at reachScanRowLimit.
func (s *Server) scanReachRows(ctx context.Context, tokens map[string]bool, sinceEpoch int64) []pathRow {
	likes := make([]string, 0, len(tokens))
	args := []interface{}{sinceEpoch}
	for tok := range tokens {
		likes = append(likes, "o.path_json LIKE ?")
		args = append(args, "%\""+tok+"\"%")
	}
	q := `SELECT LOWER(COALESCE(obs.id,'')), LOWER(COALESCE(t.from_pubkey,'')), COALESCE(t.payload_type,0), o.path_json, o.snr
	      FROM observations o
	      JOIN transmissions t ON t.id = o.transmission_id
	      LEFT JOIN observers obs ON obs.rowid = o.observer_idx
	      WHERE o.timestamp >= ? AND (` + strings.Join(likes, " OR ") + `)
	      LIMIT ?`
	args = append(args, reachScanRowLimit)
	rows, err := s.db.conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	// Modest preallocation: most nodes return far fewer than the cap, so seed a
	// reasonable capacity rather than reserving reachScanRowLimit up front.
	out := make([]pathRow, 0, 2048)
	var skipped int // malformed/empty rows discarded — surfaced below so ingest bugs aren't silent
	for rows.Next() {
		var oid, fpk, pj string
		var pt int
		var snr sql.NullFloat64
		if err := rows.Scan(&oid, &fpk, &pt, &pj, &snr); err != nil {
			skipped++
			continue
		}
		path := parsePathTokens(pj)
		if len(path) == 0 {
			skipped++
			continue
		}
		pr := pathRow{observerPK: oid, fromPubkey: fpk, payloadType: pt, path: path}
		if snr.Valid {
			v := snr.Float64
			pr.snr = &v
		}
		out = append(out, pr)
	}
	if skipped > 0 {
		log.Printf("[reach] scan discarded %d malformed/empty rows (kept %d)", skipped, len(out))
	}
	return out
}
