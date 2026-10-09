package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/pgutil/pgtest"
)

func TestPostgresExportReadsPathAndObservers(t *testing.T) {
	if backend := os.Getenv("CORESCOPE_TEST_BACKEND"); backend != "postgres" {
		if backend != "" && backend != "sqlite" {
			t.Fatal("CORESCOPE_TEST_BACKEND must be sqlite or postgres")
		}
		t.Skip("PostgreSQL matrix is not selected")
	}
	dsn := pgtest.NewSchema(t)
	writer, err := pgutil.Open(dsn, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	if err := dbschema.Apply(writer, t.Logf); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO transmissions(id,raw_hex,hash,first_seen,decoded_json) VALUES(1,'00','export-fixture','2026-01-01','{"path":{"hops":["ab","cd"]}}')`,
		`INSERT INTO observers(rowid,id,name) VALUES(10,'observer','Example observer')`,
		`INSERT INTO observations(transmission_id,observer_idx,snr,rssi,timestamp,path_json) VALUES(1,10,1.5,-90,1767225600,'["ab"]')`,
	} {
		if _, err := writer.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	reader, err := openExportDB(pgtest.ReadOnly(t, dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	assertExportMetadata(t, reader)
	if _, err := reader.Exec(`DELETE FROM observations`); err == nil {
		t.Fatal("PostgreSQL export reader permitted a write")
	}
	base := t.TempDir()
	readerURL := pgtest.ReadOnly(t, dsn)
	recordExportStorage(t, dbconfig.Storage{Backend: dbconfig.Postgres, StateDir: base, ReaderDatabaseURL: readerURL})
	selected, lease, err := openSelectedExport(dbconfig.StorageInputs{BaseDir: base, DBPath: filepath.Join(base, "stale.db"), Backend: dbconfig.SQLite, ReaderDatabaseURL: readerURL})
	if err != nil {
		t.Fatal(err)
	}
	assertExportMetadata(t, selected)
	selected.Close()
	lease.Close()
	if _, err := writer.Exec(`UPDATE corescope_schema SET ready=false`); err != nil {
		t.Fatal(err)
	}
	if db, lease, err := openSelectedExport(dbconfig.StorageInputs{BaseDir: base, StateDir: base, ReaderDatabaseURL: readerURL}); err == nil || db != nil || lease != nil {
		t.Fatal("export accepted an incomplete PostgreSQL import")
	}
}
