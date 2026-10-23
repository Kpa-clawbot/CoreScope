package main

import (
	"database/sql"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Repeaters known only from client_node_names: a mobile companion asked them
// for their own name (ANON_REQ_TYPE_OWNER) because no advert had ever named
// them, so nodes has no row and no position for them. /api/nodes/reported
// lists every such repeater so the UI can find it, and adds an estimated
// position when the receptions allow one.
//
// The estimate is a SNR-weighted centroid of where companions stood when they
// heard the repeater directly. It is not trilateration: a repeater only ever
// heard from one side lands on that side, which is why the radius is never
// smaller than estimateMinRadiusM and why the UI must present it as an
// estimate, never as a node position.

const (
	// estimateSpotM merges receptions closer than this into one spot, so a
	// companion parked in one place counts once, however long it stays.
	estimateSpotM = 100.0
	// estimateMinSpots and estimateMinSpreadM: below this there is no
	// estimate, because the centroid of one place is that place, which says
	// where the companion was, not where the repeater is.
	estimateMinSpots   = 3
	estimateMinSpreadM = 200.0
	// estimateMinRadiusM is the smallest uncertainty radius reported.
	estimateMinRadiusM = 500.0
	// estimateNoSNRdB stands in for a reception without an SNR value.
	estimateNoSNRdB = -20.0
	// reportedNodesTTL bounds how often the list is recomputed.
	reportedNodesTTL = 5 * time.Minute
	// reportedNodesMax caps the list, so the IN query stays bounded.
	reportedNodesMax = 500
)

// PositionEstimate is an estimated position with its uncertainty.
type PositionEstimate struct {
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	RadiusM float64 `json:"radius_m"`
}

// ReportedNode is one repeater known only from its own reported name.
type ReportedNode struct {
	Pubkey         string            `json:"pubkey"`
	Name           string            `json:"name"`
	NameReportedAt string            `json:"name_reported_at"`
	Reporters      int               `json:"reporters"`
	Receptions     int               `json:"receptions"`
	Spots          int               `json:"spots"`
	FirstHeard     string            `json:"first_heard,omitempty"`
	LastHeard      string            `json:"last_heard,omitempty"`
	Estimate       *PositionEstimate `json:"estimate,omitempty"`
}

// ReportedNodesResponse is the /api/nodes/reported reply. Method carries the
// thresholds the estimate used, so the UI can explain a missing estimate.
type ReportedNodesResponse struct {
	Nodes  []ReportedNode `json:"nodes"`
	Method struct {
		SpotM      float64 `json:"spot_m"`
		MinSpots   int     `json:"min_spots"`
		MinSpreadM float64 `json:"min_spread_m"`
		MinRadiusM float64 `json:"min_radius_m"`
	} `json:"method"`
}

// rxPoint is one direct reception: where the companion stood, and how well.
type rxPoint struct {
	Lat, Lon float64
	SNR      *float64
}

// distanceM is the great-circle distance in metres.
func distanceM(lat1, lon1, lat2, lon2 float64) float64 {
	return haversineKm(lat1, lon1, lat2, lon2) * 1000
}

// spotsOf merges receptions within estimateSpotM of a spot's first reception
// into that spot, keeping the best SNR. Greedy in input order, which is fine
// at 100 m: the merge only exists to stop one parked companion counting many
// times.
func spotsOf(pts []rxPoint) []rxPoint {
	var spots []rxPoint
	for _, p := range pts {
		merged := false
		for i := range spots {
			if distanceM(spots[i].Lat, spots[i].Lon, p.Lat, p.Lon) < estimateSpotM {
				if p.SNR != nil && (spots[i].SNR == nil || *p.SNR > *spots[i].SNR) {
					spots[i].SNR = p.SNR
				}
				merged = true
				break
			}
		}
		if !merged {
			spots = append(spots, p)
		}
	}
	return spots
}

// estimatePosition returns the estimate and the spot count. The estimate is
// nil below estimateMinSpots spots or when no two spots are
// estimateMinSpreadM apart.
func estimatePosition(pts []rxPoint) (*PositionEstimate, int) {
	spots := spotsOf(pts)
	if len(spots) < estimateMinSpots {
		return nil, len(spots)
	}
	spread := 0.0
	for i := range spots {
		for j := i + 1; j < len(spots); j++ {
			spread = math.Max(spread, distanceM(spots[i].Lat, spots[i].Lon, spots[j].Lat, spots[j].Lon))
		}
	}
	if spread < estimateMinSpreadM {
		return nil, len(spots)
	}
	// Linear amplitude weight: 6 dB better counts twice as much.
	var sw, slat, slon float64
	for _, s := range spots {
		snr := estimateNoSNRdB
		if s.SNR != nil {
			snr = *s.SNR
		}
		w := math.Pow(10, snr/20)
		sw += w
		slat += w * s.Lat
		slon += w * s.Lon
	}
	est := &PositionEstimate{Lat: slat / sw, Lon: slon / sw, RadiusM: estimateMinRadiusM}
	for _, s := range spots {
		est.RadiusM = math.Max(est.RadiusM, distanceM(est.Lat, est.Lon, s.Lat, s.Lon))
	}
	est.Lat = math.Round(est.Lat*1e5) / 1e5
	est.Lon = math.Round(est.Lon*1e5) / 1e5
	est.RadiusM = math.Round(est.RadiusM)
	return est, len(spots)
}

type reportedNodesCache struct {
	mu   sync.Mutex
	at   time.Time
	resp *ReportedNodesResponse
}

// handleReportedNodes is gated like the other endpoints built on
// client_receptions: the counts and the estimate come from companion GPS fixes.
func (s *Server) handleReportedNodes(w http.ResponseWriter, r *http.Request) {
	if !s.requireClientRxCoverage(w, r) {
		return
	}
	if s.db == nil || s.db.conn == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	resp, err := s.reportedNodesResponse()
	if err != nil {
		log.Printf("[reported-nodes] compute failed: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, resp)
}

// reportedNodesResponse returns the cached list, recomputing it when stale.
// The mutex makes concurrent misses wait for one computation; the response is
// written after it is released, so a slow client never holds it.
func (s *Server) reportedNodesResponse() (*ReportedNodesResponse, error) {
	c := &s.reportedNodes
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resp == nil || time.Since(c.at) > reportedNodesTTL {
		start := time.Now()
		resp, err := s.computeReportedNodes()
		if err != nil {
			return nil, err
		}
		log.Printf("[reported-nodes] %d repeaters, %d with an estimate, computed in %s", len(resp.Nodes), countEstimates(resp.Nodes), time.Since(start).Round(time.Millisecond))
		c.resp, c.at = resp, time.Now()
	}
	return c.resp, nil
}

func countEstimates(nodes []ReportedNode) int {
	n := 0
	for _, nd := range nodes {
		if nd.Estimate != nil {
			n++
		}
	}
	return n
}

// tablePresent reports whether a table exists. Used for inactive_nodes and
// client_receptions, which an older or partial database may lack.
func (db *DB) tablePresent(name string) bool {
	var n int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return err == nil && n > 0
}

// computeReportedNodes builds the list: every client_node_names target that
// never sent an advert (no row in nodes or inactive_nodes) and was heard
// directly at least once (clientNameHeard), with its direct receptions. A node
// with an advert, with or without a position, is in /api/nodes under its
// advert name, so listing it here as well would show it twice.
func (s *Server) computeReportedNodes() (*ReportedNodesResponse, error) {
	resp := &ReportedNodesResponse{Nodes: []ReportedNode{}}
	resp.Method.SpotM = estimateSpotM
	resp.Method.MinSpots = estimateMinSpots
	resp.Method.MinSpreadM = estimateMinSpreadM
	resp.Method.MinRadiusM = estimateMinRadiusM
	if !s.db.clientNamesUsable() {
		return resp, nil
	}

	advertised := `EXISTS (SELECT 1 FROM nodes n WHERE n.public_key = c.target)`
	if s.db.inactiveNodesPresent() {
		advertised += ` OR EXISTS (SELECT 1 FROM inactive_nodes n WHERE n.public_key = c.target)`
	}
	// The bare name beside MAX(observed_at) is SQLite's bare-column rule: the
	// name of the newest observation (same as lookupPrefixes).
	rows, err := s.db.conn.Query(`
		SELECT target, name, MAX(observed_at), COUNT(DISTINCT rx_pubkey)
		FROM client_node_names c
		WHERE NOT (`+advertised+`) AND `+s.db.clientNameHeard()+`
		GROUP BY target
		ORDER BY MAX(observed_at) DESC
		LIMIT ?`, reportedNodesMax)
	if err != nil {
		return nil, err
	}
	byPK := map[string]*ReportedNode{}
	var order []string
	for rows.Next() {
		var n ReportedNode
		if err := rows.Scan(&n.Pubkey, &n.Name, &n.NameReportedAt, &n.Reporters); err != nil {
			rows.Close()
			return nil, err
		}
		if s.cfg.IsBlacklisted(n.Pubkey) || s.cfg.IsNameHidden(n.Name) {
			continue
		}
		nn := n
		byPK[n.Pubkey] = &nn
		order = append(order, n.Pubkey)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(order) > 0 {
		pts, err := s.directReceptions(order)
		if err != nil {
			return nil, err
		}
		for _, pk := range order {
			n := byPK[pk]
			rx := pts[pk]
			n.Receptions = len(rx.points)
			n.FirstHeard, n.LastHeard = rx.first, rx.last
			n.Estimate, n.Spots = estimatePosition(rx.points)
		}
	}
	for _, pk := range order {
		resp.Nodes = append(resp.Nodes, *byPK[pk])
	}
	return resp, nil
}

type targetReceptions struct {
	points      []rxPoint
	first, last string
}

// directReceptions collects, per target, the receptions that certainly heard
// that repeater directly:
//   - discover/advert rows keyed by the full pubkey, which carry the
//     responder's own identity, or by its 8-byte prefix when no other known
//     pubkey starts with it (sharedEightBytePrefixes)
//   - rxlog rows keyed by a 2-3 byte path hash, only when that prefix resolves
//     uniquely to this target (lookupPrefixes); otherwise another node with
//     the same hash could be the one heard
//
// src='geo' rows are left out: their 1-byte hop was placed geographically,
// not heard as this node.
func (s *Server) directReceptions(targets []string) (map[string]*targetReceptions, error) {
	keyTarget := map[string]string{}
	var short []string
	for _, pk := range targets {
		keyTarget[pk] = pk
		for _, n := range []int{4, 6} {
			if len(pk) >= n {
				short = append(short, pk[:n])
			}
		}
	}
	shared := s.sharedEightBytePrefixes(targets)
	for _, pk := range targets {
		if len(pk) >= 16 && !shared[pk[:16]] {
			keyTarget[pk[:16]] = pk
		}
	}
	wanted := make(map[string]bool, len(targets))
	for _, pk := range targets {
		wanted[pk] = true
	}
	for k, h := range s.lookupPrefixes(short) {
		if s.visible(h) && wanted[h.pubkey] {
			keyTarget[k] = h.pubkey
		}
	}

	out := make(map[string]*targetReceptions, len(targets))
	for _, pk := range targets {
		out[pk] = &targetReceptions{}
	}
	keys := make([]string, 0, len(keyTarget))
	for k := range keyTarget {
		keys = append(keys, k)
	}
	const chunk = 400
	for i := 0; i < len(keys); i += chunk {
		end := i + chunk
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[i:end]
		args := make([]interface{}, len(batch))
		for j, k := range batch {
			args[j] = k
		}
		rows, err := s.db.conn.Query(`
			SELECT heard_key, src, lat, lon, snr, rx_at FROM client_receptions
			WHERE heard_key IN (`+sqlPlaceholders(len(batch))+`) AND src IN ('discover','advert','rxlog')`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var hk, src, at string
			var lat, lon float64
			var snr sql.NullFloat64
			if err := rows.Scan(&hk, &src, &lat, &lon, &snr, &at); err != nil {
				rows.Close()
				return nil, err
			}
			hk = strings.ToLower(hk)
			// A short key only counts as an rxlog path hash; a discover or
			// advert row always carries 8 or 32 bytes.
			if len(hk) < 16 && src != "rxlog" {
				continue
			}
			pk, ok := keyTarget[hk]
			if !ok {
				continue
			}
			t := out[pk]
			p := rxPoint{Lat: lat, Lon: lon}
			if snr.Valid {
				v := snr.Float64
				p.SNR = &v
			}
			t.points = append(t.points, p)
			if t.first == "" || at < t.first {
				t.first = at
			}
			if at > t.last {
				t.last = at
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// sharedEightBytePrefixes returns the 8-byte (16 hex) prefixes of targets that
// another known pubkey (nodes, inactive_nodes, client_node_names) also starts
// with. An 8-byte reception under such a prefix could be either node, so
// directReceptions does not credit it, the same rule as clientNameHeard. A
// failed chunk marks its prefixes shared: crediting nothing is the safe side.
func (s *Server) sharedEightBytePrefixes(targets []string) map[string]bool {
	byPrefix := map[string]string{}
	for _, pk := range targets {
		if len(pk) >= 16 {
			byPrefix[pk[:16]] = pk
		}
	}
	prefixes := make([]string, 0, len(byPrefix))
	for p := range byPrefix {
		prefixes = append(prefixes, p)
	}
	tables := [][2]string{{"nodes", "public_key"}, {"client_node_names", "target"}}
	if s.db.inactiveNodesPresent() {
		tables = append(tables, [2]string{"inactive_nodes", "public_key"})
	}
	shared := map[string]bool{}
	// Lowercase hex keys: [p, p+"g") is exactly the keys starting with p (see
	// clientNameHeard). Each prefix costs one LIMIT 1 term per table, so a chunk
	// of 150 stays within SQLite's 500-term compound limit.
	const chunk = 150
	for i := 0; i < len(prefixes); i += chunk {
		end := i + chunk
		if end > len(prefixes) {
			end = len(prefixes)
		}
		var parts []string
		var args []interface{}
		for _, p := range prefixes[i:end] {
			for _, tc := range tables {
				parts = append(parts, `SELECT * FROM (SELECT ? FROM `+tc[0]+` WHERE `+tc[1]+` >= ? AND `+tc[1]+` < ? AND `+tc[1]+` <> ? LIMIT 1)`)
				args = append(args, p, p, p+"g", byPrefix[p])
			}
		}
		rows, err := s.db.conn.Query(strings.Join(parts, " UNION ALL "), args...)
		if err != nil {
			log.Printf("WARN sharedEightBytePrefixes: %v", err)
			for _, p := range prefixes[i:end] {
				shared[p] = true
			}
			continue
		}
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil {
				shared[p] = true
			}
		}
		rows.Close()
	}
	return shared
}
