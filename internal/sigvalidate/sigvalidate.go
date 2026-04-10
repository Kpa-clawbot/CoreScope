// Package sigvalidate provides Ed25519 signature validation for MeshCore adverts.
package sigvalidate

import (
	"crypto/ed25519"
	"encoding/binary"
)

// ValidateAdvertSignature verifies an Ed25519 signature over a MeshCore advert.
// The signed message is: pubKey (32 bytes) || timestamp (4 bytes LE) || appdata.
// Returns false if pubKey is not 32 bytes or signature is not 64 bytes.
func ValidateAdvertSignature(pubKey, signature []byte, timestamp uint32, appdata []byte) bool {
	if len(pubKey) != 32 || len(signature) != 64 {
		return false
	}

	message := make([]byte, 32+4+len(appdata))
	copy(message[0:32], pubKey)
	binary.LittleEndian.PutUint32(message[32:36], timestamp)
	copy(message[36:], appdata)

	return ed25519.Verify(ed25519.PublicKey(pubKey), message, signature)
}
