// Package packetpath provides shared helpers for extracting path hops from
// raw MeshCore packet hex bytes.
package packetpath

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// DecodePathFromRawHex extracts the header path hops directly from raw hex bytes.
// This is the authoritative path that matches what's in raw_hex, as opposed to
// decoded.Path.Hops which may be overwritten for TRACE packets (issue #886).
//
// WARNING: This function returns the literal header path bytes regardless of
// payload type. For TRACE packets these bytes are SNR values, NOT hop hashes.
// Callers that may receive TRACE packets MUST check PathBytesAreHops(payloadType)
// first, or use the safer DecodeHopsForPayload wrapper.
func DecodePathFromRawHex(rawHex string) ([]string, error) {
	buf, err := hex.DecodeString(rawHex)
	if err != nil || len(buf) < 2 {
		return nil, fmt.Errorf("invalid or too-short hex")
	}

	headerByte := buf[0]
	offset := 1
	if IsTransportRoute(int(headerByte & 0x03)) {
		if len(buf) < offset+4 {
			return nil, fmt.Errorf("too short for transport codes")
		}
		offset += 4
	}
	if offset >= len(buf) {
		return nil, fmt.Errorf("too short for path byte")
	}

	pathByte := buf[offset]
	offset++

	hashSize := int(pathByte>>6) + 1
	hashCount := int(pathByte & 0x3F)

	hops := make([]string, 0, hashCount)
	for i := 0; i < hashCount; i++ {
		start := offset + i*hashSize
		end := start + hashSize
		if end > len(buf) {
			break
		}
		hops = append(hops, strings.ToUpper(hex.EncodeToString(buf[start:end])))
	}
	return hops, nil
}

// HashSize returns the path hash size (1-3 bytes) the originator chose, read
// from the path byte of raw_hex, or 0 when the packet does not carry one.
//
// A flood packet always carries it: Mesh::sendFlood sets the path byte to
// (size-1)<<6 before the first hop, so it holds even at 0 hops. A direct
// packet with no hops carries none: Mesh::sendZeroHop writes 0x00, and a
// direct path that has run out of hops has no hash left to size. That is the
// same rule cmd/server/decoder.go applies to path.hashSize. TRACE path bytes
// are SNR readings, not hashes. Only the first few bytes are parsed, so this
// is cheap on hot paths.
func HashSize(rawHex string) int {
	header, ok := hexByteAt(rawHex, 0)
	if !ok || !PathBytesAreHops(header>>2&0x0F) {
		return 0
	}
	routeType, pathByte, ok := PathByte(rawHex)
	if !ok {
		return 0
	}
	if pathByte&0x3F == 0 && (routeType == RouteDirect || routeType == RouteTransportDirect) {
		return 0
	}
	size := int(pathByte>>6) + 1
	if size > 3 {
		return 0 // 0b11 is reserved; firmware rejects sizes above 3
	}
	return size
}

// PathByte returns a packet's route type and path byte (hash size in the top
// two bits, hop count in the low six), read from raw_hex: the header is byte
// 0, transport routes carry four transport-code bytes after it, then comes
// the path byte (firmware docs/packet_format.md). ok is false when raw_hex is
// too short or not hex there. Only those bytes are parsed, so this is cheap on
// hot paths.
func PathByte(rawHex string) (routeType int, pathByte byte, ok bool) {
	header, ok := hexByteAt(rawHex, 0)
	if !ok {
		return 0, 0, false
	}
	routeType = int(header & 0x03)
	offset := 1
	if IsTransportRoute(routeType) {
		offset += 4
	}
	pathByte, ok = hexByteAt(rawHex, offset)
	return routeType, pathByte, ok
}

// hexByteAt decodes the byte at byte index i of a hex string, without
// allocating: PathByte runs per advert on the server's recompute paths.
func hexByteAt(s string, i int) (byte, bool) {
	if i < 0 || len(s) < 2*i+2 {
		return 0, false
	}
	hi, ok1 := hexNibble(s[2*i])
	lo, ok2 := hexNibble(s[2*i+1])
	if !ok1 || !ok2 {
		return 0, false
	}
	return hi<<4 | lo, true
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// DecodeHopsForPayload returns the header path hops only when the payload type's
// header bytes are actually route hops (i.e. PathBytesAreHops(payloadType) is true).
// For TRACE packets it returns (nil, ErrPayloadHasNoHeaderHops) so the caller is
// forced to source hops from the decoded payload instead.
//
// Prefer this over DecodePathFromRawHex when the payload type is known.
func DecodeHopsForPayload(rawHex string, payloadType byte) ([]string, error) {
	if !PathBytesAreHops(payloadType) {
		return nil, ErrPayloadHasNoHeaderHops
	}
	return DecodePathFromRawHex(rawHex)
}

// ErrPayloadHasNoHeaderHops is returned by DecodeHopsForPayload when the
// payload type repurposes the raw_hex header path bytes (e.g. TRACE → SNR values).
var ErrPayloadHasNoHeaderHops = errPayloadHasNoHeaderHops{}

type errPayloadHasNoHeaderHops struct{}

func (errPayloadHasNoHeaderHops) Error() string {
	return "payload type repurposes header path bytes; source hops from decoded payload"
}
