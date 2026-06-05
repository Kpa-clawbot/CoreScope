package main

// relay_airtime_share.go — issue #1359 stub (RED commit).
// Real implementation lands in the next commit.

// distinctRelayCount returns the number of distinct repeater pubkeys that
// forwarded `tx`, unioned across ALL observations of that transmission_id.
// Stub returns 0 so the test compiles and fails on the assertion.
func (s *PacketStore) distinctRelayCount(tx *StoreTx) int {
	return 0
}

// computeRelayAirtimeShare returns the relay-airtime-share aggregation.
// Stub returns an empty row set so the test compiles and fails on the
// "rows missing ADVERT bucket" assertion.
func (s *PacketStore) computeRelayAirtimeShare(window TimeWindow) map[string]interface{} {
	return map[string]interface{}{
		"rows":        []map[string]interface{}{},
		"total_count": 0,
		"total_score": 0,
		"window":      "",
		"cached":      false,
	}
}

// GetRelayAirtimeShareWithWindow is the cached wrapper. Stub passes through.
func (s *PacketStore) GetRelayAirtimeShareWithWindow(window TimeWindow) map[string]interface{} {
	return s.computeRelayAirtimeShare(window)
}
