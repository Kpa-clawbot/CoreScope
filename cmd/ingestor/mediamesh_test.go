package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// MediaMesh (radio2): observers tag what used it; see mediamesh.go and
// internal/dbschema/mediamesh.go.

// A flood ADVERT with an empty path: the advertiser is the node that sent it.
const mediaMeshAdvertHex = "120046D62DE27D4C5194D7821FC5A34A45565DCC2537B300B9AB6275255CEFB65D840CE5C169C94C9AED39E8BCB6CB6EB0335497A198B33A1A610CD3B03D8DCFC160900E5244280323EE0B44CACAB8F02B5B38B91CFA18BD067B0B5E63E94CFC85F758A8530B9240933402E0E6B8F84D5252322D52"
const mediaMeshAdvertPubkey = "46d62de27d4c5194d7821fc5a34a45565dcc2537b300b9ab6275255cefb65d84"

func mmCountRows(t *testing.T, s *Store, query string, args ...interface{}) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func nodeMediaMesh(t *testing.T, s *Store, pubkey string) (mode, radio, source string, ok bool) {
	t.Helper()
	var r *string
	err := s.db.QueryRow(`SELECT mode, radio, source FROM node_mediamesh WHERE public_key = ?`, pubkey).
		Scan(&mode, &r, &source)
	if err != nil {
		return "", "", "", false
	}
	if r != nil {
		radio = *r
	}
	return mode, radio, source, true
}

func TestMediaMeshFlag(t *testing.T) {
	for _, tc := range []struct {
		payload string
		want    bool
	}{
		{`{"mediamesh":true}`, true},
		{`{"mediamesh":false}`, false},
		{`{"mediamesh":"true"}`, true},
		{`{"mediamesh":"1"}`, true},
		{`{"mediamesh":1}`, true},
		{`{"mediamesh":"yes"}`, false},
		{`{}`, false},
	} {
		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(tc.payload), &msg); err != nil {
			t.Fatal(err)
		}
		if got := mediaMeshFlag(msg); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.payload, got, tc.want)
		}
	}
}

func TestHandleMessageMediaMeshPacket(t *testing.T) {
	store, source := newTestContext(t)
	msg := &mockMessage{
		topic: "meshcore/SEA/obs1/packets",
		payload: []byte(`{"raw":"` + mediaMeshAdvertHex + `","direction":"rx","SNR":"9.5","RSSI":"-71",` +
			`"rx_radio_id":"radio2","mediamesh":true}`),
	}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	if n := mmCountRows(t, store, `SELECT COUNT(*) FROM observation_mediamesh`); n != 1 {
		t.Fatalf("observation_mediamesh rows = %d, want 1", n)
	}
	var radioID string
	if err := store.db.QueryRow(`SELECT radio_id FROM observation_radios`).Scan(&radioID); err != nil || radioID != "radio2" {
		t.Fatalf("observation_radios radio_id = %q (%v), want radio2", radioID, err)
	}
	// The advertiser sent it on radio2: heard, so it transmits there.
	mode, _, src, ok := nodeMediaMesh(t, store, mediaMeshAdvertPubkey)
	if !ok || mode != "rxtx" || src != "heard" {
		t.Fatalf("node_mediamesh = %q/%q (%v), want rxtx/heard", mode, src, ok)
	}
}

// Every observer before this change: nothing new is written.
func TestHandleMessageWithoutMediaMeshWritesNothing(t *testing.T) {
	store, source := newTestContext(t)
	msg := &mockMessage{
		topic:   "meshcore/SEA/obs1/packets",
		payload: []byte(`{"raw":"` + mediaMeshAdvertHex + `","direction":"rx","SNR":"9.5","RSSI":"-71"}`),
	}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})
	if n := mmCountRows(t, store, `SELECT COUNT(*) FROM transmissions`); n != 1 {
		t.Fatalf("transmissions = %d, want 1", n)
	}
	for _, table := range []string{"observation_mediamesh", "node_mediamesh", "observation_radios"} {
		if n := mmCountRows(t, store, `SELECT COUNT(*) FROM `+table); n != 0 {
			t.Errorf("%s rows = %d, want 0", table, n)
		}
	}
}

// A packet the observer sent on radio2 is MediaMesh, and the observer runs
// MediaMesh both ways: it sent on radio2.
func TestHandleMessageMediaMeshTxMarksObserver(t *testing.T) {
	store, source := newTestContext(t)
	obs := strings.Repeat("d", 64)
	msg := &mockMessage{
		topic: "meshcore/SEA/" + strings.ToUpper(obs) + "/packets",
		payload: []byte(`{"raw":"` + mediaMeshAdvertHex + `","direction":"tx",` +
			`"tx_radio_id":"radio2","mediamesh":true}`),
	}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})
	if n := mmCountRows(t, store, `SELECT COUNT(*) FROM observation_mediamesh`); n != 1 {
		t.Fatalf("observation_mediamesh rows = %d, want 1", n)
	}
	if n := mmCountRows(t, store, `SELECT COUNT(*) FROM observation_radios WHERE radio_id = 'radio2'`); n != 1 {
		t.Fatalf("tx_radio_id not kept")
	}
	var mode, src string
	err := store.db.QueryRow(`SELECT mode, source FROM node_mediamesh WHERE public_key = ?`, obs).Scan(&mode, &src)
	if err != nil || mode != "rxtx" || src != "heard" {
		t.Fatalf("observer node_mediamesh = %q/%q (%v), want rxtx/heard", mode, src, err)
	}
	if n := mmCountRows(t, store, `SELECT COUNT(*) FROM node_mediamesh`); n != 1 {
		t.Errorf("node_mediamesh rows = %d, want only the observer", n)
	}
}

// An observer id that is not a public key names nobody.
func TestHandleMessageMediaMeshTxNonKeyObserver(t *testing.T) {
	store, source := newTestContext(t)
	msg := &mockMessage{
		topic: "meshcore/SEA/obs1/packets",
		payload: []byte(`{"raw":"` + mediaMeshAdvertHex + `","direction":"tx",` +
			`"tx_radio_id":"radio2","mediamesh":true}`),
	}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})
	if n := mmCountRows(t, store, `SELECT COUNT(*) FROM observation_mediamesh`); n != 1 {
		t.Fatalf("observation_mediamesh rows = %d, want 1", n)
	}
	if n := mmCountRows(t, store, `SELECT COUNT(*) FROM node_mediamesh`); n != 0 {
		t.Errorf("node_mediamesh rows = %d, want 0", n)
	}
}

func TestMediaMeshSender(t *testing.T) {
	a := strings.Repeat("a", 64)
	b := strings.Repeat("b", 64)
	rx, tx := "rx", "tx"
	flood := &PacketData{PayloadType: 4, RouteType: 1, PathJSON: `["aa","bb"]`, Direction: &rx}
	if got := mediaMeshSender(flood, []*string{&a, &b}); got != b {
		t.Errorf("flood: sender %q, want the last hop", got)
	}
	if got := mediaMeshSender(flood, []*string{&a, nil}); got != "" {
		t.Errorf("unresolved last hop: sender %q, want none", got)
	}
	empty := &PacketData{PayloadType: 4, RouteType: 1, PathJSON: `[]`, FromPubkey: strings.ToUpper(a), Direction: &rx}
	if got := mediaMeshSender(empty, nil); got != a {
		t.Errorf("empty path: sender %q, want the originator", got)
	}
	zeroHop := &PacketData{PayloadType: 4, RouteType: 2, PathJSON: `[]`, FromPubkey: a, Direction: &rx}
	if got := mediaMeshSender(zeroHop, nil); got != a {
		t.Errorf("zero-hop direct: sender %q, want the originator", got)
	}
	lastLeg := &PacketData{PayloadType: 2, RouteType: 2, PathJSON: `[]`, FromPubkey: a, Direction: &rx}
	if got := mediaMeshSender(lastLeg, nil); got != "" {
		t.Errorf("direct TXT_MSG with an empty path may come from the last repeater: sender %q, want none", got)
	}
	direct := &PacketData{PayloadType: 4, RouteType: 2, PathJSON: `["aa"]`, Direction: &rx}
	if got := mediaMeshSender(direct, []*string{&a}); got != "" {
		t.Errorf("direct route: sender %q, want none", got)
	}
	sent := &PacketData{PayloadType: 4, RouteType: 1, PathJSON: `["aa"]`, Direction: &tx, ObserverID: strings.ToUpper(b)}
	if got := mediaMeshSender(sent, []*string{&a}); got != b {
		t.Errorf("tx: sender %q, want the observer itself", got)
	}
}

func TestStatusRadiosMarksObserverNode(t *testing.T) {
	store, source := newTestContext(t)
	obs := strings.Repeat("c", 64)
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00")
	msg := &mockMessage{
		topic: "meshcore/SEA/" + strings.ToUpper(obs) + "/status",
		payload: []byte(`{"status":"online","timestamp":"` + now + `","origin":"First Hill",` +
			`"origin_id":"` + strings.ToUpper(obs) + `","radio":"910.525000,62.5,7,5",` +
			`"radios":[{"id":"radio2","radio":"911.150000,250.0,7,5","mode":"rxtx","mediamesh":true,"temporary":false}]}`),
	}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})
	mode, radio, src, ok := nodeMediaMesh(t, store, obs)
	if !ok || mode != "rxtx" || radio != "911.15,250,7,5" || src != "status" {
		t.Fatalf("node_mediamesh = %q %q %q (%v), want rxtx 911.15,250,7,5 status", mode, radio, src, ok)
	}
	// The #1869 radio history also gets the radio2 setting under its id.
	if n := mmCountRows(t, store, `SELECT COUNT(*) FROM observer_radios WHERE radio_id = 'radio2'`); n != 1 {
		t.Errorf("observer_radios radio2 rows = %d, want 1", n)
	}
}

func TestNeighborsReportRadio2(t *testing.T) {
	store := openNeighborsStore(t)
	self := strings.Repeat("d", 64)
	on := strings.Repeat("e", 64)
	off := strings.Repeat("f", 64)
	unknown := strings.Repeat("1", 64)
	msg := map[string]interface{}{
		"timestamp": "2026-10-29T08:00:00Z",
		"origin_id": strings.ToUpper(self),
		"self": map[string]interface{}{"scopes": "sea", "default_scope": "*",
			"radio2": map[string]interface{}{"mode": "rxtx", "radio": "910.525000,250.0,7,5", "temporary": false}},
		"neighbors": []interface{}{
			map[string]interface{}{"pubkey": strings.ToUpper(on), "status": "timeout",
				"radio2": map[string]interface{}{"mode": "rx", "radio": "911.150000,250.0,7,5", "temporary": true}},
			map[string]interface{}{"pubkey": off, "status": "responded", "scopes": "sea",
				"radio2": map[string]interface{}{"mode": "off"}},
			map[string]interface{}{"pubkey": unknown, "status": "responded", "scopes": "sea"},
			map[string]interface{}{"pubkey": "not-hex", "radio2": map[string]interface{}{"mode": "rx"}},
		},
	}
	handleNeighborsReport(store, "test", self, msg)

	if mode, radio, src, ok := nodeMediaMesh(t, store, self); !ok || mode != "rxtx" || radio != "910.525,250,7,5" || src != "status" {
		t.Errorf("self = %q %q %q (%v)", mode, radio, src, ok)
	}
	if mode, radio, src, ok := nodeMediaMesh(t, store, on); !ok || mode != "rx" || radio != "911.15,250,7,5" || src != "neighbor" {
		t.Errorf("on = %q %q %q (%v)", mode, radio, src, ok)
	}
	if mode, radio, _, ok := nodeMediaMesh(t, store, off); !ok || mode != "off" || radio != "" {
		t.Errorf("off = %q %q (%v)", mode, radio, ok)
	}
	if _, _, _, ok := nodeMediaMesh(t, store, unknown); ok {
		t.Errorf("a neighbour without radio2 is unknown, not stored")
	}
	if n := mmCountRows(t, store, `SELECT COUNT(*) FROM node_mediamesh`); n != 3 {
		t.Errorf("node_mediamesh rows = %d, want 3", n)
	}
}

func TestRecordMediaMeshNewestWins(t *testing.T) {
	s := newTestStore(t)
	pk := strings.Repeat("a", 64)
	t0 := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	must := func(r MediaMeshReport, at time.Time) {
		t.Helper()
		if err := s.RecordMediaMesh(r, at); err != nil {
			t.Fatal(err)
		}
	}
	must(MediaMeshReport{PubKey: pk, Mode: "rx", Radio: "911.15,250,7,5", Source: "neighbor"}, t0)
	// Heard sending: rxtx, keeps the radio it reported.
	must(MediaMeshReport{PubKey: pk, Mode: "rxtx", Source: "heard"}, t0.Add(time.Minute))
	if mode, radio, src, _ := nodeMediaMesh(t, s, pk); mode != "rxtx" || radio != "911.15,250,7,5" || src != "heard" {
		t.Fatalf("after heard: %q %q %q", mode, radio, src)
	}
	// An older report does not overwrite a newer one.
	must(MediaMeshReport{PubKey: pk, Mode: "off", Source: "neighbor"}, t0)
	if mode, _, _, _ := nodeMediaMesh(t, s, pk); mode != "rxtx" {
		t.Fatalf("older report won: %q", mode)
	}
	// A newer "off" clears it, radio included.
	must(MediaMeshReport{PubKey: pk, Mode: "off", Source: "neighbor"}, t0.Add(2*time.Minute))
	if mode, radio, _, _ := nodeMediaMesh(t, s, pk); mode != "off" || radio != "" {
		t.Fatalf("off: %q %q", mode, radio)
	}
}

func TestMediaMeshHeardDebounce(t *testing.T) {
	var h mediaMeshHeard
	t0 := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	if !h.due("a", t0) || h.due("a", t0.Add(30*time.Second)) || !h.due("b", t0) {
		t.Fatal("debounce within a minute per node")
	}
	if !h.due("a", t0.Add(61*time.Second)) {
		t.Fatal("due again after a minute")
	}
}

func TestPruneMediaMesh(t *testing.T) {
	store, source := newTestContext(t)
	msg := &mockMessage{
		topic:   "meshcore/SEA/obs1/packets",
		payload: []byte(`{"raw":"` + mediaMeshAdvertHex + `","direction":"rx","rx_radio_id":"radio2","mediamesh":true}`),
	}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})
	if _, err := store.db.Exec(`UPDATE transmissions SET first_seen = '2020-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PruneOldPackets(1); err != nil {
		t.Fatal(err)
	}
	if n := mmCountRows(t, store, `SELECT COUNT(*) FROM observation_mediamesh`); n != 0 {
		t.Errorf("observation_mediamesh rows = %d after prune, want 0", n)
	}
	if _, err := store.db.Exec(`UPDATE node_mediamesh SET last_seen = '2020-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if n, err := store.PruneMediaMeshNodes(time.Now()); err != nil || n != 1 {
		t.Errorf("PruneMediaMeshNodes = %d (%v), want 1", n, err)
	}
}
