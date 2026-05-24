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
	Edges      []routeHistoryEdge `json:"edges"`
	Hours      int                `json:"hours"`
	TotalEdges int                `json:"total_edges"`
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

	// path_json lives in observations; join to transmissions for hash and first_seen.
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
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	defer rows.Close()

	type edgeKey struct{ a, b string }
	type edgeData struct {
		count    int
		lastSeen string
		samples  []string
	}
	edgeMap := map[edgeKey]*edgeData{}

	for rows.Next() {
		var hash, pathJSON, firstSeen string
		if err := rows.Scan(&hash, &pathJSON, &firstSeen); err != nil {
			continue
		}
		var hops []string
		if err := json.Unmarshal([]byte(pathJSON), &hops); err != nil {
			continue
		}
		for i := 0; i < len(hops)-1; i++ {
			a, b := hops[i], hops[i+1]
			if a == "" || b == "" {
				continue
			}
			if a > b {
				a, b = b, a
			}
			k := edgeKey{a, b}
			e := edgeMap[k]
			if e == nil {
				e = &edgeData{}
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
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	// Resolve node GPS from nodes table — single batch query.
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
	for k, e := range edgeMap {
		nA, nB := nodeCache[k.a], nodeCache[k.b]
		if nA == nil || nB == nil || !nA.hasGPS || !nB.hasGPS {
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
		Edges:      edges,
		Hours:      hours,
		TotalEdges: len(edges),
	})
}
