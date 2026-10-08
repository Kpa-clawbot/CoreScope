package users

import (
	"encoding/hex"
	"strings"
)

// NormalizePubkey returns a companion's public key as 64 lowercase hex
// characters, or ErrBadPubkey.
func NormalizePubkey(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 64 {
		return "", ErrBadPubkey
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", ErrBadPubkey
	}
	return s, nil
}
