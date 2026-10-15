package users

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
)

func TestSQLiteWALBootstrapWaitIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	dsn, err := dbconfig.SQLiteURI(path, url.Values{"_pragma": {"busy_timeout(0)"}})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	owner.SetMaxOpenConns(1)
	mustSQL(t, owner, `CREATE TABLE preserved(value TEXT);INSERT INTO preserved VALUES('keep');BEGIN IMMEDIATE`)
	defer owner.Exec(`ROLLBACK`)
	other, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err = enableSQLiteWAL(ctx, other); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("locked journal wait did not honor its bound: %v", err)
	}
	if _, err = owner.Exec(`COMMIT`); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err = enableSQLiteWAL(ctx2, other); err != nil {
		t.Fatal("journal did not become usable after lock release", err)
	}
	var value string
	if err = other.QueryRow(`SELECT value FROM preserved`).Scan(&value); err != nil || value != "keep" {
		t.Fatal("bootstrap changed data", err)
	}
}
