package users

import (
	"strings"
	"testing"
)

func TestHashPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected PHC prefix: %s", h)
	}
	ok, err := VerifyPassword(h, "correct horse battery")
	if err != nil || !ok {
		t.Fatalf("verify correct = %v, %v", ok, err)
	}
	ok, err = VerifyPassword(h, "wrong horse battery")
	if err != nil || ok {
		t.Fatalf("verify wrong = %v, %v", ok, err)
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("two hashes of the same password are identical (salt not random)")
	}
}

func TestVerifyPasswordRejectsForeignFormats(t *testing.T) {
	for _, enc := range []string{"", "plain", "$2a$10$abcdefghijklmnopqrstuv", "$argon2i$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA"} {
		if _, err := VerifyPassword(enc, "x"); err == nil {
			t.Errorf("VerifyPassword(%q) returned no error", enc)
		}
	}
}

func TestNewTokenAndHash(t *testing.T) {
	raw, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 43 { // 32 bytes, base64url without padding
		t.Fatalf("raw token length %d", len(raw))
	}
	if HashToken(raw) != hash || len(hash) != 64 {
		t.Fatalf("hash mismatch: %s vs %s", HashToken(raw), hash)
	}
	raw2, _, _ := NewToken()
	if raw == raw2 {
		t.Fatal("tokens repeat")
	}
}

func TestHashedEmailIsStableAndOpaque(t *testing.T) {
	a, b := HashedEmail("alice@example.org"), HashedEmail("alice@example.org")
	if a != b || !strings.HasPrefix(a, "sha256:") || strings.Contains(a, "alice") {
		t.Fatalf("HashedEmail = %q / %q", a, b)
	}
}

func TestBurnPasswordCheckDoesNotPanic(t *testing.T) {
	BurnPasswordCheck("anything at all")
}

func TestVerifyPasswordRejectsDangerousParams(t *testing.T) {
	tail := "$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA"
	for _, enc := range []string{
		"$argon2id$v=19$m=19456,t=0,p=1" + tail,
		"$argon2id$v=19$m=19456,t=2,p=0" + tail,
		"$argon2id$v=19$m=1073741824,t=2,p=1" + tail,
		"$argon2id$v=19$m=19456,t=2,p=1x" + tail,
		"$argon2id$v=19$m=4,t=2,p=1" + tail,
	} {
		if _, err := VerifyPassword(enc, "x"); err == nil {
			t.Errorf("VerifyPassword(%q) returned no error", enc)
		}
	}
}
