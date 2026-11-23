package packetpath

import "strings"

// MinHeardHopBytes is the shortest path hop that may identify the node an
// observation heard. A 1-byte hop is refused: a node CoreScope does not know
// can share that byte with the one it does know, so even a prefix with a
// single known candidate can name the wrong transmitter.
const MinHeardHopBytes = 2

// HeardKey returns the key of the node whose own transmission an observation
// received off the air, following the firmware:
//
//   - Flood routes (TRANSPORT_FLOOD 0, FLOOD 1) build the path up as they
//     travel: a forwarder appends its own hash before retransmitting
//     (Mesh.cpp:349), so the last hop is the node that was heard.
//   - Direct routes (2, 3) carry the REMAINING route: a forwarder calls
//     removeSelfFromPath before retransmitting, so a non-empty direct path
//     never names the node that was heard.
//   - An empty path means the originator was heard. Only an ADVERT carries
//     the originator's pubkey in the clear, on a flood route or as a zero hop
//     on a direct route (Mesh::sendZeroHop).
//   - TRACE reuses the path bytes for per-hop SNR, so it never attributes.
//
// lastHop is the final path element as hex ("" for an empty path).
// advertPubkey is the advertised pubkey when the packet is an ADVERT, else "".
// The returned key is lower case; src is "rxlog" (from the path) or "advert".
func HeardKey(routeType int, payloadType byte, lastHop, advertPubkey string) (key, src string, ok bool) {
	if !PathBytesAreHops(payloadType) {
		return "", "", false
	}
	if lastHop != "" {
		if routeType != RouteTransportFlood && routeType != RouteFlood {
			return "", "", false
		}
		hop := strings.ToLower(strings.TrimSpace(lastHop))
		if len(hop)/2 < MinHeardHopBytes {
			return "", "", false
		}
		return hop, "rxlog", true
	}
	if advertPubkey != "" {
		return strings.ToLower(strings.TrimSpace(advertPubkey)), "advert", true
	}
	return "", "", false
}
