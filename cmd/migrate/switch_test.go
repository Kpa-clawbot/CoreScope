package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil"
)

func restrictedRole(t *testing.T, owner string) string {
	t.Helper()
	db := openImportDB(t, owner)
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	role := "corescope_convert_" + hex.EncodeToString(random[:])
	password := "test_" + hex.EncodeToString(random[:])
	if _, err := db.Exec(`CREATE ROLE ` + quote(role) + ` LOGIN PASSWORD '` + password + `'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DROP OWNED BY ` + quote(role) + `;DROP ROLE ` + quote(role)); err != nil {
			t.Error(err)
		}
	})
	u, err := url.Parse(owner)
	if err != nil {
		t.Fatal("invalid test owner URL")
	}
	u.User = url.UserPassword(role, password)
	return u.String()
}

func switchFixture(t *testing.T) (storageOptions, dbconfig.Selection) {
	t.Helper()
	telemetry, accounts := postgresDatabase(t), postgresDatabase(t)
	t.Setenv("CORESCOPE_DATABASE_URL", telemetry)
	t.Setenv("CORESCOPE_USERS_OWNER_DATABASE_URL", accounts)
	t.Setenv("CORESCOPE_READER_DATABASE_URL", restrictedRole(t, telemetry))
	t.Setenv("CORESCOPE_WRITER_DATABASE_URL", restrictedRole(t, telemetry))
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", restrictedRole(t, accounts))
	t.Setenv("CORESCOPE_APPROVED_CHANNELS_DATABASE_URL", restrictedRole(t, accounts))
	t.Setenv("CORESCOPE_DB_BACKEND", "")
	t.Setenv("DB_PATH", "")
	source, account := telemetrySource(t), accountSource(t, 5)
	if err := applySQLiteTarget(context.Background(), source, "telemetry"); err != nil {
		t.Fatal(err)
	}
	if err := applySQLiteTarget(context.Background(), account, "accounts"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	selected, err := dbconfig.NewSelection(dbconfig.Storage{Backend: dbconfig.SQLite, StateDir: dir, DBPath: source, UsersDBPath: account})
	if err != nil {
		t.Fatal(err)
	}
	selection := dbconfig.SelectionPath(dir)
	if _, err := dbconfig.AdoptSelection(selection, selected); err != nil {
		t.Fatal(err)
	}
	return storageOptions{Action: "switch", SelectionFile: selection, Backend: "postgres", OwnerURL: telemetry, UsersOwnerURL: accounts, StateDir: t.TempDir(), Offline: true, ConfigDir: t.TempDir()}, selected
}

func TestVerifiedTwoStoreSwitchRoundTripAndRuntimeGrants(t *testing.T) {
	o, source := switchFixture(t)
	ctx := context.Background()
	before, err := sourceFingerprint(source.Telemetry.SQLitePath)
	if err != nil {
		t.Fatal(err)
	}
	beforeAccount, err := sourceFingerprint(source.Accounts.SQLitePath)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runStorageAction(ctx, o, &out); err != nil {
		t.Fatal("forward switch", err)
	}
	selected, err := dbconfig.ReadSelection(o.SelectionFile)
	if err != nil || selected.Backend != dbconfig.Postgres {
		t.Fatal("PG selection", err)
	}
	if got, err := sourceFingerprint(source.Telemetry.SQLitePath); err != nil || got != before {
		t.Fatal("telemetry source changed", err)
	}
	if got, err := sourceFingerprint(source.Accounts.SQLitePath); err != nil || got != beforeAccount {
		t.Fatal("account source changed", err)
	}
	for _, pair := range [][2]string{{"CORESCOPE_WRITER_DATABASE_URL", "corescope_schema"}, {"CORESCOPE_USERS_DATABASE_URL", "schema_version"}} {
		db, err := pgutil.Open(os.Getenv(pair[0]), false)
		if err != nil {
			t.Fatal(err)
		}
		var allowed bool
		if err := db.QueryRow(`SELECT has_table_privilege($1,'INSERT,UPDATE,DELETE,TRUNCATE')`, pair[1]).Scan(&allowed); err != nil || allowed {
			t.Fatal("metadata DML granted", allowed, err)
		}
		db.Close()
	}
	channel, err := pgutil.Open(os.Getenv("CORESCOPE_APPROVED_CHANNELS_DATABASE_URL"), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := channel.Exec(`SELECT password_hash FROM users`); err == nil {
		t.Fatal("projection reader can access account secrets")
	}
	channel.Close()
	o.Backend = "sqlite"
	o.SQLitePath = filepath.Join(t.TempDir(), "telemetry.sqlite")
	o.UsersSQLitePath = filepath.Join(t.TempDir(), "accounts.sqlite")
	out.Reset()
	if err := runStorageAction(ctx, o, &out); err != nil {
		t.Fatal("reverse switch", err)
	}
	selected, err = dbconfig.ReadSelection(o.SelectionFile)
	if err != nil || selected.Backend != dbconfig.SQLite || selected.StateDir != source.StateDir || selected.Telemetry.SQLitePath != o.SQLitePath {
		t.Fatal("reverse selection", err)
	}
	db, err := openSQLite(o.UsersSQLitePath, "ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id int
	if err := db.QueryRow(`SELECT id FROM mail_events WHERE mail_id=4`).Scan(&id); err != nil || id != 900 {
		t.Fatal("round trip event", id, err)
	}
	if strings.Contains(out.String(), "postgres://") {
		t.Fatal("CLI leaked credentials")
	}
}

func TestSwitchFailureKeepsOldSelectionAndResumeVerifiesAgain(t *testing.T) {
	o, source := switchFixture(t)
	ctx := context.Background()
	o.afterStore = func(kind string) error {
		if kind == "telemetry" {
			return errors.New("injected failure before accounts")
		}
		return nil
	}
	if err := runStorageAction(ctx, o, &bytes.Buffer{}); err == nil {
		t.Fatal("partial switch succeeded")
	}
	status, err := dbconfig.InspectSelection(o.SelectionFile)
	if err != nil || status.State != "pending" || status.Backend != dbconfig.SQLite || status.Generation != source.Generation {
		t.Fatal("partial switch selected target", status, err)
	}
	if _, _, err := dbconfig.OpenSelection(o.SelectionFile); !errors.Is(err, dbconfig.ErrSelectionInProgress) {
		t.Fatal("partial switch allowed runtime", err)
	}
	o.Action = "resume"
	o.JobID = status.JobID
	o.afterStore = nil
	o.beforeCommit = func() error { return errors.New("injected cutover failure") }
	if err := runStorageAction(ctx, o, &bytes.Buffer{}); err == nil {
		t.Fatal("cutover failure passed")
	}
	status, err = dbconfig.InspectSelection(o.SelectionFile)
	if err != nil || status.Generation != source.Generation || status.State != "pending" {
		t.Fatal("failed cutover changed selection", err)
	}
	o.beforeCommit = nil
	if err := runStorageAction(ctx, o, &bytes.Buffer{}); err != nil {
		t.Fatal("verified resume", err)
	}
	final, err := dbconfig.ReadSelection(o.SelectionFile)
	if err != nil || final.Backend != dbconfig.Postgres {
		t.Fatal("resume failed to select", err)
	}
}

func TestSQLiteSourceFencePreservesWALAndRejectsWriters(t *testing.T) {
	source := telemetrySource(t)
	ctx := context.Background()
	if err := applySQLiteTarget(ctx, source, "telemetry"); err != nil {
		t.Fatal(err)
	}
	writer, err := openSQLite(source, "rw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; INSERT INTO observers(id) VALUES('wal-retained')`); err != nil {
		t.Fatal(err)
	}
	keeper, err := openSQLite(source, "ro")
	if err != nil {
		t.Fatal(err)
	}
	defer keeper.Close()
	if err := keeper.Ping(); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	before, err := sourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	f, err := fenceSQLite(ctx, source, "telemetry")
	if err != nil {
		t.Fatal(err)
	}
	other, err := openSQLite(source, "rw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(`PRAGMA busy_timeout=1; INSERT INTO observers(id) VALUES('must-not-write')`); err == nil {
		t.Fatal("source accepted a concurrent writer")
	}
	other.Close()
	f.Close()
	keeper.Close()
	after, err := sourceFingerprint(source)
	if err != nil || after != before {
		t.Fatal("source fence checkpointed or modified original WAL", err)
	}
}

func TestSwitchSourceMutationAndDestinationSubstitutionRefuse(t *testing.T) {
	o, source := switchFixture(t)
	ctx := context.Background()
	o.afterStore = func(string) error { return errors.New("stop after first store") }
	if err := runStorageAction(ctx, o, &bytes.Buffer{}); err == nil {
		t.Fatal("expected interruption")
	}
	status, err := dbconfig.InspectSelection(o.SelectionFile)
	if err != nil {
		t.Fatal(err)
	}
	var job switchJob
	jobPath := filepath.Join(o.StateDir, status.JobID, "job.json")
	if err := readPrivateJSON(jobPath, &job); err != nil {
		t.Fatal(err)
	}
	original := job
	job.Next.Telemetry.Postgres = &dbconfig.PostgresTarget{Host: "substituted.invalid", Port: 5432, Database: "other", Schema: "public"}
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jobPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	o.Action = "resume"
	o.JobID = status.JobID
	o.afterStore = nil
	if err := runStorageAction(ctx, o, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatal("changed target accepted", err)
	}
	data, err = json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jobPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := openSQLite(source.Telemetry.SQLitePath, "rw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := changed.Exec(`UPDATE observations SET score=7.5 WHERE id=90`); err != nil {
		t.Fatal(err)
	}
	changed.Close()
	if err := runStorageAction(ctx, o, &bytes.Buffer{}); err == nil {
		t.Fatal("changed original source accepted")
	}
	o.Action = "abort"
	if err := runStorageAction(ctx, o, &bytes.Buffer{}); err != nil {
		t.Fatal("abort", err)
	}
	selected, err := dbconfig.ReadSelection(o.SelectionFile)
	if err != nil || selected.Generation != source.Generation {
		t.Fatal("abort changed original selection", err)
	}
	if _, err := os.Stat(filepath.Join(o.StateDir, status.JobID, "telemetry", "recovery.sqlite")); err != nil {
		t.Fatal("abort removed recovery", err)
	}
}

func TestSetupPostgresCannotAbandonExistingSQLite(t *testing.T) {
	owner := postgresDatabase(t)
	t.Setenv("CORESCOPE_DATABASE_URL", owner)
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", "")
	t.Setenv("CORESCOPE_USERS_OWNER_DATABASE_URL", "")
	t.Setenv("CORESCOPE_APPROVED_CHANNELS_DATABASE_URL", "")
	t.Setenv("CORESCOPE_READER_DATABASE_URL", "")
	t.Setenv("CORESCOPE_WRITER_DATABASE_URL", "")
	t.Setenv("CORESCOPE_DB_BACKEND", "sqlite")
	source := telemetrySource(t)
	t.Setenv("DB_PATH", source)
	before, err := sourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte(`{"db":{"backend":"sqlite"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-storage-action=setup", "-backend=postgres", "-selection-file", filepath.Join(dir, "storage-selection.json"), "-config", config, "-offline"}
	err = runCommand(context.Background(), args, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "primary SQLite data") {
		t.Fatal("fresh PG abandoned source or explicit choice did not override hints", err)
	}
	db := openImportDB(t, owner)
	var n int
	if err := db.QueryRow(importDestinationObjects).Scan(&n); err != nil || n != 0 {
		t.Fatal("empty PG target was initialized", n, err)
	}
	if after, err := sourceFingerprint(source); err != nil || before != after {
		t.Fatal("SQLite source was changed", err)
	}
}

func TestFreshPostgresSetupOverridesBootstrapDefaultAndGrantsBeforeSelection(t *testing.T) {
	owner, accounts := postgresDatabase(t), postgresDatabase(t)
	t.Setenv("CORESCOPE_DATABASE_URL", owner)
	t.Setenv("CORESCOPE_USERS_OWNER_DATABASE_URL", accounts)
	t.Setenv("CORESCOPE_READER_DATABASE_URL", restrictedRole(t, owner))
	t.Setenv("CORESCOPE_WRITER_DATABASE_URL", restrictedRole(t, owner))
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", restrictedRole(t, accounts))
	t.Setenv("CORESCOPE_APPROVED_CHANNELS_DATABASE_URL", restrictedRole(t, accounts))
	t.Setenv("CORESCOPE_DB_BACKEND", "sqlite")
	t.Setenv("DB_PATH", "")
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte(`{"db":{"backend":"sqlite"},"userManagement":{"enabled":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	selection := filepath.Join(dir, "storage-selection.json")
	args := []string{"-storage-action=setup", "-backend=postgres", "-selection-file", selection, "-config", config, "-offline"}
	var out bytes.Buffer
	if err := runCommand(context.Background(), args, &out); err != nil {
		t.Fatal(err)
	}
	selected, err := dbconfig.ReadSelection(selection)
	if err != nil || selected.Backend != dbconfig.Postgres || selected.Accounts == nil {
		t.Fatal("explicit PG setup", err)
	}
	if bytes.Contains(out.Bytes(), []byte("postgres://")) {
		t.Fatal("owner credentials in setup output")
	}
	// Updates retain PG although the unchanged bootstrap config/env still say SQLite.
	args = []string{"-storage-action=setup", "-selection-file", selection, "-config", config, "-offline"}
	if err := runCommand(context.Background(), args, &bytes.Buffer{}); err != nil {
		t.Fatal("PG update", err)
	}
	after, err := dbconfig.ReadSelection(selection)
	if err != nil || after.Generation != selected.Generation {
		t.Fatal("update replaced installation", err)
	}
}

func TestInitialAdoptionRefusesImplicitRoleSchemaDrift(t *testing.T) {
	owner := postgresDatabase(t)
	config, err := pgutil.ParseConfig(owner)
	if err != nil {
		t.Fatal(err)
	}
	db := openImportDB(t, owner)
	if _, err := db.Exec(`CREATE SCHEMA ` + quote(config.User)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORESCOPE_DATABASE_URL", owner)
	t.Setenv("CORESCOPE_USERS_OWNER_DATABASE_URL", "")
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", "")
	t.Setenv("CORESCOPE_APPROVED_CHANNELS_DATABASE_URL", "")
	t.Setenv("CORESCOPE_READER_DATABASE_URL", "")
	t.Setenv("CORESCOPE_WRITER_DATABASE_URL", "")
	t.Setenv("DB_PATH", "")
	dir := t.TempDir()
	selection := filepath.Join(dir, "storage-selection.json")
	err = runStorageAction(context.Background(), storageOptions{Action: "setup", Backend: "postgres", OwnerURL: owner, Offline: true, SelectionFile: selection, ConfigDir: dir}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "database/schema differs") {
		t.Fatal("implicit owner schema drift was accepted", err)
	}
	if _, err := os.Stat(selection); !os.IsNotExist(err) {
		t.Fatal("ambiguous schema was selected", err)
	}
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema')`).Scan(&tables); err != nil || tables != 0 {
		t.Fatal("ambiguous schema bootstrap wrote tables", tables, err)
	}
}

func TestReverseWithoutAccountSourceResumesAfterCutoverFailure(t *testing.T) {
	owner := postgresDatabase(t)
	db := openImportDB(t, owner)
	if err := dbschema.ApplyPostgres(db, nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORESCOPE_DATABASE_URL", owner)
	t.Setenv("CORESCOPE_USERS_OWNER_DATABASE_URL", "")
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", "")
	t.Setenv("CORESCOPE_APPROVED_CHANNELS_DATABASE_URL", "")
	dir := t.TempDir()
	selected, err := dbconfig.NewSelection(dbconfig.Storage{Backend: dbconfig.Postgres, StateDir: dir, WriterDatabaseURL: owner})
	if err != nil {
		t.Fatal(err)
	}
	selection := dbconfig.SelectionPath(dir)
	if _, err := dbconfig.AdoptSelection(selection, selected); err != nil {
		t.Fatal(err)
	}
	o := storageOptions{Action: "switch", Backend: "sqlite", Offline: true, SelectionFile: selection, OwnerURL: owner, StateDir: t.TempDir(), ConfigDir: t.TempDir(), SQLitePath: filepath.Join(t.TempDir(), "telemetry.sqlite"), UsersSQLitePath: filepath.Join(t.TempDir(), "accounts.sqlite"), beforeCommit: func() error { return errors.New("cutover interrupted") }}
	if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err == nil {
		t.Fatal("injected failure passed")
	}
	status, err := dbconfig.InspectSelection(selection)
	if err != nil || status.State != "pending" {
		t.Fatal("pending job absent", err)
	}
	o.Action = "resume"
	o.JobID = status.JobID
	o.beforeCommit = nil
	if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err != nil {
		t.Fatal("empty optional account resume", err)
	}
	after, err := dbconfig.ReadSelection(selection)
	if err != nil || after.Backend != dbconfig.SQLite || after.Accounts != nil {
		t.Fatal("resume did not preserve uninitialized accounts", err)
	}
	if _, err := os.Stat(o.UsersSQLitePath); !os.IsNotExist(err) {
		t.Fatal("reverse switch invented an account database", err)
	}
	newOwner := postgresDatabase(t)
	t.Setenv("CORESCOPE_DATABASE_URL", newOwner)
	t.Setenv("CORESCOPE_READER_DATABASE_URL", restrictedRole(t, newOwner))
	t.Setenv("CORESCOPE_WRITER_DATABASE_URL", restrictedRole(t, newOwner))
	o.Action = "switch"
	o.Backend = "postgres"
	o.OwnerURL = newOwner
	o.JobID = ""
	if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err != nil {
		t.Fatal("switch nil accounts forward", err)
	}
	after, err = dbconfig.ReadSelection(selection)
	if err != nil || after.Backend != dbconfig.Postgres || after.Accounts != nil {
		t.Fatal("forward switch changed account absence", err)
	}
}
