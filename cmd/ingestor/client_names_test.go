package main

import (
	"strings"
	"testing"
	"time"
)

// clientNodeNamesCount returns the row count in client_node_names.
func clientNodeNamesCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM client_node_names`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// nodeNameMsg is a well-formed /names payload as coredrive-rx v1.20.0 sends it
// (src/publisher.js buildNamesPayload). The timestamp is an hour in the past:
// resolveRxTimeCore replaces a future one with the ingest time, which would
// make two deliveries of the same answer look like two observations.
func nodeNameMsg(name string) map[string]interface{} {
	return map[string]interface{}{
		"type": "NODE_NAME", "timestamp": time.Now().UTC().Add(-time.Hour).Format(rxTimeMillisLayout),
		"target":         testTargetPK,
		"name":           name,
		"repeater_clock": float64(1789000000),
		"gps":            map[string]interface{}{"lat": 51.05, "lon": 3.72, "acc_m": 8.0},
	}
}

func TestHandleClientNodeNamesWritesRow(t *testing.T) {
	s := newTestStore(t)
	handleClientNodeNames(s, "test", testCompanionPK, nodeNameMsg("ON8AR Rptr"))

	if n := clientNodeNamesCount(t, s); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	cur, err := s.CurrentClientNodeName(testTargetPK)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		t.Fatal("current = nil, want a row")
	}
	if cur.Name != "ON8AR Rptr" {
		t.Errorf("Name = %q, want %q", cur.Name, "ON8AR Rptr")
	}
	if cur.RxPubkey != testCompanionPK {
		t.Errorf("RxPubkey = %q, want the topic pubkey %q", cur.RxPubkey, testCompanionPK)
	}
	if cur.Lat == nil || cur.Lon == nil || *cur.Lat != 51.05 || *cur.Lon != 3.72 {
		t.Errorf("lat/lon = %v/%v, want 51.05/3.72", cur.Lat, cur.Lon)
	}
	if cur.RepeaterClock == nil || *cur.RepeaterClock != 1789000000 {
		t.Errorf("RepeaterClock = %v, want 1789000000", cur.RepeaterClock)
	}
	if _, err := time.Parse(rxTimeMillisLayout, cur.ObservedAt); err != nil {
		t.Errorf("observed_at = %q, want rxTimeMillisLayout format: %v", cur.ObservedAt, err)
	}
}

// A name is meaningful without a position, so a missing fix stores NULLs
// instead of dropping the row.
func TestHandleClientNodeNamesWithoutGPS(t *testing.T) {
	s := newTestStore(t)
	msg := nodeNameMsg("Aegis")
	delete(msg, "gps")
	handleClientNodeNames(s, "test", testCompanionPK, msg)

	cur, err := s.CurrentClientNodeName(testTargetPK)
	if err != nil || cur == nil {
		t.Fatalf("current = %v, %v; want a row", cur, err)
	}
	if cur.Lat != nil || cur.Lon != nil {
		t.Errorf("lat/lon = %v/%v, want NULL", cur.Lat, cur.Lon)
	}
}

func TestHandleClientNodeNamesRejects(t *testing.T) {
	cases := []struct {
		label, rx string
		msg       map[string]interface{}
	}{
		{"invalid topic pubkey", "NOT-HEX!", nodeNameMsg("Aegis")},
		{"short target", testCompanionPK, func() map[string]interface{} {
			m := nodeNameMsg("Aegis")
			m["target"] = testTargetPK[:16]
			return m
		}()},
		{"missing name", testCompanionPK, func() map[string]interface{} {
			m := nodeNameMsg("")
			delete(m, "name")
			return m
		}()},
		{"empty name", testCompanionPK, nodeNameMsg("   ")},
		{"padding only", testCompanionPK, nodeNameMsg("\x00\x00\x00")},
		{"control character", testCompanionPK, nodeNameMsg("Aegis\nowner")},
		{"too long", testCompanionPK, nodeNameMsg(strings.Repeat("x", maxNodeNameRunes+1))},
		{"invalid utf-8", testCompanionPK, nodeNameMsg("Ae\xffgis")},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			s := newTestStore(t)
			handleClientNodeNames(s, "test", c.rx, c.msg)
			if n := clientNodeNamesCount(t, s); n != 0 {
				t.Fatalf("rows = %d, want 0", n)
			}
		})
	}
}

func TestCleanNodeName(t *testing.T) {
	cases := map[string]string{
		"Aegis":                               "Aegis",
		"  Aegis  ":                           "Aegis",
		"Aegis\x00\x00\x00":                   "Aegis",
		"Gent Zuid ✈":                         "Gent Zuid ✈",
		strings.Repeat("x", maxNodeNameRunes): strings.Repeat("x", maxNodeNameRunes),
	}
	for in, want := range cases {
		if got, ok := cleanNodeName(in); !ok || got != want {
			t.Errorf("cleanNodeName(%q) = %q, %v; want %q, true", in, got, ok, want)
		}
	}
}

// The same answer delivered twice (QoS 1 redelivery, or a client re-sending
// its queue) is stored once.
func TestHandleClientNodeNamesIdempotent(t *testing.T) {
	s := newTestStore(t)
	// One message delivered twice: nodeNameMsg reads the clock, so building it
	// twice would give two observations a millisecond apart on a slow runner.
	msg := nodeNameMsg("Aegis")
	handleClientNodeNames(s, "test", testCompanionPK, msg)
	handleClientNodeNames(s, "test", testCompanionPK, msg)
	if n := clientNodeNamesCount(t, s); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

// Current is the newest observed_at, not the latest arrival: a drive buffered
// offline must not overwrite a fresher name.
func TestCurrentClientNodeNameByObservedAt(t *testing.T) {
	s := newTestStore(t)
	// Relative to now: resolveRxTimeCore replaces a timestamp older than 30
	// days, so fixed dates would stop testing the ordering once they age out.
	now := time.Now().UTC()
	newer := nodeNameMsg("Renamed")
	newer["timestamp"] = now.Add(-time.Hour).Format(rxTimeMillisLayout)
	older := nodeNameMsg("Old name")
	older["timestamp"] = now.Add(-5 * 24 * time.Hour).Format(rxTimeMillisLayout)
	handleClientNodeNames(s, "test", testCompanionPK, newer)
	handleClientNodeNames(s, "test", testCompanionPK, older)

	cur, err := s.CurrentClientNodeName(testTargetPK)
	if err != nil || cur == nil {
		t.Fatalf("current = %v, %v; want a row", cur, err)
	}
	if cur.Name != "Renamed" {
		t.Errorf("Name = %q, want the newest observation %q", cur.Name, "Renamed")
	}
}

func TestPruneOldClientNodeNames(t *testing.T) {
	s := newTestStore(t)
	// Inserted directly: the handler would replace a timestamp this old
	// (resolveRxTimeCore), and the prune must be tested against stored rows.
	mk := func(observedAt, name string) *ClientNodeName {
		return &ClientNodeName{Target: testTargetPK, RxPubkey: testCompanionPK, ObservedAt: observedAt, IngestedAt: observedAt, Name: name}
	}
	for _, o := range []*ClientNodeName{mk("2026-01-01T00:00:00.000Z", "Old"), mk("2026-10-16T00:00:00.000Z", "Recent")} {
		if _, err := s.InsertClientNodeName(o); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.pruneOldClientNodeNamesAt(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned = %d, want 1", n)
	}
	cur, _ := s.CurrentClientNodeName(testTargetPK)
	if cur == nil || cur.Name != "Recent" {
		t.Fatalf("current = %+v, want the recent row to survive", cur)
	}
	if n, _ := s.PruneOldClientNodeNames(0); n != 0 {
		t.Fatalf("days=0 pruned %d, want 0 (disabled)", n)
	}
}

func clientNodeNamesMsg() *mockMessage {
	payload := []byte(`{"raw":"11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172","direction":"rx","type":"NODE_NAME","timestamp":"2026-10-16T10:00:00.000Z","target":"` + testTargetPK + `","name":"Aegis","repeater_clock":1789000000}`)
	return &mockMessage{topic: "meshcore/client/" + testCompanionPK + "/names", payload: payload}
}

func TestClientNodeNamesEnabledDefault(t *testing.T) {
	if (&Config{}).ClientNodeNamesEnabled() {
		t.Fatal("nil ClientNodeNames must report disabled")
	}
	if !(&Config{ClientNodeNames: &ClientNodeNamesConfig{Enabled: true}}).ClientNodeNamesEnabled() {
		t.Fatal("Enabled:true must report enabled")
	}
}

func TestClientNodeNamesGateOn(t *testing.T) {
	store := newTestStore(t)
	cfg := &Config{ClientNodeNames: &ClientNodeNamesConfig{Enabled: true}}
	handleMessage(store, "test", MQTTSource{Name: "test"}, clientNodeNamesMsg(), nil, nil, cfg)
	if n := clientNodeNamesCount(t, store); n != 1 {
		t.Fatalf("feature ON: rows = %d, want 1", n)
	}
}

// With the feature off the message is dropped outright, never decoded on the
// observer path (the payload carries an incidental "raw" to prove that).
func TestClientNodeNamesGateOffDoesNotFallThroughToObserverPath(t *testing.T) {
	store := newTestStore(t)
	handleMessage(store, "test", MQTTSource{Name: "test"}, clientNodeNamesMsg(), nil, nil, &Config{})
	if n := clientNodeNamesCount(t, store); n != 0 {
		t.Fatalf("feature OFF: rows = %d, want 0", n)
	}
	var observerRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM observers WHERE id = ?`, testCompanionPK).Scan(&observerRows); err != nil {
		t.Fatal(err)
	}
	if observerRows != 0 {
		t.Fatalf("feature OFF: the companion pubkey must not be registered as an observer, got %d rows", observerRows)
	}
}
