package dbx_test

import (
	"database/sql"
	"testing"

	"github.com/meshcore-analyzer/dbx"
	"github.com/meshcore-analyzer/dbx/dbxtest"
)

func TestIsPostgresNil(t *testing.T) {
	if dbx.IsPostgres(nil) {
		t.Fatal("nil *sql.DB reported as Postgres")
	}
}

func TestIsPostgresOtherDriver(t *testing.T) {
	// sql.Open is lazy, so an unknown-to-the-network DSN is fine here; the
	// driver only has to be registered. pgx's own "pgx" driver is registered by
	// importing dbx, and is not the qmark wrapper.
	db, err := sql.Open("pgx", "postgres://invalid.invalid/none")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if dbx.IsPostgres(db) {
		t.Fatal(`a handle opened with the plain "pgx" driver reported as the qmark Postgres driver`)
	}
}

func TestIsPostgresQmark(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	if !dbx.IsPostgres(db) {
		t.Fatal("dbxtest.OpenPostgres handle not reported as Postgres")
	}
}
