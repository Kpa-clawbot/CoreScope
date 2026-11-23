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

func TestQueueAnchor(t *testing.T) {
	var nilDB *DB
	if got := nilDB.queueAnchor(); got != "" {
		t.Fatalf("nil DB: %q", got)
	}
	if got := (&DB{path: "data/meshcore.db"}).queueAnchor(); got != "data/meshcore.db" {
		t.Fatalf("SQLite: got %q, want the database path", got)
	}
	if got := (&DB{pg: true, queuePath: "data/meshcore.db"}).queueAnchor(); got != "data/meshcore.db" {
		t.Fatalf("Postgres: got %q, want the configured dbPath", got)
	}
}
