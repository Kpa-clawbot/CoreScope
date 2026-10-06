package users

import (
	"database/sql"
	"errors"
	"time"
)

// Purpose is what a one-time link may be used for.
type Purpose string

const (
	PurposeActivate    Purpose = "activate"
	PurposeReset       Purpose = "reset"
	PurposeEmailChange Purpose = "email_change"
)

// IssueToken creates a one-time token and invalidates the user's earlier
// unused tokens for the same purpose, so only the newest link works.
// newEmail is stored for PurposeEmailChange and ignored when empty.
func (s *Store) IssueToken(userID int64, p Purpose, ttl time.Duration, newEmail string) (string, error) {
	raw, hash, err := NewToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE tokens SET used_at = ? WHERE user_id = ? AND purpose = ? AND used_at IS NULL`,
		unix(now), userID, string(p)); err != nil {
		return "", err
	}
	var ne any
	if newEmail != "" {
		ne = newEmail
	}
	if _, err := tx.Exec(`INSERT INTO tokens (token_hash, user_id, purpose, new_email, expires_at) VALUES (?, ?, ?, ?, ?)`,
		hash, userID, string(p), ne, unix(now.Add(ttl))); err != nil {
		return "", err
	}
	return raw, tx.Commit()
}

// tokenQuerier is satisfied by *sql.DB and *sql.Tx.
type tokenQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// checkToken validates an unused, unexpired token of purpose p by hash
// without changing anything.
func (s *Store) checkToken(q tokenQuerier, hash string, p Purpose) (userID int64, newEmail string, err error) {
	var purpose string
	var expires int64
	var ne sql.NullString
	var used sql.NullInt64
	err = q.QueryRow(`SELECT user_id, purpose, new_email, expires_at, used_at FROM tokens WHERE token_hash = ?`, hash).
		Scan(&userID, &purpose, &ne, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ErrTokenInvalid
	}
	if err != nil {
		return 0, "", err
	}
	if purpose != string(p) || used.Valid {
		return 0, "", ErrTokenInvalid
	}
	if unix(s.now()) >= expires {
		return 0, "", ErrTokenExpired
	}
	return userID, ne.String, nil
}

// TokenUser returns the user a token belongs to with the same checks as
// ConsumeToken, but does not burn it.
func (s *Store) TokenUser(raw string, p Purpose) (int64, error) {
	uid, _, err := s.checkToken(s.db, HashToken(raw), p)
	return uid, err
}

// ConsumeToken validates and burns a token. A purpose mismatch returns
// ErrTokenInvalid without burning it. Expired tokens return ErrTokenExpired.
func (s *Store) ConsumeToken(raw string, p Purpose) (userID int64, newEmail string, err error) {
	hash := HashToken(raw)
	tx, err := s.db.Begin()
	if err != nil {
		return 0, "", err
	}
	defer tx.Rollback()
	userID, newEmail, err = s.checkToken(tx, hash, p)
	if err != nil {
		return 0, "", err
	}
	if err := expectOne(tx.Exec(`UPDATE tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL`, unix(s.now()), hash)); err != nil {
		if errors.Is(err, ErrNotFound) {
			return 0, "", ErrTokenInvalid
		}
		return 0, "", err
	}
	if err := tx.Commit(); err != nil {
		return 0, "", err
	}
	return userID, newEmail, nil
}

// InvalidateTokens burns all of a user's unused tokens for purpose p.
func (s *Store) InvalidateTokens(userID int64, p Purpose) error {
	_, err := s.db.Exec(`UPDATE tokens SET used_at = ? WHERE user_id = ? AND purpose = ? AND used_at IS NULL`,
		unix(s.now()), userID, string(p))
	return err
}

// PruneTokens deletes tokens that expired more than keep ago.
func (s *Store) PruneTokens(keep time.Duration) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM tokens WHERE expires_at < ?`, unix(s.now())-int64(keep/time.Second))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
