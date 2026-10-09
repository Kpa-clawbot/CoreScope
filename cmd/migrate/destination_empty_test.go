package main

import (
	"context"
	"strings"
	"testing"
)

func TestFreshImportRefusesStandaloneSequence(t *testing.T) {
	dsn := postgresSchema(t)
	db := openImportDB(t, dsn)
	if _, err := db.Exec(`CREATE SEQUENCE retained_sequence START WITH 77`); err != nil {
		t.Fatal(err)
	}
	source := accountSource(t, 5)
	before, err := fingerprint(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: dsn, StateDir: t.TempDir(), Kind: "accounts"})
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("fresh import accepted a sequence-only occupied target: %v", err)
	}
	var value int
	var called bool
	if err := db.QueryRow(`SELECT last_value,is_called FROM retained_sequence`).Scan(&value, &called); err != nil || value != 77 || called {
		t.Fatal("fresh import refusal changed the existing sequence", err)
	}
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()`).Scan(&tables); err != nil || tables != 0 {
		t.Fatal("fresh import refusal created destination tables", err)
	}
	after, err := fingerprint(context.Background(), source)
	if err != nil || before != after {
		t.Fatal("fresh import refusal changed the retained source", err)
	}
}
