package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil/pgtest"
)

func dateSource(t *testing.T, declared string, values []any) string {
	t.Helper()
	source := telemetrySource(t)
	db, err := sql.Open(importSQLiteDriver, sqliteURL(source, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if declared != "DATETIME" {
		var ddl string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='dropped_packets'`).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(ddl, "dropped_at DATETIME") {
			t.Fatal("legacy date declaration changed")
		}
		if _, err := db.Exec(`DROP TABLE dropped_packets`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(strings.Replace(ddl, "dropped_at DATETIME", "dropped_at "+declared, 1)); err != nil {
			t.Fatal(err)
		}
	}
	for i, value := range values {
		if _, err := db.Exec(`INSERT INTO dropped_packets(id,reason,dropped_at) VALUES(?,?,?)`, i, "synthetic", value); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return source
}

func TestImportPreservesDeclaredDateTextAndResume(t *testing.T) {
	values := []any{
		nil,
		"2026-10-09T01:02:03.123456789-04:30",
		"2026-10-09 01:02:03.1200+00:00",
		"2026-10-09T01:02:03Z",
		"2026-10-09",
		"",
		"not a timestamp",
	}
	for _, declared := range []string{"DATETIME", "DATE", "TIMESTAMP"} {
		t.Run(declared, func(t *testing.T) {
			source := dateSource(t, declared, values)
			before, err := sourceFingerprint(source)
			if err != nil {
				t.Fatal(err)
			}
			dsn := pgtest.NewSchema(t)
			target := openImportDB(t, dsn)
			options := importOptions{Source: source, DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "telemetry", BatchSize: 2}
			options.afterBatch = func() error {
				var rows int
				if err := target.QueryRow(`SELECT count(*) FROM dropped_packets`).Scan(&rows); err != nil {
					return err
				}
				if rows == 2 {
					return context.Canceled
				}
				return nil
			}
			if _, err := importSQLite(context.Background(), options); !errors.Is(err, context.Canceled) {
				t.Fatalf("expected interruption after preserving two date rows: %v", err)
			}
			if err := dbschema.AssertReady(target); err == nil {
				t.Fatal("interrupted date import became ready")
			}
			options.Resume, options.afterBatch = true, nil
			report, err := importSQLite(context.Background(), options)
			if err != nil || !report.Verified {
				t.Fatalf("resume date import: verified=%v error=%v", report.Verified, err)
			}
			if err := finalizeImport(context.Background(), dsn, "telemetry"); err != nil {
				t.Fatal(err)
			}
			if err := dbschema.AssertReady(target); err != nil {
				t.Fatal(err)
			}
			rows, err := target.Query(`SELECT id,dropped_at FROM dropped_packets ORDER BY id`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			count := 0
			for rows.Next() {
				var id int
				var value sql.NullString
				if err := rows.Scan(&id, &value); err != nil {
					t.Fatal(err)
				}
				if id != count || id >= len(values) {
					t.Fatalf("date row identity changed: %d", id)
				}
				if want, ok := values[id].(string); ok {
					if !value.Valid || value.String != want {
						t.Errorf("date text changed at row %d: got %q want %q", id, value.String, want)
					}
				} else if value.Valid {
					t.Errorf("NULL date became non-NULL at row %d", id)
				}
				count++
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if count != len(values) {
				t.Fatalf("date rows duplicated or lost: %d", count)
			}
			after, err := sourceFingerprint(source)
			if err != nil || before != after {
				t.Fatal("date import changed the original SQLite source", err)
			}
		})
	}
}

func TestImportRefusesNumericStorageInDeclaredDateColumn(t *testing.T) {
	for _, value := range []any{int64(1700000000), 2.5, "12345"} {
		source := dateSource(t, "DATETIME", []any{value})
		before, err := sourceFingerprint(source)
		if err != nil {
			t.Fatal(err)
		}
		dsn := pgtest.NewSchema(t)
		report, err := importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "telemetry"})
		if err == nil || !strings.Contains(err.Error(), "non-text value") || report.Verified {
			t.Fatalf("non-text date storage was silently coerced: verified=%v error=%v", report.Verified, err)
		}
		if err := dbschema.AssertReady(openImportDB(t, dsn)); err == nil {
			t.Fatal("invalid date source became ready")
		}
		after, err := sourceFingerprint(source)
		if err != nil || before != after {
			t.Fatal("refused date import changed the source", err)
		}
	}
}
