package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

// Review (#2261): every load path must feed certainHeard. A nil prefix map
// at any of the call sites in Load, loadChunk, scanAndMergeChunk, live
// ingest or late observations would leave the unit tests green; these go
// through the real paths and assert LastHeardMap.

const lpRelay = "4f2a" + "5555555555555555555555555555555555555555555555555555555555555"

// lpFixture is one flood transmission whose single 2-byte hop is lpRelay,
// a registered repeater, observed a minute ago. withResolved keeps the
// observation's resolved_path (the ingestor resolved the hop); otherwise it
// is NULL and the server re-resolves from path_json. hopInFirstObs false
// leaves the first observation without a path (for the late-observation
// case).
func lpFixture(t *testing.T, withResolved, hopInFirstObs bool) (*DB, *sql.DB, string) {
	t.Helper()
	path := createTestDBWithResolvedPath(t, 1, []string{lpRelay})
	w, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	at := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	raw := "1541" + lpRelay[:4] + "00" // GRP_TXT, flood, path byte: 2-byte hashes x1
	pathJSON, resolved := `["`+strings.ToUpper(lpRelay[:4])+`"]`, interface{}(`["`+lpRelay+`"]`)
	if !withResolved {
		resolved = nil
	}
	if !hopInFirstObs {
		pathJSON, resolved = `[]`, nil
	}
	for _, q := range []struct {
		sql  string
		args []interface{}
	}{
		{`ALTER TABLE transmissions ADD COLUMN from_pubkey TEXT`, nil},
		{`UPDATE transmissions SET raw_hex=?, route_type=1, payload_type=5, first_seen=?`, []interface{}{raw, at}},
		{`UPDATE observations SET raw_hex=?, timestamp=?, path_json=?, resolved_path=?`, []interface{}{raw, at, pathJSON, resolved}},
		{`INSERT INTO nodes (public_key, name, role) VALUES (?, 'Relay', 'repeater')`, []interface{}{lpRelay}},
	} {
		if _, err := w.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.conn.Close() })
	return db, w, at
}

func TestCertainHeardFedByEveryLoadPath(t *testing.T) {
	type mode struct {
		name                        string
		withResolved, hopInFirstObs bool
		run                         func(t *testing.T, s *PacketStore, db *DB, w *sql.DB, at string)
	}
	load := func(t *testing.T, s *PacketStore, _ *DB, _ *sql.DB, _ string) {
		if err := s.Load(); err != nil {
			t.Fatal(err)
		}
	}
	chunk := func(t *testing.T, s *PacketStore, _ *DB, _ *sql.DB, _ string) {
		if err := s.loadChunk(time.Now().Add(-time.Hour), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	chunked := func(t *testing.T, s *PacketStore, _ *DB, _ *sql.DB, _ string) {
		if err := s.LoadChunked(1); err != nil {
			t.Fatal(err)
		}
	}
	live := func(t *testing.T, s *PacketStore, _ *DB, _ *sql.DB, _ string) {
		s.IngestNewFromDB(0, 100)
	}
	late := func(t *testing.T, s *PacketStore, db *DB, w *sql.DB, at string) {
		if err := s.Load(); err != nil {
			t.Fatal(err)
		}
		if heard, _ := s.LastHeardMap([]string{lpRelay}); len(heard) != 0 {
			t.Fatal("setup: the first observation carries no hop")
		}
		maxObs := db.GetMaxObservationID()
		if _, err := w.Exec(`INSERT INTO observations (id, transmission_id, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp, raw_hex, resolved_path)
			VALUES (2, 1, 'obs2', 'Obs2', 'RX', -9, -79, 5, ?, ?, ?, NULL)`, `["`+strings.ToUpper(lpRelay[:4])+`"]`, at, "1541"+lpRelay[:4]+"00"); err != nil {
			t.Fatal(err)
		}
		s.IngestNewObservations(maxObs, 100)
	}
	for _, m := range []mode{
		{"Load/resolved_path", true, true, load},
		{"Load/path_json", false, true, load},
		{"loadChunk/resolved_path", true, true, chunk},
		{"loadChunk/path_json", false, true, chunk},
		{"LoadChunked/resolved_path", true, true, chunked},
		{"LoadChunked/path_json", false, true, chunked},
		{"IngestNewFromDB", false, true, live},
		{"IngestNewObservations", false, false, late},
	} {
		t.Run(m.name, func(t *testing.T) {
			db, w, at := lpFixture(t, m.withResolved, m.hopInFirstObs)
			s := NewPacketStore(db, &PacketStoreConfig{})
			s.useResolvedPathIndex = true
			s.initResolvedPathIndex()
			m.run(t, s, db, w, at)
			heard, _ := s.LastHeardMap([]string{lpRelay})
			want, _ := time.Parse(time.RFC3339, at)
			if !heard[lpRelay].Equal(want) {
				t.Fatalf("LastHeardMap[relay] = %v; want %v: this path does not feed certainHeard", heard[lpRelay], want)
			}
			assertCertainRelay(t, s, lpRelay, at)
		})
	}
}
