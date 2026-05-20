package main

// Tests for issue #1279 P2 items:
//   - Item 1: payloadTypeNames map must include ALL 13 firmware payload types.
//   - Item 5: RAW_CUSTOM (0x0F) decoder must expose rawLength + firstByteTag.
//
// Firmware refs:
//   - firmware/src/Packet.h:19-32 (PAYLOAD_TYPE_*) — 0..0xB plus 0xF (RAW_CUSTOM)
//   - firmware/src/Mesh.cpp:577 (createRawData) — application-defined payload

import (
	"strings"
	"testing"
)

func TestPayloadTypeNamesAll13(t *testing.T) {
	// Firmware-defined: 0..9, 0x0A, 0x0B, 0x0F — 13 total.
	want := map[int]string{
		0x00: "REQ", 0x01: "RESPONSE", 0x02: "TXT_MSG", 0x03: "ACK",
		0x04: "ADVERT", 0x05: "GRP_TXT", 0x06: "GRP_DATA", 0x07: "ANON_REQ",
		0x08: "PATH", 0x09: "TRACE", 0x0A: "MULTIPART", 0x0B: "CONTROL",
		0x0F: "RAW_CUSTOM",
	}
	if len(payloadTypeNames) != len(want) {
		t.Errorf("payloadTypeNames has %d entries, want %d", len(payloadTypeNames), len(want))
	}
	for code, name := range want {
		got, ok := payloadTypeNames[code]
		if !ok {
			t.Errorf("payloadTypeNames missing 0x%02X (%s)", code, name)
			continue
		}
		if got != name {
			t.Errorf("payloadTypeNames[0x%02X] = %q, want %q", code, got, name)
		}
	}
}

func TestDecodeRawCustomExposesLengthAndTag(t *testing.T) {
	// Build a RAW_CUSTOM packet: header byte = (route<<6 | type<<2 | ver),
	// type=0x0F. Route FLOOD (1), version 1: (1<<6)|(0x0F<<2)|1 = 0x7D.
	// Path byte: 0 hops, hash_size=1 → upper bits 0, lower 0 → 0x00.
	// Payload: first byte tag 0xA5, then arbitrary data.
	hexStr := "7D00A5DEADBEEF"
	pkt, err := DecodePacket(hexStr, false)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pkt.Payload.Type != "RAW_CUSTOM" {
		t.Fatalf("payload type = %q, want RAW_CUSTOM", pkt.Payload.Type)
	}
	if pkt.Payload.RawLength == nil {
		t.Fatal("RawLength should be set for RAW_CUSTOM")
	}
	// payload = 4 bytes (A5 DE AD BE EF) — wait, A5 DE AD BE EF = 5 bytes.
	if *pkt.Payload.RawLength != 5 {
		t.Errorf("RawLength=%d, want 5", *pkt.Payload.RawLength)
	}
	if !strings.EqualFold(pkt.Payload.FirstByteTag, "A5") {
		t.Errorf("FirstByteTag=%q, want A5", pkt.Payload.FirstByteTag)
	}
}
