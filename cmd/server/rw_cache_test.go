package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCachedRW_ReturnsSameHandle(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create the DB file
	f, _ := os.Create(dbPath)
	f.Close()

	defer closeRWCache()

	db1, err := cachedRW(dbPath)
	if err != nil {
		t.Fatalf("first cachedRW: %v", err)
	}
	db2, err := cachedRW(dbPath)
	if err != nil {
		t.Fatalf("second cachedRW: %v", err)
	}
	if db1 != db2 {
		t.Fatalf("cachedRW returned different handles: %p vs %p", db1, db2)
	}
}

func TestCachedRW_100Calls_SingleConnection(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	f, _ := os.Create(dbPath)
	f.Close()

	defer closeRWCache()

	var first interface{}
	for i := 0; i < 100; i++ {
		db, err := cachedRW(dbPath)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if i == 0 {
			first = db
		} else if db != first {
			t.Fatalf("call %d returned different handle", i)
		}
	}
	if rwCacheLen() != 1 {
		t.Fatalf("expected 1 cached connection, got %d", rwCacheLen())
	}
}

func TestSQLiteRWDSNPreservesFileURI(t *testing.T) {
	got := sqliteRWDSN("file:///var/lib/meshcore.db?mode=rwc")
	want := "file:///var/lib/meshcore.db?mode=rwc&_journal_mode=WAL"
	if got != want {
		t.Fatalf("sqliteRWDSN file URI = %q, want %q", got, want)
	}
}

func TestSQLiteRWDSNPlainPath(t *testing.T) {
	got := sqliteRWDSN("data/meshcore.db")
	want := "file:data/meshcore.db?_journal_mode=WAL"
	if got != want {
		t.Fatalf("sqliteRWDSN plain path = %q, want %q", got, want)
	}
}
