package main

import (
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// ChannelKey holds the derived AES and HMAC keys for a health-check channel.
//
// Key derivation matches the source project (yellowcooln/meshcore-health-check):
//   - TestChannelSecret in config is a hex-encoded byte string
//     (e.g. "aabbccdd..." where every 2 chars = 1 raw byte)
//   - AES key        = rawSecretBytes[:16]     (first 16 raw bytes, AES-128)
//   - HMAC key       = rawSecretBytes[:32]     (up to 32 raw bytes)
//   - ChannelHashByte = SHA-256(rawSecretBytes)[0]  (used to filter channel)
//
// AES-128-ECB is mandated by the MeshCore group-text protocol; it is not a
// design choice.
type ChannelKey struct {
	AESKey          []byte
	HMACKey         []byte
	ChannelHashByte byte // first byte of SHA-256(rawSecretBytes)
}

// NewChannelKey derives a ChannelKey from a hex-encoded channel secret.
func NewChannelKey(secretHex string) (*ChannelKey, error) {
	rawBytes, err := hex.DecodeString(secretHex)
	if err != nil {
		return nil, fmt.Errorf("health channel: invalid hex secret: %w", err)
	}
	if len(rawBytes) < 16 {
		return nil, fmt.Errorf("health channel: secret too short: need ≥16 bytes, got %d", len(rawBytes))
	}

	aesKey := make([]byte, 16)
	copy(aesKey, rawBytes[:16])

	hmacLen := len(rawBytes)
	if hmacLen > 32 {
		hmacLen = 32
	}
	hmacKey := make([]byte, hmacLen)
	copy(hmacKey, rawBytes[:hmacLen])

	digest := sha256.Sum256(rawBytes)

	return &ChannelKey{
		AESKey:          aesKey,
		HMACKey:         hmacKey,
		ChannelHashByte: digest[0],
	}, nil
}

// MatchesChannelHash returns true when packetChannelHashByte belongs to this
// channel. If no key was configured (ck == nil) every channel is accepted.
func (ck *ChannelKey) MatchesChannelHash(packetChannelHashByte int) bool {
	if ck == nil {
		return true
	}
	return byte(packetChannelHashByte) == ck.ChannelHashByte
}

// ValidateMAC checks the 2-byte HMAC prefix against the ciphertext.
// macHex is the hex-encoded MAC (e.g. "a3f2"). Returns false on any error.
func (ck *ChannelKey) ValidateMAC(macHex string, encryptedBytes []byte) bool {
	if ck == nil {
		return false
	}
	macBytes, err := hex.DecodeString(macHex)
	if err != nil || len(macBytes) < 2 {
		return false
	}
	mac := hmac.New(sha256.New, ck.HMACKey)
	mac.Write(encryptedBytes)
	digest := mac.Sum(nil)
	return macBytes[0] == digest[0] && macBytes[1] == digest[1]
}

// DecryptGroupText decrypts AES-128-ECB ciphertext produced by the MeshCore
// group-text protocol. The plaintext bytes are returned (length == input length).
// The caller is responsible for stripping the 4-byte timestamp prefix and any
// null padding before treating the remainder as UTF-8 text.
func (ck *ChannelKey) DecryptGroupText(cipherBytes []byte) ([]byte, error) {
	if ck == nil {
		return nil, fmt.Errorf("health channel: no key configured")
	}
	if len(cipherBytes) == 0 {
		return nil, fmt.Errorf("health channel: empty ciphertext")
	}

	// Pad to AES block size.
	padLen := (aes.BlockSize - len(cipherBytes)%aes.BlockSize) % aes.BlockSize
	padded := make([]byte, len(cipherBytes)+padLen)
	copy(padded, cipherBytes)

	block, err := aes.NewCipher(ck.AESKey)
	if err != nil {
		return nil, err
	}
	plaintext := make([]byte, len(padded))
	for i := 0; i < len(padded); i += aes.BlockSize {
		block.Decrypt(plaintext[i:i+aes.BlockSize], padded[i:i+aes.BlockSize])
	}
	return plaintext[:len(cipherBytes)], nil
}

// ParseGroupTextMessage decrypts and parses a group-text plaintext buffer.
// Layout: [4-byte LE timestamp][1-byte flags?][message UTF-8 bytes...]
// Returns (sender, message, ok). sender may be empty.
func (ck *ChannelKey) ParseGroupTextMessage(cipherBytes []byte) (sender, message string, ok bool) {
	plain, err := ck.DecryptGroupText(cipherBytes)
	if err != nil || len(plain) < 6 {
		return "", "", false
	}

	// Validate timestamp sanity (year 2023–2035).
	ts := uint32(plain[0]) | uint32(plain[1])<<8 | uint32(plain[2])<<16 | uint32(plain[3])<<24
	year := 1970 + int(ts/31536000)
	if year < 2023 || year > 2035 {
		return "", "", false
	}

	msgBytes := plain[5:] // skip 4-byte timestamp + 1-byte flags
	if len(msgBytes) == 0 {
		return "", "", false
	}

	// Require ≥70 % printable ASCII (same heuristic as source).
	printable := 0
	for _, b := range msgBytes {
		if (b >= 32 && b <= 126) || b == 9 || b == 10 || b == 13 {
			printable++
		}
	}
	if float64(printable)/float64(len(msgBytes)) < 0.7 {
		return "", "", false
	}

	// Strip null bytes.
	raw := string(msgBytes)
	for len(raw) > 0 && raw[len(raw)-1] == 0 {
		raw = raw[:len(raw)-1]
	}
	if raw == "" {
		return "", "", false
	}

	// Split "Sender: message" (same heuristic as source).
	for i := 0; i < len(raw)-2; i++ {
		if raw[i] == ':' && raw[i+1] == ' ' && i < 50 {
			maybe := raw[:i]
			// Sender must not contain ':', '[', ']'
			valid := true
			for _, c := range maybe {
				if c == ':' || c == '[' || c == ']' {
					valid = false
					break
				}
			}
			if valid {
				return maybe, raw[i+2:], true
			}
			break
		}
	}
	return "", raw, true
}
