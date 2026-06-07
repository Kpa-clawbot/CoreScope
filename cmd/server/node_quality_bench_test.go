package main

import (
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"
)

// benchQualityDB builds an in-memory DB with nObs observations whose path
// contains the "01FA" token, for benchmarking scanQualityRows.
func benchQualityDB(b *testing.B, nObs int) *DB {
	b.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	schema := []string{
		`CREATE TABLE transmissions (id INTEGER PRIMARY KEY, hash TEXT, first_seen TEXT, payload_type INTEGER, from_pubkey TEXT)`,
		`CREATE TABLE observers (id TEXT PRIMARY KEY, name TEXT)`,
		`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_idx INTEGER, snr REAL, path_json TEXT, timestamp INTEGER)`,
		`CREATE INDEX idx_obs_ts ON observations(timestamp)`,
	}
	for _, s := range schema {
		if _, err := conn.Exec(s); err != nil {
			b.Fatal(err)
		}
	}
	tx, _ := conn.Begin()
	tx.Exec(`INSERT INTO observers (id, name) VALUES ('OBS', 'o')`)
	for i := 0; i < nObs; i++ {
		tx.Exec(`INSERT INTO transmissions (id, hash, first_seen, payload_type, from_pubkey) VALUES (?,?,?,5,'')`,
			i, fmt.Sprintf("h%d", i), "2026-06-07T00:00:00Z")
		tx.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, snr, path_json, timestamp) VALUES (?,?,1,-7.0,?,?)`,
			i, i, `["AA","01FA","BB"]`, 1000)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	return &DB{conn: conn}
}

func BenchmarkNodeQualityScan(b *testing.B) {
	db := benchQualityDB(b, 5000)
	srv := &Server{db: db}
	tokens := map[string]bool{"01FA": true}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows := srv.scanQualityRows(tokens, 0)
		if len(rows) == 0 {
			b.Fatal("expected rows")
		}
	}
}
