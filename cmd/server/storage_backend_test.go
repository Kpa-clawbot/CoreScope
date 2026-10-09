package main

import (
	"database/sql"
	"errors"
	"github.com/meshcore-analyzer/dbconfig"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbschema/legacy"
)

func TestOpenDBNativeSQLiteWithoutPostgres(t *testing.T) {
	t.Setenv("CORESCOPE_TEST_POSTGRES_URL", "")
	path := filepath.Join(t.TempDir(), "telemetry.db")
	dsn, err := legacy.WriterDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := dbschema.ApplySQLite(owner, nil); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenDB(path)
	if err != nil {
		t.Fatalf("native SQLite reader must work without PostgreSQL: %v", err)
	}
	defer reader.Close()
	if _, err := reader.conn.Exec("CREATE TABLE forbidden_write(id INTEGER)"); err == nil {
		t.Fatal("reader acquired write access")
	}
	var count int
	if err := reader.conn.QueryRow("SELECT count(*) FROM transmissions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("reader count=%d err=%v", count, err)
	}
	if _, err := owner.Exec(`CREATE TABLE ` + dbconfig.SQLiteImportPendingTable + `(phase TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := reader.AssertReady(); !errors.Is(err, dbconfig.ErrSQLiteImportIncomplete) {
		t.Fatal("reader accepted incomplete converted target", err)
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
