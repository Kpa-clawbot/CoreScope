package sigvalidate

import (
	"bytes"
	"crypto/ed25519"
	"testing"
)

func TestVerifyMessage(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, 32))
	pub := priv.Public().(ed25519.PublicKey)
	msg := []byte("corescope-link:scope.example.org:00ff")
	sig := ed25519.Sign(priv, msg)

	if ok, err := VerifyMessage(pub, sig, msg); err != nil || !ok {
		t.Fatalf("valid signature: ok=%v err=%v", ok, err)
	}
	if ok, err := VerifyMessage(pub, sig, []byte("corescope-link:other.example.org:00ff")); err != nil || ok {
		t.Fatalf("other message: ok=%v err=%v", ok, err)
	}
	if _, err := VerifyMessage(pub[:31], sig, msg); err == nil {
		t.Fatal("31-byte pubkey accepted")
	}
	if _, err := VerifyMessage(pub, sig[:63], msg); err == nil {
		t.Fatal("63-byte signature accepted")
	}
}
