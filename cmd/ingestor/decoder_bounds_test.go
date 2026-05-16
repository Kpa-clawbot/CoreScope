package main

import (
	"encoding/hex"
	"strings"
	"testing"
)

// --- Issue #1211 round-1 protocol-correctness regressions ---
// See cmd/server/decoder_bounds_test.go for full firmware citations
// (firmware/src/Packet.cpp:13-18, firmware/src/MeshCore.h:19-21).

// pathByte=0xF6 → hash_size=4 (reserved), hash_count=54.
// Buffer holds all 216 claimed bytes so the OOB guard does NOT catch.
func TestDecodePacketRejectsReservedHashSize_Issue1211(t *testing.T) {
	raw := "12F6" + strings.Repeat("AB", 216) + strings.Repeat("CD", 8)
	pkt, err := DecodePacket(raw, nil, false)
	if err == nil {
		t.Fatalf("expected error rejecting reserved hash_size=4 (firmware Packet.cpp:13-18); got nil, pkt=%+v", pkt)
	}
	if !strings.Contains(err.Error(), "path") {
		t.Errorf("error should mention path; got %q", err)
	}
}

// pathByte=0xBF → hash_size=3, hash_count=63, total=189 > MAX_PATH_SIZE=64.
func TestDecodePacketRejectsOversizedPath_Issue1211(t *testing.T) {
	raw := "12BF" + strings.Repeat("AB", 189) + strings.Repeat("CD", 8)
	pkt, err := DecodePacket(raw, nil, false)
	if err == nil {
		t.Fatalf("expected error rejecting hash_count*hash_size > 64; got nil, pkt=%+v", pkt)
	}
}

// Payload > MAX_PACKET_PAYLOAD (184).
func TestDecodePacketRejectsOversizedPayload_Issue1211(t *testing.T) {
	raw := "1200" + strings.Repeat("AA", 200)
	pkt, err := DecodePacket(raw, nil, false)
	if err == nil {
		t.Fatalf("expected error rejecting payload > MAX_PACKET_PAYLOAD=184 (firmware MeshCore.h:19); got nil, pkt=%+v", pkt)
	}
	if !strings.Contains(err.Error(), "payload") {
		t.Errorf("error should mention payload; got %q", err)
	}
}

func TestDecodePath_RejectsReservedHashSize_Issue1211(t *testing.T) {
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

// Kent #1 — pin tautological assertion: error MUST mention "path length"
// AND "exceeds buffer", not just non-nil. Uses firmware-valid pathByte
// that exhausts a small buffer, so the OOB guard fires (not validity).
func TestDecodePacketBoundsFromWireErrorPhrasing_Issue1211(t *testing.T) {
	raw := "120A" + strings.Repeat("AA", 5)
	_, err := DecodePacket(raw, nil, false)
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

var _ = hex.EncodeToString
