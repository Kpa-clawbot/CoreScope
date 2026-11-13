package mbcapqueue

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"github.com/meshcore-analyzer/dbx"
	"github.com/meshcore-analyzer/dbx/dbxtest"
)

// pgControlDDL is the control-table contract (internal/dbschema owns the real
// migration); %s is the quoted schema.
const pgControlDDL = `
CREATE TABLE %[1]s.prune_requests (id TEXT PRIMARY KEY CHECK (id ~ '^[0-9a-f]{16,64}$'),
  created_at TEXT NOT NULL DEFAULT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"000Z"'),
  payload TEXT NOT NULL CHECK (octet_length(payload) <= 1048576));
CREATE TABLE %[1]s.prune_results (id TEXT PRIMARY KEY CHECK (id ~ '^[0-9a-f]{16,64}$'),
  created_at TEXT NOT NULL DEFAULT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"000Z"'),
  payload TEXT NOT NULL CHECK (octet_length(payload) <= 1048576));
CREATE TABLE %[1]s.mbcap_snapshots (id TEXT PRIMARY KEY CHECK (id ~ '^[0-9a-f]{16,64}$'),
  created_at TEXT NOT NULL DEFAULT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"000Z"'),
  payload TEXT NOT NULL CHECK (octet_length(payload) <= 4194304));`

// serverGrants are exactly the server role's privileges on the control tables:
// column-level INSERT, so it cannot choose created_at.
const serverGrants = `
GRANT USAGE ON SCHEMA %[1]s TO %[2]s;
GRANT INSERT (id, payload) ON %[1]s.prune_requests, %[1]s.mbcap_snapshots TO %[2]s;
GRANT SELECT ON %[1]s.prune_requests, %[1]s.mbcap_snapshots, %[1]s.prune_results TO %[2]s;`

// pgEnv is a Postgres test environment: owner is the ingestor's (owner)
// connection, server a single-connection pool that has SET ROLE to a role
// holding only serverGrants.
type pgEnv struct {
	schema string
	owner  *sql.DB
	server *sql.DB
}

func newPGEnv(t *testing.T) pgEnv {
	t.Helper()
	owner := dbxtest.OpenPostgres(t) // skips without $CORESCOPE_TEST_PG_DSN
	var schema string
	if err := owner.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	qs := `"` + schema + `"`
	if _, err := owner.Exec(fmt.Sprintf(pgControlDDL, qs)); err != nil {
		t.Fatalf("create control tables: %v", err)
	}

	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	role := "pqtest_srv_" + hex.EncodeToString(b[:])
	qr := `"` + role + `"`
	if _, err := owner.Exec(`CREATE ROLE ` + qr + ` NOLOGIN`); err != nil {
		t.Fatalf("create role: %v", err)
	}
	t.Cleanup(func() {
		// Runs before dbxtest drops the schema (cleanups are LIFO).
		if _, err := owner.Exec(`DROP OWNED BY ` + qr); err != nil {
			t.Errorf("drop owned by role: %v", err)
		}
		if _, err := owner.Exec(`DROP ROLE ` + qr); err != nil {
			t.Errorf("drop role: %v", err)
		}
	})
	if _, err := owner.Exec(fmt.Sprintf(serverGrants, qs, qr)); err != nil {
		t.Fatalf("grant: %v", err)
	}

	server, err := sql.Open(dbx.PostgresDriverName, os.Getenv("CORESCOPE_TEST_PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	// One long-lived connection, so the SET ROLE below governs every query.
	server.SetMaxOpenConns(1)
	server.SetMaxIdleConns(1)
	server.SetConnMaxLifetime(0)
	server.SetConnMaxIdleTime(0)
	if _, err := server.Exec(`SET ROLE ` + qr); err != nil {
		t.Fatalf("set role: %v", err)
	}
	var cur string
	if err := server.QueryRow(`SELECT current_user`).Scan(&cur); err != nil || cur != role {
		t.Fatalf("current_user = %q (%v), want %q", cur, err, role)
	}
	return pgEnv{schema: schema, owner: owner, server: server}
}
