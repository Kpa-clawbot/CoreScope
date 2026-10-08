package main

import (
	"database/sql"
	"testing"
	"time"
)

// txRewriteFixture is the ingestor's durable output for one GRP_TXT stored
// before its channel key was known: still encrypted, scope unmatched. The
// returned writer plays the ingestor's key backfill.
func txRewriteFixture(t *testing.T, withFeed bool) (*DB, *sql.DB) {
	t.Helper()
	path := createTestDBWithResolvedPath(t, 1, []string{"fixture-relay"})
	w, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	stmts := []string{
		`ALTER TABLE transmissions ADD COLUMN scope_name TEXT`,
		`ALTER TABLE transmissions ADD COLUMN channel_hash TEXT`,
	}
	if withFeed {
		stmts = append(stmts, txRewriteFeedDDL)
	}
	for _, stmt := range stmts {
		if _, err := w.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if _, err := w.Exec(`UPDATE transmissions SET payload_type=5, route_type=0, first_seen=?, scope_name='',
		channel_hash='enc_AB', decoded_json='{"type":"GRP_TXT","channelHashHex":"AB"}'; UPDATE observations SET timestamp=?`, now, now); err != nil {
		t.Fatal(err)
	}
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.conn.Close() })
	return db, w
}

// Mirrors internal/dbschema/key_backfill.go; the server never creates it.
const txRewriteFeedDDL = `CREATE TABLE tx_rewrite_feed (id INTEGER PRIMARY KEY AUTOINCREMENT, tx_id INTEGER NOT NULL, created_at INTEGER NOT NULL DEFAULT 0)`

// backfillRewrite does what the ingestor's commit does: rewrite the row and
// append its feed entry in one transaction.
func backfillRewrite(t *testing.T, w *sql.DB, decoded, scope string) {
	t.Helper()
	if _, err := w.Exec(`BEGIN; UPDATE transmissions SET decoded_json=?, channel_hash='#saar', scope_name=? WHERE id=1;
		INSERT INTO tx_rewrite_feed(tx_id) VALUES(1); COMMIT`, decoded, scope); err != nil {
		t.Fatal(err)
	}
}

func TestTxRewriteFeedReachesStoreWithoutRestart(t *testing.T) {
	db, w := txRewriteFixture(t, true)
	s := NewPacketStore(db, &PacketStoreConfig{})
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	tx := s.byTxID[1]
	if tx == nil {
		t.Fatal("fixture transmission not loaded")
	}
	// Parse once before the rewrite: the cached map must not survive it.
	if got := tx.ParsedDecoded()["type"]; got != "GRP_TXT" {
		t.Fatalf("fixture decoded type %v", got)
	}

	backfillRewrite(t, w, `{"type":"CHAN","channel":"#saar","text":"Alice: moin"}`, "#saar")
	s.IngestNewObservations(0, 100) // the server's normal poll path

	if got := tx.ParsedDecoded()["channel"]; got != "#saar" {
		t.Fatalf("in-memory decoded still stale after poll: channel=%v json=%s", got, tx.DecodedJSON)
	}
	if tx.ScopeName == nil || *tx.ScopeName != "#saar" {
		t.Fatalf("in-memory scope_name not refreshed: %v", tx.ScopeName)
	}
	if s.txRewriteCursor != 1 {
		t.Fatalf("cursor=%d, want 1", s.txRewriteCursor)
	}
}

// Rows committed before Load are already in what Load read; the watermark
// keeps them from being re-applied (harmless, but wasted work at scale).
func TestTxRewriteFeedCursorStartsAtLoadWatermark(t *testing.T) {
	db, w := txRewriteFixture(t, true)
	backfillRewrite(t, w, `{"type":"CHAN","channel":"#saar"}`, "#saar")
	s := NewPacketStore(db, &PacketStoreConfig{})
	if s.txRewriteCursor != 1 {
		t.Fatalf("cursor=%d before Load, want the feed's MAX(id)=1", s.txRewriteCursor)
	}
}

// An older ingestor never creates the feed. The server must not error, and
// must start tailing as soon as a newer ingestor creates it - no restart.
func TestTxRewriteFeedAbsentThenCreated(t *testing.T) {
	db, w := txRewriteFixture(t, false)
	s := NewPacketStore(db, &PacketStoreConfig{})
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if err := s.pollTxRewrites(500); err != nil {
		t.Fatalf("absent feed must be a no-op, got %v", err)
	}
	if _, err := w.Exec(txRewriteFeedDDL); err != nil {
		t.Fatal(err)
	}
	backfillRewrite(t, w, `{"type":"CHAN","channel":"#saar"}`, "#saar")
	if err := s.pollTxRewrites(500); err != nil {
		t.Fatal(err)
	}
	if got := s.byTxID[1].ParsedDecoded()["channel"]; got != "#saar" {
		t.Fatalf("feed created after startup was not picked up: %v", got)
	}
}

func TestTxRewriteFeedFailedReadKeepsCursorAndRetries(t *testing.T) {
	db, w := txRewriteFixture(t, true)
	s := NewPacketStore(db, &PacketStoreConfig{})
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	backfillRewrite(t, w, `{"type":"CHAN","channel":"#saar"}`, "#saar")
	if _, err := w.Exec(`ALTER TABLE tx_rewrite_feed RENAME TO feed_unavailable`); err != nil {
		t.Fatal(err)
	}
	if err := s.pollTxRewrites(500); err == nil {
		t.Fatal("expected failed feed read")
	}
	if s.txRewriteCursor != 0 {
		t.Fatal("failed read advanced the cursor")
	}
	if _, err := w.Exec(`ALTER TABLE feed_unavailable RENAME TO tx_rewrite_feed`); err != nil {
		t.Fatal(err)
	}
	if err := s.pollTxRewrites(500); err != nil {
		t.Fatal(err)
	}
	if got := s.byTxID[1].ParsedDecoded()["channel"]; got != "#saar" {
		t.Fatalf("retry did not apply the rewrite: %v", got)
	}
}

// A feed row for a transmission the store does not hold advances the cursor
// and is otherwise ignored; the row is read fresh if it is ever loaded.
func TestTxRewriteFeedSkipsUnloadedTransmission(t *testing.T) {
	db, w := txRewriteFixture(t, true)
	s := NewPacketStore(db, &PacketStoreConfig{})
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`INSERT INTO tx_rewrite_feed(tx_id) VALUES(999)`); err != nil {
		t.Fatal(err)
	}
	if err := s.pollTxRewrites(500); err != nil {
		t.Fatal(err)
	}
	if s.byTxID[999] != nil || s.txRewriteCursor != 1 {
		t.Fatalf("unloaded tx: present=%v cursor=%d", s.byTxID[999] != nil, s.txRewriteCursor)
	}
}
