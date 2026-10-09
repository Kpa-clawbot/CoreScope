package dbschema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema/legacy"
)

func sqliteTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "telemetry.db")
	dsn, err := legacy.WriterDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db, path
}

func sqliteExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteSchemaOwnsAsyncMigrationLedger(t *testing.T) {
	db, _ := sqliteTestDB(t)
	if err := ApplySQLite(db, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO _async_migrations(name,status,error) VALUES('retained','failed','exact error')`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSQLiteAsyncMigrations(db); err != nil {
		t.Fatal(err)
	}
	var status, started, detail string
	if err := db.QueryRow(`SELECT status,started_at,error FROM _async_migrations WHERE name='retained'`).Scan(&status, &started, &detail); err != nil || status != "failed" || started == "" || detail != "exact error" {
		t.Fatal("async ledger changed", err)
	}
}

func TestSQLitePendingConversionCannotBeAdoptedOrOpened(t *testing.T) {
	db, _ := sqliteTestDB(t)
	if err := ApplySQLite(db, nil); err != nil {
		t.Fatal(err)
	}
	sqliteExec(t, db, `CREATE TABLE corescope_reverse_progress(table_name TEXT PRIMARY KEY,rows_copied INTEGER NOT NULL,sha256 TEXT NOT NULL,complete INTEGER NOT NULL)`)
	for _, check := range []func() error{func() error { return ApplySQLite(db, nil) }, func() error { return AssertSQLiteReady(db) }, func() error { return dbconfig.AssertSQLiteImportComplete(db) }} {
		if err := check(); !errors.Is(err, dbconfig.ErrSQLiteImportIncomplete) {
			t.Fatal("incomplete target accepted", err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='corescope_reverse_progress'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("pending marker changed", err)
	}
}

func TestSQLiteFreshAndReadOnlyReadiness(t *testing.T) {
	db, path := sqliteTestDB(t)
	if err := AssertSQLiteReady(db); err == nil {
		t.Fatal("empty schema ready")
	}
	if err := ApplySQLite(db, nil); err != nil {
		t.Fatal(err)
	}
	if err := ApplySQLite(db, nil); err != nil {
		t.Fatal(err)
	}
	if err := AssertSQLiteReady(db); err != nil {
		t.Fatal(err)
	}
	var marker int
	if err := db.QueryRow(`SELECT count(*) FROM _migrations WHERE name=?`, SQLiteObserverIdentityMigration).Scan(&marker); err != nil || marker != 1 {
		t.Fatalf("marker=%d err=%v", marker, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	ro.SetMaxOpenConns(1)
	if err := AssertSQLiteReady(ro); err != nil {
		t.Fatal(err)
	}
	if _, err := ro.Exec(`INSERT INTO observers(id) VALUES('reader-write')`); err == nil {
		t.Fatal("reader wrote telemetry")
	}
	if err := ApplySQLite(ro, nil); err == nil {
		t.Fatal("reader ran writer schema entrypoint")
	}
}

func TestSQLiteFreshSchemaDoesNotSuppressInitialPlannerAnalysis(t *testing.T) {
	db, _ := sqliteTestDB(t)
	for range 2 {
		if err := ApplySQLite(db, nil); err != nil {
			t.Fatal(err)
		}
	}
	// The ingestor uses sqlite_stat1's presence to decide whether its initial
	// whole-database ANALYZE is required. A table-only refresh must not mask it.
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='sqlite_stat1'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("fresh database appears fully analyzed: tables=%d err=%v", tables, err)
	}
}

func TestSQLiteObserverIdentityPreservesOldRowsAndReferences(t *testing.T) {
	db, _ := sqliteTestDB(t)
	if err := legacy.ApplyBase(db); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Apply(db, nil); err != nil {
		t.Fatal(err)
	}
	sqliteExec(t, db, `INSERT INTO observers(rowid,id,name) VALUES(0,'zero','zero'),(7,'Node','upper'),(8,'node','lower'),(20,NULL,'null-one'),(30,NULL,'null-two')`)
	sqliteExec(t, db, `INSERT INTO transmissions(id,raw_hex,hash,first_seen) VALUES(1,'aa','hash','2026-01-01'),(77,'bb','deleted','2026-01-01')`)
	sqliteExec(t, db, `DELETE FROM transmissions WHERE id=77`)
	sqliteExec(t, db, `INSERT INTO observations(id,transmission_id,observer_idx,timestamp) VALUES(1,1,7,1),(2,1,99,1),(3,1,NULL,1),(88,1,8,2)`)
	sqliteExec(t, db, `DELETE FROM observations WHERE id=88`)
	sqliteExec(t, db, `INSERT INTO advert_evidence_backfill(id,tx_cursor,obs_cursor) VALUES(1,700,900)`)
	sqliteExec(t, db, `CREATE TABLE observer_child(observer_id TEXT REFERENCES observers(id) ON DELETE CASCADE)`)
	sqliteExec(t, db, `INSERT INTO observer_child VALUES('Node')`)
	sqliteExec(t, db, `CREATE INDEX custom_observer_name ON observers(name)`)
	sqliteExec(t, db, `CREATE TABLE observer_audit(value TEXT)`)
	sqliteExec(t, db, `CREATE TRIGGER observer_name_changed AFTER UPDATE OF name ON observers BEGIN INSERT INTO observer_audit VALUES(new.name); END`)
	sqliteExec(t, db, `CREATE VIEW observer_view AS SELECT rowid,id,name FROM observers`)
	before := sqliteObserverRows(t, db)
	if err := ApplySQLite(db, nil); err != nil {
		t.Fatal(err)
	}
	if got := sqliteObserverRows(t, db); got != before {
		t.Fatalf("rows changed: before=%s after=%s", before, got)
	}
	for query, want := range map[string]int64{
		`SELECT count(*) FROM observers`:                                       5,
		`SELECT count(*) FROM observer_child`:                                  1,
		`SELECT count(*) FROM observer_view`:                                   5,
		`SELECT count(*) FROM observer_audit`:                                  0,
		`SELECT count(*) FROM sqlite_master WHERE name='custom_observer_name'`: 1,
		`SELECT count(*) FROM observations WHERE observer_idx=99`:              1,
		`SELECT count(*) FROM observations WHERE observer_idx IS NULL`:         1,
		`SELECT seq FROM sqlite_sequence WHERE name='observers'`:               99,
		`SELECT seq FROM sqlite_sequence WHERE name='transmissions'`:           77,
		`SELECT seq FROM sqlite_sequence WHERE name='observations'`:            88,
		`SELECT tx_cursor FROM advert_evidence_backfill WHERE id=1`:            700,
		`SELECT obs_cursor FROM advert_evidence_backfill WHERE id=1`:           900,
		`PRAGMA foreign_keys`:                                                  1,
		`PRAGMA legacy_alter_table`:                                            0,
	} {
		var got int64
		if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: %d want %d err=%v", query, got, want, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO observers(id) VALUES('Node')`); err == nil {
		t.Fatal("lost text-id uniqueness")
	}
	if _, err := db.Exec(`INSERT INTO observer_child VALUES('NODE')`); err == nil {
		t.Fatal("lost binary foreign-key matching")
	}
	sqliteExec(t, db, `UPDATE observers SET name='updated' WHERE id='Node'`)
	var value string
	if err := db.QueryRow(`SELECT value FROM observer_audit`).Scan(&value); err != nil || value != "updated" {
		t.Fatalf("trigger: %q %v", value, err)
	}
	sqliteExec(t, db, `DELETE FROM observers WHERE id='Node'`)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM observer_child`).Scan(&count); err != nil || count != 0 {
		t.Fatal("foreign-key cascade changed")
	}
	for _, want := range []int64{100, 101} {
		res, err := db.Exec(`INSERT INTO observers(id) VALUES('new')`)
		if err != nil {
			t.Fatal(err)
		}
		got, err := res.LastInsertId()
		if err != nil || got != want {
			t.Fatalf("next id=%d want=%d err=%v", got, want, err)
		}
		sqliteExec(t, db, `DELETE FROM observers WHERE id='new'`)
		if err := ApplySQLite(db, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func sqliteObserverRows(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT rowid,id,name FROM observers ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out strings.Builder
	for rows.Next() {
		var id int64
		var key, name sql.NullString
		if err := rows.Scan(&id, &key, &name); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&out, "%d:%#v:%#v\n", id, key, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestSQLiteUnknownObserverLayoutRefusesBeforeMutation(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement string }{
		{"collation", "id TEXT PRIMARY KEY", "id TEXT COLLATE NOCASE PRIMARY KEY"},
		{"check", "name TEXT", "name TEXT CHECK(name<>'')"},
		{"shadow rowid", "id TEXT PRIMARY KEY", "rowid TEXT,id TEXT PRIMARY KEY"},
		{"conflict policy", "id TEXT PRIMARY KEY", "id TEXT PRIMARY KEY ON CONFLICT IGNORE"},
		{"composite key", "id TEXT PRIMARY KEY", "id TEXT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := sqliteTestDB(t)
			if err := legacy.ApplyBase(db); err != nil {
				t.Fatal(err)
			}
			if err := legacy.Apply(db, nil); err != nil {
				t.Fatal(err)
			}
			var ddl string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='observers' AND type='table'`).Scan(&ddl); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(ddl, tc.old) {
				t.Fatal("fixture does not contain expected declaration")
			}
			ddl = strings.Replace(ddl, tc.old, tc.replacement, 1)
			if tc.name == "composite key" {
				ddl = strings.TrimSuffix(strings.TrimSpace(ddl), ")") + ",PRIMARY KEY(id,name))"
			}
			sqliteExec(t, db, `DROP TABLE observers`)
			sqliteExec(t, db, ddl)
			sqliteExec(t, db, `INSERT INTO observers(id,name) VALUES('kept','value')`)
			var before, after int
			if err := db.QueryRow(`PRAGMA schema_version`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := ApplySQLite(db, nil); err == nil {
				t.Fatal("unsupported schema accepted")
			}
			if err := db.QueryRow(`PRAGMA schema_version`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("refusal mutated schema")
			}
			var name string
			if err := db.QueryRow(`SELECT name FROM observers`).Scan(&name); err != nil || name != "value" {
				t.Fatal("refusal changed row")
			}
		})
	}
}

func TestSQLiteObserverMigrationFailureRollsBackAndRestoresConnection(t *testing.T) {
	db, _ := sqliteTestDB(t)
	if err := legacy.ApplyBase(db); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Apply(db, nil); err != nil {
		t.Fatal(err)
	}
	sqliteExec(t, db, `INSERT INTO observers(rowid,id,name) VALUES(12,'preserve','value')`)
	// A conflicting pre-existing object is never overwritten or repurposed.
	sqliteExec(t, db, `CREATE TABLE _corescope_observers_identity_v1(value TEXT)`)
	sqliteExec(t, db, `INSERT INTO _corescope_observers_identity_v1 VALUES('owned elsewhere')`)
	before := sqliteObserverRows(t, db)
	if err := ApplySQLite(db, nil); err == nil {
		t.Fatal("conflicting replacement table accepted")
	}
	if got := sqliteObserverRows(t, db); got != before {
		t.Fatal("failed rebuild changed observers")
	}
	var got string
	if err := db.QueryRow(`SELECT value FROM _corescope_observers_identity_v1`).Scan(&got); err != nil || got != "owned elsewhere" {
		t.Fatal("failed rebuild modified existing object")
	}
	for query, want := range map[string]int{
		`SELECT count(*) FROM _migrations WHERE name='observers_identity_autoincrement_v1'`: 0,
		`PRAGMA foreign_keys`:       1,
		`PRAGMA legacy_alter_table`: 0,
	} {
		var got int
		if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: %d want %d err=%v", query, got, want, err)
		}
	}
}

func TestSQLiteFutureIdentityVersionRefusesBeforeMutation(t *testing.T) {
	db, _ := sqliteTestDB(t)
	if err := legacy.ApplyBase(db); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Apply(db, nil); err != nil {
		t.Fatal(err)
	}
	sqliteExec(t, db, `INSERT INTO _migrations(name) VALUES('observers_identity_autoincrement_v2')`)
	var before, after int
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := ApplySQLite(db, nil); err == nil {
		t.Fatal("future version accepted")
	}
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("refusal modified schema")
	}
}

func sqliteAuthorizer(t *testing.T, db *sql.DB, callback func(int, string, string, string) int) {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Raw(func(driver any) error { driver.(*sqlite3.SQLiteConn).RegisterAuthorizer(callback); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteIdentityRollbackAfterDroppingOriginal(t *testing.T) {
	db, _ := sqliteTestDB(t)
	if err := legacy.ApplyBase(db); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Apply(db, nil); err != nil {
		t.Fatal(err)
	}
	sqliteExec(t, db, `INSERT INTO observers(rowid,id,name) VALUES(12,'preserve','value')`)
	sqliteExec(t, db, `CREATE TABLE observer_child(observer_id TEXT REFERENCES observers(id) ON DELETE CASCADE)`)
	sqliteExec(t, db, `INSERT INTO observer_child VALUES('preserve')`)
	before := sqliteObserverRows(t, db)
	dropped, denied := false, false
	sqliteAuthorizer(t, db, func(op int, a, b, c string) int {
		if op == sqlite3.SQLITE_DROP_TABLE && a == "observers" {
			dropped = true
		}
		if op == sqlite3.SQLITE_ALTER_TABLE {
			denied = true
			return sqlite3.SQLITE_DENY
		}
		return sqlite3.SQLITE_OK
	})
	err := ApplySQLite(db, nil)
	sqliteAuthorizer(t, db, nil)
	if err == nil || !dropped || !denied {
		t.Fatalf("failure did not occur after drop: err=%v dropped=%v denied=%v", err, dropped, denied)
	}
	if got := sqliteObserverRows(t, db); got != before {
		t.Fatal("rollback changed original rows")
	}
	for query, want := range map[string]int{
		`SELECT count(*) FROM observer_child`:                                               1,
		`SELECT count(*) FROM sqlite_master WHERE name='_corescope_observers_identity_v1'`:  0,
		`SELECT count(*) FROM _migrations WHERE name='observers_identity_autoincrement_v1'`: 0,
		`PRAGMA foreign_keys`:       1,
		`PRAGMA legacy_alter_table`: 0,
	} {
		var got int
		if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got=%d err=%v", query, got, err)
		}
	}
}

func TestSQLiteRepeatedApplyDoesNotRepeatCorpusForeignKeyScan(t *testing.T) {
	db, _ := sqliteTestDB(t)
	if err := ApplySQLite(db, nil); err != nil {
		t.Fatal(err)
	}
	sqliteAuthorizer(t, db, func(op int, a, b, c string) int {
		if op == sqlite3.SQLITE_PRAGMA && a == "foreign_key_check" {
			return sqlite3.SQLITE_DENY
		}
		return sqlite3.SQLITE_OK
	})
	defer sqliteAuthorizer(t, db, nil)
	if err := ApplySQLite(db, nil); err != nil {
		t.Fatal(err)
	}
	if err := AssertSQLiteReady(db); err != nil {
		t.Fatal(err)
	}
}

func TestSQLitePartialUniqueObserverKeyIsNotReady(t *testing.T) {
	db, _ := sqliteTestDB(t)
	if err := ApplySQLite(db, nil); err != nil {
		t.Fatal(err)
	}
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='observers' AND type='table'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	const fullKey = `"id" TEXT UNIQUE`
	if !strings.Contains(ddl, fullKey) {
		t.Fatal("fixture lacks full unique text key")
	}
	ddl = strings.Replace(ddl, fullKey, `"id" TEXT`, 1)
	sqliteExec(t, db, `DROP TABLE observers`)
	sqliteExec(t, db, ddl)
	sqliteExec(t, db, `CREATE UNIQUE INDEX observers_partial_identity ON observers(id) WHERE id IS NOT NULL`)
	if err := AssertSQLiteReady(db); err == nil {
		t.Fatal("partial unique text key accepted as ready")
	}
	var before, after int
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := ApplySQLite(db, nil); err == nil {
		t.Fatal("partial unique text key accepted for upgrade")
	}
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("refusal mutated partial-key schema")
	}
}
