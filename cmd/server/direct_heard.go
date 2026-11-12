// Package main: direct-RF attribution for the node-health "Heard By" card.
//
// An observer "heard" a node when it received that node's own transmission
// off the air. That is a narrower relation than "saw a packet this node was
// involved in", which is what the node-health card reported before this
// file existed, and it is the only one for which the SNR and RSSI printed
// next to an observer belong to the node the row names.
//
// The rule is packetpath.HeardKey, shared with the ingestor's client
// receptions: the last hop of a flood route, or the advertiser of a zero-hop
// ADVERT (flood, or direct via Mesh::sendZeroHop). It refuses three things,
// each because it can name the wrong node:
//
//   - a 1-byte last hop: a node CoreScope does not know can share that byte,
//     so even a prefix with one known candidate is not proof;
//   - TRACE: its path bytes carry per-hop SNR, not node hashes;
//   - a zero-hop ADVERT whose signature failed to verify: the pubkey in it
//     is not proven to belong to the transmitter.
//
// Direct routes with a path never attribute: a forwarder calls
// removeSelfFromPath (Mesh.cpp:89,103) before retransmitting, so the path
// left behind is the remaining route. A hop prefix of 2 bytes or more that
// matches more than one node, or that a retired repeater shares, also
// attributes to nobody: under-attribute rather than credit the wrong node,
// as resolvePathForObsColdLoad and uniqueHopPrefix do.
package main

import (
	"sort"
	"strings"

	"github.com/meshcore-analyzer/packetpath"
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
// be established. See the package comment for the rule.
func directHeardNode(tx *StoreTx, obs *StoreObs, pm *prefixMap) string {
	if tx == nil || obs == nil || tx.RouteType == nil || tx.PayloadType == nil {
		return ""
	}
	hop := lastPathHop(obs.PathJSON)
	adv := ""
	if hop == "" {
		// advertOriginPubkey checks the type first, so signatureFailed (which
		// may parse DecodedJSON) only runs for ADVERTs.
		if pk := advertOriginPubkey(tx); pk != "" && !tx.signatureFailed() {
			adv = pk
		}
	}
	key, src, ok := packetpath.HeardKey(*tx.RouteType, byte(*tx.PayloadType), hop, adv)
	if !ok {
		return ""
	}
	if src == "advert" {
		return key
	}
	if pm == nil {
		return ""
	}
	// A retired repeater (moved to inactive_nodes) may still relay on this
	// prefix, so the one active candidate is not proven (uniqueHopPrefix).
	if _, shared := pm.retired[key]; shared {
		return ""
	}
	// key is already lower case: look it up directly instead of through
	// relayCandidates, which lower-cases again on every observation.
	candidates, found := pm.relay[key]
	if !found {
		candidates = pm.m[key]
	}
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
			if obs.ObserverID == "" || obs.isOwnTransmission() {
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
//
// It counts as a read of the direct-heard recomputer, so with pauseWhenIdle
// on the card keeps it refreshing (the snapshot is read here, not through
// the recomputer's Load).
func (s *PacketStore) loadDirectHeard() directHeardIndex {
	s.analyticsRecomputerMu.RLock()
	rc := s.recompDirectHeard
	s.analyticsRecomputerMu.RUnlock()
	if rc != nil {
		rc.markRead()
	}
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
