package dbschema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
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
	for _, want := range []string{`(DROP VIEW "idx_observers_iata_norm")`, "recreate it after the upgrade", "previous CoreScope version"} {
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

// The rebuild takes the write lock before it reads (BEGIN IMMEDIATE), and
// leaves cleanly when another process migrated the table while it waited
// (review of #2445; TestObserversRebuildConcurrentApply reaches both only by
// chance). Handle 1 holds the write lock, handle 2 starts the rebuild and has
// to wait on busy_timeout, handle 1 commits the migrated shape: handle 2
// returns nil without rebuilding, with its foreign_keys setting back, and
// both handles write. A deferred BEGIN would read the legacy table and then
// fail its first write with "database is locked", without waiting.
func TestObserversRebuildWaitsForWriterThenSeesMigrated(t *testing.T) {
	seed := legacyApplyableDB(t)
	var path string
	if err := seed.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	wantRows := dumpRows(t, seed, `SELECT rowid, * FROM observers ORDER BY rowid`)
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	h1, h2 := writerDB(t, path), writerDB(t, path)
	ctx := context.Background()
	// Open handle 2's connection now: opening one applies the writer DSN's
	// pragmas, which wait on the write lock themselves, and the rebuild would
	// then start only after handle 1 is done.
	if err := h2.Ping(); err != nil || h2.Stats().OpenConnections != 1 {
		t.Fatalf("handle 2: %d open connections before the rebuild, want 1 (%v)", h2.Stats().OpenConnections, err)
	}

	// Handle 1 migrates observers by hand inside BEGIN IMMEDIATE, so the test
	// decides when the lock is released.
	conn, err := h1.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	connExec := func(stmts ...string) {
		t.Helper()
		for _, s := range stmts {
			if _, err := conn.ExecContext(ctx, s); err != nil {
				t.Fatalf("handle 1: %q: %v", s, err)
			}
		}
	}
	defs := make([]string, len(observersColumns))
	for i, c := range observersColumns {
		defs[i] = c.ddl
	}
	connExec(`BEGIN IMMEDIATE`)

	done := make(chan error, 1)
	go func() { done <- ensureObserversIdx(h2, nil) }()
	// Handle 2 has to be waiting on the lock when handle 1 commits. Nothing
	// outside SQLite shows a connection sitting in its busy handler, so give
	// it time to get there: its one pooled connection taken, then a pause
	// far longer than the three statements before its BEGIN take. Returning
	// in that time, with or without an error, is the failure. If the pause
	// is ever too short the test still passes (handle 2 just finds the table
	// done at its first look); it cannot fail for that reason.
	early := func(err error) {
		t.Helper()
		t.Fatalf("ensureObserversIdx returned while another connection held the write lock (err = %v); it must wait", err)
	}
	for deadline := time.Now().Add(5 * time.Second); h2.Stats().InUse == 0 && time.Now().Before(deadline); {
		select {
		case err := <-done:
			early(err)
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case err := <-done:
		early(err)
	case <-time.After(100 * time.Millisecond):
	}

	connExec(
		`DROP VIEW packets_v`,
		`CREATE TABLE observers_new (`+strings.Join(defs, ", ")+`)`,
		`INSERT INTO observers_new (idx, id, name) SELECT rowid, id, name FROM observers`,
		`DROP TABLE observers`,
		`ALTER TABLE observers_new RENAME TO observers`,
		`CREATE INDEX idx_observers_last_seen ON observers(last_seen)`,
		packetsViewSQL(observersKey),
	)
	select {
	case err := <-done:
		early(err)
	default:
	}
	connExec(`COMMIT`)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ensureObserversIdx after the other handle migrated the table: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ensureObserversIdx still waiting 10 s after the write lock was released")
	}
	for i, h := range []*sql.DB{h1, h2} {
		if !observersIdxIsPK(t, h) {
			t.Errorf("handle %d: observers.idx is not the primary key", i+1)
		}
	}
	if got := dumpRows(t, h2, `SELECT * FROM observers ORDER BY idx`); !reflect.DeepEqual(wantRows, got) {
		t.Errorf("observer rows:\nwant=%v\ngot=%v", wantRows, got)
	}
	// One connection per pool, so this is the connection the rebuild used.
	var fk, written int
	if err := h2.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("handle 2: foreign_keys = %d (%v), want the writer DSN's 1 restored", fk, err)
	}
	if got := h2.Stats().InUse; got != 0 {
		t.Errorf("handle 2: %d connection(s) still in use", got)
	}
	// Neither connection was left inside a transaction, so both write.
	mustExec(t, h2, `INSERT INTO observers (id, name) VALUES ('h2', 'H2')`)
	mustExec(t, h1, `INSERT INTO observers (id, name) VALUES ('h1', 'H1')`)
	if err := h1.QueryRow(`SELECT COUNT(*) FROM observers WHERE id IN ('h1', 'h2')`).Scan(&written); err != nil || written != 2 {
		t.Errorf("writes after the rebuild: %d of 2 rows visible (%v)", written, err)
	}
	assertPacketsViewReadable(t, h2)
}

// A DELETE trigger on observers whose body references a table that has since
// been dropped is carried across like any other trigger (review of #2445).
// SQLite compiles a table's DELETE triggers for every DELETE on it, even one
// that matches no row, so the rebuild must not issue one: it failed with
// "no such table: main.gone" and left the table unmigrated.
func TestObserversRebuildCarriesBrokenDeleteTrigger(t *testing.T) {
	db := legacyApplyableDB(t)
	mustExec(t, db,
		`CREATE TABLE gone (id TEXT)`,
		`CREATE TRIGGER local_observers_cleanup AFTER DELETE ON observers BEGIN DELETE FROM gone WHERE id = OLD.id; END`,
		`DROP TABLE gone`,
	)
	const trigger = `SELECT name, tbl_name, sql FROM sqlite_master WHERE type = 'trigger'`
	before := dumpRows(t, db, trigger)
	if len(before) != 1 {
		t.Fatalf("fixture: triggers = %v, want the one broken DELETE trigger", before)
	}
	if err := Apply(db, nil); err != nil {
		t.Fatalf("Apply with a broken DELETE trigger on observers: %v", err)
	}
	if _, idxIsPK, err := observersColumnsInfo(context.Background(), db); err != nil || !idxIsPK {
		t.Fatalf("observers.idx is not the rowid alias after Apply (%v)", err)
	}
	if got := dumpRows(t, db, trigger); !reflect.DeepEqual(before, got) {
		t.Errorf("trigger changed by the rebuild:\nbefore=%v\nafter=%v", before, got)
	}
	if got, want := dumpRows(t, db, `SELECT idx, id FROM observers ORDER BY idx`), []string{"[1 a]", "[2 b]"}; !reflect.DeepEqual(got, want) {
		t.Errorf("observers = %v, want %v", got, want)
	}
}

// The DROP the refusal suggests quotes the name the way dropLocalObservers
// does, so it runs as pasted for a name that needs quoting (review of #2445).
func TestRecreateRefusalSuggestsRunnableDrop(t *testing.T) {
	for _, tc := range []struct{ kind, name, create, wantDrop string }{
		{"view", "my view", `CREATE VIEW "my view" AS SELECT id FROM observers`, `DROP VIEW "my view"`},
		{"trigger", `say "hi"`, `CREATE TRIGGER "say ""hi""" AFTER INSERT ON observers BEGIN SELECT 1; END`, `DROP TRIGGER "say ""hi"""`},
		{"index", "select", `CREATE INDEX "select" ON observers(name)`, `DROP INDEX "select"`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			db := legacyFullDB(t)
			mustExec(t, db, tc.create)
			cause := errors.New("boom")
			err := recreateRefusal(localObserver{kind: tc.kind, name: tc.name}, cause)
			if !errors.Is(err, cause) {
				t.Errorf("refusal does not wrap its cause: %v", err)
			}
			if !strings.Contains(err.Error(), "("+tc.wantDrop+")") {
				t.Fatalf("refusal %v does not suggest %s", err, tc.wantDrop)
			}
			mustExec(t, db, tc.wantDrop)
			var left int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?`, tc.kind, tc.name).Scan(&left); err != nil || left != 0 {
				t.Errorf("%s left %d object(s) named %q (%v)", tc.wantDrop, left, tc.name, err)
			}
		})
	}
}

// A view whose name needs quoting is dropped and recreated by the rebuild.
func TestObserversRebuildKeepsViewNeedingQuotes(t *testing.T) {
	db := legacyApplyableDB(t)
	mustExec(t, db, `CREATE VIEW "my view" AS SELECT id, name FROM observers`)
	before := localObjectsSchema(t, db)
	rebuildLogging(t, db)
	if got := localObjectsSchema(t, db); !reflect.DeepEqual(before, got) || len(got) != 1 {
		t.Fatalf("local objects changed by the rebuild:\nbefore=%v\nafter=%v", before, got)
	}
	if got, want := dumpRows(t, db, `SELECT id FROM "my view" ORDER BY id`), []string{"[a]", "[b]"}; !reflect.DeepEqual(got, want) {
		t.Errorf(`"my view" = %v, want %v`, got, want)
	}
}

func TestMentionsIdentifier(t *testing.T) {
	for _, tc := range []struct {
		sql, name string
		want      bool
	}{
		{"SELECT id FROM observers", "observers", true},
		{"observers", "observers", true},
		{"observers WHERE id = 1", "observers", true},  // start of string
		{"SELECT * FROM observers", "observers", true}, // end of string
		{"SELECT * FROM OBSERVERS o", "observers", true},
		{`SELECT * FROM "Observers"`, "observers", true},
		{"SELECT * FROM main.observers", "observers", true},
		{"SELECT * FROM [observers]", "observers", true},
		{"SELECT * FROM `observers`", "observers", true},
		{"SELECT * FROM t,observers;", "observers", true},
		{"SELECT (SELECT COUNT(*) FROM observers)", "observers", true},
		{"SELECT * FROM observers_archive", "observers", false},
		{"SELECT * FROM client_observers", "observers", false},
		{"observers_archive", "observers", false},
		{"client_observers", "observers", false},
		{"SELECT * FROM observers2", "observers", false},
		{"SELECT * FROM 2observers", "observers", false},
		{"SELECT * FROM observers$x", "observers", false},
		{"SELECT * FROM observersé", "observers", false},
		{"SELECT * FROM éobservers", "observers", false},
		{"SELECT * FROM observer", "observers", false},
		{"", "observers", false},
		// A rejected occurrence does not hide a later whole-word one.
		{"SELECT * FROM observers_archive JOIN observers", "observers", true},
		{"SELECT * FROM xobserversobservers JOIN t", "observers", false},
		{"SELECT * FROM packets_v", "packets_v", true},
		{"SELECT * FROM local_packets_v", "packets_v", false},
	} {
		if got := mentionsIdentifier(tc.sql, tc.name); got != tc.want {
			t.Errorf("mentionsIdentifier(%q, %q) = %v, want %v", tc.sql, tc.name, got, tc.want)
		}
	}
}

// A captured view whose name holds a quote character is written with that
// character doubled in the SQL of whatever uses it (`"odd""name"`, or
// "`odd“tick`" in backtick quoting), which is not the name sqlite_master
// stores (review of #2445). Its INSTEAD OF trigger went down with the view
// and was never recreated, and a view selecting from it was left behind to
// fail the RENAME. Both come back with their exact definition.
func TestObserversRebuildKeepsDependentsOfViewWithQuoteInName(t *testing.T) {
	const (
		oddView  = `CREATE VIEW "odd""name" AS SELECT id, name FROM observers`
		trigger  = `CREATE TRIGGER trg_odd INSTEAD OF UPDATE ON "odd""name" BEGIN INSERT INTO audit VALUES (NEW.id, 'update'); END`
		view     = `CREATE VIEW v2 AS SELECT id FROM "odd""name"`
		tickView = "CREATE VIEW `odd``tick` AS SELECT id, name FROM observers"
		tickUser = "CREATE VIEW v2 AS SELECT id FROM `odd``tick`"
	)
	v2Reads := func(t *testing.T, db *sql.DB) {
		if got, want := dumpRows(t, db, `SELECT id FROM v2 ORDER BY id`), []string{"[a]", "[b]"}; !reflect.DeepEqual(got, want) {
			t.Errorf("v2 = %v, want %v", got, want)
		}
	}
	for _, tc := range []struct {
		name    string
		objects []string
		check   func(t *testing.T, db *sql.DB)
	}{
		{"trigger on the view", []string{oddView, trigger}, func(t *testing.T, db *sql.DB) {
			mustExec(t, db, `UPDATE "odd""name" SET name = 'renamed' WHERE id = 'b'`)
			if got, want := dumpRows(t, db, `SELECT id, what FROM audit`), []string{"[b update]"}; !reflect.DeepEqual(got, want) {
				t.Errorf("audit = %v, want %v from trg_odd", got, want)
			}
		}},
		{"view on the view", []string{oddView, view}, v2Reads},
		{"view on the view, backtick quoting", []string{tickView, tickUser}, v2Reads},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := legacyApplyableDB(t)
			mustExec(t, db, `CREATE TABLE audit (id TEXT, what TEXT)`)
			mustExec(t, db, tc.objects...)
			before := localObjectsSchema(t, db)
			if len(before) != len(tc.objects) {
				t.Fatalf("fixture: %d local objects, want %d: %v", len(before), len(tc.objects), before)
			}
			rebuildLogging(t, db)
			if got := localObjectsSchema(t, db); !reflect.DeepEqual(before, got) {
				t.Fatalf("local objects changed by the rebuild:\nbefore=%v\nafter=%v", before, got)
			}
			tc.check(t, db)
		})
	}
}
