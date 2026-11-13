package main

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbx/dbxtest"
)

// populatedSQLite leaves a SQLite database at path holding n transmissions,
// written the way the ingestor writes them, and returns its size and mtime.
func populatedSQLite(t *testing.T, path string, n int) os.FileInfo {
	t.Helper()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, err := s.InsertTransmission(&PacketData{
			RawHex: "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976", Hash: "guard-" + string(rune('a'+i)),
			Timestamp: "2026-10-01T00:00:00Z", ObserverID: "obs1",
		}); err != nil {
			t.Fatal(err)
		}
	}
	s.WaitForAsyncMigrations()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestEmptyPostgresRefusedWhileSQLiteHoldsData: switching db.driver to
// postgres before copying the data must not start an empty mesh. The SQLite
// file is only read, never written.
func TestEmptyPostgresRefusedWhileSQLiteHoldsData(t *testing.T) {
	pg := dbxtest.OpenPostgres(t)
	path := filepath.Join(t.TempDir(), "meshcore.db")
	before := populatedSQLite(t, path, 3)
	time.Sleep(10 * time.Millisecond) // an mtime change would be visible

	s, err := openPostgresStoreDB(pg, path, 300)
	if err == nil {
		s.Close()
		t.Fatal("opened an empty Postgres database while the SQLite file still holds transmissions")
	}
	want := `refusing to start on an empty PostgreSQL database while ` + path + ` still holds 3 transmissions: ` +
		`copy it first with "corescope-migrate copy -from sqlite:` + path + ` -to <postgres url>", or move ` + path +
		` aside to start with an empty database`
	if err.Error() != want {
		t.Fatalf("error =\n  %s\nwant\n  %s", err, want)
	}
	var refusal *emptyPostgresTargetError
	if !errors.As(err, &refusal) {
		t.Fatalf("error %T is not the guard's refusal", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the guard modified %s: size %d -> %d, mtime %v -> %v", path, before.Size(), after.Size(), before.ModTime(), after.ModTime())
	}
}

// TestEmptyPostgresStartsWithoutSQLiteData: no file, an empty SQLite
// database, a SQLite database without the table, or a file that is not
// SQLite at queuePath all start an empty Postgres. None of them is created or
// changed by the check.
func TestEmptyPostgresStartsWithoutSQLiteData(t *testing.T) {
	cases := map[string]func(t *testing.T, path string){
		"no file": func(t *testing.T, path string) {},
		"empty sqlite store": func(t *testing.T, path string) {
			populatedSQLite(t, path, 0)
		},
		"not sqlite": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("this is not a database, just a file someone left here"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"zero-byte file": func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"sqlite without transmissions": func(t *testing.T, path string) {
			db, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE notes (body TEXT); INSERT INTO notes VALUES ('x')`); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			pg := dbxtest.OpenPostgres(t)
			path := filepath.Join(t.TempDir(), "meshcore.db")
			setup(t, path)
			_, statErr := os.Stat(path)

			s, err := openPostgresStoreDB(pg, path, 300)
			if err != nil {
				t.Fatalf("openPostgresStoreDB: %v", err)
			}
			defer s.Close()
			if !s.pg {
				t.Fatal("store not on Postgres")
			}
			if _, err := os.Stat(path); os.IsNotExist(err) != os.IsNotExist(statErr) {
				t.Errorf("the check changed whether %s exists", path)
			}
		})
	}
}

// TestNonEmptyPostgresStartsOverPopulatedSQLite: once Postgres holds data the
// old file is irrelevant; a leftover SQLite database must not block restarts.
func TestNonEmptyPostgresStartsOverPopulatedSQLite(t *testing.T) {
	pg := dbxtest.OpenPostgres(t)
	dir := t.TempDir()
	first, err := openPostgresStoreDB(pg, filepath.Join(dir, "elsewhere.db"), 300)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.InsertTransmission(&PacketData{
		RawHex: "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976", Hash: "pg-row", Timestamp: "2026-10-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "meshcore.db")
	populatedSQLite(t, path, 2)
	second, err := openPostgresStoreDB(pg, path, 300)
	if err != nil {
		t.Fatalf("refused a populated Postgres database: %v", err)
	}
	second.Close()
}

// TestOpenPostgresStoreNeverLeaksDSN: whatever goes wrong while connecting,
// the error the operator sees (and main logs) carries neither the DSN nor
// its password. Needs no Postgres: every DSN points at a closed port or does
// not parse.
func TestOpenPostgresStoreNeverLeaksDSN(t *testing.T) {
	const pw = "Sup3rS3cretPW"
	for _, dsn := range []string{
		"postgres://cs:" + pw + "@127.0.0.1:1/x?sslmode=disable&connect_timeout=2",
		"postgresql://cs:" + pw + "@127.0.0.1:1/x?sslmode=disable&connect_timeout=2",
		"postgres://127.0.0.1:1/x?user=cs&password=" + pw + "&sslmode=disable&connect_timeout=2",
		"postgres://cs:" + pw + "@127.0.0.1:notaport/x",
		"postgres://cs:" + pw + "@[::1/x",
		"host=127.0.0.1 port=1 user=cs password=" + pw + " dbname=x sslmode=disable connect_timeout=2",
		"host=127.0.0.1 port=1 user=cs password='" + pw + " x' dbname=x sslmode=disable connect_timeout=2",
		"host=127.0.0.1 port=notaport user=cs password=" + pw + " dbname=x",
	} {
		s, err := OpenPostgresStore(dsn, filepath.Join(t.TempDir(), "meshcore.db"), 300)
		if err == nil {
			s.Close()
			t.Errorf("opened %q", strings.ReplaceAll(dsn, pw, "<pw>"))
			continue
		}
		if msg := err.Error(); strings.Contains(msg, pw) || strings.Contains(msg, dsn) {
			t.Errorf("error leaks the password: %s", strings.ReplaceAll(msg, pw, "<PASSWORD>"))
		}
	}
}

func TestRedactDSNError(t *testing.T) {
	dsn := "postgres://cs:p%40ss@db:5432/x"
	err := redactDSNError(errors.New("cannot parse `"+dsn+"`: user cs:p%40ss pw p@ss"), dsn)
	if m := err.Error(); strings.Contains(m, "p@ss") || strings.Contains(m, "p%40ss") {
		t.Fatalf("not redacted: %s", m)
	}
	plain := errors.New("connection refused")
	if got := redactDSNError(plain, dsn); got != plain {
		t.Fatalf("an error without secrets was rewrapped: %v", got)
	}
}

// TestEmptyPostgresGuardChecksExtraPath: in the Docker image on Postgres the
// ingestor's dbPath points into /app/data/shared (it only anchors the file
// queues), so the operator's SQLite data at /app/data/meshcore.db would be
// invisible to the guard. CORESCOPE_SQLITE_GUARD_PATH names that file, and an
// empty Postgres must still be refused while it holds transmissions.
func TestEmptyPostgresGuardChecksExtraPath(t *testing.T) {
	pg := dbxtest.OpenPostgres(t)
	dir := t.TempDir()
	legacy := filepath.Join(dir, "meshcore.db")
	populatedSQLite(t, legacy, 2)
	queue := filepath.Join(dir, "shared", "meshcore.db") // absent, as in the image
	t.Setenv(sqliteGuardEnv, legacy)

	s, err := openPostgresStoreDB(pg, queue, 300)
	if err == nil {
		s.Close()
		t.Fatal("opened an empty Postgres database while the guard path still holds transmissions")
	}
	var refusal *emptyPostgresTargetError
	if !errors.As(err, &refusal) || !strings.Contains(err.Error(), legacy+" still holds 2 transmissions") {
		t.Fatalf("want the guard's refusal naming %s, got %v", legacy, err)
	}
}

func TestSQLiteGuardPaths(t *testing.T) {
	t.Setenv(sqliteGuardEnv, "")
	if got := sqliteGuardPaths("data/meshcore.db"); len(got) != 1 {
		t.Fatalf("unset env: %v", got)
	}
	t.Setenv(sqliteGuardEnv, "./data/meshcore.db")
	if got := sqliteGuardPaths("data/meshcore.db"); len(got) != 1 {
		t.Fatalf("same path after Clean must not be checked twice: %v", got)
	}
	t.Setenv(sqliteGuardEnv, "/app/data/meshcore.db")
	if got := sqliteGuardPaths("/app/data/shared/meshcore.db"); len(got) != 2 || got[1] != "/app/data/meshcore.db" {
		t.Fatalf("extra path: %v", got)
	}
}
