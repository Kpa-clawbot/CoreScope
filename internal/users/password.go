package users

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters: OWASP's minimum recommendation (19 MiB, 2 passes,
// 1 lane). They are encoded in every hash, so raising them later only
// affects new hashes.
const (
	argonTime    uint32 = 2
	argonMemory  uint32 = 19 * 1024
	argonThreads uint8  = 1
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// HashPassword returns a PHC-format argon2id hash.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("users: salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword checks password against a PHC argon2id hash in constant time.
func VerifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errors.New("users: unsupported password hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("users: unsupported argon2 version")
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, fmt.Errorf("users: bad argon2 parameters: %w", err)
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("users: bad salt: %w", err)
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errors.New("users: bad key")
	}
	if t < 1 || p < 1 || m < 8*uint32(p) || m > 1<<20 || len(salt) < 8 || len(want) < 16 || len(want) > 64 {
		return false, errors.New("users: argon2 parameters out of range")
	}
	if fmt.Sprintf("m=%d,t=%d,p=%d", m, t, p) != parts[3] {
		return false, errors.New("users: malformed argon2 parameters")
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// BurnPasswordCheck runs a full hash comparison against a fixed dummy hash,
// so a login for an unknown address costs as much as one for a real account.
func BurnPasswordCheck(password string) {
	dummyOnce.Do(func() { dummyHash, _ = HashPassword("corescope-dummy-password") })
	_, _ = VerifyPassword(dummyHash, password)
}
