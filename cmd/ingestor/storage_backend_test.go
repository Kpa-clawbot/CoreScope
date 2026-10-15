package main

import (
	"database/sql"
	"github.com/meshcore-analyzer/dbconfig"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenStoreNativeSQLiteWithoutPostgres(t *testing.T) {
	t.Setenv("CORESCOPE_TEST_POSTGRES_URL", "")
	path := filepath.Join(t.TempDir(), "telemetry.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("native SQLite writer must work without PostgreSQL: %v", err)
	}
	defer s.Close()
	s.WaitForAsyncMigrations()
	for pragma, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1"} {
		var actual string
		if err := s.db.QueryRow("PRAGMA " + pragma).Scan(&actual); err != nil || actual != want {
			t.Fatalf("%s=%q want %q: %v", pragma, actual, want, err)
		}
	}
	if s.path != path {
		t.Fatalf("legacy queue anchor changed: %q", s.path)
	}
}

func TestSQLiteObserverTimeIndexCompletesThroughAsyncLedger(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "observer-index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.WaitForAsyncMigrations()
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_observations_observer_idx_timestamp'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("native observer/time index count=%d: %v", count, err)
	}
	if status, err := s.AsyncMigrationStatus("obs_observer_ts_idx_v1"); err != nil || status != "done" {
		t.Fatal("native index migration did not complete", status, err)
	}
}

func TestSQLiteMissingObserverIndexIsNotHiddenByCompletionMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovered-index.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.WaitForAsyncMigrations()
	if _, err = s.db.Exec(`DROP INDEX idx_observations_observer_idx_timestamp`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.WaitForAsyncMigrations()
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_observations_observer_idx_timestamp'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("missing index concealed by done marker", count, err)
	}
}

func testBackend(t testing.TB) dbconfig.Backend {
	t.Helper()
	v := os.Getenv("CORESCOPE_TEST_BACKEND")
	if v != "" && v != "sqlite" && v != "postgres" {
		t.Fatalf("invalid CORESCOPE_TEST_BACKEND")
	}
	if v == "postgres" {
		return dbconfig.Postgres
	}
	return dbconfig.SQLite
}
func testBackendValue() dbconfig.Backend {
	if os.Getenv("CORESCOPE_TEST_BACKEND") == "postgres" {
		return dbconfig.Postgres
	}
	return dbconfig.SQLite
}
func testNativeSQL(sqlite, postgres string) string {
	if testBackendValue() == dbconfig.Postgres {
		return postgres
	}
	return sqlite
}

func scanTestPlan(rows *sql.Rows, detail *string) error {
	if testBackendValue() == dbconfig.SQLite {
		var id, parent, unused int
		return rows.Scan(&id, &parent, &unused, detail)
	}
	return rows.Scan(detail)
}
