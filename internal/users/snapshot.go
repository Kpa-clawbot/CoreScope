package users

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Snapshot writes a consistent copy of users.db to path with VACUUM INTO
// (the technique of GET /api/backup) and makes it readable by the owner
// only: it holds password hashes and addresses. path must not exist; an
// empty file there is refused too, although VACUUM INTO would accept it.
// On failure no file is left at path.
func (s *Store) Snapshot(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("users: snapshot target %s already exists", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("users: snapshot target %s: %w", path, err)
	}
	if _, err := s.db.Exec(`VACUUM INTO ?`, path); err != nil {
		os.Remove(path)
		return fmt.Errorf("users: snapshot: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		os.Remove(path)
		return fmt.Errorf("users: snapshot mode: %w", err)
	}
	return nil
}
