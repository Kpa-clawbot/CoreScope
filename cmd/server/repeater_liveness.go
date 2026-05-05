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
	// RelayCount1h is the count of distinct non-advert packets where this
	// pubkey appeared as a relay hop in the last 1 hour.
	RelayCount1h int `json:"relayCount1h"`
	// RelayCount24h is the count of distinct non-advert packets where this
	// pubkey appeared as a relay hop in the last 24 hours.
	RelayCount24h int `json:"relayCount24h"`
}

// payloadTypeAdvert is the MeshCore payload type for ADVERT packets.
// See firmware/src/Mesh.h. Adverts are NOT considered relay activity:
// a repeater that only sends adverts proves it is alive, not that it
// is forwarding traffic for other nodes.
const payloadTypeAdvert = 4

// parseRelayTS attempts to parse a packet first-seen timestamp using the
// formats CoreScope writes in practice. Returns zero time and false on
// failure. Accepted (in order):
//   - RFC3339Nano  — Go's default UTC marshal output
//   - RFC3339      — second-precision ISO-8601 with offset
//   - "2006-01-02T15:04:05.000Z" — millisecond-precision Z form used by ingest
func parseRelayTS(ts string) (time.Time, bool) {
	if ts == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02T15:04:05.000Z", ts); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// GetRepeaterRelayInfo returns relay-activity information for a node by
// scanning the byPathHop index for non-advert packets that name the
// pubkey as a hop. It computes the most recent appearance timestamp,
// 1h/24h hop counts, and whether the latest appearance falls within
// windowHours.
//
// Cost: O(N) over the indexed entries for `pubkey`. The byPathHop index
// is bounded by store eviction; on real data this is small per-node.
//
// Note on self-as-source: byPathHop is keyed by every hop in a packet's
// resolved path, including the originator. For ADVERT packets that's the
// node itself, which is filtered above by the payloadTypeAdvert check.
// For non-advert packets a node "originates" rather than "relays" only
// when it is the source; we don't currently have a clean signal for that
// distinction, so the count here is *path-hop appearances in non-advert
// packets*. In practice for a repeater nearly all such appearances are
// relay hops (the firmware doesn't originate user traffic), so this is
// the right approximation for issue #662.
func (s *PacketStore) GetRepeaterRelayInfo(pubkey string, windowHours float64) RepeaterRelayInfo {
	info := RepeaterRelayInfo{WindowHours: windowHours}
	if pubkey == "" {
		return info
	}
	key := strings.ToLower(pubkey)

	s.mu.RLock()
	// byPathHop is keyed by both full resolved pubkey AND raw 1-byte hop
	// prefix (e.g. "a3"). Many ingested non-advert packets only carry the
	// raw hop on the wire — resolution to the full pubkey happens later
	// via neighbor affinity. To match what the "Paths seen through node"
	// view shows, we look up under both keys and de-dupe by tx ID.
	//
	// The 1-byte prefix lookup CAN over-count when multiple nodes share
	// the same first byte. This trades a possible over-count for clearly
	// false zeros (issue #662). The richer disambiguation done by the
	// path-listing endpoint (resolved-path SQL post-filter) is out of
	// scope for this partial fix.
	txList := s.byPathHop[key]
	var prefixList []*StoreTx
	if len(key) >= 2 {
		prefix := key[:2]
		if prefix != key {
			prefixList = s.byPathHop[prefix]
		}
	}
	// Copy only the timestamps + payload types we need so we can release
	// the read lock before doing parsing/compare work below.
	type entry struct {
		ts string
		pt int
	}
	scratch := make([]entry, 0, len(txList)+len(prefixList))
	seen := make(map[int]bool, len(txList)+len(prefixList))
	collect := func(list []*StoreTx) {
		for _, tx := range list {
			if tx == nil {
				continue
			}
			if seen[tx.ID] {
				continue
			}
			seen[tx.ID] = true
			pt := -1
			if tx.PayloadType != nil {
				pt = *tx.PayloadType
			}
			scratch = append(scratch, entry{ts: tx.FirstSeen, pt: pt})
		}
	}
	collect(txList)
	collect(prefixList)
	s.mu.RUnlock()

	now := time.Now().UTC()
	cutoff1h := now.Add(-1 * time.Hour)
	cutoff24h := now.Add(-24 * time.Hour)

	var latest time.Time
	var latestRaw string
	for _, e := range scratch {
		// Self-originated adverts are not relay activity (see header comment).
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
	if latestRaw == "" {
		return info
	}
	info.LastRelayed = latestRaw

	if windowHours > 0 {
		cutoff := now.Add(-time.Duration(windowHours * float64(time.Hour)))
		if latest.After(cutoff) {
			info.RelayActive = true
		}
	}
	return info
}
