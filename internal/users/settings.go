package users

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrSettingsConflict: PutSettings was given a base version that is not
// the stored one (another device wrote first, or the document was deleted
// and started again).
var ErrSettingsConflict = errors.New("users: settings revision conflict")

// SettingsVersion identifies one state of a user's settings document.
// Revisions restart at 1 after DeleteSettings, so the generation, a random
// id created with the document, tells a new document from the old one.
// The zero value means "no document".
type SettingsVersion struct {
	Revision   int64
	Generation string
}

func newGeneration() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("users: settings generation: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// GetSettings returns the user's synced settings document and its version;
// "" and the zero version when the user has none.
func (s *Store) GetSettings(userID int64) (string, SettingsVersion, error) {
	var doc string
	var v SettingsVersion
	err := s.db.QueryRow(`SELECT doc, revision, generation FROM user_settings WHERE user_id = ?`, userID).Scan(&doc, &v.Revision, &v.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		return "", SettingsVersion{}, nil
	}
	if err != nil {
		return "", SettingsVersion{}, err
	}
	return doc, v, nil
}

// PutSettings stores doc and returns the new version. Without a stored
// document base.Revision must be 0, and the write starts a new generation.
// With one, base must equal the stored version; the new revision is
// base.Revision+1 in the same generation. Otherwise it returns the stored
// version and ErrSettingsConflict.
func (s *Store) PutSettings(userID int64, base SettingsVersion, doc string) (SettingsVersion, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return SettingsVersion{}, err
	}
	defer tx.Rollback()
	var cur SettingsVersion
	err = tx.QueryRow(`SELECT revision, generation FROM user_settings WHERE user_id = ?`, userID).Scan(&cur.Revision, &cur.Generation)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SettingsVersion{}, err
	}
	next := cur
	if cur.Revision == 0 {
		if base.Revision != 0 {
			return cur, ErrSettingsConflict
		}
		if next.Generation, err = newGeneration(); err != nil {
			return SettingsVersion{}, err
		}
	} else if base != cur {
		return cur, ErrSettingsConflict
	}
	next.Revision = cur.Revision + 1
	if _, err := tx.Exec(`INSERT INTO user_settings (user_id, doc, revision, generation, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET doc = excluded.doc, revision = excluded.revision, generation = excluded.generation, updated_at = excluded.updated_at`,
		userID, doc, next.Revision, next.Generation, unix(s.now())); err != nil {
		return SettingsVersion{}, err
	}
	return next, tx.Commit()
}

// DeleteSettings removes the user's synced settings; the next write starts
// a new generation. No row is not an error.
func (s *Store) DeleteSettings(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM user_settings WHERE user_id = ?`, userID)
	return err
}
