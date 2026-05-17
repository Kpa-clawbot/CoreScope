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
