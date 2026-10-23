package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/users"
)

func TestReverseTelemetryPreservesRowsAndFutureIDs(t *testing.T) {
	dsn := postgresDatabase(t)
	ctx := context.Background()
	_, err := importSQLite(ctx, importOptions{Source: telemetrySource(t), DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "telemetry"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := finalizeImport(ctx, dsn, "telemetry"); err != nil {
		t.Fatal(err)
	}
	pg := openImportDB(t, dsn)
	if _, err := pg.Exec(`INSERT INTO _async_migrations(name,status,started_at,ended_at,error) VALUES('obs_observer_ts_idx_v1','done','raw started','raw ended',NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`SELECT setval(pg_get_serial_sequence('transmissions','id'),100500,false)`, `UPDATE observations SET score=3.5,resolved_path=' {"raw": true} ' WHERE id=90`, `UPDATE transmissions SET first_seen='malformed date kept as text' WHERE id=80`} {
		if _, err := pg.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	dest := filepath.Join(t.TempDir(), "reverse # % é.sqlite")
	options := reverseOptions{DatabaseURL: dsn, Destination: dest, StateDir: t.TempDir(), Kind: "telemetry", BatchSize: 2}
	injected := false
	options.afterBatch = func() error {
		if !injected {
			injected = true
			return errors.New("interrupt after committed batch")
		}
		return nil
	}
	if _, err := reversePostgres(ctx, options); err == nil || !injected {
		t.Fatal("expected interruption after committed batch", err)
	}
	options.Resume = true
	options.afterBatch = nil
	report, err := reversePostgres(ctx, options)
	if err != nil || !report.Verified {
		t.Fatal("resume reverse", err)
	}
	db, err := openSQLite(dest, "rw")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := dbschema.AssertSQLiteReady(db); err != nil {
		t.Fatal(err)
	}
	var indexCount int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_observations_observer_idx_timestamp'`).Scan(&indexCount); err != nil || indexCount != 1 {
		t.Fatal("copied done ledger lacks its native index", err)
	}
	var status, started, ended string
	if err := db.QueryRow(`SELECT status,started_at,ended_at FROM _async_migrations WHERE name='obs_observer_ts_idx_v1'`).Scan(&status, &started, &ended); err != nil || status != "done" || started != "raw started" || ended != "raw ended" {
		t.Fatal("source migration ledger changed", err)
	}
	var node, parent, unused int
	var plan string
	if err := db.QueryRow(`EXPLAIN QUERY PLAN SELECT timestamp FROM observations WHERE observer_idx=999 ORDER BY timestamp`).Scan(&node, &parent, &unused, &plan); err != nil || !strings.Contains(plan, "idx_observations_observer_idx_timestamp") {
		t.Fatal("observer/time query cannot use native index", plan, err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM observations WHERE (id=0 AND observer_idx=0 AND path_json IS NULL) OR (id=90 AND observer_idx=999 AND path_json='' AND score=3.5 AND resolved_path=' {"raw": true} ') OR (id=91 AND observer_idx IS NULL)`).Scan(&n); err != nil || n != 3 {
		t.Fatal("row parity", n, err)
	}
	var id int64
	if err := db.QueryRow(`INSERT INTO transmissions(raw_hex,hash,first_seen) VALUES('00','new','2026-01-03') RETURNING id`).Scan(&id); err != nil || id != 100500 {
		t.Fatal("is_called=false future ID", id, err)
	}
	if err := db.QueryRow(`INSERT INTO observers(id) VALUES('future') RETURNING rowid`).Scan(&id); err != nil || id <= 999 {
		t.Fatal("observer orphan reuse", id, err)
	}
	if _, err := os.Stat(filepath.Join(options.StateDir, "telemetry", "recovery.pg_dump")); err != nil {
		t.Fatal("native recovery missing", err)
	}
	// Verify the actual native recovery archive, not only its filename/hash.
	restored := postgresDatabase(t)
	config, err := pgutil.ParseConfig(restored)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("pg_restore", "--no-password", "--no-owner", "--no-privileges", "--exit-on-error", "--dbname", config.Database, filepath.Join(options.StateDir, "telemetry", "recovery.pg_dump"))
	command.Env, err = pgutil.CommandEnv(restored)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Run(); err != nil {
		t.Fatal("native PostgreSQL recovery restore failed")
	}
	var manifest reverseManifest
	if err := readPrivateJSON(filepath.Join(options.StateDir, "telemetry", "reverse.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	recovery := openImportDB(t, restored)
	tables, err := layouts("telemetry")
	if err != nil {
		t.Fatal(err)
	}
	for i, table := range tables {
		got, err := digestTarget(ctx, recovery, table)
		if err != nil || got != manifest.Source[i] {
			t.Fatal("native recovery data mismatch", table.Name, err)
		}
	}
}

func TestReverseSequenceMutationLeavesNativePendingMarker(t *testing.T) {
	owner := postgresDatabase(t)
	ctx := context.Background()
	db := openImportDB(t, owner)
	if err := dbschema.ApplyPostgres(db, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO observers(id) VALUES('only-observer')`); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "pending.sqlite")
	options := reverseOptions{DatabaseURL: owner, Destination: dest, StateDir: t.TempDir(), Kind: "telemetry", BatchSize: 1, afterBatch: func() error {
		_, err := db.Exec(`SELECT nextval(pg_get_serial_sequence('observers','rowid'))`)
		return err
	}}
	if _, err := reversePostgres(ctx, options); err == nil {
		t.Fatal("changed source sequence accepted")
	}
	target, err := openSQLite(dest, "ro")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := dbconfig.AssertSQLiteImportComplete(target); !errors.Is(err, dbconfig.ErrSQLiteImportIncomplete) {
		t.Fatal("partial reverse target can be adopted elsewhere", err)
	}
}

func TestReverseAccountsPreservesAuthenticationAndIdentity(t *testing.T) {
	dsn := postgresDatabase(t)
	ctx := context.Background()
	_, err := importSQLite(ctx, importOptions{Source: accountSource(t, 5), DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "accounts"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := finalizeImport(ctx, dsn, "accounts"); err != nil {
		t.Fatal(err)
	}
	pg := openImportDB(t, dsn)
	for _, q := range []string{`SELECT setval(pg_get_serial_sequence('mail_events','id'),9001,false)`, `INSERT INTO audit_log(at,actor_user_id,action,target_user_id) VALUES(2,888,'deleted',999)`} {
		if _, err := pg.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	dest := filepath.Join(t.TempDir(), "accounts.sqlite")
	options := reverseOptions{DatabaseURL: dsn, Destination: dest, StateDir: t.TempDir(), Kind: "accounts", BatchSize: 2}
	report, err := reversePostgres(ctx, options)
	if err != nil || !report.Verified {
		t.Fatal("reverse accounts", err)
	}
	db, err := openSQLite(dest, "rw")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := users.AssertSQLiteReady(db); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM mail_events WHERE mail_id=4`).Scan(&id); err != nil || id != 900 {
		t.Fatal("event id", id, err)
	}
	if err := db.QueryRow(`INSERT INTO mail_events(mail_id,event,at) VALUES(4,'new',99) RETURNING id`).Scan(&id); err != nil || id != 9001 {
		t.Fatal("event sequence", id, err)
	}
	if err := db.QueryRow(`INSERT INTO users(email,display_name,password_hash,created_at) VALUES('new@example.invalid','','hash',3) RETURNING id`).Scan(&id); err != nil || id != 1000 {
		t.Fatal("historical user references", id, err)
	}
}
