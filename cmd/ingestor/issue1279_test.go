package main

// Tests for issue #1279 P0+P1 decoder additions.
//
// Each test uses firmware-derived wire vectors:
//   - GRP_DATA outer: firmware/src/helpers/BaseChatMesh.cpp:500 (createGroupDatagram)
//   - GRP_DATA inner: firmware/src/helpers/BaseChatMesh.cpp:382-385
//   - MULTIPART byte0: firmware/src/Mesh.cpp:289
//   - MULTIPART ACK inner: firmware/src/Mesh.cpp:292-307
//   - CONTROL byte0 flags: firmware/src/Mesh.cpp:69 + createControlData at Mesh.cpp:609
//   - advertRole label rules: firmware/src/helpers/AdvertDataHelpers.h:7-12

import (
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// --- P0 #1: GRP_DATA decoder ---

// buildChannelEncrypted encrypts arbitrary inner bytes with the channel
// key/MAC scheme firmware uses for both GRP_TXT and GRP_DATA (see
// BaseChatMesh.cpp:376-391: AES-128-ECB, HMAC-SHA256-trunc-2 MAC).
func buildChannelEncrypted(channelKeyHex string, inner []byte) (ctHex, macHex string) {
	key, _ := hex.DecodeString(channelKeyHex)
	plain := append([]byte{}, inner...)
	pad := aes.BlockSize - (len(plain) % aes.BlockSize)
	if pad != aes.BlockSize {
		plain = append(plain, make([]byte, pad)...)
	}
	block, _ := aes.NewCipher(key)
	ct := make([]byte, len(plain))
	for i := 0; i < len(plain); i += aes.BlockSize {
		block.Encrypt(ct[i:i+aes.BlockSize], plain[i:i+aes.BlockSize])
	}
	secret := make([]byte, 32)
	copy(secret, key)
	h := hmac.New(sha256.New, secret)
	h.Write(ct)
	mac := h.Sum(nil)
	return hex.EncodeToString(ct), hex.EncodeToString(mac[:2])
}

func TestDecodeGrpDataNoKey(t *testing.T) {
	// Envelope alone (no key in store).
	buf := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11}
	p := decodeGrpData(buf, nil)
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
	if p.DecryptionStatus != "no_key" {
		t.Errorf("decryptionStatus=%q want no_key", p.DecryptionStatus)
	}
}

func TestDecodeGrpDataDecryptedInner(t *testing.T) {
	// Inner per BaseChatMesh.cpp:382-385: data_type(uint16 LE) + data_len(1) + blob.
	key := "2cc3d22840e086105ad73443da2cacb8"
	blob := []byte{0x10, 0x20, 0x30, 0x40, 0x50}
	inner := []byte{0x34, 0x12, byte(len(blob))} // data_type = 0x1234
	inner = append(inner, blob...)
	ctHex, macHex := buildChannelEncrypted(key, inner)

	buf := []byte{0xAB}
	mb, _ := hex.DecodeString(macHex)
	buf = append(buf, mb...)
	cb, _ := hex.DecodeString(ctHex)
	buf = append(buf, cb...)

	p := decodeGrpData(buf, map[string]string{"test": key})
	if p.Type != "GRP_DATA" {
		t.Fatalf("type=%q want GRP_DATA", p.Type)
	}
	if p.DecryptionStatus != "decrypted" {
		t.Fatalf("decryptionStatus=%q want decrypted", p.DecryptionStatus)
	}
	if p.DataType == nil || *p.DataType != 0x1234 {
		t.Errorf("dataType=%v want 0x1234", p.DataType)
	}
	if p.DataLen == nil || *p.DataLen != 5 {
		t.Errorf("dataLen=%v want 5", p.DataLen)
	}
	if p.DecryptedBlob != hex.EncodeToString(blob) {
		t.Errorf("decryptedBlob=%q want %q", p.DecryptedBlob, hex.EncodeToString(blob))
	}
	if p.Channel != "test" {
		t.Errorf("channel=%q want test", p.Channel)
	}
}

// --- P0 #2: MULTIPART decoder ---

func TestDecodeMultipartAck(t *testing.T) {
	// remaining=3, inner_type=PAYLOAD_TYPE_ACK(0x03), ack_crc=0xDEADBEEF.
	// byte0 = (3<<4) | 3 = 0x33; next 4 bytes are LE crc.
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

func TestDecodeMultipartNonAck(t *testing.T) {
	// remaining=2, inner_type=0x02 (TXT_MSG), arbitrary inner payload.
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
	if p.InnerAckCrc != "" {
		t.Errorf("non-ACK should not surface innerAckCrc, got %q", p.InnerAckCrc)
	}
}

// --- P1 #3: advertRole label fix ---

func TestAdvertRoleLabelsRawType(t *testing.T) {
	// Firmware: ADV_TYPE_NONE=0, CHAT=1, REPEATER=2, ROOM=3, SENSOR=4, 5..15 FUTURE.
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

// --- P1 #4: CONTROL byte0 flags ---

func TestDecodeControlZeroHop(t *testing.T) {
	// byte0 = 0x81 (high-bit set ⇒ zero-hop), followed by 3 app bytes.
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

func TestDecodeControlMultiHop(t *testing.T) {
	// byte0 = 0x01 (high-bit clear ⇒ not zero-hop subset).
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

// silence unused-import diagnostics for stub-phase builds
var _ = binary.LittleEndian
