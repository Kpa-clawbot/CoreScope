package main

import (
	"database/sql"
	"fmt"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"
)

// TestServerDBHasNoWriteMethods enforces the architectural invariant from
// issue #1283: cmd/server is the read path. All write/maintenance methods
// (PruneOldPackets, PruneOldMetrics, RemoveStaleObservers) MUST live on
// the ingestor's *Store, not on the server's *DB.
//
// Before the fix, these methods existed on cmd/server/*DB and used
// cachedRW(db.path) to acquire a write lock, racing with the ingestor's
// concurrent INSERTs and producing SQLITE_BUSY (the bug in #1283).
// After the fix, this test passes because the methods are gone.
func TestServerDBHasNoWriteMethods(t *testing.T) {
	forbidden := []string{
		"PruneOldPackets",
		"PruneOldMetrics",
		"RemoveStaleObservers",
	}
	typ := reflect.TypeOf((*DB)(nil))
	for _, name := range forbidden {
		if _, ok := typ.MethodByName(name); ok {
			t.Errorf("server *DB exposes forbidden write method %q — must be relocated to ingestor (#1283)", name)
		}
	}
}

// TestServerDBConnIsReadOnly asserts that the *sql.DB the server opens
// cannot acquire a write lock. The server has always opened mode=ro, but
// before #1283 it routed around that by calling cachedRW(path) to get a
// second RW handle. After the fix, server-side writes are impossible
// because there is no helper to open a writable connection.
func TestServerDBConnIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/ro_invariant.db"

	// Bootstrap a minimal DB with the ingestor-style WAL opener so the
	// server can attach in read-only mode.
	if err := bootstrapMinimalDB(path); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	d, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer d.conn.Close()

	_, err = d.conn.Exec(`INSERT INTO nodes (public_key, name) VALUES ('x','y')`)
	if err == nil {
		t.Fatalf("expected INSERT via server *DB to fail (read-only invariant)")
	}
}

// bootstrapMinimalDB creates a tiny DB with the columns these tests
// need, opened with WAL so the read-only opener in OpenDB can attach.
// Kept in *_test.go so it does NOT add any write capability to the
// production server binary.
func bootstrapMinimalDB(path string) error {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000", path)
	rw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer rw.Close()
	if _, err := rw.Exec(`CREATE TABLE IF NOT EXISTS nodes (public_key TEXT PRIMARY KEY, name TEXT)`); err != nil {
		return err
	}
	return nil
}
