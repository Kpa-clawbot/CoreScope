package users

import "testing"

func TestMigrateToV9CreatesRepeaterTables(t *testing.T) {
	st, _ := newTestStore(t)
	if v, err := st.SchemaVersion(); err != nil || v < 9 || v != len(migrations) {
		t.Fatalf("SchemaVersion = %d, %v; want >= 9 and %d", v, err, len(migrations))
	}
	pk := "aa"
	if _, err := st.db.Exec(`INSERT INTO repeater_info (pubkey, status, updated_at) VALUES (?, 'planned', 1)`, pk); err == nil {
		t.Fatal("status 'planned' accepted, want CHECK violation")
	}
	if _, err := st.db.Exec(`INSERT INTO repeater_info (pubkey, power, updated_at) VALUES (?, 'wind', 1)`, pk); err == nil {
		t.Fatal("power 'wind' accepted, want CHECK violation")
	}
	if _, err := st.db.Exec(`INSERT INTO repeater_info (pubkey, status, power, updated_at) VALUES (?, 'active', 'solar', 1)`, pk); err != nil {
		t.Fatalf("valid repeater_info row rejected: %v", err)
	}
	if _, err := st.db.Exec(`INSERT INTO users (email, display_name, password_hash, created_at) VALUES ('a@example.org', 'Aa', 'x', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO repeater_claims (pubkey, user_id, claimed_at) VALUES (?, 1, 1)`, pk); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := st.db.Exec(`INSERT INTO repeater_claims (pubkey, user_id, claimed_at) VALUES (?, 1, 2)`, pk); err == nil {
		t.Fatal("duplicate claim accepted, want unique violation")
	}
}
