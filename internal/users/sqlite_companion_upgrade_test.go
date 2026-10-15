package users

import (
	"database/sql"
	"strings"
	"testing"
)

// Reproduce this PR's previously shipped identity-only v6, not upstream's
// companion-linking v6. Both declared the same version before this merge.
func identityV6Fixture(t *testing.T) *sql.DB {
	t.Helper()
	_, db := legacySQLiteFixture(t, 5, false)
	for _, table := range sqliteIdentities {
		body := table.body
		if table.name == "sessions" {
			body = body[:strings.Index(body, ", kind TEXT")] + ")"
		}
		rows, err := db.Query(`SELECT sql FROM sqlite_schema WHERE type='index' AND tbl_name=? AND sql IS NOT NULL`, table.name)
		if err != nil {
			t.Fatal(err)
		}
		var indexes []string
		for rows.Next() {
			var ddl string
			if err := rows.Scan(&ddl); err != nil {
				t.Fatal(err)
			}
			indexes = append(indexes, ddl)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		mustSQL(t, db, `DROP TABLE `+sqliteQuote(table.name), `CREATE TABLE `+sqliteQuote(table.name)+body)
		for _, ddl := range indexes {
			mustSQL(t, db, ddl)
		}
	}
	mustSQL(t, db, `UPDATE schema_version SET version=6`)
	return db
}

func TestSQLiteBothV6LayoutsUpgradeWithoutLosingSessions(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "upstream-companions"
		if native {
			name = "fork-identities"
		}
		t.Run(name, func(t *testing.T) {
			var db *sql.DB
			if native {
				db = identityV6Fixture(t)
			} else {
				_, db = legacySQLiteFixture(t, 6, false)
			}
			mustSQL(t, db,
				`INSERT INTO users(id,email,display_name,password_hash,created_at) VALUES(4000000000,'upgrade@example.invalid','Retained','hash',1)`,
				`INSERT INTO sessions(id,token_hash,user_id,csrf_token,created_at,expires_at,last_seen_at) VALUES(42,'session',4000000000,'csrf',1,9999999999,2)`)
			if native {
				mustSQL(t, db, `UPDATE sqlite_sequence SET seq=4000000100 WHERE name='users'`)
			} else {
				mustSQL(t, db,
					`UPDATE sessions SET kind='device',label='Radio',scopes='account:read' WHERE id=42`,
					`INSERT INTO companion_links VALUES('pubkey',4000000000,'Companion',3)`,
					`INSERT INTO link_challenges VALUES('challenge',4000000000,'pubkey',9999999999)`)
			}
			if err := ValidateSQLiteSourceLayout(db); err != nil {
				t.Fatal(err)
			}
			if err := ApplySQLite(db); err != nil {
				t.Fatal(err)
			}
			if err := AssertSQLiteReady(db); err != nil {
				t.Fatal(err)
			}
			var token, kind, label, scopes string
			var user int64
			if err := db.QueryRow(`SELECT token_hash,user_id,kind,label,scopes FROM sessions WHERE id=42`).Scan(&token, &user, &kind, &label, &scopes); err != nil {
				t.Fatal(err)
			}
			if token != "session" || user != 4000000000 {
				t.Fatal("session identity or token changed")
			}
			if native && (kind != "web" || label != "" || scopes != "") {
				t.Fatal("old web session was not preserved")
			}
			if !native && (kind != "device" || label != "Radio" || scopes != "account:read") {
				t.Fatal("device session fields changed")
			}
			var next int64
			if err := db.QueryRow(`INSERT INTO users(email,display_name,password_hash,created_at) VALUES('next@example.invalid','','hash',2) RETURNING id`).Scan(&next); err != nil {
				t.Fatal(err)
			}
			floor := int64(4000000000)
			if native {
				floor = 4000000100
			}
			if next != floor+1 {
				t.Fatalf("identity high water lost: got %d want %d", next, floor+1)
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
					t.Fatalf("%s count %d want %d", table, count, want)
				}
			}
			if err := ApplySQLite(db); err != nil {
				t.Fatal("repeated upgrade:", err)
			}
		})
	}
}

func TestSQLiteV6UnknownOrPartialLayoutsRefused(t *testing.T) {
	for _, alter := range []string{
		`ALTER TABLE sessions ADD COLUMN unexpected TEXT`,
		`CREATE TABLE companion_links(pubkey TEXT PRIMARY KEY)`,
		`CREATE TRIGGER unknown_session AFTER INSERT ON sessions BEGIN SELECT 1; END`,
	} {
		t.Run(alter, func(t *testing.T) {
			db := identityV6Fixture(t)
			mustSQL(t, db, alter)
			before := sqliteRows(t, db, `SELECT type,name,sql FROM sqlite_schema ORDER BY type,name`)
			if err := ApplySQLite(db); err == nil {
				t.Fatal("unknown schema accepted")
			}
			after := sqliteRows(t, db, `SELECT type,name,sql FROM sqlite_schema ORDER BY type,name`)
			if len(before) != len(after) {
				t.Fatal("rejected source changed")
			}
			for i := range before {
				for j := range before[i] {
					if before[i][j] != after[i][j] {
						t.Fatal("rejected schema changed")
					}
				}
			}
		})
	}
}
