package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/meshcore-analyzer/packetpath"
	"github.com/meshcore-analyzer/users"
)

// Three repeaters: two share the first pubkey byte 0x67 (as on the
// instance in #2260, where nine nodes did), one has a unique first byte.
var (
	chA = "67a7" + strings.Repeat("1", 60) // the silent repeater
	chB = "67f4" + strings.Repeat("2", 60) // its 1-byte namesake, still relaying
	chC = "40aa" + strings.Repeat("3", 60) // unique first byte
)

func chPM() *prefixMap {
	return buildPrefixMap([]nodeInfo{
		{PublicKey: chA, Role: "repeater"},
		{PublicKey: chB, Role: "repeater"},
		{PublicKey: chC, Role: "repeater"},
	})
}

// chTx builds a transmission whose raw header carries the route type and the
// path hash size (firmware docs/packet_format.md: header, transport codes for
// transport routes, then the path byte with the hash size in its top bits).
func chTx(id, routeType, payloadType, hashSize int, firstSeen time.Time) *StoreTx {
	raw := fmt.Sprintf("%02x", payloadType<<2|routeType)
	if routeType == 0 || routeType == 3 {
		raw += "00000000"
	}
	raw += fmt.Sprintf("%02x", (hashSize-1)<<6|1) + strings.Repeat("ab", hashSize) + "00"
	pt := payloadType
	return &StoreTx{ID: id, RawHex: raw, PayloadType: &pt, FirstSeen: firstSeen.UTC().Format(time.RFC3339)}
}

// chObservation gives these index tests real observation evidence. Previously
// their resolved pubkeys named arbitrary hops while the raw frame held AB.
// Preserve existing observer identities used by the adaptive learner tests.
func chObservation(tx *StoreTx, pks ...string) *StoreObs {
	route, pathByte, _ := packetpath.PathByte(tx.RawHex)
	n := 2 * (int(pathByte>>6) + 1)
	offset := 2
	if route == 0 || route == 3 {
		offset += 8
	}
	hops := make([]string, 0, len(pks))
	for _, pk := range pks {
		hops = append(hops, strings.ToUpper(pk[:n]))
	}
	tx.RawHex = tx.RawHex[:offset] + fmt.Sprintf("%02x", pathByte&0xc0|byte(len(hops))) + strings.Join(hops, "") + "00"
	pj, _ := json.Marshal(hops)
	if len(tx.Observations) == 0 {
		tx.Observations = []*StoreObs{{}}
	}
	for _, obs := range tx.Observations {
		obs.PathJSON = string(pj)
		obs.relayHopHexLen = observedRelayHopHexLen(tx.RawHex, obs.PathJSON)
	}
	return tx.Observations[0]
}

func chIndexResolved(s *PacketStore, tx *StoreTx, pks []string, seen map[string]bool, pm *prefixMap) {
	obs := chObservation(tx, pks...)
	s.indexResolvedPathHops(tx, pks, seen, pm, obs)
}

func TestRelayHopHexLen(t *testing.T) {
	at := time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		tx   *StoreTx
		want int
	}{
		{"flood 1-byte", chTx(1, 1, 5, 1, at), 2},
		{"flood 2-byte", chTx(2, 1, 5, 2, at), 4},
		{"flood 3-byte", chTx(3, 1, 5, 3, at), 6},
		{"transport flood 2-byte", chTx(4, 0, 5, 2, at), 4},
		{"direct: path lists hops still to come", chTx(5, 2, 5, 1, at), 0},
		{"transport direct", chTx(6, 3, 5, 1, at), 0},
		{"TRACE: path bytes are SNR", chTx(7, 1, packetpathTrace, 1, at), 0},
		{"no raw", &StoreTx{ID: 8}, 0},
	}
	for _, c := range cases {
		if got := relayHopHexLen(c.tx.RawHex); got != c.want {
			t.Errorf("%s: %d; want %d", c.name, got, c.want)
		}
	}
}

const packetpathTrace = 0x09

func chStore() *PacketStore {
	s := makeTestStore(0, time.Now(), 0)
	s.byPathHop = make(map[string][]*StoreTx)
	s.initResolvedPathIndex()
	return s
}

func TestNoteCertainRelays(t *testing.T) {
	pm := chPM()
	t0 := time.Date(2026, 10, 18, 16, 49, 25, 0, time.UTC)
	s := chStore()
	hopsSeen := map[string]bool{}

	// 2-byte flood hop 67a7: only chA has it, certain.
	chIndexResolved(s, chTx(1, 1, 5, 2, t0), []string{chA}, hopsSeen, pm)
	// Later 1-byte flood hops 67 (shared with chB) and 40 (unique): the
	// resolver attributed 67 to chA, but only 40 is certain.
	later := t0.Add(70 * time.Hour)
	chIndexResolved(s, chTx(2, 1, 5, 1, later), []string{chA, chC}, hopsSeen, pm)
	// Direct route and TRACE never count; a nil prefix map records nothing.
	chIndexResolved(s, chTx(3, 2, 5, 2, later), []string{chA}, hopsSeen, pm)
	chIndexResolved(s, chTx(4, 1, packetpathTrace, 2, later), []string{chA}, hopsSeen, pm)
	chIndexResolved(s, chTx(5, 1, 5, 2, later), []string{chA}, hopsSeen, nil)

	if got := s.certainHeard[chA]; got != t0.Format(time.RFC3339) {
		t.Errorf("chA certain = %q; want the 2-byte hop at %s", got, t0.Format(time.RFC3339))
	}
	if got := s.certainHeard[chC]; got != later.Format(time.RFC3339) {
		t.Errorf("chC certain = %q; want the unique 1-byte hop", got)
	}
	if len(s.byNode[chA]) != 5 {
		t.Errorf("byNode[chA] = %d packets; attribution for analytics must be unchanged", len(s.byNode[chA]))
	}

	// The cold-load path (resolved_path NULL) feeds liveness the same way.
	tx := chTx(6, 1, 5, 2, later.Add(time.Hour))
	s.indexColdLoadRelayHops(tx, []string{chB}, pm, chObservation(tx, chB))
	if got := s.certainHeard[chB]; got != later.Add(time.Hour).Format(time.RFC3339) {
		t.Errorf("chB certain from cold load = %q", got)
	}
}

func TestIndexByNodeSenderIsCertain(t *testing.T) {
	s := chStore()
	at := time.Date(2026, 10, 18, 16, 45, 51, 0, time.UTC)
	tx := chTx(1, 1, 4, 1, at)
	tx.DecodedJSON = fmt.Sprintf(`{"pubKey":%q,"destPubKey":%q}`, chA, chC)
	s.indexByNode(tx)
	if s.certainHeard[chA] != at.Format(time.RFC3339) {
		t.Errorf("sender not certain: %q", s.certainHeard[chA])
	}
	if _, ok := s.certainHeard[chC]; ok {
		t.Error("a destination is no evidence the node is alive")
	}
}

// #2260 regression: a repeater last certainly heard on 18 Oct, after which
// only its 1-byte namesake prefix shows up in paths. Liveness must stay at
// 18 Oct and the offline mail must go out once the 72 h window has passed.
func TestSilentRepeaterWithNamesakePrefixGoesOffline(t *testing.T) {
	pm := chPM()
	lastCertain := time.Date(2026, 10, 18, 16, 49, 25, 0, time.UTC)
	s := chStore()
	hopsSeen := map[string]bool{}
	chIndexResolved(s, chTx(1, 1, 5, 2, lastCertain), []string{chA}, hopsSeen, pm)
	for i := 0; i < 20; i++ {
		ts := lastCertain.Add(time.Duration(70+i) * time.Hour)
		chIndexResolved(s, chTx(2+i, 1, 5, 1, ts), []string{chA}, hopsSeen, pm)
	}

	heard, _ := s.LastHeardMap([]string{chA})
	if !heard[chA].Equal(lastCertain) {
		t.Fatalf("LastHeardMap = %v; want %v", heard[chA], lastCertain)
	}

	in := evInput()
	in.Now = lastCertain.Add(73 * time.Hour)
	in.Watches = []users.NotifyWatch{{UserID: evUser, Pubkey: chA}}
	in.Nodes[chA] = notifyNode{Pubkey: chA, Name: "Silent", Role: "repeater", LastSeen: lastCertain.Add(-4 * time.Minute).Format(time.RFC3339)}
	in.Heard = heard
	if got, _ := evState(evaluateNotifications(in), evUser, users.NotifyNodeOffline, chA); got != users.NotifyBad {
		t.Fatalf("node.offline = %q; want bad", got)
	}
}

func TestCertainHeardEvictedWithNode(t *testing.T) {
	now := time.Now().UTC()
	s := makeTestStore(4, now.Add(-48*time.Hour), 0)
	for i, tx := range s.packets {
		s.noteCertainHeard(fmt.Sprintf("pk%04d", i), tx) // makeTestStore's sender keys
	}
	if len(s.certainHeard) != 4 {
		t.Fatalf("certainHeard = %d entries; want 4", len(s.certainHeard))
	}
	s.retentionHours = 24
	s.EvictStale()
	if len(s.certainHeard) != 0 {
		t.Fatalf("certainHeard = %v after evicting every packet; want empty", s.certainHeard)
	}
}

func TestLastHeardValue(t *testing.T) {
	heard := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		lastSeen interface{}
		heard    time.Time
		want     interface{}
	}{
		{"certain newer than advert", "2026-10-19T08:00:00Z", heard, "2026-10-22T09:00:00.000Z"},
		{"advert newer", "2026-10-22T10:00:00Z", heard, "2026-10-22T10:00:00Z"},
		{"nothing certain", "2026-10-19T08:00:00Z", time.Time{}, "2026-10-19T08:00:00Z"},
		{"never advertised", nil, heard, "2026-10-22T09:00:00.000Z"},
		{"nothing at all", nil, time.Time{}, nil},
	}
	for _, c := range cases {
		if got := lastHeardValue(c.lastSeen, c.heard); got != c.want {
			t.Errorf("%s: %v; want %v", c.name, got, c.want)
		}
	}
}

// /api/nodes and the node detail report the certain last heard, not a copy
// of last_seen, and the health endpoint's lastHeard is the same value.
func TestNodeEndpointsLastHeardIsCertain(t *testing.T) {
	srv, router := setupTestServer(t)
	const pk = "aabbccdd11223344"
	heardAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	srv.store.mu.Lock()
	srv.store.certainHeard = map[string]string{pk: heardAt.Format(time.RFC3339)}
	srv.store.mu.Unlock()

	get := func(url string, out interface{}) {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d", url, w.Code)
		}
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	type node struct {
		PublicKey string `json:"public_key"`
		LastSeen  string `json:"last_seen"`
		LastHeard string `json:"last_heard"`
	}
	want := heardAt.Format("2006-01-02T15:04:05.000Z")

	var list struct {
		Nodes []node `json:"nodes"`
	}
	get("/api/nodes?limit=50", &list)
	found := false
	for _, n := range list.Nodes {
		if n.PublicKey == pk {
			found = true
			if n.LastHeard != want || n.LastSeen == want {
				t.Errorf("list: last_heard %q last_seen %q; want last_heard %q", n.LastHeard, n.LastSeen, want)
			}
		} else if n.LastHeard != n.LastSeen {
			t.Errorf("list %s: nothing certain, last_heard %q must be last_seen %q", n.PublicKey, n.LastHeard, n.LastSeen)
		}
	}
	if !found {
		t.Fatalf("node %s not listed", pk)
	}

	var detail struct {
		Node node `json:"node"`
	}
	get("/api/nodes/"+pk, &detail)
	if detail.Node.LastHeard != want {
		t.Errorf("detail last_heard %q; want %q", detail.Node.LastHeard, want)
	}

	var health struct {
		Stats struct {
			LastHeard string `json:"lastHeard"`
		} `json:"stats"`
	}
	get("/api/nodes/"+pk+"/health", &health)
	if health.Stats.LastHeard != heardAt.Format(time.RFC3339) {
		t.Errorf("health lastHeard %q; want %q", health.Stats.LastHeard, heardAt.Format(time.RFC3339))
	}
}

// noteCertainRelays runs per observation on ingest, under the write lock.
// An 8-hop 2-byte flood path against a 2,000-repeater prefix map. The frame
// was validated once while loading; each call scans only the retained path.
func BenchmarkNoteCertainRelays(b *testing.B) {
	nodes := make([]nodeInfo, 0, 2000)
	for i := 0; i < 2000; i++ {
		nodes = append(nodes, nodeInfo{PublicKey: fmt.Sprintf("%04x%060x", i, i*7919), Role: "repeater"})
	}
	pm := buildPrefixMap(nodes)
	pks := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		pks = append(pks, nodes[i*250].PublicKey)
	}
	s := chStore()
	tx := chTx(1, 1, 5, 2, time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC))
	obs := chObservation(tx, pks...)
	s.indexResolvedPathHops(tx, pks, map[string]bool{}, pm, obs)
	b.ReportAllocs()
	b.ResetTimer()
	b.ReportMetric(float64(unsafe.Sizeof(StoreObs{})), "bytes/obs")
	for i := 0; i < b.N; i++ {
		s.noteCertainRelays(tx, obs, pm)
	}
}

func TestIndexByNodeFailedSignatureIsNotCertain(t *testing.T) {
	s := chStore()
	at := time.Date(2026, 10, 18, 16, 45, 51, 0, time.UTC)
	forged := chTx(1, 1, 4, 1, at)
	forged.DecodedJSON = fmt.Sprintf(`{"pubKey":%q,"signatureValid":false}`, chA)
	s.indexByNode(forged)
	if _, ok := s.certainHeard[chA]; ok {
		t.Fatal("an advert whose signature failed is no evidence")
	}
	if len(s.byNode[chA]) != 1 {
		t.Fatal("it is still indexed for analytics")
	}
	valid := chTx(2, 1, 4, 1, at)
	valid.DecodedJSON = fmt.Sprintf(`{"pubKey":%q,"signatureValid":true}`, chA)
	s.indexByNode(valid)
	if s.certainHeard[chA] != at.Format(time.RFC3339) {
		t.Fatalf("a verified advert is evidence: %q", s.certainHeard[chA])
	}
}

func TestNodeSearchLastHeardIsCertain(t *testing.T) {
	srv, router := setupTestServer(t)
	const pk = "aabbccdd11223344"
	heardAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	srv.store.mu.Lock()
	srv.store.certainHeard = map[string]string{pk: heardAt.Format(time.RFC3339)}
	srv.store.mu.Unlock()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/nodes/search?q=TestRepeater", nil))
	var body struct {
		Nodes []struct {
			PublicKey string `json:"public_key"`
			LastHeard string `json:"last_heard"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, n := range body.Nodes {
		if n.PublicKey == pk {
			if want := heardAt.Format("2006-01-02T15:04:05.000Z"); n.LastHeard != want {
				t.Fatalf("search last_heard %q; want %q", n.LastHeard, want)
			}
			return
		}
	}
	t.Fatalf("node not found in %s", w.Body.String())
}

// Review (#2261): a repeater moved to inactive_nodes by retention may still
// relay, so its prefix keeps a hop ambiguous although the nodes table has
// only one candidate.
func TestRetiredNamesakeKeepsHopUncertain(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.conn.Exec(`CREATE TABLE IF NOT EXISTS inactive_nodes (public_key TEXT PRIMARY KEY, name TEXT, role TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO inactive_nodes (public_key, role) VALUES ('40bb` + strings.Repeat("9", 60) + `', 'repeater')`,
		`INSERT INTO inactive_nodes (public_key, role) VALUES ('6700` + strings.Repeat("8", 60) + `', 'companion')`,
	} {
		if _, err := db.conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	pm := chPM()
	if err := pm.markRetired(db); err != nil {
		t.Fatal(err)
	}
	if uniqueHopPrefix(chC, 2, pm) {
		t.Error("1-byte hop 40 is shared with a retired repeater")
	}
	if !uniqueHopPrefix(chC, 4, pm) {
		t.Error("2-byte hop 40aa is still only chC")
	}
	if _, ok := pm.retired["67"]; ok {
		t.Error("a retired companion cannot relay; its prefix must not count")
	}
}

// Review (#2261): retention copies a row into inactive_nodes and nothing
// removes it when the node is heard again, so a node can be its own
// "retired namesake". That row must not take its certain relays away.
func TestReturnedNodeIsNotItsOwnRetiredNamesake(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.conn.Exec(`CREATE TABLE IF NOT EXISTS inactive_nodes (public_key TEXT PRIMARY KEY, name TEXT, role TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO nodes (public_key, role) VALUES ('` + chC + `', 'repeater')`,
		`INSERT INTO inactive_nodes (public_key, role) VALUES ('` + strings.ToUpper(chC) + `', 'repeater')`,
	} {
		if _, err := db.conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	pm := chPM()
	if err := pm.markRetired(db); err != nil {
		t.Fatal(err)
	}
	if !uniqueHopPrefix(chC, 4, pm) {
		t.Error("2-byte hop 40aa is only chC; its own stale inactive_nodes row must not count")
	}
}

// The owner of an "uncertain" repeater (#2260) gets the offline mail, worded
// so it says why: nothing certain, only its shared prefix still in paths.
func TestOfflineMailExplainsUncertain(t *testing.T) {
	last := evNow.Add(-80 * time.Hour)
	in := evInput()
	in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Name: "Silent", Role: "repeater", LastSeen: last.Format(time.RFC3339)}
	in.Heard[evPkA] = last
	in.SharedSeen = map[string]time.Time{evPkA: evNow.Add(-4 * time.Minute)}
	in.States = []users.NotifyState{evStateRow(evUser, users.NotifyNodeOffline, evPkA, users.NotifyGood)}
	ch := evaluateNotifications(in).Changes[evUser]
	if len(ch) != 1 || ch[0].To != users.NotifyBad || !ch[0].SharedSeen.Equal(evNow.Add(-4*time.Minute)) {
		t.Fatalf("changes = %+v; want one offline change carrying the shared-prefix time", ch)
	}
	txt := notifyChangeText(ch[0], "")
	if !strings.Contains(txt, "offline (nothing that can only be this node") || !strings.Contains(txt, "possibly another node") {
		t.Fatalf("mail line %q does not explain the uncertain state", txt)
	}

	// A shared-prefix sighting older than the last certain packet adds nothing.
	in.SharedSeen[evPkA] = last.Add(-time.Hour)
	if ch := evaluateNotifications(in).Changes[evUser]; len(ch) != 1 || !ch[0].SharedSeen.IsZero() {
		t.Fatalf("changes = %+v; want a plain offline change", ch)
	}
	if txt := notifyChangeText(evaluateNotifications(in).Changes[evUser][0], ""); !strings.HasSuffix(strings.SplitN(txt, ", ", 2)[0], "offline") {
		t.Fatalf("plain offline line = %q", txt)
	}
}
