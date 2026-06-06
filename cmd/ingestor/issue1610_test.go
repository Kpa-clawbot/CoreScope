package main

// Tests for issue #1610: firmware 1.16.0 extended ACK support.
//
// Wire vectors are synthetic, derived by hand from the firmware spec:
//   - Variable-length ACK on the wire:
//       firmware/src/Mesh.cpp:545-575 createAck/createMultiAck (commit f6e6fdaa)
//   - 5-byte ACK = 4-byte truncated sha256 CRC + 1-byte attempt counter:
//       firmware/src/helpers/BaseChatMesh.cpp:218-232 (commit f6e6fdaa)
//   - 6-byte ACK = 5-byte + 1-byte RNG (so identical attempts get unique hash):
//       firmware/src/helpers/BaseChatMesh.cpp:219-234 (commit a130a95a)
//   - Multipart ACK inner blob: firmware/src/Mesh.cpp:292-307 — byte0 then
//       ack bytes, payload_len = 1 + ack_len.

import (
	"testing"
)

// --- top-level ACK (decodeAck) ---

func TestDecodeAckLegacy4Byte(t *testing.T) {
	// Backwards-compat: 4-byte ACK leaves the new optional fields nil.
	buf := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	p := decodeAck(buf)
	if p.ExtraHash != "ddccbbaa" {
		t.Errorf("extraHash=%q want ddccbbaa", p.ExtraHash)
	}
	if p.AckLen == nil || *p.AckLen != 4 {
		t.Errorf("ackLen=%v want 4", p.AckLen)
	}
	if p.AckAttempt != nil {
		t.Errorf("ackAttempt=%v want nil for legacy 4-byte ACK", *p.AckAttempt)
	}
	if p.AckRand != nil {
		t.Errorf("ackRand=%v want nil for legacy 4-byte ACK", *p.AckRand)
	}
}

func TestDecodeAck5ByteExtended(t *testing.T) {
	// v1.16 sender (commit f6e6fdaa): 4-byte CRC + 1-byte attempt.
	buf := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0x07}
	p := decodeAck(buf)
	if p.ExtraHash != "ddccbbaa" {
		t.Errorf("extraHash=%q want ddccbbaa", p.ExtraHash)
	}
	if p.AckLen == nil || *p.AckLen != 5 {
		t.Errorf("ackLen=%v want 5", p.AckLen)
	}
	if p.AckAttempt == nil || *p.AckAttempt != 7 {
		t.Errorf("ackAttempt=%v want 7", p.AckAttempt)
	}
	if p.AckRand != nil {
		t.Errorf("ackRand=%v want nil for 5-byte ACK", *p.AckRand)
	}
}

func TestDecodeAck6ByteExtended(t *testing.T) {
	// v1.16 sender (commit a130a95a): 4-byte CRC + 1-byte attempt + 1-byte RNG.
	buf := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0x02, 0x5A}
	p := decodeAck(buf)
	if p.ExtraHash != "ddccbbaa" {
		t.Errorf("extraHash=%q want ddccbbaa", p.ExtraHash)
	}
	if p.AckLen == nil || *p.AckLen != 6 {
		t.Errorf("ackLen=%v want 6", p.AckLen)
	}
	if p.AckAttempt == nil || *p.AckAttempt != 2 {
		t.Errorf("ackAttempt=%v want 2", p.AckAttempt)
	}
	if p.AckRand == nil || *p.AckRand != 0x5A {
		t.Errorf("ackRand=%v want 90", p.AckRand)
	}
}

// --- multipart-with-ACK (decodeMultipart) ---

// buildMultipartAckByte0: remaining<<4 | PayloadACK (0x02).
func buildMultipartAckByte0(remaining int) byte {
	return byte((remaining<<4)&0xF0) | byte(PayloadACK&0x0F)
}

func TestDecodeMultipartAck4ByteLegacy(t *testing.T) {
	// Pre-1.16 inner ACK is 4 bytes → ackLen=4, attempt/rand nil.
	buf := []byte{buildMultipartAckByte0(3), 0xAA, 0xBB, 0xCC, 0xDD}
	p := decodeMultipart(buf)
	if p.InnerAckCrc != "ddccbbaa" {
		t.Errorf("innerAckCrc=%q want ddccbbaa", p.InnerAckCrc)
	}
	if p.InnerAckLen == nil || *p.InnerAckLen != 4 {
		t.Errorf("innerAckLen=%v want 4", p.InnerAckLen)
	}
	if p.InnerAckAttempt != nil {
		t.Errorf("innerAckAttempt=%v want nil", *p.InnerAckAttempt)
	}
	if p.InnerAckRand != nil {
		t.Errorf("innerAckRand=%v want nil", *p.InnerAckRand)
	}
}

func TestDecodeMultipartAck5Byte(t *testing.T) {
	// v1.16: byte0 + 4-byte CRC + 1-byte attempt → payload_len = 6.
	buf := []byte{buildMultipartAckByte0(1), 0xAA, 0xBB, 0xCC, 0xDD, 0x09}
	p := decodeMultipart(buf)
	if p.InnerAckCrc != "ddccbbaa" {
		t.Errorf("innerAckCrc=%q want ddccbbaa", p.InnerAckCrc)
	}
	if p.InnerAckLen == nil || *p.InnerAckLen != 5 {
		t.Errorf("innerAckLen=%v want 5", p.InnerAckLen)
	}
	if p.InnerAckAttempt == nil || *p.InnerAckAttempt != 9 {
		t.Errorf("innerAckAttempt=%v want 9", p.InnerAckAttempt)
	}
	if p.InnerAckRand != nil {
		t.Errorf("innerAckRand=%v want nil for 5-byte inner ACK", *p.InnerAckRand)
	}
}

func TestDecodeMultipartAck6Byte(t *testing.T) {
	// v1.16: byte0 + 4-byte CRC + 1-byte attempt + 1-byte RNG → payload_len = 7.
	buf := []byte{buildMultipartAckByte0(0), 0xAA, 0xBB, 0xCC, 0xDD, 0x04, 0xC3}
	p := decodeMultipart(buf)
	if p.InnerAckCrc != "ddccbbaa" {
		t.Errorf("innerAckCrc=%q want ddccbbaa", p.InnerAckCrc)
	}
	if p.InnerAckLen == nil || *p.InnerAckLen != 6 {
		t.Errorf("innerAckLen=%v want 6", p.InnerAckLen)
	}
	if p.InnerAckAttempt == nil || *p.InnerAckAttempt != 4 {
		t.Errorf("innerAckAttempt=%v want 4", p.InnerAckAttempt)
	}
	if p.InnerAckRand == nil || *p.InnerAckRand != 0xC3 {
		t.Errorf("innerAckRand=%v want 195", p.InnerAckRand)
	}
}
