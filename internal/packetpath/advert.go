package packetpath

import "encoding/hex"

const (
	AdvertFlood           uint8 = 1
	AdvertDirectEmptyPath uint8 = 2
)

// AdvertRouteEvidence classifies one received wire frame, independently of a
// canonical transmission's metadata. Mesh::sendZeroHop writes path_len = 0;
// Mesh::onRecvPacket -> removeSelfFromPath -> retransmit can also leave an
// empty remaining path, preserving the hash-size flags. Neither encoding
// proves an original zero-hop send or RF distance. Transport
// routes carry four bytes before path_len (firmware Packet.cpp / Mesh.cpp).
// The fixed buffer bounds both work and allocation to a radio-sized frame.
func AdvertRouteEvidence(raw string) uint8 {
	var frame [256]byte
	if len(raw)%2 != 0 || len(raw) > len(frame)*2 {
		return 0
	}
	n, err := hex.Decode(frame[:], []byte(raw))
	if err != nil || n < 3 || (frame[0]>>2)&15 != 4 {
		return 0
	}
	route := int(frame[0] & 3)
	pathOffset := 1
	if IsTransportRoute(route) {
		pathOffset += 4
	}
	if n <= pathOffset+1 {
		return 0
	}
	path := frame[pathOffset]
	hashSize := int(path>>6) + 1
	pathBytes := int(path&63) * hashSize
	payloadBytes := n - pathOffset - 1 - pathBytes
	if hashSize == 4 || pathBytes > 64 || payloadBytes < 1 || payloadBytes > 184 {
		return 0
	}
	if route == RouteFlood || route == RouteTransportFlood {
		return AdvertFlood
	}
	if path&63 == 0 {
		return AdvertDirectEmptyPath
	}
	return 0
}

// AdvertKind describes the union of known evidence, not exclusive historical
// use: legacy observations may already have been overwritten before upgrade,
// or evidence writes may have failed. The API spelling "zero_hop" is retained
// for compatibility and denotes observed direct empty-path frames only.
func AdvertKind(evidence uint8) string {
	switch evidence & (AdvertFlood | AdvertDirectEmptyPath) {
	case AdvertFlood:
		return "flood"
	case AdvertDirectEmptyPath:
		return "zero_hop"
	case AdvertFlood | AdvertDirectEmptyPath:
		return "mixed"
	default:
		return "other"
	}
}
