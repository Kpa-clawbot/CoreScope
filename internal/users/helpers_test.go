package users

import (
	"database/sql"
	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/pgutil/pgtest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// Ordinary tests always exercise a real SQLite database. The explicit PostgreSQL
// matrix requires its service; it never silently substitutes SQLite or skips it.
func testBackend(t *testing.T) dbconfig.Backend {
	t.Helper()
	switch os.Getenv("CORESCOPE_TEST_BACKEND") {
	case "", "sqlite":
		return dbconfig.SQLite
	case "postgres":
		return dbconfig.Postgres
	default:
		t.Fatal("CORESCOPE_TEST_BACKEND must be sqlite or postgres")
		return ""
	}
}
func requirePostgres(t *testing.T) {
	t.Helper()
	if testBackend(t) != dbconfig.Postgres {
		t.Skip("PostgreSQL-specific contract; run CORESCOPE_TEST_BACKEND=postgres")
	}
}
func testTarget(t *testing.T, wholeDatabase bool) string {
	t.Helper()
	if testBackend(t) == dbconfig.SQLite {
		return filepath.Join(t.TempDir(), "accounts.db")
	}
	if wholeDatabase {
		return pgtest.NewDatabase(t)
	}
	return pgtest.NewSchema(t)
}
func schemaVersion(t *testing.T) int {
	if testBackend(t) == dbconfig.SQLite {
		return SQLiteSchemaVersion
	}
	return CurrentSchemaVersion
}
func applyTestSchema(t *testing.T, db *sql.DB) error {
	if testBackend(t) == dbconfig.SQLite {
		return ApplySQLite(db)
	}
	return ApplyPostgres(db)
}

// newTestStore bootstraps an isolated database with a controllable clock.
func newTestStore(t *testing.T) (*Store, *fakeClock) {
	t.Helper()
	return newTestStoreAt(t, testTarget(t, false))
}

func newTestStoreAt(t *testing.T, dsn string) (*Store, *fakeClock) {
	t.Helper()
	if testBackend(t) == dbconfig.Postgres {
		db := testOwner(t, dsn)
		if err := Apply(db); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
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
	if testBackend(t) == dbconfig.SQLite {
		return ownerURL
	}
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
	dsn := testTarget(t, false)
	db := testOwner(t, dsn)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_version(version INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_version VALUES ($1)`, version); err != nil {
		t.Fatal(err)
	}
	history := migrations
	if testBackend(t) == dbconfig.SQLite {
		history = legacyMigrations
	}
	for _, migration := range history[:version] {
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
	if err := applyTestSchema(t, db); err != nil {
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
	var db *sql.DB
	var err error
	if testBackend(t) == dbconfig.SQLite {
		db, err = sql.Open("sqlite", dsn)
	} else {
		db, err = pgutil.Open(dsn, false)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
