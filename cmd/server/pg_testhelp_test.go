package main

import (
	"context"
	"database/sql"
	"testing"

	"github.com/meshcore-analyzer/dbschema/pgschema"
	"github.com/meshcore-analyzer/dbx/dbxtest"
)

// newPostgresTestDB returns a server *DB reading a fresh Postgres schema
// (pgschema's end state) in an isolated schema of $CORESCOPE_TEST_PG_DSN, plus
// a separate writable handle on the same schema for seeding rows — the server
// handle itself must never be used to write. Skips when the env var is unset.
//
// The *DB is built the way OpenDB builds one, minus SQLite specifics: schema
// detection and prepared statements run against Postgres, so a test using it
// exercises the server's real SQL.
func newPostgresTestDB(t testing.TB) (srv *DB, seed *sql.DB) {
	t.Helper()
	seed = dbxtest.OpenPostgres(t)
	ctx := context.Background()
	if err := pgschema.Apply(ctx, seed, t.Logf); err != nil {
		t.Fatalf("pgschema.Apply: %v", err)
	}
	srv = &DB{conn: seed, pg: true}
	if err := srv.detectSchema(ctx, seed); err != nil {
		t.Fatalf("detectSchema on Postgres: %v", err)
	}
	if !srv.isV3 {
		t.Fatal("Postgres schema not detected as v3")
	}
	if err := srv.prepareStatements(); err != nil {
		t.Fatalf("prepareStatements on Postgres: %v", err)
	}
	return srv, seed
}

func TestNewPostgresTestDBDetectsEndStateSchema(t *testing.T) {
	db, _ := newPostgresTestDB(t)
	for name, got := range map[string]bool{
		"hasResolvedPath":     db.hasResolvedPath,
		"hasObsRawHex":        db.hasObsRawHex,
		"hasScopeName":        db.hasScopeName,
		"hasLastSeen":         db.hasLastSeen,
		"hasDefaultScope":     db.hasDefaultScope,
		"hasConfiguredScope":  db.hasConfiguredScope,
		"hasMultibyteSupCols": db.hasMultibyteSupCols,
	} {
		if !got {
			t.Errorf("%s = false on the end-state Postgres schema", name)
		}
	}
}
