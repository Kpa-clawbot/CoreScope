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
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT observation_id FROM observation_hops WHERE hop IN ('AA','BB') AND timestamp >= 10`)
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
