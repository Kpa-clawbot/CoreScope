// Package pgschema creates and upgrades the CoreScope schema on PostgreSQL.
//
// Unlike SQLite, which carries years of in-place ALTER/backfill history in
// cmd/ingestor/db.go and the parent dbschema package, a Postgres database is
// only ever created at the current end state. Its schema is therefore a short
// list of forward-only, versioned SQL migrations (migrations/NNNN_name.sql),
// recorded in schema_version. As with SQLite (#1283), only the ingestor calls
// Apply; the server calls AssertReady.
package pgschema

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/meshcore-analyzer/dbschema"
)

// LatestVersion is the highest migration version shipped.
const LatestVersion = 1

// minServerMajor is the oldest Postgres major version CoreScope supports.
const minServerMajor = 16

// advisoryLockKey serialises Apply across every process on the database.
// The value is arbitrary but must never change: two binaries using different
// keys would not exclude each other. (ASCII "CoreScop".)
const advisoryLockKey int64 = 0x436f726553636f70

// nowText is SQLite's datetime('now') format, so applied_at reads the same
// as every other timestamp default in the schema.
const nowText = `to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS')`

//go:embed migrations/*.sql
var migrationFS embed.FS

// migration is one embedded SQL file.
type migration struct {
	version int
	name    string // file name, for logs and errors
	sql     string
}

// Apply runs every pending migration in version order, each in its own
// transaction, recording it in schema_version(version INTEGER PRIMARY KEY,
// applied_at TEXT NOT NULL). It holds pg_advisory_lock(<fixed int64 key>) on
// one pinned connection for the whole run so two ingestors cannot race, and
// refuses (error) if the server's major version is < 16 or the database
// already has a schema_version above LatestVersion (newer binary wrote it).
func Apply(ctx context.Context, db *sql.DB, logf func(string, ...any)) error {
	migs, err := loadMigrations(migrationFS)
	if err != nil {
		return err
	}
	return apply(ctx, db, logf, migs)
}

// AssertReady returns nil when schema_version reaches LatestVersion, else an
// error telling the operator to start the ingestor first.
func AssertReady(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pgschema: %w", err)
	}
	defer conn.Close()
	v, err := currentVersion(ctx, conn)
	if err != nil {
		return err
	}
	if v < LatestVersion {
		return fmt.Errorf("pgschema: database schema is at version %d, need %d: start the ingestor first so it can create or upgrade the schema", v, LatestVersion)
	}
	return nil
}

// loadMigrations reads migrations/NNNN_name.sql from fsys and checks that the
// versions run 1..n without gaps or duplicates.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	names, err := fs.Glob(fsys, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("pgschema: list migrations: %w", err)
	}
	migs := make([]migration, 0, len(names))
	for _, p := range names {
		base := path.Base(p)
		num, _, ok := strings.Cut(base, "_")
		v, err := strconv.Atoi(num)
		if !ok || len(num) != 4 || err != nil || v < 1 {
			return nil, fmt.Errorf("pgschema: migration file %q is not named NNNN_name.sql", base)
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil, fmt.Errorf("pgschema: read %s: %w", base, err)
		}
		migs = append(migs, migration{version: v, name: base, sql: string(body)})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	for i, m := range migs {
		if m.version != i+1 {
			return nil, fmt.Errorf("pgschema: migration %s: expected version %d (versions must run 1..n with no gaps or duplicates)", m.name, i+1)
		}
	}
	return migs, nil
}

// apply is Apply over an explicit migration list; the highest version in
// migs plays the role of LatestVersion. Tests use it to inject migrations.
func apply(ctx context.Context, db *sql.DB, logf func(string, ...any), migs []migration) (err error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	latest := 0
	if n := len(migs); n > 0 {
		latest = migs[n-1].version
	}

	// Session-level advisory locks belong to a connection, so the lock, the
	// version read and every migration transaction share one pinned conn.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pgschema: %w", err)
	}
	defer conn.Close()

	if err := checkServerVersion(ctx, conn); err != nil {
		return err
	}

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(?)`, advisoryLockKey); err != nil {
		return fmt.Errorf("pgschema: acquire migration lock: %w", err)
	}
	defer func() {
		// Background, not ctx: a cancelled ctx must not leave the lock held.
		var released bool
		uerr := conn.QueryRowContext(context.Background(), `SELECT pg_advisory_unlock(?)`, advisoryLockKey).Scan(&released)
		if uerr == nil && released {
			return
		}
		// The lock may still be held by this session. Closing the session
		// is the only sure way to drop it, so keep this connection out of
		// the pool: database/sql closes a conn whose Raw returns ErrBadConn.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		if err == nil {
			if uerr == nil {
				uerr = errors.New("lock was not held")
			}
			err = fmt.Errorf("pgschema: release migration lock: %w", uerr)
		}
	}()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("pgschema: create schema_version: %w", err)
	}

	current, err := currentVersion(ctx, conn)
	if err != nil {
		return err
	}
	if current > latest {
		return fmt.Errorf("pgschema: database schema is at version %d but this binary only knows up to %d; a newer CoreScope has upgraded it, run that version", current, latest)
	}

	for _, m := range migs {
		if m.version <= current {
			continue
		}
		if err := applyOne(ctx, conn, m); err != nil {
			return err
		}
		logf("[pgschema] applied migration %s (schema version %d)", m.name, m.version)
	}
	return nil
}

// applyOne runs one migration and records it, atomically.
func applyOne(ctx context.Context, conn *sql.Conn, m migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pgschema: migration %s: begin: %w", m.name, err)
	}
	defer tx.Rollback() // no-op after Commit
	// No arguments, so pgx sends this over the simple protocol, which
	// accepts a whole multi-statement file.
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("pgschema: migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version, applied_at) VALUES (?, `+nowText+`)`, m.version); err != nil {
		return fmt.Errorf("pgschema: migration %s: record version: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pgschema: migration %s: commit: %w", m.name, err)
	}
	return nil
}

// currentVersion returns MAX(version) from schema_version, or 0 when the
// table does not exist yet.
func currentVersion(ctx context.Context, conn *sql.Conn) (int, error) {
	exists, err := tableExists(ctx, conn, "schema_version")
	if err != nil {
		return 0, fmt.Errorf("pgschema: probe schema_version: %w", err)
	}
	if !exists {
		return 0, nil
	}
	var v int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("pgschema: read schema_version: %w", err)
	}
	return v, nil
}

// tableExists runs dbschema.TableExists on a pinned connection under ctx.
func tableExists(ctx context.Context, conn *sql.Conn, table string) (bool, error) {
	return dbschema.TableExists(connQuerier{ctx, conn}, table)
}

// connQuerier adapts a context-bound *sql.Conn to dbschema.Querier.
type connQuerier struct {
	ctx  context.Context
	conn *sql.Conn
}

func (q connQuerier) Query(query string, args ...any) (*sql.Rows, error) {
	return q.conn.QueryContext(q.ctx, query, args...)
}

func (q connQuerier) QueryRow(query string, args ...any) *sql.Row {
	return q.conn.QueryRowContext(q.ctx, query, args...)
}

// checkServerVersion refuses servers older than minServerMajor.
func checkServerVersion(ctx context.Context, conn *sql.Conn) error {
	var num string
	if err := conn.QueryRowContext(ctx, `SELECT current_setting('server_version_num')`).Scan(&num); err != nil {
		return fmt.Errorf("pgschema: read server version: %w", err)
	}
	return checkServerVersionNum(num)
}

// checkServerVersionNum validates a server_version_num string (e.g. "170002").
func checkServerVersionNum(num string) error {
	n, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil {
		return fmt.Errorf("pgschema: unparseable server_version_num %q", num)
	}
	if major := n / 10000; major < minServerMajor {
		return fmt.Errorf("pgschema: PostgreSQL %d is not supported; CoreScope needs PostgreSQL %d or newer", major, minServerMajor)
	}
	return nil
}
