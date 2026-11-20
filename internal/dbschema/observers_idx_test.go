package dbschema

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// legacyObserversDDL is the observers table exactly as cmd/ingestor's
// applySchema created it before the explicit idx (implicit rowid only).
const legacyObserversDDL = `CREATE TABLE observers (
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

// writerDB opens a file DB the way the ingestor does: writer DSN
// (foreign_keys on) and a single-connection pool.
func writerDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", WriterDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
}

// observationLinks maps every observation id to the observer id it joins to
// via `obs.<col> = o.observer_idx` ("" when the join finds nothing).
func observationLinks(t *testing.T, db *sql.DB, col string) map[int64]string {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf(`SELECT o.id, COALESCE(obs.id, '')
		FROM observations o LEFT JOIN observers obs ON obs.%s = o.observer_idx`, col))
	if err != nil {
		t.Fatalf("links via %s: %v", col, err)
	}
	defer rows.Close()
	links := map[int64]string{}
	for rows.Next() {
		var id int64
		var obs string
		if err := rows.Scan(&id, &obs); err != nil {
			t.Fatal(err)
		}
		links[id] = obs
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return links
}

func linkedCount(links map[int64]string) int {
	n := 0
	for _, v := range links {
		if v != "" {
			n++
		}
	}
	return n
}

// dumpRows returns every row of query as strings, for before/after equality.
func dumpRows(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []string
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
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

func observersIdxIsPK(t *testing.T, db *sql.DB) bool {
	t.Helper()
	var pk int
	err := db.QueryRow(`SELECT pk FROM pragma_table_info('observers') WHERE name = 'idx'`).Scan(&pk)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return pk == 1
}

// seedGappyLegacy fills minimalDB's legacy observers with rowids 1..8, then
// deletes 3, 5 and the max (8) so the rowid sequence has gaps, and points
// observations at every rowid 1..8 — including the deleted ones (orphans).
func seedGappyLegacy(t *testing.T, db *sql.DB) {
	t.Helper()
	// packets_v selects created_at; minimalDB's transmissions predates it.
	mustExec(t, db, `ALTER TABLE transmissions ADD COLUMN created_at TEXT`,
		`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1, 'aa', 'h1', '2026-01-01T00:00:00Z')`)
	for i := 1; i <= 8; i++ {
		mustExec(t, db, fmt.Sprintf(`INSERT INTO observers (id, name) VALUES ('obs-%d', 'Observer %d')`, i, i))
	}
	mustExec(t, db, `DELETE FROM observers WHERE id IN ('obs-3', 'obs-5', 'obs-8')`)
	for i := 1; i <= 8; i++ {
		var rowid int64
		err := db.QueryRow(`SELECT rowid FROM observers WHERE id = ?`, fmt.Sprintf("obs-%d", i)).Scan(&rowid)
		if err == sql.ErrNoRows {
			rowid = int64(i) // deleted: keep the dangling reference
		} else if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 2; j++ {
			mustExec(t, db, fmt.Sprintf(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (1, %d, '[%d]', %d)`, rowid, j, 1000+i*10+j))
		}
	}
}

// (a) + (g): rowids with gaps survive as idx; every observation joins the
// same observer before and after, and an old-style rowid join still agrees.
func TestObserversIdxPreservesObservationLinks(t *testing.T) {
	db := minimalDB(t)
	defer db.Close()
	seedGappyLegacy(t, db)

	before := observationLinks(t, db, "rowid")
	beforeRows := dumpRows(t, db, `SELECT rowid, id, name FROM observers ORDER BY rowid`)
	if linkedCount(before) != 10 || len(before) != 16 {
		t.Fatalf("seed: want 16 observations / 10 linked, got %d / %d", len(before), linkedCount(before))
	}

	if err := Apply(db, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !observersIdxIsPK(t, db) {
		t.Fatal("observers.idx is not the primary key after Apply")
	}

	after := observationLinks(t, db, "idx")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("links changed:\nbefore(rowid)=%v\nafter(idx)=%v", before, after)
	}
	if got := dumpRows(t, db, `SELECT idx, id, name FROM observers ORDER BY idx`); !reflect.DeepEqual(beforeRows, got) {
		t.Fatalf("observer rows changed:\nbefore=%v\nafter=%v", beforeRows, got)
	}
	// (g) old binaries still say rowid; INTEGER PRIMARY KEY makes it an alias.
	if oldStyle := observationLinks(t, db, "rowid"); !reflect.DeepEqual(after, oldStyle) {
		t.Fatalf("obs.rowid join disagrees with obs.idx join:\nidx=%v\nrowid=%v", after, oldStyle)
	}
	// packets_v was dropped for the swap and must be back, joining on idx.
	viewLinks := map[int64]string{}
	rows, err := db.Query(`SELECT id, COALESCE(observer_id, '') FROM packets_v`)
	if err != nil {
		t.Fatalf("packets_v after rebuild: %v", err)
	}
	for rows.Next() {
		var id int64
		var obs string
		if err := rows.Scan(&id, &obs); err != nil {
			t.Fatal(err)
		}
		viewLinks[id] = obs
	}
	rows.Close()
	if !reflect.DeepEqual(after, viewLinks) {
		t.Fatalf("packets_v links differ:\njoin=%v\nview=%v", after, viewLinks)
	}
	for _, idx := range []string{"idx_observers_last_seen", "idx_observers_iata_norm"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=? AND tbl_name='observers'`, idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s missing after rebuild (n=%d err=%v)", idx, n, err)
		}
	}
	if err := AssertReady(db); err != nil {
		t.Fatalf("AssertReady: %v", err)
	}
}

// (b) A second Apply is a no-op for observers.
func TestObserversIdxIdempotent(t *testing.T) {
	db := minimalDB(t)
	defer db.Close()
	seedGappyLegacy(t, db)
	if err := Apply(db, nil); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	schema := dumpRows(t, db, `SELECT type, name, sql FROM sqlite_master WHERE tbl_name = 'observers' ORDER BY name`)
	rows := dumpRows(t, db, `SELECT * FROM observers ORDER BY idx`)

	var logs []string
	logf := func(f string, a ...interface{}) { logs = append(logs, fmt.Sprintf(f, a...)) }
	if err := Apply(db, logf); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	for _, l := range logs {
		if strings.Contains(l, "rebuilt observers") {
			t.Errorf("second Apply rebuilt observers again: %q", l)
		}
	}
	if got := dumpRows(t, db, `SELECT type, name, sql FROM sqlite_master WHERE tbl_name = 'observers' ORDER BY name`); !reflect.DeepEqual(schema, got) {
		t.Errorf("schema changed on second Apply:\n%v\n%v", schema, got)
	}
	if got := dumpRows(t, db, `SELECT * FROM observers ORDER BY idx`); !reflect.DeepEqual(rows, got) {
		t.Errorf("rows changed on second Apply")
	}
}

// (c) AUTOINCREMENT: new observers never reuse an idx — not a deleted max
// row's, and not one an orphaned observation still references.
func TestObserversIdxNeverReused(t *testing.T) {
	db := minimalDB(t)
	defer db.Close()
	seedGappyLegacy(t, db) // surviving max rowid 7; orphans reference 8
	if err := Apply(db, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	insert := func(id string) int64 {
		res, err := db.Exec(`INSERT INTO observers (id, name) VALUES (?, ?)`, id, id)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := res.LastInsertId()
		return n
	}
	first := insert("new-1")
	if first != 9 {
		t.Fatalf("first new observer idx = %d, want 9 (above surviving max 7 and orphan reference 8)", first)
	}
	mustExec(t, db, `DELETE FROM observers WHERE id = 'new-1'`)
	second := insert("new-2")
	if second <= first {
		t.Fatalf("idx %d reused after deleting max row %d", second, first)
	}
	var orphans int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations o JOIN observers obs ON obs.idx = o.observer_idx WHERE obs.id LIKE 'new-%'`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Fatalf("%d orphaned observations adopted by a new observer", orphans)
	}
}

func legacyFullDB(t *testing.T) *sql.DB {
	t.Helper()
	db := writerDB(t, filepath.Join(t.TempDir(), "legacy.db"))
	mustExec(t, db,
		`CREATE TABLE transmissions (id INTEGER PRIMARY KEY AUTOINCREMENT, raw_hex TEXT NOT NULL, hash TEXT NOT NULL UNIQUE, first_seen TEXT NOT NULL, route_type INTEGER, payload_type INTEGER, payload_version INTEGER, decoded_json TEXT, created_at TEXT)`,
		`CREATE TABLE observations (id INTEGER PRIMARY KEY AUTOINCREMENT, transmission_id INTEGER NOT NULL REFERENCES transmissions(id), observer_idx INTEGER, direction TEXT, snr REAL, rssi REAL, score INTEGER, path_json TEXT, timestamp INTEGER NOT NULL, raw_hex TEXT)`,
		legacyObserversDDL,
		`CREATE INDEX idx_observers_last_seen ON observers(last_seen)`,
		packetsViewSQL,
		`INSERT INTO observers (id, name) VALUES ('a', 'A'), ('b', 'B')`,
	)
	return db
}

func assertRefusedUntouched(t *testing.T, db *sql.DB, wantErr string) {
	t.Helper()
	schema := dumpRows(t, db, `SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name`)
	rows := dumpRows(t, db, `SELECT rowid, * FROM observers ORDER BY rowid`)

	err := ensureObserversIdx(db, nil)
	if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("ensureObserversIdx err = %v, want mention of %q", err, wantErr)
	}
	if got := dumpRows(t, db, `SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name`); !reflect.DeepEqual(schema, got) {
		t.Errorf("schema changed by refused rebuild:\nbefore=%v\nafter=%v", schema, got)
	}
	if got := dumpRows(t, db, `SELECT rowid, * FROM observers ORDER BY rowid`); !reflect.DeepEqual(rows, got) {
		t.Errorf("observer rows changed by refused rebuild")
	}
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys not restored after refused rebuild: %d (%v)", fk, err)
	}
}

// (d) An unknown column would be dropped by the rebuild — refuse instead.
func TestObserversIdxRefusesUnknownColumn(t *testing.T) {
	db := legacyFullDB(t)
	mustExec(t, db, `ALTER TABLE observers ADD COLUMN operator_note TEXT`,
		`UPDATE observers SET operator_note = 'keep me'`)
	assertRefusedUntouched(t, db, "operator_note")
}

// (e) A NULL id (legal under the legacy TEXT PRIMARY KEY) can't satisfy the
// new NOT NULL — refuse instead.
func TestObserversIdxRefusesNullID(t *testing.T) {
	db := legacyFullDB(t)
	mustExec(t, db, `INSERT INTO observers (id, name) VALUES (NULL, 'ghost')`)
	assertRefusedUntouched(t, db, "NULL id")
}

// The full canonical legacy table migrates and keeps foreign_keys on.
func TestObserversIdxFullLegacyTable(t *testing.T) {
	db := legacyFullDB(t)
	mustExec(t, db, `UPDATE observers SET clock_skew_seconds = 42, can_relay = 0, can_relay_seen = 1 WHERE id = 'b'`)
	before := dumpRows(t, db, `SELECT rowid, * FROM observers ORDER BY rowid`)
	if err := ensureObserversIdx(db, nil); err != nil {
		t.Fatalf("ensureObserversIdx: %v", err)
	}
	if got := dumpRows(t, db, `SELECT * FROM observers ORDER BY idx`); !reflect.DeepEqual(before, got) {
		t.Fatalf("rows changed:\nbefore=%v\nafter=%v", before, got)
	}
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys not restored: %d (%v)", fk, err)
	}
}

// (f) Real staging data: a copy of the committed e2e fixture keeps every
// observation linked to the same observer.
func TestObserversIdxPreservesLinksOnE2EFixture(t *testing.T) {
	src := filepath.Join("..", "..", "test-fixtures", "e2e-fixture.db")
	dst := filepath.Join(t.TempDir(), "fixture.db")
	copyFixture(t, src, dst)
	db := writerDB(t, dst)

	before := observationLinks(t, db, "rowid")
	if linkedCount(before) == 0 {
		t.Fatal("fixture has no linked observations — test would prove nothing")
	}
	if err := Apply(db, nil); err != nil {
		t.Fatalf("Apply on fixture: %v", err)
	}
	if !observersIdxIsPK(t, db) {
		t.Fatal("fixture observers.idx is not the primary key after Apply")
	}
	after := observationLinks(t, db, "idx")
	// Apply's dedup-index step (ensureObservationsDedupIndex) may collapse
	// duplicate observations, so compare every observation that survives.
	changed := 0
	for id, obs := range after {
		if prev, ok := before[id]; !ok || prev != obs {
			changed++
		}
	}
	t.Logf("e2e fixture: before (rowid join) %d observations, %d linked; after (idx join) %d observations, %d linked; %d surviving links changed",
		len(before), linkedCount(before), len(after), linkedCount(after), changed)
	if changed != 0 {
		t.Fatalf("%d observation links changed by the rebuild", changed)
	}
	if len(after) == 0 || linkedCount(after) != len(after) {
		t.Fatalf("fixture lost links: %d of %d observations linked after rebuild", linkedCount(after), len(after))
	}
	if err := AssertReady(db); err != nil {
		t.Fatalf("AssertReady: %v", err)
	}
}

func copyFixture(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
