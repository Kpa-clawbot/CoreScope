package dbschema

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// observationsDB builds a database with an observations table but deliberately
// no idx_observations_dedup — the shape of every database created before
// cmd/ingestor/db.go started making that index, and of any database whose
// observations table it never ran against.
func observationsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "obs.db")+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// One connection, like cmd/ingestor. An unbounded pool hides any code that
	// queries the pool while holding a transaction: it quietly opens a second
	// connection instead of deadlocking, so the suite passes and production
	// hangs. Every fixture here must match the tightest pool in production.
	db.SetMaxOpenConns(1)
	// The full observations shape, including all five columns the ingestor's
	// UPSERT merges. An earlier fixture omitted score, raw_hex and
	// resolved_path, so removing those three from upsertMergedColumns left the
	// entire suite green — three fifths of the merge was untested.
	if _, err := db.Exec(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		transmission_id INTEGER NOT NULL,
		observer_idx INTEGER,
		direction TEXT,
		snr REAL,
		rssi REAL,
		score INTEGER,
		path_json TEXT,
		timestamp INTEGER,
		raw_hex TEXT,
		resolved_path TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func dedupIndexExists(t *testing.T, db *sql.DB) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_observations_dedup'`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestEnsureObservationsDedupIndexOnCleanTable(t *testing.T) {
	db := observationsDB(t)
	if _, err := db.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (1, 1, '[]', 10), (1, 2, '[]', 10)`,
	); err != nil {
		t.Fatal(err)
	}
	if err := ensureObservationsDedupIndex(db, t.Logf); err != nil {
		t.Fatalf("ensureObservationsDedupIndex: %v", err)
	}
	if !dedupIndexExists(t, db) {
		t.Error("index was not created")
	}
}

// The in-transaction DROP only matters on the success path: on failure ROLLBACK
// removes the temp table regardless. Replacing that DROP with a no-op survived
// every other test here, because none of them looked at the temp schema after a
// repair that worked.
//
// It matters because the connection goes back to the pool. A leftover
// dedup_groups is stale data the next repair would otherwise inherit.
func TestCollapseLeavesNoTempTableAfterSuccess(t *testing.T) {
	db := observationsDB(t) // SetMaxOpenConns(1), so this is the same connection
	if _, err := db.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp) VALUES
		(1, 21, 9, '[]', 10), (2, 21, 9, '[]', 10)`); err != nil {
		t.Fatal(err)
	}
	if err := ensureObservationsDedupIndex(db, t.Logf); err != nil {
		t.Fatalf("ensureObservationsDedupIndex: %v", err)
	}
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM temp.sqlite_master WHERE name LIKE 'dedup_groups%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d temp dedup_groups object(s) left on the connection after a successful repair", n)
	}
}

// The interesting case: duplicates already present, which is what blocked the
// index from being created in the first place. They must be collapsed, and the
// survivor must inherit the non-NULL fields of the rows that went away — the
// same merge the ingestor's ON CONFLICT ... DO UPDATE SET x = COALESCE(...)
// would have performed had the index existed.
func TestEnsureObservationsDedupIndexCollapsesDuplicates(t *testing.T) {
	db := observationsDB(t)
	if _, err := db.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, rssi, path_json, timestamp) VALUES
		(1, 5, 3, 'rx', NULL, -90,  '[]', 100),
		(2, 5, 3, NULL, 7.5,  NULL, '[]', 100),
		(3, 5, 3, NULL, NULL, NULL, NULL, 100),
		(4, 9, 1, 'rx', 1.0,  -80,  '["AA"]', 200)`); err != nil {
		t.Fatal(err)
	}

	if err := ensureObservationsDedupIndex(db, t.Logf); err != nil {
		t.Fatalf("ensureObservationsDedupIndex: %v", err)
	}
	if !dedupIndexExists(t, db) {
		t.Fatal("index was not created after collapsing duplicates")
	}

	// Rows 1 and 2 share the key (5, 3, '[]') and collapse into id 1.
	//
	// Row 3 does NOT join them: its path_json is NULL, and COALESCE(path_json,'')
	// makes that the empty string, a different key from '[]'. That is the
	// ingestor's conflict target verbatim, so an unrecorded path and an
	// explicitly empty path are distinct observations here — worth pinning,
	// because from the outside it looks like it ought to be one group.
	// Row 4 has its own key and is untouched.
	var ids string
	if err := db.QueryRow(`SELECT GROUP_CONCAT(id) FROM (SELECT id FROM observations ORDER BY id)`).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	if ids != "1,3,4" {
		t.Errorf("surviving ids = %q, want \"1,3,4\" (lowest id of each group; NULL path_json is its own group)", ids)
	}

	var direction sql.NullString
	var snr, rssi sql.NullFloat64
	if err := db.QueryRow(`SELECT direction, snr, rssi FROM observations WHERE id = 1`).Scan(&direction, &snr, &rssi); err != nil {
		t.Fatal(err)
	}
	if direction.String != "rx" {
		t.Errorf("direction = %q, want \"rx\" (its own value)", direction.String)
	}
	if snr.Float64 != 7.5 {
		t.Errorf("snr = %v, want 7.5 (merged from the row that was removed)", snr.Float64)
	}
	if rssi.Float64 != -90 {
		t.Errorf("rssi = %v, want -90 (its own value, not overwritten by a NULL)", rssi.Float64)
	}
}

// The merge has to replay the UPSERT, and the UPSERT's
// `COALESCE(excluded.x, x)` means a later non-NULL value REPLACES an earlier
// one. Complementary NULLs (as above) cannot tell "first non-NULL wins" from
// "last non-NULL wins" — both produce the same answer — so this asserts the
// direction explicitly with values that conflict.
func TestEnsureObservationsDedupIndexKeepsLatestValues(t *testing.T) {
	db := observationsDB(t)
	// One group, three rows, every merged column non-NULL and different.
	// path_json is identical so they collide; direction is NOT in the UPSERT's
	// SET list, so the survivor must keep its own.
	// All five merged columns differ across the group, so each one proves the
	// direction independently. direction/timestamp are NOT in the UPSERT's SET
	// list and must keep the survivor's own values.
	if _, err := db.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp, raw_hex, resolved_path) VALUES
		(1, 7, 2, 'first',  1.0, -10, 11, '[]', 100, 'aa', '["a"]'),
		(2, 7, 2, 'second', 7.0, -20, 22, '[]', 200, 'bb', '["b"]'),
		(3, 7, 2, 'third',  9.0, -30, 33, '[]', 300, 'cc', '["c"]')`); err != nil {
		t.Fatal(err)
	}
	if err := ensureObservationsDedupIndex(db, t.Logf); err != nil {
		t.Fatalf("ensureObservationsDedupIndex: %v", err)
	}

	var direction, rawHex, resolvedPath string
	var snr, rssi float64
	var score, timestamp int64
	if err := db.QueryRow(`SELECT direction, snr, rssi, score, raw_hex, resolved_path, timestamp
		FROM observations WHERE id = 1`).Scan(
		&direction, &snr, &rssi, &score, &rawHex, &resolvedPath, &timestamp); err != nil {
		t.Fatal(err)
	}
	// Merged: last non-NULL down the group wins.
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"snr", snr, 9.0},
		{"rssi", rssi, -30.0},
		{"score", score, int64(33)},
		{"raw_hex", rawHex, "cc"},
		{"resolved_path", resolvedPath, `["c"]`},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v (last non-NULL: COALESCE(excluded.x, x) lets later rows win)", c.name, c.got, c.want)
		}
	}
	// Not merged: the UPSERT never SETs these, so the survivor keeps its own.
	if direction != "first" {
		t.Errorf("direction = %q, want \"first\": not in the UPSERT's SET list", direction)
	}
	if timestamp != 100 {
		t.Errorf("timestamp = %d, want 100: not in the UPSERT's SET list", timestamp)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("observations = %d, want 1", n)
	}
}

// The repair must be all-or-nothing. If the index cannot be created, the
// deletions must not survive: rows destroyed with no index to show for it is
// the worst outcome available.
func TestCollapseDuplicatesAndIndexIsAtomic(t *testing.T) {
	db := observationsDB(t)
	// Conflicting readings, so the merge has something to write. Checking only
	// the row count let a version that committed the merge early pass: the rows
	// came back but their values did not.
	if _, err := db.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, snr, rssi, path_json, timestamp) VALUES
		(1, 1, 1, 1.0, -10, '[]', 10),
		(2, 1, 1, 2.0, -20, '[]', 10)`); err != nil {
		t.Fatal(err)
	}
	// Occupy the index name with a TABLE. `CREATE INDEX IF NOT EXISTS` only
	// shrugs when an *index* of that name exists; a table of that name is an
	// error, so the CREATE fails after the merge and delete have already run.
	if _, err := db.Exec(`CREATE TABLE idx_observations_dedup (x INTEGER)`); err != nil {
		t.Fatal(err)
	}

	if _, err := collapseDuplicatesAndIndex(db, t.Logf); err == nil {
		t.Fatal("expected index creation to fail")
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("observations = %d, want 2: a failed index creation must roll the deletions back", n)
	}
	// The merge must roll back too, not just the delete.
	var snr, rssi float64
	if err := db.QueryRow(`SELECT snr, rssi FROM observations WHERE id = 1`).Scan(&snr, &rssi); err != nil {
		t.Fatal(err)
	}
	if snr != 1.0 || rssi != -10 {
		t.Errorf("id=1 snr=%v rssi=%v, want 1/-10: the merge must roll back with the delete", snr, rssi)
	}
	// And nothing may be left in the temp schema for the next user of this
	// connection. Replacing the in-transaction DROP with a no-op used to pass.
	var temps int
	if err := db.QueryRow(`SELECT COUNT(*) FROM temp.sqlite_master WHERE name LIKE 'dedup_groups%'`).Scan(&temps); err != nil {
		t.Fatal(err)
	}
	if temps != 0 {
		t.Errorf("temp.dedup_groups survived a failed repair (%d objects)", temps)
	}
}

// Running twice must be a no-op: Apply runs on every ingestor start.
func TestEnsureObservationsDedupIndexIsIdempotent(t *testing.T) {
	db := observationsDB(t)
	if _, err := db.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (1, 1, '[]', 10), (1, 1, '[]', 10)`); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err := ensureObservationsDedupIndex(db, t.Logf); err != nil {
			t.Fatalf("pass %d: %v", i+1, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("observations = %d, want 1", n)
	}
}

// v2 schemas key observations by observer_id, not observer_idx. The ingestor's
// UPSERT does not apply there, and indexing a missing column would error, so the
// step must skip rather than fail.
func TestEnsureObservationsDedupIndexSkipsV2Schema(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "v2.db")+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := ensureObservationsDedupIndex(db, t.Logf); err != nil {
		t.Fatalf("expected a skip on a v2 schema, got: %v", err)
	}
	if dedupIndexExists(t, db) {
		t.Error("index must not be created on a v2 schema")
	}
}

// Nothing covered the branch that DECIDES to repair. TestCollapseDuplicates...
// calls collapseDuplicatesAndIndex directly, so a broken error check in
// ensureObservationsDedupIndex would leave every one of those tests green while
// production silently skipped the repair and failed later at OpenStore.
//
// This asserts the decision: duplicates present, the real driver's real error,
// and the repair actually taken. It is the test that would catch the driver
// rewording its constraint message.
func TestEnsureObservationsDedupIndexTakesRepairPathOnRealDriverError(t *testing.T) {
	db := observationsDB(t)
	if _, err := db.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp) VALUES
		(1, 4, 4, '[]', 10), (2, 4, 4, '[]', 10)`); err != nil {
		t.Fatal(err)
	}

	// The error the fast path actually gets. If isConstraintViolation stops
	// recognising this, the repair below never runs.
	_, createErr := db.Exec(dedupIndexDDL)
	if createErr == nil {
		t.Fatal("expected CREATE UNIQUE INDEX to fail over duplicates")
	}
	if !isConstraintViolation(createErr) {
		t.Fatalf("isConstraintViolation did not recognise the driver's own error: %v (%T)", createErr, createErr)
	}

	// The above passes just as happily with a strings.Contains check, because
	// the current driver still says "UNIQUE constraint failed". These pin the
	// typed behaviour itself: a constraint error whose text says nothing of the
	// sort must still be recognised, a wrapped one must be unwrapped, and an
	// unrelated error carrying the magic phrase must NOT trigger a repair that
	// deletes rows.
	typed := sqlite3.Error{Code: sqlite3.ErrConstraint, ExtendedCode: sqlite3.ErrConstraintUnique}
	if !isConstraintViolation(typed) {
		t.Error("a bare sqlite3.Error with ErrConstraint was not recognised")
	}
	if !isConstraintViolation(fmt.Errorf("create index: %w", typed)) {
		t.Error("a wrapped constraint error was not recognised; errors.As should unwrap it")
	}
	if isConstraintViolation(errors.New("UNIQUE constraint failed: index 'x'")) {
		t.Error("a plain error carrying the message was treated as a constraint violation — " +
			"that is the string match this check exists to replace")
	}
	if isConstraintViolation(sqlite3.Error{Code: sqlite3.ErrBusy}) {
		t.Error("SQLITE_BUSY was treated as a constraint violation; a locked database must not start deleting rows")
	}

	var repaired bool
	logf := func(format string, args ...interface{}) {
		repaired = true
		t.Logf(format, args...)
	}
	if err := ensureObservationsDedupIndex(db, logf); err != nil {
		t.Fatalf("ensureObservationsDedupIndex: %v", err)
	}
	if !repaired {
		t.Error("repair path was not taken: no log output, so the constraint error was not recognised")
	}
	if !dedupIndexExists(t, db) {
		t.Error("index missing after repair")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("observations = %d, want 1", n)
	}
}

// The audit log has to name what it destroyed, not just count it.
func TestCollapseLogsGroupKeysBeforeDeleting(t *testing.T) {
	db := observationsDB(t)
	if _, err := db.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp) VALUES
		(1, 11, 3, '["AA"]', 10), (2, 11, 3, '["AA"]', 10), (3, 11, 3, '["AA"]', 10)`); err != nil {
		t.Fatal(err)
	}
	var out []string
	logf := func(format string, args ...interface{}) {
		out = append(out, fmt.Sprintf(format, args...))
	}
	if err := ensureObservationsDedupIndex(db, logf); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(out, "\n")
	for _, want := range []string{
		"1 duplicate observation group(s), 2 row(s) to remove",
		"transmission_id=11",
		`path_json="[\"AA\"]"`,
		"keeping id=1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("audit log missing %q; got:\n%s", want, joined)
		}
	}
}

// Content is not the point — order is. The log exists so that a repair which
// destroys the wrong rows leaves a record of what it destroyed, which requires
// the keys to be written BEFORE the delete, not after. Asserting content alone
// passed with the logging moved below the DELETE.
//
// Forcing the repair to fail after the merge proves the ordering: if the keys
// were logged before the failure, they were logged before the delete.
func TestCollapseLogsGroupKeysEvenWhenTheRepairFails(t *testing.T) {
	db := observationsDB(t)
	if _, err := db.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp) VALUES
		(1, 12, 4, '["BB"]', 10), (2, 12, 4, '["BB"]', 10)`); err != nil {
		t.Fatal(err)
	}
	// Fail at the DELETE specifically. Failing later (at CREATE INDEX) does not
	// pin the ordering: logging moved to just after the DELETE would still have
	// run. A BEFORE DELETE trigger that aborts puts the failure exactly at the
	// step the log is supposed to precede.
	if _, err := db.Exec(`CREATE TRIGGER block_delete BEFORE DELETE ON observations
		BEGIN SELECT RAISE(ABORT, 'no deletes'); END`); err != nil {
		t.Fatal(err)
	}

	var out []string
	logf := func(format string, args ...interface{}) { out = append(out, fmt.Sprintf(format, args...)) }
	if _, err := collapseDuplicatesAndIndex(db, logf); err == nil {
		t.Fatal("expected the repair to fail")
	}

	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "transmission_id=12") || !strings.Contains(joined, "keeping id=1") {
		t.Errorf("group keys were not logged before the repair failed, so a failed or wrong "+
			"repair leaves no record of what it touched; got:\n%s", joined)
	}
}

// A pool of one is what cmd/ingestor runs. Anything in the repair that queries
// the pool while holding the transaction waits for a connection the transaction
// itself has checked out, and never gets it: the ingestor hangs at boot, after
// logging that it is repairing, with the database untouched and ingest dead.
//
// Found on staging, not here, because every fixture used an unbounded pool.
// Runs in a goroutine so a regression fails in seconds with a usable message
// rather than hanging until the package timeout.
func TestCollapseDoesNotDeadlockOnSingleConnectionPool(t *testing.T) {
	db := observationsDB(t) // SetMaxOpenConns(1)
	if _, err := db.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp) VALUES
		(1, 3, 1, '[]', 10), (2, 3, 1, '[]', 10)`); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- ensureObservationsDedupIndex(db, func(string, ...interface{}) {}) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ensureObservationsDedupIndex: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("deadlock: the repair is querying the pool while holding its own transaction — " +
			"pass tx, not rw, to anything that reads inside collapseDuplicatesAndIndex")
	}
	if !dedupIndexExists(t, db) {
		t.Error("index missing")
	}
}

// GROUP BY folds NULLs together; a UNIQUE index keeps them apart. Rows with a
// NULL in an indexed column can never violate the index, so the repair must not
// treat them as duplicates at all.
//
// The damage is not the obvious one. Both the DELETE and the merge's correlated
// subquery join on `observer_idx = observer_idx`, and NULL = NULL is not true,
// so the rows are never actually deleted — the *merge* is what destroys data:
// the subquery matches nothing and writes NULL over the survivor's real
// readings. Measured with the guard removed, a row holding snr=4.5 rssi=-70
// came back with both NULL and its row still in place, so nothing looks missing
// while the measurements are gone. Assert the values, not just the row count:
// an earlier version of this test checked survival alone and passed against the
// bug.
//
// On an 11.2M-row instance, 198 of 222 reported groups were observer_idx IS
// NULL — direction='tx' rows.
func TestCollapseLeavesNullKeyedRowsAlone(t *testing.T) {
	db := observationsDB(t)
	if _, err := db.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, rssi, path_json, timestamp) VALUES
		(1, 1, NULL, 'tx', 4.5, -70, '[]', 10),
		(2, 1, NULL, 'tx', 5.5, -60, '[]', 10),
		(3, 2, 7,    'rx', 1.0, -80, '[]', 20),
		(4, 2, 7,    'rx', 2.0, -90, '[]', 20)`); err != nil {
		t.Fatal(err)
	}

	var logged []string
	logf := func(format string, args ...interface{}) { logged = append(logged, fmt.Sprintf(format, args...)) }
	if err := ensureObservationsDedupIndex(db, logf); err != nil {
		t.Fatalf("ensureObservationsDedupIndex: %v", err)
	}

	// Only the observer_idx=7 pair was a real violation: one row removed there,
	// both NULL rows left entirely alone.
	var ids string
	if err := db.QueryRow(`SELECT GROUP_CONCAT(id) FROM (SELECT id FROM observations ORDER BY id)`).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	if ids != "1,2,3" {
		t.Errorf("surviving ids = %q, want \"1,2,3\": NULL observer_idx rows never violate the unique index", ids)
	}

	// The readings on the NULL rows must be exactly as inserted.
	for _, want := range []struct {
		id        int64
		snr, rssi float64
	}{{1, 4.5, -70}, {2, 5.5, -60}} {
		var snr, rssi sql.NullFloat64
		if err := db.QueryRow(`SELECT snr, rssi FROM observations WHERE id = ?`, want.id).Scan(&snr, &rssi); err != nil {
			t.Fatal(err)
		}
		if !snr.Valid || !rssi.Valid {
			t.Errorf("id=%d: snr/rssi wiped to NULL — the merge matched nothing and overwrote real readings", want.id)
			continue
		}
		if snr.Float64 != want.snr || rssi.Float64 != want.rssi {
			t.Errorf("id=%d: snr=%v rssi=%v, want %v/%v", want.id, snr.Float64, rssi.Float64, want.snr, want.rssi)
		}
	}

	if !dedupIndexExists(t, db) {
		t.Error("index missing — proof the NULL rows were never in its way")
	}
	if strings.Contains(strings.Join(logged, "\n"), "observer_idx=0") {
		t.Error("a NULL observer_idx was logged as 0, which hides exactly this class of bug")
	}
}
