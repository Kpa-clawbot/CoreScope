package main

// Tests for issue #1143: pubkey attribution must use exact-match on a
// dedicated `from_pubkey` column, not `decoded_json LIKE '%pubkey%'`.
//
// These tests demonstrate the structural holes documented in #1143:
//   Hole 1: name-LIKE fallback surfaces same-name nodes
//   Hole 2a: an attacker can name themselves with someone else's pubkey
//            and get their transmissions attributed to the victim
//   Hole 2b: any 64-char hex substring inside decoded_json (path elements,
//            channel names, message bodies) produces false positives

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const (
	pkVictim   = "f7181c468dfe7c55aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pkAttacker = "deadbeefdeadbeefcccccccccccccccccccccccccccccccccccccccccccccccc"
	pkOther    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// seedAttribution inserts the standard adversarial fixture used by the
// issue #1143 tests. It returns the victim pubkey for convenience.
func seedAttribution(t *testing.T, db *DB) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)

	// (1) Legitimate ADVERT from the victim.
	mustExec(t, db, `INSERT INTO transmissions
		(raw_hex, hash, first_seen, route_type, payload_type, decoded_json, from_pubkey)
		VALUES ('AA','h_victim_advert',?,1,4,
			'{"type":"ADVERT","pubKey":"`+pkVictim+`","name":"VictimNode"}',
			?)`, now, pkVictim)

	// (2) Hole 1: a different node sharing the *display name* "VictimNode".
	mustExec(t, db, `INSERT INTO transmissions
		(raw_hex, hash, first_seen, route_type, payload_type, decoded_json, from_pubkey)
		VALUES ('BB','h_namespoof_advert',?,1,4,
			'{"type":"ADVERT","pubKey":"`+pkOther+`","name":"VictimNode"}',
			?)`, now, pkOther)

	// (3) Hole 2a: malicious node whose *name* is the victim's pubkey.
	//     decoded_json contains pkVictim as a substring (in the name field),
	//     but the actual originator is pkAttacker.
	mustExec(t, db, `INSERT INTO transmissions
		(raw_hex, hash, first_seen, route_type, payload_type, decoded_json, from_pubkey)
		VALUES ('CC','h_spoof_advert',?,1,4,
			'{"type":"ADVERT","pubKey":"`+pkAttacker+`","name":"`+pkVictim+`"}',
			?)`, now, pkAttacker)

	// (4) Hole 2b: free-text packet (e.g. channel message) whose body
	//     coincidentally contains the victim's pubkey as a substring.
	//     Real originator is pkAttacker; from_pubkey reflects that.
	mustExec(t, db, `INSERT INTO transmissions
		(raw_hex, hash, first_seen, route_type, payload_type, decoded_json, from_pubkey)
		VALUES ('DD','h_freetext_msg',?,1,5,
			'{"type":"GRP_TXT","text":"hello `+pkVictim+` how are you"}',
			?)`, now, pkAttacker)

	return pkVictim
}

func mustExec(t *testing.T, db *DB, q string, args ...interface{}) {
	t.Helper()
	if _, err := db.conn.Exec(q, args...); err != nil {
		t.Fatalf("exec failed: %v\nquery: %s", err, q)
	}
}

func hashesOf(rows []map[string]interface{}) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if h, ok := r["hash"].(string); ok {
			out = append(out, h)
		}
	}
	return out
}

func TestRecentTransmissions_Hole1_SameNameDifferentPubkey(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	victim := seedAttribution(t, db)

	got, err := db.GetRecentTransmissionsForNode(victim, 20)
	if err != nil {
		t.Fatal(err)
	}

	hashes := hashesOf(got)
	for _, h := range hashes {
		if h == "h_namespoof_advert" {
			t.Fatalf("Hole 1: same-name node was attributed to the victim. got hashes=%v", hashes)
		}
	}
}

func TestRecentTransmissions_Hole2a_PubkeyAsNameSpoof(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	victim := seedAttribution(t, db)

	got, err := db.GetRecentTransmissionsForNode(victim, 20)
	if err != nil {
		t.Fatal(err)
	}

	hashes := hashesOf(got)
	for _, h := range hashes {
		if h == "h_spoof_advert" {
			t.Fatalf("Hole 2a: attacker who named themselves with victim's pubkey "+
				"was attributed to the victim. got hashes=%v", hashes)
		}
	}
}

func TestRecentTransmissions_Hole2b_FreeTextHexFalsePositive(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	victim := seedAttribution(t, db)

	got, err := db.GetRecentTransmissionsForNode(victim, 20)
	if err != nil {
		t.Fatal(err)
	}

	hashes := hashesOf(got)
	for _, h := range hashes {
		if h == "h_freetext_msg" {
			t.Fatalf("Hole 2b: free-text containing the victim's pubkey as a "+
				"substring produced a false positive. got hashes=%v", hashes)
		}
	}
}

func TestRecentTransmissions_LegitimateAdvertReturned(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	victim := seedAttribution(t, db)

	got, err := db.GetRecentTransmissionsForNode(victim, 20)
	if err != nil {
		t.Fatal(err)
	}

	hashes := hashesOf(got)
	found := false
	for _, h := range hashes {
		if h == "h_victim_advert" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected legitimate victim advert (h_victim_advert) in result, got %v", hashes)
	}
}

// --- Multi-pubkey OR query (#1143 — db.go:1785) ---

func TestQueryMultiNodePackets_ExactMatchOnly(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedAttribution(t, db)

	// Query the victim's pubkey via the multi-node API. The malicious
	// "name = victim pubkey" row and the free-text row must NOT show up.
	res, err := db.QueryMultiNodePackets([]string{pkVictim}, 50, 0, "DESC", "", "")
	if err != nil {
		t.Fatal(err)
	}
	hashes := hashesOf(res.Packets)
	for _, bad := range []string{"h_spoof_advert", "h_freetext_msg", "h_namespoof_advert"} {
		for _, h := range hashes {
			if h == bad {
				t.Fatalf("QueryMultiNodePackets returned spurious match %q (pubkey %s as substring); hashes=%v",
					bad, pkVictim, hashes)
			}
		}
	}
	// The legitimate one must still be present.
	if !contains(hashes, "h_victim_advert") {
		t.Fatalf("expected h_victim_advert in QueryMultiNodePackets result, got %v", hashes)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// --- Index sanity check (#1143 perf): verify EXPLAIN QUERY PLAN uses the
// new index, not a SCAN. ---

func TestFromPubkeyIndexUsed(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	mustExec(t, db, `CREATE INDEX IF NOT EXISTS idx_transmissions_from_pubkey ON transmissions(from_pubkey)`)

	rows, err := db.conn.Query(
		`EXPLAIN QUERY PLAN SELECT id FROM transmissions WHERE from_pubkey = ?`,
		pkVictim)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err == nil {
			plan += detail + "\n"
		}
	}
	if !strings.Contains(plan, "idx_transmissions_from_pubkey") {
		t.Fatalf("expected EXPLAIN QUERY PLAN to use idx_transmissions_from_pubkey, got:\n%s", plan)
	}
}

// TestFromPubkeyIndexUsedForInClause verifies the index is used for the
// IN (?, ?, ...) query path used by QueryMultiNodePackets (db.go ~1787).
// Coverage extension — the equality path is covered above; this asserts
// the multi-node path doesn't silently regress to a full scan when the
// planner can't use the index for set membership.
func TestFromPubkeyIndexUsedForInClause(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	mustExec(t, db, `CREATE INDEX IF NOT EXISTS idx_transmissions_from_pubkey ON transmissions(from_pubkey)`)

	rows, err := db.conn.Query(
		`EXPLAIN QUERY PLAN SELECT id FROM transmissions WHERE from_pubkey IN (?, ?)`,
		pkVictim, pkOther)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err == nil {
			plan += detail + "\n"
		}
	}
	if !strings.Contains(plan, "idx_transmissions_from_pubkey") {
		t.Fatalf("expected EXPLAIN QUERY PLAN for IN(...) to use idx_transmissions_from_pubkey, got:\n%s", plan)
	}
}

// --- Migration / backfill ---

func TestBackfillFromPubkey_AdvertRowsPopulated(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test.db"

	// Create a legacy-style DB: transmissions table WITHOUT from_pubkey,
	// then run ensureFromPubkeyColumn to ALTER it in.
	rw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rw.Exec(`CREATE TABLE transmissions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		raw_hex TEXT, hash TEXT UNIQUE, first_seen TEXT,
		route_type INTEGER, payload_type INTEGER, payload_version INTEGER,
		decoded_json TEXT, created_at TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	// Two ADVERTs (different pubkeys) and a non-ADVERT.
	if _, err := rw.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, payload_type, decoded_json) VALUES
		('AA','m1','2026-01-01T00:00:00Z',4,'{"type":"ADVERT","pubKey":"`+pkVictim+`","name":"V"}'),
		('BB','m2','2026-01-01T00:00:00Z',4,'{"type":"ADVERT","pubKey":"`+pkOther+`","name":"O"}'),
		('CC','m3','2026-01-01T00:00:00Z',5,'{"type":"GRP_TXT","text":"hi"}')`); err != nil {
		t.Fatal(err)
	}
	rw.Close()

	if err := ensureFromPubkeyColumn(dbPath); err != nil {
		t.Fatalf("ensureFromPubkeyColumn: %v", err)
	}

	// Run synchronously by calling the function directly.
	backfillFromPubkeyAsync(dbPath, 100, 0)

	// Verify backfill populated the ADVERT rows.
	rw2, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rw2.Close()
	rows, err := rw2.Query("SELECT hash, from_pubkey FROM transmissions ORDER BY hash")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var h string
		var pk sql.NullString
		if err := rows.Scan(&h, &pk); err != nil {
			t.Fatal(err)
		}
		got[h] = pk.String
	}
	if got["m1"] != pkVictim {
		t.Errorf("m1 from_pubkey = %q, want %q", got["m1"], pkVictim)
	}
	if got["m2"] != pkOther {
		t.Errorf("m2 from_pubkey = %q, want %q", got["m2"], pkOther)
	}
	// Non-ADVERT row was not in the backfill scope; from_pubkey stays NULL.
	if got["m3"] != "" {
		t.Errorf("m3 from_pubkey = %q, want empty (NULL)", got["m3"])
	}
}

// TestBackfillFromPubkey_DoesNotBlockBoot exercises the async contract:
// main.go (cmd/server/main.go) calls startFromPubkeyBackfill, which is the
// SAME entry point used at production startup. The wrapper must dispatch
// the backfill in a goroutine; if anyone removes the `go` keyword inside
// startFromPubkeyBackfill, this test fails because the call no longer
// returns within the 50ms boot dispatch budget. The test does NOT use `go`
// itself — that would test only the test's own scheduler, not the
// production code path (cycle-3 M1c).
//
// DO NOT t.Parallel — uses package-global atomics
// (fromPubkeyBackfillTotal/Processed/Done). Concurrent tests would clobber
// the resets (cycle-3 m1c).
func TestBackfillFromPubkey_DoesNotBlockBoot(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/async_boot.db"

	rw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rw.Exec(`CREATE TABLE transmissions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		raw_hex TEXT, hash TEXT UNIQUE, first_seen TEXT,
		route_type INTEGER, payload_type INTEGER, payload_version INTEGER,
		decoded_json TEXT, created_at TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	// Insert N=1000 legacy ADVERT rows. With chunkSize=100 + yield=100ms
	// between chunks, sync would be ~900ms; we assert dispatch is <50ms.
	tx, err := rw.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO transmissions
		(raw_hex, hash, first_seen, payload_type, decoded_json) VALUES (?, ?, ?, 4, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	const N = 1000
	for i := 0; i < N; i++ {
		hash := fmt.Sprintf("h_async_boot_%d", i)
		dj := fmt.Sprintf(`{"type":"ADVERT","pubKey":"%s","name":"N%d"}`, pkVictim, i)
		if _, err := stmt.Exec("AA", hash, "2026-01-01T00:00:00Z", dj); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rw.Close()

	if err := ensureFromPubkeyColumn(dbPath); err != nil {
		t.Fatalf("ensureFromPubkeyColumn: %v", err)
	}

	// Reset atomics — other tests may have set them.
	fromPubkeyBackfillTotal.Store(0)
	fromPubkeyBackfillProcessed.Store(0)
	fromPubkeyBackfillDone.Store(false)
	defer func() {
		fromPubkeyBackfillTotal.Store(0)
		fromPubkeyBackfillProcessed.Store(0)
		fromPubkeyBackfillDone.Store(false)
	}()

	// Dispatch via the production wrapper. startFromPubkeyBackfill is the
	// same entry point main.go calls at boot; it must launch the backfill
	// in a goroutine internally. We deliberately do NOT prefix `go` here —
	// if the wrapper is ever made synchronous, the dispatch budget below
	// fires first.
	t0 := time.Now()
	startFromPubkeyBackfill(dbPath, 100, 100*time.Millisecond)
	dispatchElapsed := time.Since(t0)

	// (a) Boot-time dispatch budget: must return ~immediately.
	if dispatchElapsed > 50*time.Millisecond {
		t.Fatalf("backfill dispatch took %v (>50ms): not async — would block boot", dispatchElapsed)
	}

	// (b) Eventual completion via the fromPubkeyBackfillDone atomic.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if fromPubkeyBackfillDone.Load() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !fromPubkeyBackfillDone.Load() {
		t.Fatalf("backfill never flipped Done atomic within 30s; dispatched=%v", dispatchElapsed)
	}

	// (c) Backfill actually populated rows.
	rw2, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rw2.Close()
	var nullCount int
	if err := rw2.QueryRow(
		`SELECT COUNT(*) FROM transmissions WHERE payload_type = 4 AND from_pubkey IS NULL`,
	).Scan(&nullCount); err != nil {
		t.Fatal(err)
	}
	if nullCount > 0 {
		t.Errorf("backfill left %d ADVERT rows with NULL from_pubkey", nullCount)
	}
	if got := fromPubkeyBackfillProcessed.Load(); got != int64(N) {
		t.Errorf("fromPubkeyBackfillProcessed = %d, want %d", got, N)
	}
}
