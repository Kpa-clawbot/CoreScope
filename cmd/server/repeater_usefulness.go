package main

// GetRepeaterUsefulnessScore returns a 0..1 score representing what
// fraction of non-advert traffic in the store's window passes through
// this repeater. Issue #672 (Traffic axis only — bridge/coverage/
// redundancy axes are deferred).
//
// Numerator:  count of non-advert packets where pubkey appears as a
//             path hop (from byPathHop index, filtering ADVERTs).
// Denominator: count of non-advert packets in the store
//             (sum of byPayloadType for all keys != payloadTypeAdvert).
//
// Returns 0 when there is no non-advert traffic. Stub: always 0.
func (s *PacketStore) GetRepeaterUsefulnessScore(pubkey string) float64 {
	return 0
}
