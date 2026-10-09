package users

import (
	"context"
	"errors"
	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/pgutil"
	"os"
	"path/filepath"
)

// Snapshot writes a consistent, owner-readable native backup. SQLite produces
// a standalone database; PostgreSQL produces a pg_restore archive. The target
// must not exist, including an existing empty file.
func (s *Store) Snapshot(path string) error {
	return s.SnapshotContext(context.Background(), path)
}

// SnapshotContext cancels the native backup when the caller disconnects or times out.
func (s *Store) SnapshotContext(ctx context.Context, path string) error {
	if err := s.db.PingContext(ctx); err != nil {
		return errors.New("users: account database is not available for snapshot")
	}
	if s.backend == dbconfig.Postgres {
		return pgutil.Dump(ctx, s.target, path)
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("users: snapshot target already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// VACUUM INTO accepts a pre-created empty file, so private mode applies before
	// any account data is written. Publish with a hard link to avoid overwriting a
	// target created by another caller while the snapshot was running.
	f, err := os.CreateTemp(filepath.Dir(path), ".accounts-snapshot-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?1`, temporary); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Link(temporary, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("users: snapshot target already exists")
		}
		return err
	}
	return nil
}
