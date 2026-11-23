package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// legacyObserversTableDDL is the observers table as the previous release left
// it: every canonical column, but the implicit rowid instead of idx.
const legacyObserversTableDDL = `CREATE TABLE observers (
	id TEXT PRIMARY KEY,
	name TEXT,
	iata TEXT,
	last_seen TEXT,
	first_seen TEXT,
	packet_count INTEGER DEFAULT 0,
	model TEXT,
	firmware TEXT,
	client_version TEXT,
	radio TEXT,
	battery_mv INTEGER,
	uptime_secs INTEGER,
	noise_floor REAL,
	inactive INTEGER DEFAULT 0,
	last_packet_at TEXT DEFAULT NULL,
	clock_skew_seconds INTEGER DEFAULT NULL,
	clock_skew_count_24h INTEGER DEFAULT 0,
	clock_last_naive_at TEXT DEFAULT NULL,
	can_relay INTEGER DEFAULT 1,
	can_relay_seen INTEGER DEFAULT 0
)`

// legacyStoreDB builds a database as an upgrade from the previous release
// finds it: a full ingestor schema (every migration recorded) whose observers
// table is still the legacy rowid shape, with observations pointing at those
// rowids. mutate then makes the table one the rebuild must refuse.
func legacyStoreDB(t *testing.T, mutate ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.WaitForAsyncMigrations()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		`DROP VIEW packets_v`,
		`DROP TABLE observers`,
		`DELETE FROM sqlite_sequence WHERE name = 'observers'`,
		legacyObserversTableDDL,
		`CREATE INDEX idx_observers_last_seen ON observers(last_seen)`,
		`INSERT INTO observers (id, name) VALUES ('a', 'A'), ('b', 'B'), ('c', 'C')`,
		`DELETE FROM observers WHERE id = 'b'`,
		`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1, 'aa', 'h1', '2026-01-01T00:00:00Z')`,
		`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (1, 1, '[]', 1000), (1, 3, '[]', 1001), (1, 2, '[]', 1002)`,
	}
	stmts = append(stmts, mutate...)
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("legacy fixture %q: %v", q, err)
		}
	}
	// The legacy-shape view, on the rowid join, as the previous release had it.
	if _, err := db.Exec(`CREATE VIEW packets_v AS
		SELECT o.id, obs.id AS observer_id FROM observations o
		JOIN transmissions t ON t.id = o.transmission_id
		LEFT JOIN observers obs ON obs.rowid = o.observer_idx`); err != nil {
		t.Fatal(err)
	}
	return path
}

func dumpQuery(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%v", vals))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// observersSnapshot is what a refused rebuild must leave as it found it.
func observersSnapshot(t *testing.T, path string) (table, rows, links []string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	table = dumpQuery(t, db, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'observers'`)
	rows = dumpQuery(t, db, `SELECT rowid, * FROM observers ORDER BY rowid`)
	// packets_v must answer a full read, joining observations to the same
	// observers as before.
	links = dumpQuery(t, db, `SELECT id, COALESCE(observer_id, '') FROM packets_v ORDER BY id`)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM packets_v`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("SELECT COUNT(*) FROM packets_v = %d (%v), want 3", n, err)
	}
	return table, rows, links
}

// A legacy observers table the rebuild cannot convert without losing data
// makes OpenStore fail, and leaves the table, its rows and packets_v as they
// were.
func TestOpenStoreRefusesUnconvertibleObservers(t *testing.T) {
	cases := []struct {
		name    string
		mutate  []string
		wantErr string
	}{
		{"unknown column", []string{
			`ALTER TABLE observers ADD COLUMN operator_note TEXT`,
			`UPDATE observers SET operator_note = 'keep me'`,
		}, "operator_note"},
		{"null id", []string{
			`INSERT INTO observers (id, name) VALUES (NULL, 'ghost')`,
		}, "NULL id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := legacyStoreDB(t, tc.mutate...)
			table, rows, links := observersSnapshot(t, path)
			if want := []string{"[1 a]", "[2 c]", "[3 ]"}; !reflect.DeepEqual(links, want) {
				t.Fatalf("fixture links = %v, want %v", links, want)
			}

			s, err := OpenStore(path)
			if err == nil {
				s.Close()
				t.Fatal("OpenStore accepted an observers table the rebuild must refuse")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("OpenStore err = %v, want mention of %q", err, tc.wantErr)
			}

			gotTable, gotRows, gotLinks := observersSnapshot(t, path)
			if !reflect.DeepEqual(table, gotTable) {
				t.Errorf("observers table changed:\nbefore=%v\nafter=%v", table, gotTable)
			}
			if !reflect.DeepEqual(rows, gotRows) {
				t.Errorf("observers rows changed:\nbefore=%v\nafter=%v", rows, gotRows)
			}
			if !reflect.DeepEqual(links, gotLinks) {
				t.Errorf("packets_v links changed:\nbefore=%v\nafter=%v", links, gotLinks)
			}
		})
	}
}

// openFDsOn counts this process's open file descriptors on path or its
// -wal/-shm siblings. ok is false where /proc/self/fd is unavailable.
func openFDsOn(t *testing.T, path string) (n int, ok bool) {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, false
	}
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err != nil {
			continue // the fd ReadDir itself used, closed by now
		}
		if target == path || target == path+"-wal" || target == path+"-shm" {
			n++
		}
	}
	return n, true
}

// A failed OpenStore closes the database it opened. A leaked handle keeps
// the file open: on Windows the caller cannot even delete it (the temp dir
// cleanup of TestOpenStoreRefusesUnconvertibleObservers failed there).
func TestOpenStoreClosesDBOnFailure(t *testing.T) {
	path := legacyStoreDB(t, `ALTER TABLE observers ADD COLUMN operator_note TEXT`)
	// Descriptors are only countable where /proc/self/fd exists (Linux); the
	// removal at the end observes the leak on Windows.
	n, countable := openFDsOn(t, path)
	if countable && n != 0 {
		t.Fatalf("fixture left %d descriptor(s) open on the database", n)
	}
	s, err := OpenStore(path)
	if err == nil {
		s.Close()
		t.Fatal("OpenStore accepted an observers table the rebuild must refuse")
	}
	if n, _ := openFDsOn(t, path); countable && n != 0 {
		t.Fatalf("failed OpenStore left %d descriptor(s) open on the database (%v)", n, err)
	}
	if rerr := os.Remove(path); rerr != nil {
		t.Fatalf("database file cannot be removed after the failed OpenStore, a handle is still open: %v", rerr)
	}
}
