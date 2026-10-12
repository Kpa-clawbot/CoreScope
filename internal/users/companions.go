package users

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
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

// CompanionLink ties a companion's pubkey to the user who proved they hold
// its key. A pubkey has at most one owner.
type CompanionLink struct {
	Pubkey   string
	UserID   int64
	Name     string
	LinkedAt time.Time
}

const companionCols = `pubkey, user_id, name, linked_at`

func scanCompanion(row rowScanner) (*CompanionLink, error) {
	var l CompanionLink
	var at int64
	if err := row.Scan(&l.Pubkey, &l.UserID, &l.Name, &at); err != nil {
		return nil, err
	}
	l.LinkedAt = fromUnix(at)
	return &l, nil
}

// UpsertCompanionLink links pubkey to userID; the newest proof wins. When
// the pubkey belonged to another user it moves to userID with a fresh
// LinkedAt, and prevOwner is that user's id. Otherwise prevOwner is 0; a
// re-link by the same owner only updates the name. name is cleaned with
// CleanLabel. The caller must have consumed a valid challenge and verified
// the signature first.
func (s *Store) UpsertCompanionLink(userID int64, pubkey, name string) (*CompanionLink, int64, error) {
	pk, err := NormalizePubkey(pubkey)
	if err != nil {
		return nil, 0, err
	}
	name = CleanLabel(name)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	var prev, linkedAt int64
	err = tx.QueryRow(`SELECT user_id, linked_at FROM companion_links WHERE pubkey = ?`, pk).Scan(&prev, &linkedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		prev, linkedAt = 0, unix(s.now())
	case err != nil:
		tx.Rollback()
		return nil, 0, fmt.Errorf("users: read companion link: %w", err)
	case prev != userID:
		linkedAt = unix(s.now())
	}
	if _, err := tx.Exec(`INSERT INTO companion_links (pubkey, user_id, name, linked_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(pubkey) DO UPDATE SET user_id = excluded.user_id, name = excluded.name, linked_at = excluded.linked_at`,
		pk, userID, name, linkedAt); err != nil {
		tx.Rollback()
		return nil, 0, fmt.Errorf("users: upsert companion link: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, fmt.Errorf("users: upsert companion link: %w", err)
	}
	if prev == userID {
		prev = 0
	}
	return &CompanionLink{Pubkey: pk, UserID: userID, Name: name, LinkedAt: fromUnix(linkedAt)}, prev, nil
}

// ListCompanionLinks returns a user's linked companions, newest first.
func (s *Store) ListCompanionLinks(userID int64) ([]CompanionLink, error) {
	rows, err := s.db.Query(`SELECT `+companionCols+` FROM companion_links WHERE user_id = ? ORDER BY linked_at DESC, pubkey`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CompanionLink
	for rows.Next() {
		l, err := scanCompanion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// GetCompanionLink returns the link for pubkey, or ErrNotFound.
func (s *Store) GetCompanionLink(pubkey string) (*CompanionLink, error) {
	pk, err := NormalizePubkey(pubkey)
	if err != nil {
		return nil, err
	}
	l, err := scanCompanion(s.db.QueryRow(`SELECT `+companionCols+` FROM companion_links WHERE pubkey = ?`, pk))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("users: get companion link: %w", err)
	}
	return l, nil
}

// DeleteCompanionLink unlinks pubkey, only if it is linked to userID.
func (s *Store) DeleteCompanionLink(userID int64, pubkey string) error {
	pk, err := NormalizePubkey(pubkey)
	if err != nil {
		return err
	}
	return expectOne(s.db.Exec(`DELETE FROM companion_links WHERE pubkey = ? AND user_id = ?`, pk, userID))
}

// LinkedPubkeys returns every linked pubkey, sorted. (The ingestor does not
// call this: it reads users.db read-only with raw SQL.)
func (s *Store) LinkedPubkeys() ([]string, error) {
	rows, err := s.db.Query(`SELECT pubkey FROM companion_links ORDER BY pubkey`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err != nil {
			return nil, err
		}
		out = append(out, pk)
	}
	return out, rows.Err()
}
