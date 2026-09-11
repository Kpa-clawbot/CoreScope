package dbschema

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
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
	if _, err := db.Exec(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		transmission_id INTEGER NOT NULL,
		observer_idx INTEGER,
		direction TEXT,
		snr REAL,
		rssi REAL,
		path_json TEXT,
		timestamp INTEGER
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
	if _, err := db.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, rssi, path_json, timestamp) VALUES
		(1, 7, 2, 'first',  1.0, -10, '[]', 100),
		(2, 7, 2, 'second', 7.0, -20, '[]', 100),
		(3, 7, 2, 'third',  9.0, -30, '[]', 100)`); err != nil {
		t.Fatal(err)
	}
	if err := ensureObservationsDedupIndex(db, t.Logf); err != nil {
		t.Fatalf("ensureObservationsDedupIndex: %v", err)
	}

	var direction string
	var snr, rssi float64
	if err := db.QueryRow(`SELECT direction, snr, rssi FROM observations WHERE id = 1`).Scan(&direction, &snr, &rssi); err != nil {
		t.Fatal(err)
	}
	if snr != 9.0 {
		t.Errorf("snr = %v, want 9 (last non-NULL: COALESCE(excluded.snr, snr) lets later rows win)", snr)
	}
	if rssi != -30 {
		t.Errorf("rssi = %v, want -30 (last non-NULL)", rssi)
	}
	if direction != "first" {
		t.Errorf("direction = %q, want \"first\": the UPSERT never SETs direction, so the survivor keeps its own", direction)
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
	if _, err := db.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp) VALUES
		(1, 1, 1, '[]', 10), (2, 1, 1, '[]', 10)`); err != nil {
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
