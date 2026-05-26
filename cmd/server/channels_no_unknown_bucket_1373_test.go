package main

import (
	"database/sql"
	"fmt"
	"testing"
)

// Issue #1373: /api/channels emits a ghost "unknown" bucket for encrypted GRP_TXT
// packets whose decoded JSON sets channel="" (server has no PSK to decrypt).
// Fix A (cosmetic): drop the "unknown" bucket from the response so users only
// see real channels. Encrypted-no-key packets are still observable via the
// encrypted-channels analytics, just not as a fake "unknown" channel.
//
// This test seeds 5 GRP_TXT with Channel="" (encrypted-no-key) + 3 with
// Channel="#real" and asserts GetChannels returns exactly one entry, #real —
// no "unknown" bucket.

func TestGetChannels_NoUnknownBucket_1373(t *testing.T) {
	packets := []*StoreTx{
		makeGrpTx(129, "", "", ""),
		makeGrpTx(129, "", "", ""),
		makeGrpTx(129, "", "", ""),
		makeGrpTx(129, "", "", ""),
		makeGrpTx(129, "", "", ""),
		makeGrpTx(72, "#real", "hello", "alice"),
		makeGrpTx(72, "#real", "world", "bob"),
		makeGrpTx(72, "#real", "third", "carol"),
	}
	store := newChannelTestStore(packets)

	channels := store.GetChannels("")

	var gotNames []string
	for _, ch := range channels {
		name, _ := ch["name"].(string)
		gotNames = append(gotNames, name)
		if name == "unknown" {
			t.Errorf("GetChannels emitted ghost 'unknown' bucket (issue #1373): %+v", ch)
		}
	}
	if len(channels) != 1 {
		t.Fatalf("expected exactly 1 channel (#real), got %d: %v", len(channels), gotNames)
	}
	if name, _ := channels[0]["name"].(string); name != "#real" {
		t.Errorf("expected channel name '#real', got %q", name)
	}
	if mc, _ := channels[0]["messageCount"].(int); mc != 3 {
		t.Errorf("expected messageCount=3 for #real, got %v", channels[0]["messageCount"])
	}
}

// TestGetChannels_DB_NoUnknownBucket_1373 mirrors the in-memory test against
// the DB-backed GetChannels path in cmd/server/db.go. It seeds GRP_TXT rows
// with channel_hash NULL (encrypted, no PSK known to ingestor) + rows with
// channel_hash="#real" and asserts the response contains only #real.
//
// Note: the DB path already filters NULL channel_hash via the SELECT (`channel_hash IS NOT NULL`),
// AND nullStr("")==empty triggers `continue` in the loop. This test pins that
// contract so a future refactor can't reintroduce an "unknown" bucket on the
// DB side either.
func TestGetChannels_DB_NoUnknownBucket_1373(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Seed 5 encrypted GRP_TXT rows with channel_hash NULL (server had no PSK).
	for i := 0; i < 5; i++ {
		_, err := db.conn.Exec(`INSERT INTO transmissions
			(raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
			VALUES (?, ?, '2026-05-25T12:00:00Z', 1, 5,
			'{"type":"CHAN","channel":"","text":"","sender":""}', NULL)`,
			"AA", sqlHashFor(i))
		if err != nil {
			t.Fatalf("seed encrypted row %d: %v", i, err)
		}
	}

	// Seed 3 decrypted GRP_TXT rows with channel_hash="#real".
	for i := 0; i < 3; i++ {
		_, err := db.conn.Exec(`INSERT INTO transmissions
			(raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
			VALUES (?, ?, '2026-05-25T12:00:00Z', 1, 5,
			'{"type":"CHAN","channel":"#real","text":"Alice: hi","sender":"Alice"}', '#real')`,
			"BB", sqlHashFor(100+i))
		if err != nil {
			t.Fatalf("seed real row %d: %v", i, err)
		}
	}

	channels, err := db.GetChannels()
	if err != nil {
		t.Fatalf("GetChannels: %v", err)
	}

	var gotNames []string
	for _, ch := range channels {
		name, _ := ch["name"].(string)
		gotNames = append(gotNames, name)
		if name == "unknown" {
			t.Errorf("DB GetChannels emitted ghost 'unknown' bucket (issue #1373): %+v", ch)
		}
		if name == "" {
			t.Errorf("DB GetChannels emitted empty-name channel bucket (issue #1373): %+v", ch)
		}
	}
	if len(channels) != 1 {
		t.Fatalf("expected exactly 1 channel (#real), got %d: %v", len(channels), gotNames)
	}
	if name, _ := channels[0]["name"].(string); name != "#real" {
		t.Errorf("expected channel name '#real', got %q", name)
	}
}

// sqlHashFor returns a unique 16-char hex string per index for the
// `hash` UNIQUE column in transmissions.
func sqlHashFor(i int) string {
	return fmt.Sprintf("%016x", uint64(0x1373_0000_0000_0000)+uint64(i))
}

// silence unused-import warning when the file is reduced.
var _ = sql.ErrNoRows
