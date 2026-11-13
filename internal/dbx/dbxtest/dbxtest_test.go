package dbxtest

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func currentSchema(t *testing.T, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) string {
	t.Helper()
	var s string
	if err := q.QueryRowContext(context.Background(), `SELECT current_schema()`).Scan(&s); err != nil {
		t.Fatalf("current_schema: %v", err)
	}
	return s
}

func schemaExists(t *testing.T, db *sql.DB, schema string) bool {
	t.Helper()
	var n int
	err := db.QueryRow(`SELECT count(*) FROM information_schema.schemata WHERE schema_name = ?`, schema).Scan(&n)
	if err != nil {
		t.Fatalf("query schemata: %v", err)
	}
	return n == 1
}

func TestOpenPostgresIsolatedSchemas(t *testing.T) {
	a := OpenPostgres(t)
	b := OpenPostgres(t)

	sa, sb := currentSchema(t, a), currentSchema(t, b)
	if sa == sb {
		t.Fatalf("both databases use schema %q", sa)
	}

	for _, db := range []*sql.DB{a, b} {
		if _, err := db.Exec(`CREATE TABLE same_name (v INTEGER)`); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	if _, err := a.Exec(`INSERT INTO same_name (v) VALUES (?), (?)`, 1, 2); err != nil {
		t.Fatalf("insert a: %v", err)
	}
	if _, err := b.Exec(`INSERT INTO same_name (v) VALUES (?)`, 3); err != nil {
		t.Fatalf("insert b: %v", err)
	}
	var na, nb int
	if err := a.QueryRow(`SELECT count(*) FROM same_name`).Scan(&na); err != nil {
		t.Fatal(err)
	}
	if err := b.QueryRow(`SELECT count(*) FROM same_name`).Scan(&nb); err != nil {
		t.Fatal(err)
	}
	if na != 2 || nb != 1 {
		t.Fatalf("counts a=%d b=%d, want 2 and 1", na, nb)
	}
}

// Every connection in the pool, not just the first, must land in the schema.
func TestOpenPostgresEveryPooledConnUsesSchema(t *testing.T) {
	db := OpenPostgres(t)
	want := currentSchema(t, db)

	ctx := context.Background()
	conns := make([]*sql.Conn, 5)
	for i := range conns {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("conn %d: %v", i, err)
		}
		defer c.Close()
		conns[i] = c
	}
	pids := map[int]bool{}
	for i, c := range conns {
		if got := currentSchema(t, c); got != want {
			t.Fatalf("conn %d current_schema = %q, want %q", i, got, want)
		}
		var pid int
		if err := c.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		pids[pid] = true
	}
	if len(pids) != len(conns) {
		t.Fatalf("got %d distinct backends, want %d", len(pids), len(conns))
	}
}

func TestOpenPostgresDropsSchemaOnCleanup(t *testing.T) {
	var schema string
	t.Run("inner", func(t *testing.T) {
		schema = currentSchema(t, OpenPostgres(t))
	})
	observer := OpenPostgres(t)
	if schemaExists(t, observer, schema) {
		t.Fatalf("schema %q still exists after the test's cleanup", schema)
	}
}

func TestWithSearchPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"postgres://u:p@h:5432/db?sslmode=disable", "postgres://u:p@h:5432/db?search_path=s1&sslmode=disable"},
		{"postgresql://h/db", "postgresql://h/db?search_path=s1"},
		{"host=h dbname=db", "host=h dbname=db search_path=s1"},
	}
	for _, tc := range cases {
		got, err := withSearchPath(tc.in, "s1")
		if err != nil || got != tc.want {
			t.Errorf("withSearchPath(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := withSearchPath("postgres://u:secret@h/%zz", "s1"); err == nil {
		t.Error("want error for unparseable URL")
	} else if strings.Contains(err.Error(), "secret") {
		t.Errorf("error leaks DSN: %v", err)
	}
}
