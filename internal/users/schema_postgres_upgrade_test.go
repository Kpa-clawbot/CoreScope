package users

import "testing"

func TestPostgresAccountsV5UpgradeKeepsAuthentication(t *testing.T) {
	requirePostgres(t)
	db := testOwner(t, testTarget(t, false))
	for _, migration := range migrations[:5] {
		for _, q := range migration {
			if _, err := db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, q := range []string{
		`CREATE TABLE schema_version(version INTEGER NOT NULL); INSERT INTO schema_version VALUES(5)`,
		`CREATE TABLE corescope_schema(version INTEGER NOT NULL,kind TEXT PRIMARY KEY,ready BOOLEAN NOT NULL); INSERT INTO corescope_schema VALUES(1,'accounts',true)`,
		`INSERT INTO users(id,email,display_name,password_hash,created_at) VALUES(4000000000,'upgrade@example.invalid','Retained','opaque-hash',1)`,
		`INSERT INTO sessions(id,token_hash,user_id,csrf_token,created_at,expires_at,last_seen_at) VALUES(42,'opaque-session',4000000000,'opaque-csrf',1,9999999999,2)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := AssertPostgresReady(db); err == nil {
		t.Fatal("v5 accepted before owner upgrade")
	}
	if err := ApplyPostgres(db); err != nil {
		t.Fatal(err)
	}
	if err := AssertPostgresReady(db); err != nil {
		t.Fatal(err)
	}
	var token, csrf, kind, label, scopes string
	var user int64
	if err := db.QueryRow(`SELECT token_hash,csrf_token,user_id,kind,label,scopes FROM sessions WHERE id=42`).Scan(&token, &csrf, &user, &kind, &label, &scopes); err != nil {
		t.Fatal(err)
	}
	if token != "opaque-session" || csrf != "opaque-csrf" || user != 4000000000 || kind != "web" || label != "" || scopes != "" {
		t.Fatal("existing web authentication changed")
	}
	if _, err := db.Exec(`INSERT INTO companion_links(pubkey,user_id,linked_at) VALUES('fixture-key',4000000000,9999999999)`); err != nil {
		t.Fatal("new account references narrowed to 32 bits", err)
	}
	if err := ApplyPostgres(db); err != nil {
		t.Fatal("repeated owner upgrade", err)
	}
}
