package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbschema"
)

// The accelerator can be absent, complete, or midway through its independently
// committed backfill. None of those states may change the canonical rows.
func hopIndexSource(t *testing.T, state string) string {
	t.Helper()
	path := telemetrySource(t)
	db, err := openSQLite(path, "rw")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`UPDATE observations SET path_json=' {malformed',score=0.75 WHERE id=90`,
		`UPDATE observations SET path_json=' ["aB","Cd","aB",17,null] ' WHERE id=91`,
		`INSERT INTO observations(id,transmission_id,observer_idx,path_json,timestamp) VALUES(92,80,NULL,'["Ef"]',3)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if state == "absent" {
		return path
	}
	if err := dbschema.EnsureObservationHopsSchema(db, nil); err != nil {
		t.Fatal(err)
	}
	floor := int64(0)
	if state == "partial" {
		floor = 3
	}
	if _, err := dbschema.IndexObservationHops(db, floor, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE observation_hops_coverage SET since=? WHERE id=1`, floor); err != nil {
		t.Fatal(err)
	}
	return path
}

type observationTuple struct {
	ID, Timestamp int64
	Path          sql.NullString
	Score         sql.NullFloat64
}

func observationTuples(t *testing.T, db *sql.DB) []observationTuple {
	t.Helper()
	rows, err := db.Query(`SELECT id,timestamp,path_json,score FROM observations ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []observationTuple
	for rows.Next() {
		var row observationTuple
		if err := rows.Scan(&row.ID, &row.Timestamp, &row.Path, &row.Score); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPrepareHopIndexSourcePreservesOriginalAndRecovery(t *testing.T) {
	for _, state := range []string{"absent", "complete", "partial"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			source := hopIndexSource(t, state)
			before, err := fingerprint(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			o := importOptions{Source: source, StateDir: t.TempDir(), Kind: "telemetry", targetDigest: "fixture"}
			tables, err := layouts(o.Kind)
			if err != nil {
				t.Fatal(err)
			}
			manifest, normalized, err := prepareSource(ctx, o, tables, true)
			if err != nil {
				t.Fatalf("known %s hop-index source rejected: %v", state, err)
			}
			if after, err := fingerprint(ctx, source); err != nil || after != before {
				t.Fatal("preparation modified source", err)
			}
			var original []observationTuple
			for i, path := range []string{source, filepath.Join(o.StateDir, o.Kind, "recovery.sqlite"), normalized} {
				db, err := openSQLite(path, "ro")
				if err != nil {
					t.Fatal(err)
				}
				got := observationTuples(t, db)
				if i == 0 {
					original = got
				} else if !reflect.DeepEqual(got, original) {
					t.Fatal("canonical observations changed", got, original)
				}
				var objects int
				err = db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name LIKE 'observation_hops%' OR name LIKE 'trg_observation_hops%'`).Scan(&objects)
				db.Close()
				want := 5
				if state == "absent" || i == 2 {
					want = 0
				}
				if err != nil || objects != want {
					t.Fatalf("snapshot %d has %d hop objects, want %d: %v", i, objects, want, err)
				}
			}
			o.Resume = true
			resumed, resumedPath, err := prepareSource(ctx, o, tables, false)
			if err != nil || !reflect.DeepEqual(resumed, manifest) || resumedPath != normalized {
				t.Fatal("prepared snapshots cannot resume unchanged", err)
			}
		})
	}
}

func TestPrepareHopIndexRejectsUnknownOrChangedObjects(t *testing.T) {
	for name, statements := range map[string][]string{
		"missing trigger":     {`DROP TRIGGER trg_observation_hops_ad`},
		"changed trigger":     {`DROP TRIGGER trg_observation_hops_ai`, `CREATE TRIGGER trg_observation_hops_ai AFTER INSERT ON observations BEGIN DELETE FROM observations; END`},
		"changed column":      {`ALTER TABLE observation_hops ADD COLUMN private_payload TEXT`},
		"changed constraint":  {`DROP TABLE observation_hops_coverage`, `CREATE TABLE observation_hops_coverage(id INTEGER PRIMARY KEY,since INTEGER NOT NULL)`},
		"changed primary key": {`DROP TABLE observation_hops`, `CREATE TABLE observation_hops(hop TEXT NOT NULL,timestamp INTEGER NOT NULL,observation_id INTEGER NOT NULL,PRIMARY KEY(observation_id,hop,timestamp)) WITHOUT ROWID`},
		"unknown index":       {`CREATE INDEX surprise_hop_index ON observation_hops(observation_id)`},
		"unknown table":       {`CREATE TABLE private_hops(payload TEXT)`},
		"unknown trigger":     {`CREATE TRIGGER unexpected_hops AFTER INSERT ON observations BEGIN DELETE FROM observation_hops; END`},
		"unknown view":        {`CREATE VIEW extra_hops AS SELECT * FROM observation_hops`},
	} {
		t.Run(name, func(t *testing.T) {
			source := hopIndexSource(t, "complete")
			db, err := openSQLite(source, "rw")
			if err != nil {
				t.Fatal(err)
			}
			for _, stmt := range statements {
				if _, err := db.Exec(stmt); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			ctx := context.Background()
			before, err := fingerprint(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			o := importOptions{Source: source, StateDir: t.TempDir(), Kind: "telemetry", targetDigest: "fixture"}
			tables, err := layouts(o.Kind)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := prepareSource(ctx, o, tables, true); err == nil {
				t.Fatal("unknown or altered source layout was accepted")
			}
			if after, err := fingerprint(ctx, source); err != nil || after != before {
				t.Fatal("refusal modified source", err)
			}
		})
	}
}

func TestHopIndexResumeRejectsChangedExclusionReport(t *testing.T) {
	ctx := context.Background()
	o := importOptions{Source: hopIndexSource(t, "partial"), StateDir: t.TempDir(), Kind: "telemetry", targetDigest: "fixture"}
	tables, err := layouts(o.Kind)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := prepareSource(ctx, o, tables, true)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ExcludedDerived = nil
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.StateDir, o.Kind, "manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	o.Resume = true
	if _, _, err := prepareSource(ctx, o, tables, false); err == nil {
		t.Fatal("altered exclusion report was trusted on resume")
	}
}

func TestHopIndexNativeRoundTripAndInterruptedImport(t *testing.T) {
	requirePostgres(t)
	for _, state := range []string{"absent", "complete", "partial"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			source := hopIndexSource(t, state)
			before, err := fingerprint(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			o := importOptions{Source: source, DatabaseURL: postgresSchema(t), StateDir: t.TempDir(), Kind: "telemetry", BatchSize: 1, afterBatch: func() error { return context.Canceled }}
			if _, err := importSQLite(ctx, o); !errors.Is(err, context.Canceled) {
				t.Fatal("expected committed batch interruption", err)
			}
			o.Resume, o.afterBatch = true, nil
			report, err := importSQLite(ctx, o)
			if err != nil || !report.Verified {
				t.Fatal("hop-index import did not resume", err)
			}
			for _, table := range report.Tables {
				if table.Table == "observation_hops" || table.Table == "observation_hops_coverage" {
					t.Fatal("derived rows included in canonical report")
				}
			}
			var decoded struct {
				ExcludedDerived []struct {
					Table         string `json:"table"`
					SchemaVersion int    `json:"schema_version"`
					Reason        string `json:"reason"`
				} `json:"excluded_derived"`
			}
			data, err := json.Marshal(report)
			if err != nil || json.Unmarshal(data, &decoded) != nil {
				t.Fatal("invalid import report", err)
			}
			wantExclusions := 2
			if state == "absent" {
				wantExclusions = 0
			}
			if len(decoded.ExcludedDerived) != wantExclusions {
				t.Fatal("report did not explicitly identify excluded derived rows", string(data))
			}
			for i, table := range decoded.ExcludedDerived {
				if table.SchemaVersion != 1 || table.Reason == "" || table.Table != []string{"observation_hops", "observation_hops_coverage"}[i] {
					t.Fatal("exclusion report lacks its exact known layout", table)
				}
			}
			target := openImportDB(t, o.DatabaseURL)
			var count int
			if err := target.QueryRow(`SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname IN ('observation_hops','observation_hops_coverage')`).Scan(&count); err != nil || count != 0 {
				t.Fatal("derived SQLite cache copied to PostgreSQL", count, err)
			}
			if _, err := finalizeImport(ctx, o.DatabaseURL, o.Kind); err != nil {
				t.Fatal(err)
			}
			resumed, err := importSQLite(ctx, o)
			if err != nil || !reflect.DeepEqual(resumed, report) {
				t.Fatal("finalized resume changed report", err)
			}
			reverse := reverseOptions{DatabaseURL: o.DatabaseURL, Destination: filepath.Join(t.TempDir(), "reverse.sqlite"), StateDir: t.TempDir(), Kind: "telemetry", BatchSize: 1, afterBatch: func() error { return context.Canceled }}
			if _, err := reversePostgres(ctx, reverse); !errors.Is(err, context.Canceled) {
				t.Fatal("expected reverse interruption", err)
			}
			reverse.Resume, reverse.afterBatch = true, nil
			back, err := reversePostgres(ctx, reverse)
			if err != nil || !back.Verified {
				t.Fatal("hop-index reverse did not resume", err)
			}
			if !reflect.DeepEqual(back.Tables, report.Tables) {
				t.Fatal("canonical table counts or digests changed in the round trip")
			}
			recovered, err := openSQLite(reverse.Destination, "rw")
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			if err := recovered.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name LIKE 'observation_hops%' OR name LIKE 'trg_observation_hops%'`).Scan(&count); err != nil || count != 0 {
				t.Fatal("reverse created false hop coverage", count, err)
			}
			if got, want := observationTuples(t, recovered), observationTuples(t, target); !reflect.DeepEqual(got, want) {
				t.Fatal("raw paths, IDs, or fractional scores changed in reverse", got, want)
			}
			// The owner-controlled SQLite ingestor installs this exact schema.
			// Installing it must not inherit source coverage or imply old rows
			// have been rebuilt; its existing bounded backfill does that later.
			now := time.Now().Unix()
			if err := dbschema.EnsureObservationHopsSchema(recovered, nil); err != nil {
				t.Fatal(err)
			}
			var since int64
			if err := recovered.QueryRow(`SELECT since FROM observation_hops_coverage WHERE id=1`).Scan(&since); err != nil || since < now {
				t.Fatal("new installation inherited old coverage", since, err)
			}
			if err := recovered.QueryRow(`SELECT count(*) FROM observation_hops`).Scan(&count); err != nil || count != 0 {
				t.Fatal("unrebuilt cache unexpectedly populated", count, err)
			}
			if after, err := fingerprint(ctx, source); err != nil || after != before {
				t.Fatal("round trip changed original source", err)
			}
			if state == "partial" {
				report.ExcludedDerived = nil
				data, err := json.Marshal(report)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := target.Exec(`UPDATE corescope_import SET report=$1 WHERE id=1`, string(data)); err != nil {
					t.Fatal(err)
				}
				if _, err := importSQLite(ctx, o); err == nil {
					t.Fatal("finalized resume trusted changed exclusion report")
				}
			}
		})
	}
}
