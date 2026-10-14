package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// #1879: the region filter put a node in every region where any observer heard
// any of its adverts, relayed ones included, so a node leaked into each region
// its flood adverts were relayed into. A node now belongs to the regions whose
// observers heard it zero-hop (#804's rule, as a set); a node never heard
// zero-hop falls back to the observer-based check.

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
		mkHomeRegionAdvert(4, "pk_far", homeRelayedFlood, "o-sat"),
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

func TestNodeHomeRegionSets(t *testing.T) {
	ps := newHomeRegionStore(t)
	want := map[string][]string{
		"pk_aus":  {"AUS"},
		"pk_edge": {"AUS", "SAT"},
	}
	if got := ps.GetNodeHomeRegionSets(); !reflect.DeepEqual(got, want) {
		t.Errorf("home region sets = %v, want %v", got, want)
	}
}

// /api/nodes says which rule placed a node: home_regions is present exactly
// when zero-hop data answered for it.
func TestHandleNodesHomeRegions(t *testing.T) {
	srv, router := setupTestServer(t)
	// obs1 is SJC in seedTestData; only the repeater gets a zero-hop advert.
	addToStore(srv.store, mkHomeRegionAdvert(7101, "aabbccdd11223344", homeZeroHopDirect, "obs1"))
	srv.store.recomputeNodeHomeRegionSets()
	srv.invalidateNodesCache()

	req := httptest.NewRequest(http.MethodGet, "/api/nodes", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Nodes []map[string]interface{} `json:"nodes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, n := range body.Nodes {
		hr, has := n["home_regions"]
		switch n["public_key"] {
		case "aabbccdd11223344":
			seen++
			if fmt.Sprint(hr) != "[SJC]" {
				t.Errorf("repeater home_regions = %v, want [SJC]", hr)
			}
		case "eeff00112233aabb":
			seen++
			if has {
				t.Errorf("companion without zero-hop data has home_regions %v", hr)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("expected both seeded nodes in the response, saw %d", seen)
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
// fewest hops, and in none when even that is over regionFallbackMaxHops.
func TestRegionFallbackUsesFewestHops(t *testing.T) {
	ps := newRegionTestStore(t, map[string]string{"o-aus": "AUS", "o-sat": "SAT", "o-hou": "HOU"})
	addToStore(ps,
		// One hop from SAT, relayed four hops to AUS: SAT only.
		mkPathAdvert(1, "pk_near", map[string]string{"o-sat": `["aa"]`, "o-aus": `["aa","bb","cc","dd"]`}),
		// Tied fewest hops: both.
		mkPathAdvert(2, "pk_tie", map[string]string{"o-sat": `["aa","bb"]`, "o-hou": `["cc","dd"]`, "o-aus": `["aa","bb","cc"]`}),
		// Exactly regionFallbackMaxHops is still placed.
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

	want := map[string][]string{
		"pk_near":  {"SAT"},
		"pk_tie":   {"HOU", "SAT"},
		"pk_cap":   {"AUS"},
		"pk_gulf":  {},
		"pk_gulf2": {"HOU"},
	}
	if got := ps.GetNodeFallbackRegionSets(); !reflect.DeepEqual(got, want) {
		t.Errorf("fallback region sets = %v, want %v", got, want)
	}
}

// Zero-hop home regions still win: a node with any zero-hop hearing is not in
// the fallback snapshot at all.
func TestRegionFallbackSkipsZeroHopNodes(t *testing.T) {
	ps := newHomeRegionStore(t)
	fb := ps.GetNodeFallbackRegionSets()
	if _, has := fb["pk_aus"]; has {
		t.Errorf("pk_aus has zero-hop home regions but is in the fallback: %v", fb)
	}
	if !reflect.DeepEqual(fb["pk_far"], []string{"SAT"}) {
		t.Errorf("pk_far fallback = %v, want [SAT]", fb["pk_far"])
	}
}

// BenchmarkNodeFewestHopsByRegion measures the fallback's added recompute work
// at BenchmarkRegionNodePubkeys' scale: 220k adverts from 4,000 nodes, each
// heard by 8 observers across 10 regions over 0-7 hop paths.
func BenchmarkNodeFewestHopsByRegion(b *testing.B) {
	regions := []string{"ANR", "BRU", "GNE", "HEP", "KJK", "LGG", "MST", "NRW", "OBL", "OST"}
	observers := make(map[string]string, len(regions))
	for _, r := range regions {
		observers["o-"+r] = r
	}
	ps := newRegionTestStore(b, observers)
	paths := []string{`[]`, `["aa"]`, `["aa","bb"]`, `["aa","bb","cc"]`, `["aa","bb","cc","dd"]`,
		`["aa","bb","cc","dd","ee"]`, `["aa","bb","cc","dd","ee","ff"]`, `["aa","bb","cc","dd","ee","ff","11"]`}
	txs := make([]*StoreTx, 0, 220000)
	for i := 0; i < 220000; i++ {
		tx := mkRegionAdvert(i+1, fmt.Sprintf("pk%04d", i%4000))
		for j := 0; j < 8; j++ {
			tx.Observations = append(tx.Observations, &StoreObs{
				ID: (i+1)*10 + j, TransmissionID: i + 1,
				ObserverID: "o-" + regions[(i+j*3)%len(regions)], PathJSON: paths[(i+j)%len(paths)],
			})
		}
		txs = append(txs, tx)
	}
	addToStore(ps, txs...)
	obsIATA := ps.observerIATAs()
	ps.recomputeNodeHomeRegionSets() // parse each advert's JSON once, as a running server has
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
