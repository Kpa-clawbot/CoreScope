package main

import (
	"context"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/users"
	"path/filepath"
	"testing"
)

func TestNewSQLiteIdentitySourceLayouts(t *testing.T) {
	for _, kind := range []string{"telemetry", "accounts"} {
		t.Run(kind, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "identity # % é.sqlite")
			db, err := openSQLite(source, "rwc")
			if err != nil {
				t.Fatal(err)
			}
			if kind == "telemetry" {
				err = dbschema.ApplySQLite(db, nil)
			} else {
				err = users.ApplySQLite(db)
			}
			if err != nil {
				db.Close()
				t.Fatal(err)
			}
			if kind == "accounts" {
				for _, q := range []string{
					`INSERT INTO users(id,email,display_name,password_hash,created_at) VALUES(5,'test@example.invalid','','hash',1)`,
					`INSERT INTO mail_log(id,user_id,to_email,purpose,sent_at,last_event_at) VALUES(4,5,'test@example.invalid','test',1,1)`,
					`INSERT INTO mail_events(id,mail_id,event,at) VALUES(-9,4,'zero',1),(0,4,'negative',2),(900,4,'delivered',3)`,
					`UPDATE sqlite_sequence SET seq=9000 WHERE name='mail_events'`,
				} {
					if _, err := db.Exec(q); err != nil {
						t.Fatal(err)
					}
				}
			}
			db.Close()
			before, err := sourceFingerprint(source)
			if err != nil {
				t.Fatal(err)
			}
			tables, err := layouts(kind)
			if err != nil {
				t.Fatal(err)
			}
			_, normalized, err := prepareSource(context.Background(), importOptions{Source: source, StateDir: t.TempDir(), Kind: kind, targetDigest: "test-only"}, tables, true)
			if err != nil {
				t.Fatal(err)
			}
			after, err := sourceFingerprint(source)
			if err != nil || after != before {
				t.Fatal("source changed", err)
			}
			copyDB, err := openSQLite(normalized, "ro")
			if err != nil {
				t.Fatal(err)
			}
			defer copyDB.Close()
			if kind == "accounts" {
				var n, seq, version int
				if err := copyDB.QueryRow(`SELECT count(*) FROM mail_events WHERE id IN (-9,0,900)`).Scan(&n); err != nil || n != 3 {
					t.Fatal("explicit event IDs changed", n, err)
				}
				if err := copyDB.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='mail_events'`).Scan(&seq); err != nil || seq != 9000 {
					t.Fatal("event sequence changed", seq, err)
				}
				if err := copyDB.QueryRow(`SELECT version FROM schema_version`).Scan(&version); err != nil || version != 6 {
					t.Fatal("SQLite source version changed", version, err)
				}
			}
		})
	}
}

func TestForwardNewSQLiteAccountIDsAndSequence(t *testing.T) {
	dsn := postgresSchema(t)
	source := accountSource(t, 5)
	db, err := openSQLite(source, "rw")
	if err != nil {
		t.Fatal(err)
	}
	if err := users.ApplySQLite(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sqlite_sequence SET seq=9000 WHERE name='mail_events'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	report, err := importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "accounts"})
	if err != nil || !report.Verified {
		t.Fatal("forward v6", err)
	}
	pg := openImportDB(t, dsn)
	var id, version int
	if err := pg.QueryRow(`SELECT id FROM mail_events WHERE mail_id=4`).Scan(&id); err != nil || id != 900 {
		t.Fatal("mail event identity", id, err)
	}
	if err := pg.QueryRow(`SELECT version FROM schema_version`).Scan(&version); err != nil || version != 5 {
		t.Fatal("PG version", version, err)
	}
	if err := pg.QueryRow(`INSERT INTO mail_events(mail_id,event,at) VALUES(4,'later',44) RETURNING id`).Scan(&id); err != nil || id != 9001 {
		t.Fatal("mail sequence", id, err)
	}
}
