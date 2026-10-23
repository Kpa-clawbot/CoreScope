package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/pgutil"
)

func TestStorageStatusNeedsNoCredentialsOrDatabase(t *testing.T) {
	t.Setenv("CORESCOPE_DATABASE_URL", "unreachable-private-secret")
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", "unreachable-private-secret")
	selection := filepath.Join(t.TempDir(), "storage-selection.json")
	var out bytes.Buffer
	if err := runCommand(context.Background(), []string{"-storage-action=status", "-selection-file", selection}, &out); err != nil {
		t.Fatal(err)
	}
	var status dbconfig.SelectionStatus
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.State != "unrecorded" || status.Backend != "" {
		t.Fatalf("status=%+v", status)
	}
	if bytes.Contains(out.Bytes(), []byte("secret")) {
		t.Fatal("status exposed connection input")
	}
}

func TestRecordedPostgresPortCannotDriftThroughEnvironment(t *testing.T) {
	t.Setenv("PGPORT", "")
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGOPTIONS", "")
	base := t.TempDir()
	selected, err := dbconfig.NewSelection(dbconfig.Storage{Backend: dbconfig.Postgres, StateDir: base, WriterDatabaseURL: "postgres://owner:secret@localhost/data"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGPORT", "6543")
	raw, err := selected.ApplyTo(dbconfig.StorageInputs{BaseDir: base, DatabaseURL: "postgres://rotated:secret@localhost/data"})
	if err != nil {
		t.Fatal(err)
	}
	config, err := pgutil.ParseConfig(raw.WriterDatabaseURL)
	if err != nil || config.Port != 5432 || config.Database != "data" || config.RuntimeParams["search_path"] != `"public"` {
		t.Fatal("native parser drifted from recorded target", err)
	}
}

func TestSetupRefusesMalformedPrimaryBeforeValidFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{broken`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", "config.json"), []byte(`{"db":{"backend":"sqlite"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	selection := filepath.Join(dir, "data", "storage-selection.json")
	if err := runCommand(context.Background(), []string{"-storage-action=setup", "-selection-file", selection, "-config-dir", dir, "-offline"}, &bytes.Buffer{}); err == nil {
		t.Fatal("bad primary config silently chose a fallback")
	}
	if _, err := os.Stat(selection); !os.IsNotExist(err) {
		t.Fatal("bad primary initialized an installation", err)
	}
}

func TestStorageSetupDefaultsSQLiteAndPreservesRecordedTargets(t *testing.T) {
	for _, name := range []string{"CORESCOPE_DB_BACKEND", "DB_PATH", "CORESCOPE_DATABASE_URL", "CORESCOPE_READER_DATABASE_URL", "CORESCOPE_WRITER_DATABASE_URL", "CORESCOPE_USERS_DATABASE_URL", "CORESCOPE_USERS_OWNER_DATABASE_URL", "CORESCOPE_APPROVED_CHANNELS_DATABASE_URL"} {
		t.Setenv(name, "")
	}
	root := t.TempDir()
	selection := filepath.Join(root, "data", "storage-selection.json")
	var out bytes.Buffer
	args := []string{"-storage-action=setup", "-selection-file", selection, "-config-dir", root, "-offline"}
	if err := runCommand(context.Background(), args, &out); err != nil {
		t.Fatal(err)
	}
	got, err := dbconfig.ReadSelection(selection)
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != dbconfig.SQLite || got.Telemetry.SQLitePath != filepath.Join(root, "data", "meshcore.db") || got.Accounts == nil {
		t.Fatal("fresh default or account target wrong")
	}
	db, err := openSQLite(got.Telemetry.SQLitePath, "rw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO nodes(public_key,name) VALUES('retained','still here')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := sourceFingerprint(got.Telemetry.SQLitePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORESCOPE_DB_BACKEND", "postgres")
	t.Setenv("DB_PATH", filepath.Join(root, "obsolete.sqlite"))
	out.Reset()
	if err := runCommand(context.Background(), args, &out); err != nil {
		t.Fatal(err)
	}
	after, err := dbconfig.ReadSelection(selection)
	if err != nil || after.Generation != got.Generation || after.Backend != dbconfig.SQLite {
		t.Fatal("update changed selection", err)
	}
	if current, err := sourceFingerprint(got.Telemetry.SQLitePath); err != nil || current != before {
		t.Fatal("ready setup changed working data", err)
	}
	out.Reset()
	args[0] = "-storage-action=init"
	if err := runCommand(context.Background(), args, &out); err == nil {
		t.Fatal("init replaced existing installation")
	}
}

func TestUnrecordedConfigPathConflictRequiresOverride(t *testing.T) {
	t.Setenv("CORESCOPE_DB_BACKEND", "")
	t.Setenv("CORESCOPE_DATABASE_URL", "")
	root := t.TempDir()
	config := filepath.Join(root, "custom.json")
	if err := os.WriteFile(config, []byte(`{"dbPath":"one.sqlite","userManagement":{"enabled":false,"dbPath":"saved-users.sqlite"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_PATH", "two.sqlite")
	args := []string{"-storage-action=setup", "-selection-file", filepath.Join(root, "storage-selection.json"), "-config", config, "-offline"}
	var out bytes.Buffer
	if err := runCommand(context.Background(), args, &out); err == nil {
		t.Fatal("conflicting server/ingestor paths silently adopted")
	}
	chosen := filepath.Join(root, "chosen.sqlite")
	accounts := filepath.Join(root, "chosen-users.sqlite")
	args = append(args, "-sqlite-path", chosen, "-users-sqlite-path", accounts)
	if err := runCommand(context.Background(), args, &out); err != nil {
		t.Fatal(err)
	}
	got, err := dbconfig.ReadSelection(filepath.Join(root, "storage-selection.json"))
	if err != nil || got.Telemetry.SQLitePath != chosen || got.Accounts.SQLitePath != accounts {
		t.Fatal("explicit target override failed", err)
	}
}
