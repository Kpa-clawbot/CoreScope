package main

import "testing"

// Durable settings are measured through the runtime connection, not logs.
func TestOpenStoreDurability(t *testing.T) {
	s := newTestStore(t)
	settings := map[string]string{"SHOW fsync": "on", "SHOW full_page_writes": "on", "SHOW synchronous_commit": "on"}
	if s.Backend() == "sqlite" {
		settings = map[string]string{"PRAGMA journal_mode": "wal", "PRAGMA synchronous": "2", "PRAGMA foreign_keys": "1"}
	}
	for setting, want := range settings {
		var got string
		if err := s.db.QueryRow(setting).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s=%s; require on", setting, got)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO observations(transmission_id,timestamp) VALUES(999999,1)`); err == nil {
		t.Fatal("foreign key not enforced")
	}
}
