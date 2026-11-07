package users

import (
	"bytes"
	"testing"
)

func testKey(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func TestSecretBox_RoundTrip(t *testing.T) {
	box, err := NewSecretBox(testKey(1))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if sealed == "hunter2" {
		t.Fatal("Seal returned the plaintext")
	}
	got, err := box.Open(sealed)
	if err != nil || got != "hunter2" {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestSecretBox_WrongKeyFails(t *testing.T) {
	a, _ := NewSecretBox(testKey(1))
	b, _ := NewSecretBox(testKey(2))
	sealed, err := a.Seal("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Open(sealed); err == nil {
		t.Fatal("Open with another key succeeded")
	}
}

func TestSecretBox_NonceUnique(t *testing.T) {
	box, _ := NewSecretBox(testKey(1))
	s1, _ := box.Seal("same")
	s2, _ := box.Seal("same")
	if s1 == s2 {
		t.Fatal("two Seals of the same plaintext are identical")
	}
}

func TestSecretBox_BadInput(t *testing.T) {
	if _, err := NewSecretBox(make([]byte, 16)); err == nil {
		t.Error("16-byte key accepted")
	}
	box, _ := NewSecretBox(testKey(1))
	for _, in := range []string{"", "!!!not-base64", "AAAA"} {
		if _, err := box.Open(in); err == nil {
			t.Errorf("Open(%q) succeeded", in)
		}
	}
}
