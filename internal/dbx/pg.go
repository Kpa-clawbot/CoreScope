package dbx

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// ConnectWait bounds how long a first connect rides out a refused or
// unresolvable endpoint (a new pod's NetworkPolicy not admitting it yet).
const ConnectWait = 30 * time.Second

// QuoteIdent quotes s as a Postgres identifier.
func QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// IsPostgresURL reports whether s is a postgres:// or postgresql:// URL.
func IsPostgresURL(s string) bool {
	return strings.HasPrefix(s, "postgres://") || strings.HasPrefix(s, "postgresql://")
}

// PingFirst pings db once; on a transient connection error it logs through
// logf and retries for up to ConnectWait. Any other error is returned at once.
func PingFirst(ctx context.Context, db *sql.DB, logf func(format string, args ...any)) error {
	err := db.PingContext(ctx)
	if err == nil || !IsTransientConnError(err) {
		return err
	}
	logf("postgres not reachable yet, retrying for up to %s", ConnectWait)
	return PingWithRetry(ctx, db, ConnectWait)
}
