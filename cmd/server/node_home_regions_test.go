package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"
)

// #1879: the region filter put a node in every region where any observer heard
// any of its adverts, relayed ones included, so a node leaked into each region
// its flood adverts were relayed into. A node now belongs to the regions whose
// observers heard it zero-hop (#804's rule, as a set); a node never heard
// zero-hop goes by its fewest hops (TestRegionFallbackUsesFewestHops).

const (
	homeZeroHopDirect = "1240aabbccdd" // route direct, path byte 0x40: 2-byte hashes, 0 hops
	homeRelayedFlood  = "1141aabbccdd" // route flood, path byte 0x41: 1 hop
)

func mkHomeRegionAdvert(id int, pubkey, rawHex string, observerIDs ...string) *StoreTx {
	tx := mkRegionAdvert(id, pubkey, observerIDs...)
	tx.RawHex = rawHex
	return tx
}

func newHomeRegionStore(t *testing.T) *PacketStore {
	t.Helper()
	ps := newRegionTestStore(t, map[string]string{"o-aus": "AUS", "o-sat": "SAT"})
	addToStore(ps,
		// Home in AUS; a relayed advert also reached a SAT observer.
		mkHomeRegionAdvert(1, "pk_aus", homeZeroHopDirect, "o-aus"),
		mkHomeRegionAdvert(2, "pk_aus", homeRelayedFlood, "o-sat"),
		// On the boundary: heard zero-hop in both regions.
		mkHomeRegionAdvert(3, "pk_edge", homeZeroHopDirect, "o-aus", "o-sat"),
		// Never heard zero-hop: only the fallback can place it.
		mkPathAdvert(4, "pk_far", map[string]string{"o-sat": `["aa"]`}),
	)
	ps.recomputeNodeHomeRegionSets()
	return ps
}

func TestRegionNodePubkeysUsesZeroHopHomeRegions(t *testing.T) {
	ps := newHomeRegionStore(t)

	cases := []struct {
		region string
		want   []string
	}{
		// pk_aus is out of SAT despite its relayed advert there; pk_far stays
		// through the fallback.
		{"SAT", []string{"pk_edge", "pk_far"}},
		{"AUS", []string{"pk_aus", "pk_edge"}},
		// The boundary node is in both regions, and counted once.
		{"AUS,SAT", []string{"pk_aus", "pk_edge", "pk_far"}},
	}
	for _, c := range cases {
		got, ok := ps.RegionNodePubkeys(c.region)
		if !ok {
			t.Fatalf("region %q: ok=false", c.region)
		}
		if g := sortedKeys(got); !reflect.DeepEqual(g, c.want) {
			t.Errorf("region %q: got %v, want %v", c.region, g, c.want)
		}
	}
}

// Selecting one region must not drop a boundary node from the other: per-region
// counts no longer sum to the node total, by design.
func TestBoundaryNodeStaysInEachHomeRegion(t *testing.T) {
	ps := newHomeRegionStore(t)
	for _, region := range []string{"AUS", "SAT"} {
		got, _ := ps.RegionNodePubkeys(region)
		found := false
		for _, pk := range got {
			found = found || pk == "pk_edge"
		}
		if !found {
			t.Errorf("boundary node missing from %s: %v", region, sortedKeys(got))
		}
	}
}

func TestNodeRegionPlacementsZeroHop(t *testing.T) {
	ps := newHomeRegionStore(t)
	got := ps.GetNodeRegionPlacements()
	want := map[string]NodeRegionPlacement{
		"pk_aus":  {Basis: RegionBasisZeroHop, Regions: []string{"AUS"}},
		"pk_edge": {Basis: RegionBasisZeroHop, Regions: []string{"AUS", "SAT"}},
		"pk_far":  {Basis: RegionBasisFewestHops, Regions: []string{"SAT"}, Hops: intPtr(1)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("placements = %s, want %s", placementsString(got), placementsString(want))
	}
}

// /api/nodes says which rule placed each node, for every rule (#2190 review):
// zero-hop home regions, fewest hops, no region past the cap, and the
// observer scan for a node the last recompute did not cover.
func TestHandleNodesRegionPlacement(t *testing.T) {
	srv, router := setupTestServer(t)
	// obs1 is SJC in seedTestData. The repeater gets a zero-hop advert, the
	// room is heard only five hops out, and the companion has no advert.
	addToStore(srv.store,
		mkHomeRegionAdvert(7101, "aabbccdd11223344", homeZeroHopDirect, "obs1"),
		mkPathAdvert(7102, "1122334455667788", map[string]string{"obs1": `["aa","bb","cc","dd","ee"]`}),
	)
	srv.store.recomputeNodeHomeRegionSets()
	srv.invalidateNodesCache()

	req := httptest.NewRequest(http.MethodGet, "/api/nodes", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Nodes []struct {
			PublicKey       string               `json:"public_key"`
			RegionPlacement *NodeRegionPlacement `json:"region_placement"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]NodeRegionPlacement{
		"aabbccdd11223344": {Basis: RegionBasisZeroHop, Regions: []string{"SJC"}},
		"1122334455667788": {Basis: RegionBasisNone, Hops: intPtr(5)},
		"eeff00112233aabb": {Basis: RegionBasisObserverScan},
	}
	seen := 0
	for _, n := range body.Nodes {
		w, ok := want[n.PublicKey]
		if !ok {
			continue
		}
		seen++
		if n.RegionPlacement == nil {
			t.Errorf("%s: region_placement missing", n.PublicKey)
			continue
		}
		if !reflect.DeepEqual(*n.RegionPlacement, w) {
			t.Errorf("%s: region_placement = %s, want %s", n.PublicKey,
				placementString(*n.RegionPlacement), placementString(w))
		}
	}
	if seen != len(want) {
		t.Fatalf("expected all %d seeded nodes in the response, saw %d", len(want), seen)
	}
}

func placementString(p NodeRegionPlacement) string {
	hops := "-"
	if p.Hops != nil {
		hops = fmt.Sprint(*p.Hops)
	}
	return fmt.Sprintf("{%s %v hops=%s}", p.Basis, p.Regions, hops)
}

func placementsString(m map[string]NodeRegionPlacement) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for _, k := range keys {
		out += k + ":" + placementString(m[k]) + " "
	}
	return out
}

// #2225: transport routes carry 4 transport-code bytes between the header and
// the path byte, so for TRANSPORT_DIRECT the path byte is at offset 5, not 1.
const (
	// route transport-direct, codes aa bb cc dd (0xaa's low 6 bits are not
	// 0), path byte 0x40: 0 hops.
	homeTransportZeroHop = "13aabbccdd40aabbccdd"
	// route transport-direct, codes 00 bb cc dd (0x00 would read as 0 hops
	// at the wrong offset), path byte 0x41: 1 hop.
	homeTransportRelayed = "1300bbccdd41aabbccdd"
)

func TestZeroHopCountsReadTransportDirectPathByte(t *testing.T) {
	ps := newRegionTestStore(t, map[string]string{"o-aus": "AUS", "o-sat": "SAT"})
	addToStore(ps,
		mkHomeRegionAdvert(1, "pk_transport", homeTransportZeroHop, "o-aus"),
		mkHomeRegionAdvert(2, "pk_relayed", homeTransportRelayed, "o-sat"),
	)
	obsIATA := ps.observerIATAs()
	ps.mu.RLock()
	got := ps.nodeZeroHopRegionCounts(obsIATA)
	ps.mu.RUnlock()
	want := map[string]map[string]int{"pk_transport": {"AUS": 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("zero-hop counts = %v, want %v", got, want)
	}

	// #804 analytics reads the same tally.
	ps.mu.RLock()
	homes := ps.computeNodeHomeRegions(obsIATA)
	ps.mu.RUnlock()
	if want := map[string]string{"pk_transport": "AUS"}; !reflect.DeepEqual(homes, want) {
		t.Errorf("computeNodeHomeRegions = %v, want %v", homes, want)
	}
}

// mkPathAdvert is a relayed flood advert heard by each observer over the given
// path (JSON array of hop hashes, "[]" for a direct hearing).
func mkPathAdvert(id int, pubkey string, hearings map[string]string) *StoreTx {
	tx := mkHomeRegionAdvert(id, pubkey, homeRelayedFlood)
	i := 0
	for oid, path := range hearings {
		tx.Observations = append(tx.Observations, &StoreObs{
			ID: id*10 + i, TransmissionID: id, ObserverID: oid, PathJSON: path,
		})
		i++
	}
	return tx
}

// A node never heard zero-hop used to be in every region that heard any of its
// adverts. Adverts flood the whole mesh, so on a statewide deployment that put
// most nodes in every region. It now goes in the regions that heard it in the
// fewest hops, and in none when even that is over the cap (default 3).
func TestRegionFallbackUsesFewestHops(t *testing.T) {
	ps := newRegionTestStore(t, map[string]string{"o-aus": "AUS", "o-sat": "SAT", "o-hou": "HOU"})
	addToStore(ps,
		// One hop from SAT, relayed four hops to AUS: SAT only.
		mkPathAdvert(1, "pk_near", map[string]string{"o-sat": `["aa"]`, "o-aus": `["aa","bb","cc","dd"]`}),
		// Tied fewest hops: both.
		mkPathAdvert(2, "pk_tie", map[string]string{"o-sat": `["aa","bb"]`, "o-hou": `["cc","dd"]`, "o-aus": `["aa","bb","cc"]`}),
		// Exactly the cap is still placed.
		mkPathAdvert(3, "pk_cap", map[string]string{"o-aus": `["aa","bb","cc"]`}),
		// Closest hearing beyond the cap: no region.
		mkPathAdvert(4, "pk_gulf", map[string]string{"o-aus": `["aa","bb","cc","dd"]`, "o-hou": `["aa","bb","cc","dd","ee","ff"]`}),
		// A later advert heard closer counts.
		mkPathAdvert(5, "pk_gulf2", map[string]string{"o-hou": `["aa","bb","cc","dd","ee"]`}),
		mkPathAdvert(6, "pk_gulf2", map[string]string{"o-hou": `["aa"]`}),
	)
	ps.recomputeNodeHomeRegionSets()

	cases := []struct {
		region string
		want   []string
	}{
		{"SAT", []string{"pk_near", "pk_tie"}},
		{"AUS", []string{"pk_cap"}},
		{"HOU", []string{"pk_gulf2", "pk_tie"}},
	}
	for _, c := range cases {
		got, ok := ps.RegionNodePubkeys(c.region)
		if !ok {
			t.Fatalf("region %q: ok=false", c.region)
		}
		if g := sortedKeys(got); !reflect.DeepEqual(g, c.want) {
			t.Errorf("region %q: got %v, want %v", c.region, g, c.want)
		}
	}

	want := map[string]NodeRegionPlacement{
		"pk_near":  {Basis: RegionBasisFewestHops, Regions: []string{"SAT"}, Hops: intPtr(1)},
		"pk_tie":   {Basis: RegionBasisFewestHops, Regions: []string{"HOU", "SAT"}, Hops: intPtr(2)},
		"pk_cap":   {Basis: RegionBasisFewestHops, Regions: []string{"AUS"}, Hops: intPtr(3)},
		"pk_gulf":  {Basis: RegionBasisNone, Hops: intPtr(4)},
		"pk_gulf2": {Basis: RegionBasisFewestHops, Regions: []string{"HOU"}, Hops: intPtr(1)},
	}
	if got := ps.GetNodeRegionPlacements(); !reflect.DeepEqual(got, want) {
		t.Errorf("placements = %s, want %s", placementsString(got), placementsString(want))
	}
}

// regionFallback.maxHops sets the cap (#2190 review); unset or negative
// keeps the default of 3, and 0 places only nodes heard over an empty path.
func TestRegionFallbackMaxHopsConfig(t *testing.T) {
	var parsed Config
	if err := json.Unmarshal([]byte(`{"regionFallback":{"maxHops":4}}`), &parsed); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		cfg  *Config
		want int
	}{
		{"nil config", nil, 3},
		{"unset", &Config{}, 3},
		{"empty block", &Config{RegionFallback: &RegionFallbackConfig{}}, 3},
		{"negative", &Config{RegionFallback: &RegionFallbackConfig{MaxHops: intPtr(-1)}}, 3},
		{"zero", &Config{RegionFallback: &RegionFallbackConfig{MaxHops: intPtr(0)}}, 0},
		{"from JSON", &parsed, 4},
	}
	for _, c := range cases {
		if got := c.cfg.RegionFallbackMaxHops(); got != c.want {
			t.Errorf("%s: RegionFallbackMaxHops() = %d, want %d", c.name, got, c.want)
		}
	}

	ps := newRegionTestStore(t, map[string]string{"o-aus": "AUS"})
	ps.config = &Config{RegionFallback: &RegionFallbackConfig{MaxHops: intPtr(4)}}
	addToStore(ps,
		mkPathAdvert(1, "pk_four", map[string]string{"o-aus": `["aa","bb","cc","dd"]`}),
		mkPathAdvert(2, "pk_five", map[string]string{"o-aus": `["aa","bb","cc","dd","ee"]`}),
	)
	ps.recomputeNodeHomeRegionSets()
	want := map[string]NodeRegionPlacement{
		"pk_four": {Basis: RegionBasisFewestHops, Regions: []string{"AUS"}, Hops: intPtr(4)},
		"pk_five": {Basis: RegionBasisNone, Hops: intPtr(5)},
	}
	if got := ps.GetNodeRegionPlacements(); !reflect.DeepEqual(got, want) {
		t.Errorf("maxHops 4: placements = %s, want %s", placementsString(got), placementsString(want))
	}
}

// A legacy observation with no path_json says nothing about hops, so it must
// not count as a direct hearing (#2190 review). "[]" is still zero hops.
func TestRegionFallbackIgnoresUnknownPaths(t *testing.T) {
	ps := newRegionTestStore(t, map[string]string{"o-aus": "AUS", "o-sat": "SAT"})
	addToStore(ps,
		// The empty AUS path would have been 0 hops and won.
		mkPathAdvert(1, "pk_mixed", map[string]string{"o-aus": "", "o-sat": `["aa","bb"]`}),
		// Only unknown paths: no placement, so the observer scan decides.
		mkPathAdvert(2, "pk_legacy", map[string]string{"o-aus": ""}),
		mkPathAdvert(3, "pk_direct", map[string]string{"o-aus": `[]`}),
	)
	ps.recomputeNodeHomeRegionSets()
	want := map[string]NodeRegionPlacement{
		"pk_mixed":  {Basis: RegionBasisFewestHops, Regions: []string{"SAT"}, Hops: intPtr(2)},
		"pk_direct": {Basis: RegionBasisFewestHops, Regions: []string{"AUS"}, Hops: intPtr(0)},
	}
	if got := ps.GetNodeRegionPlacements(); !reflect.DeepEqual(got, want) {
		t.Errorf("placements = %s, want %s", placementsString(got), placementsString(want))
	}
}

// A node with no placement yet (its first advert arrived after the last
// recompute) is decided by the observer scan: in each region whose observers
// heard any of its adverts, however many hops out.
func TestRegionFilterObserverScanForNodesNewerThanRecompute(t *testing.T) {
	ps := newHomeRegionStore(t)
	addToStore(ps, mkPathAdvert(9, "pk_new", map[string]string{"o-aus": `["aa","bb","cc","dd","ee"]`}))
	if _, placed := ps.GetNodeRegionPlacements()["pk_new"]; placed {
		t.Fatal("pk_new placed before any recompute")
	}
	got, _ := ps.RegionNodePubkeys("AUS")
	if want := []string{"pk_aus", "pk_edge", "pk_new"}; !reflect.DeepEqual(sortedKeys(got), want) {
		t.Errorf("AUS before recompute: got %v, want %v", sortedKeys(got), want)
	}
}

// Naming every region explicitly is still a region filter: a node in no
// region drops out, unlike "All regions", which sends no ?region= at all.
func TestRegionFilterListingEveryRegionOmitsUnplacedNodes(t *testing.T) {
	ps := newRegionTestStore(t, map[string]string{"o-aus": "AUS", "o-sat": "SAT"})
	addToStore(ps,
		mkPathAdvert(1, "pk_near", map[string]string{"o-sat": `["aa"]`}),
		mkPathAdvert(2, "pk_gulf", map[string]string{"o-aus": `["aa","bb","cc","dd"]`, "o-sat": `["aa","bb","cc","dd","ee"]`}),
	)
	ps.recomputeNodeHomeRegionSets()
	got, ok := ps.RegionNodePubkeys("AUS,SAT")
	if !ok {
		t.Fatal("ok=false")
	}
	if want := []string{"pk_near"}; !reflect.DeepEqual(sortedKeys(got), want) {
		t.Errorf("AUS,SAT: got %v, want %v", sortedKeys(got), want)
	}
}

// The recomputer starts at the first load chunk, so its prewarm misses
// adverts in later chunks. It recomputes once the startup load is done
// instead of up to one interval later (#2190 review).
func TestNodeRegionsRecomputeWhenStartupLoadDone(t *testing.T) {
	resetStarted := func() {
		nodeHomeRegionsRecompStartedMu.Lock()
		nodeHomeRegionsRecompStarted = false
		nodeHomeRegionsRecompStartedMu.Unlock()
	}
	resetStarted()
	t.Cleanup(resetStarted)

	ps := newRegionTestStore(t, map[string]string{"o-aus": "AUS"})
	stop := ps.StartNodeHomeRegionsRecomputer(time.Hour)
	defer stop()
	// A later chunk lands, then the background fill finishes.
	addToStore(ps, mkHomeRegionAdvert(1, "pk_late", homeZeroHopDirect, "o-aus"))
	ps.signalStartupLoadDone()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := ps.GetNodeRegionPlacements()["pk_late"]; ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("pk_late not placed after the startup load finished")
}

// newRegionRecomputeBenchStore builds BenchmarkRegionNodePubkeys' scale: 220k
// adverts from 4,000 nodes, each heard by 8 observers across 10 regions over
// 0-7 hop paths (one in eight sent zero-hop direct), plus nonAdverts channel
// messages, which the zero-hop count also walks past.
func newRegionRecomputeBenchStore(b *testing.B, nonAdverts int) *PacketStore {
	regions := []string{"ANR", "BRU", "GNE", "HEP", "KJK", "LGG", "MST", "NRW", "OBL", "OST"}
	observers := make(map[string]string, len(regions))
	for _, r := range regions {
		observers["o-"+r] = r
	}
	ps := newRegionTestStore(b, observers)
	paths := []string{`[]`, `["aa"]`, `["aa","bb"]`, `["aa","bb","cc"]`, `["aa","bb","cc","dd"]`,
		`["aa","bb","cc","dd","ee"]`, `["aa","bb","cc","dd","ee","ff"]`, `["aa","bb","cc","dd","ee","ff","11"]`}
	txs := make([]*StoreTx, 0, 220000+nonAdverts)
	for i := 0; i < 220000; i++ {
		raw := homeRelayedFlood
		if i%8 == 0 {
			raw = homeZeroHopDirect
		}
		tx := mkHomeRegionAdvert(i+1, fmt.Sprintf("pk%04d", i%4000), raw)
		for j := 0; j < 8; j++ {
			tx.Observations = append(tx.Observations, &StoreObs{
				ID: (i+1)*10 + j, TransmissionID: i + 1,
				ObserverID: "o-" + regions[(i+j*3)%len(regions)], PathJSON: paths[(i+j)%len(paths)],
			})
		}
		txs = append(txs, tx)
	}
	chanType := PayloadGRP_TXT
	for i := 0; i < nonAdverts; i++ {
		id := 220000 + i + 1
		txs = append(txs, &StoreTx{ID: id, Hash: fmt.Sprintf("rchan%d", id), PayloadType: &chanType, RawHex: "1541aabbccdd",
			Observations: []*StoreObs{{ID: id * 10, TransmissionID: id, ObserverID: "o-" + regions[i%len(regions)], PathJSON: `["aa"]`}}})
	}
	addToStore(ps, txs...)
	ps.recomputeNodeHomeRegionSets() // parse each advert's JSON once, as a running server has
	return ps
}

// BenchmarkNodeFewestHopsByRegion measures the fallback's own pass.
func BenchmarkNodeFewestHopsByRegion(b *testing.B) {
	ps := newRegionRecomputeBenchStore(b, 0)
	obsIATA := ps.observerIATAs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ps.mu.RLock()
		hops := ps.nodeFewestHopsByRegion(obsIATA)
		ps.mu.RUnlock()
		if len(hops) == 0 {
			b.Fatal("no nodes")
		}
	}
}

// BenchmarkRecomputeNodeHomeRegionSets measures the whole recompute, the cost
// a running server pays once per interval: the zero-hop count, the fewest-hops
// pass, and building both snapshots, here with 1M channel messages in the
// store besides the adverts.
func BenchmarkRecomputeNodeHomeRegionSets(b *testing.B) {
	ps := newRegionRecomputeBenchStore(b, 1000000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ps.recomputeNodeHomeRegionSets()
	}
	if len(ps.GetNodeRegionPlacements()) == 0 {
		b.Fatal("no placements")
	}
}
