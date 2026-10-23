package main

import (
	"context"
	"database/sql"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/pgutil/pgtest"
)

// copyTestRows streams a bounded synthetic fixture through native PostgreSQL
// COPY. Each COPY remains an atomic durable statement; no test durability
// settings are changed and there is no INSERT round trip per fixture row.
func copyTestRows(t testing.TB, db *sql.DB, table string, columns []string, n int, row func(int) []any) {
	t.Helper()
	if testBackend(t) == dbconfig.SQLite {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		slots := make([]string, len(columns))
		for i := range slots {
			slots[i] = dbconfig.SQLite.Parameter(i + 1)
		}
		stmt, err := tx.Prepare("INSERT INTO " + table + "(" + strings.Join(columns, ",") + ") VALUES(" + strings.Join(slots, ",") + ")")
		if err != nil {
			t.Fatal(err)
		}
		defer stmt.Close()
		for i := 0; i < n; i++ {
			if _, err = stmt.Exec(row(i)...); err != nil {
				t.Fatal(err)
			}
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	err = conn.Raw(func(driver any) error {
		count, err := driver.(*stdlib.Conn).Conn().CopyFrom(ctx, pgx.Identifier{table}, columns,
			pgx.CopyFromSlice(n, func(i int) ([]any, error) { return row(i), nil }))
		if err == nil && count != int64(n) {
			t.Errorf("copied %d/%d rows into %s", count, n, table)
		}
		return err
	})
	if err != nil {
		t.Fatalf("seed %s: %v", table, err)
	}
}

func postgresTestDSN(t testing.TB) string {
	t.Helper()
	if testBackend(t) == dbconfig.SQLite {
		return filepath.Join(t.TempDir(), "fixture.db")
	}
	return pgtest.NewSchema(t)
}

func openFixtureSQL(dsn string) (*sql.DB, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return pgutil.Open(dsn, false)
	}
	uri, err := dbconfig.SQLiteURI(dsn, url.Values{"_journal_mode": {"WAL"}, "_synchronous": {"FULL"}, "_busy_timeout": {"5000"}})
	if err != nil {
		return nil, err
	}
	return sql.Open("sqlite3", uri)
}

func openPostgresTestDB(t testing.TB) (*sql.DB, error) {
	t.Helper()
	db, err := openFixtureSQL(postgresTestDSN(t))
	if err == nil {
		t.Cleanup(func() { db.Close() })
	}
	return db, err
}

func openFixtureReader(t testing.TB, dsn string) (*DB, error) {
	t.Helper()
	if testBackend(t) == dbconfig.SQLite {
		return OpenDB(dsn)
	}
	return OpenDB(pgtest.ReadOnly(t, dsn))
}

func restorePostgresTestBackup(t testing.TB, path string) *sql.DB {
	if testBackend(t) == dbconfig.SQLite {
		db, err := openFixtureSQL(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	t.Helper()
	dsn := pgtest.NewDatabase(t)
	config, err := pgutil.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	env, err := pgutil.CommandEnv(dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pg_restore", "--exit-on-error", "--no-owner", "--no-privileges", "--dbname="+config.Database, path)
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		t.Fatal("restore native PostgreSQL backup failed")
	}
	db, err := openFixtureSQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func testDatabaseDSN(t testing.TB) string {
	t.Helper()
	if testBackend(t) == dbconfig.SQLite {
		return filepath.Join(t.TempDir(), "fixture.db")
	}
	return pgtest.NewDatabase(t)
}
func postgresOnly(t testing.TB) {
	t.Helper()
	if testBackend(t) != dbconfig.Postgres {
		t.Skip("PostgreSQL matrix not selected")
	}
}
func applyTestSchema(t testing.TB, db *sql.DB) error {
	t.Helper()
	if testBackend(t) == dbconfig.SQLite {
		return dbschema.ApplySQLite(db, nil)
	}
	return dbschema.ApplyPostgres(db, nil)
}
