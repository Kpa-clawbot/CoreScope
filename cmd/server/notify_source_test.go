package main

import (
	"fmt"
	"testing"
	"time"
)

func TestDBNotifyNodes(t *testing.T) {
	db := setupTestDB(t)
	for _, q := range []string{
		fmt.Sprintf(`INSERT INTO nodes (public_key, name, role, last_seen, battery_mv, foreign_advert) VALUES ('%s', 'Alpha', 'repeater', '2026-10-07T11:00:00Z', 3712, 0)`, evPkA),
		fmt.Sprintf(`INSERT INTO nodes (public_key, name, role, last_seen, foreign_advert) VALUES ('%s', NULL, 'companion', NULL, 0)`, evPkB),
		fmt.Sprintf(`INSERT INTO nodes (public_key, name, role, last_seen, foreign_advert) VALUES ('%s', 'Foxtrot', 'repeater', '2026-10-07T10:00:00Z', 1)`, evPkF),
	} {
		if _, err := db.conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.NotifyNodes([]string{evPkA, evPkB, evPkG}, false)
	if err != nil {
		t.Fatal(err)
	}
	a, b := got[evPkA], got[evPkB]
	if len(got) != 2 || a.Pubkey != evPkA || a.Name != "Alpha" || a.Role != "repeater" || a.LastSeen != "2026-10-07T11:00:00Z" ||
		a.BatteryMv == nil || *a.BatteryMv != 3712 || a.Foreign {
		t.Fatalf("NotifyNodes = %+v", got)
	}
	if b.Name != "" || b.LastSeen != "" || b.BatteryMv != nil {
		t.Fatalf("NULL columns = %+v", b)
	}
	got, err = db.NotifyNodes([]string{evPkA}, true)
	if err != nil || len(got) != 2 || !got[evPkF].Foreign || got[evPkF].Name != "Foxtrot" {
		t.Fatalf("with foreign = %+v, %v", got, err)
	}
	if got, err := db.NotifyNodes(nil, false); err != nil || len(got) != 0 {
		t.Fatalf("no keys = %+v, %v", got, err)
	}
}

func TestDBNotifyNodesChunksLongLists(t *testing.T) {
	db := setupTestDB(t)
	keys := make([]string, 0, 1200)
	for i := 0; i < 1200; i++ {
		keys = append(keys, fmt.Sprintf("%064x", i))
	}
	for _, i := range []int{3, 600, 1199} {
		if _, err := db.conn.Exec(`INSERT INTO nodes (public_key, role) VALUES (?, 'companion')`, keys[i]); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.NotifyNodes(keys, false)
	if err != nil || len(got) != 3 {
		t.Fatalf("1200 keys over %d-key chunks = %d nodes, %v; want 3", notifyNodeChunk, len(got), err)
	}
}

func TestPacketStoreLastHeardMap(t *testing.T) {
	s := &PacketStore{byNode: map[string][]*StoreTx{
		evPkA: {{FirstSeen: "2026-10-07T10:00:00Z"}, {FirstSeen: "2026-10-07T11:30:00.000Z"}, nil, {FirstSeen: "2026-10-07T09:00:00Z"}},
		evPkB: {{FirstSeen: "not a time"}},
	}}
	got, lock := s.LastHeardMap([]string{evPkA, evPkB, evPkF})
	if len(got) != 1 || !got[evPkA].Equal(time.Date(2026, 10, 7, 11, 30, 0, 0, time.UTC)) {
		t.Fatalf("LastHeardMap = %v", got)
	}
	if lock < 0 || lock > time.Second {
		t.Fatalf("lock time = %v", lock)
	}
}

// 5,000 watched nodes with 20 packets each, the spec's upper figure. Quote
// the result in the PR: this runs under the store's read lock.
func BenchmarkLastHeardMap(b *testing.B) {
	s := &PacketStore{byNode: map[string][]*StoreTx{}}
	keys := make([]string, 0, 5000)
	for i := 0; i < 5000; i++ {
		pk := fmt.Sprintf("%064x", i)
		keys = append(keys, pk)
		for j := 0; j < 20; j++ {
			s.byNode[pk] = append(s.byNode[pk], &StoreTx{FirstSeen: evNow.Add(-time.Duration(j) * time.Minute).Format(time.RFC3339)})
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.LastHeardMap(keys)
	}
}

func TestRelayTimes(t *testing.T) {
	m := map[string]RepeaterRelayInfo{
		evPkA: {LastRelayed: "2026-10-07T11:00:00Z"},
		evPkB: {},
	}
	got := relayTimes(m, []string{evPkA, evPkB, evPkF})
	if len(got) != 1 || !got[evPkA].Equal(time.Date(2026, 10, 7, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("relayTimes = %v", got)
	}
}

func TestNotifyObserversFrom(t *testing.T) {
	name, seen := "Roof", "2026-10-07T11:00:00Z"
	got := notifyObserversFrom([]Observer{{ID: "OBS1", Name: &name, LastSeen: &seen}, {ID: "OBS2"}})
	if len(got) != 2 || got[0] != (notifyObserver{ID: "OBS1", Name: "Roof", LastSeen: seen}) || got[1] != (notifyObserver{ID: "OBS2"}) {
		t.Fatalf("notifyObserversFrom = %+v", got)
	}
}

func TestServerNotifySourceWithoutStore(t *testing.T) {
	src := serverNotifySource{s: &Server{cfg: &Config{}}}
	if heard, lock := src.lastHeard([]string{evPkA}); len(heard) != 0 || lock != 0 || len(src.lastRelayed([]string{evPkA})) != 0 {
		t.Fatal("a server without a packet store reported times")
	}
	if src.lowBatteryMv() != 3300 || src.health().InfraSilentHours != 72 {
		t.Fatal("defaults not taken from the config")
	}
}

// The startup load (hot window plus background fill) must be finished, not
// only /api/healthz readiness: readiness can be reached while the newest
// packets are still loading, which would mail "offline" then "back online".
func TestServerNotifySourceReadyWaitsForTheStartupLoad(t *testing.T) {
	readiness.Store(1)
	defer readiness.Store(0)
	store := &PacketStore{clockSkew: &ClockSkewEngine{}}
	src := serverNotifySource{s: &Server{cfg: &Config{}, store: store}}
	if src.ready() {
		t.Fatal("ready before the startup load finished")
	}
	store.signalStartupLoadDone()
	if !src.ready() {
		t.Fatal("not ready after readiness and the startup load")
	}
	readiness.Store(0)
	if src.ready() {
		t.Fatal("ready without readiness")
	}
	if (serverNotifySource{s: &Server{cfg: &Config{}}}).ready() {
		t.Fatal("ready without a packet store")
	}
}

func TestPacketStoreNewestFirstSeen(t *testing.T) {
	if got := (&PacketStore{}).NewestFirstSeen(); !got.IsZero() {
		t.Fatalf("empty store = %v", got)
	}
	s := &PacketStore{packets: []*StoreTx{{FirstSeen: "2026-10-07T10:00:00Z"}, {FirstSeen: "2026-10-07T11:30:00.000Z"}}}
	if got := s.NewestFirstSeen(); !got.Equal(time.Date(2026, 10, 7, 11, 30, 0, 0, time.UTC)) {
		t.Fatalf("NewestFirstSeen = %v", got)
	}
}
