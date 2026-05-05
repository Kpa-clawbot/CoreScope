package main

import (
	"strings"
	"time"
)

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

// payloadTypeAdvert is the MeshCore payload type for ADVERT packets.
// See firmware/src/Mesh.h. Adverts are NOT considered relay activity:
// a repeater that only sends adverts proves it is alive, not that it
// is forwarding traffic for other nodes.
const payloadTypeAdvert = 4

// GetRepeaterRelayInfo returns relay-activity information for a node by
// scanning the byPathHop index for non-advert packets that name the
// pubkey as a hop. It computes the most recent appearance timestamp and
// whether that timestamp falls within windowHours.
//
// Cost: O(N) over the indexed entries for `pubkey`. The byPathHop index
// is bounded by store eviction; on real data this is small per-node.
func (s *PacketStore) GetRepeaterRelayInfo(pubkey string, windowHours float64) RepeaterRelayInfo {
	info := RepeaterRelayInfo{WindowHours: windowHours}
	if pubkey == "" {
		return info
	}
	key := strings.ToLower(pubkey)

	s.mu.RLock()
	txList := s.byPathHop[key]
	// Copy only the timestamps + payload types we need so we can release
	// the read lock before doing string compares (cheap but principled).
	type entry struct {
		ts string
		pt int
	}
	scratch := make([]entry, 0, len(txList))
	for _, tx := range txList {
		if tx == nil {
			continue
		}
		pt := -1
		if tx.PayloadType != nil {
			pt = *tx.PayloadType
		}
		scratch = append(scratch, entry{ts: tx.FirstSeen, pt: pt})
	}
	s.mu.RUnlock()

	var latest string
	for _, e := range scratch {
		if e.pt == payloadTypeAdvert {
			continue
		}
		if e.ts == "" {
			continue
		}
		if e.ts > latest {
			latest = e.ts
		}
	}
	if latest == "" {
		return info
	}
	info.LastRelayed = latest

	if windowHours > 0 {
		if t, err := time.Parse(time.RFC3339, latest); err == nil {
			cutoff := time.Now().UTC().Add(-time.Duration(windowHours * float64(time.Hour)))
			if t.After(cutoff) {
				info.RelayActive = true
			}
		} else if t, err := time.Parse("2006-01-02T15:04:05.000Z", latest); err == nil {
			cutoff := time.Now().UTC().Add(-time.Duration(windowHours * float64(time.Hour)))
			if t.After(cutoff) {
				info.RelayActive = true
			}
		}
	}
	return info
}
