package main

// Tests for issue #1279 P0+P1 decoder additions (server-side).
//
// Wire-vector citations identical to the ingestor counterpart:
//   - GRP_DATA outer: firmware/src/helpers/BaseChatMesh.cpp:500
//   - MULTIPART byte0: firmware/src/Mesh.cpp:289
//   - MULTIPART ACK inner: firmware/src/Mesh.cpp:292-307
//   - CONTROL byte0 flags: firmware/src/Mesh.cpp:69 + Mesh.cpp:609
//   - advertRole label rules: firmware/src/helpers/AdvertDataHelpers.h:7-12

import "testing"

func TestDecodeGrpDataEnvelopeServer(t *testing.T) {
	// Server-side decoder has no channel keys: envelope only.
	buf := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11}
	p := decodeGrpData(buf)
	if p.Type != "GRP_DATA" {
		t.Fatalf("type=%q want GRP_DATA", p.Type)
	}
	if p.ChannelHash != 0xAA {
		t.Errorf("channelHash=%d want 170", p.ChannelHash)
	}
	if p.ChannelHashHex != "AA" {
		t.Errorf("channelHashHex=%q want AA", p.ChannelHashHex)
	}
	if p.MAC != "bbcc" {
		t.Errorf("mac=%q want bbcc", p.MAC)
	}
	if p.EncryptedData != "ddeeff11" {
		t.Errorf("encryptedData=%q want ddeeff11", p.EncryptedData)
	}
}

func TestDecodeMultipartAckServer(t *testing.T) {
	buf := []byte{0x33, 0xEF, 0xBE, 0xAD, 0xDE}
	p := decodeMultipart(buf)
	if p.Type != "MULTIPART" {
		t.Fatalf("type=%q want MULTIPART", p.Type)
	}
	if p.Remaining == nil || *p.Remaining != 3 {
		t.Errorf("remaining=%v want 3", p.Remaining)
	}
	if p.InnerType == nil || *p.InnerType != 0x03 {
		t.Errorf("innerType=%v want 3", p.InnerType)
	}
	if p.InnerTypeName != "ACK" {
		t.Errorf("innerTypeName=%q want ACK", p.InnerTypeName)
	}
	if p.InnerAckCrc != "deadbeef" {
		t.Errorf("innerAckCrc=%q want deadbeef", p.InnerAckCrc)
	}
}

func TestDecodeMultipartNonAckServer(t *testing.T) {
	buf := []byte{0x22, 0x01, 0x02, 0x03}
	p := decodeMultipart(buf)
	if p.Remaining == nil || *p.Remaining != 2 {
		t.Errorf("remaining=%v want 2", p.Remaining)
	}
	if p.InnerType == nil || *p.InnerType != 0x02 {
		t.Errorf("innerType=%v want 2", p.InnerType)
	}
	if p.InnerTypeName != "TXT_MSG" {
		t.Errorf("innerTypeName=%q want TXT_MSG", p.InnerTypeName)
	}
	if p.InnerPayload != "010203" {
		t.Errorf("innerPayload=%q want 010203", p.InnerPayload)
	}
}

func TestAdvertRoleLabelsRawTypeServer(t *testing.T) {
	cases := []struct {
		typ  int
		want string
	}{
		{0, "none"},
		{1, "companion"},
		{2, "repeater"},
		{3, "room"},
		{4, "sensor"},
		{5, "type-5"},
		{15, "type-15"},
	}
	for _, tc := range cases {
		got := advertRole(&AdvertFlags{Type: tc.typ, Repeater: tc.typ == 2, Room: tc.typ == 3, Sensor: tc.typ == 4})
		if got != tc.want {
			t.Errorf("advertRole(type=%d) = %q, want %q", tc.typ, got, tc.want)
		}
	}
}

func TestDecodeControlZeroHopServer(t *testing.T) {
	buf := []byte{0x81, 0xAA, 0xBB, 0xCC}
	p := decodeControl(buf)
	if p.Type != "CONTROL" {
		t.Fatalf("type=%q want CONTROL", p.Type)
	}
	if p.CtrlFlags != "81" {
		t.Errorf("ctrlFlags=%q want 81", p.CtrlFlags)
	}
	if p.CtrlZeroHop == nil || !*p.CtrlZeroHop {
		t.Errorf("ctrlZeroHop=%v want true", p.CtrlZeroHop)
	}
	if p.CtrlLength == nil || *p.CtrlLength != 4 {
		t.Errorf("ctrlLength=%v want 4", p.CtrlLength)
	}
}

func TestDecodeControlMultiHopServer(t *testing.T) {
	buf := []byte{0x01, 0x42}
	p := decodeControl(buf)
	if p.CtrlFlags != "01" {
		t.Errorf("ctrlFlags=%q want 01", p.CtrlFlags)
	}
	if p.CtrlZeroHop == nil || *p.CtrlZeroHop {
		t.Errorf("ctrlZeroHop=%v want false", p.CtrlZeroHop)
	}
	if p.CtrlLength == nil || *p.CtrlLength != 2 {
		t.Errorf("ctrlLength=%v want 2", p.CtrlLength)
	}
}
