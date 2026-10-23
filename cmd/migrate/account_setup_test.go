package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/users"
)

func TestLegacyAccountAbsenceFirstSetupAndRecordedLoss(t *testing.T) {
	for _, key := range []string{"CORESCOPE_DB_BACKEND", "DB_PATH", "CORESCOPE_DATABASE_URL", "CORESCOPE_USERS_OWNER_DATABASE_URL", "CORESCOPE_USERS_DATABASE_URL", "CORESCOPE_APPROVED_CHANNELS_DATABASE_URL"} {
		t.Setenv(key, "")
	}
	source := telemetrySource(t)
	accounts := filepath.Join(t.TempDir(), "first-accounts.sqlite")
	dir := t.TempDir()
	o := storageOptions{Action: "adopt", Backend: "sqlite", Offline: true, SelectionFile: filepath.Join(dir, "storage-selection.json"), SQLitePath: source, UsersSQLitePath: accounts, ConfigDir: t.TempDir()}
	if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	selected, err := dbconfig.ReadSelection(o.SelectionFile)
	if err != nil || selected.Accounts != nil {
		t.Fatal("legacy absence was initialized", err)
	}
	if _, err := os.Stat(accounts); !os.IsNotExist(err) {
		t.Fatal("legacy adoption created account file", err)
	}
	_, lease, err := dbconfig.OpenSelection(o.SelectionFile)
	if err != nil {
		t.Fatal(err)
	}
	o.Action = "setup"
	if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); !errors.Is(err, dbconfig.ErrSelectionBusy) {
		t.Fatal("first initialization lacked exclusive lease", err)
	}
	lease.Close()
	if _, err := os.Stat(accounts); !os.IsNotExist(err) {
		t.Fatal("busy setup created account file", err)
	}
	if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err != nil {
		t.Fatal("first explicit setup", err)
	}
	initialized, err := dbconfig.ReadSelection(o.SelectionFile)
	if err != nil || initialized.Accounts == nil || initialized.Accounts.SQLitePath != accounts || initialized.Generation == selected.Generation {
		t.Fatal("initialized target not recorded", err)
	}
	db, err := openSQLite(accounts, "ro")
	if err != nil {
		t.Fatal(err)
	}
	if err := users.AssertSQLiteReady(db); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.Remove(accounts); err != nil {
		t.Fatal(err)
	}
	if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err == nil {
		t.Fatal("recorded missing accounts recreated")
	}
	if _, err := os.Stat(accounts); !os.IsNotExist(err) {
		t.Fatal("lost account file replaced", err)
	}
	after, err := dbconfig.ReadSelection(o.SelectionFile)
	if err != nil || after.Generation != initialized.Generation {
		t.Fatal("failed setup changed recorded target", err)
	}
}

func TestLegacyDisabledExistingAccountStoreIsRecorded(t *testing.T) {
	source, accounts := telemetrySource(t), accountSource(t, 5)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte(`{"userManagement":{"enabled":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	o := storageOptions{Action: "adopt", Backend: "sqlite", Offline: true, SelectionFile: filepath.Join(dir, "storage-selection.json"), SQLitePath: source, UsersSQLitePath: accounts, ConfigFile: config}
	if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	selected, err := dbconfig.ReadSelection(o.SelectionFile)
	if err != nil || selected.Accounts == nil || selected.Accounts.SQLitePath != accounts {
		t.Fatal("disabled initialized store was omitted", err)
	}
	db, err := openSQLite(accounts, "ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id int
	if err := db.QueryRow(`SELECT id FROM mail_events WHERE mail_id=4`).Scan(&id); err != nil || id != 900 {
		t.Fatal("existing account data changed", id, err)
	}
}

func TestSQLiteAccountSetupResumeUsesOnlyItsJournal(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "lost-target"}[missing], func(t *testing.T) {
			source := telemetrySource(t)
			accounts := filepath.Join(t.TempDir(), "accounts.sqlite")
			dir := t.TempDir()
			o := storageOptions{Action: "adopt", Backend: "sqlite", Offline: true, SelectionFile: filepath.Join(dir, "storage-selection.json"), SQLitePath: source, UsersSQLitePath: accounts, ConfigDir: t.TempDir()}
			if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			o.Action = "setup"
			o.beforeCommit = func() error { return errors.New("stop before account selection") }
			if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err == nil {
				t.Fatal("injected cutover failure passed")
			}
			status, err := dbconfig.InspectSelection(o.SelectionFile)
			if err != nil || status.State != "pending" || status.SourceBackend != dbconfig.SQLite || status.TargetBackend != dbconfig.SQLite {
				t.Fatal("same-backend journal status", status, err)
			}
			if missing {
				if err := os.Remove(accounts); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("CORESCOPE_DATABASE_URL", "must-not-connect")
			t.Setenv("CORESCOPE_USERS_OWNER_DATABASE_URL", "must-not-connect")
			t.Setenv("CORESCOPE_USERS_DATABASE_URL", "must-not-connect")
			o.Action = "resume"
			o.JobID = status.JobID
			o.beforeCommit = nil
			err = runStorageAction(context.Background(), o, &bytes.Buffer{})
			if missing {
				if err == nil || !strings.Contains(err.Error(), "not recreated") {
					t.Fatal("lost staged account target was recreated", err)
				}
				if _, err := os.Stat(accounts); !os.IsNotExist(err) {
					t.Fatal("missing staged target replaced")
				}
				o.Action = "abort"
				if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal("SQLite-only account resume", err)
			}
		})
	}
}

func TestUnstagedJournalNeedsNoDatabaseForAbort(t *testing.T) {
	source := telemetrySource(t)
	if err := applySQLiteTarget(context.Background(), source, "telemetry"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	selected, err := dbconfig.NewSelection(dbconfig.Storage{Backend: dbconfig.SQLite, StateDir: dir, DBPath: source})
	if err != nil {
		t.Fatal(err)
	}
	path := dbconfig.SelectionPath(dir)
	if _, err := dbconfig.AdoptSelection(path, selected); err != nil {
		t.Fatal(err)
	}
	update, err := dbconfig.BeginSelectionUpdate(path, selected.Generation)
	if err != nil {
		t.Fatal(err)
	}
	id := update.ID()
	update.Close()
	t.Setenv("CORESCOPE_DATABASE_URL", "must-not-connect")
	o := storageOptions{Action: "resume", Offline: true, SelectionFile: path, JobID: id, ConfigDir: t.TempDir()}
	if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "no staged destination") {
		t.Fatal("unstaged job resume guessed a target", err)
	}
	o.Action = "abort"
	if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err != nil {
		t.Fatal("metadata-only abort", err)
	}
}

func TestPostgresAccountFirstSetupRequiresCredentialsAndResumes(t *testing.T) {
	owner, accountOwner := postgresDatabase(t), postgresDatabase(t)
	db := openImportDB(t, owner)
	if err := dbschema.ApplyPostgres(db, nil); err != nil {
		t.Fatal(err)
	}
	reader, writer := restrictedRole(t, owner), restrictedRole(t, owner)
	dir := t.TempDir()
	selected, err := dbconfig.NewSelection(dbconfig.Storage{Backend: dbconfig.Postgres, StateDir: dir, WriterDatabaseURL: owner})
	if err != nil {
		t.Fatal(err)
	}
	raw := dbconfig.StorageInputs{BaseDir: dir, ReaderDatabaseURL: reader, WriterDatabaseURL: writer}
	if err := grantRuntimeTargets(context.Background(), selected, dbconfig.Storage{WriterDatabaseURL: owner}, raw); err != nil {
		t.Fatal(err)
	}
	selection := dbconfig.SelectionPath(dir)
	if _, err := dbconfig.AdoptSelection(selection, selected); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORESCOPE_DATABASE_URL", owner)
	t.Setenv("CORESCOPE_READER_DATABASE_URL", reader)
	t.Setenv("CORESCOPE_WRITER_DATABASE_URL", writer)
	t.Setenv("CORESCOPE_USERS_OWNER_DATABASE_URL", "")
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", "")
	t.Setenv("CORESCOPE_APPROVED_CHANNELS_DATABASE_URL", "")
	o := storageOptions{Action: "setup", Offline: true, SelectionFile: selection, ConfigDir: t.TempDir()}
	if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err != nil {
		t.Fatal("ordinary PG setup invented an account target", err)
	}
	unchanged, err := dbconfig.ReadSelection(selection)
	if err != nil || unchanged.Accounts != nil || unchanged.Generation != selected.Generation {
		t.Fatal("ordinary PG setup changed absence", err)
	}
	t.Setenv("CORESCOPE_USERS_OWNER_DATABASE_URL", accountOwner)
	if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err == nil {
		t.Fatal("account initialization lacked runtime credentials")
	}
	unchanged, err = dbconfig.ReadSelection(selection)
	if err != nil || unchanged.Accounts != nil {
		t.Fatal("failed preflight changed selection", err)
	}
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", restrictedRole(t, accountOwner))
	o.beforeCommit = func() error { return errors.New("interrupt account commit") }
	if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err == nil {
		t.Fatal("injected account commit passed")
	}
	status, err := dbconfig.InspectSelection(selection)
	if err != nil || status.SourceBackend != dbconfig.Postgres || status.TargetBackend != dbconfig.Postgres {
		t.Fatal("PG account journal", status, err)
	}
	o.Action = "resume"
	o.JobID = status.JobID
	o.beforeCommit = nil
	if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err != nil {
		t.Fatal("PG account initialization resume", err)
	}
	after, err := dbconfig.ReadSelection(selection)
	if err != nil || after.Accounts == nil || after.Generation == selected.Generation {
		t.Fatal("account target not atomically recorded", err)
	}
}
