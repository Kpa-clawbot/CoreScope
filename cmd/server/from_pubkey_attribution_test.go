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
	"strings"
	"testing"
	"time"
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

	got, err := db.GetRecentTransmissionsForNode(victim, "VictimNode", 20)
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

	got, err := db.GetRecentTransmissionsForNode(victim, "VictimNode", 20)
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

	got, err := db.GetRecentTransmissionsForNode(victim, "VictimNode", 20)
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

	got, err := db.GetRecentTransmissionsForNode(victim, "VictimNode", 20)
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
