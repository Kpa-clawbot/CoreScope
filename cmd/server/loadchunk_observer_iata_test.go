package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The background loader (loadChunk) fills in days 2-7 of the retention
// window after the hot startup window. Its query left out the observer's
// IATA code, so packets older than the hot window lost observer_iata in the
// packets API and the iata filter (#1188), while Load and ingest kept it.
func TestLoadChunkKeepsObserverIATA(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "iata.db")
	createTestDBAt(t, dbPath, 20)
	t.Cleanup(func() { os.RemoveAll(dir) })
	conn, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO observers (id, name, iata) VALUES ('obs1', 'Obs1', 'SJC')`); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := NewPacketStore(db, &PacketStoreConfig{})
	from := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	if err := store.loadChunk(from, from.Add(72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if len(store.packets) == 0 {
		t.Fatal("loadChunk loaded nothing")
	}
	for _, tx := range store.packets {
		for _, o := range tx.Observations {
			if o.ObserverIATA != "SJC" {
				t.Fatalf("tx %d: observation IATA = %q, want SJC", tx.ID, o.ObserverIATA)
			}
		}
		if tx.ObserverIATA != "SJC" {
			t.Fatalf("tx %d: IATA = %q, want SJC", tx.ID, tx.ObserverIATA)
		}
	}
}
