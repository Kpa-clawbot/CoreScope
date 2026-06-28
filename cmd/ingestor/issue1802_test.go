package main

// Tests for #1802 — CONTROL DISCOVER_REQ / DISCOVER_RESP decode.
// Wire format references:
//   firmware/src/Mesh.cpp:69 (CTL_TYPE constants)
//   firmware/examples/simple_repeater/MyMesh.cpp:773-820
//
// Subtype = byte0 & 0xF0. 0x80 = DISCOVER_REQ, 0x90 = DISCOVER_RESP.
// REQ body (>=6B after byte0): filter:u8 | tag:u32 LE | since:u32 LE (optional)
// RESP body (>=6+pubkey): snr:i8 | tag:u32 LE | pubkey (32B full, or 8B prefix)

import (
	"encoding/binary"
	"testing"
)

func TestDecodeControl_DiscoverReq_FullBody(t *testing.T) {
	// byte0 = 0x80 (DISCOVER_REQ), filter=0x02, tag=0xDEADBEEF, since=0x11223344.
	buf := []byte{0x80, 0x02, 0xEF, 0xBE, 0xAD, 0xDE, 0x44, 0x33, 0x22, 0x11}
	p := decodeControl(buf)
	if p.Type != "CONTROL" {
		t.Fatalf("type=%q want CONTROL", p.Type)
	}
	if p.CtrlSubtype != "DISCOVER_REQ" {
		t.Errorf("ctrlSubtype=%q want DISCOVER_REQ", p.CtrlSubtype)
	}
	if p.CtrlFilter == nil || *p.CtrlFilter != 0x02 {
		t.Errorf("ctrlFilter=%v want 2", p.CtrlFilter)
	}
	if p.CtrlTag == nil || *p.CtrlTag != 0xDEADBEEF {
		t.Errorf("ctrlTag=%v want 0xDEADBEEF", p.CtrlTag)
	}
	if p.CtrlSince == nil || *p.CtrlSince != 0x11223344 {
		t.Errorf("ctrlSince=%v want 0x11223344", p.CtrlSince)
	}
}

func TestDecodeControl_DiscoverReq_NoSince(t *testing.T) {
	// 6-byte body (no optional since): byte0=0x80, filter, tag.
	buf := []byte{0x80, 0x04, 0x01, 0x02, 0x03, 0x04}
	p := decodeControl(buf)
	if p.CtrlSubtype != "DISCOVER_REQ" {
		t.Errorf("ctrlSubtype=%q want DISCOVER_REQ", p.CtrlSubtype)
	}
	if p.CtrlFilter == nil || *p.CtrlFilter != 0x04 {
		t.Errorf("ctrlFilter=%v want 4", p.CtrlFilter)
	}
	wantTag := binary.LittleEndian.Uint32([]byte{0x01, 0x02, 0x03, 0x04})
	if p.CtrlTag == nil || *p.CtrlTag != wantTag {
		t.Errorf("ctrlTag=%v want %#x", p.CtrlTag, wantTag)
	}
	if p.CtrlSince != nil {
		t.Errorf("ctrlSince should be nil for 6B REQ body, got %v", p.CtrlSince)
	}
}

func TestDecodeControl_DiscoverResp_FullPubKey(t *testing.T) {
	// byte0 = 0x90 | 0x02 (node_type=2, REPEATER), snr=0x10, tag, 32B pubkey.
	body := []byte{0x92, 0x10, 0x44, 0x33, 0x22, 0x11}
	for i := 0; i < 32; i++ {
		body = append(body, byte(i))
	}
	p := decodeControl(body)
	if p.CtrlSubtype != "DISCOVER_RESP" {
		t.Errorf("ctrlSubtype=%q want DISCOVER_RESP", p.CtrlSubtype)
	}
	if p.CtrlNodeType == nil || *p.CtrlNodeType != 2 {
		t.Errorf("ctrlNodeType=%v want 2", p.CtrlNodeType)
	}
	if p.CtrlSNR == nil || *p.CtrlSNR != 0x10 {
		t.Errorf("ctrlSNR=%v want 16", p.CtrlSNR)
	}
	wantTag := binary.LittleEndian.Uint32([]byte{0x44, 0x33, 0x22, 0x11})
	if p.CtrlTag == nil || *p.CtrlTag != wantTag {
		t.Errorf("ctrlTag=%v want %#x", p.CtrlTag, wantTag)
	}
	if len(p.CtrlPubKey) != 64 {
		t.Fatalf("ctrlPubKey hex len=%d want 64 (32B)", len(p.CtrlPubKey))
	}
	if p.CtrlPubKey[:4] != "0001" {
		t.Errorf("ctrlPubKey prefix=%q want 0001…", p.CtrlPubKey[:4])
	}
}

func TestDecodeControl_DiscoverResp_PrefixPubKey(t *testing.T) {
	// 6 header bytes + 8 pubkey bytes only (prefix_only path).
	body := []byte{0x91, 0xF0, 0xAA, 0xBB, 0xCC, 0xDD, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	p := decodeControl(body)
	if p.CtrlSubtype != "DISCOVER_RESP" {
		t.Errorf("ctrlSubtype=%q want DISCOVER_RESP", p.CtrlSubtype)
	}
	if p.CtrlNodeType == nil || *p.CtrlNodeType != 1 {
		t.Errorf("ctrlNodeType=%v want 1", p.CtrlNodeType)
	}
	// snr = signed 0xF0 = -16
	if p.CtrlSNR == nil || *p.CtrlSNR != -16 {
		t.Errorf("ctrlSNR=%v want -16", p.CtrlSNR)
	}
	if len(p.CtrlPubKey) != 16 {
		t.Errorf("ctrlPubKey hex len=%d want 16 (8B)", len(p.CtrlPubKey))
	}
}

func TestDecodeControl_DiscoverResp_TruncatedPubKey(t *testing.T) {
	// 6 header bytes + 4 pubkey bytes — neither 8 nor 32. Must NOT panic;
	// subtype emitted, pubkey omitted.
	body := []byte{0x91, 0x05, 0xAA, 0xBB, 0xCC, 0xDD, 0x11, 0x22, 0x33, 0x44}
	p := decodeControl(body)
	if p.CtrlSubtype != "DISCOVER_RESP" {
		t.Errorf("ctrlSubtype=%q want DISCOVER_RESP", p.CtrlSubtype)
	}
	if p.CtrlPubKey != "" {
		t.Errorf("ctrlPubKey=%q want empty for 4B pubkey blob", p.CtrlPubKey)
	}
}

func TestDecodeControl_ShortBody_NoSubtypeFields(t *testing.T) {
	// byte0=0x80 only (no body). Must emit subtype, no panic, no body fields.
	buf := []byte{0x80}
	p := decodeControl(buf)
	if p.CtrlSubtype != "DISCOVER_REQ" {
		t.Errorf("ctrlSubtype=%q want DISCOVER_REQ", p.CtrlSubtype)
	}
	if p.CtrlFilter != nil {
		t.Errorf("ctrlFilter should be nil, got %v", p.CtrlFilter)
	}
	if p.CtrlTag != nil {
		t.Errorf("ctrlTag should be nil, got %v", p.CtrlTag)
	}
}

func TestDecodeControl_UnknownSubtype(t *testing.T) {
	// byte0 = 0xA0 — not DISCOVER_REQ/RESP. Subtype = "UNKNOWN".
	buf := []byte{0xA0, 0x11, 0x22}
	p := decodeControl(buf)
	if p.CtrlSubtype != "UNKNOWN" {
		t.Errorf("ctrlSubtype=%q want UNKNOWN", p.CtrlSubtype)
	}
}
