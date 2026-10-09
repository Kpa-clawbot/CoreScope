package main

import (
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
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
	want, _ := filepath.Abs("environment.db")
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

func TestStorageInputsWriterURLAndStatePrecedence(t *testing.T) {
	base := t.TempDir()
	cfg := &Config{DB: &DBConfig{Backend: dbconfig.Postgres}, DatabaseURL: "postgresql://reader@localhost/config", StateDir: "configured"}
	env := func(k string) string {
		switch k {
		case "CORESCOPE_DATABASE_URL":
			return "postgresql://reader@localhost/generic"
		case "CORESCOPE_WRITER_DATABASE_URL":
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
	if got.WriterDatabaseURL != env("CORESCOPE_WRITER_DATABASE_URL") || got.StateDir != filepath.Join(base, "state") {
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
