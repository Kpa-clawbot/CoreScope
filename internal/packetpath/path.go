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
