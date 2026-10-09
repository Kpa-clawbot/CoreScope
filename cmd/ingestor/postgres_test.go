package main

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/pgutil/pgtest"
)

var testDatabases sync.Map

func openPostgresTestStore(t testing.TB, key string) (*Store, error) {
	return openPostgresTestStoreInterval(t, key, 300)
}
func openPostgresTestStoreInterval(t testing.TB, key string, interval int) (*Store, error) {
	stateDir := filepath.Dir(key)
	if strings.HasPrefix(key, "postgres") {
		stateDir = t.TempDir()
	}
	return OpenStoreWithState(testPostgresURL(t, key), stateDir, interval)
}

// A historical fixture path is only a test key. Runtime OpenStore accepts
// PostgreSQL URLs exclusively, with a real restricted writer credential.
func testPostgresURL(t testing.TB, key string) string {
	t.Helper()
	if strings.HasPrefix(key, "postgres://") || strings.HasPrefix(key, "postgresql://") {
		return key
	}
	cacheKey := t.Name() + "|" + key
	if v, ok := testDatabases.Load(cacheKey); ok {
		return v.(string)
	}
	owner := pgtest.NewSchema(t)
	db, err := pgutil.Open(owner, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = dbschema.Apply(db, nil); err != nil {
		db.Close()
		t.Fatal(err)
	}
	u, _ := url.Parse(owner)
	schema := u.Query().Get("search_path")
	role := schema + "_writer"
	for _, stmt := range []string{`CREATE ROLE ` + role + ` LOGIN PASSWORD '` + role + `'`,
		`GRANT USAGE ON SCHEMA ` + schema + ` TO ` + role,
		`GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA ` + schema + ` TO ` + role,
		`GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA ` + schema + ` TO ` + role,
		`REVOKE INSERT,UPDATE,DELETE ON ` + schema + `.corescope_schema FROM ` + role} {
		if _, err = db.Exec(stmt); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	u.User = url.UserPassword(role, role)
	dsn := u.String()
	testDatabases.Store(cacheKey, dsn)
	t.Cleanup(func() {
		testDatabases.Delete(cacheKey)
		defer db.Close()
		if _, err := db.Exec(`DROP OWNED BY ` + role); err != nil {
			t.Error(err)
		}
		if _, err := db.Exec(`DROP ROLE ` + role); err != nil {
			t.Error(err)
		}
	})
	return dsn
}

func testAdmin(t testing.TB, s *Store) *sql.DB {
	t.Helper()
	var schema string
	if err := s.db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(os.Getenv("CORESCOPE_TEST_POSTGRES_URL"))
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := pgutil.Open(u.String(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestPostgresPacketCommitOrdering(t *testing.T) {
	s, err := OpenStore(testPostgresURL(t, "cursor"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := testAdmin(t, s)
	tx, err := beginWrite(owner)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var earlier int64
	if err = tx.QueryRow(`INSERT INTO transmissions(raw_hex,hash,first_seen) VALUES('AA','delayed','2026-01-01T00:00:00Z') RETURNING id`).Scan(&earlier); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, e := s.InsertTransmission(&PacketData{Hash: "following", RawHex: "BB", Timestamp: "2026-01-01T00:00:01Z"})
		done <- e
	}()
	select {
	case err := <-done:
		t.Fatalf("later ingest passed unfinished transaction: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("writer did not resume")
	}
	var later int64
	if err = s.db.QueryRow(`SELECT id FROM transmissions WHERE hash='following'`).Scan(&later); err != nil {
		t.Fatal(err)
	}
	if later <= earlier {
		t.Fatalf("cursor order %d then %d", earlier, later)
	}
}

func TestPostgresRuntimeCannotMigrate(t *testing.T) {
	s, err := OpenStore(testPostgresURL(t, "roles"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range []string{`CREATE TABLE forbidden(id int)`, `UPDATE corescope_schema SET ready=false`, `DROP TABLE transmissions`} {
		if _, err = s.db.Exec(q); err == nil {
			t.Errorf("runtime role allowed %s", q)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = s.db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresNaiveClockUpsert(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for i, at := range []time.Time{now, now.Add(time.Hour), now.Add(48 * time.Hour)} {
		if err := s.RecordNaiveSkew("clock-observer", -90, at); err != nil {
			t.Fatal(err)
		}
		var count, skew int
		if err := s.db.QueryRow(`SELECT clock_skew_count_24h,clock_skew_seconds FROM observers WHERE id='clock-observer'`).Scan(&count, &skew); err != nil {
			t.Fatal(err)
		}
		want := []int{1, 2, 1}[i]
		if count != want || skew != -90 {
			t.Fatalf("event %d: count=%d skew=%d", i, count, skew)
		}
	}
}
