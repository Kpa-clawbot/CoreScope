package main

import (
	"sort"
	"strings"
)

// GPSSanityClusterTightKm: neighbors within this of the strongest-weight
// neighbor (the "anchor") form a trusted cluster. Chosen from stg's real
// neighbor_edges distance distribution (2026-07-30): the median
// real-GPS-to-real-GPS neighbor pair sits ~20km apart, p75 ~87km -- 50km
// comfortably covers legitimate direct/short-hop links without absorbing
// the long MQTT-bridge tail (p90+ jumps past 200km).
const GPSSanityClusterTightKm = 50.0

// GPSSanityMinClusterSize: need at least this many neighbors agreeing with
// each other before trusting their consensus -- a single neighbor could
// itself be wrong (misattributed via hash-prefix collision, or its own bad
// GPS), so one agreeing pair is the minimum for real corroboration.
const GPSSanityMinClusterSize = 2

// GPSSanitySuspectKm: a node is flagged when its own reported position is
// farther than this from its trusted cluster's weighted centroid --
// comfortably beyond any real single/few-hop LoRa distance, so a flag here
// means either a bad GPS fix or the node has genuinely relocated without a
// fresh advert since.
const GPSSanitySuspectKm = 100.0

// computeSuspiciousGPSPositions cross-checks every node with a real GPS fix
// against a trusted cluster of its own strongest RF neighbors: take the
// neighbor with the most shared observations (the "anchor"), keep whichever
// other neighbors agree with the anchor to within GPSSanityClusterTightKm,
// and if at least GPSSanityMinClusterSize survive, compare the node's own
// position against their weighted centroid. Most nodes are skipped, not
// evaluated: no neighbor_edges at all, no neighbor with its own real GPS
// fix, or a neighbor set too scattered to trust (itself likely contaminated
// by the same MQTT-bridge observer<->last-hop edges
// nearestPositionedNeighbor's geo-sanity filter already guards against).
//
// v1, kept deliberately simple: no confidence-weighting by neighbor_edges'
// hash-prefix ambiguity mode (the green/yellow/red "confidence" the
// Neighbors panel shows, see nodes.js's getConfidenceIndicator) -- that
// breakdown only lives in the in-memory NeighborGraph, not the persisted
// neighbor_edges table this reads directly, and raw observation count alone
// already produced plausible, sparse results against real stg data.
func computeSuspiciousGPSPositions(db *DB, positioned []areaAnalyticsNode) (GPSSanityResponse, error) {
	posByPK := make(map[string]areaAnalyticsNode, len(positioned))
	for _, n := range positioned {
		posByPK[n.PublicKey] = n
	}

	rows, err := db.conn.Query("SELECT node_a, node_b, count FROM neighbor_edges")
	if err != nil {
		return GPSSanityResponse{}, err
	}
	defer rows.Close()

	type edge struct {
		neighbor string
		weight   float64
	}
	adj := make(map[string][]edge)
	for rows.Next() {
		var a, b string
		var count float64
		if rows.Scan(&a, &b, &count) != nil {
			continue
		}
		aLower, bLower := strings.ToLower(a), strings.ToLower(b)
		adj[aLower] = append(adj[aLower], edge{neighbor: bLower, weight: count})
		adj[bLower] = append(adj[bLower], edge{neighbor: aLower, weight: count})
	}
	if err := rows.Err(); err != nil {
		return GPSSanityResponse{}, err
	}

	var flagged []SuspiciousGPSNode
	evaluated := 0
	for _, n := range positioned {
		neighbors := adj[n.PublicKey]
		if len(neighbors) == 0 {
			continue
		}
		sort.Slice(neighbors, func(i, j int) bool { return neighbors[i].weight > neighbors[j].weight })
		if len(neighbors) > 20 {
			neighbors = neighbors[:20]
		}

		var candidates []edge
		for _, e := range neighbors {
			if e.neighbor == n.PublicKey {
				continue
			}
			if _, ok := posByPK[e.neighbor]; ok {
				candidates = append(candidates, e)
			}
		}
		if len(candidates) == 0 {
			continue
		}

		anchor := posByPK[candidates[0].neighbor]
		tight := []edge{candidates[0]}
		for _, c := range candidates[1:] {
			cp := posByPK[c.neighbor]
			if haversineKm(anchor.Lat, anchor.Lon, cp.Lat, cp.Lon) <= GPSSanityClusterTightKm {
				tight = append(tight, c)
			}
		}
		if len(tight) < GPSSanityMinClusterSize {
			continue
		}
		evaluated++

		var sumLat, sumLon, sumW float64
		for _, c := range tight {
			p := posByPK[c.neighbor]
			w := c.weight
			if w <= 0 {
				w = 1
			}
			sumLat += p.Lat * w
			sumLon += p.Lon * w
			sumW += w
		}
		clusterLat := sumLat / sumW
		clusterLon := sumLon / sumW

		var clusterSpread float64
		for _, c := range tight {
			p := posByPK[c.neighbor]
			if d := haversineKm(clusterLat, clusterLon, p.Lat, p.Lon); d > clusterSpread {
				clusterSpread = d
			}
		}

		dist := haversineKm(n.Lat, n.Lon, clusterLat, clusterLon)
		if dist > GPSSanitySuspectKm {
			flagged = append(flagged, SuspiciousGPSNode{
				PublicKey: n.PublicKey, Name: n.Name,
				Lat: n.Lat, Lon: n.Lon,
				ClusterLat: clusterLat, ClusterLon: clusterLon,
				DistanceKm: dist, ClusterSpreadKm: clusterSpread,
				ClusterSize: len(tight),
			})
		}
	}

	sort.Slice(flagged, func(i, j int) bool { return flagged[i].DistanceKm > flagged[j].DistanceKm })

	return GPSSanityResponse{
		Nodes:        flagged,
		TotalRealGPS: len(positioned),
		Evaluated:    evaluated,
	}, nil
}
