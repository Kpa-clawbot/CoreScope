package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
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
}

type rhEdgeKey struct{ a, b string }
type rhEdgeData struct {
	count    int
	lastSeen string
	samples  []string
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

	since := time.Now().Add(-time.Duration(hours) * time.Hour).UTC().Format(time.RFC3339)
	edgeMap := map[rhEdgeKey]*rhEdgeData{}

	// Serve from the in-memory store when it covers the full requested window
	// only on schemas without persisted resolved_path. When resolved_path is
	// available, route-history must use it so path prefixes become full pubkeys
	// that can join to nodes/GPS.
	// tx.PathJSON is set once under s.mu.Lock() and never mutated after, so
	// reading it from a snapshotted *StoreTx outside the lock is race-free.
	// The snap slice header itself is O(1) to copy under RLock.
	servedFromStore := false
	if s.store != nil && (s.db == nil || !s.db.hasResolvedPath) {
		s.store.mu.RLock()
		oldest := s.store.oldestLoaded
		snap := s.store.packets // O(1) slice header copy
		s.store.mu.RUnlock()

		if oldest != "" && oldest <= since {
			// Store covers the full window — no DB query needed.
			servedFromStore = true
			buildRouteHistoryEdgesFromSnap(snap, since, edgeMap)
		}
	}

	if !servedFromStore {
		// Fall back to DB: store doesn't cover the full window (e.g. hot window
		// shorter than hours, or store disabled). Same query as before.
		if err := buildRouteHistoryEdgesFromDB(r, s, since, edgeMap); err != nil {
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}
	}

	candidateEdges := len(edgeMap)

	// Resolve node GPS from nodes table — single batch query regardless of path taken above.
	type nodeInfo struct {
		name   string
		lat    float64
		lon    float64
		hasGPS bool
	}
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
				info := &nodeInfo{name: name}
				if lat != nil && lon != nil {
					info.lat, info.lon, info.hasGPS = *lat, *lon, true
				}
				nodeCache[pk] = info
			}
		}
	}

	edges := make([]routeHistoryEdge, 0, len(edgeMap))
	missingGPS := 0
	for k, e := range edgeMap {
		nA, nB := nodeCache[k.a], nodeCache[k.b]
		if nA == nil || nB == nil || !nA.hasGPS || !nB.hasGPS {
			missingGPS++
			continue
		}
		samples := e.samples
		if samples == nil {
			samples = []string{}
		}
		edges = append(edges, routeHistoryEdge{
			NodeA: k.a, NodeB: k.b,
			NameA: nA.name, NameB: nB.name,
			LatA: nA.lat, LonA: nA.lon,
			LatB: nB.lat, LonB: nB.lon,
			Count: e.count, LastSeen: e.lastSeen,
			Samples: samples,
		})
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].Count > edges[j].Count })

	writeJSON(w, routeHistoryResponse{
		Edges:           edges,
		Hours:           hours,
		TotalEdges:      len(edges),
		CandidateEdges:  candidateEdges,
		MissingGPSEdges: missingGPS,
	})
}

// buildRouteHistoryEdgesFromSnap populates edgeMap from the in-memory packet
// snapshot.  Packets are stored oldest-first; we scan backwards and stop as
// soon as we pass the 'since' boundary.  tx.PathJSON is read lock-free because
// it is written once under s.mu.Lock() and never mutated afterward.
func buildRouteHistoryEdgesFromSnap(snap []*StoreTx, since string, edgeMap map[rhEdgeKey]*rhEdgeData) {
	for i := len(snap) - 1; i >= 0; i-- {
		tx := snap[i]
		if tx.FirstSeen < since {
			break
		}
		addRouteHistoryEdges(tx.Hash, tx.PathJSON, tx.FirstSeen, edgeMap)
	}
}

// buildRouteHistoryEdgesFromDB runs the legacy JOIN query and populates edgeMap.
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
			hops = append(hops, *h)
		}
	}
	if len(hops) < 2 {
		return false
	}
	for i := 0; i < len(hops)-1; i++ {
		a, b := hops[i], hops[i+1]
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
	return true
}
