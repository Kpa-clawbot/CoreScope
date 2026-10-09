package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
)

func TestRuntimeMissingSQLiteCannotCreateReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "uninitialized", "meshcore.db")
	raw := dbconfig.StorageInputs{BaseDir: dir, Backend: dbconfig.SQLite, DBPath: path}
	if err := adoptLegacyStorage(raw); err == nil {
		t.Fatal("missing source was treated as fresh")
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("runtime created replacement storage", err)
	}
}

func TestRuntimeAdoptsExistingSQLiteAndPinsRecordedChoice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meshcore.db")
	seed, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	seed.Close()
	raw := dbconfig.StorageInputs{BaseDir: dir, DBPath: path}
	if err := adoptLegacyStorage(raw); err != nil {
		t.Fatal(err)
	}
	raw.Backend = dbconfig.Postgres
	raw.EnvBackend = dbconfig.Postgres
	raw.DatabaseURL = "postgresql://unused@localhost/unselected"
	storage, lease, err := resolveRuntimeStorage(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if storage.Backend != dbconfig.SQLite || storage.DBPath != path || storage.UsersDBPath != "" {
		t.Fatal("obsolete bootstrap choices overrode recorded installation")
	}
	selected, err := dbconfig.ReadSelection(dbconfig.SelectionPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if selected.Accounts != nil {
		t.Fatal("absent accounts marked initialized")
	}
	if _, err := dbconfig.BeginSelectionUpdate(dbconfig.SelectionPath(dir), selected.Generation); !errors.Is(err, dbconfig.ErrSelectionBusy) {
		t.Fatal("runtime did not hold the selection lease", err)
	}
}

func TestRuntimeCannotAdoptStagedIncompleteSQLite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "staged.db")
	seed, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = seed.db.Exec(`CREATE TABLE ` + dbconfig.SQLiteImportPendingTable + `(phase TEXT)`); err != nil {
		t.Fatal(err)
	}
	seed.Close()
	raw := dbconfig.StorageInputs{BaseDir: dir, StateDir: dir, DBPath: path}
	if err := adoptLegacyStorage(raw); !errors.Is(err, dbconfig.ErrSQLiteImportIncomplete) {
		t.Fatal("incomplete target accepted", err)
	}
	if _, err := dbconfig.ReadSelection(dbconfig.SelectionPath(dir)); !errors.Is(err, dbconfig.ErrSelectionMissing) {
		t.Fatal("incomplete target recorded", err)
	}
}

func TestRecordedSQLiteCannotCreateReplacement(t *testing.T) {
	for _, empty := range []bool{false, true} {
		name := "missing"
		if empty {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "meshcore.db")
			seed, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			seed.Close()
			raw := dbconfig.StorageInputs{BaseDir: dir, DBPath: path}
			if err := adoptLegacyStorage(raw); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path, path+".preserved"); err != nil {
				t.Fatal(err)
			}
			if empty {
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			storage, lease, err := resolveRuntimeStorage(raw)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			store, err := openStoreStorage(storage, nil, 300)
			if err == nil {
				store.Close()
				t.Error("recorded runtime silently initialized replacement telemetry")
			}
			info, statErr := os.Stat(path)
			if empty {
				if statErr != nil || info.Size() != 0 {
					t.Error("runtime modified the empty recorded target", statErr)
				}
			} else if !errors.Is(statErr, os.ErrNotExist) {
				t.Error("runtime created a missing recorded target", statErr)
			}
		})
	}
}
