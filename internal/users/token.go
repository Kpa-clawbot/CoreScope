package users

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// NewToken returns a 256-bit random token (base64url, sent to the user) and
// its SHA-256 hex hash (the only form stored).
func NewToken() (raw, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("users: token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, HashToken(raw), nil
}

// HashToken is the stored form of a raw token.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// HashedEmail replaces an address in records that outlive the account.
func HashedEmail(email string) string {
	sum := sha256.Sum256([]byte(email))
	return "sha256:" + hex.EncodeToString(sum[:8])
}
