package users

import (
	"context"
	"errors"
	"github.com/meshcore-analyzer/pgutil"
)

// Snapshot writes a consistent PostgreSQL custom-format archive readable only
// by its owner. Restore with pg_restore; the destination must not exist.
func (s *Store) Snapshot(path string) error {
	return s.SnapshotContext(context.Background(), path)
}

// SnapshotContext cancels the dump when the caller disconnects or times out.
func (s *Store) SnapshotContext(ctx context.Context, path string) error {
	if err := s.db.PingContext(ctx); err != nil {
		return errors.New("users: account database is not available for snapshot")
	}
	return pgutil.Dump(ctx, s.databaseURL, path)
}
