package main

// GetRepeaterUsefulnessScore returns a 0..1 score representing what
// fraction of non-advert traffic in the store passes through this
// repeater as a relay hop. Issue #672 (Traffic axis only — bridge,
// coverage, and redundancy axes are deferred to follow-up work).
//
// Numerator: count of non-advert StoreTx entries indexed under pubkey in
// byPathHop.
//
// Denominator: total non-advert StoreTx entries in the store, summed from
// byPayloadType for all keys != payloadTypeAdvert.
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
	return s.GetRepeaterEnrichment([]string{pubkey}, 24)[pubkey].UsefulnessScore
}
