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

// computeNodeHopPackets returns one entry per flood packet in txs that the node
// forwarded, and how many packets matched the node's prefix but could not be
// attributed to it.
//
// A packet counts when the node's prefix sits at exactly one index across all
// its observations, and that hop is the node: either the prefix belongs to no
// other relay-capable node, or the store's resolved path for the packet already
// contains the node (resolvedTxIDs, from resolvedPubkeyIndex, which ingest and
// load fill from resolved paths).
// A packet whose prefix sits at several indices, or collides without that
// confirmation, is counted as ambiguous and left out.
//
// txs must already be deduplicated by hash (byNode is). Cost is linear in the
// hops of all observations of txs.
func computeNodeHopPackets(pubkey string, txs []*StoreTx, pm *prefixMap, resolvedTxIDs map[int]struct{}) ([]NodeHopPacket, int) {
	lowerPK := strings.ToLower(pubkey)
	packets := make([]NodeHopPacket, 0)
	ambiguous := 0
	uniquePrefix := map[string]bool{}

	for _, tx := range txs {
		if tx.RouteType == nil || (*tx.RouteType != RouteFlood && *tx.RouteType != RouteTransportFlood) {
			continue
		}
		if tx.DecodedJSON != "" && strings.Contains(tx.DecodedJSON, "ubKey") && strings.EqualFold(extractFromNode(tx), lowerPK) {
			continue
		}

		idx, prefix, conflict := -1, "", false
		for _, obs := range tx.Observations {
			p := obs.PathJSON
			// path_json is a JSON array of hex hop strings, which carry no
			// escapes, so hops are read between quotes without unmarshalling.
			// parsePathJSON here took 247 ms and 3.96M allocations per call in
			// BenchmarkNodeHopPackets, against 25 ms and 27 for this scan, and
			// the scan runs under s.mu.RLock.
			for i, pos := 0, 0; ; i++ {
				open := strings.IndexByte(p[pos:], '"')
				if open < 0 {
					break
				}
				start := pos + open + 1
				n := strings.IndexByte(p[start:], '"')
				if n < 0 {
					break
				}
				hop := p[start : start+n]
				pos = start + n + 1
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
		if conflict {
			ambiguous++
			continue
		}
		if _, confirmed := resolvedTxIDs[tx.ID]; !confirmed {
			unique, seen := uniquePrefix[prefix]
			if !seen {
				cands := pm.relayCandidates(prefix)
				unique = len(cands) == 1 && strings.EqualFold(cands[0].PublicKey, lowerPK)
				uniquePrefix[prefix] = unique
			}
			if !unique {
				ambiguous++
				continue
			}
		}

		tags := hopTagsUnscoped
		advert := tx.PayloadType != nil && *tx.PayloadType == PayloadADVERT
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
	defer s.mu.RUnlock()

	_, pm := s.getCachedNodesAndPM()
	var resolvedTxIDs map[int]struct{}
	if ids := s.resolvedPubkeyIndex[resolvedPubkeyHash(pubkey)]; len(ids) > 0 {
		resolvedTxIDs = make(map[int]struct{}, len(ids))
		for _, id := range ids {
			resolvedTxIDs[id] = struct{}{}
		}
	}
	packets, ambiguous := computeNodeHopPackets(pubkey, s.nodeTxsSince(pubkey, fromISO), pm, resolvedTxIDs)

	return &NodeHopAnalyticsResponse{
		TimeRange: TimeRangeResp{From: fromISO, To: now.Format(time.RFC3339), Days: days},
		Packets:   packets,
		Ambiguous: ambiguous,
	}, nil
}
