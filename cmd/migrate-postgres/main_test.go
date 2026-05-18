package main

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestValidateTransmissionHashesMatchesSamples(t *testing.T) {
	src := openTestDB(t)
	dst := openTestDB(t)
	insertTransmission(t, src, 1, "hash-one")
	insertTransmission(t, src, 2, "hash-two")
	insertTransmission(t, dst, 1, "hash-one")
	insertTransmission(t, dst, 2, "hash-two")

	if err := validateTransmissionHashes(src, dst, 1); err != nil {
		t.Fatalf("validateTransmissionHashes: %v", err)
	}
}

func TestValidateTransmissionHashesDetectsMismatch(t *testing.T) {
	src := openTestDB(t)
	dst := openTestDB(t)
	insertTransmission(t, src, 1, "hash-one")
	insertTransmission(t, dst, 1, "wrong")

	err := validateTransmissionHashes(src, dst, 1)
	if err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("expected hash mismatch, got %v", err)
	}
}

func TestCopyTableMapsLegacyTextObserverIdx(t *testing.T) {
	src := openObserverCopySource(t)
	dst := openObserverCopyDest(t)

	if _, err := src.Exec(`INSERT INTO observers (id) VALUES ('ABCDEF')`); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, timestamp) VALUES (10, 99, 'ABCDEF', 1234)`); err != nil {
		t.Fatal(err)
	}

	if _, err := copyTable(src, dst, tableSpec{name: "observers", columns: []string{"rowid", "id"}}); err != nil {
		t.Fatalf("copy observers: %v", err)
	}
	if _, err := copyTable(src, dst, tableSpec{name: "observations", columns: []string{"id", "transmission_id", "observer_idx", "timestamp"}}); err != nil {
		t.Fatalf("copy observations: %v", err)
	}

	var observerIdx int64
	if err := dst.QueryRow(`SELECT observer_idx FROM observations WHERE id = 10`).Scan(&observerIdx); err != nil {
		t.Fatal(err)
	}
	if observerIdx != 1 {
		t.Fatalf("observer_idx = %d, want copied observer rowid 1", observerIdx)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE transmissions (id INTEGER PRIMARY KEY, hash TEXT)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func openObserverCopySource(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE observers (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_idx INTEGER, timestamp INTEGER)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func openObserverCopyDest(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE observers (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_idx INTEGER, timestamp INTEGER)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertTransmission(t *testing.T, db *sql.DB, id int, hash string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO transmissions (id, hash) VALUES (?, ?)`, id, hash); err != nil {
		t.Fatal(err)
	}
}
