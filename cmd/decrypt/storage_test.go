package main

import (
	"bytes"
	"database/sql"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbschema/legacy"
)

func sqliteExportFixture(t *testing.T, path string, current bool) *sql.DB {
	t.Helper()
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
	if current {
		err = dbschema.ApplySQLite(db, t.Logf)
	} else if err = legacy.ApplyBase(db); err == nil {
		err = legacy.Apply(db, t.Logf)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO transmissions(id,raw_hex,hash,first_seen,decoded_json) VALUES(1,'00','export-fixture','2026-01-01','{"path":{"hops":["ab","cd"]}}')`,
		`INSERT INTO observers(rowid,id,name) VALUES(10,'observer','Example observer')`,
		`INSERT INTO observations(transmission_id,observer_idx,snr,rssi,timestamp,path_json) VALUES(1,10,1.5,-90,1767225600,'["ab"]')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func assertExportMetadata(t *testing.T, db *sql.DB) {
	t.Helper()
	path := getPathFromDB(db, 1)
	if len(path) != 2 || path[0] != "ab" || path[1] != "cd" {
		t.Fatalf("path query lost data: %v", path)
	}
	observers := getObservers(db, 1)
	if len(observers) != 1 || observers[0].Name != "Example observer" || observers[0].SNR != 1.5 || observers[0].RSSI != -90 || observers[0].Timestamp != "2026-01-01T00:00:00Z" {
		t.Fatalf("observer query lost data: %+v", observers)
	}
}

func TestSQLiteExportReadsPathAndObservers(t *testing.T) {
	for _, current := range []bool{false, true} {
		name := "legacy"
		if current {
			name = "current"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "telemetry #percent%.db")
			sqliteExportFixture(t, path, current)
			db, err := openExportDB(path)
			if err != nil {
				t.Fatalf("native SQLite export should open its existing source: %v", err)
			}
			defer db.Close()
			assertExportMetadata(t, db)
			if _, err := db.Exec(`DELETE FROM observations`); err == nil {
				t.Fatal("export reader permitted a write")
			}
			var queryOnly int
			if err := db.QueryRow(`PRAGMA query_only`).Scan(&queryOnly); err != nil || queryOnly != 1 {
				t.Fatalf("query_only=%d err=%v", queryOnly, err)
			}
		})
	}
}

func TestSQLiteExportRejectsMissingEmptyWrongAndPendingSources(t *testing.T) {
	for _, kind := range []string{"missing", "missing-directory", "empty", "header-only", "wrong-schema", "pending"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "telemetry.db")
			switch kind {
			case "missing-directory":
				path = filepath.Join(filepath.Dir(path), "absent", "telemetry.db")
			case "empty":
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "header-only", "wrong-schema":
				dsn, err := dbconfig.SQLiteURI(path, nil)
				if err != nil {
					t.Fatal(err)
				}
				db, err := sql.Open("sqlite3", dsn)
				if err != nil {
					t.Fatal(err)
				}
				query := `PRAGMA user_version=123`
				if kind == "wrong-schema" {
					query = `CREATE TABLE keep(value TEXT); INSERT INTO keep VALUES('preserve')`
				}
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
				db.Close()
			case "pending":
				db := sqliteExportFixture(t, path, true)
				if _, err := db.Exec(`CREATE TABLE corescope_reverse_progress(value TEXT)`); err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
			before, beforeErr := os.ReadFile(path)
			db, err := openExportDB(path)
			if err == nil {
				db.Close()
				t.Fatal("invalid source was accepted")
			}
			if kind == "pending" && !errors.Is(err, dbconfig.ErrSQLiteImportIncomplete) {
				t.Fatalf("pending import guard lost: %v", err)
			}
			after, afterErr := os.ReadFile(path)
			if errors.Is(beforeErr, os.ErrNotExist) {
				if !errors.Is(afterErr, os.ErrNotExist) {
					t.Fatal("export created a replacement database")
				}
			} else if beforeErr != nil || afterErr != nil || !bytes.Equal(before, after) {
				t.Fatal("export modified its rejected source")
			}
		})
	}
}

func recordExportStorage(t *testing.T, storage dbconfig.Storage) dbconfig.Selection {
	t.Helper()
	selected, err := dbconfig.NewSelection(storage)
	if err != nil {
		t.Fatal(err)
	}
	selected, err = dbconfig.AdoptSelection(dbconfig.SelectionPath(storage.StateDir), selected)
	if err != nil {
		t.Fatal(err)
	}
	return selected
}

func TestExportHonorsRecordedSQLiteAndHoldsLease(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "installed.db")
	sqliteExportFixture(t, path, true)
	selected := recordExportStorage(t, dbconfig.Storage{Backend: dbconfig.SQLite, StateDir: base, DBPath: path})
	db, lease, err := openSelectedExport(dbconfig.StorageInputs{BaseDir: base, StateDir: base, Backend: dbconfig.Postgres, DBPath: "stale.db"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	defer lease.Close()
	assertExportMetadata(t, db)
	update, err := dbconfig.BeginSelectionUpdate(dbconfig.SelectionPath(base), selected.Generation)
	if err == nil {
		update.Abort()
		update.Close()
		t.Fatal("export did not hold the selection lease")
	}
	if !errors.Is(err, dbconfig.ErrSelectionBusy) {
		t.Fatalf("unexpected lease error: %v", err)
	}
	db.Close()
	lease.Close()
	update, err = dbconfig.BeginSelectionUpdate(dbconfig.SelectionPath(base), selected.Generation)
	if err != nil {
		t.Fatalf("export did not release the lease: %v", err)
	}
	defer update.Close()
	if err := update.Abort(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordedSQLiteExportDoesNotFallBackOrCreate(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "lost.db")
	recordExportStorage(t, dbconfig.Storage{Backend: dbconfig.SQLite, StateDir: base, DBPath: path})
	stale := filepath.Join(base, "stale.db")
	sqliteExportFixture(t, stale, true)
	db, lease, err := openSelectedExport(dbconfig.StorageInputs{BaseDir: base, StateDir: base, DBPath: stale})
	if err == nil || db != nil || lease != nil {
		t.Fatalf("lost recorded source did not fail closed: db=%t lease=%t err=%v", db != nil, lease != nil, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("export created the missing selected source")
	}
}

func TestExportSelectionErrorsCannotFallBack(t *testing.T) {
	for _, kind := range []string{"corrupt", "switching"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			path := filepath.Join(base, "legacy.db")
			sqliteExportFixture(t, path, true)
			metadata := dbconfig.SelectionPath(base)
			if kind == "switching" {
				metadata += ".switch.json"
			}
			if err := os.WriteFile(metadata, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			if db, lease, err := openSelectedExport(dbconfig.StorageInputs{BaseDir: base, DBPath: path}); err == nil || db != nil || lease != nil {
				t.Fatal("invalid selection fell back to legacy SQLite")
			}
		})
	}
}

func TestRecordedPostgresExportNeverUsesStaleSQLite(t *testing.T) {
	base := t.TempDir()
	stale := filepath.Join(base, "meshcore.db")
	w := sqliteExportFixture(t, stale, true)
	w.Close()
	before, err := os.ReadFile(stale)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := listener.Addr().String()
	listener.Close()
	dsn := "postgres://reader:never-print-this@" + endpoint + "/unavailable?sslmode=disable&connect_timeout=1"
	recordExportStorage(t, dbconfig.Storage{Backend: dbconfig.Postgres, StateDir: base, ReaderDatabaseURL: dsn})
	for _, reader := range []string{"", dsn} {
		db, lease, err := openSelectedExport(dbconfig.StorageInputs{BaseDir: base, DBPath: stale, Backend: dbconfig.SQLite, ReaderDatabaseURL: reader})
		if err == nil || db != nil || lease != nil {
			t.Fatal("missing PostgreSQL selection fell back to SQLite")
		}
		if strings.Contains(err.Error(), "never-print-this") {
			t.Fatal("connection failure exposed its password")
		}
	}
	after, err := os.ReadFile(stale)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("stale SQLite source changed")
	}
}

func TestExportConfigResolution(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(data, "meshcore.db")
	sqliteExportFixture(t, path, true)
	noEnv := func(string) string { return "" }
	raw, err := exportStorageInputs(base, exportOptions{}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	db, lease, err := openSelectedExport(raw)
	if err != nil {
		t.Fatalf("default SQLite export: %v", err)
	}
	if lease != nil {
		lease.Close()
		t.Fatal("unrecorded export must not adopt a selection")
	}
	assertExportMetadata(t, db)
	db.Close()
	if _, err := os.Stat(dbconfig.SelectionPath(data)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("export persisted a selection")
	}
	if err := os.WriteFile(filepath.Join(data, "config.json"), []byte(`{"dbPath":"data/meshcore.db"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "config.json"), []byte(`{"db":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := exportStorageInputs(base, exportOptions{}, noEnv); err == nil {
		t.Fatal("malformed primary config fell back to a valid secondary config")
	}
	if _, err := exportStorageInputs(base, exportOptions{ConfigPath: filepath.Join(base, "missing.json")}, noEnv); err == nil {
		t.Fatal("missing explicit config was ignored")
	}
	getenv := func(key string) string {
		return map[string]string{"DB_PATH": "env.db", "CORESCOPE_DATABASE_URL": "postgres://reader@localhost/example", "CORESCOPE_READER_DATABASE_URL": "postgres://reader@localhost/reader", "CORESCOPE_STATE_DIR": "env-state"}[key]
	}
	raw, err = exportStorageInputs(base, exportOptions{ConfigPath: filepath.Join(data, "config.json"), DBPath: "cli.db", DatabaseURL: "postgres://reader@localhost/cli", StateDir: "cli-state"}, getenv)
	if err != nil || raw.DBPath != "cli.db" || raw.DatabaseURL != "postgres://reader@localhost/cli" || raw.ReaderDatabaseURL != raw.DatabaseURL || raw.StateDir != "cli-state" {
		t.Fatal("explicit flag precedence was lost")
	}
	if _, err := dbconfig.ResolveStorage(raw); err == nil {
		t.Fatal("ambiguous unrecorded file and URL inputs were accepted")
	}
}
