package main

// Tests for issue #1279 P2 item 5: ingestor RAW_CUSTOM exposure.

import (
	"strings"
	"testing"
)

func TestDecodeRawCustomExposesLengthAndTag(t *testing.T) {
	// header = (1<<6)|(0x0F<<2)|1 = 0x7D ; path byte = 0x00 ; payload = A5 DE AD BE EF
	hexStr := "7D00A5DEADBEEF"
	pkt, err := DecodePacket(hexStr, nil, false)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pkt.Payload.Type != "RAW_CUSTOM" {
		t.Fatalf("payload type = %q, want RAW_CUSTOM", pkt.Payload.Type)
	}
	if pkt.Payload.RawLength == nil || *pkt.Payload.RawLength != 5 {
		got := -1
		if pkt.Payload.RawLength != nil {
			got = *pkt.Payload.RawLength
		}
		t.Errorf("RawLength=%d, want 5", got)
	}
	if !strings.EqualFold(pkt.Payload.FirstByteTag, "A5") {
		t.Errorf("FirstByteTag=%q, want A5", pkt.Payload.FirstByteTag)
	}
}
