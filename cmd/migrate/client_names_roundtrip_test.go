package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil"
)

func TestClientNodeNamesSwitchResumeAndRoundTrip(t *testing.T) {
	o, source := switchFixture(t)
	sqlite, err := openSQLite(source.Telemetry.SQLitePath, "rw")
	if err != nil {
		t.Fatal(err)
	}
	// This is the upstream SQLite layout, including the nullable GPS fields.
	// It must be accepted before normalizing the private conversion snapshot.
	_, err = sqlite.Exec(`CREATE TABLE IF NOT EXISTS client_node_names (
		id INTEGER PRIMARY KEY AUTOINCREMENT, target TEXT NOT NULL, rx_pubkey TEXT NOT NULL,
		observed_at TEXT NOT NULL, ingested_at TEXT NOT NULL, name TEXT NOT NULL,
		lat REAL, lon REAL, pos_acc_m REAL, repeater_clock INTEGER,
		UNIQUE(target, rx_pubkey, observed_at));
		INSERT INTO client_node_names VALUES(23,'target','companion','2026-01-01T00:00:00.123Z','buffered','Newest ✓',NULL,NULL,NULL,123);
		INSERT INTO client_node_names VALUES(99,'target','companion','deleted','deleted','Deleted',1.5,-2.5,3.5,NULL);
		DELETE FROM client_node_names WHERE id=99`)
	sqlite.Close()
	if err != nil {
		t.Fatal(err)
	}
	before, err := sourceFingerprint(source.Telemetry.SQLitePath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	interrupted := errors.New("interrupted after telemetry verification")
	o.afterStore = func(kind string) error {
		if kind == "telemetry" {
			return interrupted
		}
		return nil
	}
	if err := runStorageAction(ctx, o, &bytes.Buffer{}); !errors.Is(err, interrupted) {
		t.Fatalf("new canonical table must reach verified cutover: %v", err)
	}
	status, err := dbconfig.InspectSelection(o.SelectionFile)
	if err != nil || status.State != "pending" || status.Backend != dbconfig.SQLite {
		t.Fatal("interrupted switch changed selection", err)
	}
	o.Action, o.JobID, o.afterStore = "resume", status.JobID, nil
	owner := openImportDB(t, o.OwnerURL)
	if _, err := owner.Exec(`UPDATE client_node_names SET name='changed' WHERE id=23`); err != nil {
		t.Fatal(err)
	}
	if err := runStorageAction(ctx, o, &bytes.Buffer{}); err == nil {
		t.Fatal("resume ignored changed reported-name evidence")
	}
	if _, err := owner.Exec(`UPDATE client_node_names SET name='Newest ✓' WHERE id=23`); err != nil {
		t.Fatal(err)
	}
	if err := runStorageAction(ctx, o, &bytes.Buffer{}); err != nil {
		t.Fatal("verified resume", err)
	}
	reader, err := pgutil.Open(os.Getenv("CORESCOPE_READER_DATABASE_URL"), true)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := reader.QueryRow(`SELECT count(*) FROM client_node_names WHERE id=23 AND name='Newest ✓' AND lat IS NULL AND repeater_clock=123`).Scan(&count); err != nil || count != 1 {
		t.Fatal("reader lacks preserved reported names", err)
	}
	if _, err := reader.Exec(`DELETE FROM client_node_names`); err == nil {
		t.Fatal("telemetry reader can write reported names")
	}
	reader.Close()
	writer, err := pgutil.Open(os.Getenv("CORESCOPE_WRITER_DATABASE_URL"), false)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := writer.QueryRow(`INSERT INTO client_node_names(target,rx_pubkey,observed_at,ingested_at,name,lat,lon) VALUES('next','companion','new','new','Next',1.5,-2.5) RETURNING id`).Scan(&id); err != nil || id <= 99 {
		t.Fatal("writer grant or deleted identity high-water lost", id, err)
	}
	if _, err := writer.Exec(`ALTER TABLE client_node_names ADD COLUMN forbidden TEXT`); err == nil {
		t.Fatal("telemetry writer can change reported-name schema")
	}
	writer.Close()
	o.Action, o.Backend, o.JobID = "switch", "sqlite", ""
	o.SQLitePath = filepath.Join(t.TempDir(), "telemetry.sqlite")
	o.UsersSQLitePath = filepath.Join(t.TempDir(), "accounts.sqlite")
	if err := runStorageAction(ctx, o, &bytes.Buffer{}); err != nil {
		t.Fatal("reverse switch", err)
	}
	sqlite, err = openSQLite(o.SQLitePath, "rw")
	if err != nil {
		t.Fatal(err)
	}
	defer sqlite.Close()
	if err := sqlite.QueryRow(`SELECT count(*) FROM client_node_names WHERE (id=23 AND observed_at='2026-01-01T00:00:00.123Z' AND ingested_at='buffered' AND name='Newest ✓' AND lat IS NULL AND lon IS NULL AND pos_acc_m IS NULL AND repeater_clock=123) OR (target='next' AND lat=1.5 AND lon=-2.5 AND repeater_clock IS NULL)`).Scan(&count); err != nil || count != 2 {
		t.Fatal("round trip changed reported-name values", count, err)
	}
	var next int64
	if err := sqlite.QueryRow(`INSERT INTO client_node_names(target,rx_pubkey,observed_at,ingested_at,name) VALUES('after','companion','after','after','After') RETURNING id`).Scan(&next); err != nil || next <= id {
		t.Fatal("reverse switch reused reported-name identity", next, err)
	}
	if after, err := sourceFingerprint(source.Telemetry.SQLitePath); err != nil || before != after {
		t.Fatal("conversion changed original SQLite source", err)
	}
}

func TestRecordedPostgresClientNodeNamesUpgradeVersions(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			o, _ := recordedOldPostgres(t, true)
			db := openImportDB(t, o.OwnerURL)
			if version == 2 {
				if err := dbschema.ApplyPostgres(db, nil); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`DROP TABLE IF EXISTS client_node_names; UPDATE corescope_schema SET version=2 WHERE kind='telemetry'`); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(o.SelectionFile)
			if err != nil {
				t.Fatal(err)
			}
			if err := runStorageAction(context.Background(), o, &bytes.Buffer{}); err != nil {
				t.Fatal("recorded schema upgrade", err)
			}
			var got int
			if err := db.QueryRow(`SELECT version FROM corescope_schema WHERE kind='telemetry'`).Scan(&got); err != nil || got != 3 {
				t.Fatalf("recorded upgrade version=%d, want 3: %v", got, err)
			}
			if after, err := os.ReadFile(o.SelectionFile); err != nil || !bytes.Equal(before, after) {
				t.Fatal("upgrade changed recorded selection", err)
			}
			writer := openImportDB(t, os.Getenv("CORESCOPE_WRITER_DATABASE_URL"))
			if _, err := writer.Exec(`INSERT INTO client_node_names(target,rx_pubkey,observed_at,ingested_at,name) VALUES('target','companion','time','time','Retained')`); err != nil {
				t.Fatal("upgraded runtime writer lacks name grant", err)
			}
			var token string
			if err := openImportDB(t, o.UsersOwnerURL).QueryRow(`SELECT token_hash FROM sessions WHERE id=2`).Scan(&token); err != nil || !strings.EqualFold(token, "opaque session") {
				t.Fatal("upgrade changed initialized accounts", err)
			}
		})
	}
}
