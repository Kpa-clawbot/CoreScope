package dbschema

import (
	"context"
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
	return legacyDBWith(t, legacyObserversDDL, `INSERT INTO observers (id, name) VALUES ('a', 'A'), ('b', 'B')`)
}

// legacyDBWith is legacyFullDB with the given observers table and seed rows.
func legacyDBWith(t *testing.T, observersDDL, seed string) *sql.DB {
	t.Helper()
	db := writerDB(t, filepath.Join(t.TempDir(), "legacy.db"))
	mustExec(t, db,
		`CREATE TABLE transmissions (id INTEGER PRIMARY KEY AUTOINCREMENT, raw_hex TEXT NOT NULL, hash TEXT NOT NULL UNIQUE, first_seen TEXT NOT NULL, route_type INTEGER, payload_type INTEGER, payload_version INTEGER, decoded_json TEXT, created_at TEXT)`,
		`CREATE TABLE observations (id INTEGER PRIMARY KEY AUTOINCREMENT, transmission_id INTEGER NOT NULL REFERENCES transmissions(id), observer_idx INTEGER, direction TEXT, snr REAL, rssi REAL, score INTEGER, path_json TEXT, timestamp INTEGER NOT NULL, raw_hex TEXT)`,
		observersDDL,
		`CREATE INDEX idx_observers_last_seen ON observers(last_seen)`,
		seed,
	)
	// As the ingestor leaves it before dbschema.Apply: the legacy-shape view.
	if err := EnsurePacketsView(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// packetsViewJoin returns the observers column packets_v joins on, read
// from its stored definition.
func packetsViewJoin(t *testing.T, db *sql.DB) string {
	t.Helper()
	var def string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'view' AND name = 'packets_v'`).Scan(&def); err != nil {
		t.Fatalf("read packets_v: %v", err)
	}
	for _, key := range []string{observersKey, legacyObserversKey} {
		if strings.Contains(def, "obs."+key+" = o.observer_idx") {
			return key
		}
	}
	t.Fatalf("packets_v joins on neither key: %s", def)
	return ""
}

// assertPacketsViewReadable fails unless packets_v answers a full read.
func assertPacketsViewReadable(t *testing.T, db *sql.DB) {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM packets_v`).Scan(&n); err != nil {
		t.Errorf("packets_v not readable: %v", err)
	}
}

func assertRefusedUntouched(t *testing.T, db *sql.DB, wantErr string) {
	t.Helper()
	schema := dumpRows(t, db, `SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name`)
	rows := dumpRows(t, db, `SELECT rowid, * FROM observers ORDER BY rowid`)

	err := ensureObserversIdx(db, nil)
	if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("ensureObserversIdx err = %v, want mention of %q", err, wantErr)
	}
	// Review of #2407: a refused rebuild must leave packets_v readable, on
	// the rowid join the legacy table still supports.
	assertPacketsViewReadable(t, db)
	if key := packetsViewJoin(t, db); key != legacyObserversKey {
		t.Errorf("packets_v joins on %s after a refused rebuild, want %s", key, legacyObserversKey)
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
	// The refusal names the way out, since it stops the ingestor on every start.
	if err := ensureObserversIdx(db, nil); err == nil || !strings.Contains(err.Error(), "ALTER TABLE observers DROP COLUMN <name>") ||
		!strings.Contains(err.Error(), "previous CoreScope version") {
		t.Errorf("refusal does not say how to continue: %v", err)
	} else if !strings.Contains(err.Error(), "first drop any index, view or trigger that uses the column") {
		t.Errorf("refusal does not say what blocks DROP COLUMN: %v", err)
	}
	// The statement it gives, with the column it named, runs and clears the way.
	mustExec(t, db, `ALTER TABLE observers DROP COLUMN operator_note`)
	if err := ensureObserversIdx(db, nil); err != nil {
		t.Fatalf("rebuild after the suggested DROP COLUMN: %v", err)
	}
}

// (e) A NULL id (legal under the legacy TEXT PRIMARY KEY) can't satisfy the
// new NOT NULL — refuse instead.
func TestObserversIdxRefusesNullID(t *testing.T) {
	db := legacyFullDB(t)
	mustExec(t, db, `INSERT INTO observers (id, name) VALUES (NULL, 'ghost')`)
	assertRefusedUntouched(t, db, "NULL id")
	if err := ensureObserversIdx(db, nil); err == nil || !strings.Contains(err.Error(), "WHERE id IS NULL") ||
		!strings.Contains(err.Error(), "previous CoreScope version") {
		t.Errorf("refusal does not say how to continue: %v", err)
	}
}

// A child column declared `REFERENCES observers` with no column follows the
// parent's primary key: id before the rebuild, idx after it. The rebuild
// refuses (the child rows would all be orphans) and says what to change;
// with the column named, the next start completes the rebuild (review of
// #2445). The refusal cannot tell that cause from rows that were orphans
// already, so it gives the way out for both.
func TestObserversIdxForeignKeyRefusalSaysHowToContinue(t *testing.T) {
	db := legacyFullDB(t)
	mustExec(t, db,
		`CREATE TABLE observer_notes (observer_id TEXT REFERENCES observers, note TEXT)`,
		`INSERT INTO observer_notes VALUES ('a', 'roof')`,
	)
	assertRefusedUntouched(t, db, "1 foreign key violation(s) in observer_notes")
	err := ensureObserversIdx(db, nil)
	for _, want := range []string{"`REFERENCES observers` with no column", "points at idx", "`REFERENCES observers(id)`",
		"rows that already reference a missing observer", orphanCheckSQL, "delete or fix them", "previous CoreScope version"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %v does not say %q", err, want)
		}
	}
	// Not this cause: before the rebuild the column follows id, and 'a' exists.
	if got := dumpRows(t, db, orphanCheckSQL); len(got) != 0 {
		t.Errorf("%s = %v, want no rows on the untouched table", orphanCheckSQL, got)
	}
	mustExec(t, db,
		`DROP TABLE observer_notes`,
		`CREATE TABLE observer_notes (observer_id TEXT REFERENCES observers(id), note TEXT)`,
		`INSERT INTO observer_notes VALUES ('a', 'roof')`,
	)
	if err := ensureObserversIdx(db, nil); err != nil {
		t.Fatalf("rebuild after the child column was declared REFERENCES observers(id): %v", err)
	}
	if !observersIdxIsPK(t, db) {
		t.Fatal("observers.idx is not the primary key after the retry")
	}
}

// orphanCheckSQL is the statement the foreign key refusal gives for the
// observer_notes table of these tests, the table name quoted.
const orphanCheckSQL = `PRAGMA foreign_key_check("observer_notes")`

// A child column that already names the column (`REFERENCES observers(id)`)
// can still hold a row for an observer that does not exist: the sqlite3 CLI
// writes with foreign_keys off. The rebuild refuses on it at every start, so
// the refusal must not only say to redeclare the column (review of #2445):
// the statement it gives lists the row, and with the row gone the next start
// completes the rebuild.
func TestObserversIdxForeignKeyRefusalCoversOrphanRows(t *testing.T) {
	db := legacyFullDB(t)
	mustExec(t, db,
		`CREATE TABLE observer_notes (observer_id TEXT REFERENCES observers(id), note TEXT)`,
		`INSERT INTO observer_notes VALUES ('a', 'roof')`,
		`PRAGMA foreign_keys=OFF`,
		`INSERT INTO observer_notes VALUES ('gone', 'orphan')`,
		`PRAGMA foreign_keys=ON`,
	)
	assertRefusedUntouched(t, db, "1 foreign key violation(s) in observer_notes")
	err := ensureObserversIdx(db, nil)
	for _, want := range []string{"rows that already reference a missing observer", "list them with `" + orphanCheckSQL + "`",
		"delete or fix them", "previous CoreScope version"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %v does not say %q", err, want)
		}
	}
	// Columns: table, rowid, parent, fkid.
	if got, want := dumpRows(t, db, orphanCheckSQL), []string{"[observer_notes 2 observers 0]"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %v, want %v", orphanCheckSQL, got, want)
	}
	mustExec(t, db, `DELETE FROM observer_notes WHERE rowid = 2`)
	if err := ensureObserversIdx(db, nil); err != nil {
		t.Fatalf("rebuild after the orphan row was deleted: %v", err)
	}
	if !observersIdxIsPK(t, db) {
		t.Fatal("observers.idx is not the primary key after the retry")
	}
	if got, want := dumpRows(t, db, `SELECT observer_id, note FROM observer_notes`), []string{"[a roof]"}; !reflect.DeepEqual(got, want) {
		t.Errorf("observer_notes = %v, want %v", got, want)
	}
}

// A failure after the old table is dropped and observers_new renamed (the
// deepest point of the rebuild) rolls everything back: the legacy table and
// its rows, no observers_new left behind, the readable packets_v and the
// caller's foreign_keys setting. A table squatting on the name of an index
// the rebuild creates makes CREATE INDEX IF NOT EXISTS fail there.
func TestObserversIdxRollsBackMidRebuild(t *testing.T) {
	db := legacyFullDB(t)
	mustExec(t, db, `CREATE TABLE idx_observers_iata_norm (x INTEGER)`)
	assertRefusedUntouched(t, db, "already a table named idx_observers_iata_norm")
	var stale int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'observers_new'`).Scan(&stale); err != nil || stale != 0 {
		t.Errorf("observers_new left behind after the rollback: %d (%v)", stale, err)
	}
	// Once the blocker is gone, the next start completes the rebuild.
	mustExec(t, db, `DROP TABLE idx_observers_iata_norm`)
	if err := ensureObserversIdx(db, nil); err != nil {
		t.Fatalf("rebuild after the blocker was removed: %v", err)
	}
	if !observersIdxIsPK(t, db) {
		t.Fatal("observers.idx is not the primary key after the retry")
	}
}

// packets_v follows the table: the rowid join on a legacy table (a read that
// works), the idx join once the rebuild ran, and EnsurePacketsView after the
// rebuild keeps the idx join.
func TestPacketsViewFollowsObserversShape(t *testing.T) {
	db := legacyFullDB(t)
	if key := packetsViewJoin(t, db); key != legacyObserversKey {
		t.Fatalf("legacy table: packets_v joins on %s, want %s", key, legacyObserversKey)
	}
	assertPacketsViewReadable(t, db)
	if err := ensureObserversIdx(db, nil); err != nil {
		t.Fatalf("ensureObserversIdx: %v", err)
	}
	if key := packetsViewJoin(t, db); key != observersKey {
		t.Fatalf("after the rebuild: packets_v joins on %s, want %s", key, observersKey)
	}
	if err := EnsurePacketsView(db); err != nil {
		t.Fatal(err)
	}
	if key := packetsViewJoin(t, db); key != observersKey {
		t.Fatalf("EnsurePacketsView after the rebuild: joins on %s, want %s", key, observersKey)
	}
	assertPacketsViewReadable(t, db)
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

// A ROLLBACK that fails (here: no transaction is open) leaves the connection
// in an unknown state, so rollbackOrDiscard reports the error and has the
// pool close it. The next connection works and carries the DSN's
// foreign_keys, not the OFF the rebuild had set on the discarded one.
func TestRollbackOrDiscardDropsConnectionOnFailedRollback(t *testing.T) {
	db := writerDB(t, filepath.Join(t.TempDir(), "rollback.db"))
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	discarded, err := rollbackOrDiscard(ctx, conn)
	if !discarded {
		t.Error("rollbackOrDiscard did not report the connection discarded")
	}
	if err == nil || !strings.Contains(err.Error(), "roll back observers rebuild") ||
		!strings.Contains(err.Error(), "cannot rollback - no transaction is active") {
		t.Errorf("rollbackOrDiscard err = %v, want the failed ROLLBACK", err)
	}
	// As ensureObserversIdx's deferred Close: a no-op on a discarded connection.
	_ = conn.Close()
	if n := db.Stats().OpenConnections; n != 0 {
		t.Errorf("%d connection(s) still open after the discard, want 0", n)
	}
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys on the pool's next connection = %d (%v), want 1 as the DSN sets it", fk, err)
	}
	mustExec(t, db, `CREATE TABLE after_discard (x INTEGER)`)
}

// A ROLLBACK that succeeds keeps the connection in the pool.
func TestRollbackOrDiscardKeepsConnectionOnRollback(t *testing.T) {
	db := writerDB(t, filepath.Join(t.TempDir(), "rollback.db"))
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if discarded, err := rollbackOrDiscard(ctx, conn); discarded || err != nil {
		t.Errorf("rollbackOrDiscard = %v, %v; want false, nil", discarded, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if n := db.Stats().OpenConnections; n != 1 {
		t.Errorf("%d open connection(s) after a clean rollback, want the 1 pooled", n)
	}
}
