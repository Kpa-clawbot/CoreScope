package dbconfig

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageSelection(t *testing.T) {
	base := t.TempDir()
	pg := "postgres://reader:private-value@localhost/telemetry"
	cases := []struct {
		name string
		in   StorageInputs
		want Backend
		err  bool
	}{
		{"fresh", StorageInputs{FreshInstall: true}, SQLite, false},
		{"unknown existing", StorageInputs{}, "", true},
		{"legacy sqlite", StorageInputs{DBPath: "old/mesh.db"}, SQLite, false},
		{"legacy postgres", StorageInputs{DatabaseURL: pg}, Postgres, false},
		{"legacy conflict", StorageInputs{DBPath: "old.db", DatabaseURL: pg}, "", true},
		{"recorded postgres missing url", StorageInputs{ExistingBackend: Postgres, FreshInstall: true}, "", true},
		{"recorded postgres", StorageInputs{ExistingBackend: Postgres, ReaderDatabaseURL: pg}, Postgres, false},
		{"ordinary backend change", StorageInputs{Backend: SQLite, ExistingBackend: Postgres}, "", true},
		{"selector conflict", StorageInputs{Backend: SQLite, EnvBackend: Postgres, DatabaseURL: pg}, "", true},
		{"unknown selector", StorageInputs{Backend: "mysql", FreshInstall: true}, "", true},
		{"explicit sqlite ignores inactive pg", StorageInputs{Backend: SQLite, DatabaseURL: "private-invalid-pg"}, SQLite, false},
		{"explicit postgres ignores old path", StorageInputs{Backend: Postgres, DBPath: "old.db", DatabaseURL: pg}, Postgres, false},
		{"postgres path is not url", StorageInputs{Backend: Postgres, DatabaseURL: "old.db"}, "", true},
		{"sqlite url is not path", StorageInputs{Backend: SQLite, DBPath: pg}, "", true},
		{"accounts alone cannot configure telemetry", StorageInputs{UsersDatabaseURL: pg}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.in.BaseDir = base
			got, err := ResolveStorage(c.in)
			if (err != nil) != c.err {
				t.Fatalf("error = %v, want error %v", err, c.err)
			}
			if err != nil {
				if strings.Contains(err.Error(), "private-") {
					t.Fatal("error leaked connection value")
				}
				return
			}
			if got.Backend != c.want {
				t.Fatalf("backend=%q, want %q", got.Backend, c.want)
			}
		})
	}
}

func TestStorageTargets(t *testing.T) {
	base := t.TempDir()
	got, err := ResolveStorage(StorageInputs{BaseDir: base, Backend: SQLite, DBPath: "legacy/mesh.db"})
	if err != nil {
		t.Fatal(err)
	}
	if got.DBPath != filepath.Join(base, "legacy", "mesh.db") || got.UsersDBPath != filepath.Join(base, "legacy", "users.db") || got.StateDir != filepath.Join(base, "legacy") {
		t.Fatalf("SQLite paths: %+v", got)
	}
	if _, err := ResolveStorage(StorageInputs{BaseDir: base, Backend: SQLite, DBPath: "same.db", UsersDBPath: "same.db"}); err == nil {
		t.Fatal("accepted same telemetry/account target")
	}
	if _, err := ResolveStorage(StorageInputs{BaseDir: "relative", Backend: SQLite}); err == nil {
		t.Fatal("accepted non-absolute base instead of caller-resolved location")
	}
	common := "postgres://common@localhost/telemetry"
	reader := "postgres://reader@localhost/telemetry"
	got, err = ResolveStorage(StorageInputs{BaseDir: base, Backend: Postgres, DatabaseURL: common, ReaderDatabaseURL: reader, StateDir: "state"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ReaderDatabaseURL != reader || got.WriterDatabaseURL != common || got.StateDir != filepath.Join(base, "state") || got.DBPath != "" || got.UsersDBPath != "" {
		t.Fatal("PostgreSQL roles/state were not resolved independently of file paths")
	}
	got, err = ResolveStorage(StorageInputs{BaseDir: base, FreshInstall: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.DBPath != filepath.Join(base, "data", "meshcore.db") || got.UsersDBPath != filepath.Join(base, "data", "users.db") {
		t.Fatal("fresh SQLite defaults changed")
	}
}

func TestBackendParameters(t *testing.T) {
	for _, backend := range []Backend{SQLite, Postgres} {
		prefix := "$"
		if backend == SQLite {
			prefix = "?"
		}
		if got := backend.Parameter(12); got != prefix+"12" {
			t.Fatalf("parameter=%q", got)
		}
	}
	for _, call := range []func(){func() { SQLite.Parameter(0) }, func() { Backend("unknown").Parameter(1) }} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("invalid parameter construction did not fail")
				}
			}()
			call()
		}()
	}
}
