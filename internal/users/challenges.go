package users

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// LinkChallengeTTL is how long a companion-link challenge stays valid.
const LinkChallengeTTL = 5 * time.Minute

// challengeHash is the stored form of a challenge: SHA-256 of its 32 raw bytes.
func challengeHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// CreateLinkChallenge issues a single-use challenge bound to userID and
// pubkey. It returns the 32 random bytes as lowercase hex; only their hash
// is stored.
func (s *Store) CreateLinkChallenge(userID int64, pubkey string) (string, time.Time, error) {
	pk, err := NormalizePubkey(pubkey)
	if err != nil {
		return "", time.Time{}, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("users: challenge: %w", err)
	}
	exp := unix(s.now().Add(LinkChallengeTTL))
	if _, err := s.db.Exec(`INSERT INTO link_challenges (challenge_hash, user_id, pubkey, expires_at) VALUES (?, ?, ?, ?)`,
		challengeHash(raw), userID, pk, exp); err != nil {
		return "", time.Time{}, fmt.Errorf("users: create link challenge: %w", err)
	}
	return hex.EncodeToString(raw), fromUnix(exp), nil
}

// ConsumeLinkChallenge checks a challenge returned by the client and deletes
// it in every case. It returns ErrChallengeMissing (unknown, malformed or
// already used), ErrChallengeExpired, or ErrChallengeMismatch (bound to
// another user or pubkey). One DELETE … RETURNING statement, so two
// concurrent consumers cannot both succeed.
func (s *Store) ConsumeLinkChallenge(userID int64, pubkey, challenge string) error {
	raw, err := hex.DecodeString(challenge)
	if err != nil || len(raw) != 32 {
		return ErrChallengeMissing
	}
	var owner, exp int64
	var pk string
	err = s.db.QueryRow(`DELETE FROM link_challenges WHERE challenge_hash = ? RETURNING user_id, pubkey, expires_at`,
		challengeHash(raw)).Scan(&owner, &pk, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrChallengeMissing
	}
	if err != nil {
		return fmt.Errorf("users: consume link challenge: %w", err)
	}
	if !s.now().Before(fromUnix(exp)) {
		return ErrChallengeExpired
	}
	want, perr := NormalizePubkey(pubkey)
	if owner != userID || perr != nil || pk != want {
		return ErrChallengeMismatch
	}
	return nil
}
