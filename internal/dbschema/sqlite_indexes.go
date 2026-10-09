package dbschema

import (
	"context"
	"database/sql"
)

// EnsureSQLiteObserverTimeIndex is shared by the online asynchronous migration
// and offline conversion. PostgreSQL creates its own index during owner setup.
// A transaction or reserved connection may be used during offline finalization.
func EnsureSQLiteObserverTimeIndex(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	_, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_observations_observer_idx_timestamp ON observations(observer_idx, timestamp)`)
	return err
}
