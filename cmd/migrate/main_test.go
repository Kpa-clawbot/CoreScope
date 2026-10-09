// Test that the migrate binary brings the e2e fixture DB up to the
// shape required by cmd/server's dbschema.AssertReady. Regression test
// for PR #1289 / fix for the CI "Server failed to start within 30s"
// failure: AssertReady fired against the unmigrated fixture and the
// server fatal-logged before opening its HTTP listener.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	postgresSchema "github.com/meshcore-analyzer/dbschema"
	dbschema "github.com/meshcore-analyzer/dbschema/legacy"
	"github.com/meshcore-analyzer/packetpath"
	"github.com/meshcore-analyzer/pgutil/pgtest"
)

// fixtureCandidates lists possible locations of the committed e2e
// fixture DB relative to this test's package directory. We resolve
// against runtime cwd which is cmd/migrate when `go test` runs.
var fixtureCandidates = []string{
	"../../test-fixtures/e2e-fixture.db",
}

func locateFixture(t *testing.T) string {
	t.Helper()
	for _, p := range fixtureCandidates {
		if _, err := os.Stat(p); err == nil {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}
	t.Fatalf("committed e2e fixture not found (looked in: %v)", fixtureCandidates)
	return ""
}

func TestCommittedFixtureRequiresExplicitDuplicateRepair(t *testing.T) {
	source := locateFixture(t)
	_, err := importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: pgtest.NewSchema(t), StateDir: t.TempDir(), Kind: "telemetry"})
	if err == nil || !strings.Contains(err.Error(), "duplicate observation identity") {
		t.Fatalf("raw legacy fixture must require explicit duplicate repair: %v", err)
	}
}

func TestPostgresImportCommittedFixture(t *testing.T) {
	original := locateFixture(t)
	before, err := sourceFingerprint(original)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "reviewed-fixture.sqlite")
	if err := snapshotSource(context.Background(), original, source); err != nil {
		t.Fatal(err)
	}
	legacyDB, err := sql.Open("sqlite3", sqliteURL(source, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	// This historical fixture has one duplicate identity group. Its documented
	// legacy repair is explicit and confined to this disposable copy; the runtime
	// importer itself rejects duplicates and never silently collapses records.
	if err := dbschema.Apply(legacyDB, t.Logf); err != nil {
		legacyDB.Close()
		t.Fatal(err)
	}
	var wantTransmissions, wantObservations int
	if err := legacyDB.QueryRow(`SELECT count(*) FROM transmissions`).Scan(&wantTransmissions); err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.QueryRow(`SELECT count(*) FROM observations`).Scan(&wantObservations); err != nil {
		t.Fatal(err)
	}
	legacyDB.Close()
	dsn := pgtest.NewSchema(t)
	report, err := importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "telemetry", BatchSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Verified {
		t.Fatal("committed fixture import was not verified")
	}
	if err := finalizeImport(context.Background(), dsn, "telemetry"); err != nil {
		t.Fatal(err)
	}
	db := openImportDB(t, dsn)
	if err := postgresSchema.AssertReady(db); err != nil {
		t.Fatal(err)
	}
	var transmissions, observations int
	if err := db.QueryRow(`SELECT count(*) FROM transmissions`).Scan(&transmissions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM observations`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if transmissions != wantTransmissions || observations != wantObservations || transmissions == 0 {
		t.Fatalf("reviewed fixture changed: %d/%d transmissions, %d/%d observations", transmissions, wantTransmissions, observations, wantObservations)
	}
	after, err := sourceFingerprint(original)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("committed fixture changed during import")
	}
}

func TestFixturePreparationToolKeepsOriginalAndRefusesOverwrite(t *testing.T) {
	source := locateFixture(t)
	before, err := sourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "prepared.sqlite")
	args := []string{"run", "../../scripts/prepare-postgres-fixture.go", "-source", source, "-destination", destination}
	output, err := exec.Command("go", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture tool: %v: %s", err, output)
	}
	if !strings.Contains(string(output), "observations:") || !strings.Contains(string(output), "original fixture unchanged") {
		t.Fatal("fixture tool did not report the repair counts")
	}
	if output, err := exec.Command("go", "run", "../../scripts/migrate-fixture-hashes.go", destination).CombinedOutput(); err != nil {
		t.Fatalf("prepared fixture hash normalization: %v: %s", err, output)
	}
	db, err := sql.Open("sqlite3", sqliteURL(destination, "ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := dbschema.CheckLegacySource(db); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("go", args...).Run(); err == nil {
		t.Fatal("fixture tool overwrote an existing destination")
	}
	after, err := sourceFingerprint(source)
	if err != nil || after != before {
		t.Fatal("fixture tool changed original source", err)
	}
}

func TestFixtureHashToolUsesSharedRuntimeIdentity(t *testing.T) {
	file := filepath.Join(t.TempDir(), "hashes.sqlite")
	db, err := sql.Open("sqlite3", file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE transmissions(id INTEGER PRIMARY KEY,raw_hex TEXT,hash TEXT UNIQUE)`); err != nil {
		t.Fatal(err)
	}
	raws := []string{"14010203040001020304", "150001020305", "160001020306", "17010203040001020307", "250001020304", "1d0001020304"}
	for i, raw := range raws {
		if _, err := db.Exec(`INSERT INTO transmissions VALUES(?,?,?)`, i, raw, fmt.Sprint("old-", i)); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	output, err := exec.Command("go", "run", "../../scripts/migrate-fixture-hashes.go", file).CombinedOutput()
	if err != nil {
		t.Fatalf("hash fixture: %v: %s", err, output)
	}
	db, err = sql.Open("sqlite3", sqliteURL(file, "ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, raw := range raws {
		var got string
		if err := db.QueryRow(`SELECT hash FROM transmissions WHERE id=?`, i).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if want := packetpath.ContentHash(raw); got != want {
			t.Fatalf("fixture route/type %s hash=%s, want runtime %s", raw, got, want)
		}
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("create dst: %v", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("copy: %v", err)
	}
}

// TestMigrateBringsFixtureToReady is the gate test for the CI bug.
// Before the fix landed, AssertReady against the committed fixture
// returned an error ("missing: inactive_nodes.foreign_advert" etc.).
// After Apply(), AssertReady must return nil.
func TestMigrateBringsFixtureToReady(t *testing.T) {
	src := locateFixture(t)
	dst := filepath.Join(t.TempDir(), "fixture-copy.db")
	copyFile(t, src, dst)

	db, err := sql.Open("sqlite3", dst)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Sanity: the committed fixture is missing at least one expected
	// migration column. If this stops being true, either someone
	// pre-migrated the fixture (and this test no longer protects #1289)
	// or AssertReady's required set changed.
	if err := dbschema.AssertReady(db); err == nil {
		t.Logf("note: fixture already passes AssertReady; skipping pre-condition assertion")
	}

	if err := dbschema.Apply(db, t.Logf); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := dbschema.AssertReady(db); err != nil {
		t.Fatalf("AssertReady after Apply: %v", err)
	}
}
