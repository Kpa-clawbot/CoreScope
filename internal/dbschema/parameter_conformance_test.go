package dbschema

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/pgutil"
)

func testNumberedParameters(t *testing.T, db *sql.DB, backend dbconfig.Backend) {
	t.Helper()
	query := fmt.Sprintf("SELECT CAST(%s AS BIGINT),CAST(%s AS BIGINT),CAST(%s AS BIGINT)", backend.Parameter(2), backend.Parameter(1), backend.Parameter(2))
	for _, second := range []any{int64(22), nil} {
		var a, b, c sql.NullInt64
		if err := db.QueryRow(query, int64(11), second).Scan(&a, &b, &c); err != nil {
			t.Fatal(err)
		}
		if !b.Valid || b.Int64 != 11 || a != c {
			t.Fatalf("reordered/repeated parameters: %v,%v,%v", a, b, c)
		}
		if second == nil {
			if a.Valid {
				t.Fatal("NULL changed")
			}
		} else if !a.Valid || a.Int64 != 22 {
			t.Fatal("second argument bound by appearance, not position")
		}
	}
}

func TestSQLiteNumberedParameterConformance(t *testing.T) {
	db, _ := sqliteTestDB(t)
	testNumberedParameters(t, db, dbconfig.SQLite)
}

func TestPostgresNumberedParameterConformance(t *testing.T) {
	db, err := pgutil.Open(postgresSchema(t), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	testNumberedParameters(t, db, dbconfig.Postgres)
}

func TestPostgresNamedEntrypointsKeepImportGate(t *testing.T) {
	db, err := pgutil.Open(postgresSchema(t), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := ApplyForImport(db, nil); err != nil {
		t.Fatal(err)
	}
	if err := AssertPostgresReady(db); err == nil {
		t.Fatal("named reader entrypoint bypassed incomplete import")
	}
	if err := ApplyPostgres(db, nil); err == nil {
		t.Fatal("named bootstrap entrypoint bypassed incomplete import")
	}
}
