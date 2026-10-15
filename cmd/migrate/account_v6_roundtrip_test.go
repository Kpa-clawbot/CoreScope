package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/users"
)

func TestBothSQLiteV6AccountLayoutsRoundTrip(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "upstream-companion-v6"
		if native {
			name = "fork-identity-v6"
		}
		t.Run(name, func(t *testing.T) {
			source := accountSource(t, 6)
			db, err := openSQLite(source, "rw")
			if err != nil {
				t.Fatal(err)
			}
			if native {
				if err := users.ApplySQLite(db); err != nil {
					db.Close()
					t.Fatal(err)
				}
				// Reconstruct the previous native v6 exact shape: identity tables,
				// no companion columns/tables. No source is weakened or guessed.
				for _, q := range []string{
					`DROP TABLE companion_links`, `DROP TABLE link_challenges`,
					`ALTER TABLE sessions DROP COLUMN kind`, `ALTER TABLE sessions DROP COLUMN label`, `ALTER TABLE sessions DROP COLUMN scopes`,
					`UPDATE schema_version SET version=6`,
					`UPDATE sqlite_sequence SET seq=10000 WHERE name='users'`,
				} {
					if _, err := db.Exec(q); err != nil {
						db.Close()
						t.Fatal(err)
					}
				}
			} else {
				for _, q := range []string{
					`UPDATE sessions SET kind='device',label='Radio',scopes='rx,settings' WHERE id=90`,
					`INSERT INTO companion_links(pubkey,user_id,name,linked_at) VALUES('fixture-key',0,'Companion',1)`,
					`INSERT INTO link_challenges(challenge_hash,user_id,pubkey,expires_at) VALUES('fixture-challenge',0,'fixture-key',9999999999)`,
				} {
					if _, err := db.Exec(q); err != nil {
						db.Close()
						t.Fatal(err)
					}
				}
			}
			if err := users.ValidateSQLiteSourceLayout(db); err != nil {
				db.Close()
				t.Fatal(err)
			}
			db.Close()
			before, err := sourceFingerprint(source)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			dsn := postgresDatabase(t)
			if _, err := importSQLite(ctx, importOptions{Source: source, DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "accounts", BatchSize: 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := finalizeImport(ctx, dsn, "accounts"); err != nil {
				t.Fatal(err)
			}
			after, err := sourceFingerprint(source)
			if err != nil || before != after {
				t.Fatal("source changed", err)
			}
			dest := filepath.Join(t.TempDir(), "accounts.sqlite")
			if report, err := reversePostgres(ctx, reverseOptions{DatabaseURL: dsn, Destination: dest, StateDir: t.TempDir(), Kind: "accounts", BatchSize: 1}); err != nil || !report.Verified {
				t.Fatal("reverse", err)
			}
			db, err = openSQLite(dest, "rw")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := users.AssertSQLiteReady(db); err != nil {
				t.Fatal(err)
			}
			var token, kind, label, scopes string
			if err := db.QueryRow(`SELECT token_hash,kind,label,scopes FROM sessions WHERE id=90`).Scan(&token, &kind, &label, &scopes); err != nil {
				t.Fatal(err)
			}
			if token != "preserved-token" {
				t.Fatal("session token changed")
			}
			if native && (kind != "web" || label != "" || scopes != "") {
				t.Fatal("native v6 session changed")
			}
			if !native && (kind != "device" || label != "Radio" || scopes != "rx,settings") {
				t.Fatal("companion v6 device session changed")
			}
			for _, table := range []string{"companion_links", "link_challenges"} {
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
					t.Fatal(err)
				}
				want := 1
				if native {
					want = 0
				}
				if count != want {
					t.Fatalf("%s rows=%d want=%d", table, count, want)
				}
			}
			if native {
				var id int64
				if err := db.QueryRow(`INSERT INTO users(email,display_name,password_hash,created_at) VALUES('after@example.invalid','','hash',2) RETURNING id`).Scan(&id); err != nil || id <= 10000 {
					t.Fatal("identity high water changed", id, err)
				}
			}
		})
	}
}
