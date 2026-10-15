package users

import (
	"database/sql"
	"github.com/meshcore-analyzer/dbconfig"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSQLitePathOpensWithoutPostgres(t *testing.T) {
	t.Setenv("CORESCOPE_TEST_POSTGRES_URL", "")
	st, err := Open(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatalf("open default local account storage without PostgreSQL: %v", err)
	}
	defer st.Close()
	u := mustCreate(t, st, "local@example.invalid", "Local account")
	want := NotifyState{NotifyKey: NotifyKey{UserID: u.ID, Event: NotifyNodeBattery, Subject: "node-local"}, State: NotifyBad, ChangedAt: time.Unix(1234567890, 0).UTC()}
	if err := st.WriteNotifyStates([]NotifyState{want}); err != nil {
		t.Fatal(err)
	}
	got, err := st.AllNotifyStates()
	if err != nil || len(got) != 1 || got[0] != want {
		t.Fatalf("out-of-order notification parameters changed: %+v, %v", got, err)
	}
}

func TestModerncNumberedParametersPreservePositions(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var second, first, repeated int
	var null sql.NullString
	if err := db.QueryRow(`SELECT ?2,?1,?2,?3`, 11, 22, nil).Scan(&second, &first, &repeated, &null); err != nil {
		t.Fatal(err)
	}
	if second != 22 || first != 11 || repeated != 22 || null.Valid {
		t.Fatal("SQLite numbered parameters did not retain logical argument positions")
	}
}

func TestOpenStorageUsesOnlySelectedAccountTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "accounts.db")
	setup, err := Open(target)
	if err != nil {
		t.Fatal(err)
	}
	setup.Close()
	st, err := OpenStorage(dbconfig.Storage{Backend: dbconfig.SQLite, UsersDBPath: target, UsersDatabaseURL: "postgres://inactive.invalid/accounts", WriterDatabaseURL: "postgres://inactive.invalid/accounts"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Backend() != dbconfig.SQLite {
		t.Fatal("inactive URL changed selected backend")
	}
	st.Close()
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []dbconfig.Backend{dbconfig.Postgres, ""} {
		if st, err := OpenStorage(dbconfig.Storage{Backend: backend, UsersDBPath: target}); err == nil {
			st.Close()
			t.Fatal("missing explicit account backend/URL fell back to SQLite")
		}
	}
	after, err := os.ReadFile(target)
	if err != nil || string(before) != string(after) {
		t.Fatal("invalid selection mutated existing account file")
	}
	if st, err := OpenStorage(dbconfig.Storage{Backend: dbconfig.SQLite, UsersDBPath: target, DBPath: target}); err == nil {
		st.Close()
		t.Fatal("selected telemetry file accepted as account storage")
	}
}

func TestSQLiteAccountLiteralFilenameAndAliasIsolation(t *testing.T) {
	names := []string{"accounts # % ü.db"}
	if runtime.GOOS != "windows" {
		names = append(names, "accounts ?mode=memory.db")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			setup, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			setup.Close()
			st, err := OpenStorage(dbconfig.Storage{Backend: dbconfig.SQLite, UsersDBPath: path})
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			mustCreate(t, st, "literal@example.invalid", "Literal")
			if _, err := os.Stat(path); err != nil {
				t.Fatal("literal account file was not created:", err)
			}
			var actual string
			var n int
			if err := st.db.QueryRow(`PRAGMA database_list`).Scan(&n, &actual, &actual); err != nil {
				t.Fatal(err)
			}
			if !samePath(path, actual) {
				t.Fatal("URI opened another filesystem target")
			}
			alias := filepath.Join(filepath.Dir(path), "alias.db")
			if err := os.Link(path, alias); err != nil {
				t.Fatal(err)
			}
			if bad, err := OpenStorage(dbconfig.Storage{Backend: dbconfig.SQLite, UsersDBPath: alias, DBPath: path}); err == nil {
				bad.Close()
				t.Fatal("hardlink alias bypassed telemetry isolation")
			}
		})
	}
}
