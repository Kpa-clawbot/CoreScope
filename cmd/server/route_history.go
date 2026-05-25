package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type routeHistoryEdge struct {
	NodeA    string   `json:"node_a"`
	NodeB    string   `json:"node_b"`
	NameA    string   `json:"name_a"`
	NameB    string   `json:"name_b"`
	LatA     float64  `json:"lat_a"`
	LonA     float64  `json:"lon_a"`
	LatB     float64  `json:"lat_b"`
	LonB     float64  `json:"lon_b"`
	Count    int      `json:"count"`
	LastSeen string   `json:"last_seen"`
	Samples  []string `json:"samples"`
}

type routeHistoryResponse struct {
	Edges           []routeHistoryEdge `json:"edges"`
	Hours           int                `json:"hours"`
	TotalEdges      int                `json:"total_edges"`
	CandidateEdges  int                `json:"candidate_edges"`
	MissingGPSEdges int                `json:"missing_gps_edges"`
	MappedEdges     int                `json:"mapped_edges"`
	RawEdges        int                `json:"raw_edges"`
	UnmappedEdges   int                `json:"unmapped_edges"`
}

type rhEdgeKey struct{ a, b string }
type rhEdgeData struct {
	count    int
	lastSeen string
	samples  []string
}

type routeHistoryCacheState struct {
	mu      sync.Mutex
	entries map[int]routeHistoryCacheEntry
}

type routeHistoryCacheEntry struct {
	payload   []byte
	expiresAt time.Time
}

const routeHistoryCacheTTL = 15 * time.Second

func (c *routeHistoryCacheState) get(hours int) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		return nil
	}
	entry, ok := c.entries[hours]
	if !ok || time.Now().After(entry.expiresAt) {
		delete(c.entries, hours)
		return nil
	}
	payload := make([]byte, len(entry.payload))
	copy(payload, entry.payload)
	return payload
}

func (c *routeHistoryCacheState) set(hours int, payload []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[int]routeHistoryCacheEntry)
	}
	copied := make([]byte, len(payload))
	copy(copied, payload)
	c.entries[hours] = routeHistoryCacheEntry{
		payload:   copied,
		expiresAt: time.Now().Add(routeHistoryCacheTTL),
	}
}

func (s *Server) handleRouteHistory(w http.ResponseWriter, r *http.Request) {
	hours := 24
	if h := r.URL.Query().Get("hours"); h != "" {
		n, err := strconv.Atoi(h)
		if err != nil || n < 1 || n > 168 {
			writeError(w, http.StatusBadRequest, "hours must be between 1 and 168")
			return
		}
		hours = n
	}

	if payload := s.routeHistoryCache.get(hours); payload != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write(payload)
		return
	}

	since := time.Now().Add(-time.Duration(hours) * time.Hour).UTC().Format(time.RFC3339)
	edgeMap := map[rhEdgeKey]*rhEdgeData{}

	// Serve from the in-memory store only when resolved_path is unavailable.
	// Current schemas persist resolved_path, which turns short hop prefixes into
	// full pubkeys and is required for reliable node/GPS joins.
	servedFromStore := false
	if s.store != nil && (s.db == nil || !s.db.hasResolvedPath) {
		s.store.mu.RLock()
		oldest := s.store.oldestLoaded
		snap := s.store.packets
		s.store.mu.RUnlock()

		if oldest != "" && oldest <= since {
			servedFromStore = true
			buildRouteHistoryEdgesFromSnap(s.store, snap, since, edgeMap)
		}
	}

	if !servedFromStore {
		if err := buildRouteHistoryEdgesFromDB(r, s, since, edgeMap); err != nil {
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}
	}

	candidateEdges := len(edgeMap)

	pubkeys := make(map[string]bool, len(edgeMap)*2)
	for k := range edgeMap {
		pubkeys[k.a] = true
		pubkeys[k.b] = true
	}
	nodeCache := make(map[string]*nodeInfo, len(pubkeys))
	if len(pubkeys) > 0 {
		placeholders := make([]string, 0, len(pubkeys))
		args := make([]interface{}, 0, len(pubkeys))
		for pk := range pubkeys {
			placeholders = append(placeholders, "?")
			args = append(args, pk)
		}
		nrows, nerr := s.db.conn.QueryContext(r.Context(),
			`SELECT public_key, COALESCE(name,''), lat, lon FROM nodes WHERE public_key IN (`+strings.Join(placeholders, ",")+`)`,
			args...,
		)
		if nerr == nil {
			defer nrows.Close()
			for nrows.Next() {
				var pk, name string
				var lat, lon *float64
				if nerr := nrows.Scan(&pk, &name, &lat, &lon); nerr != nil {
					continue
				}
				info := &nodeInfo{PublicKey: pk, Name: name}
				if lat != nil && lon != nil {
					info.Lat, info.Lon, info.HasGPS = *lat, *lon, !(*lat == 0 && *lon == 0)
				}
				nodeCache[pk] = info
			}
		}
	}

	edges := make([]routeHistoryEdge, 0, len(edgeMap))
	missingGPS := 0
	for k, e := range edgeMap {
		nA, nB := nodeCache[k.a], nodeCache[k.b]
		if nA == nil || nB == nil || !nA.HasGPS || !nB.HasGPS {
			missingGPS++
			continue
		}
		samples := e.samples
		if samples == nil {
			samples = []string{}
		}
		edges = append(edges, routeHistoryEdge{
			NodeA: k.a, NodeB: k.b,
			NameA: nA.Name, NameB: nB.Name,
			LatA: nA.Lat, LonA: nA.Lon,
			LatB: nB.Lat, LonB: nB.Lon,
			Count: e.count, LastSeen: e.lastSeen,
			Samples: samples,
		})
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].Count > edges[j].Count })

	resp := routeHistoryResponse{
		Edges:           edges,
		Hours:           hours,
		TotalEdges:      len(edges),
		CandidateEdges:  candidateEdges,
		MissingGPSEdges: missingGPS,
		MappedEdges:     len(edges),
		RawEdges:        candidateEdges,
		UnmappedEdges:   missingGPS,
	}
	payload, err := json.Marshal(resp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "json error")
		return
	}
	s.routeHistoryCache.set(hours, payload)
	w.Header().Set("Content-Type", "application/json")
	w.Write(payload)
}

func buildRouteHistoryEdgesFromSnap(store *PacketStore, snap []*StoreTx, since string, edgeMap map[rhEdgeKey]*rhEdgeData) {
	txs := make([]*StoreTx, 0, 256)
	for i := len(snap) - 1; i >= 0; i-- {
		tx := snap[i]
		if tx.FirstSeen < since {
			break
		}
		txs = append(txs, tx)
	}

	txIDs := make([]int, 0, len(txs))
	for _, tx := range txs {
		txIDs = append(txIDs, tx.ID)
	}
	rpByTx := map[int]map[int][]*string{}
	if store != nil && store.db != nil && store.db.hasResolvedPath {
		rpByTx = store.prefetchResolvedPathsForTxs(txIDs)
	}
	for _, tx := range txs {
		if rp := resolvedPathForTxBestFromCache(tx, rpByTx[tx.ID]); rp != nil {
			if addRouteHistoryEdgesFromResolvedPath(tx.Hash, rp, tx.FirstSeen, edgeMap) > 0 {
				continue
			}
		}
		addRouteHistoryEdges(tx.Hash, tx.PathJSON, tx.FirstSeen, edgeMap)
	}
}

func buildRouteHistoryEdgesFromDB(r *http.Request, s *Server, since string, edgeMap map[rhEdgeKey]*rhEdgeData) error {
	resolvedPathSelect := "NULL AS resolved_path"
	resolvedPathWhere := ""
	if s.db.hasResolvedPath {
		resolvedPathSelect = "o.resolved_path"
		resolvedPathWhere = " OR (o.resolved_path IS NOT NULL AND o.resolved_path != '' AND o.resolved_path != '[]')"
	}
	rows, err := s.db.conn.QueryContext(r.Context(),
		`SELECT t.hash, o.path_json, `+resolvedPathSelect+`, t.first_seen
		 FROM transmissions t
		 JOIN observations o ON o.transmission_id = t.id
		 WHERE t.first_seen >= ?
		   AND ((o.path_json IS NOT NULL AND o.path_json != '' AND o.path_json != '[]')`+resolvedPathWhere+`)`,
		since,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var hash, firstSeen string
		var pathJSON, resolvedPath *string
		if err := rows.Scan(&hash, &pathJSON, &resolvedPath, &firstSeen); err != nil {
			continue
		}
		addRouteHistoryEdgesFromPaths(hash, strPtrValue(resolvedPath), strPtrValue(pathJSON), firstSeen, edgeMap)
	}
	return rows.Err()
}

func strPtrValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// addRouteHistoryEdgesFromPaths prefers resolvedPath because it contains full
// node pubkeys. pathJSON stores wire path prefixes and is only useful as a
// fallback on legacy rows without resolved_path.
func addRouteHistoryEdgesFromPaths(hash, resolvedPath, pathJSON, firstSeen string, edgeMap map[rhEdgeKey]*rhEdgeData) {
	if addRouteHistoryEdges(hash, resolvedPath, firstSeen, edgeMap) {
		return
	}
	addRouteHistoryEdges(hash, pathJSON, firstSeen, edgeMap)
}

func addRouteHistoryEdgesFromResolvedPath(hash string, resolvedPath []*string, firstSeen string, edgeMap map[rhEdgeKey]*rhEdgeData) int {
	added := 0
	for i := 0; i < len(resolvedPath)-1; i++ {
		if resolvedPath[i] == nil || resolvedPath[i+1] == nil {
			continue
		}
		a, b := strings.ToLower(*resolvedPath[i]), strings.ToLower(*resolvedPath[i+1])
		if a == "" || b == "" {
			continue
		}
		addRouteHistoryEdge(a, b, hash, firstSeen, edgeMap)
		added++
	}
	return added
}

// addRouteHistoryEdges parses a JSON path and increments all adjacent-hop edge
// counts in edgeMap. It returns false when the path had fewer than two usable
// hops so callers can fall back to another path source.
func addRouteHistoryEdges(hash, pathJSON, firstSeen string, edgeMap map[rhEdgeKey]*rhEdgeData) bool {
	if pathJSON == "" || pathJSON == "[]" {
		return false
	}
	var rawHops []*string
	if err := json.Unmarshal([]byte(pathJSON), &rawHops); err != nil {
		return false
	}
	hops := make([]string, 0, len(rawHops))
	for _, h := range rawHops {
		if h != nil && *h != "" {
			hops = append(hops, strings.ToLower(*h))
		}
	}
	if len(hops) < 2 {
		return false
	}
	for i := 0; i < len(hops)-1; i++ {
		addRouteHistoryEdge(hops[i], hops[i+1], hash, firstSeen, edgeMap)
	}
	return true
}

func addRouteHistoryEdge(a, b, hash, firstSeen string, edgeMap map[rhEdgeKey]*rhEdgeData) {
	if a > b {
		a, b = b, a
	}
	k := rhEdgeKey{a, b}
	e := edgeMap[k]
	if e == nil {
		e = &rhEdgeData{}
		edgeMap[k] = e
	}
	e.count++
	if firstSeen > e.lastSeen {
		e.lastSeen = firstSeen
	}
	if len(e.samples) < 5 {
		e.samples = append(e.samples, hash)
	}
}
