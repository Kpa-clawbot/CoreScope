package main

// Tests for issue #1694 — server-side decoder parity with the ingestor's
// firmware-1.16.0 extended ACK support (issue #1610). Wire vectors mirror
// the ingestor's tests so both decoders agree byte-for-byte.
//
//   - decodeAck:       firmware/src/helpers/BaseChatMesh.cpp:218-234
//   - decodeMultipart: firmware/src/Mesh.cpp:287-310

import "testing"

func TestDecodeAckExtended(t *testing.T) {
	tests := []struct {
		name       string
		buf        []byte
		wantLen    int
		wantAttPtr bool
		wantAtt    int
		wantRndPtr bool
		wantRnd    int
	}{
		{
			name:    "legacy 4-byte ACK (CRC only)",
			buf:     []byte{0xEF, 0xBE, 0xAD, 0xDE},
			wantLen: 4,
		},
		{
			name:       "5-byte ACK (CRC + attempt)",
			buf:        []byte{0xEF, 0xBE, 0xAD, 0xDE, 0x07},
			wantLen:    5,
			wantAttPtr: true,
			wantAtt:    7,
		},
		{
			name:       "6-byte ACK (CRC + attempt + rand)",
			buf:        []byte{0xEF, 0xBE, 0xAD, 0xDE, 0x07, 0x42},
			wantLen:    6,
			wantAttPtr: true,
			wantAtt:    7,
			wantRndPtr: true,
			wantRnd:    0x42,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := decodeAck(tc.buf)
			if p.Type != "ACK" {
				t.Fatalf("type=%q want ACK", p.Type)
			}
			if p.AckLen == nil {
				t.Fatalf("AckLen=nil want %d", tc.wantLen)
			}
			if *p.AckLen != tc.wantLen {
				t.Errorf("AckLen=%d want %d", *p.AckLen, tc.wantLen)
			}
			if tc.wantAttPtr {
				if p.AckAttempt == nil {
					t.Errorf("AckAttempt=nil want %d", tc.wantAtt)
				} else if *p.AckAttempt != tc.wantAtt {
					t.Errorf("AckAttempt=%d want %d", *p.AckAttempt, tc.wantAtt)
				}
			} else if p.AckAttempt != nil {
				t.Errorf("AckAttempt=%d want nil", *p.AckAttempt)
			}
			if tc.wantRndPtr {
				if p.AckRand == nil {
					t.Errorf("AckRand=nil want %d", tc.wantRnd)
				} else if *p.AckRand != tc.wantRnd {
					t.Errorf("AckRand=%d want %d", *p.AckRand, tc.wantRnd)
				}
			} else if p.AckRand != nil {
				t.Errorf("AckRand=%d want nil", *p.AckRand)
			}
		})
	}
}

func TestDecodeMultipartAckExtendedInner(t *testing.T) {
	// byte0 = (remaining<<4)|inner_type = (3<<4)|0x03 = 0x33
	// inner ACK = CRC(deadbeef LE) + attempt(0x07) + rand(0x42) = 6 bytes
	// total buf = 1 + 6 = 7 bytes.
	buf := []byte{0x33, 0xEF, 0xBE, 0xAD, 0xDE, 0x07, 0x42}
	p := decodeMultipart(buf)
	if p.InnerAckCrc != "deadbeef" {
		t.Fatalf("InnerAckCrc=%q want deadbeef", p.InnerAckCrc)
	}
	if p.InnerAckLen == nil || *p.InnerAckLen != 6 {
		t.Errorf("InnerAckLen=%v want 6", p.InnerAckLen)
	}
	if p.InnerAckAttempt == nil || *p.InnerAckAttempt != 7 {
		t.Errorf("InnerAckAttempt=%v want 7", p.InnerAckAttempt)
	}
	if p.InnerAckRand == nil || *p.InnerAckRand != 0x42 {
		t.Errorf("InnerAckRand=%v want 0x42", p.InnerAckRand)
	}
}
