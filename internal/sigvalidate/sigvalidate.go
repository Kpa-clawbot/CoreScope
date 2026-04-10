// Package sigvalidate provides ed25519 signature validation for MeshCore advert packets.
package sigvalidate

import (
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
)

// ValidateAdvert verifies the ed25519 signature on a MeshCore advert.
// pubKey must be 32 bytes, signature must be 64 bytes.
// The signed message is: pubKey (32) + timestamp (4 LE) + appdata.
func ValidateAdvert(pubKey, signature []byte, timestamp uint32, appdata []byte) (bool, error) {
	if len(pubKey) != 32 {
		return false, fmt.Errorf("invalid pubkey length: %d", len(pubKey))
	}
	if len(signature) != 64 {
		return false, fmt.Errorf("invalid signature length: %d", len(signature))
	}

	message := make([]byte, 32+4+len(appdata))
	copy(message[0:32], pubKey)
	binary.LittleEndian.PutUint32(message[32:36], timestamp)
	copy(message[36:], appdata)

	return ed25519.Verify(ed25519.PublicKey(pubKey), message, signature), nil
}
