package main

import (
	"testing"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/pgutil/pgtest"
)

func TestPostgresExportReadsPathAndObservers(t *testing.T) {
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
	path := getPathFromDB(reader, 1)
	if len(path) != 2 || path[0] != "ab" || path[1] != "cd" {
		t.Fatalf("PostgreSQL path query lost data: %v", path)
	}
	observers := getObservers(reader, 1)
	if len(observers) != 1 || observers[0].Name != "Example observer" || observers[0].SNR != 1.5 {
		t.Fatalf("PostgreSQL observer query lost data: %+v", observers)
	}
}
