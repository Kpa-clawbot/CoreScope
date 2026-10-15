package dbschema

import (
	"strings"
	"testing"

	"github.com/meshcore-analyzer/pgutil"
)

func TestPostgresV1KeyBackfillUpgradeIsAtomicAndPreservesRows(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		name := "normal"
		if conflict {
			name = "unknown conflicting table"
		}
		t.Run(name, func(t *testing.T) {
			db, err := pgutil.Open(postgresSchema(t), false)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			oldSQL := strings.Replace(SchemaSQL, keyBackfillSchemaSQL, "", 1)
			if oldSQL == SchemaSQL {
				t.Fatal("v1 fixture did not remove v2 additions")
			}
			if _, err := db.Exec(oldSQL); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE corescope_schema(version INTEGER NOT NULL,kind TEXT PRIMARY KEY,ready BOOLEAN NOT NULL);
				INSERT INTO corescope_schema VALUES(1,'telemetry',true);
				INSERT INTO transmissions(id,raw_hex,hash,first_seen) VALUES(7,'00','retained','raw-date')`); err != nil {
				t.Fatal(err)
			}
			if err := AssertReady(db); err == nil {
				t.Fatal("v1 accepted before owner upgrade")
			}
			if conflict {
				if _, err := db.Exec(`CREATE TABLE key_backfill_state(private_extension TEXT)`); err != nil {
					t.Fatal(err)
				}
			}
			err = ApplyPostgres(db, nil)
			if conflict {
				if err == nil {
					t.Fatal("unknown conflicting table accepted")
				}
				var version, added int
				if err := db.QueryRow(`SELECT version FROM corescope_schema WHERE kind='telemetry'`).Scan(&version); err != nil || version != 1 {
					t.Fatal("failed upgrade changed marker", err)
				}
				if err := db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='tx_rewrite_feed'`).Scan(&added); err != nil || added != 0 {
					t.Fatal("failed upgrade left partial table", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := AssertReady(db); err != nil {
				t.Fatal(err)
			}
			if err := ApplyPostgres(db, nil); err != nil {
				t.Fatal("repeat upgrade", err)
			}
			var hash, date string
			if err := db.QueryRow(`SELECT hash,first_seen FROM transmissions WHERE id=7`).Scan(&hash, &date); err != nil || hash != "retained" || date != "raw-date" {
				t.Fatal("old row changed", err)
			}
		})
	}
}
