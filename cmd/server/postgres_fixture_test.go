package main

import (
	"context"
	"database/sql"
	"os/exec"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/pgutil/pgtest"
)

// copyTestRows streams a bounded synthetic fixture through native PostgreSQL
// COPY. Each COPY remains an atomic durable statement; no test durability
// settings are changed and there is no INSERT round trip per fixture row.
func copyTestRows(t testing.TB, db *sql.DB, table string, columns []string, n int, row func(int) []any) {
	t.Helper()
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
	return pgtest.NewSchema(t)
}

func openFixtureSQL(dsn string) (*sql.DB, error) { return pgutil.Open(dsn, false) }

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
	return OpenDB(pgtest.ReadOnly(t, dsn))
}

func restorePostgresTestBackup(t testing.TB, path string) *sql.DB {
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
