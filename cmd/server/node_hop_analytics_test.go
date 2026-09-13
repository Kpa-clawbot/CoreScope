package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// Issue #1812: per-node hop count, as the repeater's own flood.max check sees
// it. The firmware compares getPathHashCount() (the number of hashes already in
// the path when the packet arrives) against the limits before appending its own
// hash, so the target's zero-based index in an observed flood path is exactly
// that count.

const (
	hopTarget    = "ab12cd34ef567890"
	hopOther     = "77aa88bb99cc0011"
	hopCollider  = "ab99ffee00112233" // shares the 1-byte prefix "ab" with hopTarget
	hopListener  = "ab12ffff00000000" // shares the 2-byte prefix "ab12", listener only
	hopBridge    = "88cc000000000000" // neighbor of hopCollider only
	hopBoth      = "55dd000000000000" // neighbor of hopTarget and hopCollider
	hopTwinA     = "99aa000000000000" // shares the 1-byte prefix "99" with hopTwinB
	hopTwinB     = "99bb000000000000"
	hopTimestamp = "2026-09-10T10:00:00Z"
)

func hopTx(id int, hash string, routeType, payloadType int, paths ...string) *StoreTx {
	rt, pt := routeType, payloadType
	tx := &StoreTx{ID: id, Hash: hash, FirstSeen: hopTimestamp, RouteType: &rt, PayloadType: &pt}
	for i, p := range paths {
		tx.Observations = append(tx.Observations, &StoreObs{ID: id*10 + i, TransmissionID: id, PathJSON: p})
	}
	return tx
}

func hopPM() *prefixMap {
	return buildPrefixMap([]nodeInfo{
		{PublicKey: hopTarget, Role: "repeater"},
		{PublicKey: hopOther, Role: "repeater"},
		{PublicKey: hopCollider, Role: "repeater"},
		{PublicKey: hopBridge, Role: "repeater"},
		{PublicKey: hopBoth, Role: "repeater"},
		{PublicKey: hopTwinA, Role: "repeater"},
		{PublicKey: hopTwinB, Role: "repeater"},
	})
}

// hopGraph: hopOther and hopTwinB neighbor hopTarget, hopBridge neighbors
// hopCollider, hopBoth neighbors both, and hopOther and hopBoth neighbor both
// twins.
func hopGraph() *NeighborGraph {
	g := NewNeighborGraph()
	now := time.Now()
	for _, e := range [][2]string{
		{hopOther, hopTarget},
		{hopTwinB, hopTarget},
		{hopBridge, hopCollider},
		{hopBoth, hopTarget},
		{hopBoth, hopCollider},
		{hopOther, hopTwinA},
		{hopOther, hopTwinB},
		{hopBoth, hopTwinA},
		{hopBoth, hopTwinB},
	} {
		g.upsertEdge(e[0], e[1], "", "obs", nil, now)
	}
	return g
}

func hopsOf(packets []NodeHopPacket) []int {
	out := make([]int, 0, len(packets))
	for _, p := range packets {
		out = append(out, p.Hops)
	}
	return out
}

func hashesOf(packets []NodeHopPacket) []string {
	out := make([]string, 0, len(packets))
	for _, p := range packets {
		out = append(out, p.Hash)
	}
	return out
}

func TestNodeHopPackets_IndexIsHopCountAtDifferentPositions(t *testing.T) {
	txs := []*StoreTx{
		hopTx(1, "h1", RouteFlood, PayloadGRP_TXT, `["AB12"]`),
		hopTx(2, "h2", RouteFlood, PayloadGRP_TXT, `["77AA","AB12"]`),
		hopTx(3, "h3", RouteTransportFlood, PayloadGRP_TXT, `["77AA","77AA","77AA","AB12","77AA"]`),
	}
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, "", hopPM(), nil)
	if got, want := hopsOf(packets), []int{0, 1, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hops = %v, want %v (zero-based index of the node in the path, no +1)", got, want)
	}
	if ambiguous != 0 {
		t.Errorf("ambiguous = %d, want 0", ambiguous)
	}
	if packets[1].Hash != "h2" || packets[1].Timestamp != hopTimestamp {
		t.Errorf("packet[1] = %+v, want hash h2 and timestamp %s", packets[1], hopTimestamp)
	}
}

func TestNodeHopPackets_Tags(t *testing.T) {
	txs := []*StoreTx{
		hopTx(1, "unscoped", RouteFlood, PayloadGRP_TXT, `["AB12"]`),
		hopTx(2, "scoped", RouteTransportFlood, PayloadGRP_TXT, `["AB12"]`),
		hopTx(3, "advert", RouteFlood, PayloadADVERT, `["AB12"]`),
		hopTx(4, "scoped-advert", RouteTransportFlood, PayloadADVERT, `["AB12"]`),
	}
	packets, _ := computeNodeHopPackets(hopTarget, txs, "", hopPM(), nil)
	want := map[string][]string{
		"unscoped":      {"flood", "unscoped"},
		"scoped":        {"flood", "scoped"},
		"advert":        {"flood", "unscoped", "advert"},
		"scoped-advert": {"flood", "scoped", "advert"},
	}
	if len(packets) != len(want) {
		t.Fatalf("got %d packets, want %d", len(packets), len(want))
	}
	for _, p := range packets {
		if !reflect.DeepEqual(p.Tags, want[p.Hash]) {
			t.Errorf("%s tags = %v, want %v", p.Hash, p.Tags, want[p.Hash])
		}
	}
}

func TestNodeHopPackets_DirectRoutesExcluded(t *testing.T) {
	txs := []*StoreTx{
		hopTx(1, "direct", RouteDirect, PayloadTXT_MSG, `["77AA","AB12"]`),
		hopTx(2, "tdirect", RouteTransportDirect, PayloadTXT_MSG, `["AB12"]`),
	}
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, "", hopPM(), nil)
	if len(packets) != 0 || ambiguous != 0 {
		t.Fatalf("direct packets must be skipped: packets=%v ambiguous=%d", packets, ambiguous)
	}
}

func TestNodeHopPackets_OneEventPerPacketAcrossObservations(t *testing.T) {
	txs := []*StoreTx{
		hopTx(1, "h1", RouteFlood, PayloadGRP_TXT, `["77AA","AB12"]`, `["77AA","AB12","77AA"]`, `["77AA"]`),
	}
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, "", hopPM(), nil)
	if got := hopsOf(packets); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("hops = %v, want [1] (one event per packet hash)", got)
	}
	if ambiguous != 0 {
		t.Errorf("ambiguous = %d, want 0", ambiguous)
	}
}

func TestNodeHopPackets_NotInPathIsNeitherCountedNorAmbiguous(t *testing.T) {
	txs := []*StoreTx{hopTx(1, "h1", RouteFlood, PayloadGRP_TXT, `["77AA"]`, `[]`)}
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, "", hopPM(), nil)
	if len(packets) != 0 || ambiguous != 0 {
		t.Fatalf("packets=%v ambiguous=%d, want none", packets, ambiguous)
	}
}

func TestNodeHopPackets_OriginatorNeverForwardsOwnFlood(t *testing.T) {
	tx := hopTx(1, "own-advert", RouteFlood, PayloadADVERT, `["AB12"]`)
	tx.DecodedJSON = `{"pubKey":"` + hopTarget + `"}`
	packets, ambiguous := computeNodeHopPackets(hopTarget, []*StoreTx{tx}, "", hopPM(), hopGraph())
	if len(packets) != 0 || ambiguous != 0 {
		t.Fatalf("own advert: packets=%v ambiguous=%d, want none", packets, ambiguous)
	}
}

// A colliding prefix counts for the node only under the ingestor's strict rule
// (cmd/ingestor/path_resolver.go resolvePathWithContext): the previous hop is
// itself identified without a tiebreak, and exactly one candidate is its
// graph neighbor. The server resolver's pick (affinity, GPS, advert count)
// plays no part.
func TestNodeHopPackets_CollisionNeedsStrictNeighborAttribution(t *testing.T) {
	advert := func(id int, hash, origin string, path string) *StoreTx {
		tx := hopTx(id, hash, RouteFlood, PayloadADVERT, path)
		tx.DecodedJSON = `{"pubKey":"` + origin + `"}`
		return tx
	}
	txs := []*StoreTx{
		hopTx(1, "after-neighbor", RouteFlood, PayloadGRP_TXT, `["77","AB"]`),
		hopTx(2, "no-previous-hop", RouteFlood, PayloadGRP_TXT, `["AB","77"]`),
		hopTx(3, "after-collider-neighbor", RouteFlood, PayloadGRP_TXT, `["88","AB"]`),
		hopTx(4, "after-common-neighbor", RouteFlood, PayloadGRP_TXT, `["55","AB"]`),
		hopTx(5, "previous-hop-ambiguous", RouteFlood, PayloadGRP_TXT, `["99","AB"]`),
		hopTx(6, "previous-hop-repeats", RouteFlood, PayloadGRP_TXT, `["77","88","77","AB"]`),
		advert(7, "advert-from-neighbor", hopOther, `["AB"]`),
		advert(8, "advert-from-stranger", hopBridge, `["AB"]`),
		hopTx(9, "observations-disagree", RouteFlood, PayloadGRP_TXT, `["77","AB"]`, `["88","AB"]`),
		hopTx(10, "one-observation-unresolved", RouteFlood, PayloadGRP_TXT, `["77","AB"]`, `["99","AB"]`),
		hopTx(11, "observations-without-node", RouteFlood, PayloadGRP_TXT, `["77","88"]`, `["77"]`, `["77","AB"]`),
		hopTx(12, "chain-broken", RouteFlood, PayloadGRP_TXT, `["77","99","AB"]`),
		hopTx(13, "origin-of-non-advert", RouteFlood, PayloadTXT_MSG, `["AB"]`),
		advert(14, "originator-excluded-later", hopTwinA, `["55","99","AB"]`),
	}
	txs[12].DecodedJSON = `{"pubKey":"` + hopOther + `"}`
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, "", hopPM(), hopGraph())
	got := map[string]int{}
	for _, p := range packets {
		got[p.Hash] = p.Hops
	}
	want := map[string]int{"after-neighbor": 1, "advert-from-neighbor": 0, "one-observation-unresolved": 1,
		"observations-without-node": 1, "originator-excluded-later": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attributed = %v, want %v", got, want)
	}
	if ambiguous != 9 {
		t.Errorf("ambiguous = %d, want 9", ambiguous)
	}
}

func TestNodeHopPackets_CollisionWithoutGraphIsAmbiguous(t *testing.T) {
	txs := []*StoreTx{hopTx(1, "h1", RouteFlood, PayloadGRP_TXT, `["77","AB"]`)}
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, "", hopPM(), nil)
	if len(packets) != 0 || ambiguous != 1 {
		t.Fatalf("packets=%v ambiguous=%d, want none and 1", packets, ambiguous)
	}
}

func TestNodeHopPackets_PrefixAtSeveralPositionsIsAmbiguous(t *testing.T) {
	txs := []*StoreTx{
		hopTx(1, "same-path", RouteFlood, PayloadGRP_TXT, `["AB","77","AB"]`),
		hopTx(2, "across-obs", RouteFlood, PayloadGRP_TXT, `["AB"]`, `["77","AB"]`),
	}
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, "", hopPM(), hopGraph())
	if len(packets) != 0 {
		t.Fatalf("packets = %+v, want none: a node forwards a flood once, so two positions mean a collision", packets)
	}
	if ambiguous != 2 {
		t.Errorf("ambiguous = %d, want 2", ambiguous)
	}
}

func TestNodeHopPackets_ListenerDoesNotMakePrefixAmbiguous(t *testing.T) {
	pm := buildPrefixMap([]nodeInfo{
		{PublicKey: hopTarget, Role: "repeater"},
		{PublicKey: hopListener, Role: "repeater"},
	})
	pm.markNonRelay([]string{hopListener})
	txs := []*StoreTx{hopTx(1, "h1", RouteFlood, PayloadGRP_TXT, `["AB12","77AA"]`)}
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, "", pm, nil)
	if got := hopsOf(packets); !reflect.DeepEqual(got, []int{0}) || ambiguous != 0 {
		t.Fatalf("hops=%v ambiguous=%d, want [0] and 0", got, ambiguous)
	}
}

// When the node itself is a listener, the only relay candidate for its prefix
// is another node: nothing may be attributed to it.
func TestNodeHopPackets_OnlyRelayCandidateIsAnotherNode(t *testing.T) {
	pm := buildPrefixMap([]nodeInfo{
		{PublicKey: hopTarget, Role: "repeater"},
		{PublicKey: hopCollider, Role: "repeater"},
	})
	pm.markNonRelay([]string{hopTarget})
	txs := []*StoreTx{hopTx(1, "h1", RouteFlood, PayloadGRP_TXT, `["AB"]`)}
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, "", pm, nil)
	if len(packets) != 0 || ambiguous != 1 {
		t.Fatalf("packets=%v ambiguous=%d, want none and 1", packets, ambiguous)
	}
}

// Uniqueness is per prefix: "ab" collides with hopCollider, "ab12" does not.
func TestNodeHopPackets_UniquenessIsPerPrefixLength(t *testing.T) {
	txs := []*StoreTx{
		hopTx(1, "two-byte", RouteFlood, PayloadGRP_TXT, `["AB12"]`),
		hopTx(2, "one-byte", RouteFlood, PayloadGRP_TXT, `["AB"]`),
		hopTx(3, "two-byte-again", RouteFlood, PayloadGRP_TXT, `["AB12"]`),
		hopTx(4, "one-byte-again", RouteFlood, PayloadGRP_TXT, `["AB"]`),
	}
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, "", hopPM(), nil)
	if got, want := hashesOf(packets), []string{"two-byte", "two-byte-again"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attributed = %v, want %v", got, want)
	}
	if ambiguous != 2 {
		t.Errorf("ambiguous = %d, want 2", ambiguous)
	}
}

// ─── Store level: live ingest and cold load ────────────────────────────────

func hopInsertNode(t *testing.T, db *DB, pk, name string, adverts int) {
	t.Helper()
	mustExec(t, db, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES (?, ?, 'repeater', ?, '2026-01-01', ?)`, pk, name, time.Now().UTC().Format(time.RFC3339), adverts)
}

func hopInsertTx(t *testing.T, db *DB, id int, hash string, routeType, payloadType int, paths ...string) {
	t.Helper()
	ts := time.Now().Add(-1 * time.Hour)
	mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type) VALUES (?, 'AA', ?, ?, ?, ?)`,
		id, hash, ts.UTC().Format(time.RFC3339), routeType, payloadType)
	for _, p := range paths {
		mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (?, NULL, ?, ?, NULL)`,
			id, p, ts.Unix())
	}
}

func hopLoadedStore(t *testing.T, db *DB) *PacketStore {
	t.Helper()
	store := NewPacketStore(db, nil)
	store.graph.Store(loadNeighborEdgesFromDB(db.conn))
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if !store.WaitIndexesReady(10 * time.Second) {
		t.Fatal("indexes not ready")
	}
	return store
}

func hopAnalytics(t *testing.T, store *PacketStore, pk string) *NodeHopAnalyticsResponse {
	t.Helper()
	resp, err := store.GetNodeHopAnalytics(pk, 7)
	if err != nil || resp == nil {
		t.Fatalf("GetNodeHopAnalytics(%s): resp=%v err=%v", pk, resp, err)
	}
	sort.Slice(resp.Packets, func(i, j int) bool { return resp.Packets[i].Hash < resp.Packets[j].Hash })
	return resp
}

// Review repro: live ingest resolves a colliding hop with a tiebreak (here the
// advert count). That pick must not become a hop count.
func TestNodeHopAnalytics_LiveIngestDoesNotTrustResolverPick(t *testing.T) {
	db := setupTestDB(t)
	hopInsertNode(t, db, hopTarget, "Target", 500)
	hopInsertNode(t, db, hopCollider, "Collider", 1)
	store := hopLoadedStore(t, db)

	hopInsertTx(t, db, 1, "collides", RouteFlood, PayloadGRP_TXT, `["AB"]`)
	if _, maxID := store.IngestNewFromDB(0, 100); maxID != 1 {
		t.Fatalf("IngestNewFromDB maxID = %d, want 1", maxID)
	}

	for _, pk := range []string{hopTarget, hopCollider} {
		resp := hopAnalytics(t, store, pk)
		if len(resp.Packets) != 0 || resp.Ambiguous != 1 {
			t.Errorf("%s: packets=%+v ambiguous=%d, want none and 1", pk, resp.Packets, resp.Ambiguous)
		}
	}
}

// Live ingest and a cold load of the same DB must give the same answer: hops
// are read from every observation's raw path and attributed from the prefix
// map and neighbor graph, not from what each load path indexed.
func TestNodeHopAnalytics_SameResultAfterRestart(t *testing.T) {
	db := setupTestDB(t)
	mustExec(t, db, `CREATE TABLE neighbor_edges (node_a TEXT NOT NULL, node_b TEXT NOT NULL,
		count INTEGER DEFAULT 1, last_seen TEXT, PRIMARY KEY (node_a, node_b))`)
	mustExec(t, db, `INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES (?, ?, 50, ?)`,
		hopOther, hopTarget, time.Now().UTC().Format(time.RFC3339))
	hopInsertNode(t, db, hopTarget, "Target", 1)
	hopInsertNode(t, db, hopCollider, "Collider", 500)
	hopInsertNode(t, db, hopOther, "Other", 1)
	live := hopLoadedStore(t, db)

	hopInsertTx(t, db, 1, "after-neighbor", RouteFlood, PayloadGRP_TXT, `["77","AB"]`, `["77","AB","99"]`)
	hopInsertTx(t, db, 2, "no-previous-hop", RouteFlood, PayloadGRP_TXT, `["AB"]`)
	hopInsertTx(t, db, 3, "two-byte", RouteTransportFlood, PayloadGRP_TXT, `["77AA","AB12"]`)
	hopInsertTx(t, db, 4, "three-byte", RouteFlood, PayloadGRP_TXT, `["77AA88","77AA88","AB12CD"]`)
	// The longest observation runs through another branch of the flood.
	hopInsertTx(t, db, 5, "not-on-longest-path", RouteFlood, PayloadGRP_TXT, `["77AA","77AA","77AA"]`, `["77AA","AB12"]`)
	live.IngestNewFromDB(0, 100)
	fromLive := hopAnalytics(t, live, hopTarget)

	restarted := hopAnalytics(t, hopLoadedStore(t, db), hopTarget)

	want := []NodeHopPacket{
		{Hash: "after-neighbor", Hops: 1, Tags: hopTagsUnscoped},
		{Hash: "not-on-longest-path", Hops: 1, Tags: hopTagsUnscoped},
		{Hash: "three-byte", Hops: 2, Tags: hopTagsUnscoped},
		{Hash: "two-byte", Hops: 1, Tags: hopTagsScoped},
	}
	for name, resp := range map[string]*NodeHopAnalyticsResponse{"live ingest": fromLive, "cold load": restarted} {
		got := make([]NodeHopPacket, len(resp.Packets))
		for i, p := range resp.Packets {
			got[i] = NodeHopPacket{Hash: p.Hash, Hops: p.Hops, Tags: p.Tags}
		}
		if !reflect.DeepEqual(got, want) || resp.Ambiguous != 1 {
			t.Errorf("%s: packets=%+v ambiguous=%d, want %+v and 1", name, got, resp.Ambiguous, want)
		}
	}
	if !reflect.DeepEqual(fromLive.Packets, restarted.Packets) || fromLive.Ambiguous != restarted.Ambiguous {
		t.Errorf("live ingest %+v/%d differs from cold load %+v/%d",
			fromLive.Packets, fromLive.Ambiguous, restarted.Packets, restarted.Ambiguous)
	}
}

// End to end through the route: days window, response shape, 404.
func TestHandleNodeHopAnalytics(t *testing.T) {
	db := setupTestDB(t)
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := time.Now().Add(-1 * time.Hour).Unix()
	old := time.Now().Add(-3 * 24 * time.Hour).Format(time.RFC3339)
	oldEpoch := time.Now().Add(-3 * 24 * time.Hour).Unix()

	hopInsertNode(t, db, hopTarget, "Target", 1)
	hopInsertNode(t, db, hopOther, "Other", 1)
	hopInsertNode(t, db, hopCollider, "Collider", 1)

	mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type) VALUES (1, 'AA', 'hop_recent_2', ?, 1, 5)`, recent)
	mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (1, NULL, '["77AA","77AA","AB12"]', ?)`, recentEpoch)
	mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type) VALUES (2, 'BB', 'hop_recent_1byte', ?, 0, 4)`, recent)
	mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (2, NULL, '["AB"]', ?)`, recentEpoch)
	mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type) VALUES (3, 'CC', 'hop_scoped_advert', ?, 0, 4)`, recent)
	mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (3, NULL, '["AB12"]', ?)`, recentEpoch)
	mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type) VALUES (4, 'DD', 'hop_old', ?, 1, 5)`, old)
	mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (4, NULL, '["AB12"]', ?)`, oldEpoch)

	srv := NewServer(db, &Config{Port: 3000}, NewHub())
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	get := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		return w
	}

	srv.store = hopLoadedStore(t, db)

	w := get("/api/nodes/" + hopTarget + "/hop_analytics?days=1")
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var resp NodeHopAnalyticsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	byHash := map[string]NodeHopPacket{}
	for _, p := range resp.Packets {
		byHash[p.Hash] = p
	}
	if len(resp.Packets) != 2 {
		t.Fatalf("packets = %+v, want the two attributable packets inside the 1-day window", resp.Packets)
	}
	if p := byHash["hop_recent_2"]; p.Hops != 2 || p.Timestamp != recent || !reflect.DeepEqual(p.Tags, []string{"flood", "unscoped"}) {
		t.Errorf("hop_recent_2 = %+v, want hops 2, timestamp %s, tags [flood unscoped]", p, recent)
	}
	if p := byHash["hop_scoped_advert"]; p.Hops != 0 || !reflect.DeepEqual(p.Tags, []string{"flood", "scoped", "advert"}) {
		t.Errorf("hop_scoped_advert = %+v, want hops 0 tags [flood scoped advert]", p)
	}
	if resp.TimeRange.Days != 1 || resp.Ambiguous != 1 {
		t.Errorf("timeRange.days=%d ambiguous=%d, want 1 and 1 (colliding 1-byte prefix, no neighbor graph)", resp.TimeRange.Days, resp.Ambiguous)
	}

	if w := get("/api/nodes/" + hopTarget + "/hop_analytics?days=7"); w.Code != http.StatusOK || !json.Valid(w.Body.Bytes()) {
		t.Fatalf("days=7: code=%d", w.Code)
	} else {
		var wide NodeHopAnalyticsResponse
		_ = json.Unmarshal(w.Body.Bytes(), &wide)
		if len(wide.Packets) != 3 {
			t.Errorf("days=7 packets = %d, want 3", len(wide.Packets))
		}
	}

	if w := get("/api/nodes/ffffffffffffffff/hop_analytics"); w.Code != http.StatusNotFound {
		t.Errorf("unknown node: code=%d, want 404", w.Code)
	}
}

// BenchmarkNodeHopPackets sizes one request for a busy repeater over 7 days,
// as counted in a 1,669-node mesh DB on 2026-09-13: 73,782 flood packets
// with 1,430,280 observations (19 per packet). A third of the packets pass the
// node under a unique 2-byte prefix, a sixth under a 1-byte prefix shared with
// another relay (strict neighbor walk), the rest do not pass it; in each, half
// of the observations contain the node. Observation slices are shared between
// packets to keep the fixture small; the scan only reads them.
func BenchmarkNodeHopPackets(b *testing.B) {
	obsOf := func(self, other string) []*StoreObs {
		through := `["` + other + `","` + self + `","` + other + `","` + other + `","` + other + `","` + other + `"]`
		elsewhere := `["` + other + `","` + other + `","` + other + `","` + other + `","` + other + `","` + other + `"]`
		obs := make([]*StoreObs, 19)
		for j := range obs {
			obs[j] = &StoreObs{PathJSON: elsewhere}
			if j%2 == 0 {
				obs[j].PathJSON = through
			}
		}
		return obs
	}
	unique, colliding, elsewhere := obsOf("AB12", "77AA"), obsOf("AB", "77"), obsOf("88CC", "77AA")
	rt, pt := RouteFlood, PayloadGRP_TXT
	txs := make([]*StoreTx, 73782)
	for i := range txs {
		txs[i] = &StoreTx{ID: i, Hash: "h", FirstSeen: hopTimestamp, RouteType: &rt, PayloadType: &pt, Observations: elsewhere}
		switch i % 6 {
		case 0, 1:
			txs[i].Observations = unique
		case 2:
			txs[i].Observations = colliding
		}
	}
	pm, graph := hopPM(), hopGraph()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		computeNodeHopPackets(hopTarget, txs, "", pm, graph)
	}
}
