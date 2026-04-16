package channel

import (
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestDeriveKey(t *testing.T) {
	key := DeriveKey("#wardriving")
	h := sha256.Sum256([]byte("#wardriving"))
	expected := h[:16]
	if len(key) != 16 {
		t.Fatalf("key length %d, want 16", len(key))
	}
	for i := range key {
		if key[i] != expected[i] {
			t.Fatalf("DeriveKey mismatch at byte %d", i)
		}
	}
}

func TestChannelHash(t *testing.T) {
	key := DeriveKey("#wardriving")
	ch := ChannelHash(key)
	h := sha256.Sum256(key)
	if ch != h[0] {
		t.Fatalf("ChannelHash %02x, want %02x", ch, h[0])
	}
}

func testECBEncrypt(t *testing.T, key, plaintext []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	ct := make([]byte, len(plaintext))
	for i := 0; i < len(plaintext); i += aes.BlockSize {
		block.Encrypt(ct[i:i+aes.BlockSize], plaintext[i:i+aes.BlockSize])
	}
	return ct
}

func testComputeMAC(key, ciphertext []byte) []byte {
	secret := make([]byte, 32)
	copy(secret, key)
	h := hmac.New(sha256.New, secret)
	h.Write(ciphertext)
	sum := h.Sum(nil)
	return sum[:2]
}

func TestDecryptValidMAC(t *testing.T) {
	key := DeriveKey("#test")
	padded := make([]byte, 16)
	copy(padded, []byte{0x01, 0x00, 0x00, 0x00, 0x00})
	ciphertext := testECBEncrypt(t, key, padded)
	mac := testComputeMAC(key, ciphertext)

	result, ok := Decrypt(key, mac, ciphertext)
	if !ok {
		t.Fatal("Decrypt returned false for valid MAC")
	}
	if len(result) != 16 {
		t.Fatalf("result length %d, want 16", len(result))
	}
}

func TestDecryptInvalidMAC(t *testing.T) {
	key := DeriveKey("#test")
	ciphertext := make([]byte, 16)
	mac := []byte{0xFF, 0xFF}
	_, ok := Decrypt(key, mac, ciphertext)
	if ok {
		t.Fatal("Decrypt should reject wrong MAC")
	}
}

func TestDecryptWrongChannel(t *testing.T) {
	key1 := DeriveKey("#channel1")
	key2 := DeriveKey("#channel2")
	padded := make([]byte, 16)
	copy(padded, []byte{0x01, 0x00, 0x00, 0x00, 0x00, 'h', 'i'})
	ciphertext := testECBEncrypt(t, key1, padded)
	mac := testComputeMAC(key1, ciphertext)

	_, ok := Decrypt(key2, mac, ciphertext)
	if ok {
		t.Fatal("Decrypt should reject wrong channel key")
	}
}

func TestParsePlaintext(t *testing.T) {
	plain := []byte{100, 0, 0, 0, 0}
	plain = append(plain, []byte("Alice: Hello\x00")...)
	ts, sender, msg, err := ParsePlaintext(plain)
	if err != nil {
		t.Fatal(err)
	}
	if ts != 100 {
		t.Fatalf("timestamp %d, want 100", ts)
	}
	if sender != "Alice" {
		t.Fatalf("sender %q, want Alice", sender)
	}
	if msg != "Hello" {
		t.Fatalf("message %q, want Hello", msg)
	}
}

func TestParsePlaintextNoSender(t *testing.T) {
	plain := []byte{1, 0, 0, 0, 0}
	plain = append(plain, []byte("just a message\x00")...)
	_, sender, msg, err := ParsePlaintext(plain)
	if err != nil {
		t.Fatal(err)
	}
	if sender != "" {
		t.Fatalf("sender %q, want empty", sender)
	}
	if msg != "just a message" {
		t.Fatalf("message %q", msg)
	}
}

func TestDeriveKeyMatchesIngestor(t *testing.T) {
	channelName := "#MeshCore"
	key := DeriveKey(channelName)
	hexKey := hex.EncodeToString(key)
	h := sha256.Sum256([]byte(channelName))
	expected := hex.EncodeToString(h[:16])
	if hexKey != expected {
		t.Fatalf("key hex %s != expected %s", hexKey, expected)
	}
}

func TestRoundTrip(t *testing.T) {
	key := DeriveKey("#test")
	original := make([]byte, 32)
	copy(original, []byte{0x64, 0x00, 0x00, 0x00, 0x00})
	copy(original[5:], []byte("Bob: world\x00"))

	ciphertext := testECBEncrypt(t, key, original)
	mac := testComputeMAC(key, ciphertext)

	plaintext, ok := Decrypt(key, mac, ciphertext)
	if !ok {
		t.Fatal("round-trip MAC failed")
	}

	ts, sender, msg, err := ParsePlaintext(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if ts != 100 || sender != "Bob" || msg != "world" {
		t.Fatalf("got ts=%d sender=%q msg=%q", ts, sender, msg)
	}
}
