package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbschema/pgschema"
	"github.com/meshcore-analyzer/dbx"
)

// pgSearchPathRandom returns a random lowercase hex suffix for role and
// database names.
func pgSearchPathRandom(t *testing.T) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// pgReaderSearchPathDB sets up what the "$user" hazard needs and dbxtest's
// per-test schemas cannot give: a throwaway database whose CoreScope tables
// are in public (applied by the test superuser, with one transmission), and
// a read-only LOGIN role on it with no search_path in its DSN. It returns a
// superuser handle on the database, the reader's name, and the reader's
// URL DSN. The database and role are dropped in Cleanup.
func pgReaderSearchPathDB(t *testing.T) (in *sql.DB, reader, dsn string) {
	t.Helper()
	base := os.Getenv("CORESCOPE_TEST_PG_DSN")
	if base == "" {
		t.Skip("CORESCOPE_TEST_PG_DSN not set; skipping Postgres test")
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal("unparseable test DSN")
	}
	admin, err := sql.Open(dbx.PostgresDriverName, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })

	suffix := pgSearchPathRandom(t)
	reader = "cs_reader_" + suffix
	password := "pw" + pgSearchPathRandom(t)
	if _, err := admin.Exec(`CREATE ROLE ` + reader + ` LOGIN PASSWORD '` + password + `'`); err != nil {
		t.Fatalf("create role: %v", err)
	}
	// Registered before the database's cleanup, so it runs after it: the
	// role holds grants in that database until it is dropped.
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP ROLE IF EXISTS ` + reader); err != nil {
			t.Errorf("drop role %s: %v", reader, err)
		}
	})
	name := "cs_srv_searchpath_" + suffix
	if _, err := admin.Exec(`CREATE DATABASE ` + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})

	u.Path = "/" + name
	in, err = sql.Open(dbx.PostgresDriverName, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close() })
	var schema string
	if err := in.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil || schema != "public" {
		t.Fatalf("setup: superuser current_schema() = %q (%v); want public", schema, err)
	}
	if err := pgschema.Apply(context.Background(), in, t.Logf); err != nil {
		t.Fatalf("pgschema.Apply: %v", err)
	}
	for _, stmt := range []string{
		// Before Postgres 15 PUBLIC may create in public, which the
		// privilege check rightly refuses; this test is not about that.
		`REVOKE CREATE ON SCHEMA public FROM PUBLIC`,
		`GRANT USAGE ON SCHEMA public TO ` + reader,
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO ` + reader,
		`INSERT INTO transmissions (raw_hex, hash, first_seen) VALUES ('1100aa', 'real', '2026-01-01T00:00:00Z')`,
	} {
		if _, err := in.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	u.User = url.UserPassword(reader, password)
	q := u.Query()
	q.Del("search_path")
	u.RawQuery = q.Encode()
	return in, reader, u.String()
}

// pgPlantUserSchema creates, as another role, a schema named after reader
// holding a transmissions table shaped like the real one with three fake
// rows, readable by reader: the "$user" hazard, set up as an attacker able
// to CREATE in the database would.
func pgPlantUserSchema(t *testing.T, in *sql.DB, reader string) {
	t.Helper()
	intruder := "cs_intruder_" + pgSearchPathRandom(t)
	if _, err := in.Exec(`CREATE ROLE ` + intruder + ` NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	// Registered after the database's cleanup, so it runs first: the role
	// owns objects in that database until they are dropped.
	t.Cleanup(func() {
		in.Exec(`DROP SCHEMA IF EXISTS "` + reader + `" CASCADE`)
		in.Exec(`DROP ROLE IF EXISTS ` + intruder)
	})
	q := `"` + reader + `"`
	for _, stmt := range []string{
		`CREATE SCHEMA ` + q + ` AUTHORIZATION ` + intruder,
		`CREATE TABLE ` + q + `.transmissions (LIKE public.transmissions)`,
		`INSERT INTO ` + q + `.transmissions SELECT * FROM public.transmissions`,
		`INSERT INTO ` + q + `.transmissions SELECT * FROM public.transmissions`,
		`INSERT INTO ` + q + `.transmissions SELECT * FROM public.transmissions`,
		`ALTER TABLE ` + q + `.transmissions OWNER TO ` + intruder,
		`GRANT USAGE ON SCHEMA ` + q + ` TO ` + reader,
		`GRANT SELECT ON ` + q + `.transmissions TO ` + reader,
	} {
		if _, err := in.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// pgKeywordDSN is a URL DSN rewritten in keyword/value form.
func pgKeywordDSN(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("unparseable DSN")
	}
	pw, _ := u.User.Password()
	kv := fmt.Sprintf("host=%s user=%s password='%s' dbname=%s", u.Hostname(), u.User.Username(), pw, strings.TrimPrefix(u.Path, "/"))
	if p := u.Port(); p != "" {
		kv += " port=" + p
	}
	if m := u.Query().Get("sslmode"); m != "" {
		kv += " sslmode=" + m
	}
	return kv
}

// pgCaptureLog sends the standard logger to a buffer until the test ends.
func pgCaptureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// pgReadTransmissions returns the server pool's current_schema() and how
// many transmissions it sees.
func pgReadTransmissions(t *testing.T, d *DB) (string, int) {
	t.Helper()
	var schema string
	var n int
	if err := d.conn.QueryRow(`SELECT current_schema(), (SELECT count(*) FROM transmissions)`).Scan(&schema, &n); err != nil {
		t.Fatal(err)
	}
	return schema, n
}

// A schema named after the server's role, owned by another role, comes first
// in the default search_path ("$user", public) and holds a fake
// transmissions table. The pinned server reads the real tables in public, in
// both DSN forms, and says what it left out.
func TestOpenPostgresDBIgnoresPlantedUserSchema(t *testing.T) {
	in, reader, dsn := pgReaderSearchPathDB(t)
	pgPlantUserSchema(t, in, reader)

	// The hazard is real: an unpinned session as the reader resolves
	// transmissions to the planted table.
	unpinned, err := sql.Open(dbx.PostgresDriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var fake int
	err = unpinned.QueryRow(`SELECT count(*) FROM transmissions`).Scan(&fake)
	unpinned.Close()
	if err != nil || fake != 3 {
		t.Fatalf("setup: an unpinned reader session sees %d transmissions (%v); want the 3 planted", fake, err)
	}

	for form, dsn := range map[string]string{"url": dsn, "keyword": pgKeywordDSN(t, dsn)} {
		t.Run(form, func(t *testing.T) {
			buf := pgCaptureLog(t)
			d, err := OpenPostgresDB(dsn)
			if err != nil {
				t.Fatalf("OpenPostgresDB: %v", err)
			}
			defer d.Close()
			if schema, n := pgReadTransmissions(t, d); schema != "public" || n != 1 {
				t.Fatalf("server reads %d transmissions in %q; want the 1 real one in public", n, schema)
			}
			// Every pooled connection is pinned, not just the first.
			ctx := context.Background()
			c1, err := d.conn.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer c1.Close()
			c2, err := d.conn.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer c2.Close()
			for i, c := range []*sql.Conn{c1, c2} {
				var n int
				if err := c.QueryRowContext(ctx, `SELECT count(*) FROM transmissions`).Scan(&n); err != nil || n != 1 {
					t.Errorf("connection %d sees %d transmissions (%v); want 1", i, n, err)
				}
			}
			l := buf.String()
			for _, want := range []string{
				fmt.Sprintf("schema %q is named after the reader role but owned by", reader),
				"[db] reader search_path pinned to public",
			} {
				if !strings.Contains(l, want) {
					t.Errorf("log lacks %q:\n%s", want, l)
				}
			}
		})
	}
}

// A schema named after the reader and owned by it stays on the pinned path,
// and the privilege check refuses the reader for owning a schema it searches
// with its own explanation: the pin adds no error of its own on top.
func TestOpenPostgresDBOwnUserSchemaRefusedByPrivilegeCheck(t *testing.T) {
	in, reader, dsn := pgReaderSearchPathDB(t)
	if _, err := in.Exec(`CREATE SCHEMA "` + reader + `" AUTHORIZATION ` + reader); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Exec(`DROP SCHEMA IF EXISTS "` + reader + `" CASCADE`) })

	buf := pgCaptureLog(t)
	d, err := OpenPostgresDB(dsn)
	if err == nil {
		d.Close()
		t.Fatal("OpenPostgresDB accepted a reader that owns the schema named after it")
	}
	msg := err.Error()
	for _, want := range []string{"could gain write access", "role " + reader + " owns schema " + reader} {
		if !strings.Contains(msg, want) {
			t.Errorf("error lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "search_path pinned") || strings.Contains(msg, dsn) {
		t.Errorf("error is not the privilege check's alone:\n%s", msg)
	}
	if want := fmt.Sprintf(`[db] reader search_path pinned to "%s", public`, reader); !strings.Contains(buf.String(), want) {
		t.Errorf("log lacks %q:\n%s", want, buf.String())
	}
}

// A search_path in the DSN is the operator's choice and is kept as given,
// with a planted "$user" schema present or not: no probe, no pin.
func TestOpenPostgresDBKeepsDSNSearchPath(t *testing.T) {
	in, reader, dsn := pgReaderSearchPathDB(t)
	pgPlantUserSchema(t, in, reader)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	for form, raw := range map[string]string{
		"param":   "search_path=public",
		"options": "options=-c%20search_path%3Dpublic",
	} {
		t.Run(form, func(t *testing.T) {
			v := *u
			v.RawQuery = raw
			buf := pgCaptureLog(t)
			d, err := OpenPostgresDB(v.String())
			if err != nil {
				t.Fatalf("OpenPostgresDB: %v", err)
			}
			defer d.Close()
			if schema, n := pgReadTransmissions(t, d); schema != "public" || n != 1 {
				t.Fatalf("server reads %d transmissions in %q; want 1 in public", n, schema)
			}
			if strings.Contains(buf.String(), "search_path pinned") {
				t.Errorf("a DSN search_path was pinned over:\n%s", buf.String())
			}
		})
	}
}

// A URL DSN's query values reach the server pools as pgx reads them: an
// options value with spaces survives pgServerDSN and the pin.
func TestPGServerDSNKeepsQueryBytes(t *testing.T) {
	in := "postgres://u:p@h/db?application_name=a+b&options=-c%20work_mem%3D64MB&sslmode=disable"
	out, _, err := pgServerDSN(in, testPGParams)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "application_name=a+b&options=-c%20work_mem%3D64MB&sslmode=disable&") {
		t.Errorf("query values re-encoded: %s", out)
	}
}
