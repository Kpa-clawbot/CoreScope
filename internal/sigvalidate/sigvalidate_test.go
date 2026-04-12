package sigvalidate

import (
	"crypto/ed25519"
	"encoding/binary"
	"testing"
)

func TestValidateAdvert_ValidSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	var timestamp uint32 = 1234567890
	appdata := []byte{0x02, 0x10, 0x20}

	// Build the signed message: pubKey + timestamp(LE) + appdata
	msg := make([]byte, 32+4+len(appdata))
	copy(msg[0:32], pub)
	binary.LittleEndian.PutUint32(msg[32:36], timestamp)
	copy(msg[36:], appdata)

	sig := ed25519.Sign(priv, msg)

	valid, err := ValidateAdvert([]byte(pub), sig, timestamp, appdata)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !valid {
		t.Fatal("expected valid signature")
	}
}

func TestValidateAdvert_InvalidSignature(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	badSig := make([]byte, 64)
	valid, err := ValidateAdvert([]byte(pub), badSig, 100, []byte{0x01})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if valid {
		t.Fatal("expected invalid signature")
	}
}

func TestValidateAdvert_BadPubkeyLength(t *testing.T) {
	_, err := ValidateAdvert([]byte{1, 2, 3}, make([]byte, 64), 0, nil)
	if err == nil {
		t.Fatal("expected error for short pubkey")
	}
}

func TestValidateAdvert_BadSignatureLength(t *testing.T) {
	_, err := ValidateAdvert(make([]byte, 32), []byte{1, 2, 3}, 0, nil)
	if err == nil {
		t.Fatal("expected error for short signature")
	}
}
