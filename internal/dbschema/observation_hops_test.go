package dbschema

import (
	"database/sql"
	"reflect"
	"sort"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func hopTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // :memory: is per-connection
	if _, err := db.Exec(`CREATE TABLE observations (id INTEGER PRIMARY KEY AUTOINCREMENT, path_json TEXT, timestamp INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureObservationHopsSchema(db, nil); err != nil {
		t.Fatal(err)
	}
	// Fresh installs seed coverage at "now"; most tests use small timestamps.
	db.Exec(`UPDATE ` + ObservationHopsCoverageTable + ` SET since = 0`)
	return db
}

func hopsFor(t *testing.T, db *sql.DB, id int) []string {
	t.Helper()
	rows, err := db.Query(`SELECT hop FROM observation_hops WHERE observation_id = ? ORDER BY hop`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		rows.Scan(&h)
		out = append(out, h)
	}
	return out
}

func TestObservationHops_TriggersTrackInsertUpdateDelete(t *testing.T) {
	db := hopTestDB(t)
	defer db.Close()
	db.Exec(`INSERT INTO observations (path_json, timestamp) VALUES ('["AA","01FA","AA"]', 100)`) // id 1, dup hop
	if got, want := hopsFor(t, db, 1), []string{"01FA", "AA"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after insert: %v want %v", got, want)
	}
	db.Exec(`UPDATE observations SET path_json = '["BB"]' WHERE id = 1`)
	if got, want := hopsFor(t, db, 1), []string{"BB"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after update: %v want %v", got, want)
	}
	db.Exec(`DELETE FROM observations WHERE id = 1`)
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM observation_hops`).Scan(&n)
	if n != 0 {
		t.Fatalf("delete left %d hop rows", n)
	}
}

// A trigger error would fail the observation INSERT itself, taking ingest down.
func TestObservationHops_BadPathJSONNeverBlocksInsert(t *testing.T) {
	db := hopTestDB(t)
	defer db.Close()
	for _, p := range []interface{}{nil, "", "[]", "not json", `{"a":1}`, `[1,2]`} {
		if _, err := db.Exec(`INSERT INTO observations (path_json, timestamp) VALUES (?, 100)`, p); err != nil {
			t.Fatalf("insert path_json=%v failed: %v", p, err)
		}
	}
	if _, err := db.Exec(`UPDATE observations SET path_json = 'garbage'`); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM observations`); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
}

func TestObservationHops_IdempotentAndCoverageSeededOnce(t *testing.T) {
	db := hopTestDB(t)
	defer db.Close()
	db.Exec(`UPDATE ` + ObservationHopsCoverageTable + ` SET since = 5`)
	if err := EnsureObservationHopsSchema(db, nil); err != nil {
		t.Fatal(err)
	}
	var since int64
	db.QueryRow(`SELECT since FROM ` + ObservationHopsCoverageTable).Scan(&since)
	if since != 5 {
		t.Fatalf("re-running Apply reset coverage to %d", since)
	}
}

// The reach query is a seek on (hop, timestamp): pin that it never degrades to
// a scan of the whole table.
func TestObservationHops_QueryUsesPrimaryKey(t *testing.T) {
	db := hopTestDB(t)
	defer db.Close()
	// Same shape as cmd/server scanReachRowsIndexed: tokens upper-cased in SQL.
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT observation_id FROM observation_hops WHERE hop IN (UPPER(?),UPPER(?)) AND timestamp >= ?`, "aa", "BB", 10)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var a, b, c int
		var d string
		rows.Scan(&a, &b, &c, &d)
		plan = append(plan, d)
	}
	sort.Strings(plan)
	if len(plan) == 0 || !contains(plan[0], "PRIMARY KEY") || contains(plan[0], "SCAN") {
		t.Fatalf("expected PK SEARCH, got %v", plan)
	}
}

// Rows below the coverage mark are deliberately not indexed or maintained, so
// the index stays bounded to its window.
func TestObservationHops_IgnoresRowsBelowCoverageMark(t *testing.T) {
	db := hopTestDB(t)
	defer db.Close()
	db.Exec(`UPDATE ` + ObservationHopsCoverageTable + ` SET since = 1000`)
	db.Exec(`INSERT INTO observations (path_json, timestamp) VALUES ('["AA"]', 500)`)  // id 1, below
	db.Exec(`INSERT INTO observations (path_json, timestamp) VALUES ('["BB"]', 1500)`) // id 2, in window
	if got := hopsFor(t, db, 1); len(got) != 0 {
		t.Fatalf("below-mark row was indexed: %v", got)
	}
	if got, want := hopsFor(t, db, 2), []string{"BB"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("in-window row: %v want %v", got, want)
	}
	db.Exec(`UPDATE observations SET path_json = '["CC"]' WHERE id IN (1,2)`)
	if got := hopsFor(t, db, 1); len(got) != 0 {
		t.Fatalf("update indexed a below-mark row: %v", got)
	}
	if got, want := hopsFor(t, db, 2), []string{"CC"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("update in-window: %v want %v", got, want)
	}
	db.Exec(`DELETE FROM observations`)
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM observation_hops`).Scan(&n)
	if n != 0 {
		t.Fatalf("delete left %d rows", n)
	}
}

func hopObjects(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master
		WHERE name LIKE 'observation_hops%' OR name LIKE 'trg_observation_hops%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		out = append(out, n)
	}
	return out
}

// The index is opt-in: the shared schema (Apply, used by the ingestor and the
// migrate tool) must not install any of it.
func TestApplyDoesNotInstallObservationHops(t *testing.T) {
	db := minimalDB(t)
	defer db.Close()
	if err := Apply(db, nil); err != nil {
		t.Fatal(err)
	}
	if got := hopObjects(t, db); len(got) != 0 {
		t.Fatalf("Apply installed %v", got)
	}
}

func TestDropObservationHopsSchemaRemovesEverything(t *testing.T) {
	db := hopTestDB(t)
	defer db.Close()
	if got := hopObjects(t, db); len(got) != 5 {
		t.Fatalf("setup: %v, want 2 tables + 3 triggers", got)
	}
	removed, err := DropObservationHopsSchema(db)
	if err != nil || !removed {
		t.Fatalf("drop: removed=%v err=%v", removed, err)
	}
	if got := hopObjects(t, db); len(got) != 0 {
		t.Fatalf("left behind: %v", got)
	}
	if _, err := db.Exec(`INSERT INTO observations (path_json, timestamp) VALUES ('["AA"]', 5)`); err != nil {
		t.Fatalf("insert after drop: %v", err)
	}
	removed, err = DropObservationHopsSchema(db)
	if err != nil || removed {
		t.Fatalf("second drop: removed=%v err=%v, want false, nil", removed, err)
	}
}

// A trigger definition changed in a later version must reach an install that
// already has the old one; the tables and coverage mark are kept.
func TestEnsureObservationHopsSchemaRefreshesTriggers(t *testing.T) {
	db := hopTestDB(t)
	defer db.Close()
	db.Exec(`UPDATE ` + ObservationHopsCoverageTable + ` SET since = 7`)
	db.Exec(`DROP TRIGGER trg_observation_hops_ai`)
	if _, err := db.Exec(`CREATE TRIGGER trg_observation_hops_ai AFTER INSERT ON observations
		BEGIN INSERT OR IGNORE INTO observation_hops SELECT value, NEW.timestamp, NEW.id FROM json_each(NEW.path_json); END`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureObservationHopsSchema(db, nil); err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO observations (path_json, timestamp) VALUES ('["ab"]', 10)`)
	if got, want := hopsFor(t, db, 1), []string{"AB"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hops %v, want %v (old trigger still installed?)", got, want)
	}
	var since int64
	db.QueryRow(`SELECT since FROM ` + ObservationHopsCoverageTable).Scan(&since)
	if since != 7 {
		t.Fatalf("coverage mark reset to %d, want 7 kept", since)
	}
}

// The backfill and trim chunk functions add and remove exactly the rows the
// triggers would, upper-cased, and only inside [lo, hi).
func TestIndexAndUnindexObservationHops(t *testing.T) {
	db := hopTestDB(t)
	defer db.Close()
	removed, _ := DropObservationHopsSchema(db) // insert with no triggers, as before an upgrade
	if !removed {
		t.Fatal("setup: drop")
	}
	for _, r := range []struct {
		p  string
		ts int
	}{{`["aa","BB","aa"]`, 10}, {`["CC",5,["DD"]]`, 20}, {`bad`, 20}, {`["EE"]`, 30}} {
		db.Exec(`INSERT INTO observations (path_json, timestamp) VALUES (?, ?)`, r.p, r.ts)
	}
	if err := EnsureObservationHopsSchema(db, nil); err != nil {
		t.Fatal(err)
	}
	n, err := IndexObservationHops(db, 10, 30)
	if err != nil || n != 3 {
		t.Fatalf("index [10,30): %d rows, err %v; want 3 (AA, BB, CC)", n, err)
	}
	if got, want := hopsFor(t, db, 1), []string{"AA", "BB"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("obs 1 hops %v, want %v", got, want)
	}
	if got := hopsFor(t, db, 4); len(got) != 0 {
		t.Fatalf("obs 4 at hi bound indexed: %v", got)
	}
	n, err = UnindexObservationHops(db, 10, 20)
	if err != nil || n != 2 {
		t.Fatalf("unindex [10,20): %d rows, err %v; want 2", n, err)
	}
	if got, want := hopsFor(t, db, 2), []string{"CC"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("obs 2 hops after unindex %v, want %v", got, want)
	}
}
