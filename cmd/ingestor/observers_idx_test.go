package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// observations.observer_idx references observers.idx, an explicit INTEGER
// PRIMARY KEY. Joining or looking up observers by the hidden implicit rowid
// instead is what dump-and-reload silently renumbers (and Postgres lacks), so
// no non-test ingestor or dbschema source may do it.
func TestNoObserversRowidInSource(t *testing.T) {
	forbidden := regexp.MustCompile(`(?i)\.rowid\b|rowid\s+FROM\s+observers\b|observers\.rowid\b`)
	var checked int
	for _, dir := range []string{".", filepath.Join("..", "..", "internal", "dbschema")} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			checked++
			for i, line := range strings.Split(string(src), "\n") {
				if forbidden.MatchString(line) {
					t.Errorf("%s:%d references the implicit observers rowid; use observers.idx: %s", f, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
	if checked < 10 {
		t.Fatalf("source guard scanned only %d files — wrong working directory?", checked)
	}
}

type columnInfo struct {
	Name, Type  string
	NotNull, PK int
	Default     sql.NullString
}

func observersShape(t *testing.T, db *sql.DB) ([]columnInfo, []string) {
	t.Helper()
	rows, err := db.Query(`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info('observers') ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	var cols []columnInfo
	for rows.Next() {
		var c columnInfo
		if err := rows.Scan(&c.Name, &c.Type, &c.NotNull, &c.Default, &c.PK); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	rows.Close()
	var idx []string
	irows, err := db.Query(`SELECT name || ':' || COALESCE(sql, '') FROM sqlite_master WHERE type = 'index' AND tbl_name = 'observers' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for irows.Next() {
		var s string
		if err := irows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		idx = append(idx, s)
	}
	irows.Close()
	return cols, idx
}

// A fresh install (applySchema's CREATE TABLE) and an upgraded legacy DB
// (dbschema's rebuild) must end up with the identical observers table, and an
// upgraded DB must keep each observation linked to its observer through the
// ingestor's own lookup path.
func TestFreshObserversMatchesRebuiltShape(t *testing.T) {
	fresh, err := OpenStore(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("OpenStore fresh: %v", err)
	}
	defer fresh.Close()

	legacyPath := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite3", legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`CREATE TABLE observers (id TEXT PRIMARY KEY, name TEXT, iata TEXT, last_seen TEXT, first_seen TEXT, packet_count INTEGER DEFAULT 0, model TEXT, firmware TEXT, client_version TEXT, radio TEXT, battery_mv INTEGER, uptime_secs INTEGER, noise_floor REAL, inactive INTEGER DEFAULT 0, last_packet_at TEXT DEFAULT NULL)`,
		`INSERT INTO observers (id, name) VALUES ('gone', 'G'), ('keep-a', 'A'), ('keep-b', 'B')`,
		`DELETE FROM observers WHERE id = 'gone'`, // rowid 1 freed: a dump-and-reload would renumber the rest
	} {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	raw.Close()

	legacy, err := OpenStore(legacyPath)
	if err != nil {
		t.Fatalf("OpenStore legacy: %v", err)
	}
	defer legacy.Close()

	freshCols, freshIdx := observersShape(t, fresh.db)
	legacyCols, legacyIdx := observersShape(t, legacy.db)
	if !reflect.DeepEqual(freshCols, legacyCols) {
		t.Errorf("observers columns differ:\nfresh   =%+v\nrebuilt =%+v", freshCols, legacyCols)
	}
	if !reflect.DeepEqual(freshIdx, legacyIdx) {
		t.Errorf("observers indexes differ:\nfresh   =%v\nrebuilt =%v", freshIdx, legacyIdx)
	}

	for want, id := range map[int64]string{2: "keep-a", 3: "keep-b"} {
		var got int64
		if err := legacy.stmtGetObserverIdx.QueryRow(id).Scan(&got); err != nil || got != want {
			t.Errorf("idx of %s = %d (%v), want its legacy rowid %d", id, got, err, want)
		}
	}
	var name string
	if err := legacy.db.QueryRow(`SELECT name FROM observers WHERE idx = 3`).Scan(&name); err != nil || name != "B" {
		t.Errorf("observer at idx 3 = %q (%v), want B", name, err)
	}
}
