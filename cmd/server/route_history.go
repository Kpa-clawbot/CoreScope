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
	mu       sync.Mutex
	entries  map[int]routeHistoryCacheEntry
	inFlight map[int]*routeHistoryInFlight
}

type routeHistoryCacheEntry struct {
	payload   []byte
	expiresAt time.Time
}

type routeHistoryInFlight struct {
	done    chan struct{}
	payload []byte
	err     error
}

const routeHistoryCacheTTL = 60 * time.Second

func (c *routeHistoryCacheState) get(hours int) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.getLocked(hours, time.Now())
}

func (c *routeHistoryCacheState) getLocked(hours int, now time.Time) []byte {
	if c.entries == nil {
		return nil
	}
	entry, ok := c.entries[hours]
	if !ok || now.After(entry.expiresAt) {
		delete(c.entries, hours)
		return nil
	}
	payload := make([]byte, len(entry.payload))
	copy(payload, entry.payload)
	return payload
}

func (c *routeHistoryCacheState) getOrStart(hours int) ([]byte, *routeHistoryInFlight, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if payload := c.getLocked(hours, time.Now()); payload != nil {
		return payload, nil, false
	}
	if c.inFlight == nil {
		c.inFlight = make(map[int]*routeHistoryInFlight)
	}
	if call := c.inFlight[hours]; call != nil {
		return nil, call, false
	}
	call := &routeHistoryInFlight{done: make(chan struct{})}
	c.inFlight[hours] = call
	return nil, call, true
}

func (c *routeHistoryCacheState) finish(hours int, call *routeHistoryInFlight, payload []byte, err error) {
	c.mu.Lock()
	if c.inFlight != nil && c.inFlight[hours] == call {
		delete(c.inFlight, hours)
	}
	if err == nil && payload != nil {
		c.setLocked(hours, payload, time.Now())
	}
	if payload != nil {
		call.payload = make([]byte, len(payload))
		copy(call.payload, payload)
	}
	call.err = err
	c.mu.Unlock()
	close(call.done)
}

func (c *routeHistoryCacheState) wait(call *routeHistoryInFlight) ([]byte, error) {
	<-call.done
	c.mu.Lock()
	defer c.mu.Unlock()
	if call.err != nil {
		return nil, call.err
	}
	payload := make([]byte, len(call.payload))
	copy(payload, call.payload)
	return payload, nil
}

func (c *routeHistoryCacheState) set(hours int, payload []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setLocked(hours, payload, time.Now())
}

func (c *routeHistoryCacheState) setLocked(hours int, payload []byte, now time.Time) {
	if c.entries == nil {
		c.entries = make(map[int]routeHistoryCacheEntry)
	}
	copied := make([]byte, len(payload))
	copy(copied, payload)
	c.entries[hours] = routeHistoryCacheEntry{
		payload:   copied,
		expiresAt: now.Add(routeHistoryCacheTTL),
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

	payload, call, started := s.routeHistoryCache.getOrStart(hours)
	if payload != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write(payload)
		return
	}
	if !started {
		payload, err := s.routeHistoryCache.wait(call)
		if err != nil || payload == nil {
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(payload)
		return
	}

	payload, err := s.buildRouteHistoryPayload(r, hours)
	s.routeHistoryCache.finish(hours, call, payload, err)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(payload)
}

func (s *Server) buildRouteHistoryPayload(r *http.Request, hours int) ([]byte, error) {
	since := time.Now().Add(-time.Duration(hours) * time.Hour).UTC().Format(time.RFC3339)
	edgeMap := map[rhEdgeKey]*rhEdgeData{}

	servedFromMaterialized := false
	if s.db != nil {
		ok, err := buildRouteHistoryEdgesFromMaterialized(r, s, since, edgeMap)
		if err != nil {
			return nil, err
		}
		servedFromMaterialized = ok
	}

	// Prefer the in-memory store when it covers the requested window. For modern
	// schemas we batch-fetch resolved_path only for the loaded tx IDs, avoiding a
	// wide observations scan on every route-history cache miss.
	servedFromStore := false
	if !servedFromMaterialized && s.store != nil {
		s.store.mu.RLock()
		oldest := s.store.oldestLoaded
		snap := s.store.packets
		s.store.mu.RUnlock()

		if oldest != "" && oldest <= since {
			servedFromStore = true
			buildRouteHistoryEdgesFromSnap(s.store, snap, since, edgeMap)
		}
	}

	if !servedFromMaterialized && !servedFromStore {
		if err := buildRouteHistoryEdgesFromDB(r, s, since, edgeMap); err != nil {
			return nil, err
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
		return nil, err
	}
	return payload, nil
}

func buildRouteHistoryEdgesFromMaterialized(r *http.Request, s *Server, since string, edgeMap map[rhEdgeKey]*rhEdgeData) (bool, error) {
	rows, err := s.db.conn.QueryContext(r.Context(),
		`SELECT node_a, node_b, packet_hash, last_seen
		 FROM route_history_edges
		 WHERE last_seen >= ?
		 ORDER BY last_seen DESC`,
		since,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return false, nil
		}
		return false, err
	}
	defer rows.Close()

	seen := false
	for rows.Next() {
		var a, b, hash, lastSeen string
		if err := rows.Scan(&a, &b, &hash, &lastSeen); err != nil {
			continue
		}
		addRouteHistoryEdge(a, b, hash, lastSeen, edgeMap)
		seen = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return seen, nil
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
	hasResolvedPath := store != nil && store.db != nil && store.db.hasResolvedPath
	for _, tx := range txs {
		if hasResolvedPath {
			if rp := resolvedPathForTxBestFromCache(tx, rpByTx[tx.ID]); rp != nil {
				addRouteHistoryEdgesFromResolvedPath(tx.Hash, rp, tx.FirstSeen, edgeMap)
			}
			continue
		}
		addRouteHistoryEdges(tx.Hash, tx.PathJSON, tx.FirstSeen, edgeMap)
	}
}

func buildRouteHistoryEdgesFromDB(r *http.Request, s *Server, since string, edgeMap map[rhEdgeKey]*rhEdgeData) error {
	if s.db.hasResolvedPath {
		rows, err := s.db.conn.QueryContext(r.Context(),
			`SELECT t.hash, o.resolved_path, t.first_seen
			 FROM observations o
			 JOIN transmissions t ON t.id = o.transmission_id
			 WHERE t.first_seen >= ?
			   AND o.resolved_path IS NOT NULL
			   AND o.resolved_path != ''
			   AND o.resolved_path != '[]'`,
			since,
		)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var hash, resolvedPath, firstSeen string
			if err := rows.Scan(&hash, &resolvedPath, &firstSeen); err != nil {
				continue
			}
			addRouteHistoryEdges(hash, resolvedPath, firstSeen, edgeMap)
		}
		return rows.Err()
	}

	rows, err := s.db.conn.QueryContext(r.Context(),
		`SELECT t.hash, o.path_json, t.first_seen
		 FROM transmissions t
		 JOIN observations o ON o.transmission_id = t.id
		 WHERE t.first_seen >= ?
		   AND o.path_json IS NOT NULL
		   AND o.path_json != ''
		   AND o.path_json != '[]'`,
		since,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var hash, firstSeen string
		var pathJSON string
		if err := rows.Scan(&hash, &pathJSON, &firstSeen); err != nil {
			continue
		}
		addRouteHistoryEdges(hash, pathJSON, firstSeen, edgeMap)
	}
	return rows.Err()
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
