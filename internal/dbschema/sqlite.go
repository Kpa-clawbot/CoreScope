package dbschema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema/legacy"
)

// The existing logical migration table records this SQLite physical-layout
// change. No new data table or PostgreSQL manifest column is introduced.
const SQLiteObserverIdentityMigration = "observers_identity_autoincrement_v1"

// Existing entrypoints remain PostgreSQL-compatible while callers migrate to
// explicit engine selection. PostgreSQL grants/readiness checks are unchanged.
func ApplyPostgres(db *sql.DB, logf Logger) error { return Apply(db, logf) }
func AssertPostgresReady(db *sql.DB) error        { return AssertReady(db) }

// ApplySQLite is a writer-only startup/upgrade entrypoint. Existing v3 files
// are checked before historical migrations can perform cleanup. Older v2 or
// unknown layouts require an explicit supported offline upgrade first.
func ApplySQLite(db *sql.DB, logf Logger) error {
	if err := dbconfig.AssertSQLiteImportComplete(db); err != nil {
		return err
	}
	migrated, cols, err := sqliteObserverLayout(db)
	if err != nil {
		return err
	}
	if err := sqliteIdentityVersion(db); err != nil {
		return err
	}
	var ledger, applied int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='_migrations'`).Scan(&ledger); err != nil {
		return err
	}
	if ledger != 0 {
		if err := db.QueryRow(`SELECT count(*) FROM _migrations WHERE name=?`, SQLiteObserverIdentityMigration).Scan(&applied); err != nil {
			return err
		}
	}
	if applied != 0 && !migrated {
		return errors.New("SQLite observer identity marker does not match its physical schema")
	}
	needsIdentity := applied == 0
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
		return err
	}
	if tables != 0 && needsIdentity {
		if len(cols) == 0 {
			return errors.New("existing SQLite file is not a supported telemetry database")
		}
		if err := legacy.CheckLegacySource(db); err != nil {
			return err
		}
		if err := sqliteForeignKeysValid(db); err != nil {
			return err
		}
	}
	if err := legacy.ApplyBase(db); err != nil {
		return err
	}
	if err := legacy.Apply(db, legacy.Logger(logf)); err != nil {
		return err
	}
	if err := EnsureSQLiteAsyncMigrations(db); err != nil {
		return err
	}
	// The identity migration's corpus/FK scans run once. Ordinary restarts
	// retain existing schema maintenance without rescanning all observations.
	if !needsIdentity {
		return AssertSQLiteReady(db)
	}
	return migrateSQLiteObserverIdentity(db)
}

// EnsureSQLiteAsyncMigrations retains the upstream ingestor's exact ledger
// layout. Setup and reverse conversion need the complete schema before the
// ingestor starts; that process also calls this same idempotent entrypoint.
func EnsureSQLiteAsyncMigrations(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS _async_migrations (
  name TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  started_at TEXT NOT NULL DEFAULT (datetime('now')),
  ended_at TEXT,
  error TEXT
 )`)
	return err
}

// AssertSQLiteReady only reads schema metadata. It is safe on a mode=ro handle;
// it never applies migrations or marks a partially initialized target ready.
func AssertSQLiteReady(db *sql.DB) error {
	if err := dbconfig.AssertSQLiteImportComplete(db); err != nil {
		return err
	}
	if err := legacy.AssertReady(db); err != nil {
		return err
	}
	if err := sqliteIdentityVersion(db); err != nil {
		return err
	}
	migrated, _, err := sqliteObserverLayout(db)
	if err != nil {
		return err
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM _migrations WHERE name=?`, SQLiteObserverIdentityMigration).Scan(&count); err != nil {
		return err
	}
	if !migrated || count != 1 {
		return errors.New("SQLite observer identity schema is not migrated; run the writer upgrade")
	}
	return nil
}

type sqliteColumn struct {
	name, kind                  string
	notNull, primaryKey, hidden int
	defaultSQL                  sql.NullString
}

var unsupportedObserverConstraint = regexp.MustCompile(`(?i)\b(CHECK|COLLATE|REFERENCES|CONSTRAINT|GENERATED|WITHOUT|STRICT)\b|\bON\s+CONFLICT\b`)

func sqliteQuote(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

// Rebuilding unknown constraints can silently change semantics. Accept the
// ordinary supported layout; refuse extensions we cannot reproduce before any
// schema writes. Explicit indexes and triggers are preserved by the rebuild.
func sqliteObserverLayout(db Querier) (bool, []sqliteColumn, error) {
	var ddl string
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='observers'`).Scan(&ddl)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if unsupportedObserverConstraint.MatchString(ddl) {
		return false, nil, errors.New("unsupported SQLite observer constraints; source was not changed")
	}
	rows, err := db.Query(`PRAGMA table_xinfo(observers)`)
	if err != nil {
		return false, nil, err
	}
	var cols []sqliteColumn
	for rows.Next() {
		var cid int
		var c sqliteColumn
		if err = rows.Scan(&cid, &c.name, &c.kind, &c.notNull, &c.defaultSQL, &c.primaryKey, &c.hidden); err != nil {
			rows.Close()
			return false, nil, err
		}
		known := false
		for _, table := range Tables {
			if table.Name == "observers" {
				for _, col := range table.Columns {
					if col.Name == c.name {
						known = true
					}
				}
			}
		}
		c.kind = strings.ToUpper(c.kind)
		if !known || c.hidden != 0 || (c.kind != "TEXT" && c.kind != "INTEGER" && c.kind != "REAL") {
			rows.Close()
			return false, nil, errors.New("unsupported SQLite observer column layout; source was not changed")
		}
		cols = append(cols, c)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return false, nil, err
	}
	rows.Close()
	var id, rowid *sqliteColumn
	for i := range cols {
		if cols[i].name == "id" {
			id = &cols[i]
		}
		if cols[i].name == "rowid" {
			rowid = &cols[i]
		}
	}
	if id == nil || id.kind != "TEXT" {
		return false, nil, errors.New("unsupported SQLite observer text identity")
	}
	if rowid == nil {
		if id.primaryKey != 1 || strings.Contains(strings.ToUpper(ddl), "UNIQUE") {
			return false, nil, errors.New("unsupported SQLite observer primary key")
		}
		for _, c := range cols {
			if c.name != "id" && c.primaryKey != 0 {
				return false, nil, errors.New("unsupported SQLite composite observer primary key")
			}
		}
		return false, cols, nil
	}
	if rowid.kind != "INTEGER" || rowid.primaryKey != 1 || id.primaryKey != 0 || !strings.Contains(strings.ToUpper(ddl), "AUTOINCREMENT") {
		return false, nil, errors.New("unsupported SQLite observer rowid identity")
	}
	// A text identity remains a BINARY unique key (including SQLite's existing
	// multiple-NULL behavior), so foreign keys naming observers(id) still work.
	idx, err := db.Query(`SELECT name FROM pragma_index_list('observers') WHERE "unique"=1 AND partial=0`)
	if err != nil {
		return false, nil, err
	}
	var names []string
	for idx.Next() {
		var name string
		if err = idx.Scan(&name); err != nil {
			idx.Close()
			return false, nil, err
		}
		names = append(names, name)
	}
	if err = idx.Err(); err != nil {
		idx.Close()
		return false, nil, err
	}
	idx.Close()
	for _, name := range names {
		var total, matching int
		err = db.QueryRow(`SELECT count(*),coalesce(sum(name='id' AND coll='BINARY'),0) FROM pragma_index_xinfo(?) WHERE key=1`, name).Scan(&total, &matching)
		if err != nil {
			return false, nil, err
		}
		if total == 1 && matching == 1 {
			return true, cols, nil
		}
	}
	return false, nil, errors.New("SQLite observer text identity is not a BINARY unique key")
}

func sqliteIdentityVersion(db Querier) error {
	var exists int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='_migrations'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	const prefix = "observers_identity_autoincrement_v"
	rows, err := db.Query(`SELECT name FROM _migrations WHERE substr(name,1,?)=?`, len(prefix), prefix)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name != SQLiteObserverIdentityMigration {
			return errors.New("unsupported newer SQLite observer identity migration")
		}
	}
	return rows.Err()
}

func migrateSQLiteObserverIdentity(db *sql.DB) (err error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var foreignKeys, legacyAlter int
	if err = conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return err
	}
	if err = conn.QueryRowContext(ctx, `PRAGMA legacy_alter_table`).Scan(&legacyAlter); err != nil {
		return err
	}
	defer func() {
		for _, q := range []string{fmt.Sprintf("PRAGMA legacy_alter_table=%d", legacyAlter), fmt.Sprintf("PRAGMA foreign_keys=%d", foreignKeys)} {
			if _, restoreErr := conn.ExecContext(ctx, q); err == nil && restoreErr != nil {
				err = restoreErr
			}
		}
	}()
	// Use the same reserved connection throughout. Disabling FK actions avoids
	// cascades while replacing the parent; references are checked before commit.
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	migrated, cols, err := sqliteObserverLayout(tx)
	if err != nil {
		return err
	}
	if err = sqliteForeignKeysValid(tx); err != nil {
		return err
	}
	if !migrated {
		var objects []string
		rows, e := tx.Query(`SELECT sql FROM sqlite_master WHERE tbl_name='observers' AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY type,name`)
		if e != nil {
			return e
		}
		for rows.Next() {
			var ddl string
			if e = rows.Scan(&ddl); e != nil {
				rows.Close()
				return e
			}
			objects = append(objects, ddl)
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			return e
		}
		rows.Close()
		fields := []string{`rowid INTEGER PRIMARY KEY AUTOINCREMENT`}
		names := []string{`rowid`}
		for _, c := range cols {
			field := sqliteQuote(c.name) + " " + c.kind
			if c.name == "id" {
				field += " UNIQUE"
			}
			if c.notNull != 0 {
				field += " NOT NULL"
			}
			if c.defaultSQL.Valid {
				field += " DEFAULT " + c.defaultSQL.String
			}
			fields = append(fields, field)
			names = append(names, sqliteQuote(c.name))
		}
		const replacement = "_corescope_observers_identity_v1"
		if _, err = tx.Exec(`CREATE TABLE ` + replacement + ` (` + strings.Join(fields, ",") + `)`); err != nil {
			return err
		}
		columns := strings.Join(names, ",")
		if _, err = tx.Exec(`INSERT INTO ` + replacement + ` (` + columns + `) SELECT ` + columns + ` FROM observers`); err != nil {
			return err
		}
		if _, err = tx.Exec(`DROP TABLE observers`); err != nil {
			return err
		}
		if _, err = tx.Exec(`ALTER TABLE ` + replacement + ` RENAME TO observers`); err != nil {
			return err
		}
		for _, ddl := range objects {
			if _, err = tx.Exec(ddl); err != nil {
				return err
			}
		}
	}
	// Include retained orphan links and the sequence itself, so deleted IDs can
	// never be reassigned to a new observer. Other table sequences and cursors
	// are untouched by this migration; reverse import must seed them separately.
	var high int64
	if err = tx.QueryRow(`SELECT max(value) FROM (
		SELECT coalesce(max(rowid),0) AS value FROM observers UNION ALL
		SELECT coalesce(max(observer_idx),0) FROM observations UNION ALL
		SELECT coalesce(max(seq),0) FROM sqlite_sequence WHERE name='observers')`).Scan(&high); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE sqlite_sequence SET seq=? WHERE name='observers'`, high)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		if _, err = tx.Exec(`INSERT INTO sqlite_sequence(name,seq) VALUES('observers',?)`, high); err != nil {
			return err
		}
	}
	if err = sqliteForeignKeysValid(tx); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO _migrations(name) VALUES(?)`, SQLiteObserverIdentityMigration); err != nil {
		return err
	}
	return tx.Commit()
}

func sqliteForeignKeysValid(db Querier) error {
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("SQLite foreign-key violations prevent observer identity migration")
	}
	return rows.Err()
}
