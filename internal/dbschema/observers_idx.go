package dbschema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// observations.observer_idx points at an observer row by integer. Until this
// migration that integer was the observers table's hidden implicit rowid (its
// declared key is `id TEXT PRIMARY KEY`). Implicit rowids are not part of the
// schema: a dump-and-reload (`sqlite3 .dump`, Python iterdump) renumbers them
// and silently re-links every observation to a different observer, and
// Postgres has no rowid at all. ensureObserversIdx rebuilds observers with an
// explicit `idx INTEGER PRIMARY KEY AUTOINCREMENT` that carries every existing
// rowid value over unchanged.
//
// In SQLite an INTEGER PRIMARY KEY column IS the rowid, so after the rebuild
// rowid == idx forever: an older server or ingestor that still joins on the
// implicit rowid keeps resolving the same observer (rolling upgrade and
// rollback are both safe), and an old ingestor that inserts observers without idx gets
// one auto-assigned.

// observersColumns is the canonical observers shape, in table order. The
// rebuild refuses any table carrying a column outside this list (it would be
// dropped) and copies the intersection, so a legacy table missing a column
// gets that column's default. Keep in sync with the CREATE TABLE observers
// in cmd/ingestor/db.go applySchema (TestFreshObserversMatchesRebuiltShape
// in cmd/ingestor pins the parity).
var observersColumns = []struct{ name, ddl string }{
	{"idx", "idx INTEGER PRIMARY KEY AUTOINCREMENT"},
	{"id", "id TEXT NOT NULL UNIQUE"},
	{"name", "name TEXT"},
	{"iata", "iata TEXT"},
	{"last_seen", "last_seen TEXT"},
	{"first_seen", "first_seen TEXT"},
	{"packet_count", "packet_count INTEGER DEFAULT 0"},
	{"model", "model TEXT"},
	{"firmware", "firmware TEXT"},
	{"client_version", "client_version TEXT"},
	{"radio", "radio TEXT"},
	{"battery_mv", "battery_mv INTEGER"},
	{"uptime_secs", "uptime_secs INTEGER"},
	{"noise_floor", "noise_floor REAL"},
	{"inactive", "inactive INTEGER DEFAULT 0"},
	{"last_packet_at", "last_packet_at TEXT DEFAULT NULL"},
	{"clock_skew_seconds", "clock_skew_seconds INTEGER DEFAULT NULL"},
	{"clock_skew_count_24h", "clock_skew_count_24h INTEGER DEFAULT 0"},
	{"clock_last_naive_at", "clock_last_naive_at TEXT DEFAULT NULL"},
	{"can_relay", "can_relay INTEGER DEFAULT 1"},
	{"can_relay_seen", "can_relay_seen INTEGER DEFAULT 0"},
}

// packetsViewSQL is the single definition of packets_v (v3 schema:
// observations.observer_idx → observers.idx).
const packetsViewSQL = `
	CREATE VIEW packets_v AS
		SELECT o.id, COALESCE(o.raw_hex, t.raw_hex) AS raw_hex,
			   datetime(o.timestamp, 'unixepoch') AS timestamp,
			   obs.id AS observer_id, obs.name AS observer_name,
			   o.direction, o.snr, o.rssi, o.score, t.hash, t.route_type,
			   t.payload_type, t.payload_version, o.path_json, t.decoded_json,
			   t.created_at
		FROM observations o
		JOIN transmissions t ON t.id = o.transmission_id
		LEFT JOIN observers obs ON obs.idx = o.observer_idx AND (obs.inactive IS NULL OR obs.inactive = 0)`

// EnsurePacketsView drops and recreates packets_v. The ingestor calls it on
// every boot so the view always matches packetsViewSQL; fresh installs need
// it or readers get "no such table: packets_v".
func EnsurePacketsView(rw *sql.DB) error {
	if _, err := rw.Exec(`DROP VIEW IF EXISTS packets_v`); err != nil {
		return fmt.Errorf("drop packets_v: %w", err)
	}
	if _, err := rw.Exec(packetsViewSQL); err != nil {
		return fmt.Errorf("create packets_v: %w", err)
	}
	return nil
}

// observersColumnsInfo returns the observers table's column names
// (lower-cased; empty if the table doesn't exist) and whether idx is already
// its INTEGER PRIMARY KEY.
func observersColumnsInfo(ctx context.Context, q interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}) (cols []string, idxIsPK bool, err error) {
	rows, err := q.QueryContext(ctx, `PRAGMA table_info(observers)`)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name string
		var ctype, dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, false, err
		}
		name = strings.ToLower(name)
		cols = append(cols, name)
		if name == "idx" && pk == 1 {
			idxIsPK = true
		}
	}
	return cols, idxIsPK, rows.Err()
}

// ensureObserversIdx rebuilds a legacy observers table (implicit rowid only)
// into the explicit-idx shape, preserving every row's rowid as its idx so all
// existing observations.observer_idx values keep pointing at the same
// observer. Idempotent: a table that already has idx as its primary key is
// left alone. Fails closed — without touching the table — on any column the
// canonical shape doesn't know (the rebuild would drop it) or a NULL id (a
// legacy `TEXT PRIMARY KEY` admits one; the new NOT NULL would reject it).
//
// MUST run last in Apply: the ensure*Column steps before it bring a legacy
// table up to (a subset of) the canonical columns.
func ensureObserversIdx(rw *sql.DB, logf Logger) (err error) {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	ctx := context.Background()
	conn, err := rw.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	cols, idxIsPK, err := observersColumnsInfo(ctx, conn)
	if err != nil {
		return fmt.Errorf("inspect observers: %w", err)
	}
	if idxIsPK || len(cols) == 0 {
		return nil // already migrated, or no observers table to migrate
	}

	// foreign_keys is a no-op inside a transaction, so toggle it on the pinned
	// connection around BEGIN/COMMIT, and restore the caller's setting on
	// every path (the ingestor's writer DSN runs with it on).
	var fkPrev int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fkPrev); err != nil {
		return fmt.Errorf("read foreign_keys: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("disable foreign_keys: %w", err)
	}
	defer func() {
		if _, rerr := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA foreign_keys=%d`, fkPrev)); rerr != nil && err == nil {
			err = fmt.Errorf("restore foreign_keys: %w", rerr)
		}
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Re-read inside the transaction so the checks and the copy see the
	// same table.
	cols, idxIsPK, err = observersColumnsInfo(ctx, tx)
	if err != nil {
		return fmt.Errorf("inspect observers: %w", err)
	}
	if idxIsPK {
		return tx.Commit()
	}
	known := make(map[string]bool, len(observersColumns))
	for _, c := range observersColumns {
		known[c.name] = true
	}
	var unknown []string
	hasCol := make(map[string]bool, len(cols))
	for _, c := range cols {
		hasCol[c] = true
		// idx is canonical only as the PRIMARY KEY; a plain idx column here
		// would be clobbered by the copy, so it counts as unknown.
		if !known[c] || c == "idx" {
			unknown = append(unknown, c)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("observers has columns outside the canonical shape (%s); refusing to rebuild it — they would be dropped", strings.Join(unknown, ", "))
	}
	if !hasCol["id"] {
		return fmt.Errorf("observers has no id column; refusing to rebuild it")
	}
	var nullIDs int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM observers WHERE id IS NULL`).Scan(&nullIDs); err != nil {
		return fmt.Errorf("count NULL observer ids: %w", err)
	}
	if nullIDs > 0 {
		return fmt.Errorf("observers has %d row(s) with NULL id; refusing to rebuild it (the new id column is NOT NULL)", nullIDs)
	}

	defs := make([]string, 0, len(observersColumns))
	copyCols := []string{}
	for _, c := range observersColumns {
		defs = append(defs, c.ddl)
		if c.name != "idx" && hasCol[c.name] {
			copyCols = append(copyCols, c.name)
		}
	}
	colList := strings.Join(copyCols, ", ")

	// packets_v joins observers; modern SQLite refuses ALTER TABLE RENAME
	// while a view references a table that no longer exists.
	if _, err := tx.ExecContext(ctx, `DROP VIEW IF EXISTS packets_v`); err != nil {
		return fmt.Errorf("drop packets_v: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS observers_new`); err != nil {
		return fmt.Errorf("drop stale observers_new: %w", err)
	}
	// PREFLIGHT: async=true reason="full rebuild of observers, a low-cardinality table (~1k rows in prod): one INSERT…SELECT copy plus two index builds over ~1k rows, milliseconds at any deployment size; observations (1.9M+) is never scanned — only MAX(observer_idx) via idx_observations_observer_idx"
	if _, err := tx.ExecContext(ctx, `CREATE TABLE observers_new (`+strings.Join(defs, ", ")+`)`); err != nil {
		return fmt.Errorf("create observers_new: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO observers_new (idx, `+colList+`) SELECT rowid, `+colList+` FROM observers`); err != nil {
		return fmt.Errorf("copy observers: %w", err)
	}
	var before, after int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM observers`).Scan(&before); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM observers_new`).Scan(&after); err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("observers rebuild copied %d of %d rows; rolled back", after, before)
	}
	if err := seedObserversSequence(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE observers`); err != nil {
		return fmt.Errorf("drop old observers: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE observers_new RENAME TO observers`); err != nil {
		return fmt.Errorf("rename observers_new: %w", err)
	}
	for _, s := range []string{
		`CREATE INDEX IF NOT EXISTS idx_observers_last_seen ON observers(last_seen)`,
		`CREATE INDEX IF NOT EXISTS idx_observers_iata_norm ON observers(UPPER(TRIM(iata)))`,
		packetsViewSQL,
	} {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("observers rebuild: %q: %w", s, err)
		}
	}
	if err := checkObserversForeignKeys(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit observers rebuild: %w", err)
	}
	logf("[dbschema] rebuilt observers with explicit idx primary key (%d rows, every rowid preserved as idx)", after)
	return nil
}

// seedObserversSequence makes AUTOINCREMENT start above every integer an
// observation already references, not just above the surviving observers.
// A legacy table could reuse the rowid of a deleted max row, re-linking that
// row's orphaned observations to a brand-new observer; seeding past
// MAX(observations.observer_idx) closes that for good. The MAX is an index
// seek (idx_observations_observer_idx, ensured earlier in Apply).
func seedObserversSequence(ctx context.Context, tx *sql.Tx) error {
	hasObsIdx, err := TableHasColumn(tx, "observations", "observer_idx")
	if err != nil {
		return err
	}
	if !hasObsIdx {
		return nil
	}
	var maxRef sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(observer_idx) FROM observations`).Scan(&maxRef); err != nil {
		return fmt.Errorf("max observations.observer_idx: %w", err)
	}
	if !maxRef.Valid {
		return nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = MAX(seq, ?) WHERE name = 'observers_new'`, maxRef.Int64)
	if err != nil {
		return fmt.Errorf("seed observers sequence: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 && maxRef.Int64 > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sqlite_sequence (name, seq) VALUES ('observers_new', ?)`, maxRef.Int64); err != nil {
			return fmt.Errorf("seed observers sequence: %w", err)
		}
	}
	return nil
}

// checkObserversForeignKeys runs PRAGMA foreign_key_check on observers and on
// every table whose foreign keys reference it — the only rows the rebuild can
// have invalidated. A whole-database check would scan observations (1.9M+
// rows in prod) and fail on unrelated pre-existing orphans.
func checkObserversForeignKeys(ctx context.Context, tx *sql.Tx) error {
	tables := []string{"observers"}
	rows, err := tx.QueryContext(ctx, `SELECT m.name FROM sqlite_master m, pragma_foreign_key_list(m.name) f
		WHERE m.type = 'table' AND f."table" = 'observers' GROUP BY m.name`)
	if err != nil {
		return fmt.Errorf("list tables referencing observers: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, table := range tables {
		var violations int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check(?)`, table).Scan(&violations); err != nil {
			return fmt.Errorf("foreign_key_check(%s): %w", table, err)
		}
		if violations > 0 {
			return fmt.Errorf("observers rebuild left %d foreign key violation(s) in %s; rolled back", violations, table)
		}
	}
	return nil
}
