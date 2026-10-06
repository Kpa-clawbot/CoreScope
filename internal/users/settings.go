package users

import (
	"database/sql"
	"errors"
)

// ErrSettingsConflict: PutSettings was given a base revision that is not
// the stored one (another device wrote first).
var ErrSettingsConflict = errors.New("users: settings revision conflict")

// GetSettings returns the user's synced settings document and its
// revision; "" and 0 when the user has none.
func (s *Store) GetSettings(userID int64) (string, int64, error) {
	var doc string
	var rev int64
	err := s.db.QueryRow(`SELECT doc, revision FROM user_settings WHERE user_id = ?`, userID).Scan(&doc, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	return doc, rev, nil
}

// PutSettings stores doc when the stored revision equals baseRevision (0 =
// no row yet) and returns the new revision, baseRevision+1. Otherwise it
// returns the stored revision and ErrSettingsConflict.
func (s *Store) PutSettings(userID, baseRevision int64, doc string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var cur int64
	err = tx.QueryRow(`SELECT revision FROM user_settings WHERE user_id = ?`, userID).Scan(&cur)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if cur != baseRevision {
		return cur, ErrSettingsConflict
	}
	next := baseRevision + 1
	if _, err := tx.Exec(`INSERT INTO user_settings (user_id, doc, revision, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET doc = excluded.doc, revision = excluded.revision, updated_at = excluded.updated_at`,
		userID, doc, next, unix(s.now())); err != nil {
		return 0, err
	}
	return next, tx.Commit()
}

// DeleteSettings removes the user's synced settings. No row is not an error.
func (s *Store) DeleteSettings(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM user_settings WHERE user_id = ?`, userID)
	return err
}
