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
	if err := lockUser(tx, userID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`UPDATE tokens SET used_at = $1 WHERE user_id = $2 AND purpose = $3 AND used_at IS NULL`,
		unix(now), userID, string(p)); err != nil {
		return "", err
	}
	var ne any
	if newEmail != "" {
		ne = newEmail
	}
	if _, err := tx.Exec(`INSERT INTO tokens (token_hash, user_id, purpose, new_email, expires_at) VALUES ($1, $2, $3, $4, $5)`,
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
	err = q.QueryRow(`SELECT user_id, purpose, new_email, expires_at, used_at FROM tokens WHERE token_hash = $1`, hash).
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
	if err := lockUser(tx, userID); err != nil {
		return 0, "", ErrTokenInvalid
	}
	userID, newEmail, err = s.checkToken(tx, hash, p)
	if err != nil {
		return 0, "", err
	}
	if err := expectOne(tx.Exec(`UPDATE tokens SET used_at = $1 WHERE token_hash = $2 AND used_at IS NULL`, unix(s.now()), hash)); err != nil {
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

// ActivateWithToken burns an activation token of user id and activates the
// account with role in one transaction, but only while the user is still
// pending with verifiedHash, the hash the caller checked the password
// against. Otherwise it returns ErrAccountChanged and burns nothing.
// A token of another purpose or user returns ErrTokenInvalid.
func (s *Store) ActivateWithToken(raw string, id int64, role Role, verifiedHash string) error {
	hash := HashToken(raw)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockUser(tx, id); err != nil {
		return ErrAccountChanged
	}
	uid, _, err := s.checkToken(tx, hash, PurposeActivate)
	if err != nil {
		return err
	}
	if uid != id {
		return ErrTokenInvalid
	}
	now := unix(s.now())
	if err := expectOne(tx.Exec(`UPDATE tokens SET used_at = $1 WHERE token_hash = $2 AND used_at IS NULL`, now, hash)); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrTokenInvalid
		}
		return err
	}
	err = expectOne(tx.Exec(`UPDATE users SET status = 'active', role = $1, activated_at = $2, activated_by = NULL
		WHERE id = $3 AND status = 'pending' AND password_hash = $4`, string(role), now, id, verifiedHash))
	if errors.Is(err, ErrNotFound) {
		return ErrAccountChanged
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// InvalidateTokens burns all of a user's unused tokens for purpose p.
func (s *Store) InvalidateTokens(userID int64, p Purpose) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockUser(tx, userID); errors.Is(err, ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE tokens SET used_at=$1 WHERE user_id=$2 AND purpose=$3 AND used_at IS NULL`, unix(s.now()), userID, string(p)); err != nil {
		return err
	}
	return tx.Commit()
}

// PendingEmailChange returns the new address of the user's unused,
// unexpired email change, or "" when there is none. Only the address is
// returned, never the token or its hash.
func (s *Store) PendingEmailChange(userID int64) (string, error) {
	var ne sql.NullString
	err := s.db.QueryRow(`SELECT new_email FROM tokens
		WHERE user_id = $1 AND purpose = $2 AND used_at IS NULL AND expires_at > $3
		ORDER BY expires_at DESC LIMIT 1`, userID, string(PurposeEmailChange), unix(s.now())).Scan(&ne)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return ne.String, err
}

// PruneTokens deletes tokens that expired more than keep ago.
func (s *Store) PruneTokens(keep time.Duration) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM tokens WHERE expires_at < $1`, unix(s.now())-int64(keep/time.Second))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
