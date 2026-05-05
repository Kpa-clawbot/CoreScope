package main

// RepeaterRelayInfo describes whether a repeater has been observed
// relaying traffic (appearing as a path hop in non-advert packets) and
// when. This is distinct from advert-based liveness (last_seen / last_heard),
// which only proves the repeater can transmit its own adverts.
//
// See issue #662.
type RepeaterRelayInfo struct {
	// LastRelayed is the ISO-8601 timestamp of the most recent non-advert
	// packet where this pubkey appeared as a relay hop. Empty if never.
	LastRelayed string `json:"lastRelayed,omitempty"`
	// RelayActive is true if LastRelayed falls within the configured
	// activity window (default 24h).
	RelayActive bool `json:"relayActive"`
	// WindowHours is the active-window threshold actually used.
	WindowHours float64 `json:"windowHours"`
}

// GetRepeaterRelayInfo returns relay-activity information for a repeater
// (or any node whose pubkey may appear as a path hop). Currently a stub
// pending issue #662 implementation — returns zero value so callers and
// tests compile and exercise the assertion path.
func (s *PacketStore) GetRepeaterRelayInfo(pubkey string, windowHours float64) RepeaterRelayInfo {
	_ = pubkey
	return RepeaterRelayInfo{WindowHours: windowHours}
}
