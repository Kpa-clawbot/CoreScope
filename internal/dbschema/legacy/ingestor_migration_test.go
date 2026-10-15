package legacy

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestNormalizeRejectsDestructiveLegacyCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Create a bare-bones DB with legacy bad data
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE IF NOT EXISTS transmissions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		raw_hex TEXT NOT NULL,
		hash TEXT NOT NULL,
		first_seen TEXT NOT NULL,
		route_type INTEGER,
		payload_type INTEGER,
		payload_version INTEGER,
		decoded_json TEXT,
		created_at TEXT DEFAULT (datetime('now')),
		channel_hash TEXT DEFAULT NULL
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS observations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
		observer_idx INTEGER,
		direction TEXT,
		snr REAL,
		rssi REAL,
		score INTEGER,
		path_json TEXT,
		timestamp INTEGER NOT NULL
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS _migrations (name TEXT PRIMARY KEY)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS nodes (public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL, last_seen TEXT, first_seen TEXT, advert_count INTEGER DEFAULT 0, battery_mv INTEGER, temperature_c REAL)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS observers (id TEXT PRIMARY KEY, name TEXT, iata TEXT, last_seen TEXT, first_seen TEXT, packet_count INTEGER DEFAULT 0, model TEXT, firmware TEXT, client_version TEXT, radio TEXT, battery_mv INTEGER, uptime_secs INTEGER, noise_floor REAL, inactive INTEGER DEFAULT 0, last_packet_at TEXT DEFAULT NULL)`)

	// Insert good transmission
	db.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1, 'aabb', 'abc123', '2024-01-01T00:00:00Z')`)
	db.Exec(`INSERT INTO observations (transmission_id, observer_idx, timestamp) VALUES (1, 1, 1704067200)`)

	// Insert bad: empty hash
	db.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (2, 'ccdd', '', '2024-01-01T00:00:00Z')`)
	db.Exec(`INSERT INTO observations (transmission_id, observer_idx, timestamp) VALUES (2, 1, 1704067200)`)

	// Insert bad: empty first_seen
	db.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (3, 'eeff', 'def456', '')`)
	db.Exec(`INSERT INTO observations (transmission_id, observer_idx, timestamp) VALUES (3, 2, 1704067200)`)

	defer db.Close()
	if err := Normalize(db, nil); err == nil {
		t.Fatal("lossy legacy cleanup accepted")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM transmissions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("source rows changed on refusal: %d", count)
	}
}
