package main

import (
	"strings"
	"time"
)

// GetRepeaterUsefulnessScore returns a 0..1 score representing what
// fraction of non-advert traffic in the store passes through this
// repeater as a relay hop. Issue #672 (Traffic axis only — bridge,
// coverage, and redundancy axes are deferred to follow-up work).
//
// Numerator:   count of non-advert StoreTx entries indexed under
//              pubkey in byPathHop.
// Denominator: total non-advert StoreTx entries in the store
//              (sum of byPayloadType for all keys != payloadTypeAdvert).
//
// Returns 0 when there is no non-advert traffic, the pubkey is empty,
// or the repeater never appears as a relay hop. Scores are clamped to
// [0,1] for defensive bounds.
//
// Cost: O(N) over byPayloadType keys (typically <20) plus the per-hop
// slice for pubkey. Cheap relative to the per-request enrichment loop
// in handleNodes; if it ever shows up in profiles, denominator can be
// memoized off store invalidation.
func (s *PacketStore) GetRepeaterUsefulnessScore(pubkey string) float64 {
	if pubkey == "" {
		return 0
	}
	key := strings.ToLower(pubkey)

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Denominator: total non-advert packets.
	totalNonAdvert := 0
	for pt, list := range s.byPayloadType {
		if pt == payloadTypeAdvert {
			continue
		}
		totalNonAdvert += len(list)
	}
	if totalNonAdvert == 0 {
		return 0
	}

	// Numerator: this repeater's non-advert hop appearances.
	relayed := 0
	for _, tx := range s.byPathHop[key] {
		if tx == nil {
			continue
		}
		if tx.PayloadType != nil && *tx.PayloadType == payloadTypeAdvert {
			continue
		}
		relayed++
	}

	score := float64(relayed) / float64(totalNonAdvert)
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

// RepeaterNodeStats bundles relay-activity and usefulness data for a single node.
type RepeaterNodeStats struct {
	Info  RepeaterRelayInfo
	Score float64
}

// GetRepeaterNodeStatsBatch computes relay info and usefulness scores for all given
// pubkeys in a single read-lock pass, sharing the non-advert denominator across all
// nodes. Replaces the per-node loop in handleNodes that called GetRepeaterRelayInfo +
// GetRepeaterUsefulnessScore N times (O(N × byPayloadType) → O(byPayloadType + N)).
func (s *PacketStore) GetRepeaterNodeStatsBatch(pubkeys []string, windowHours float64) map[string]RepeaterNodeStats {
	result := make(map[string]RepeaterNodeStats, len(pubkeys))
	if len(pubkeys) == 0 {
		return result
	}

	type nodeSnap struct {
		txList     []*StoreTx
		prefixList []*StoreTx
	}

	s.mu.RLock()

	totalNonAdvert := 0
	for pt, list := range s.byPayloadType {
		if pt != payloadTypeAdvert {
			totalNonAdvert += len(list)
		}
	}

	snaps := make(map[string]nodeSnap, len(pubkeys))
	for _, pk := range pubkeys {
		key := strings.ToLower(pk)
		snap := nodeSnap{txList: s.byPathHop[key]}
		if len(key) >= 2 {
			if prefix := key[:2]; prefix != key {
				snap.prefixList = s.byPathHop[prefix]
			}
		}
		snaps[pk] = snap
	}

	s.mu.RUnlock()

	now := time.Now().UTC()
	cutoff1h := now.Add(-time.Hour)
	cutoff24h := now.Add(-24 * time.Hour)
	var windowCutoff time.Time
	if windowHours > 0 {
		windowCutoff = now.Add(-time.Duration(float64(time.Hour) * windowHours))
	}

	for _, pk := range pubkeys {
		snap := snaps[pk]
		info := RepeaterRelayInfo{WindowHours: windowHours}

		type entry struct {
			ts string
			pt int
		}
		uniq := make(map[int]struct{}, len(snap.txList)+len(snap.prefixList))
		for _, tx := range snap.txList {
			if tx != nil {
				uniq[tx.ID] = struct{}{}
			}
		}
		for _, tx := range snap.prefixList {
			if tx != nil {
				uniq[tx.ID] = struct{}{}
			}
		}
		entries := make([]entry, 0, len(uniq))
		seen := make(map[int]bool, len(uniq))
		collect := func(list []*StoreTx) {
			for _, tx := range list {
				if tx == nil || seen[tx.ID] {
					continue
				}
				seen[tx.ID] = true
				pt := -1
				if tx.PayloadType != nil {
					pt = *tx.PayloadType
				}
				entries = append(entries, entry{ts: tx.FirstSeen, pt: pt})
			}
		}
		collect(snap.txList)
		collect(snap.prefixList)

		var latest time.Time
		var latestRaw string
		for _, e := range entries {
			if e.pt == payloadTypeAdvert {
				continue
			}
			t, ok := parseRelayTS(e.ts)
			if !ok {
				continue
			}
			if t.After(latest) {
				latest = t
				latestRaw = e.ts
			}
			if t.After(cutoff24h) {
				info.RelayCount24h++
				if t.After(cutoff1h) {
					info.RelayCount1h++
				}
			}
		}
		if latestRaw != "" {
			info.LastRelayed = latestRaw
			if windowHours > 0 && latest.After(windowCutoff) {
				info.RelayActive = true
			}
		}

		// Usefulness score uses full-key list only (matches GetRepeaterUsefulnessScore).
		var score float64
		if totalNonAdvert > 0 {
			relayed := 0
			for _, tx := range snap.txList {
				if tx == nil {
					continue
				}
				if tx.PayloadType != nil && *tx.PayloadType == payloadTypeAdvert {
					continue
				}
				relayed++
			}
			if relayed > 0 {
				score = float64(relayed) / float64(totalNonAdvert)
				if score > 1 {
					score = 1
				}
			}
		}

		result[pk] = RepeaterNodeStats{Info: info, Score: score}
	}

	return result
}
