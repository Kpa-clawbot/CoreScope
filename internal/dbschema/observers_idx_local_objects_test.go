package dbschema

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// Objects an operator added to a legacy database that the observers rebuild
// must carry across (#2443): an index and a trigger on observers (DROP TABLE
// takes both with it), a trigger on another table that writes observers, a
// view on observers, a view on that view (with an INSTEAD OF trigger), and a
// view on packets_v. Each view and the outside trigger make the RENAME fail
// if left in place ("error in view …: no such table: main.observers").
var localObserverObjects = []string{
	`CREATE TABLE observer_audit (id TEXT, what TEXT)`,
	`CREATE INDEX local_observers_name ON observers(name)`,
	`CREATE TRIGGER local_observers_audit AFTER INSERT ON observers BEGIN INSERT INTO observer_audit VALUES (NEW.id, 'insert'); END`,
	`CREATE TRIGGER local_observations_count AFTER INSERT ON observations BEGIN UPDATE observers SET packet_count = packet_count + 1 WHERE rowid = NEW.observer_idx; END`,
	`CREATE VIEW local_observer_names AS SELECT id, name FROM observers`,
	`CREATE VIEW local_observer_names_upper AS SELECT id, UPPER(name) AS name FROM local_observer_names`,
	`CREATE TRIGGER local_observer_names_rename INSTEAD OF UPDATE ON local_observer_names_upper BEGIN INSERT INTO observer_audit VALUES (NEW.id, 'rename'); END`,
	`CREATE VIEW local_packets AS SELECT id, observer_id FROM packets_v`,
}

// localObjectsSchema is every non-table schema object that is not one the
// rebuild owns, in creation order.
func localObjectsSchema(t *testing.T, db *sql.DB) []string {
	t.Helper()
	return dumpRows(t, db, `SELECT type, name, tbl_name, sql FROM sqlite_master
		WHERE type IN ('index', 'trigger', 'view') AND sql IS NOT NULL
		  AND name NOT IN ('packets_v', 'idx_observers_last_seen', 'idx_observers_iata_norm')
		ORDER BY rowid`)
}

// legacyApplyableDB is legacyFullDB plus the other tables a full Apply
// migrates.
func legacyApplyableDB(t *testing.T) *sql.DB {
	t.Helper()
	db := legacyFullDB(t)
	mustExec(t, db,
		`CREATE TABLE nodes (public_key TEXT PRIMARY KEY, name TEXT)`,
		`CREATE TABLE inactive_nodes (public_key TEXT PRIMARY KEY, name TEXT, last_seen TEXT)`,
	)
	return db
}

func legacyDBWithLocalObjects(t *testing.T) *sql.DB {
	t.Helper()
	db := legacyApplyableDB(t)
	mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1, 'aa', 'h1', '2026-01-01T00:00:00Z')`)
	mustExec(t, db, localObserverObjects...)
	return db
}

func rebuildLogging(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var logs []string
	logf := func(f string, a ...interface{}) { logs = append(logs, fmt.Sprintf(f, a...)) }
	if err := ensureObserversIdx(db, logf); err != nil {
		t.Fatalf("ensureObserversIdx: %v", err)
	}
	if !observersIdxIsPK(t, db) {
		t.Fatal("observers.idx is not the primary key after the rebuild")
	}
	return logs
}

// Every local object comes back with its exact definition, in its original
// order, and the rebuild says which it recreated.
func TestObserversRebuildKeepsLocalObjects(t *testing.T) {
	db := legacyDBWithLocalObjects(t)
	before := localObjectsSchema(t, db)
	if len(before) != len(localObserverObjects)-1 {
		t.Fatalf("fixture: %d local objects, want %d: %v", len(before), len(localObserverObjects)-1, before)
	}
	logs := rebuildLogging(t, db)
	if got := localObjectsSchema(t, db); !reflect.DeepEqual(before, got) {
		t.Fatalf("local objects changed by the rebuild:\nbefore=%v\nafter=%v", before, got)
	}
	var line string
	for _, l := range logs {
		if strings.Contains(l, "recreated") {
			line = l
		}
	}
	for _, want := range []string{
		"index local_observers_name", "trigger local_observers_audit", "trigger local_observations_count",
		"view local_observer_names", "view local_observer_names_upper", "trigger local_observer_names_rename",
		"view local_packets",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("recreate log %q does not list %s (logs: %v)", line, want, logs)
		}
	}
}

// The recreated triggers fire, on observers and on another table.
func TestObserversRebuildLocalTriggersFire(t *testing.T) {
	db := legacyDBWithLocalObjects(t)
	rebuildLogging(t, db)
	mustExec(t, db,
		`INSERT INTO observers (id, name) VALUES ('z', 'Z')`,
		`INSERT INTO observations (transmission_id, observer_idx, timestamp) VALUES (1, (SELECT idx FROM observers WHERE id = 'a'), 1)`,
		`UPDATE local_observer_names_upper SET name = 'renamed' WHERE id = 'b'`,
	)
	if got, want := dumpRows(t, db, `SELECT id, what FROM observer_audit ORDER BY rowid`), []string{"[z insert]", "[b rename]"}; !reflect.DeepEqual(got, want) {
		t.Errorf("observer_audit = %v, want %v", got, want)
	}
	var n int64
	if err := db.QueryRow(`SELECT packet_count FROM observers WHERE id = 'a'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("packet_count of a = %d (%v), want 1 from local_observations_count", n, err)
	}
}

// The recreated views answer, including the one on a view and the one on
// packets_v, with the same rows as before.
func TestObserversRebuildLocalViewsQueryable(t *testing.T) {
	db := legacyDBWithLocalObjects(t)
	mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, timestamp) VALUES (1, 1, 1)`)
	queries := []string{
		`SELECT * FROM local_observer_names ORDER BY id`,
		`SELECT * FROM local_observer_names_upper ORDER BY id`,
		`SELECT * FROM local_packets ORDER BY id`,
	}
	var before [][]string
	for _, q := range queries {
		before = append(before, dumpRows(t, db, q))
	}
	rebuildLogging(t, db)
	for i, q := range queries {
		if got := dumpRows(t, db, q); !reflect.DeepEqual(before[i], got) || len(got) == 0 {
			t.Errorf("%s:\nbefore=%v\nafter=%v", q, before[i], got)
		}
	}
}

// A local object that cannot be recreated (here a view squatting on the name
// of an index the rebuild creates) rolls the whole rebuild back, and the
// error names the object and the way out.
func TestObserversRebuildRefusesUnrecreatableLocalObject(t *testing.T) {
	db := legacyDBWithLocalObjects(t)
	mustExec(t, db, `CREATE VIEW idx_observers_iata_norm AS SELECT id FROM observers`)
	before := localObjectsSchema(t, db)
	assertRefusedUntouched(t, db, "could not recreate view idx_observers_iata_norm")
	err := ensureObserversIdx(db, nil)
	for _, want := range []string{"DROP VIEW idx_observers_iata_norm", "recreate it after the upgrade", "previous CoreScope version"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %v does not say %q", err, want)
		}
	}
	if got := localObjectsSchema(t, db); !reflect.DeepEqual(before, got) {
		t.Errorf("local objects changed by the refused rebuild:\nbefore=%v\nafter=%v", before, got)
	}
}

// A second Apply after the rebuild leaves the local objects alone.
func TestObserversRebuildLocalObjectsIdempotent(t *testing.T) {
	db := legacyDBWithLocalObjects(t)
	if err := Apply(db, nil); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	schema := dumpRows(t, db, `SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY rowid`)
	var logs []string
	logf := func(f string, a ...interface{}) { logs = append(logs, fmt.Sprintf(f, a...)) }
	if err := Apply(db, logf); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	for _, l := range logs {
		if strings.Contains(l, "observers") && (strings.Contains(l, "rebuilt") || strings.Contains(l, "recreated")) {
			t.Errorf("second Apply touched observers: %q", l)
		}
	}
	if got := dumpRows(t, db, `SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY rowid`); !reflect.DeepEqual(schema, got) {
		t.Errorf("schema changed on second Apply:\nbefore=%v\nafter=%v", schema, got)
	}
}

// Apply racing from several goroutines over two handles on one file (the
// writer DSN: WAL, busy_timeout) rebuilds exactly once: every call succeeds,
// every local object exists once with its definition, every row survives.
func TestObserversRebuildConcurrentApply(t *testing.T) {
	seed := legacyApplyableDB(t)
	// Everything else already migrated, so only the rebuild is in play.
	if err := Apply(seed, nil); err != nil {
		t.Fatal(err)
	}
	mustExec(t, seed,
		`DROP VIEW packets_v`,
		`DROP TABLE observers`,
		`DELETE FROM sqlite_sequence WHERE name = 'observers'`,
		legacyObserversDDL,
		`CREATE INDEX idx_observers_last_seen ON observers(last_seen)`,
		`INSERT INTO observers (id, name) VALUES ('a', 'A'), ('b', 'B'), ('c', 'C')`,
		`DELETE FROM observers WHERE id = 'b'`,
	)
	if err := EnsurePacketsView(seed); err != nil {
		t.Fatal(err)
	}
	mustExec(t, seed, localObserverObjects...)
	var path string
	if err := seed.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	wantLocal := localObjectsSchema(t, seed)
	wantRows := dumpRows(t, seed, `SELECT rowid, * FROM observers ORDER BY rowid`)
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	handles := []*sql.DB{writerDB(t, path), writerDB(t, path)}
	const perHandle = 4
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, len(handles)*perHandle)
	for _, h := range handles {
		for i := 0; i < perHandle; i++ {
			wg.Add(1)
			go func(h *sql.DB) {
				defer wg.Done()
				<-start
				errs <- Apply(h, nil)
			}(h)
		}
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Apply: %v", err)
		}
	}

	db := handles[0]
	if !observersIdxIsPK(t, db) {
		t.Fatal("observers.idx is not the primary key after concurrent Apply")
	}
	if got := localObjectsSchema(t, db); !reflect.DeepEqual(wantLocal, got) {
		t.Errorf("local objects after concurrent Apply:\nwant=%v\ngot=%v", wantLocal, got)
	}
	if got := dumpRows(t, db, `SELECT * FROM observers ORDER BY idx`); !reflect.DeepEqual(wantRows, got) {
		t.Errorf("observer rows after concurrent Apply:\nwant=%v\ngot=%v", wantRows, got)
	}
	var stale int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'observers_new'`).Scan(&stale); err != nil || stale != 0 {
		t.Errorf("observers_new left behind: %d (%v)", stale, err)
	}
	assertPacketsViewReadable(t, db)
}
