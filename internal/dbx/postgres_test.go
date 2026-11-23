package dbx_test

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbx"
	"github.com/meshcore-analyzer/dbx/dbxtest"
)

func createTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`CREATE TABLE items (
		id    BIGSERIAL PRIMARY KEY,
		name  TEXT NOT NULL,
		data  BYTEA,
		n     BIGINT,
		f     DOUBLE PRECISION,
		ts    TIMESTAMPTZ,
		note  TEXT
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
}

func countItems(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM items`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestPostgresInsertSelectTypes(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	createTable(t, db)

	blob := []byte{0x00, 0xff, '?', 0x10}
	ts := time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.UTC)

	res, err := db.Exec(`INSERT INTO items (name, data, n, f, ts, note) VALUES (?, ?, ?, ?, ?, ?)`,
		"alpha", blob, int64(1)<<40, 3.25, ts, nil)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("RowsAffected = %d, %v; want 1", n, err)
	}

	var id int64
	err = db.QueryRow(`INSERT INTO items (name, n, note) VALUES (?, ?, ?) RETURNING id`,
		"beta", int64(-7), "note?").Scan(&id)
	if err != nil {
		t.Fatalf("insert returning: %v", err)
	}
	if id != 2 {
		t.Fatalf("RETURNING id = %d, want 2", id)
	}

	// Three-plus args, one of them NULL.
	var (
		name  string
		data  []byte
		n     int64
		f     float64
		gotTS time.Time
		note  sql.NullString
	)
	err = db.QueryRow(`SELECT name, data, n, f, ts, note FROM items
		WHERE n = ? AND f = ? AND ts = ? AND note IS NOT DISTINCT FROM ?`,
		int64(1)<<40, 3.25, ts, nil).Scan(&name, &data, &n, &f, &gotTS, &note)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if name != "alpha" || !bytes.Equal(data, blob) || n != 1<<40 || f != 3.25 || !gotTS.Equal(ts) || note.Valid {
		t.Fatalf("round trip mismatch: name=%q data=%x n=%d f=%v ts=%v note=%v", name, data, n, f, gotTS, note)
	}
}

func TestPostgresQuestionMarkInLiteralRoundTrips(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	createTable(t, db)

	if _, err := db.Exec(`INSERT INTO items (name, note) VALUES ('what? really?', ?)`, "arg?"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var name, note, lit string
	err := db.QueryRow(`SELECT name, note, 'it''s ?' || $$ ? $$ FROM items WHERE note = ? /* ? */ -- ?
		`, "arg?").Scan(&name, &note, &lit)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if name != "what? really?" || note != "arg?" || lit != "it's ? ? " {
		t.Fatalf("got name=%q note=%q lit=%q", name, note, lit)
	}
}

func TestPostgresPreparedStatementReuse(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	createTable(t, db)

	ins, err := db.Prepare(`INSERT INTO items (name, n) VALUES (?, ?)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer ins.Close()
	for i := int64(1); i <= 3; i++ {
		if _, err := ins.Exec("p", i); err != nil {
			t.Fatalf("exec %d: %v", i, err)
		}
	}

	sel, err := db.Prepare(`SELECT n FROM items WHERE name = ? AND n = ?`)
	if err != nil {
		t.Fatalf("prepare select: %v", err)
	}
	defer sel.Close()
	for i := int64(1); i <= 3; i++ {
		var got int64
		if err := sel.QueryRow("p", i).Scan(&got); err != nil || got != i {
			t.Fatalf("select %d: got %d, %v", i, got, err)
		}
	}
}

func TestPostgresTransactionRollback(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	createTable(t, db)

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO items (name) VALUES (?)`, "doomed"); err != nil {
		t.Fatalf("tx insert: %v", err)
	}
	var inTx int
	if err := tx.QueryRow(`SELECT count(*) FROM items WHERE name = ?`, "doomed").Scan(&inTx); err != nil || inTx != 1 {
		t.Fatalf("count inside tx = %d, %v; want 1", inTx, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if n := countItems(t, db); n != 0 {
		t.Fatalf("after rollback count = %d, want 0", n)
	}

	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO items (name) VALUES (?)`, "kept"); err != nil {
		t.Fatalf("tx insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if n := countItems(t, db); n != 1 {
		t.Fatalf("after commit count = %d, want 1", n)
	}
}

// pgx's CheckNamedValue accepts a []int64 as-is; database/sql's default
// conversion would reject it. This fails if the wrapper stops forwarding
// driver.NamedValueChecker.
func TestPostgresNamedValueCheckerForwarded(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	var sum int64
	if err := db.QueryRow(`SELECT sum(x) FROM unnest(?::bigint[]) AS x`, []int64{1, 2, 39}).Scan(&sum); err != nil {
		t.Fatalf("slice arg: %v", err)
	}
	if sum != 42 {
		t.Fatalf("sum = %d, want 42", sum)
	}
}

func TestPostgresPooledConnIsWrapped(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer c.Close()
	err = c.Raw(func(dc any) error {
		// pgx's own conn is not a driver.Validator; ours is.
		v, ok := dc.(driver.Validator)
		if !ok {
			return errors.New("pooled conn is not the dbx wrapper")
		}
		if !v.IsValid() {
			return errors.New("fresh conn reports IsValid() = false")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

// sql.OpenDB with the driver's Connector must rewrite placeholders too.
func TestPostgresOpenDBViaConnector(t *testing.T) {
	dsn := os.Getenv("CORESCOPE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("CORESCOPE_TEST_PG_DSN not set; skipping Postgres test")
	}
	probe, err := sql.Open(dbx.PostgresDriverName, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer probe.Close()
	dc, ok := probe.Driver().(driver.DriverContext)
	if !ok {
		t.Fatalf("driver %T does not implement driver.DriverContext", probe.Driver())
	}
	cn, err := dc.OpenConnector(dsn)
	if err != nil {
		t.Fatalf("OpenConnector: %v", err)
	}
	db := sql.OpenDB(cn)
	defer db.Close()
	var got int64
	if err := db.QueryRow(`SELECT ?::bigint + ?::bigint`, int64(40), int64(2)).Scan(&got); err != nil || got != 42 {
		t.Fatalf("got %d, %v; want 42", got, err)
	}
}

// Rebind's literal lexing assumes standard_conforming_strings=on; a connection
// that starts with it off is refused with an error that names the setting.
func TestPostgresRefusesNonStandardStrings(t *testing.T) {
	dsn := os.Getenv("CORESCOPE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("CORESCOPE_TEST_PG_DSN not set; skipping Postgres test")
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	db, err := sql.Open(dbx.PostgresDriverName, dsn+sep+"standard_conforming_strings=off")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.Ping()
	if err == nil || !strings.Contains(err.Error(), "standard_conforming_strings") {
		t.Fatalf("ping with standard_conforming_strings=off: err = %v, want a refusal naming the setting", err)
	}
}

// A pooled connection whose user turned standard_conforming_strings off is
// discarded before reuse, not handed to the next caller.
func TestPostgresDiscardsConnAfterNonStandardStringsSet(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`SET standard_conforming_strings = off`); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SHOW standard_conforming_strings`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "on" {
		t.Fatalf("reused connection has standard_conforming_strings=%q; it should have been discarded", got)
	}
}
