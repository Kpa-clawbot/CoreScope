package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbx"
)

// The search_path rules themselves (setsSearchPath, safeSearchPath,
// WithSearchPath) are unit-tested in internal/dbx; these tests pin the
// ingestor's use of them against a real database.

// searchPathTestDB creates a throwaway database (the pinning is about which
// schema the writer's unqualified names land in, so it needs a database whose
// public schema is its own) and returns its DSN, a superuser connection to it
// and the role the DSN connects as. Both are dropped in Cleanup.
func searchPathTestDB(t *testing.T) (string, *sql.DB, string) {
	t.Helper()
	base := os.Getenv("CORESCOPE_TEST_PG_DSN")
	if base == "" {
		t.Skip("CORESCOPE_TEST_PG_DSN not set; skipping Postgres test")
	}
	u, err := url.Parse(base)
	if err != nil || !isPostgresURL(base) {
		t.Fatal("CORESCOPE_TEST_PG_DSN must be a postgres:// URL for this test")
	}
	var b [6]byte
	rand.Read(b[:])
	name := "cs_searchpath_" + hex.EncodeToString(b[:])
	admin, err := sql.Open(dbx.PostgresDriverName, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE DATABASE ` + name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		admin.Close()
	})
	u.Path = "/" + name
	dsn := u.String()
	in, err := sql.Open(dbx.PostgresDriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close() })
	var user string
	if err := in.QueryRow(`SELECT current_user`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	return dsn, in, user
}

// openSearchPathStore opens a store on dsn, writes one transmission through
// it and returns the writer's current_schema().
func openSearchPathStore(t *testing.T, dsn string) string {
	t.Helper()
	s, err := OpenPostgresStore(dsn, filepath.Join(t.TempDir(), "meshcore.db"), 300)
	if err != nil {
		t.Fatalf("OpenPostgresStore: %v", err)
	}
	defer s.Close()
	if _, err := s.InsertTransmission(&PacketData{Hash: "searchpath", RawHex: "1100aa", PayloadType: 4, RouteType: 1, PathJSON: "[]", Timestamp: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	var cur string
	if err := s.db.QueryRow(`SELECT current_schema()`).Scan(&cur); err != nil {
		t.Fatal(err)
	}
	return cur
}

func transmissionsIn(t *testing.T, db *sql.DB, schema string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM "` + schema + `".transmissions`).Scan(&n); err != nil {
		t.Fatalf("count %s.transmissions: %v", schema, err)
	}
	return n
}

// A schema named after the writer role, created by another role, comes first
// in the default search_path ("$user", public). The pinned writer ignores it
// and writes where it always has: public.
func TestOpenPostgresStoreIgnoresPlantedUserSchema(t *testing.T) {
	dsn, in, user := searchPathTestDB(t)
	var b [6]byte
	rand.Read(b[:])
	intruder := "cs_intruder_" + hex.EncodeToString(b[:])
	q := `"` + user + `"`
	for _, stmt := range []string{
		`CREATE ROLE ` + intruder + ` NOLOGIN`,
		`CREATE SCHEMA ` + q + ` AUTHORIZATION ` + intruder,
		`CREATE TABLE ` + q + `.transmissions (id BIGINT, raw_hex TEXT)`,
		`GRANT ALL ON ` + q + `.transmissions TO PUBLIC`,
	} {
		if _, err := in.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	// Registered after the database's cleanup, so it runs first: the role
	// owns objects in that database until it is dropped.
	t.Cleanup(func() {
		in.Exec(`DROP SCHEMA IF EXISTS ` + q + ` CASCADE`)
		in.Exec(`DROP ROLE IF EXISTS ` + intruder)
	})
	var unpinned string
	if err := in.QueryRow(`SELECT current_schema()`).Scan(&unpinned); err != nil || unpinned != user {
		t.Fatalf("setup: an unpinned session's current_schema() = %q (%v); want the planted %q", unpinned, err, user)
	}

	buf := captureLog(t)
	if cur := openSearchPathStore(t, dsn); cur != "public" {
		t.Fatalf("writer current_schema() = %q; want public", cur)
	}
	if n := transmissionsIn(t, in, "public"); n != 1 {
		t.Fatalf("public.transmissions has %d rows; want 1", n)
	}
	if n := transmissionsIn(t, in, user); n != 0 {
		t.Fatalf("the planted %s.transmissions got %d rows", user, n)
	}
	var planted int
	if err := in.QueryRow(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = ?`, user).Scan(&planted); err != nil || planted != 1 {
		t.Fatalf("relations in the planted schema = %d (%v); want only the planted table", planted, err)
	}
	if l := buf.String(); !strings.Contains(l, "is named after the writer role but owned by") || !strings.Contains(l, "writer search_path pinned to public") {
		t.Fatalf("log:\n%s", buf.String())
	}
}

// A schema named after the writer and owned by it is a deliberate setup
// (only the writer or a superuser could have made it): the writer keeps
// using it, as it did before the pin.
func TestOpenPostgresStoreKeepsOwnUserSchema(t *testing.T) {
	dsn, in, user := searchPathTestDB(t)
	if _, err := in.Exec(`CREATE SCHEMA "` + user + `" AUTHORIZATION "` + user + `"`); err != nil {
		t.Fatal(err)
	}
	if cur := openSearchPathStore(t, dsn); cur != user {
		t.Fatalf("writer current_schema() = %q; want its own %q", cur, user)
	}
	if n := transmissionsIn(t, in, user); n != 1 {
		t.Fatalf("%s.transmissions has %d rows; want 1", user, n)
	}
}

// A search_path in the DSN is the operator's choice and is kept, even with a
// planted "$user" schema present.
func TestOpenPostgresStoreKeepsDSNSearchPath(t *testing.T) {
	dsn, in, _ := searchPathTestDB(t)
	if _, err := in.Exec(`CREATE SCHEMA corescope_data`); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(dsn)
	qv := u.Query()
	qv.Set("search_path", "corescope_data")
	u.RawQuery = qv.Encode()
	if cur := openSearchPathStore(t, u.String()); cur != "corescope_data" {
		t.Fatalf("writer current_schema() = %q; want corescope_data", cur)
	}
	if n := transmissionsIn(t, in, "corescope_data"); n != 1 {
		t.Fatalf("corescope_data.transmissions has %d rows; want 1", n)
	}
}
