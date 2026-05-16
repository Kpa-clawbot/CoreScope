package main

import (
	"encoding/hex"
	"strings"
	"testing"
)

// --- Issue #1211 round-1 protocol-correctness regressions ---
//
// Background: the round-0 PR added a bounds check on `offset > len(buf)` AFTER
// decodePath returned, but did NOT enforce the firmware-level validity rules
// for pathByte:
//
//   firmware/src/Packet.cpp:13-18 — isValidPathLen():
//     hash_count = path_len & 63
//     hash_size  = (path_len >> 6) + 1
//     hash_size == 4 is RESERVED — invalid
//     hash_count * hash_size MUST be <= MAX_PATH_SIZE (64)  [MeshCore.h:21]
//
//   firmware/src/MeshCore.h:19 — MAX_PACKET_PAYLOAD = 184
//
// A malformed pathByte=0xF6 (hash_size=4, hash_count=54) inside a buffer LARGE
// ENOUGH to hold 216 path bytes would slip past the round-0 bounds check and
// pollute analytics with a bogus "decoded" packet. Similarly, payloads
// exceeding MAX_PACKET_PAYLOAD should be rejected — firmware would never
// produce them on the wire.

// TestDecodePacketRejectsReservedHashSize_Issue1211 — pathByte=0xF6:
// hash_size = (0xF6 >> 6) + 1 = 3 + 1 = 4   ← RESERVED per firmware
// hash_count = 0xF6 & 0x3F = 54
// total path bytes = 4 * 54 = 216
// We provide a buffer with 216 path bytes available, so the round-0 OOB
// guard PASSES — only the firmware-derived isValidPathLen check catches this.
func TestDecodePacketRejectsReservedHashSize_Issue1211(t *testing.T) {
	// header (1) + pathByte (1) + 216 path bytes + 8-byte payload = 226 bytes
	raw := "12F6" + strings.Repeat("AB", 216) + strings.Repeat("CD", 8)
	pkt, err := DecodePacket(raw, false)
	if err == nil {
		t.Fatalf("expected error rejecting reserved hash_size=4 (firmware Packet.cpp:13-18); got nil, pkt=%+v", pkt)
	}
	if !strings.Contains(err.Error(), "path") {
		t.Errorf("error should mention path; got %q", err)
	}
}

// TestDecodePacketRejectsOversizedPath_Issue1211 — pathByte=0xBF:
// hash_size = (0xBF >> 6) + 1 = 2 + 1 = 3
// hash_count = 0xBF & 0x3F = 63
// total path bytes = 3 * 63 = 189  > MAX_PATH_SIZE (64)  ← INVALID per firmware
// Buffer holds all 189 path bytes — only the firmware check rejects.
func TestDecodePacketRejectsOversizedPath_Issue1211(t *testing.T) {
	raw := "12BF" + strings.Repeat("AB", 189) + strings.Repeat("CD", 8)
	pkt, err := DecodePacket(raw, false)
	if err == nil {
		t.Fatalf("expected error rejecting hash_count*hash_size > 64 (firmware Packet.cpp:13-18); got nil, pkt=%+v", pkt)
	}
}

// TestDecodePacketRejectsOversizedPayload_Issue1211 — payload exceeds
// MAX_PACKET_PAYLOAD (184). Firmware (MeshCore.h:19) cannot emit such a
// packet on the wire; the decoder should drop it rather than emit a bogus
// "successfully decoded" record.
func TestDecodePacketRejectsOversizedPayload_Issue1211(t *testing.T) {
	// hash_size=1, hash_count=0 → no path bytes, then 200-byte payload
	// header=0x12 (DIRECT/ADVERT), pathByte=0x00 → no hops, then payload
	// payload length 200 > 184 → must reject
	raw := "1200" + strings.Repeat("AA", 200)
	pkt, err := DecodePacket(raw, false)
	if err == nil {
		t.Fatalf("expected error rejecting payload > MAX_PACKET_PAYLOAD=184 (firmware MeshCore.h:19); got nil, pkt=%+v", pkt)
	}
	if !strings.Contains(err.Error(), "payload") {
		t.Errorf("error should mention payload; got %q", err)
	}
}

// TestDecodePath_RejectsReservedHashSize_Issue1211 — adversarial M1: push the
// invalid-path check INTO decodePath so callers can't accidentally rely on the
// downstream OOB guard. Direct unit test on decodePath.
func TestDecodePath_RejectsReservedHashSize_Issue1211(t *testing.T) {
	// Plenty of buffer — 216 bytes — so the *test* doesn't hide the check
	// behind an OOB failure.
	buf := make([]byte, 216)
	for i := range buf {
		buf[i] = 0xAB
	}
	_, _, err := decodePath(0xF6, buf, 0)
	if err == nil {
		t.Fatalf("decodePath should reject pathByte=0xF6 (hash_size=4 reserved); got nil err")
	}
}

func TestDecodePath_RejectsOversizedPath_Issue1211(t *testing.T) {
	buf := make([]byte, 189)
	_, _, err := decodePath(0xBF, buf, 0)
	if err == nil {
		t.Fatalf("decodePath should reject hash_count*hash_size=189 > MAX_PATH_SIZE=64; got nil err")
	}
}

func TestDecodePath_AcceptsValidEncodings_Issue1211(t *testing.T) {
	// hash_size=1, hash_count=5 → 5 path bytes — valid per firmware.
	buf := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	path, consumed, err := decodePath(0x05, buf, 0)
	if err != nil {
		t.Fatalf("decodePath rejected valid encoding: %v", err)
	}
	if consumed != 5 {
		t.Errorf("consumed=%d, want 5", consumed)
	}
	if path.HashCount != 5 || path.HashSize != 1 {
		t.Errorf("decode wrong: hashCount=%d hashSize=%d", path.HashCount, path.HashSize)
	}
}

// Pin the round-0 tautological assertion. Specific error phrasing required.
// (Kent #1) — `TestDecodePacketBoundsFromWire_Issue1211` used to assert only
// `err != nil`; a generic recover would have passed. Now must contain
// "path length" AND "exceeds buffer".
func TestDecodePacketBoundsFromWireErrorPhrasing_Issue1211(t *testing.T) {
	raw := "12F6" + strings.Repeat("AA", 13)
	_, err := DecodePacket(raw, false)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "path length") {
		t.Errorf("error missing 'path length'; got %q", err)
	}
	if !strings.Contains(err.Error(), "exceeds buffer") {
		t.Errorf("error missing 'exceeds buffer'; got %q", err)
	}
}

// silence unused import in case of future trimming
var _ = hex.EncodeToString
