// Package main: direct-RF attribution for the node-health "Heard By" card.
//
// An observer "heard" a node when it received that node's own transmission
// off the air. That is a narrower relation than "saw a packet this node was
// involved in", which is what the node-health card reported before this
// file existed, and it is the only one for which the SNR and RSSI printed
// next to an observer belong to the node the row names.
//
// The rule follows the firmware:
//
//   - Flood routes build the path up as they travel: a forwarding repeater
//     appends its own hash before retransmitting (Mesh.cpp:349). The last
//     hop is therefore the node whose transmission the observer received.
//   - Direct routes carry the REMAINING route, not the travelled one. A
//     forwarder matches itself against the head of the path and calls
//     removeSelfFromPath (Mesh.cpp:89,103) before retransmitting, so the
//     node the observer heard is not in the path at all. Direct routes
//     therefore never attribute.
//   - An empty flood path means the observer received the originator's own
//     transmission. Only ADVERTs carry the originator's pubkey in the
//     clear, so other payload types with an empty path attribute to nobody.
//   - A hop prefix that matches more than one node attributes to nobody.
//     Path hop sizes are chosen by the originator (Packet.h:83,
//     Mesh.cpp:649) and default to one byte, so a hop often matches many
//     candidates. resolveWithContext guesses in that case; this file does
//     not. The gate matches resolvePathForObsColdLoad: under-attribute
//     rather than credit the wrong node.
package main

import (
	"sort"
	"strings"
)

// directHeardAgg accumulates one observer's direct receptions of one node.
type directHeardAgg struct {
	ObserverName string
	Count        int
	SNRSum       float64
	SNRCount     int
	RSSISum      float64
	RSSICount    int
}

// directHeardIndex maps node pubkey (lowercase) to observer id to aggregate.
type directHeardIndex map[string]map[string]*directHeardAgg

// HealthObserverRow is one row of the node-health "Heard By" table.
//
// Field names and null semantics are unchanged from the map-based rows this
// replaced, so the frontend needs no migration: avgSnr and avgRssi are null
// when the observer contributed no sample, and can_relay is tri-state (null
// = no repeat field ever reported, see PR #1624).
type HealthObserverRow struct {
	ObserverID   string   `json:"observer_id"`
	ObserverName string   `json:"observer_name"`
	AvgSNR       *float64 `json:"avgSnr"`
	AvgRSSI      *float64 `json:"avgRssi"`
	PacketCount  int      `json:"packetCount"`
	CanRelay     *bool    `json:"can_relay"`
}

// lastPathHop returns the last quoted token of a path_json array, or "" when
// there is none. It scans backwards rather than unmarshalling: the compute
// pass runs over every observation in the store (2.9M on the reference
// deployment) and only ever needs the final element.
func lastPathHop(pathJSON string) string {
	end := -1
	for i := len(pathJSON) - 1; i >= 0; i-- {
		if pathJSON[i] != '"' {
			continue
		}
		if end < 0 {
			end = i
			continue
		}
		return pathJSON[i+1 : end]
	}
	return ""
}

// advertOriginPubkey returns the pubkey an ADVERT announces, or "" when the
// transmission is not an ADVERT or carries no decodable pubkey. Mirrors the
// field probing in trackAdvertPubkey.
func advertOriginPubkey(tx *StoreTx) string {
	if tx.PayloadType == nil || *tx.PayloadType != PayloadADVERT || tx.DecodedJSON == "" {
		return ""
	}
	d := tx.ParsedDecoded()
	if d == nil {
		return ""
	}
	if v, ok := d["pubKey"].(string); ok && v != "" {
		return strings.ToLower(v)
	}
	if v, ok := d["public_key"].(string); ok && v != "" {
		return strings.ToLower(v)
	}
	return ""
}

// directHeardNode returns the lowercase pubkey of the node whose
// transmission this observation received off the air, or "" when that cannot
// be established. See the package comment for the rule and its firmware
// grounding.
func directHeardNode(tx *StoreTx, obs *StoreObs, pm *prefixMap) string {
	if tx == nil || obs == nil || tx.RouteType == nil {
		return ""
	}
	hop := lastPathHop(obs.PathJSON)
	switch *tx.RouteType {
	case RouteFlood, RouteTransportFlood:
		// Handled below: empty path means the originator, otherwise the last
		// hop is whoever was heard.
	case RouteDirect, RouteTransportDirect:
		// A direct route normally says nothing about the transmitter, because
		// the forwarder calls removeSelfFromPath before retransmitting
		// (firmware Mesh.cpp) and the path left behind is the REMAINING route.
		//
		// The exception is a zero hop. Mesh::sendZeroHop sets ROUTE_TYPE_DIRECT
		// (its transport overload ROUTE_TYPE_TRANSPORT_DIRECT) and path_len = 0,
		// commented there as "path_len of zero means Zero Hop". Repeaters send
		// their periodic local advert that way (simple_repeater/MyMesh.cpp, the
		// next_local_advert branch and sendSelfAdvertisement with flood=false),
		// and so do companions. An ADVERT arriving on a direct route with an
		// empty path therefore cannot have been forwarded: the observer heard
		// the advertiser's own transmission, and the advert carries its pubkey
		// in the clear. That is the strongest direct-RF evidence there is.
		//
		// Anything else direct still says nothing: a non-empty path is the
		// remaining route, and advertOriginPubkey returns "" for every payload
		// type other than ADVERT.
		if hop == "" {
			return advertOriginPubkey(tx)
		}
		return ""
	default:
		return ""
	}
	if hop == "" {
		return advertOriginPubkey(tx)
	}
	if pm == nil {
		return ""
	}
	candidates := pm.relayCandidates(hop)
	if len(candidates) != 1 {
		return ""
	}
	return strings.ToLower(candidates[0].PublicKey)
}

// buildDirectHeardIndex folds every observation of every transmission into
// the node-to-observer aggregate. Pure over its arguments so it can be
// tested and benchmarked without a store.
func buildDirectHeardIndex(packets []*StoreTx, pm *prefixMap) directHeardIndex {
	idx := make(directHeardIndex, 256)
	for _, tx := range packets {
		for _, obs := range tx.Observations {
			if obs.ObserverID == "" {
				continue
			}
			pk := directHeardNode(tx, obs, pm)
			if pk == "" {
				continue
			}
			byObs := idx[pk]
			if byObs == nil {
				byObs = make(map[string]*directHeardAgg, 4)
				idx[pk] = byObs
			}
			agg := byObs[obs.ObserverID]
			if agg == nil {
				agg = &directHeardAgg{ObserverName: obs.ObserverName}
				byObs[obs.ObserverID] = agg
			}
			agg.Count++
			if obs.SNR != nil {
				agg.SNRSum += *obs.SNR
				agg.SNRCount++
			}
			if obs.RSSI != nil {
				agg.RSSISum += *obs.RSSI
				agg.RSSICount++
			}
		}
	}
	return idx
}

// computeDirectHeard rebuilds the whole index from the current store. Run by
// a background recomputer rather than per request: the reference deployment
// holds 232,928 transmissions and 2,887,861 observations, and one node's
// byNode slice alone can hold 1.45M observations.
//
// Rebuilding wholesale also means eviction needs no bookkeeping — a pass
// simply does not see transmissions that are gone.
func (s *PacketStore) computeDirectHeard() directHeardIndex {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, pm := s.getCachedNodesAndPM()
	return buildDirectHeardIndex(s.packets, pm)
}

// publishDirectHeard installs a snapshot for readers. Called by the
// recomputer after each pass; a nil-safe no-op for callers that have nothing
// to publish.
func (s *PacketStore) publishDirectHeard(idx directHeardIndex) {
	if idx == nil {
		idx = directHeardIndex{}
	}
	s.directHeardSnap.Store(idx)
}

// loadDirectHeard returns the latest snapshot, or nil before the first
// compute has published one. A nil index simply yields empty direct rows:
// the card degrades to "nobody hears this node" until the first pass lands,
// never to a wrong attribution.
func (s *PacketStore) loadDirectHeard() directHeardIndex {
	idx, _ := s.directHeardSnap.Load().(directHeardIndex)
	return idx
}

// canRelaySets fetches the two inputs behind the can_relay tri-state badge
// (#1290, PR #1624): the observers that reported repeat:off, and the
// observers we have any repeat field for at all. Both are lowercase to match
// pm.nonRelay and GetNonRelayObserverPubkeys; two case conventions on the
// same upstream string would be a latent regression. A read failure degrades
// to "no badge" rather than a wrong badge.
func (s *PacketStore) canRelaySets() (nonRelay, seen map[string]struct{}) {
	nonRelay = map[string]struct{}{}
	seen = map[string]struct{}{}
	if s.db == nil || s.db.conn == nil {
		return nonRelay, seen
	}
	if pks, err := s.db.GetNonRelayObserverPubkeys(); err == nil {
		for _, pk := range pks {
			nonRelay[strings.ToLower(pk)] = struct{}{}
		}
	}
	if pks, err := s.db.GetCanRelaySeenObserverPubkeys(); err == nil {
		for _, pk := range pks {
			seen[strings.ToLower(pk)] = struct{}{}
		}
	}
	return nonRelay, seen
}

// relayOnlyObserverCount counts observers that saw traffic involving the node
// without hearing it on air. seenObservers is the set the health builders
// already collect from each transmission's representative observation.
func relayOnlyObserverCount(seenObservers map[string]struct{}, direct map[string]*directHeardAgg) int {
	n := 0
	for id := range seenObservers {
		if _, isDirect := direct[id]; !isDirect {
			n++
		}
	}
	return n
}

// buildDirectObserverRows renders one node's aggregate as sorted API rows.
// nonRelay and seen carry the can_relay tri-state; both may be nil.
func buildDirectObserverRows(byObs map[string]*directHeardAgg, nonRelay, seen map[string]struct{}) []HealthObserverRow {
	rows := make([]HealthObserverRow, 0, len(byObs))
	for id, agg := range byObs {
		row := HealthObserverRow{
			ObserverID:   id,
			ObserverName: agg.ObserverName,
			PacketCount:  agg.Count,
		}
		if agg.SNRCount > 0 {
			v := agg.SNRSum / float64(agg.SNRCount)
			row.AvgSNR = &v
		}
		if agg.RSSICount > 0 {
			v := agg.RSSISum / float64(agg.RSSICount)
			row.AvgRSSI = &v
		}
		idLower := strings.ToLower(id)
		if _, ok := seen[idLower]; ok {
			_, isListener := nonRelay[idLower]
			canRelay := !isListener
			row.CanRelay = &canRelay
		}
		rows = append(rows, row)
	}
	// Packet count descending, observer id ascending as a deterministic
	// tiebreak so repeated requests return a stable order.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].PacketCount != rows[j].PacketCount {
			return rows[i].PacketCount > rows[j].PacketCount
		}
		return rows[i].ObserverID < rows[j].ObserverID
	})
	return rows
}
