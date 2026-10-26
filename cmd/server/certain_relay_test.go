package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func assertCertainRelay(t *testing.T, s *PacketStore, pk, want string) {
	t.Helper()
	got := s.LastCertainRelayMap([]string{pk})[pk]
	if want == "" {
		if !got.IsZero() {
			t.Errorf("last certain relay = %s; want no evidence", got)
		}
		return
	}
	wantTime, err := time.Parse(time.RFC3339Nano, want)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(wantTime) {
		t.Errorf("last certain relay = %s; want %s", got, wantTime)
	}
}

func TestCertainRelaySeparatesOwnAdvertsAndForwarding(t *testing.T) {
	s := chStore()
	pm := chPM()
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	tx := chTx(1, 1, 4, 2, at)
	tx.DecodedJSON = fmt.Sprintf(`{"pubKey":%q,"signatureValid":true}`, chA)
	s.indexByNode(tx)
	assertCertainRelay(t, s, chA, "")
	// Even if a resolved/observed path lists the originator, its own advert
	// must not be mistaken for forwarding. A different actual hop can count.
	chIndexResolved(s, tx, []string{chA, chC}, map[string]bool{}, pm)
	assertCertainRelay(t, s, chA, "")
	assertCertainRelay(t, s, chC, tx.FirstSeen)
	if s.certainHeard[chA] != tx.FirstSeen {
		t.Error("originated advert must still feed certain last heard")
	}
	chIndexResolved(s, chTx(2, 1, 5, 2, at.Add(time.Second)), []string{chA}, map[string]bool{}, pm)
	assertCertainRelay(t, s, chA, at.Add(time.Second).Format(time.RFC3339))
}

func TestCertainRelayRequiresAdvertOrigin(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  string
	}{
		{"malformed payload", "114140aa00"},
		{"truncated public key", "114140aa" + strings.Repeat("33", 31)},
	} {
		t.Run(c.name, func(t *testing.T) {
			decoded, err := DecodePacket(c.raw, true)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Header.PayloadType != payloadTypeAdvert || decoded.Payload.Error == "" || decoded.Payload.PubKey != "" {
				t.Fatalf("expected an advert without a decoded origin, got %+v", decoded.Payload)
			}
			path, err := json.Marshal(decoded.Path.Hops)
			if err != nil {
				t.Fatal(err)
			}
			pt := decoded.Header.PayloadType
			tx := &StoreTx{
				ID: 1, RawHex: c.raw, PayloadType: &pt,
				FirstSeen: "2026-10-20T12:00:00Z", DecodedJSON: PayloadJSON(&decoded.Payload),
			}
			obs := &StoreObs{PathJSON: string(path), relayHopHexLen: observedRelayHopHexLen(c.raw, string(path))}
			if obs.relayHopHexLen != 4 {
				t.Fatalf("expected an eligible two-byte hop, got %d hex characters", obs.relayHopHexLen)
			}
			tx.Observations = []*StoreObs{obs}
			s := chStore()
			s.indexResolvedPathHops(tx, []string{chC}, map[string]bool{}, chPM(), obs)
			if got := s.certainHeard[chC]; got != tx.FirstSeen {
				t.Errorf("existing certain last heard = %q; want %q", got, tx.FirstSeen)
			}
			assertCertainRelay(t, s, chC, "")
		})
	}
}

func TestCertainRelayGatesAndTimestamps(t *testing.T) {
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name  string
		setup func(*StoreTx, *prefixMap, *StoreObs)
	}{
		{"retired prefix", func(_ *StoreTx, pm *prefixMap, _ *StoreObs) { pm.retired = map[string]struct{}{chC[:4]: {}} }},
		{"active collision", func(_ *StoreTx, pm *prefixMap, _ *StoreObs) {
			pm.m[chC[:4]] = append(pm.m[chC[:4]], nodeInfo{PublicKey: chC[:4] + strings.Repeat("9", 60), Role: "repeater"})
		}},
		{"direct", func(tx *StoreTx, _ *prefixMap, obs *StoreObs) {
			obs.relayHopHexLen = observedRelayHopHexLen("164140aa00", obs.PathJSON)
		}},
		{"TRACE", func(tx *StoreTx, _ *prefixMap, obs *StoreObs) {
			obs.relayHopHexLen = observedRelayHopHexLen("254140aa00", obs.PathJSON)
		}},
		{"bad signature", func(tx *StoreTx, _ *prefixMap, _ *StoreObs) { tx.DecodedJSON = `{"signatureValid":false}` }},
		{"invalid timestamp", func(tx *StoreTx, _ *prefixMap, _ *StoreObs) { tx.FirstSeen = "not-a-date" }},
		{"missing timestamp", func(tx *StoreTx, _ *prefixMap, _ *StoreObs) { tx.FirstSeen = "" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, pm := chStore(), chPM()
			tx := chTx(1, 1, 5, 2, at)
			obs := chObservation(tx, chC)
			c.setup(tx, pm, obs)
			s.indexResolvedPathHops(tx, []string{chC}, map[string]bool{}, pm, obs)
			assertCertainRelay(t, s, chC, "")
		})
	}

	s, pm := chStore(), chPM()
	for i, stamp := range []string{
		"2026-10-20T12:00:00.100Z", "2026-10-20T12:00:00Z",
		"2026-10-20T13:00:00+02:00", "not-a-date", "",
		"2026-10-20T12:00:00.101Z",
	} {
		tx := chTx(i+1, 1, 5, 2, at)
		tx.FirstSeen, tx.LatestSeen = stamp, at.Add(48*time.Hour).Format(time.RFC3339)
		obs := chObservation(tx, chC)
		obs.Timestamp = tx.LatestSeen
		s.indexResolvedPathHops(tx, []string{chC}, map[string]bool{}, pm, obs)
		want := "2026-10-20T12:00:00.100Z"
		if i == 5 {
			want = stamp
		}
		assertCertainRelay(t, s, chC, want)
	}
	// The returned map is an independent snapshot, normalized on lookup,
	// and contains only the requested keys. Unknown membership cannot grow it.
	snapshot := s.LastCertainRelayMap([]string{strings.ToUpper(chC), chA})
	if len(snapshot) != 1 || snapshot[strings.ToUpper(chC)].IsZero() {
		t.Errorf("unexpected snapshot: %v", snapshot)
	}
	delete(snapshot, strings.ToUpper(chC))
	assertCertainRelay(t, s, chC, "2026-10-20T12:00:00.101Z")
	tx := chTx(99, 1, 5, 2, at)
	s.noteCertainRelays(tx, chObservation(tx, chA), pm)
	assertCertainRelay(t, s, chA, "")
}

func TestCertainRelayEvictedWithNode(t *testing.T) {
	db, w, _ := lpFixture(t, true, true)
	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	if _, err := w.Exec(`UPDATE transmissions SET first_seen=?`, old); err != nil {
		t.Fatal(err)
	}
	s := NewPacketStore(db, &PacketStoreConfig{})
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if !s.WaitIndexesReady(5 * time.Second) {
		t.Fatal("indexes not ready")
	}
	assertCertainRelay(t, s, lpRelay, old)
	s.retentionHours = 24
	s.mu.Lock()
	s.EvictStaleWithRP(map[int][]string{1: {lpRelay}})
	s.mu.Unlock()
	if len(s.byNode[lpRelay]) != 0 {
		t.Fatal("fixture node membership was not evicted")
	}
	assertCertainRelay(t, s, lpRelay, "")
}

func TestNodeEndpointsLastCertainRelay(t *testing.T) {
	srv, router := setupTestServer(t)
	const repeater, room, companion = "aabbccdd11223344", "1122334455667788", "eeff00112233aabb"
	const unheard = "ab" + "00000000000000000000000000000000000000000000000000000000000000"
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes(public_key, name, role) VALUES(?, 'No relay', 'repeater')`, unheard); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	pm := buildPrefixMap([]nodeInfo{{PublicKey: repeater, Role: "repeater"}, {PublicKey: room, Role: "room"}})
	srv.store.mu.Lock()
	for i, pk := range []string{repeater, room} {
		chIndexResolved(srv.store, chTx(9000+i, 1, 5, 2, at), []string{pk}, map[string]bool{}, pm)
	}
	srv.store.mu.Unlock()
	legacy := srv.store.GetRepeaterRelayInfo(repeater, 24)
	type node struct {
		PublicKey        string  `json:"public_key"`
		LastCertainRelay *string `json:"last_certain_relay"`
		LastRelayed      string  `json:"last_relayed"`
		RelayActive      bool    `json:"relay_active"`
		RelayCount1h     int     `json:"relay_count_1h"`
		RelayCount24h    int     `json:"relay_count_24h"`
	}
	get := func(url string, out interface{}) {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		if w.Code != 200 {
			t.Fatalf("%s: status %d", url, w.Code)
		}
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	var list struct {
		Nodes []node `json:"nodes"`
	}
	get("/api/nodes?limit=50", &list)
	seen := make(map[string]bool)
	for _, n := range list.Nodes {
		seen[n.PublicKey] = true
		var detail struct {
			Node node `json:"node"`
		}
		get("/api/nodes/"+n.PublicKey, &detail)
		if !reflect.DeepEqual(n, detail.Node) {
			t.Errorf("list/detail mismatch: %+v / %+v", n, detail.Node)
		}
		if n.PublicKey == repeater || n.PublicKey == room {
			if n.LastCertainRelay == nil || *n.LastCertainRelay != at.Format(time.RFC3339) {
				t.Errorf("%s last_certain_relay = %v; want %s", n.PublicKey, n.LastCertainRelay, at)
			}
		} else if n.LastCertainRelay != nil {
			t.Errorf("%s has no certain relay, got %q", n.PublicKey, *n.LastCertainRelay)
		}
		if n.PublicKey == repeater && (n.LastRelayed != legacy.LastRelayed || n.RelayActive != legacy.RelayActive || n.RelayCount1h != legacy.RelayCount1h || n.RelayCount24h != legacy.RelayCount24h) {
			t.Errorf("legacy relay fields changed: %+v vs %+v", n, legacy)
		}
	}
	for _, pk := range []string{repeater, room, companion, unheard} {
		if !seen[pk] {
			t.Errorf("fixture node missing: %s", pk)
		}
	}

	srv.cfg.HealthThresholds = &HealthThresholds{RelayActiveHours: 1.5}
	var config struct {
		HealthThresholds struct {
			RelayActiveMs int `json:"relayActiveMs"`
		} `json:"healthThresholds"`
	}
	get("/api/config/client", &config)
	if config.HealthThresholds.RelayActiveMs != 5400000 {
		t.Errorf("relayActiveMs = %d; want 5400000 from existing setting", config.HealthThresholds.RelayActiveMs)
	}
}

func TestOpenAPILastCertainRelay(t *testing.T) {
	spec := fetchSpec(t)
	schemas := asMap(t, asMap(t, spec["components"], "components")["schemas"], "schemas")
	props := asMap(t, asMap(t, schemas["Node"], "Node")["properties"], "Node.properties")
	field := asMap(t, props["last_certain_relay"], "last_certain_relay")
	if field["type"] != "string" {
		t.Errorf("last_certain_relay type = %v", field["type"])
	}
	desc, _ := field["description"].(string)
	for _, word := range []string{"first_seen", "observation", "retired", "omitted"} {
		if !strings.Contains(desc, word) {
			t.Errorf("last_certain_relay must document %q", word)
		}
	}
}
