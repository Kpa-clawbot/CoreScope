package main

// Regression tests for issue #1465 — observer.last_seen MUST always reflect
// ingest time (server wall clock), never the MQTT envelope timestamp. Observers
// with broken clocks (wrong TZ, RTC drift, replayed retained messages) must
// NOT be able to drag the analyzer's "last heard from" field into the past
// or future.
//
// Per-packet rxTime semantics (envelope time with naive-clamp from #1464)
// are out of scope here — those continue to use envelope time. This file
// asserts only the observer.last_seen path.

import (
	"strconv"
	"testing"
	"time"
)

// Status path: envelope timestamp is a well-formed RFC3339 value 3h in the
// past. observer.last_seen must be server wall clock, NOT the envelope value.
func TestStatusMessage_ObserverLastSeen_AlwaysIngestTime_PastEnvelope_1465(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	stale := time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339)
	before := time.Now().Unix()

	payload := []byte(`{"status":"online","origin":"obs-past","timestamp":"` + stale + `"}`)
	msg := &mockMessage{topic: "meshcore/SJC/obs-past/status", payload: payload}

	handleMessage(store, "test", source, msg, nil, nil, &Config{})
	after := time.Now().Unix()

	var lastSeen string
	if err := store.db.QueryRow(`SELECT last_seen FROM observers WHERE id = ?`, "obs-past").Scan(&lastSeen); err != nil {
		t.Fatalf("scan last_seen: %v", err)
	}
	ls, err := time.Parse(time.RFC3339, lastSeen)
	if err != nil {
		t.Fatalf("last_seen %q not RFC3339: %v", lastSeen, err)
	}
	if ls.Unix() < before-5 || ls.Unix() > after+5 {
		t.Errorf("observer.last_seen = %q (epoch %d); want in [%d, %d] (server wall clock). "+
			"Envelope reported well-formed stale %q (3h ago) — must NOT drag last_seen into the past. Issue #1465.",
			lastSeen, ls.Unix(), before, after, stale)
	}
}

// Status path: envelope timestamp 5 min in the future. observer.last_seen
// must still be server wall clock.
func TestStatusMessage_ObserverLastSeen_AlwaysIngestTime_FutureEnvelope_1465(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	future := time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339)
	before := time.Now().Unix()

	payload := []byte(`{"status":"online","origin":"obs-future","timestamp":"` + future + `"}`)
	msg := &mockMessage{topic: "meshcore/SJC/obs-future/status", payload: payload}

	handleMessage(store, "test", source, msg, nil, nil, &Config{})
	after := time.Now().Unix()

	var lastSeen string
	if err := store.db.QueryRow(`SELECT last_seen FROM observers WHERE id = ?`, "obs-future").Scan(&lastSeen); err != nil {
		t.Fatalf("scan last_seen: %v", err)
	}
	ls, err := time.Parse(time.RFC3339, lastSeen)
	if err != nil {
		t.Fatalf("last_seen %q not RFC3339: %v", lastSeen, err)
	}
	if ls.Unix() < before-5 || ls.Unix() > after+5 {
		t.Errorf("observer.last_seen = %q (epoch %d); want in [%d, %d] (server wall clock). "+
			"Envelope reported well-formed future %q (5 min ahead) — must NOT drag last_seen into the future. Issue #1465.",
			lastSeen, ls.Unix(), before, after, future)
	}
}

// Packet path: a transmission whose envelope timestamp is 3h in the past
// MUST still bump observer.last_seen to server wall clock — observer is
// clearly alive (we just ingested a packet from it), regardless of what
// its clock claims.
func TestPacketMessage_ObserverLastSeen_AlwaysIngestTime_PastEnvelope_1465(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	stale := time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339)
	before := time.Now().Unix()

	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	payload := []byte(`{"raw":"` + rawHex + `","SNR":5.5,"RSSI":-100.0,"origin":"obs-pkt","timestamp":"` + stale + `"}`)
	msg := &mockMessage{topic: "meshcore/SJC/obs-pkt/packets", payload: payload}

	handleMessage(store, "test", source, msg, nil, nil, &Config{})
	after := time.Now().Unix()

	var lastSeen string
	if err := store.db.QueryRow(`SELECT last_seen FROM observers WHERE id = ?`, "obs-pkt").Scan(&lastSeen); err != nil {
		t.Fatalf("scan last_seen: %v", err)
	}
	ls, err := time.Parse(time.RFC3339, lastSeen)
	if err != nil {
		t.Fatalf("last_seen %q not RFC3339: %v", lastSeen, err)
	}
	if ls.Unix() < before-5 || ls.Unix() > after+5 {
		t.Errorf("packet-path observer.last_seen = %q (epoch %d); want in [%d, %d] (server wall clock). "+
			"Envelope stale = %q. Observer just delivered a packet; last_seen must be NOW. Issue #1465.",
			lastSeen, ls.Unix(), before, after, stale)
	}
	_ = strconv.Itoa // keep import stable
}
