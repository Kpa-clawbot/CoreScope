package packetpath

import "encoding/hex"

const (
	AdvertFlood   uint8 = 1
	AdvertZeroHop uint8 = 2
)

// AdvertRouteEvidence classifies one received wire frame, independently of a
// canonical transmission's metadata. Mesh::sendZeroHop writes path_len = 0;
// hash-size flags with a zero count are not that same encoding. Transport
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
	if path == 0 {
		return AdvertZeroHop
	}
	return 0
}

// AdvertKind describes the union of known evidence, not exclusive historical
// use: legacy observations may already have been overwritten before upgrade.
func AdvertKind(evidence uint8) string {
	switch evidence & (AdvertFlood | AdvertZeroHop) {
	case AdvertFlood:
		return "flood"
	case AdvertZeroHop:
		return "zero_hop"
	case AdvertFlood | AdvertZeroHop:
		return "mixed"
	default:
		return "other"
	}
}
