package users

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// A users.db written by a v5 binary gains the companion-linking schema and
// keeps its sessions, which become web sessions with no label or scopes.
func TestMigrateV5DatabaseToV6(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{`CREATE TABLE schema_version (version INTEGER NOT NULL)`, `INSERT INTO schema_version (version) VALUES (5)`}
	for _, m := range migrations[:5] {
		stmts = append(stmts, m...)
	}
	stmts = append(stmts,
		`INSERT INTO users (email, display_name, password_hash, created_at) VALUES ('old@example.org', 'Old', 'x', 1)`,
		`INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, expires_at, last_seen_at) VALUES ('h', 1, 'c', 1, 4102444800, 1)`)
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("build v5 db: %v", err)
		}
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open v5 db: %v", err)
	}
	defer st.Close()
	if len(migrations) < 6 {
		t.Fatal("migration v6 missing")
	}
	if v, err := st.SchemaVersion(); err != nil || v != len(migrations) {
		t.Fatalf("SchemaVersion = %d, %v; want %d", v, err, len(migrations))
	}

	var kind, label, scopes string
	if err := st.db.QueryRow(`SELECT kind, label, scopes FROM sessions WHERE token_hash = 'h'`).Scan(&kind, &label, &scopes); err != nil {
		t.Fatalf("v5 session lost: %v", err)
	}
	if kind != "web" || label != "" || scopes != "" {
		t.Fatalf("migrated session kind=%q label=%q scopes=%q", kind, label, scopes)
	}
	if _, err := st.db.Exec(`UPDATE sessions SET kind = 'api_key' WHERE token_hash = 'h'`); err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Fatalf("kind outside web/device accepted: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE sessions SET kind = 'device' WHERE token_hash = 'h'`); err != nil {
		t.Fatalf("kind device rejected: %v", err)
	}

	pk := strings.Repeat("ab", 32)
	if _, err := st.db.Exec(`INSERT INTO companion_links (pubkey, user_id, linked_at) VALUES (?, 1, 1)`, pk); err != nil {
		t.Fatalf("companion_links: %v", err)
	}
	var name string
	if err := st.db.QueryRow(`SELECT name FROM companion_links WHERE pubkey = ?`, pk).Scan(&name); err != nil || name != "" {
		t.Fatalf("companion_links default name = %q, %v", name, err)
	}
	if _, err := st.db.Exec(`INSERT INTO companion_links (pubkey, user_id, linked_at) VALUES (?, 99, 1)`, strings.Repeat("cd", 32)); err == nil {
		t.Fatal("companion_links accepted an unknown user (foreign key off?)")
	}
	if _, err := st.db.Exec(`INSERT INTO link_challenges (challenge_hash, user_id, pubkey, expires_at) VALUES ('x', 1, ?, 1)`, pk); err != nil {
		t.Fatalf("link_challenges: %v", err)
	}
	var idx int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'companion_links_user'`).Scan(&idx); err != nil || idx != 1 {
		t.Fatalf("index companion_links_user = %d, %v", idx, err)
	}
}
