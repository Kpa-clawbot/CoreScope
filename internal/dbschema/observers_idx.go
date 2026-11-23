package dbschema

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
)

// sqlRunner is the statement surface shared by *sql.Conn, *sql.Tx and
// *sql.DB. The observers rebuild runs every statement of its transaction on
// one pinned *sql.Conn, so its helpers take this, not *sql.Tx.
type sqlRunner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

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

// The observers key packets_v joins observations.observer_idx on.
// legacyObserversKey is the implicit rowid of a table not yet rebuilt; in
// SQLite both name the same value (idx is the rowid alias), so the view
// resolves the same observer either way.
const (
	observersKey       = "idx"
	legacyObserversKey = "rowid"
)

// packetsViewSQL is the single definition of packets_v (v3 schema:
// observations.observer_idx → observers.<key>).
func packetsViewSQL(key string) string {
	return `
	CREATE VIEW packets_v AS
		SELECT o.id, COALESCE(o.raw_hex, t.raw_hex) AS raw_hex,
			   datetime(o.timestamp, 'unixepoch') AS timestamp,
			   obs.id AS observer_id, obs.name AS observer_name,
			   o.direction, o.snr, o.rssi, o.score, t.hash, t.route_type,
			   t.payload_type, t.payload_version, o.path_json, t.decoded_json,
			   t.created_at
		FROM observations o
		JOIN transmissions t ON t.id = o.transmission_id
		LEFT JOIN observers obs ON obs.` + key + ` = o.observer_idx AND (obs.inactive IS NULL OR obs.inactive = 0)`
}

// EnsurePacketsView drops and recreates packets_v. The ingestor calls it on
// every boot so the view always matches packetsViewSQL; fresh installs need
// it or readers get "no such table: packets_v". It joins on idx once
// observers has it, and on the rowid while observers is still the legacy
// shape: before the rebuild, or for good when the rebuild refused. SQLite
// checks a view's columns only when it is read, so an idx join over a
// legacy table would be created fine and then fail every read.
func EnsurePacketsView(rw *sql.DB) error {
	cols, idxIsPK, err := observersColumnsInfo(context.Background(), rw)
	if err != nil {
		return fmt.Errorf("inspect observers: %w", err)
	}
	key := observersKey
	if len(cols) > 0 && !idxIsPK {
		key = legacyObserversKey
	}
	if _, err := rw.Exec(`DROP VIEW IF EXISTS packets_v`); err != nil {
		return fmt.Errorf("drop packets_v: %w", err)
	}
	if _, err := rw.Exec(packetsViewSQL(key)); err != nil {
		return fmt.Errorf("create packets_v: %w", err)
	}
	return nil
}

// observersColumnsInfo returns the observers table's column names
// (lower-cased; empty if the table doesn't exist) and whether idx is already
// its rowid alias — the only key observations.observer_idx can rely on. That
// takes all of: idx is the sole primary-key column, declared exactly INTEGER,
// and the key has no separate index. SQLite builds a "pk"-origin index for
// any primary key that is not the rowid, which also catches the two shapes
// that pass the first tests yet alias nothing: `INTEGER PRIMARY KEY DESC`
// (a documented quirk) and a WITHOUT ROWID table.
func observersColumnsInfo(ctx context.Context, q sqlRunner) (cols []string, idxIsPK bool, err error) {
	rows, err := q.QueryContext(ctx, `PRAGMA table_info(observers)`)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	idxIntegerPK, otherPK := false, false
	for rows.Next() {
		var cid, notnull, pk int
		var name string
		var ctype, dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, false, err
		}
		name = strings.ToLower(name)
		cols = append(cols, name)
		switch {
		case name == "idx" && pk == 1:
			idxIntegerPK = strings.EqualFold(strings.TrimSpace(ctype.String), "INTEGER")
		case pk > 0:
			otherPK = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	// Close before the next query: on a one-connection pool (the ingestor's)
	// a second query with these rows still open would wait forever.
	rows.Close()
	if !idxIntegerPK || otherPK {
		return cols, false, nil
	}
	pkIndexes, err := q.QueryContext(ctx, `SELECT 1 FROM pragma_index_list('observers') WHERE origin = 'pk'`)
	if err != nil {
		return nil, false, err
	}
	defer pkIndexes.Close()
	if pkIndexes.Next() {
		return cols, false, nil
	}
	return cols, true, pkIndexes.Err()
}

// ensureObserversIdx rebuilds a legacy observers table (implicit rowid only)
// into the explicit-idx shape, preserving every row's rowid as its idx so all
// existing observations.observer_idx values keep pointing at the same
// observer. Idempotent: a table whose idx is already the rowid alias (see
// observersColumnsInfo) is left alone. Fails closed — without touching the
// table — on any column the canonical shape doesn't know (the rebuild would
// drop it), an idx that is not the rowid alias (the copy would overwrite it)
// or a NULL id (a legacy `TEXT PRIMARY KEY` admits one; the new NOT NULL
// would reject it).
// Indexes, triggers and views the operator added are recreated over the new
// table (captureLocalObservers); one that no longer applies rolls the whole
// rebuild back, naming it.
//
// The rebuild is one BEGIN IMMEDIATE … COMMIT issued as plain statements on a
// pinned connection (database/sql's Tx can only BEGIN deferred with this
// driver). Every exit that does not COMMIT issues ROLLBACK on that same
// connection, so it never goes back to the pool inside a transaction.
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
	// Set when a failed ROLLBACK made us close the connection for good: its
	// foreign_keys setting went with it.
	discarded := false
	defer func() {
		if discarded {
			return
		}
		if _, rerr := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA foreign_keys=%d`, fkPrev)); rerr != nil && err == nil {
			err = fmt.Errorf("restore foreign_keys: %w", rerr)
		}
	}()

	// Take the write lock before the first read. A deferred transaction
	// that reads and then writes, while another process rebuilds the same
	// file, holds a stale WAL snapshot and fails with "database is locked"
	// without waiting (busy_timeout does not apply to a snapshot upgrade).
	// BEGIN IMMEDIATE waits on busy_timeout instead, so concurrent rebuilds
	// serialise and the later one sees the table done. It compiles nothing
	// against observers, so a table, trigger or view the operator left
	// broken cannot fail it; the refusals below report a malformed table
	// themselves.
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin observers rebuild: %w", err)
	}
	committed := false
	// Registered after the foreign_keys restore and conn.Close, so it runs
	// before both, on every return that did not COMMIT and on a panic.
	defer func() {
		if committed {
			return
		}
		var rerr error
		if discarded, rerr = rollbackOrDiscard(ctx, conn); rerr != nil && err == nil {
			err = rerr
		}
	}()

	// Re-read inside the transaction so the checks and the copy see the
	// same table.
	cols, idxIsPK, err = observersColumnsInfo(ctx, conn)
	if err != nil {
		return fmt.Errorf("inspect observers: %w", err)
	}
	if idxIsPK || len(cols) == 0 {
		// Rebuilt, or dropped, by another process meanwhile; the deferred
		// ROLLBACK ends the empty transaction.
		return nil
	}
	known := make(map[string]bool, len(observersColumns))
	for _, c := range observersColumns {
		known[c.name] = true
	}
	var unknown []string
	hasCol := make(map[string]bool, len(cols))
	for _, c := range cols {
		hasCol[c] = true
		if !known[c] {
			unknown = append(unknown, c)
		}
	}
	// idx is canonical only as the rowid alias; any other idx column (plain,
	// or a key that is not the rowid) would be clobbered by the copy.
	if hasCol["idx"] {
		return refuseNonAliasIdx(ctx, conn)
	}
	if len(unknown) > 0 {
		return fmt.Errorf("observers has columns outside the canonical shape (%s); refusing to rebuild it — they would be dropped. "+
			"To continue, save their data and drop them (ALTER TABLE observers DROP COLUMN <name>) — %s — "+
			"or run the previous CoreScope version", strings.Join(unknown, ", "), dropColumnDependentsHint)
	}
	if !hasCol["id"] {
		return fmt.Errorf("observers has no id column; refusing to rebuild it")
	}
	var nullIDs int64
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM observers WHERE id IS NULL`).Scan(&nullIDs); err != nil {
		return fmt.Errorf("count NULL observer ids: %w", err)
	}
	if nullIDs > 0 {
		return fmt.Errorf("observers has %d row(s) with NULL id; refusing to rebuild it (the new id column is NOT NULL). "+
			"To continue, delete or fix those rows (SELECT rowid, * FROM observers WHERE id IS NULL), "+
			"or run the previous CoreScope version", nullIDs)
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

	local, err := captureLocalObservers(ctx, conn)
	if err != nil {
		return err
	}

	// packets_v joins observers; modern SQLite refuses ALTER TABLE RENAME
	// while a view references a table that no longer exists.
	if _, err := conn.ExecContext(ctx, `DROP VIEW IF EXISTS packets_v`); err != nil {
		return fmt.Errorf("drop packets_v: %w", err)
	}
	if err := dropLocalObservers(ctx, conn, local); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS observers_new`); err != nil {
		return fmt.Errorf("drop stale observers_new: %w", err)
	}
	// PREFLIGHT: async=true reason="full rebuild of observers, a low-cardinality table (~1k rows in prod): one INSERT…SELECT copy plus two index builds over ~1k rows, milliseconds at any deployment size; observations (1.9M+) is never scanned — only MAX(observer_idx) via idx_observations_observer_idx"
	if _, err := conn.ExecContext(ctx, `CREATE TABLE observers_new (`+strings.Join(defs, ", ")+`)`); err != nil {
		return fmt.Errorf("create observers_new: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO observers_new (idx, `+colList+`) SELECT rowid, `+colList+` FROM observers`); err != nil {
		return fmt.Errorf("copy observers: %w", err)
	}
	var before, after int64
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM observers`).Scan(&before); err != nil {
		return err
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM observers_new`).Scan(&after); err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("observers rebuild copied %d of %d rows; rolled back", after, before)
	}
	if err := seedObserversSequence(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DROP TABLE observers`); err != nil {
		return fmt.Errorf("drop old observers: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `ALTER TABLE observers_new RENAME TO observers`); err != nil {
		return fmt.Errorf("rename observers_new: %w", err)
	}
	for _, s := range []string{
		`CREATE INDEX IF NOT EXISTS idx_observers_last_seen ON observers(last_seen)`,
		`CREATE INDEX IF NOT EXISTS idx_observers_iata_norm ON observers(UPPER(TRIM(iata)))`,
		packetsViewSQL(observersKey),
	} {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("observers rebuild: %q: %w", s, err)
		}
	}
	for _, o := range local {
		if _, err := conn.ExecContext(ctx, o.sql); err != nil {
			return recreateRefusal(o, err)
		}
	}
	if err := checkObserversForeignKeys(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit observers rebuild: %w", err)
	}
	committed = true
	logf("[dbschema] rebuilt observers with explicit idx primary key (%d rows, every rowid preserved as idx)", after)
	if len(local) > 0 {
		names := make([]string, len(local))
		for i, o := range local {
			names[i] = o.kind + " " + o.name
		}
		logf("[dbschema] observers rebuild recreated %d local object(s): %s", len(local), strings.Join(names, ", "))
	}
	return nil
}

// localObserver is a schema object the operator added that the observers
// rebuild has to take down and put back (#2443).
type localObserver struct{ kind, name, table, sql string }

// quoteIdent quotes name as an SQL identifier, so one holding a space, a
// keyword or a quote still names the same object.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// dropPlainIdxSQL is the way out refuseNonAliasIdx gives for an idx column
// that is in no key.
const dropPlainIdxSQL = `ALTER TABLE observers DROP COLUMN idx`

// dropColumnDependentsHint goes with every DROP COLUMN a refusal suggests:
// SQLite rejects the statement while an index, view or trigger uses the
// column ("error in index my_idx after drop column: no such column: idx").
const dropColumnDependentsHint = "first drop any index, view or trigger that uses the column, and recreate it after if still wanted"

// refuseNonAliasIdx is the refusal for an observers table whose idx column is
// not the rowid alias. SQLite can drop a plain column, but not one in the
// primary key ("cannot drop PRIMARY KEY column"), so the way out differs.
func refuseNonAliasIdx(ctx context.Context, q sqlRunner) error {
	var pk int
	if err := q.QueryRowContext(ctx, `SELECT pk FROM pragma_table_info('observers') WHERE LOWER(name) = 'idx'`).Scan(&pk); err != nil {
		return fmt.Errorf("inspect observers.idx: %w", err)
	}
	const refusal = "observers already has an idx column that is not the rowid alias (the rowid alias is a sole `idx INTEGER PRIMARY KEY` on a table that is not WITHOUT ROWID); " +
		"refusing to rebuild it — the upgrade would overwrite idx. "
	if pk > 0 {
		return fmt.Errorf(refusal + "This idx is in the primary key, which SQLite cannot drop or change in place: " +
			"to continue, recreate the observers table without an idx column (copy the rows to a new table, drop the old one, rename), " +
			"or run the previous CoreScope version")
	}
	return fmt.Errorf(refusal+"To continue, save its data and drop it (%s) — %s — "+
		"or run the previous CoreScope version", dropPlainIdxSQL, dropColumnDependentsHint)
}

// rollbackOrDiscard ends the rebuild's transaction on conn. When ROLLBACK
// fails the transaction may still be open, so it has database/sql close the
// connection (which rolls back) instead of pooling it, and reports that:
// whatever was set on the connection, foreign_keys included, went with it.
func rollbackOrDiscard(ctx context.Context, conn *sql.Conn) (discarded bool, err error) {
	if _, rerr := conn.ExecContext(ctx, `ROLLBACK`); rerr != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return true, fmt.Errorf("roll back observers rebuild: %w", rerr)
	}
	return false, nil
}

// recreateRefusal is the error for a local object whose definition no longer
// runs over the rebuilt table. The DROP it suggests quotes the name, like the
// one dropLocalObservers runs.
func recreateRefusal(o localObserver, cause error) error {
	return fmt.Errorf("observers rebuild could not recreate %s %s: %w; rolled back. "+
		"To continue, drop it (DROP %s %s) and recreate it after the upgrade, "+
		"or run the previous CoreScope version", o.kind, o.name, cause, strings.ToUpper(o.kind), quoteIdent(o.name))
}

// captureLocalObservers lists, in creation (sqlite_master rowid) order,
// every index, trigger and view the rebuild would otherwise lose or trip
// over, leaving out the ones it recreates itself (packets_v and the two
// observers indexes):
//   - indexes and triggers on observers: DROP TABLE takes them with it;
//   - views, and triggers on other tables, whose SQL mentions observers,
//     packets_v, or a view already captured: while one exists, ALTER TABLE
//     RENAME fails ("error in view v: no such table: main.observers"), and
//     a view on a dropped view breaks it the same way;
//   - triggers on a captured view (INSTEAD OF), by their tbl_name: DROP VIEW
//     takes them with it.
//
// The mention test is a case-insensitive whole-word match, on the name and
// on the name as it is written inside double quotes or backticks, where the
// quote character is doubled (`odd"name` is `"odd""name"` in SQL); a false
// positive only means an unrelated view or trigger is dropped and recreated
// as is.
// Indexes on other tables cannot reference observers and are never touched.
// SQL is NULL for SQLite's own auto-indexes, which follow their table.
func captureLocalObservers(ctx context.Context, tx sqlRunner) ([]localObserver, error) {
	rows, err := tx.QueryContext(ctx, `SELECT type, name, tbl_name, sql FROM sqlite_master
		WHERE type IN ('index', 'trigger', 'view') AND sql IS NOT NULL ORDER BY rowid`)
	if err != nil {
		return nil, fmt.Errorf("list observers dependents: %w", err)
	}
	var all []localObserver
	for rows.Next() {
		var o localObserver
		if err := rows.Scan(&o.kind, &o.name, &o.table, &o.sql); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	owned := func(o localObserver) bool {
		switch o.kind {
		case "view":
			return strings.EqualFold(o.name, "packets_v")
		case "index":
			return strings.EqualFold(o.name, "idx_observers_last_seen") || strings.EqualFold(o.name, "idx_observers_iata_norm")
		}
		return false
	}
	referenced := []string{"observers", "packets_v"}
	captured := make([]bool, len(all))
	// A view can mention one created after it, so repeat until nothing new
	// is captured. The schema has tens of objects; quadratic is fine.
	for changed := true; changed; {
		changed = false
		for i, o := range all {
			if captured[i] || owned(o) {
				continue
			}
			hit := strings.EqualFold(o.table, "observers")
			if !hit && o.kind != "index" {
				for _, name := range referenced {
					if (o.kind == "trigger" && strings.EqualFold(o.table, name)) ||
						mentionsIdentifier(o.sql, name) ||
						mentionsIdentifier(o.sql, strings.ReplaceAll(name, `"`, `""`)) ||
						mentionsIdentifier(o.sql, strings.ReplaceAll(name, "`", "``")) {
						hit = true
						break
					}
				}
			}
			if hit {
				captured[i], changed = true, true
				if o.kind == "view" {
					referenced = append(referenced, o.name)
				}
			}
		}
	}
	var local []localObserver
	for i, o := range all {
		if captured[i] {
			local = append(local, o)
		}
	}
	return local, nil
}

// dropLocalObservers drops the captured views and the captured triggers on
// other tables, newest first. Indexes and triggers on observers go with the
// table; IF EXISTS covers a trigger its view already took.
func dropLocalObservers(ctx context.Context, tx sqlRunner, local []localObserver) error {
	for i := len(local) - 1; i >= 0; i-- {
		o := local[i]
		if o.kind == "index" || (o.kind == "trigger" && strings.EqualFold(o.table, "observers")) {
			continue
		}
		q := `DROP ` + strings.ToUpper(o.kind) + ` IF EXISTS ` + quoteIdent(o.name)
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("observers rebuild: %s: %w", q, err)
		}
	}
	return nil
}

// mentionsIdentifier reports whether sqlText contains name as a whole word,
// ignoring case. Word characters are those of an unquoted SQLite identifier
// (ASCII letters, digits, _ and $, plus every non-ASCII byte), so quoted,
// bracketed and schema-qualified uses all count.
func mentionsIdentifier(sqlText, name string) bool {
	text, word := strings.ToLower(sqlText), strings.ToLower(name)
	isWord := func(c byte) bool {
		return c == '_' || c == '$' || c >= 0x80 || ('a' <= c && c <= 'z') || ('0' <= c && c <= '9')
	}
	for from := 0; ; {
		i := strings.Index(text[from:], word)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(word)
		if (start == 0 || !isWord(text[start-1])) && (end == len(text) || !isWord(text[end])) {
			return true
		}
		from = start + 1
	}
}

// seedObserversSequence makes AUTOINCREMENT start above every integer an
// observation already references, not just above the surviving observers.
// A legacy table could reuse the rowid of a deleted max row, re-linking that
// row's orphaned observations to a brand-new observer; seeding past
// MAX(observations.observer_idx) closes that for good. The MAX is an index
// seek (idx_observations_observer_idx, ensured earlier in Apply).
func seedObserversSequence(ctx context.Context, tx sqlRunner) error {
	// Not TableHasColumn: its Querier is not the Context surface of *sql.Conn.
	var hasObsIdx int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('observations') WHERE name = 'observer_idx'`).Scan(&hasObsIdx); err != nil {
		return fmt.Errorf("inspect observations: %w", err)
	}
	if hasObsIdx == 0 {
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
func checkObserversForeignKeys(ctx context.Context, tx sqlRunner) error {
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
			// Two causes look the same here, so name both ways out.
			return fmt.Errorf("observers rebuild left %d foreign key violation(s) in %s; rolled back. "+
				"Either a child column declared `REFERENCES observers` with no column, which follows the primary key and so now points at idx "+
				"(to continue, declare it `REFERENCES observers(id)`), "+
				"or rows that already reference a missing observer "+
				"(to continue, list them with `PRAGMA foreign_key_check(%s)` and delete or fix them); "+
				"or run the previous CoreScope version", violations, table, quoteIdent(table))
		}
	}
	return nil
}
