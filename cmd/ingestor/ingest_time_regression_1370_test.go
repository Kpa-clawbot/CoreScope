package main

// Regression test for issue #1370 — counters PR #1233 (commit 498fbc03).
//
// PR #1233 made the ingestor use the MQTT envelope's "timestamp" field as
// transmissions.first_seen / observations.timestamp, on the premise that
// uploaders stamp it at radio receive and the value is trustworthy.
//
// That premise FAILS for observers whose own clock is wrong. Staging
// Voodoo3 tx 304114 in channel #test had 5 observations:
//   - 4 from Voodoo3 stamped "18:42" — Voodoo3's broken client clock,
//   - 1 from another observer stamped "01:42" — the actual receive time.
// Voodoo3 ingested first, so first_seen locked at "18:42" and the
// /api/channels row showed the channel as last-active 7h+ in the past.
//
// Fix: revert the storage path — packet/observation timestamps are
// server ingest time (time.Now() at the ingestor). Envelope timestamp
// stays usable for observer.last_seen (PR #1233's MAX/MIN guard there
// is fine and unrelated to the channel-ordering bug).

import (
	"strconv"
	"testing"
	"time"
)

// Raw packet path: envelope reports timestamp 7h in the past
// (simulating Voodoo3's broken client clock). After ingest,
// transmissions.first_seen and observations.timestamp must reflect
// SERVER wall clock, not the bogus envelope value.
func TestHandleMessage_PacketTimestamp_IgnoresStaleEnvelope_1370(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	stale := time.Now().UTC().Add(-7 * time.Hour).Format(time.RFC3339)
	before := time.Now().Unix()

	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	payload := []byte(`{"raw":"` + rawHex + `","SNR":5.5,"RSSI":-100.0,"origin":"voodoo3","timestamp":"` + stale + `"}`)
	msg := &mockMessage{topic: "meshcore/SJC/voodoo3/packets", payload: payload}

	handleMessage(store, "test", source, msg, nil, nil, &Config{})
	after := time.Now().Unix()

	// ─── transmissions.first_seen ───────────────────────────────────────
	var firstSeen string
	if err := store.db.QueryRow(`SELECT first_seen FROM transmissions LIMIT 1`).Scan(&firstSeen); err != nil {
		t.Fatalf("scan first_seen: %v", err)
	}
	fsParsed, err := time.Parse(time.RFC3339, firstSeen)
	if err != nil {
		t.Fatalf("first_seen %q not RFC3339: %v", firstSeen, err)
	}
	if fsParsed.Unix() < before-5 || fsParsed.Unix() > after+5 {
		t.Errorf("transmissions.first_seen = %q (epoch %d); want in [%d, %d] (server wall clock). "+
			"Envelope reported stale %q (7h ago) — PR #1233's premise that envelope timestamp is trustworthy is FALSE for buggy-clock observers. Issue #1370.",
			firstSeen, fsParsed.Unix(), before, after, stale)
	}

	// ─── observations.timestamp (epoch) ─────────────────────────────────
	var obsTs int64
	if err := store.db.QueryRow(`SELECT timestamp FROM observations LIMIT 1`).Scan(&obsTs); err != nil {
		t.Fatalf("scan observations.timestamp: %v", err)
	}
	if obsTs < before-5 || obsTs > after+5 {
		t.Errorf("observations.timestamp = %d; want in [%d, %d] (server wall clock). Envelope stale = %q. Issue #1370.",
			obsTs, before, after, stale)
	}
}

// Channel-message (BLE companion) path: envelope timestamp stale → stored
// transmissions.first_seen must still be server wall clock.
func TestHandleMessage_ChannelPath_PacketTimestamp_IgnoresStaleEnvelope_1370(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	stale := time.Now().UTC().Add(-7 * time.Hour).Format(time.RFC3339)
	before := time.Now().Unix()

	payload := []byte(`{"text":"Voodoo3: tst hmdpt","channel_idx":3,"SNR":5.0,"RSSI":-95,"timestamp":"` + stale + `","sender_timestamp":` + strconv.FormatInt(time.Now().Unix(), 10) + `}`)
	msg := &mockMessage{topic: "meshcore/message/channel/3", payload: payload}

	handleMessage(store, "test", source, msg, nil, nil, &Config{})
	after := time.Now().Unix()

	var firstSeen string
	if err := store.db.QueryRow(`SELECT first_seen FROM transmissions LIMIT 1`).Scan(&firstSeen); err != nil {
		t.Fatalf("scan first_seen: %v", err)
	}
	fsParsed, err := time.Parse(time.RFC3339, firstSeen)
	if err != nil {
		t.Fatalf("first_seen %q not RFC3339: %v", firstSeen, err)
	}
	if fsParsed.Unix() < before-5 || fsParsed.Unix() > after+5 {
		t.Errorf("channel-path transmissions.first_seen = %q (epoch %d); want in [%d, %d] (server wall clock). Envelope stale = %q. Issue #1370.",
			firstSeen, fsParsed.Unix(), before, after, stale)
	}
}

// DM (BLE companion direct-message) path: same revert applies.
func TestHandleMessage_DMPath_PacketTimestamp_IgnoresStaleEnvelope_1370(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	stale := time.Now().UTC().Add(-7 * time.Hour).Format(time.RFC3339)
	before := time.Now().Unix()

	payload := []byte(`{"text":"Voodoo3: hello","SNR":5.0,"RSSI":-95,"timestamp":"` + stale + `"}`)
	msg := &mockMessage{topic: "meshcore/message/direct/voodoo3", payload: payload}

	handleMessage(store, "test", source, msg, nil, nil, &Config{})
	after := time.Now().Unix()

	var firstSeen string
	if err := store.db.QueryRow(`SELECT first_seen FROM transmissions LIMIT 1`).Scan(&firstSeen); err != nil {
		t.Fatalf("scan first_seen: %v", err)
	}
	fsParsed, err := time.Parse(time.RFC3339, firstSeen)
	if err != nil {
		t.Fatalf("first_seen %q not RFC3339: %v", firstSeen, err)
	}
	if fsParsed.Unix() < before-5 || fsParsed.Unix() > after+5 {
		t.Errorf("DM-path transmissions.first_seen = %q (epoch %d); want in [%d, %d] (server wall clock). Envelope stale = %q. Issue #1370.",
			firstSeen, fsParsed.Unix(), before, after, stale)
	}
}
