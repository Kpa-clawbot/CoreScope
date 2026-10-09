package sqliteutil

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbconfig"
)

func open(t *testing.T, path, mode string) *sql.DB {
	t.Helper()
	dsn, err := dbconfig.SQLiteURI(path, url.Values{"mode": {mode}, "_busy_timeout": {"50"}})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSnapshotPreservesWALRowIDsAndSequence(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "live # %.db")
	destination := filepath.Join(dir, "snapshot # %.db")
	db := open(t, source, "rwc")
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
	 CREATE TABLE legacy(id TEXT); INSERT INTO legacy(rowid,id) VALUES(3,'a'),(101,'b');
	 CREATE TABLE identities(id INTEGER PRIMARY KEY AUTOINCREMENT,value TEXT);
	 INSERT INTO identities(id,value) VALUES(900,'deleted'); DELETE FROM identities;
	 INSERT INTO identities(value) VALUES('last');`); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(source + "-wal"); err != nil || info.Size() == 0 {
		t.Fatal("fixture must contain uncheckpointed WAL")
	}
	if err := Snapshot(context.Background(), source, destination); err != nil {
		t.Fatal(err)
	}
	copy := open(t, destination, "ro")
	var rowid, id int
	if err := copy.QueryRow(`SELECT rowid FROM legacy WHERE id='b'`).Scan(&rowid); err != nil || rowid != 101 {
		t.Fatalf("rowid=%d: %v", rowid, err)
	}
	if err := copy.QueryRow(`SELECT id FROM identities`).Scan(&id); err != nil || id != 901 {
		t.Fatalf("identity=%d: %v", id, err)
	}
	if err := copy.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='identities'`).Scan(&id); err != nil || id != 901 {
		t.Fatalf("sequence=%d: %v", id, err)
	}
	if _, err := copy.Exec(`CREATE TABLE forbidden(id)`); err == nil {
		t.Fatal("readonly snapshot handle allowed write")
	}
	if _, err := open(t, source, "ro").Exec(`DELETE FROM legacy`); err == nil {
		t.Fatal("readonly source allowed write")
	}
}

func TestSnapshotRefusesExistingAndMissingSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	if _, err := open(t, source, "rwc").Exec(`CREATE TABLE t(id);INSERT INTO t VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{source, filepath.Join(dir, "existing.db")} {
		if destination != source {
			if err := os.WriteFile(destination, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		before, err := os.ReadFile(destination)
		if err != nil {
			t.Fatal(err)
		}
		if err := Snapshot(context.Background(), source, destination); err == nil {
			t.Fatal("overwrote existing target")
		}
		after, err := os.ReadFile(destination)
		if err != nil || string(after) != string(before) {
			t.Fatal("existing target changed")
		}
	}
	missing := filepath.Join(dir, "missing.db")
	destination := filepath.Join(dir, "new.db")
	if err := Snapshot(context.Background(), missing, destination); err == nil {
		t.Fatal("missing source accepted")
	}
	for _, p := range []string{missing, destination} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected file %q: %v", p, err)
		}
	}
}

func TestSnapshotCancellationRemovesPartialOutput(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "busy.db")
	destination := filepath.Join(dir, "cancelled.db")
	db := open(t, source, "rwc")
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE t(id);INSERT INTO t VALUES(1);BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`ROLLBACK`)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if err := Snapshot(ctx, source, destination); err == nil {
		t.Fatal("snapshot ignored locked source/cancellation")
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Stat(destination + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial snapshot remains: %v", err)
		}
	}
}
