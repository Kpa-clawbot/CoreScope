package main

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/pgutil"
)

func TestStorageInputsKeepBootstrapRawAndLegacyPrecedence(t *testing.T) {
	base := t.TempDir()
	cfg := &Config{DBPath: "configured.db", UserManagement: &UserManagementConfig{DBPath: "accounts.db"}}
	env := func(k string) string {
		if k == "DB_PATH" {
			return "environment.db"
		}
		return ""
	}
	in, err := cfg.storageInputs(base, storageFlags{}, env)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs("configured.db")
	if in.DBPath != want || in.StateDir != "" || in.Backend != "" || in.FreshInstall {
		t.Fatalf("raw values/defaults changed: %+v", in)
	}
	in, err = cfg.storageInputs(base, storageFlags{DBPath: "cli.db"}, env)
	if err != nil {
		t.Fatal(err)
	}
	want, _ = filepath.Abs("cli.db")
	if in.DBPath != want {
		t.Fatal("CLI path did not win")
	}
	in, err = (&Config{}).storageInputs(base, storageFlags{}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dbconfig.ResolveStorage(in); err == nil {
		t.Fatal("unknown existing install was guessed fresh")
	}
	in.FreshInstall = true
	got, err := dbconfig.ResolveStorage(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != dbconfig.SQLite || got.DBPath != filepath.Join(base, "data", "meshcore.db") {
		t.Fatalf("fresh default: %+v", got)
	}
}

func TestRecordedPostgresPortCannotDriftThroughEnvironment(t *testing.T) {
	t.Setenv("PGPORT", "")
	const target = "postgresql://reader@localhost/telemetry?sslmode=disable"
	dir := t.TempDir()
	selected, err := dbconfig.NewSelection(dbconfig.Storage{Backend: dbconfig.Postgres, StateDir: dir, ReaderDatabaseURL: target, WriterDatabaseURL: target})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGPORT", "6543")
	raw, err := selected.ApplyTo(dbconfig.StorageInputs{BaseDir: dir, DatabaseURL: target})
	if err != nil {
		return
	} // Rejecting a changed effective endpoint is safe too.
	storage, err := dbconfig.ResolveStorage(raw)
	if err != nil {
		t.Fatal(err)
	}
	config, err := pgutil.ParseConfig(storage.ReaderDatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if config.Port != selected.Telemetry.Postgres.Port {
		t.Fatalf("recorded port %d became effective port %d", selected.Telemetry.Postgres.Port, config.Port)
	}
}

func TestStorageInputsReaderURLAndStatePrecedence(t *testing.T) {
	base := t.TempDir()
	cfg := &Config{DB: &DBConfig{Backend: dbconfig.Postgres}, DatabaseURL: "postgresql://reader@localhost/config", StateDir: "configured"}
	env := func(k string) string {
		switch k {
		case "CORESCOPE_DATABASE_URL":
			return "postgresql://reader@localhost/generic"
		case "CORESCOPE_READER_DATABASE_URL":
			return "postgresql://reader@localhost/reader"
		case "CORESCOPE_STATE_DIR":
			return "state"
		}
		return ""
	}
	in, err := cfg.storageInputs(base, storageFlags{}, env)
	if err != nil {
		t.Fatal(err)
	}
	got, err := dbconfig.ResolveStorage(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReaderDatabaseURL != env("CORESCOPE_READER_DATABASE_URL") || got.StateDir != filepath.Join(base, "state") {
		t.Fatal("reader environment precedence changed")
	}
	in, err = cfg.storageInputs(base, storageFlags{DatabaseURL: "postgresql://reader@localhost/cli", StateDir: "cli-state"}, env)
	if err != nil {
		t.Fatal(err)
	}
	got, err = dbconfig.ResolveStorage(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReaderDatabaseURL != "postgresql://reader@localhost/cli" || got.WriterDatabaseURL != "postgresql://reader@localhost/cli" || got.StateDir != filepath.Join(base, "cli-state") {
		t.Fatal("CLI overrides lost")
	}
}

func TestRuntimeUsesInstalledSelectionAndHoldsLease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "selected.db")
	selected, err := dbconfig.NewSelection(dbconfig.Storage{Backend: dbconfig.SQLite, StateDir: dir, DBPath: path, UsersDBPath: filepath.Join(dir, "users.db")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dbconfig.AdoptSelection(dbconfig.SelectionPath(dir), selected); err != nil {
		t.Fatal(err)
	}
	raw := dbconfig.StorageInputs{BaseDir: dir, StateDir: dir, Backend: dbconfig.Postgres, EnvBackend: dbconfig.Postgres, DBPath: filepath.Join(dir, "old.db"), DatabaseURL: "postgresql://unused@localhost/old"}
	storage, lease, err := resolveRuntimeStorage(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if storage.Backend != dbconfig.SQLite || storage.DBPath != path {
		t.Fatal("recorded SQLite choice not honored")
	}
	if _, err := dbconfig.BeginSelectionUpdate(dbconfig.SelectionPath(dir), selected.Generation); !errors.Is(err, dbconfig.ErrSelectionBusy) {
		t.Fatal("reader did not retain selection lease", err)
	}
}
func TestReaderMissingSelectionDoesNotAdoptOrHoldLease(t *testing.T) {
	dir := t.TempDir()
	raw := dbconfig.StorageInputs{BaseDir: dir, StateDir: dir, DBPath: filepath.Join(dir, "legacy.db")}
	if _, lease, err := resolveRuntimeStorage(raw); !errors.Is(err, dbconfig.ErrSelectionMissing) || lease != nil {
		t.Fatal("reader guessed unrecorded storage", err)
	}
	update, err := dbconfig.BeginSelectionUpdate(dbconfig.SelectionPath(dir), "")
	if err != nil {
		t.Fatal("reader blocked setup adoption", err)
	}
	defer update.Close()
	if err = update.Abort(); err != nil {
		t.Fatal(err)
	}
}
