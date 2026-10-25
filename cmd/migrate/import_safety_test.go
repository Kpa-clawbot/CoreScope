package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbschema"
)

func TestResumeRefusesDifferentDestination(t *testing.T) {
	o := importOptions{Source: telemetrySource(t), DatabaseURL: postgresSchema(t), StateDir: t.TempDir(), Kind: "telemetry", BatchSize: 1, afterBatch: func() error { return context.Canceled }}
	if _, err := importSQLite(context.Background(), o); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	o.Resume = true
	o.afterBatch = nil
	o.DatabaseURL = postgresSchema(t)
	if _, err := importSQLite(context.Background(), o); err == nil {
		t.Fatal("resume accepted a different PostgreSQL destination")
	}
}

func TestResumeAndFinalizationRejectTargetCorruption(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "finalization", true: "resume"}[partial], func(t *testing.T) {
			o := importOptions{Source: telemetrySource(t), DatabaseURL: postgresSchema(t), StateDir: t.TempDir(), Kind: "telemetry", BatchSize: 1}
			if partial {
				o.afterBatch = func() error { return context.Canceled }
			}
			_, err := importSQLite(context.Background(), o)
			if partial {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			db := openImportDB(t, o.DatabaseURL)
			if _, err := db.Exec(`UPDATE observers SET name='tampered'`); err != nil {
				t.Fatal(err)
			}
			if partial {
				o.Resume = true
				o.afterBatch = nil
				_, err = importSQLite(context.Background(), o)
			} else {
				_, err = finalizeImport(context.Background(), o.DatabaseURL, o.Kind)
			}
			if err == nil {
				t.Fatal("corrupted target was accepted")
			}
			if err := dbschema.AssertReady(db); err == nil {
				t.Fatal("corrupted target became runtime-ready")
			}
		})
	}
}

func TestImportPreservesRawCaseJSONAndFractionalScore(t *testing.T) {
	source := telemetrySource(t)
	db, err := sql.Open("sqlite3", source)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{`INSERT INTO nodes(public_key,name,advert_count) VALUES('AbCd','MiXeD',123)`, `UPDATE observations SET path_json='{malformed',score=0.75 WHERE id=90`, `UPDATE transmissions SET channel_hash='public',payload_type=5 WHERE id=80`} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	db.Close()
	dsn := postgresSchema(t)
	if _, err := importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "telemetry"}); err != nil {
		t.Fatal(err)
	}
	target := openImportDB(t, dsn)
	var key, jsonText, channel string
	var count int
	var score float64
	if err := target.QueryRow(`SELECT public_key,advert_count FROM nodes`).Scan(&key, &count); err != nil || key != "AbCd" || count != 123 {
		t.Fatal("raw key/counter changed", err)
	}
	if err := target.QueryRow(`SELECT path_json,score FROM observations WHERE id=90`).Scan(&jsonText, &score); err != nil || jsonText != "{malformed" || score != 0.75 {
		t.Fatal("raw JSON or fractional score changed", err)
	}
	if err := target.QueryRow(`SELECT channel_hash FROM transmissions WHERE id=80`).Scan(&channel); err != nil || channel != "public" {
		t.Fatal("raw channel hash changed", err)
	}
}

func TestImportRejectsUnexpectedViewsTriggersAndFutureAccounts(t *testing.T) {
	for _, statement := range []string{`CREATE VIEW unexpected AS SELECT * FROM users`, `CREATE TRIGGER surprise AFTER INSERT ON users BEGIN DELETE FROM tokens; END`, `UPDATE schema_version SET version=999`} {
		source := accountSource(t, 5)
		db, err := sql.Open("sqlite3", source)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
		db.Close()
		dsn := postgresSchema(t)
		_, err = importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "accounts"})
		if err == nil {
			t.Fatal("unsupported source accepted")
		}
		var n int
		if err := openImportDB(t, dsn).QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()`).Scan(&n); err != nil || n != 0 {
			t.Fatal("unsupported source changed target", err)
		}
	}
}

func TestAccountsReseedDeletedHistoricalReferences(t *testing.T) {
	source := accountSource(t, 5)
	db, err := sql.Open("sqlite3", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_log(at,actor_user_id,target_user_id,action) VALUES(1,700000,900000,'deleted'); UPDATE users SET activated_by=1000000`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	dsn := postgresSchema(t)
	if _, err := importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "accounts"}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := openImportDB(t, dsn).QueryRow(`INSERT INTO users(email,display_name,password_hash,created_at) VALUES('next@example.invalid','next','hash',1) RETURNING id`).Scan(&id); err != nil || id <= 1000000 {
		t.Fatalf("historical account identity reused: %d,%v", id, err)
	}
}

func TestSourceFingerprintCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := sourceFingerprintContext(ctx, telemetrySource(t))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled source hashing: %v", err)
	}
}

func TestImportRefusesLegacyV2WithUpgradeGuidance(t *testing.T) {
	source := telemetrySource(t)
	db, err := sql.Open("sqlite3", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE observations RENAME COLUMN observer_idx TO observer_id`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, err = importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: postgresSchema(t), StateDir: t.TempDir(), Kind: "telemetry"})
	if err == nil || !strings.Contains(err.Error(), "upgrade") {
		t.Fatal("v2 refusal lacks explicit upgrade guidance")
	}
}

func TestAccountVersionMatrixAndMissingDeclaredTable(t *testing.T) {
	for version := 0; version <= 5; version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			var source string
			if version > 0 {
				source = accountSource(t, version)
			} else {
				source = filepath.Join(t.TempDir(), "empty.db")
				db, err := sql.Open("sqlite3", source)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`CREATE TABLE schema_version(version INTEGER NOT NULL);INSERT INTO schema_version VALUES(0)`); err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
			report, err := importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: postgresSchema(t), StateDir: t.TempDir(), Kind: "accounts"})
			if err != nil || !report.Verified {
				t.Fatalf("account version %d: %v", version, err)
			}
		})
	}
	t.Run("missing-table", func(t *testing.T) {
		source := accountSource(t, 5)
		db, err := sql.Open("sqlite3", source)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DROP TABLE mail_events`); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if _, err := importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: postgresSchema(t), StateDir: t.TempDir(), Kind: "accounts"}); err == nil {
			t.Fatal("incomplete declared account schema was accepted")
		}
	})
}

func TestNormalizeDoesNotRewriteObservationRows(t *testing.T) {
	raw := telemetrySource(t)
	db, err := sql.Open("sqlite3", raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<1000)
		INSERT INTO observations(id,transmission_id,observer_idx,timestamp) SELECT x+1000,80,NULL,x FROM n`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	working := filepath.Join(t.TempDir(), "working.sqlite")
	if err := snapshotSource(context.Background(), raw, working); err != nil {
		t.Fatal(err)
	}
	db, err = openSQLite(working, "rw")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tables, err := layouts("telemetry")
	if err != nil {
		t.Fatal(err)
	}
	var before, after int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := normalizeSource(db, "telemetry", raw, tables); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after-before >= 50 {
		t.Fatalf("normalization rewrote unchanged bulk rows: %d changes for 1003 observations", after-before)
	}
	t.Logf("normalization wrote %d rows for 1003 unchanged observations", after-before)
}

func TestImportPopulatesPlannerStatsBeforeReadiness(t *testing.T) {
	dsn := postgresSchema(t)
	if _, err := importSQLite(context.Background(), importOptions{Source: telemetrySource(t), DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "telemetry"}); err != nil {
		t.Fatal(err)
	}
	db := openImportDB(t, dsn)
	var estimate float64
	if err := db.QueryRow(`SELECT reltuples FROM pg_class WHERE oid=to_regclass('observations')`).Scan(&estimate); err != nil {
		t.Fatal(err)
	}
	if estimate != 3 {
		t.Fatalf("planner estimate before readiness=%v; want 3 imported observations", estimate)
	}
	if err := dbschema.AssertReady(db); err == nil {
		t.Fatal("statistics preparation opened readiness before finalization")
	}
}

func TestReservedNameDoesNotHideSourceTrigger(t *testing.T) {
	source := accountSource(t, 5)
	db, err := sql.Open("sqlite3", source)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TRIGGER hidden_source AFTER INSERT ON users BEGIN DELETE FROM tokens; END`,
		`PRAGMA writable_schema=ON`,
		`UPDATE sqlite_master SET name='sqlite_hidden_source',sql=replace(sql,'hidden_source','sqlite_hidden_source') WHERE type='trigger' AND name='hidden_source'`,
		`PRAGMA writable_schema=OFF`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	db.Close()
	db, err = openSQLite(source, "ro")
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name='sqlite_hidden_source'`).Scan(&count); err != nil || count != 1 {
		db.Close()
		t.Fatalf("tampered schema fixture: count=%d err=%v", count, err)
	}
	db.Close()
	if _, err := importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: postgresSchema(t), StateDir: t.TempDir(), Kind: "accounts"}); err == nil {
		t.Fatal("reserved-name trigger bypassed source validation")
	}
}
