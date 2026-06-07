package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
)

// advertPayloadType mirrors MeshCore ADVERT (0x04). Local const so this file
// stays independent of decoder internals.
const advertPayloadType = 4

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
	d := dirCounts{we: map[string]int{}, they: map[string]int{}, obs: map[string]*obsAgg{}}
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
			} else if r.payloadType == advertPayloadType && r.fromPubkey != "" && r.fromPubkey != ourPK {
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

type QualityNode struct {
	Pubkey    string   `json:"pubkey"`
	Name      string   `json:"name"`
	Role      string   `json:"role"`
	Lat       *float64 `json:"lat"`
	Lon       *float64 `json:"lon"`
	FirstSeen string   `json:"first_seen"`
}
type QualityWindow struct {
	Days  int    `json:"days"`
	Since string `json:"since"`
}
type QualityImportance struct {
	NeighborDegree     int `json:"neighbor_degree"`
	DegreeRank         int `json:"degree_rank"`
	NodesWithEdges     int `json:"nodes_with_edges"`
	RelayObservations  int `json:"relay_observations"`
	BidirectionalLinks int `json:"bidirectional_links"`
	DirectObservers    int `json:"direct_observers"`
}
type QualityObserver struct {
	Pubkey     string   `json:"pubkey"`
	Name       string   `json:"name"`
	Count      int      `json:"count"`
	AvgSNR     *float64 `json:"avg_snr"`
	Lat        *float64 `json:"lat"`
	Lon        *float64 `json:"lon"`
	DistanceKm *float64 `json:"distance_km"`
}
type QualityLink struct {
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
type NodeQualityResponse struct {
	Node            QualityNode       `json:"node"`
	Window          QualityWindow     `json:"window"`
	ReliableTokens  []string          `json:"reliable_tokens"`
	Importance      QualityImportance `json:"importance"`
	DirectObservers []QualityObserver `json:"direct_observers"`
	Links           []QualityLink     `json:"links"`
}

func fptr(v float64) *float64 { return &v }

// gpsPtrs returns (lat,lon) pointers, nil when the node has no GPS (0,0).
func gpsPtrs(info nodeInfo, ok bool) (*float64, *float64) {
	if !ok || !info.HasGPS {
		return nil, nil
	}
	return fptr(info.Lat), fptr(info.Lon)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- bounded TTL cache (perf is gated by the time window; this just avoids
// recompute under dashboard polling). Keyed "pubkey|days". ---
const (
	qualityCacheTTL = 5 * time.Minute
	qualityCacheMax = 256
)

type qualityCacheEntry struct {
	at  time.Time
	raw []byte
}

var (
	qualityCacheMu sync.Mutex
	qualityCache   = map[string]qualityCacheEntry{}
)

func qualityCacheGet(key string) ([]byte, bool) {
	qualityCacheMu.Lock()
	defer qualityCacheMu.Unlock()
	e, ok := qualityCache[key]
	if !ok || time.Since(e.at) > qualityCacheTTL {
		return nil, false
	}
	return e.raw, true
}

func qualityCachePut(key string, raw []byte) {
	qualityCacheMu.Lock()
	defer qualityCacheMu.Unlock()
	if len(qualityCache) >= qualityCacheMax {
		qualityCache = map[string]qualityCacheEntry{} // crude bounded reset
	}
	qualityCache[key] = qualityCacheEntry{at: time.Now(), raw: raw}
}

func (s *Server) handleNodeQuality(w http.ResponseWriter, r *http.Request) {
	pubkey := strings.ToLower(mux.Vars(r)["pubkey"])
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
	if days < 1 {
		days = 1
	}
	if days > 30 {
		days = 30
	}

	cacheKey := pubkey + "|" + strconv.Itoa(days)
	if raw, ok := qualityCacheGet(cacheKey); ok {
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
		return
	}

	resp, ok := s.computeNodeQuality(pubkey, days)
	if !ok {
		writeError(w, 404, "Not found")
		return
	}
	raw, _ := json.Marshal(resp)
	qualityCachePut(cacheKey, raw)
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

// computeNodeQuality does the read-only scan + assembly. ok=false → 404.
func (s *Server) computeNodeQuality(pubkey string, days int) (NodeQualityResponse, bool) {
	if s.store == nil || s.db == nil || s.db.conn == nil {
		return NodeQualityResponse{}, false
	}
	nodeMap := s.buildNodeInfoMap()
	self, found := nodeMap[pubkey]
	if !found {
		return NodeQualityResponse{}, false
	}
	_, pm := s.store.getCachedNodesAndPM()
	tokens := reliableTokens(pubkey, pm)

	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	sinceEpoch := since.Unix()

	var d dirCounts
	if len(tokens) > 0 {
		rows := s.scanQualityRows(tokens, sinceEpoch)
		d = attributeDirections(rows, tokens, pubkey, func(tok string) string {
			return uniqueResolve(pm, tok)
		})
	} else {
		d = dirCounts{we: map[string]int{}, they: map[string]int{}, obs: map[string]*obsAgg{}}
	}

	// importance: neighbor_edges degree + rank (all-time)
	var degree, rank, nodesWithEdges int
	s.db.conn.QueryRow(`
		WITH dd AS (SELECT node_a pk, count c FROM neighbor_edges
		            UNION ALL SELECT node_b, count FROM neighbor_edges),
		     aa AS (SELECT pk, COUNT(*) neigh FROM dd GROUP BY pk)
		SELECT (SELECT COUNT(*) FROM aa),
		       COALESCE((SELECT neigh FROM aa WHERE pk=?),0),
		       (SELECT 1+COUNT(*) FROM aa WHERE neigh > COALESCE((SELECT neigh FROM aa WHERE pk=?),0))
	`, pubkey, pubkey).Scan(&nodesWithEdges, &degree, &rank)

	// node first_seen (nodeInfo only carries last_seen; the contract wants first_seen)
	var firstSeen sql.NullString
	s.db.conn.QueryRow(`SELECT first_seen FROM nodes WHERE LOWER(public_key)=?`, pubkey).Scan(&firstSeen)

	// assemble links
	links := []QualityLink{}
	bidir := 0
	seen := map[string]bool{}
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
		links = append(links, QualityLink{
			Pubkey: pk, Name: info.Name, Role: info.Role, Lat: lat, Lon: lon,
			WeHear: we, TheyHear: they, Bottleneck: minInt(we, they), Bidir: b, DistanceKm: dist,
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
	directObs := []QualityObserver{}
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
		directObs = append(directObs, QualityObserver{
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
	return NodeQualityResponse{
		Node: QualityNode{Pubkey: pubkey, Name: self.Name, Role: self.Role,
			Lat: selfLat, Lon: selfLon, FirstSeen: firstSeen.String},
		Window:         QualityWindow{Days: days, Since: since.Format(time.RFC3339)},
		ReliableTokens: toks,
		Importance: QualityImportance{
			NeighborDegree: degree, DegreeRank: rank, NodesWithEdges: nodesWithEdges,
			RelayObservations: d.relay, BidirectionalLinks: bidir, DirectObservers: len(directObs),
		},
		DirectObservers: directObs,
		Links:           links,
	}, true
}

// scanQualityRows reads windowed observations whose path contains any reliable
// token, with the originator + observer + snr needed for attribution.
func (s *Server) scanQualityRows(tokens map[string]bool, sinceEpoch int64) []pathRow {
	likes := make([]string, 0, len(tokens))
	args := []interface{}{sinceEpoch}
	for tok := range tokens {
		likes = append(likes, "o.path_json LIKE ?")
		args = append(args, "%\""+tok+"\"%")
	}
	q := `SELECT COALESCE(obs.id,''), COALESCE(t.from_pubkey,''), COALESCE(t.payload_type,0), o.path_json, o.snr
	      FROM observations o
	      JOIN transmissions t ON t.id = o.transmission_id
	      LEFT JOIN observers obs ON obs.rowid = o.observer_idx
	      WHERE o.timestamp >= ? AND (` + strings.Join(likes, " OR ") + `)`
	rows, err := s.db.conn.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []pathRow
	for rows.Next() {
		var oid, fpk, pj string
		var pt int
		var snr sql.NullFloat64
		if err := rows.Scan(&oid, &fpk, &pt, &pj, &snr); err != nil {
			continue
		}
		var raw []string
		if json.Unmarshal([]byte(pj), &raw) != nil || len(raw) == 0 {
			continue
		}
		path := make([]string, len(raw))
		for i, h := range raw {
			path[i] = strings.ToUpper(h)
		}
		pr := pathRow{observerPK: strings.ToLower(oid), fromPubkey: strings.ToLower(fpk),
			payloadType: pt, path: path}
		if snr.Valid {
			v := snr.Float64
			pr.snr = &v
		}
		out = append(out, pr)
	}
	return out
}
