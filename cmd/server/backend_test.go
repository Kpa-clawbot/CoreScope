package main

import "testing"

func TestSqlForPicksBackend(t *testing.T) {
	var nilDB *DB
	if got := nilDB.sqlFor("s", "p"); got != "s" {
		t.Fatalf("nil DB: got %q, want sqlite form", got)
	}
	if got := (&DB{}).sqlFor("s", "p"); got != "s" {
		t.Fatalf("SQLite DB: got %q, want sqlite form", got)
	}
	if got := (&DB{pg: true}).sqlFor("s", "p"); got != "p" {
		t.Fatalf("Postgres DB: got %q, want postgres form", got)
	}
}
