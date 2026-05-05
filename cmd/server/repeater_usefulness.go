package main

import "strings"

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
