package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil/pgtest"
	"github.com/meshcore-analyzer/users"
)

// Include tuple versions and sequence state so a refusal or a verified ready
// store cannot conceal rewrites of identical values, progress, or readiness.
func importDatabaseState(t *testing.T, db *sql.DB, kind string) string {
	t.Helper()
	tables, err := layouts(kind)
	if err != nil {
		t.Fatal(err)
	}
	var state strings.Builder
	for _, table := range append(tables, importTable{Name: "corescope_import"}, importTable{Name: "corescope_import_progress"}, importTable{Name: "corescope_schema"}) {
		rows, err := db.Query(`SELECT row_to_json(t)::text,t.xmin::text,t.ctid::text FROM ` + quote(table.Name) + ` t ORDER BY row_to_json(t)::text COLLATE "C"`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var values, version, position string
			if err := rows.Scan(&values, &version, &position); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(&state, table.Name, values, version, position)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		for _, column := range table.Columns {
			if !column.Identity {
				continue
			}
			var sequence string
			if err := db.QueryRow(`SELECT pg_get_serial_sequence($1,$2)`, table.Name, column.Name).Scan(&sequence); err != nil {
				t.Fatal(err)
			}
			var value int64
			var called bool
			if err := db.QueryRow(`SELECT last_value,is_called FROM `+sequence).Scan(&value, &called); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(&state, sequence, value, called)
		}
	}
	return state.String()
}

func mixedImport(t *testing.T, startAccounts, readyTelemetry bool) [2]importOptions {
	t.Helper()
	state := filepath.Join(t.TempDir(), "migration state")
	options := [2]importOptions{
		{Source: telemetrySource(t), DatabaseURL: pgtest.NewDatabase(t), StateDir: state, Kind: "telemetry"},
		{Source: accountSource(t, 5), DatabaseURL: pgtest.NewDatabase(t), StateDir: state, Kind: "accounts"},
	}
	for i, o := range options {
		if i == 1 && !startAccounts {
			continue
		}
		if _, err := importSQLite(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	if readyTelemetry {
		if _, err := finalizeImport(context.Background(), options[0].DatabaseURL, "telemetry"); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CORESCOPE_DATABASE_URL", options[0].DatabaseURL)
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", options[1].DatabaseURL)
	return options
}

func resumeMixedCommand(options [2]importOptions, out io.Writer) error {
	return runCommand(context.Background(), []string{"-offline", "-resume", "-from-sqlite", options[0].Source, "-users-from-sqlite", options[1].Source, "-state-dir", options[0].StateDir}, out)
}

func TestCLIResumeAfterFirstStoreFinalized(t *testing.T) {
	options := mixedImport(t, true, true)
	telemetry := openImportDB(t, options[0].DatabaseURL)
	accounts := openImportDB(t, options[1].DatabaseURL)
	before := importDatabaseState(t, telemetry, "telemetry")
	beforeSources := make(map[string]string)
	for _, o := range options {
		for _, path := range []string{o.Source, filepath.Join(o.StateDir, o.Kind, "recovery.sqlite"), filepath.Join(o.StateDir, o.Kind, "normalized.sqlite")} {
			digest, err := fingerprint(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			beforeSources[path] = digest
		}
	}
	if err := users.AssertReady(accounts); err == nil {
		t.Fatal("fixture must reproduce the crash between the two readiness commits")
	}
	var output bytes.Buffer
	if err := resumeMixedCommand(options, &output); err != nil {
		t.Fatalf("resume after telemetry finalized but accounts unready: %v", err)
	}
	if err := dbschema.AssertReady(telemetry); err != nil {
		t.Fatal(err)
	}
	if err := users.AssertReady(accounts); err != nil {
		t.Fatal(err)
	}
	if got := importDatabaseState(t, telemetry, "telemetry"); got != before {
		t.Fatal("resume wrote already-ready telemetry data, progress, sequence, or readiness")
	}
	for _, o := range options {
		if !strings.Contains(output.String(), o.Kind+" import verified and finalized") || strings.Contains(output.String(), o.DatabaseURL) {
			t.Fatal("missing success evidence or leaked database URL")
		}
	}
	for path, before := range beforeSources {
		after, err := fingerprint(context.Background(), path)
		if err != nil || after != before {
			t.Fatal("resume changed original source or immutable snapshot", err)
		}
	}
}

type importOutputFunc func([]byte) (int, error)

func (f importOutputFunc) Write(data []byte) (int, error) { return f(data) }

func TestCLIResumeFailureDoesNotResetAlreadyReadyStore(t *testing.T) {
	options := mixedImport(t, true, true)
	telemetry := openImportDB(t, options[0].DatabaseURL)
	accounts := openImportDB(t, options[1].DatabaseURL)
	before := importDatabaseState(t, telemetry, "telemetry")
	// Fail the second finalization after both imports have been verified, so
	// the actual CLI rollback path must distinguish previously ready stores.
	out := importOutputFunc(func(data []byte) (int, error) {
		if bytes.Contains(data, []byte(`"kind":"accounts"`)) {
			if _, err := accounts.Exec(`UPDATE users SET display_name='post-verification change'`); err != nil {
				return 0, err
			}
		}
		return len(data), nil
	})
	if err := resumeMixedCommand(options, out); err == nil || !strings.Contains(err.Error(), "final verification failed") {
		t.Fatalf("second finalization corruption should be refused: %v", err)
	}
	if got := importDatabaseState(t, telemetry, "telemetry"); got != before {
		t.Fatal("failed second finalization reset or rewrote the previously ready store")
	}
	if err := users.AssertReady(accounts); err == nil {
		t.Fatal("changed accounts became ready")
	}
}

func TestCLIRollbackDoesNotCloseGateOpenedByAnotherFinalizer(t *testing.T) {
	options := mixedImport(t, true, false)
	telemetry := openImportDB(t, options[0].DatabaseURL)
	accounts := openImportDB(t, options[1].DatabaseURL)
	var finalizedState string
	out := importOutputFunc(func(data []byte) (int, error) {
		if bytes.Contains(data, []byte(`"kind":"accounts"`)) {
			// The importer observed telemetry unready. A separate allowed
			// finalizer opens it before this CLI reaches its finalization loop.
			if _, err := finalizeImport(context.Background(), options[0].DatabaseURL, "telemetry"); err != nil {
				return 0, err
			}
			finalizedState = importDatabaseState(t, telemetry, "telemetry")
			if _, err := accounts.Exec(`UPDATE users SET display_name='fail second finalization'`); err != nil {
				return 0, err
			}
		}
		return len(data), nil
	})
	if err := resumeMixedCommand(options, out); err == nil || !strings.Contains(err.Error(), "final verification failed") {
		t.Fatalf("second finalization should fail after the separate finalizer ran: %v", err)
	}
	if finalizedState == "" {
		t.Fatal("fixture did not execute the intervening finalization")
	}
	if err := dbschema.AssertReady(telemetry); err != nil {
		t.Error("CLI reset a gate it did not open", err)
	}
	if got := importDatabaseState(t, telemetry, "telemetry"); got != finalizedState {
		t.Error("CLI changed tuples or readiness owned by the separate finalizer")
	}
	if err := users.AssertReady(accounts); err == nil {
		t.Fatal("corrupted accounts became ready")
	}
}

func TestCLIRollbackClosesGateOpenedByThisInvocation(t *testing.T) {
	options := mixedImport(t, true, false)
	telemetry := openImportDB(t, options[0].DatabaseURL)
	accounts := openImportDB(t, options[1].DatabaseURL)
	out := importOutputFunc(func(data []byte) (int, error) {
		if bytes.Contains(data, []byte(`"kind":"accounts"`)) {
			if _, err := accounts.Exec(`UPDATE users SET display_name='fail second finalization'`); err != nil {
				return 0, err
			}
		}
		return len(data), nil
	})
	if err := resumeMixedCommand(options, out); err == nil || !strings.Contains(err.Error(), "final verification failed") {
		t.Fatalf("second finalization should fail: %v", err)
	}
	if err := dbschema.AssertReady(telemetry); err == nil {
		t.Fatal("CLI did not close the telemetry gate it opened before account failure")
	}
	if err := users.AssertReady(accounts); err == nil {
		t.Fatal("corrupted accounts became ready")
	}
}

func TestCLIResumeDoesNotStartOccupiedSecondStore(t *testing.T) {
	options := mixedImport(t, false, true)
	telemetry := openImportDB(t, options[0].DatabaseURL)
	accounts := openImportDB(t, options[1].DatabaseURL)
	if _, err := accounts.Exec(`CREATE SEQUENCE keep_me START WITH 77`); err != nil {
		t.Fatal(err)
	}
	before := importDatabaseState(t, telemetry, "telemetry")
	if err := resumeMixedCommand(options, io.Discard); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("resume accepted an occupied unmarked destination: %v", err)
	}
	if got := importDatabaseState(t, telemetry, "telemetry"); got != before {
		t.Fatal("refusal changed already-ready telemetry")
	}
	var value int
	var called bool
	if err := accounts.QueryRow(`SELECT last_value,is_called FROM keep_me`).Scan(&value, &called); err != nil || value != 77 || called {
		t.Fatal("refusal changed occupied account destination", err)
	}
	if _, err := os.Stat(filepath.Join(options[1].StateDir, "accounts")); !os.IsNotExist(err) {
		t.Fatal("occupied destination should be refused before source preparation")
	}
}

func TestCLIResumeStartsUntouchedSecondStore(t *testing.T) {
	options := mixedImport(t, false, false)
	if err := resumeMixedCommand(options, io.Discard); err != nil {
		t.Fatalf("resume must finish the verified first store and start untouched accounts: %v", err)
	}
	if err := dbschema.AssertReady(openImportDB(t, options[0].DatabaseURL)); err != nil {
		t.Fatal(err)
	}
	if err := users.AssertReady(openImportDB(t, options[1].DatabaseURL)); err != nil {
		t.Fatal(err)
	}
}

func TestResumePreparationArtifactsArePreserved(t *testing.T) {
	o := importOptions{Source: telemetrySource(t), DatabaseURL: pgtest.NewSchema(t), StateDir: t.TempDir(), Kind: "telemetry", Resume: true}
	recovery := filepath.Join(o.StateDir, o.Kind, "recovery.sqlite")
	if err := os.MkdirAll(filepath.Dir(recovery), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := snapshotSource(context.Background(), o.Source, recovery); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(recovery)
	if err != nil {
		t.Fatal(err)
	}
	_, err = importSQLite(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "preserve") || !strings.Contains(err.Error(), "state directory") {
		t.Fatalf("interrupted preparation needs preserving recovery guidance: %v", err)
	}
	after, err := os.ReadFile(recovery)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refusal changed the only recovery snapshot", err)
	}
	var n int
	if err := openImportDB(t, o.DatabaseURL).QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()`).Scan(&n); err != nil || n != 0 {
		t.Fatal("preparation refusal changed the empty destination", err)
	}
	// Exercise the documented recovery: archive the incomplete per-store state
	// without deleting it, then let resume prepare the still-empty store anew.
	archived := filepath.Join(t.TempDir(), "preserved incomplete preparation")
	if err := os.Rename(filepath.Dir(recovery), archived); err != nil {
		t.Fatal(err)
	}
	if report, err := importSQLite(context.Background(), o); err != nil || !report.Verified {
		t.Fatal("resume after preserving incomplete preparation failed", err)
	}
	retained, err := os.ReadFile(filepath.Join(archived, "recovery.sqlite"))
	if err != nil || !bytes.Equal(before, retained) {
		t.Fatal("retry changed archived recovery evidence", err)
	}
}

func TestReadyResumeRefusesChangedEvidenceWithoutWrites(t *testing.T) {
	changes := map[string]string{
		"new PostgreSQL row":      `INSERT INTO observers(id) VALUES('post-cutover')`,
		"changed PostgreSQL row":  `UPDATE observers SET name='changed' WHERE rowid=0`,
		"changed sequence only":   `SELECT nextval(pg_get_serial_sequence('observers','rowid'))`,
		"incomplete progress":     `UPDATE corescope_import_progress SET complete=false WHERE table_name='observers'`,
		"changed progress digest": `UPDATE corescope_import_progress SET sha256=repeat('0',64) WHERE table_name='observers'`,
		"unverified marker":       `UPDATE corescope_import SET verified=false`,
		"changed report":          `UPDATE corescope_import SET report=replace(report,'"rows":2','"rows":200')`,
		"changed run identity":    `UPDATE corescope_import SET run_id='different'`,
		"source":                  "",
		"recovery snapshot":       "",
		"normalized snapshot":     "",
	}
	for name, statement := range changes {
		t.Run(name, func(t *testing.T) {
			o := importOptions{Source: telemetrySource(t), DatabaseURL: pgtest.NewSchema(t), StateDir: t.TempDir(), Kind: "telemetry"}
			if _, err := importSQLite(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			if _, err := finalizeImport(context.Background(), o.DatabaseURL, o.Kind); err != nil {
				t.Fatal(err)
			}
			db := openImportDB(t, o.DatabaseURL)
			if statement != "" {
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			} else {
				path := o.Source
				if name != "source" {
					file := "normalized.sqlite"
					if name == "recovery snapshot" {
						file = "recovery.sqlite"
					}
					path = filepath.Join(o.StateDir, o.Kind, file)
				}
				changed, err := sql.Open("sqlite3", path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = changed.Exec(`UPDATE observers SET name='changed' WHERE rowid=0`)
				changed.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			before := importDatabaseState(t, db, o.Kind)
			o.Resume = true
			if _, err := importSQLite(context.Background(), o); err == nil {
				t.Fatal("changed evidence accepted for an already-ready destination")
			}
			if after := importDatabaseState(t, db, o.Kind); after != before {
				t.Fatal("refusal changed data, progress, sequence, or readiness")
			}
		})
	}
}
