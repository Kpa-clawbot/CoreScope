package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestKeyBackfillStateAndRewriteFeedRoundTrip(t *testing.T) {
	source := telemetrySource(t)
	sqlite, err := openSQLite(source, "rw")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO tx_rewrite_feed(id,tx_id,created_at) VALUES(700,80,123)`,
		`INSERT INTO key_backfill_state(kind,run_fp,cursor,upper,rewritten,started_at) VALUES('channel','opaque fingerprint',50,80,3,124)`,
		`INSERT INTO key_backfill_done(kind,key_fp,done_at) VALUES('region','key fingerprint',125)`,
		`INSERT INTO tx_rewrite_feed(id,tx_id,created_at) VALUES(900,80,126)`,
		`DELETE FROM tx_rewrite_feed WHERE id=900`,
	} {
		if _, err := sqlite.Exec(statement); err != nil {
			sqlite.Close()
			t.Fatal(err)
		}
	}
	sqlite.Close()
	ctx := context.Background()
	dsn := postgresDatabase(t)
	if _, err := importSQLite(ctx, importOptions{Source: source, DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "telemetry", BatchSize: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := finalizeImport(ctx, dsn, "telemetry"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "roundtrip.sqlite")
	report, err := reversePostgres(ctx, reverseOptions{DatabaseURL: dsn, Destination: target, StateDir: t.TempDir(), Kind: "telemetry", BatchSize: 1})
	if err != nil || !report.Verified {
		t.Fatal("reverse verification", err)
	}
	sqlite, err = openSQLite(target, "rw")
	if err != nil {
		t.Fatal(err)
	}
	defer sqlite.Close()
	var count int
	for _, query := range []string{
		`SELECT count(*) FROM tx_rewrite_feed WHERE id=700 AND tx_id=80 AND created_at=123`,
		`SELECT count(*) FROM key_backfill_state WHERE kind='channel' AND run_fp='opaque fingerprint' AND cursor=50 AND upper=80 AND rewritten=3 AND started_at=124`,
		`SELECT count(*) FROM key_backfill_done WHERE kind='region' AND key_fp='key fingerprint' AND done_at=125`,
	} {
		if err := sqlite.QueryRow(query).Scan(&count); err != nil || count != 1 {
			t.Fatal("backfill state changed", err)
		}
	}
	var id int64
	if err := sqlite.QueryRow(`INSERT INTO tx_rewrite_feed(tx_id,created_at) VALUES(80,127) RETURNING id`).Scan(&id); err != nil || id <= 900 {
		t.Fatalf("feed identity reused deleted high water: %d,%v", id, err)
	}
}
