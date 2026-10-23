package legacy

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbconfig"
)

// TestWriterDSNPragmas pins what every writer connection actually gets.
//
// cmd/ingestor has its own end-to-end version of this through the store, but
// this one covers the DSN itself, which is what cmd/migrate also opens with.
// The synchronous line matters most: mattn defaults it to NORMAL and runs the
// pragma unconditionally, so if it ever drops out of the DSN the durability
// change is silent.
func TestWriterDSNPragmas(t *testing.T) {
	dsn, err := WriterDSN(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	for _, want := range []struct{ pragma, value, why string }{
		{"journal_mode", "wal", "cmd/server reads concurrently"},
		{"synchronous", "2", "FULL; mattn defaults to 1 (NORMAL) unless the DSN says otherwise"},
		{"auto_vacuum", "2", "INCREMENTAL; maintenance.go drives incremental_vacuum"},
		{"foreign_keys", "1", "the schema relies on FK enforcement"},
		{"busy_timeout", "5000", "writers serialise behind the reader"},
		{"cache_size", "-2000", "C-allocated page cache, pinned: it sits outside GOMEMLIMIT"},
	} {
		var got string
		if err := db.QueryRow("PRAGMA " + want.pragma).Scan(&got); err != nil {
			t.Errorf("PRAGMA %s: %v", want.pragma, err)
			continue
		}
		if got != want.value {
			t.Errorf("PRAGMA %s = %q, want %q (%s)", want.pragma, got, want.value, want.why)
		}
	}
}

func TestWriterDSNLiteralPathAndReadOnlyURI(t *testing.T) {
	names := []string{"literal #100%20 space µ.db"}
	if runtime.GOOS != "windows" {
		names = append(names, "literal ? query.db")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			dsn, err := WriterDSN(path)
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite3", dsn)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`CREATE TABLE kept(value TEXT);INSERT INTO kept VALUES('same file')`); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("literal filename was not created", err)
			}
			dsn, err = dbconfig.SQLiteURI(path, url.Values{"mode": {"ro"}})
			if err != nil {
				t.Fatal(err)
			}
			ro, err := sql.Open("sqlite3", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer ro.Close()
			var value string
			if err = ro.QueryRow(`SELECT value FROM kept`).Scan(&value); err != nil || value != "same file" {
				t.Fatalf("read-only URI opened another file: %q %v", value, err)
			}
			if _, err = ro.Exec(`INSERT INTO kept VALUES('forbidden')`); err == nil {
				t.Fatal("read-only URI allowed writes")
			}
		})
	}
	if _, err := WriterDSN(""); err == nil {
		t.Fatal("empty path accepted")
	}
}
