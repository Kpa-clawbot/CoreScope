package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbschema/legacy"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/pgutil/pgtest"
	"github.com/meshcore-analyzer/users"
)

func telemetrySource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := legacy.ApplyBase(db); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Apply(db, t.Logf); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO observers(rowid,id,name) VALUES(0,'observer-zero',NULL),(50,'observer-gap','')`,
		`INSERT INTO transmissions(id,raw_hex,hash,first_seen,last_seen) VALUES(0,'00','zero','2026-01-01 00:00:00',0),(80,'00','gap','2026-01-02T00:00:00Z',1),(99999,'00','deleted','2026-01-01',0)`,
		`DELETE FROM transmissions WHERE id=99999`,
		`INSERT INTO observations(id,transmission_id,observer_idx,path_json,timestamp) VALUES(0,0,0,NULL,0),(90,80,999,'',1),(91,80,NULL,'[]',2)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func openImportDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := pgutil.Open(dsn, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestImportTelemetryPreservesIDsNullsAndHighWater(t *testing.T) {
	source := telemetrySource(t)
	before, err := sourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	dsn := pgtest.NewSchema(t)
	report, err := importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "telemetry", BatchSize: 2})
	if err != nil {
		t.Fatalf("import valid telemetry: %v", err)
	}
	if !report.Verified || len(report.Tables) != len(dbschema.Tables) {
		t.Fatalf("missing verification: %+v", report)
	}
	db := openImportDB(t, dsn)
	if err := dbschema.AssertReady(db); err == nil {
		t.Fatal("import accepted before explicit cutover finalization")
	}
	if err := finalizeImport(context.Background(), dsn, "telemetry"); err != nil {
		t.Fatal(err)
	}
	if err := dbschema.AssertReady(db); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM observations WHERE (id=0 AND observer_idx=0 AND path_json IS NULL) OR (id=90 AND observer_idx=999 AND path_json='') OR (id=91 AND observer_idx IS NULL)`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("lost observer identity/null/orphan links: %d", n)
	}
	var id int64
	if err := db.QueryRow(`INSERT INTO observers(id) VALUES('next-observer') RETURNING rowid`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id <= 999 {
		t.Fatalf("observer identity reused historical reference: %d", id)
	}
	if err := db.QueryRow(`INSERT INTO transmissions(raw_hex,hash,first_seen) VALUES('00','next','2026-01-03') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id <= 99999 {
		t.Fatalf("transmission identity ignored deleted high water: %d", id)
	}
	after, err := sourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("migration changed original SQLite source")
	}
}

func TestImportInterruptResumeAndWrongSource(t *testing.T) {
	source := telemetrySource(t)
	dsn, state := pgtest.NewSchema(t), t.TempDir()
	options := importOptions{Source: source, DatabaseURL: dsn, StateDir: state, Kind: "telemetry", BatchSize: 1, afterBatch: func() error { return context.Canceled }}
	if _, err := importSQLite(context.Background(), options); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation after committed batch, got %v", err)
	}
	db := openImportDB(t, dsn)
	if err := dbschema.AssertReady(db); err == nil {
		t.Fatal("partially imported database accepted")
	}
	options.afterBatch = nil
	if _, err := importSQLite(context.Background(), options); err == nil {
		t.Fatal("partial import resumed without explicit resume")
	}
	options.Resume = true
	options.Source = telemetrySource(t)
	changed, err := sql.Open("sqlite3", options.Source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := changed.Exec(`UPDATE transmissions SET raw_hex='ff' WHERE id=0`); err != nil {
		t.Fatal(err)
	}
	changed.Close()
	if _, err := importSQLite(context.Background(), options); err == nil {
		t.Fatal("resumed with a different source")
	}
	options.Source = source
	report, err := importSQLite(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Verified {
		t.Fatal("resumed import unverified")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("resume duplicated/lost observations: %d", n)
	}
	if _, err := importSQLite(context.Background(), options); err != nil {
		t.Fatalf("verified retry must be idempotent: %v", err)
	}
}

func TestImportRefusesUnknownSourceAndOccupiedTarget(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		source, dsn := telemetrySource(t), pgtest.NewSchema(t)
		if unknown {
			db, err := sql.Open("sqlite3", source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`ALTER TABLE transmissions ADD COLUMN future_secret TEXT`); err != nil {
				t.Fatal(err)
			}
			db.Close()
		} else {
			db := openImportDB(t, dsn)
			if _, err := db.Exec(`CREATE TABLE keep_me(value TEXT); INSERT INTO keep_me VALUES('retained')`); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "telemetry"}); err == nil {
			t.Fatal("unsupported source/occupied destination accepted")
		}
		if !unknown {
			var value string
			if err := openImportDB(t, dsn).QueryRow(`SELECT value FROM keep_me`).Scan(&value); err != nil {
				t.Fatal(err)
			}
			if value != "retained" {
				t.Fatal("existing target changed")
			}
		}
	}
}

func accountSource(t *testing.T, version int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_version(version INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range users.LegacyMigrations()[:version] {
		for _, stmt := range migration {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := db.Exec(`INSERT INTO schema_version VALUES(?)`, version); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO users(id,email,display_name,password_hash,role,status,created_at) VALUES(0,'user@example.invalid','test','preserved-hash','user','active',1)`,
		`INSERT INTO sessions(id,token_hash,user_id,csrf_token,created_at,expires_at,last_seen_at) VALUES(90,'preserved-token',0,'preserved-csrf',1,9999999999,2)`,
		`INSERT INTO tokens(token_hash,user_id,purpose,expires_at,used_at) VALUES('one-use',0,'reset',9999999999,NULL)`,
		`INSERT INTO mail_log(id,to_email,purpose,sent_at,last_event_at) VALUES(4,'user@example.invalid','activate',1,1)`,
		`INSERT INTO mail_events(rowid,mail_id,event,at) VALUES(900,4,'delivered',1)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestImportLegacyAccountsPreservesAuthenticationAndEventOrder(t *testing.T) {
	source, dsn := accountSource(t, 1), pgtest.NewSchema(t)
	report, err := importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "accounts", BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Verified {
		t.Fatal("accounts not verified")
	}
	db := openImportDB(t, dsn)
	if err := users.AssertReady(db); err == nil {
		t.Fatal("accounts accepted before finalization")
	}
	if err := finalizeImport(context.Background(), dsn, "accounts"); err != nil {
		t.Fatal(err)
	}
	if err := users.AssertReady(db); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := db.QueryRow(`SELECT password_hash FROM users WHERE id=0`).Scan(&value); err != nil || value != "preserved-hash" {
		t.Fatalf("password hash changed: %v", err)
	}
	if err := db.QueryRow(`SELECT token_hash FROM sessions WHERE id=90 AND user_id=0`).Scan(&value); err != nil || value != "preserved-token" {
		t.Fatalf("session changed: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE token_hash='one-use' AND used_at IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("one-time token changed: %v", err)
	}
	if err := db.QueryRow(`SELECT id FROM mail_events WHERE mail_id=4`).Scan(&n); err != nil || n != 900 {
		t.Fatalf("mail event identity changed: %d %v", n, err)
	}
}
