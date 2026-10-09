package packetpath

import (
	"crypto/sha256"
	"encoding/hex"
)

// ContentHash preserves CoreScope packet identity. This is the existing server
// algorithm shared by runtime and offline fixture preparation. Transport routes
// and TRACE uint16 path_len were checked against MeshCore a366955 Packet.cpp/Packet.h.
func ContentHash(rawHex string) string {
	buf, err := hex.DecodeString(rawHex)
	if err != nil || len(buf) < 2 {
		if len(rawHex) >= 16 {
			return rawHex[:16]
		}
		return rawHex
	}

	headerByte := buf[0]
	offset := 1
	if IsTransportRoute(int(headerByte & 0x03)) {
		offset += 4
	}
	if offset >= len(buf) {
		if len(rawHex) >= 16 {
			return rawHex[:16]
		}
		return rawHex
	}
	pathByte := buf[offset]
	offset++
	hashSize := int((pathByte>>6)&0x3) + 1
	hashCount := int(pathByte & 0x3F)
	pathBytes := hashSize * hashCount

	payloadStart := offset + pathBytes
	if payloadStart > len(buf) {
		if len(rawHex) >= 16 {
			return rawHex[:16]
		}
		return rawHex
	}

	payload := buf[payloadStart:]

	// Hash payload-type byte only (bits 2-5 of header), not the full header.
	// Firmware: SHA256(payload_type + [path_len for TRACE] + payload)
	// Using the full header caused different hashes for the same logical packet
	// when route type or version bits differed. See issue #786.
	payloadType := (headerByte >> 2) & 0x0F
	toHash := []byte{payloadType}
	if int(payloadType) == 0x09 {
		// Firmware uses uint16_t path_len (2 bytes, little-endian)
		toHash = append(toHash, pathByte, 0x00)
	}
	toHash = append(toHash, payload...)

	h := sha256.Sum256(toHash)
	return hex.EncodeToString(h[:])[:16]
}
