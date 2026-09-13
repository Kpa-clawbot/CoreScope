package main

import (
	"strings"
	"time"
)

// Per-node hop count statistics (issue #1812).
//
// A repeater decides whether to forward a flood with
// isFloodHopLimitExceeded (firmware src/helpers/RoutingPolicy.h:15-21),
// comparing getPathHashCount() (src/Packet.h:80, path_len & 63) against
// flood.max, flood.max.unscoped (ROUTE_TYPE_FLOOD only) and flood.max.advert
// (PAYLOAD_TYPE_ADVERT only). Mesh::routeRecvPacket (src/Mesh.cpp:344-350)
// runs that check with n hashes in the path and then writes its own hash at
// index n. So the node's zero-based index in an observed flood path is the hop
// count its flood.max check saw. Firmware refs: meshcore-dev/MeshCore 0679dbef.
//
// Hash rules the attribution relies on:
//   - A node forwards a given flood once (wasSeen/markSeen before
//     routeRecvPacket, e.g. src/Mesh.cpp:265-285 for adverts), so it sits at
//     one index in every observation that contains it. A prefix match at two
//     different indices means another node shares the prefix.
//   - All hops of one packet share one hash size (src/Packet.h:79), so every
//     match in a packet is the same prefix string.
//   - An originator marks its own flood seen before sending
//     (Mesh::sendFlood, src/Mesh.cpp:651 and :680), so it never appears in
//     that path.
//   - DIRECT paths are the remaining route and shrink at every hop
//     (removeSelfFromPath, src/Mesh.cpp:334-341); the flood.max limits only
//     apply to floods (examples/simple_repeater/MyMesh.cpp:436-437). DIRECT
//     packets carry no hop count for this purpose and are skipped.

var (
	hopTagsUnscoped       = []string{"flood", "unscoped"}
	hopTagsScoped         = []string{"flood", "scoped"}
	hopTagsUnscopedAdvert = []string{"flood", "unscoped", "advert"}
	hopTagsScopedAdvert   = []string{"flood", "scoped", "advert"}
)

// nextPathHop returns the hop string starting after offset pos of a path_json
// array and the offset after it, or ok=false when no hop is left. path_json
// is a JSON array of hex hop strings, which carry no escapes, so hops are read
// between quotes without unmarshalling and without allocating: the scan runs
// under s.mu.RLock for every observation in the window.
func nextPathHop(p string, pos int) (hop string, next int, ok bool) {
	open := strings.IndexByte(p[pos:], '"')
	if open < 0 {
		return "", pos, false
	}
	start := pos + open + 1
	n := strings.IndexByte(p[start:], '"')
	if n < 0 {
		return "", pos, false
	}
	return p[start : start+n], start + n + 1, true
}

// hopAttributor decides whether a path hop is a given node without using the
// server resolver's tiebreaks (affinity score, GPS distance, advert count,
// pubkey order), which always name a winner among colliding prefixes.
// Candidates are prefixMap.relayCandidates; adjacency is the neighbor graph,
// which the server loads from the ingestor's neighbor_edges, the same table
// the ingestor's resolver reads. Caches live for one request.
type hopAttributor struct {
	pm    *prefixMap
	graph *NeighborGraph
	cands map[string][]string
	adj   map[string]map[string]struct{}
	seen  []string
}

func newHopAttributor(pm *prefixMap, graph *NeighborGraph) *hopAttributor {
	return &hopAttributor{pm: pm, graph: graph, cands: map[string][]string{}, adj: map[string]map[string]struct{}{}}
}

// candidates returns the lowercase pubkeys of the relay candidates for a hop.
// The cache is keyed by the hop as given, so wire-case hops are looked up
// without lowercasing a copy.
func (a *hopAttributor) candidates(hop string) []string {
	c, ok := a.cands[hop]
	if !ok {
		for _, n := range a.pm.relayCandidates(hop) {
			c = append(c, strings.ToLower(n.PublicKey))
		}
		a.cands[hop] = c
	}
	return c
}

func (a *hopAttributor) adjacent(anchor, pk string) bool {
	nbrs, ok := a.adj[anchor]
	if !ok {
		nbrs = map[string]struct{}{}
		if a.graph != nil {
			for _, e := range a.graph.Neighbors(anchor) {
				if e.Ambiguous || e.NodeA == "" || e.NodeB == "" {
					continue
				}
				other := e.NodeA
				if other == anchor {
					other = e.NodeB
				}
				nbrs[other] = struct{}{}
			}
		}
		a.adj[anchor] = nbrs
	}
	_, ok = nbrs[pk]
	return ok
}

// strictHopAt resolves the hop at index idx of path p the way the ingestor
// does (cmd/ingestor/path_resolver.go resolvePathWithContext): walking from
// hop 0, a hop with one candidate resolves to it, a hop with several resolves
// only when exactly one of them is a graph neighbor of the previous resolved
// hop (the advert originator for hop 0), and nodes already on the path are
// excluded. An unresolved hop breaks the chain for the next one.
//
// ok is false when p has no hop idx or that hop is not prefix. Otherwise pk is
// the lowercase pubkey the hop resolves to, or "" when it does not resolve,
// and end is the offset in p after hop idx.
func (a *hopAttributor) strictHopAt(p string, idx int, prefix, origin string) (pk string, end int, ok bool) {
	anchor := origin
	a.seen = a.seen[:0]
	if anchor != "" {
		a.seen = append(a.seen, anchor)
	}
	pos := 0
	for i := 0; i <= idx; i++ {
		hop, next, found := nextPathHop(p, pos)
		if !found || (i == idx && !strings.EqualFold(hop, prefix)) {
			return "", 0, false
		}
		pos = next
		cands := a.candidates(hop)
		match, survivors := "", 0
		switch {
		case len(cands) == 1:
			if c := cands[0]; !a.onPath(c) {
				match, survivors = c, 1
			}
		case len(cands) > 1 && anchor != "" && a.graph != nil:
			for _, c := range cands {
				if !a.onPath(c) && a.adjacent(anchor, c) {
					match = c
					survivors++
				}
			}
		}
		if survivors != 1 {
			match = ""
		}
		if i == idx {
			return match, pos, true
		}
		anchor = match
		if match != "" {
			a.seen = append(a.seen, match)
		}
	}
	return "", 0, false
}

func (a *hopAttributor) onPath(pk string) bool {
	for _, s := range a.seen {
		if s == pk {
			return true
		}
	}
	return false
}

// computeNodeHopPackets returns one entry per flood packet in txs, first seen
// after fromISO, that the node forwarded, and how many packets carry the node's
// prefix at one index without being attributable to it.
//
// A packet counts when the node's prefix sits at exactly one index across all
// its observations, and that hop is the node independently of the server
// resolver's pick: either the node is the only relay candidate for the prefix,
// or, for a colliding prefix, at least one observation resolves that hop to the
// node under the ingestor's strict neighbor rule (hopAttributor.strictHopAt)
// and none resolves it to another node. Everything else with the node's prefix
// is counted as ambiguous and left out. The answer depends only on the
// observed paths, the prefix map and the neighbor graph, so it is the same
// after a restart as after live ingest.
//
// Every observation is read, not an index: byNode holds the resolver's pick at
// ingest and other picks after a cold load, and byPathHop only indexes each
// packet's longest path, which for a busy relay often runs through another
// branch of the flood (on a live 7-day sample it held 9,995 of the 23,081
// attributable packets of one repeater, skewed towards higher hop counts).
//
// txs must be deduplicated. Cost is linear in the observations of the flood
// packets in the window: a substring test per observation, the hop scan only
// for observations that contain the node's first prefix byte, and the strict
// walk only for colliding prefixes.
func computeNodeHopPackets(pubkey string, txs []*StoreTx, fromISO string, pm *prefixMap, graph *NeighborGraph) ([]NodeHopPacket, int) {
	lowerPK := strings.ToLower(pubkey)
	// Every hop hash of the node, whatever its size, starts with its first
	// pubkey byte, and path hops are written in upper case
	// (internal/packetpath/path.go:50; none of 1,495,712 live observations of
	// 7 days had a lower-case hex digit), so an observation without that byte
	// after an opening quote holds no hop of the node.
	quotedFirstByte := `"` + strings.ToUpper(lowerPK[:2])
	packets := make([]NodeHopPacket, 0)
	ambiguous := 0
	attr := newHopAttributor(pm, graph)
	onlyCandidate := map[string]bool{}

	for _, tx := range txs {
		if tx.RouteType == nil || (*tx.RouteType != RouteFlood && *tx.RouteType != RouteTransportFlood) || tx.FirstSeen <= fromISO {
			continue
		}

		idx, prefix, conflict := -1, "", false
		for _, obs := range tx.Observations {
			p := obs.PathJSON
			if !strings.Contains(p, quotedFirstByte) {
				continue
			}
			for i, pos := 0, 0; ; i++ {
				hop, next, ok := nextPathHop(p, pos)
				if !ok {
					break
				}
				pos = next
				if len(hop) == 0 || len(hop) > len(lowerPK) || !strings.EqualFold(hop, lowerPK[:len(hop)]) {
					continue
				}
				if idx < 0 {
					idx, prefix = i, lowerPK[:len(hop)]
				} else if i != idx {
					conflict = true
				}
			}
		}
		if idx < 0 {
			continue
		}
		origin := ""
		if tx.DecodedJSON != "" && strings.Contains(tx.DecodedJSON, "ubKey") {
			origin = strings.ToLower(extractFromNode(tx))
			if origin == lowerPK {
				continue
			}
		}
		if conflict {
			ambiguous++
			continue
		}

		advert := tx.PayloadType != nil && *tx.PayloadType == PayloadADVERT
		only, known := onlyCandidate[prefix]
		if !known {
			cands := attr.candidates(prefix)
			only = len(cands) == 1 && cands[0] == lowerPK
			onlyCandidate[prefix] = only
		}
		if !only && !attributeByNeighbor(attr, tx, idx, prefix, advert, origin, lowerPK) {
			ambiguous++
			continue
		}

		tags := hopTagsUnscoped
		switch {
		case *tx.RouteType == RouteTransportFlood && advert:
			tags = hopTagsScopedAdvert
		case *tx.RouteType == RouteTransportFlood:
			tags = hopTagsScoped
		case advert:
			tags = hopTagsUnscopedAdvert
		}
		packets = append(packets, NodeHopPacket{Hash: tx.Hash, Timestamp: tx.FirstSeen, Hops: idx, Tags: tags})
	}
	return packets, ambiguous
}

// attributeByNeighbor reports whether the hop at idx, which carries prefix, is
// the node under the strict neighbor rule in at least one observation and
// another node in none. The ingestor anchors hop 0 on the originator only for
// adverts (cmd/ingestor/db.go FromPubkey), and so does this. An observation
// whose path text equals the previous walked one up to hop idx reuses its
// result.
func attributeByNeighbor(attr *hopAttributor, tx *StoreTx, idx int, prefix string, advert bool, origin, lowerPK string) bool {
	if !advert {
		origin = ""
	}
	self := false
	walked, pk := "", ""
	for _, obs := range tx.Observations {
		if walked == "" || !strings.HasPrefix(obs.PathJSON, walked) {
			r, end, ok := attr.strictHopAt(obs.PathJSON, idx, prefix, origin)
			if !ok {
				continue
			}
			walked, pk = obs.PathJSON[:end], r
		}
		switch pk {
		case "":
		case lowerPK:
			self = true
		default:
			return false
		}
	}
	return self
}

// GetNodeHopAnalytics returns the hop count at this node for every flood packet
// it forwarded in the last days. Returns nil for an unknown node.
func (s *PacketStore) GetNodeHopAnalytics(pubkey string, days int) (*NodeHopAnalyticsResponse, error) {
	node, err := s.db.GetNodeByPubkey(pubkey)
	if err != nil || node == nil {
		return nil, err
	}

	now := time.Now()
	fromISO := now.Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)

	s.mu.RLock()
	_, pm := s.getCachedNodesAndPM()
	packets, ambiguous := computeNodeHopPackets(pubkey, s.packets, fromISO, pm, s.graph.Load())
	s.mu.RUnlock()

	return &NodeHopAnalyticsResponse{
		TimeRange: TimeRangeResp{From: fromISO, To: now.Format(time.RFC3339), Days: days},
		Packets:   packets,
		Ambiguous: ambiguous,
	}, nil
}
