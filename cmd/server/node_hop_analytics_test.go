package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	})
}

func hopsOf(packets []NodeHopPacket) []int {
	out := make([]int, 0, len(packets))
	for _, p := range packets {
		out = append(out, p.Hops)
	}
	return out
}

func TestNodeHopPackets_IndexIsHopCountAtDifferentPositions(t *testing.T) {
	txs := []*StoreTx{
		hopTx(1, "h1", RouteFlood, PayloadGRP_TXT, `["AB12"]`),
		hopTx(2, "h2", RouteFlood, PayloadGRP_TXT, `["77AA","AB12"]`),
		hopTx(3, "h3", RouteTransportFlood, PayloadGRP_TXT, `["77AA","77AA","77AA","AB12","77AA"]`),
	}
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, hopPM(), nil)
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
	packets, _ := computeNodeHopPackets(hopTarget, txs, hopPM(), nil)
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
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, hopPM(), nil)
	if len(packets) != 0 || ambiguous != 0 {
		t.Fatalf("direct packets must be skipped: packets=%v ambiguous=%d", packets, ambiguous)
	}
}

func TestNodeHopPackets_OneEventPerPacketAcrossObservations(t *testing.T) {
	txs := []*StoreTx{
		hopTx(1, "h1", RouteFlood, PayloadGRP_TXT, `["77AA","AB12"]`, `["77AA","AB12","77AA"]`, `["77AA"]`),
	}
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, hopPM(), nil)
	if got := hopsOf(packets); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("hops = %v, want [1] (one event per packet hash)", got)
	}
	if ambiguous != 0 {
		t.Errorf("ambiguous = %d, want 0", ambiguous)
	}
}

func TestNodeHopPackets_NotInPathIsNeitherCountedNorAmbiguous(t *testing.T) {
	txs := []*StoreTx{hopTx(1, "h1", RouteFlood, PayloadGRP_TXT, `["77AA"]`, `[]`)}
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, hopPM(), nil)
	if len(packets) != 0 || ambiguous != 0 {
		t.Fatalf("packets=%v ambiguous=%d, want none", packets, ambiguous)
	}
}

func TestNodeHopPackets_OriginatorNeverForwardsOwnFlood(t *testing.T) {
	tx := hopTx(1, "own-advert", RouteFlood, PayloadADVERT, `["AB12"]`)
	tx.DecodedJSON = `{"pubKey":"` + hopTarget + `"}`
	packets, ambiguous := computeNodeHopPackets(hopTarget, []*StoreTx{tx}, hopPM(), map[int]struct{}{1: {}})
	if len(packets) != 0 || ambiguous != 0 {
		t.Fatalf("own advert: packets=%v ambiguous=%d, want none", packets, ambiguous)
	}
}

func TestNodeHopPackets_ShortPrefixCollision(t *testing.T) {
	txs := []*StoreTx{
		hopTx(1, "confirmed", RouteFlood, PayloadGRP_TXT, `["77","AB"]`),
		hopTx(2, "unconfirmed", RouteFlood, PayloadGRP_TXT, `["AB","77"]`),
	}
	resolved := map[int]struct{}{1: {}}
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, hopPM(), resolved)
	if len(packets) != 1 || packets[0].Hash != "confirmed" || packets[0].Hops != 1 {
		t.Fatalf("packets = %+v, want only the resolved-path-confirmed packet at hop 1", packets)
	}
	if ambiguous != 1 {
		t.Errorf("ambiguous = %d, want 1 (colliding 1-byte prefix without resolved-path confirmation)", ambiguous)
	}
}

func TestNodeHopPackets_PrefixAtSeveralPositionsIsAmbiguous(t *testing.T) {
	txs := []*StoreTx{
		hopTx(1, "same-path", RouteFlood, PayloadGRP_TXT, `["AB","77","AB"]`),
		hopTx(2, "across-obs", RouteFlood, PayloadGRP_TXT, `["AB"]`, `["77","AB"]`),
	}
	resolved := map[int]struct{}{1: {}, 2: {}}
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, hopPM(), resolved)
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
	packets, ambiguous := computeNodeHopPackets(hopTarget, txs, pm, nil)
	if got := hopsOf(packets); !reflect.DeepEqual(got, []int{0}) || ambiguous != 0 {
		t.Fatalf("hops=%v ambiguous=%d, want [0] and 0", got, ambiguous)
	}
}

// End to end through the route: store load, days window, response shape, 404.
func TestHandleNodeHopAnalytics(t *testing.T) {
	db := setupTestDB(t)
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := time.Now().Add(-1 * time.Hour).Unix()
	old := time.Now().Add(-3 * 24 * time.Hour).Format(time.RFC3339)
	oldEpoch := time.Now().Add(-3 * 24 * time.Hour).Unix()

	mustExec(t, db, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES (?, 'Target', 'repeater', ?, '2026-01-01', 1)`, hopTarget, recent)
	mustExec(t, db, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES (?, 'Other', 'repeater', ?, '2026-01-01', 1)`, hopOther, recent)
	mustExec(t, db, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES (?, 'Collider', 'repeater', ?, '2026-01-01', 1)`, hopCollider, recent)

	rp := func(pks ...string) string {
		b, _ := json.Marshal(pks)
		return string(b)
	}
	mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type) VALUES (1, 'AA', 'hop_recent_2', ?, 1, 5)`, recent)
	mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (1, NULL, '["77AA","77AA","AB12"]', ?, ?)`,
		recentEpoch, rp(hopOther, hopOther, hopTarget))
	mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type) VALUES (2, 'BB', 'hop_recent_1byte', ?, 0, 4)`, recent)
	mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (2, NULL, '["AB"]', ?, ?)`,
		recentEpoch, rp(hopTarget))
	mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type) VALUES (3, 'CC', 'hop_old', ?, 1, 5)`, old)
	mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (3, NULL, '["AB12"]', ?, ?)`,
		oldEpoch, rp(hopTarget))

	srv := NewServer(db, &Config{Port: 3000}, NewHub())
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	get := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		return w
	}

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
		t.Fatalf("packets = %+v, want the two packets inside the 1-day window", resp.Packets)
	}
	if p := byHash["hop_recent_2"]; p.Hops != 2 || !reflect.DeepEqual(p.Tags, []string{"flood", "unscoped"}) {
		t.Errorf("hop_recent_2 = %+v, want hops 2 tags [flood unscoped]", p)
	}
	if p := byHash["hop_recent_1byte"]; p.Hops != 0 || !reflect.DeepEqual(p.Tags, []string{"flood", "scoped", "advert"}) {
		t.Errorf("hop_recent_1byte = %+v, want hops 0 tags [flood scoped advert] (1-byte collision confirmed by resolved_path)", p)
	}
	if resp.TimeRange.Days != 1 || resp.Ambiguous != 0 {
		t.Errorf("timeRange.days=%d ambiguous=%d, want 1 and 0", resp.TimeRange.Days, resp.Ambiguous)
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

// BenchmarkNodeHopPackets sizes the per-request scan for a busy repeater:
// 60k relayed flood packets (about 14 days at the 4.5k/day the busiest
// repeaters on a 2k-node mesh relay), 5 observations each, 5-hop paths.
func BenchmarkNodeHopPackets(b *testing.B) {
	paths := []string{
		`["77AA","AB12","77AA","77AA","77AA"]`,
		`["77AA","77AA","AB12","77AA","77AA"]`,
		`["AB12","77AA","77AA","77AA","77AA"]`,
		`["77AA","77AA","77AA","AB12","77AA"]`,
		`["77AA","77AA","77AA","77AA","77AA"]`,
	}
	txs := make([]*StoreTx, 60000)
	for i := range txs {
		p := paths[i%4]
		txs[i] = hopTx(i, "h", RouteFlood, PayloadGRP_TXT, p, p, p, p, paths[4])
	}
	pm := hopPM()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		computeNodeHopPackets(hopTarget, txs, pm, nil)
	}
}
