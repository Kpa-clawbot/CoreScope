package dbschema

import (
	"database/sql"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/pgutil/pgtest"
	"sync"
	"testing"
)

func TestPostgresImportInitializationNeverReady(t *testing.T) {
	db, err := pgutil.Open(pgtest.NewSchema(t), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := ApplyForImport(db, nil); err != nil {
		t.Fatal(err)
	}
	if err := AssertReady(db); err == nil {
		t.Fatal("empty import target accepted before verification")
	}
	if err := Apply(db, nil); err == nil {
		t.Fatal("normal initialization bypassed incomplete import")
	}
}

func TestPostgresConcurrentInitialization(t *testing.T) {
	db, err := pgutil.Open(pgtest.NewSchema(t), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Apply(db, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := AssertReady(db); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresIdentityRawValuesAndNullDedup(t *testing.T) {
	db, err := pgutil.Open(pgtest.NewSchema(t), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Apply(db, nil); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO observers(rowid,id) VALUES(0,'zero'),(77,'stable')`,
		`INSERT INTO transmissions(id,raw_hex,hash,first_seen,decoded_json) VALUES(0,'aa','zero','2026-01-01T01:02:03.123456Z','{malformed')`,
		`INSERT INTO observations(id,transmission_id,observer_idx,path_json,timestamp,score) VALUES(0,0,77,NULL,1,3.5),(50,0,NULL,NULL,2,NULL),(60,0,NULL,'',3,NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO observations(transmission_id,observer_idx,path_json,timestamp) VALUES(0,77,'',4)`); err == nil {
		t.Fatal("null/empty path identity diverged")
	}
	var score float64
	var raw, stamp string
	if err := db.QueryRow(`SELECT o.score,t.decoded_json,t.first_seen FROM observations o JOIN transmissions t ON t.id=o.transmission_id WHERE o.id=0`).Scan(&score, &raw, &stamp); err != nil {
		t.Fatal(err)
	}
	if score != 3.5 || raw != "{malformed" || stamp != "2026-01-01T01:02:03.123456Z" {
		t.Fatal("raw values changed")
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := ReseedIdentities(tx, map[string]int64{"observations.id": 500}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`INSERT INTO observations(transmission_id,timestamp) VALUES(0,5) RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 501 {
		t.Fatalf("deleted identity high-water lost: %d", id)
	}
}

func TestPostgresWriterRejectsOwnerAndReader(t *testing.T) {
	dsn := pgtest.NewSchema(t)
	owner, err := pgutil.Open(dsn, false)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := Apply(owner, nil); err != nil {
		t.Fatal(err)
	}
	if err := AssertWriter(owner); err == nil {
		t.Fatal("owner accepted as runtime writer")
	}
	reader, err := pgutil.Open(pgtest.ReadOnly(t, dsn), true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := AssertReady(reader); err != nil {
		t.Fatal(err)
	}
	if err := AssertWriter(reader); err == nil {
		t.Fatal("SELECT-only role accepted as writer")
	}
	for _, q := range []string{`INSERT INTO nodes(public_key) VALUES('x')`, `UPDATE nodes SET name='x'`, `DELETE FROM nodes`, `CREATE TABLE forbidden(id int)`} {
		if _, err := reader.Exec(q); err == nil {
			t.Fatalf("reader permitted %s", q)
		}
	}
}

func TestPostgresInitializationRejectsUnversionedData(t *testing.T) {
	db, err := pgutil.Open(pgtest.NewSchema(t), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE existing(id int)`); err != nil {
		t.Fatal(err)
	}
	if err := Apply(db, nil); err == nil {
		t.Fatal("unversioned nonempty schema accepted")
	}
	var marker sql.NullString
	if err := db.QueryRow(`SELECT to_regclass('corescope_schema')::text`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker.Valid {
		t.Fatal("failed initialization left readiness metadata behind")
	}
}

func TestPostgresSchemaFreshRepeatedAndReadiness(t *testing.T) {
	dsn := pgtest.NewSchema(t)
	db, err := pgutil.Open(dsn, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	apply := Apply
	for i := 0; i < 2; i++ {
		if err := apply(db, nil); err != nil {
			t.Fatalf("initialize PostgreSQL schema: %v", err)
		}
	}
	if err := AssertReady(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE corescope_schema SET ready=false`); err != nil {
		t.Fatal(err)
	}
	if err := AssertReady(db); err == nil {
		t.Fatal("incomplete import accepted")
	}
	if _, err := db.Exec(`UPDATE corescope_schema SET ready=true,version=999`); err != nil {
		t.Fatal(err)
	}
	if err := AssertReady(db); err == nil {
		t.Fatal("newer schema accepted")
	}
}
