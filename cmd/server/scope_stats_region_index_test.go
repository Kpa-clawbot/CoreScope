package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// /api/scope-stats took 2.6 s on average on a production instance (17.7 s
// max). Four of its five queries take 10-100 ms there; the per-region count
// took 2.5-2.9 s for every window, even 1h, because SQLite chose
// idx_tx_scope_name (scope_name > '') and walked every scoped transmission in
// the database (45 days of them), filtering by first_seen afterwards. The
// window is the selective condition, so the query must stay on
// idx_transmissions_first_seen.

// scopeStatsFixtureDB builds a database with the real schema (both indexes)
// holding numTx transmissions spread over 45 days, a quarter of them
// transport-scoped with a named region, as on the production instance.
func scopeStatsFixtureDB(tb testing.TB, numTx int) *DB {
	tb.Helper()
	log.SetOutput(io.Discard)
	tb.Cleanup(func() { log.SetOutput(os.Stderr) })
	path := filepath.Join(tb.TempDir(), "scope-stats.db")
	rw, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		tb.Fatal(err)
	}
	// The tables and the two competing indexes as production has them:
	// idx_transmissions_first_seen from the ingestor's base schema
	// (cmd/ingestor/db.go), idx_tx_scope_name (partial) from
	// dbschema.ensureScopeNameColumn.
	for _, ddl := range []string{
		`CREATE TABLE transmissions (id INTEGER PRIMARY KEY AUTOINCREMENT, raw_hex TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE, first_seen TEXT NOT NULL, route_type INTEGER, payload_type INTEGER,
			payload_version INTEGER, decoded_json TEXT, from_pubkey TEXT, scope_name TEXT DEFAULT NULL)`,
		`CREATE INDEX idx_transmissions_first_seen ON transmissions(first_seen)`,
		`CREATE INDEX idx_transmissions_payload_type ON transmissions(payload_type)`,
		`CREATE INDEX idx_transmissions_from_pubkey ON transmissions(from_pubkey)`,
		`CREATE INDEX idx_tx_scope_name ON transmissions(scope_name) WHERE scope_name IS NOT NULL`,
		`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_id TEXT,
			observer_name TEXT, direction TEXT, snr REAL, rssi REAL, score INTEGER, path_json TEXT, timestamp TEXT)`,
		`CREATE TABLE observers (rowid INTEGER PRIMARY KEY, id TEXT, name TEXT, iata TEXT, inactive INTEGER)`,
		`CREATE TABLE nodes (public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL,
			last_seen TEXT, first_seen TEXT, frequency REAL)`,
	} {
		if _, err := rw.Exec(ddl); err != nil {
			tb.Fatalf("fixture schema: %v\n%s", err, ddl)
		}
	}
	regions := []string{"#be", "#be-van", "#be-ant", "#be-gnt", "#be-lge", "#nl", "#nl-ams", "#de"}
	now := time.Now().UTC()
	if _, err := rw.Exec("BEGIN"); err != nil {
		tb.Fatal(err)
	}
	stmt, err := rw.Prepare(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, scope_name)
		VALUES ('aa', ?, ?, ?, 5, ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	// One step, computed once: i * 45 days in nanoseconds would overflow int64.
	step := (45 * 24 * time.Hour) / time.Duration(numTx)
	for i := 0; i < numTx; i++ {
		ts := now.Add(-time.Duration(i) * step).Format(time.RFC3339)
		var scope interface{}
		route := 1 // FLOOD, unscoped by protocol
		if i%4 == 0 {
			route = 0 // TRANSPORT_FLOOD
			scope = regions[i%len(regions)]
		}
		if _, err := stmt.Exec(fmt.Sprintf("h%08d", i), ts, route, scope); err != nil {
			tb.Fatal(err)
		}
	}
	stmt.Close()
	if _, err := rw.Exec("COMMIT"); err != nil {
		tb.Fatal(err)
	}
	if _, err := rw.Exec("ANALYZE"); err != nil { // production runs a bounded ANALYZE (#2072)
		tb.Fatal(err)
	}
	rw.Close()
	db, err := OpenDB(path)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { db.Close() })
	return db
}

func TestScopeStatsByRegion_UsesFirstSeenIndex(t *testing.T) {
	db := scopeStatsFixtureDB(t, 40000)
	since := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	rows, err := db.conn.Query("EXPLAIN QUERY PLAN "+scopeStatsByRegionQuery, since)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	got := strings.Join(plan, " | ")
	if !strings.Contains(got, "idx_transmissions_first_seen") || strings.Contains(got, "idx_tx_scope_name") {
		t.Fatalf("per-region scope count must search the window on idx_transmissions_first_seen, plan: %s", got)
	}
}

func TestScopeStatsByRegion_CountsMatchWindow(t *testing.T) {
	db := scopeStatsFixtureDB(t, 4000)
	resp, err := db.GetScopeStats("24h")
	if err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	var want int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM transmissions WHERE route_type IN (0,3)
		AND scope_name IS NOT NULL AND scope_name != '' AND first_seen >= ?`, since).Scan(&want); err != nil {
		t.Fatal(err)
	}
	got := 0
	for _, rc := range resp.ByRegion {
		got += rc.Count
	}
	if got != want || want == 0 {
		t.Fatalf("byRegion sums to %d, window holds %d named-region transport transmissions", got, want)
	}
}

// BenchmarkGetScopeStats24h: the full /api/scope-stats query set for the 24h
// window on 300k transmissions over 45 days.
func BenchmarkGetScopeStats24h(b *testing.B) {
	db := scopeStatsFixtureDB(b, 300000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := db.GetScopeStats("24h"); err != nil {
			b.Fatal(err)
		}
	}
}
