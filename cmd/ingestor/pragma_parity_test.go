package main

import "testing"

// Durable settings are measured through the runtime connection, not logs.
func TestOpenStoreDurability(t *testing.T) {
	s := newTestStore(t)
	for _, setting := range []string{"fsync", "full_page_writes", "synchronous_commit"} {
		var got string
		if err := s.db.QueryRow("SHOW " + setting).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != "on" {
			t.Errorf("%s=%s; require on", setting, got)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO observations(transmission_id,timestamp) VALUES(999999,1)`); err == nil {
		t.Fatal("foreign key not enforced")
	}
}
