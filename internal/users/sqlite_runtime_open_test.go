package users

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
)

func TestSelectedSQLiteAccountsCannotInitializeReplacement(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "header only", "wrong schema"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "accounts.db")
			original, err := Open(path) // Explicit setup/fixture creation.
			if err != nil {
				t.Fatal(err)
			}
			user := mustCreate(t, original, "preserved@example.invalid", "Preserved")
			token, session, err := original.CreateSession(user.ID, time.Hour, "test")
			if err != nil {
				t.Fatal(err)
			}
			mustSQL(t, original.db, `INSERT INTO audit_log(id,at,action,detail) VALUES(900,1,'seed','{}'); DELETE FROM audit_log WHERE id=900`)
			original.Close()
			selected, err := dbconfig.NewSelection(dbconfig.Storage{Backend: dbconfig.SQLite, StateDir: dir, DBPath: filepath.Join(dir, "telemetry.db"), UsersDBPath: path})
			if err != nil {
				t.Fatal(err)
			}
			preserved := path + ".preserved"
			if err := os.Rename(path, preserved); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(preserved)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "empty" {
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else if kind == "header only" || kind == "wrong schema" {
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				query := `PRAGMA user_version=1`
				if kind == "wrong schema" {
					query = `CREATE TABLE unrelated(value TEXT); INSERT INTO unrelated VALUES('keep')`
				}
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
			badBefore, _ := os.ReadFile(path)
			raw, err := selected.ApplyTo(dbconfig.StorageInputs{BaseDir: dir, UsersDBPath: filepath.Join(dir, "inactive-old-setting.db")})
			if err != nil {
				t.Fatal(err)
			}
			storage, err := dbconfig.ResolveStorage(raw)
			if err != nil {
				t.Fatal(err)
			}
			if storage.UsersDBPath != path {
				t.Fatal("recorded initialized account target disappeared from resolution")
			}
			if st, err := OpenStorage(storage); err == nil {
				st.Close()
				t.Fatal("selected account storage initialized a replacement")
			}
			badAfter, statErr := os.ReadFile(path)
			if kind == "missing" {
				if !errors.Is(statErr, os.ErrNotExist) {
					t.Fatal("runtime created missing accounts", statErr)
				}
			} else {
				if statErr != nil || sha256.Sum256(badBefore) != sha256.Sum256(badAfter) {
					t.Fatal("runtime modified rejected account target", statErr)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			after, err := os.ReadFile(preserved)
			if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
				t.Fatal("preserved accounts changed", err)
			}
			if err := os.Rename(preserved, path); err != nil {
				t.Fatal(err)
			}
			restored, err := OpenStorage(storage)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			if got, err := restored.GetByID(user.ID); err != nil || got.Email != user.Email {
				t.Fatal("user lost after restoring original store", err)
			}
			if got, err := restored.LookupSession(token); err != nil || got.ID != session.ID {
				t.Fatal("session lost after restoring original store", err)
			}
			var sequence int
			if err := restored.db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='audit_log'`).Scan(&sequence); err != nil || sequence != 900 {
				t.Fatal("allocator high-water lost", sequence, err)
			}
		})
	}
}

func TestSQLiteAccountTargetRemovedAfterDSNValidation(t *testing.T) {
	for _, replaceEmpty := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "accounts.db")
		setup, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		setup.Close()
		dsn, err := sqliteAccountDSN(path, false)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.Rename(path, path+".preserved"); err != nil {
			t.Fatal(err)
		}
		if replaceEmpty {
			if err = os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		if err = validateExistingSQLiteAccounts(db); err == nil {
			t.Fatal("raced missing/empty target accepted")
		}
		db.Close()
		info, err := os.Stat(path)
		if replaceEmpty {
			if err != nil || info.Size() != 0 {
				t.Fatal("raced empty accounts were initialized", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal("raced missing accounts were recreated", err)
		}
	}
}

func TestSelectedSQLiteAccountsUpgradeSupportedInitializedVersions(t *testing.T) {
	for version := 1; version < SQLiteSchemaVersion; version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path, db := legacySQLiteFixture(t, version, false)
			mustSQL(t, db, `INSERT INTO users(id,email,display_name,password_hash,role,status,created_at) VALUES(91,'legacy@example.invalid','Legacy','hash','admin','active',1)`)
			db.Close()
			st, err := OpenStorage(dbconfig.Storage{Backend: dbconfig.SQLite, UsersDBPath: path})
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if user, err := st.GetByID(91); err != nil || user.Email != "legacy@example.invalid" {
				t.Fatal("supported initialized account row lost", err)
			}
			if err = AssertSQLiteReady(st.db); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSelectedSQLiteAccountsNeedExplicitInitialization(t *testing.T) {
	dir := t.TempDir()
	selected, err := dbconfig.NewSelection(dbconfig.Storage{Backend: dbconfig.SQLite, StateDir: dir, DBPath: filepath.Join(dir, "telemetry.db")})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := selected.ApplyTo(dbconfig.StorageInputs{BaseDir: dir, UsersDBPath: filepath.Join(dir, "unused.db")})
	if err != nil {
		t.Fatal(err)
	}
	uninitialized, err := dbconfig.ResolveStorage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if uninitialized.UsersDBPath != "" {
		t.Fatal("uninitialized selected accounts received an inferred file")
	}
	if st, err := OpenStorage(uninitialized); err == nil {
		st.Close()
		t.Fatal("uninitialized accounts were opened")
	}
	path := filepath.Join(t.TempDir(), "new", "accounts.db")
	if st, err := OpenStorage(dbconfig.Storage{Backend: dbconfig.SQLite, UsersDBPath: path}); err == nil {
		st.Close()
		t.Fatal("selected runtime performed first initialization")
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("runtime created account directory", err)
	}
	setup, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	setup.Close()
	runtime, err := OpenStorage(dbconfig.Storage{Backend: dbconfig.SQLite, UsersDBPath: path})
	if err != nil {
		t.Fatal("explicitly initialized accounts failed to open", err)
	}
	runtime.Close()
}
