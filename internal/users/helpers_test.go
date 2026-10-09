package users

import (
	"database/sql"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/pgutil/pgtest"
	"net/url"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// newTestStore bootstraps an isolated PostgreSQL schema with a controllable clock.
func newTestStore(t *testing.T) (*Store, *fakeClock) {
	t.Helper()
	return newTestStoreAt(t, pgtest.NewSchema(t))
}

func newTestStoreAt(t *testing.T, dsn string) (*Store, *fakeClock) {
	t.Helper()
	db, err := pgutil.Open(dsn, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	st, err := Open(testRuntimeURL(t, dsn))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	clk := &fakeClock{t: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	st.SetClock(clk.Now)
	t.Cleanup(func() { st.Close() })
	return st, clk
}

func testRuntimeURL(t *testing.T, ownerURL string) string {
	t.Helper()
	dsn := pgtest.Writer(t, ownerURL)
	u, _ := url.Parse(dsn)
	db, err := pgutil.Open(ownerURL, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`REVOKE INSERT,UPDATE,DELETE ON corescope_schema,schema_version FROM "` + u.User.Username() + `"`); err != nil {
		t.Fatal(err)
	}
	return dsn
}

// migratedTestStore exercises every intermediate native account layout while
// SQLite source normalization is covered by the offline importer's fixtures.
func migratedTestStore(t *testing.T, version int, seed string) *Store {
	t.Helper()
	dsn := pgtest.NewSchema(t)
	db, err := pgutil.Open(dsn, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_version(version INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_version VALUES ($1)`, version); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:version] {
		for _, stmt := range migration {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatal(err)
			}
		}
	}
	if seed != "" {
		if _, err := db.Exec(seed); err != nil {
			t.Fatal(err)
		}
	}
	if err := Apply(db); err != nil {
		t.Fatal(err)
	}
	st, err := Open(testRuntimeURL(t, dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func testOwner(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := pgutil.Open(dsn, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
