package users

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbx/dbxtest"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// testPGDSNEnv is the variable dbxtest reads; the Postgres store is opened
// from it too, in the schema dbxtest created for the test.
const testPGDSNEnv = "CORESCOPE_TEST_PG_DSN"

// storeFactory opens a fresh, empty store with a controllable clock; it is
// closed in t.Cleanup.
type storeFactory func(t *testing.T) (*Store, *fakeClock)

// forEachBackend runs body once per backend, as the subtests "sqlite" and
// "postgres". Inside body, newTestStore opens a store of that backend. The
// Postgres run skips without $CORESCOPE_TEST_PG_DSN.
func forEachBackend(t *testing.T, body func(t *testing.T, newTestStore storeFactory)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { body(t, newSQLiteTestStore) })
	t.Run("postgres", func(t *testing.T) { body(t, newPostgresTestStore) })
}

func withTestClock(t *testing.T, st *Store) (*Store, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	st.SetClock(clk.Now)
	t.Cleanup(func() { st.Close() })
	return st, clk
}

// newSQLiteTestStore opens a fresh users.db in a temp dir.
func newSQLiteTestStore(t *testing.T) (*Store, *fakeClock) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return withTestClock(t, st)
}

// newPostgresTestStore opens a store in a fresh schema (created and dropped
// by dbxtest). Skips without $CORESCOPE_TEST_PG_DSN.
func newPostgresTestStore(t *testing.T) (*Store, *fakeClock) {
	t.Helper()
	st, err := openPostgres(os.Getenv(testPGDSNEnv), newPostgresTestSchema(t))
	if err != nil {
		t.Fatalf("openPostgres: %v", err)
	}
	return withTestClock(t, st)
}

// newPostgresTestSchema returns the name of an empty schema that is dropped
// when the test ends (after the stores opened later, cleanups being LIFO).
func newPostgresTestSchema(t *testing.T) string {
	t.Helper()
	db := dbxtest.OpenPostgres(t) // skips without $CORESCOPE_TEST_PG_DSN
	var schema string
	if err := db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	return schema
}
