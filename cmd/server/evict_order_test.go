package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// s.packets is declared "sorted by first_seen ASC (oldest first; newest at
// tail)" (store.go:177), and retention eviction depends on it: it walks from
// the head and stops at the first transmission inside the window, so a slice
// out of order is silently under-evicted rather than noisily wrong.
//
// The background chunk loader used to break that. Chunks are windowed on
// last_seen, so a transmission first heard weeks ago and heard again recently
// arrives in a recent chunk carrying its old FirstSeen; the chunk was then put
// in front of the slice with `append(localPackets, s.packets...)` and never
// re-sorted, leaving that ancient row behind everything a later, older chunk
// prepended. On a production database 2071 of the 236080 transmissions in a
// 14 day window have a first_seen more than a day older than their last_seen,
// 1848 of them more than a week.

// seedReheardDB writes a database where one transmission is first heard weeks
// before the others and heard again recently, which is what puts it in a
// recent chunk with an ancient first_seen.
func seedReheardDB(t *testing.T, path string, now time.Time) {
	t.Helper()
	conn, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	defer conn.Close()

	stmts := []string{
		`CREATE TABLE transmissions (id INTEGER PRIMARY KEY, raw_hex TEXT, hash TEXT UNIQUE,
			first_seen TEXT, route_type INTEGER, payload_type INTEGER, payload_version INTEGER,
			decoded_json TEXT, last_seen TEXT)`,
		`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_id TEXT,
			observer_name TEXT, direction TEXT, snr REAL, rssi REAL, score INTEGER, path_json TEXT,
			timestamp INTEGER)`,
		`CREATE INDEX idx_tx_last_seen ON transmissions(last_seen)`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(s); err != nil {
			t.Fatalf("seed exec: %v\nSQL: %s", err, s)
		}
	}

	// id, first_seen offset (hours before now), last_seen offset.
	rows := []struct {
		id                int
		firstSeenH, lastH int
	}{
		{1, 24 * 21, 1},       // re-heard: first seen three weeks ago, heard again an hour ago
		{2, 2, 2},             // recent, in the same recent window
		{3, 24 * 5, 24*5 - 1}, // genuinely older, lands in an older chunk
		{4, 24*5 + 1, 24 * 5}, // ditto
	}
	for _, r := range rows {
		fs := now.Add(-time.Duration(r.firstSeenH) * time.Hour).Format(time.RFC3339)
		ls := now.Add(-time.Duration(r.lastH) * time.Hour).Format(time.RFC3339)
		if _, err := conn.Exec(
			`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type,
				payload_version, decoded_json, last_seen) VALUES (?,?,?,?,?,?,?,?,?)`,
			r.id, "aabb", fmt.Sprintf("h%04d", r.id), fs, 0, 4, 1,
			fmt.Sprintf(`{"pubKey":"pk%04d"}`, r.id), ls); err != nil {
			t.Fatalf("insert tx %d: %v", r.id, err)
		}
		if _, err := conn.Exec(
			`INSERT INTO observations (id, transmission_id, observer_id, observer_name, direction,
				snr, rssi, score, path_json, timestamp) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			r.id, r.id, "obs1", "Obs1", "RX", -10.0, -80.0, 5, `["aa"]`,
			now.Add(-time.Duration(r.lastH)*time.Hour).Unix()); err != nil {
			t.Fatalf("insert obs %d: %v", r.id, err)
		}
	}
}

// TestLoadChunk_KeepsPacketsSortedByFirstSeen drives the real background path:
// the recent chunk first, as the loader does, then an older one. The ancient
// re-heard row rides in with the recent chunk, so prepending the older chunk
// in front of it leaves the slice out of order.
func TestLoadChunk_KeepsPacketsSortedByFirstSeen(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	dbPath := filepath.Join(t.TempDir(), "reheard.db")
	seedReheardDB(t, dbPath, now)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()
	store := NewPacketStore(db, &PacketStoreConfig{})

	// Newest window first, then the older one: the order the background
	// loader walks, and the order that made the prepend look safe.
	if err := store.loadChunk(now.Add(-24*time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatalf("loadChunk (recent): %v", err)
	}
	if err := store.loadChunk(now.Add(-6*24*time.Hour), now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("loadChunk (older): %v", err)
	}

	if len(store.packets) < 3 {
		t.Fatalf("loaded %d packets, want at least 3 — the seed or the window is wrong", len(store.packets))
	}
	for i := 1; i < len(store.packets); i++ {
		if store.packets[i-1].FirstSeen > store.packets[i].FirstSeen {
			t.Fatalf("s.packets is out of order at index %d: %s (%s) before %s (%s). Retention eviction walks from the head and stops at the first in-window packet, so everything behind this point is never evicted",
				i, store.packets[i-1].Hash, store.packets[i-1].FirstSeen,
				store.packets[i].Hash, store.packets[i].FirstSeen)
		}
	}
}
