package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattn/go-sqlite3"
)

func TestSnapshotSourceIncludesWALWithoutChangingSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	destination := filepath.Join(dir, "recovery.db")
	db, err := sql.Open("sqlite3", source+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`PRAGMA wal_autocheckpoint=0`,
		`CREATE TABLE records (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO records VALUES (0, NULL), (90, '')`,
		`CREATE TABLE identities (key TEXT PRIMARY KEY)`,
		`INSERT INTO identities(rowid,key) VALUES (0,'zero'),(900,'gap')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	before := make(map[string][32]byte)
	for _, path := range []string{source, source + "-wal"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = sha256.Sum256(content)
	}
	if err := snapshotSource(context.Background(), source, destination); err != nil {
		t.Fatalf("WAL-aware recovery snapshot: %v", err)
	}
	for path, expected := range before {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if sha256.Sum256(content) != expected {
			t.Fatalf("source was modified: %s", filepath.Base(path))
		}
	}
	copyDB, err := sql.Open("sqlite3", destination)
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	var count int
	if err := copyDB.QueryRow(`SELECT COUNT(*) FROM records WHERE (id=0 AND value IS NULL) OR (id=90 AND value='')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("snapshot lost committed WAL rows or null/empty values: got %d", count)
	}
	if err := copyDB.QueryRow(`SELECT COUNT(*) FROM identities WHERE (rowid=0 AND key='zero') OR (rowid=900 AND key='gap')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatal("snapshot changed hidden rowids")
	}
}

func TestSnapshotSourceDoesNotOverwriteRecoveryOrOriginal(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	db, err := sql.Open("sqlite3", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE records(id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{source, filepath.Join(dir, "existing.db")} {
		if destination != source {
			if err := os.WriteFile(destination, []byte("existing recovery"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		before, err := os.ReadFile(destination)
		if err != nil {
			t.Fatal(err)
		}
		if err := snapshotSource(context.Background(), source, destination); err == nil {
			t.Fatal("overwriting an existing file was accepted")
		}
		after, err := os.ReadFile(destination)
		if err != nil {
			t.Fatal(err)
		}
		if sha256.Sum256(before) != sha256.Sum256(after) {
			t.Fatal("existing file was changed")
		}
	}
}

func TestSnapshotSourceFailureLeavesNoPartialCopy(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		dir := t.TempDir()
		source, destination := filepath.Join(dir, "source.db"), filepath.Join(dir, "recovery.db")
		if err := os.WriteFile(source, []byte("not a SQLite database"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		if cancelled {
			cancel()
		}
		if err := snapshotSource(ctx, source, destination); err == nil {
			t.Fatal("invalid/cancelled snapshot accepted")
		}
		cancel()
		if _, err := os.Stat(destination); !os.IsNotExist(err) {
			t.Fatalf("partial output retained: %v", err)
		}
	}
}

func TestSourceFingerprintEmptyWALMatchesAbsentWAL(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.db")
	if err := os.WriteFile(source, []byte("stable database bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := sourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source+"-wal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	empty, err := sourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	if empty != before {
		t.Fatal("an empty WAL changed the source identity")
	}
	if err := os.WriteFile(source+"-wal", []byte("nonempty WAL bytes must be observed"), 0o600); err != nil {
		t.Fatal(err)
	}
	nonempty, err := sourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	if nonempty == before {
		t.Fatal("nonempty WAL bytes were ignored")
	}
}

func TestSQLiteSourceLengthLimitPrecedesScan(t *testing.T) {
	db, err := sql.Open(importSQLiteDriver, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Raw(func(driver any) error {
		sqlite := driver.(*sqlite3.SQLiteConn)
		if got := sqlite.GetLimit(sqlite3.SQLITE_LIMIT_LENGTH); got != maxImportRowBytes {
			t.Fatalf("native SQLite length limit=%d", got)
		}
		sqlite.SetLimit(sqlite3.SQLITE_LIMIT_LENGTH, 1024)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var data []byte
	if err := conn.QueryRowContext(context.Background(), `SELECT zeroblob(2048)`).Scan(&data); err == nil {
		t.Fatal("native length limit did not reject a large result before Scan")
	}
}
