package main

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil/pgtest"
)

func TestCheckReadyDoesNotBootstrap(t *testing.T) {
	dsn := pgtest.NewSchema(t)
	t.Setenv("CORESCOPE_DATABASE_URL", dsn)
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", "")
	var output bytes.Buffer
	if err := runCommand(context.Background(), []string{"-check-ready"}, &output); err == nil {
		t.Fatal("empty schema reported ready")
	}
	var n int
	if err := openImportDB(t, dsn).QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("readiness check created tables")
	}
	if err := runCommand(context.Background(), nil, &output); err != nil {
		t.Fatal(err)
	}
	if err := runCommand(context.Background(), []string{"-check-ready"}, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), dsn) {
		t.Fatal("CLI output leaked a database URL")
	}
}

func TestCLIImportRequiresOfflineAndDoesNotFinalizeOnAccountFailure(t *testing.T) {
	source := telemetrySource(t)
	telemetry, accounts := pgtest.NewDatabase(t), pgtest.NewDatabase(t)
	t.Setenv("CORESCOPE_DATABASE_URL", telemetry)
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", accounts)
	var output bytes.Buffer
	args := []string{"-from-sqlite", source, "-state-dir", t.TempDir()}
	if err := runCommand(context.Background(), args, &output); err == nil {
		t.Fatal("import accepted without explicit offline confirmation")
	}
	args = append(args, "-offline", "-users-from-sqlite", "missing-account-source.db")
	if err := runCommand(context.Background(), args, &output); err == nil {
		t.Fatal("missing account source was ignored")
	}
	if err := dbschema.AssertReady(openImportDB(t, telemetry)); err == nil {
		t.Fatal("telemetry became ready despite failed account import")
	}
}

func TestCheckImportMarkerBindsExactSource(t *testing.T) {
	dsn, source := pgtest.NewSchema(t), telemetrySource(t)
	t.Setenv("CORESCOPE_DATABASE_URL", dsn)
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", "")
	var output bytes.Buffer
	args := []string{"-check-import-kind=telemetry", "-from-sqlite", source}
	if err := runCommand(context.Background(), args, &output); err == nil {
		t.Fatal("empty database passed imported-source check")
	}
	if _, err := importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "telemetry"}); err != nil {
		t.Fatal(err)
	}
	if err := runCommand(context.Background(), args, &output); err == nil {
		t.Fatal("unfinalized import passed bootstrap guard")
	}
	if _, err := finalizeImport(context.Background(), dsn, "telemetry"); err != nil {
		t.Fatal(err)
	}
	if err := runCommand(context.Background(), args, &output); err != nil {
		t.Fatal(err)
	}
	args[len(args)-1] = accountSource(t, 1)
	if err := runCommand(context.Background(), args, &output); err == nil {
		t.Fatal("different legacy source passed bootstrap guard")
	}
}

func TestWALModeCleanShutdownImportAndGuard(t *testing.T) {
	source := telemetrySource(t)
	db, err := sql.Open("sqlite3", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	dsn := pgtest.NewSchema(t)
	t.Setenv("CORESCOPE_DATABASE_URL", dsn)
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", "")
	var output bytes.Buffer
	if err := runCommand(context.Background(), []string{"-offline", "-from-sqlite", source, "-state-dir", t.TempDir()}, &output); err != nil {
		t.Fatal(err)
	}
	if err := runCommand(context.Background(), []string{"-check-import-kind=telemetry", "-from-sqlite", source}, &output); err != nil {
		t.Fatal(err)
	}
}
