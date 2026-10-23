package dbschema

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/meshcore-analyzer/pgutil"
)

func TestClientNodeNamesSQLiteSchema(t *testing.T) {
	db, _ := sqliteTestDB(t)
	if err := ApplySQLite(db, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='client_node_names'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("canonical SQLite schema must include reported names: count=%d err=%v", count, err)
	}
}

func TestPostgresClientNodeNamesFreshAndUpgrade(t *testing.T) {
	for _, version := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("from_v%d", version), func(t *testing.T) {
			db, err := pgutil.Open(postgresSchema(t), false)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if version > 0 {
				if err := ApplyPostgres(db, nil); err != nil {
					t.Fatal(err)
				}
				for _, query := range []string{
					`DROP TABLE IF EXISTS client_node_names`,
					`INSERT INTO transmissions(id,raw_hex,hash,first_seen) VALUES(7,'00','retained','raw-date')`,
					`SELECT setval(pg_get_serial_sequence('transmissions','id'),900,true)`,
				} {
					if _, err := db.Exec(query); err != nil {
						t.Fatal(err)
					}
				}
				if version == 1 {
					if _, err := db.Exec(`DROP TABLE tx_rewrite_feed,key_backfill_state,key_backfill_done`); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := db.Exec(`UPDATE corescope_schema SET version=$1 WHERE kind='telemetry'`, version); err != nil {
					t.Fatal(err)
				}
			}
			if err := ApplyPostgres(db, nil); err != nil {
				t.Fatal("owner upgrade", err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='client_node_names'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("schema v3 must include reported names: count=%d err=%v", count, err)
			}
			if err := AssertReady(db); err != nil {
				t.Fatal(err)
			}
			if err := ApplyPostgres(db, nil); err != nil {
				t.Fatal("idempotent upgrade", err)
			}
			if version > 0 {
				var hash, stamp string
				var high int64
				if err := db.QueryRow(`SELECT hash,first_seen FROM transmissions WHERE id=7`).Scan(&hash, &stamp); err != nil || hash != "retained" || stamp != "raw-date" {
					t.Fatal("upgrade changed telemetry", err)
				}
				if err := db.QueryRow(`SELECT last_value FROM transmissions_id_seq`).Scan(&high); err != nil || high != 900 {
					t.Fatal("upgrade changed identity", err)
				}
			}
			var id int64
			if err := db.QueryRow(`INSERT INTO client_node_names(target,rx_pubkey,observed_at,ingested_at,name) VALUES('target','companion','time','time','Reported') RETURNING id`).Scan(&id); err != nil || id < 1 {
				t.Fatal("reported-name identity", err)
			}
			var lat sql.NullFloat64
			if err := db.QueryRow(`SELECT lat FROM client_node_names WHERE id=$1`, id).Scan(&lat); err != nil || lat.Valid {
				t.Fatal("optional position changed", err)
			}
		})
	}
}

func TestPostgresClientNodeNamesConflictingUpgradeRollsBack(t *testing.T) {
	db, err := pgutil.Open(postgresSchema(t), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := ApplyPostgres(db, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE IF EXISTS client_node_names;
		CREATE TABLE client_node_names(keep TEXT);
		INSERT INTO client_node_names VALUES('private extension');
		UPDATE corescope_schema SET version=2 WHERE kind='telemetry'`); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPostgres(db, nil); err == nil {
		t.Fatal("v2 upgrade accepted a conflicting reported-name table")
	}
	var version int
	var keep string
	if err := db.QueryRow(`SELECT version FROM corescope_schema WHERE kind='telemetry'`).Scan(&version); err != nil || version != 2 {
		t.Fatal("failed upgrade changed readiness version", err)
	}
	if err := db.QueryRow(`SELECT keep FROM client_node_names`).Scan(&keep); err != nil || keep != "private extension" {
		t.Fatal("failed upgrade changed existing table", err)
	}
}
