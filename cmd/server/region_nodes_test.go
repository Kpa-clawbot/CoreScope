package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

// #2101: /api/nodes?region= ran a subquery over every advert and all of its
// observations, twice per request, and saturated the reader pool on a 10 GB
// database. The region filter now resolves to a pubkey set from the packet
// store's in-memory adverts.

func mkRegionAdvert(id int, pubkey string, iatas ...string) *StoreTx {
	pt := PayloadADVERT
	j, _ := json.Marshal(map[string]interface{}{"pubKey": pubkey})
	tx := &StoreTx{ID: id, Hash: fmt.Sprintf("radv%d", id), PayloadType: &pt, DecodedJSON: string(j)}
	for i, iata := range iatas {
		tx.Observations = append(tx.Observations, &StoreObs{
			ID: id*10 + i, TransmissionID: id, ObserverID: fmt.Sprintf("obs-%s-%d", iata, i), ObserverIATA: iata,
		})
	}
	return tx
}

func addToStore(ps *PacketStore, txs ...*StoreTx) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, tx := range txs {
		ps.packets = append(ps.packets, tx)
		ps.byHash[tx.Hash] = tx
		ps.byTxID[tx.ID] = tx
		if tx.PayloadType != nil {
			ps.byPayloadType[*tx.PayloadType] = append(ps.byPayloadType[*tx.PayloadType], tx)
		}
	}
}

func sortedKeys(pks []string) []string {
	out := append([]string(nil), pks...)
	sort.Strings(out)
	return out
}

func TestRegionNodePubkeys(t *testing.T) {
	ps := NewPacketStore(nil, nil)
	chanType := PayloadGRP_TXT
	chanTx := &StoreTx{ID: 90, Hash: "chan90", PayloadType: &chanType, DecodedJSON: `{"pubKey":"pk_chan"}`,
		Observations: []*StoreObs{{ID: 900, TransmissionID: 90, ObserverID: "o", ObserverIATA: "SJC"}}}
	addToStore(ps,
		mkRegionAdvert(1, "pk_sjc", "SJC"),
		mkRegionAdvert(2, "pk_sfo", "SFO"),
		mkRegionAdvert(3, "pk_both", "SFO", " sjc "), // one in-region observation is enough
		mkRegionAdvert(4, "pk_none"),                 // advert never observed
		chanTx,                                       // not an advert: never counts
	)

	cases := []struct {
		region string
		want   []string
	}{
		{"SJC", []string{"pk_both", "pk_sjc"}},
		{" sjc ", []string{"pk_both", "pk_sjc"}},
		{"SJC,SFO", []string{"pk_both", "pk_sfo", "pk_sjc"}},
		{"ZZZ", []string{}},
	}
	for _, c := range cases {
		got, ok := ps.RegionNodePubkeys(c.region)
		if !ok {
			t.Fatalf("region %q: want a resolved set, got none", c.region)
		}
		if g := sortedKeys(got); fmt.Sprint(g) != fmt.Sprint(c.want) {
			t.Errorf("region %q: got %v, want %v", c.region, g, c.want)
		}
	}
	if _, ok := ps.RegionNodePubkeys("  "); ok {
		t.Error("a blank region must not resolve to a set")
	}
}

func TestRegionNodePubkeysCached(t *testing.T) {
	ps := NewPacketStore(nil, nil)
	addToStore(ps, mkRegionAdvert(1, "pk_a", "SJC"))
	if got, _ := ps.RegionNodePubkeys("SJC"); len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	addToStore(ps, mkRegionAdvert(2, "pk_b", "SJC"))
	if got, _ := ps.RegionNodePubkeys("sjc"); len(got) != 1 {
		t.Errorf("within the TTL a normalised repeat must be served from cache, got %v", got)
	}
	ps.regionNodesMu.Lock()
	for k, e := range ps.regionNodesCache {
		e.at = time.Now().Add(-time.Hour)
		ps.regionNodesCache[k] = e
	}
	ps.regionNodesMu.Unlock()
	if got, _ := ps.RegionNodePubkeys("SJC"); len(got) != 2 {
		t.Errorf("after the TTL the set must be recomputed, got %v", got)
	}
}

func TestGetNodesRegionPubkeys(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	nodes, total, _, err := db.GetNodes(NodeQuery{Limit: 50, RegionPubkeys: []string{"aabbccdd11223344", "eeff00112233aabb", "not-a-node"}})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(nodes) != 2 {
		t.Errorf("want 2 nodes and total 2, got %d nodes, total %d", len(nodes), total)
	}

	nodes, total, _, err = db.GetNodes(NodeQuery{Limit: 50, Role: "repeater", RegionPubkeys: []string{"aabbccdd11223344", "eeff00112233aabb"}})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(nodes) != 1 || nodes[0]["public_key"] != "aabbccdd11223344" {
		t.Errorf("role filter must combine with the set: got %v (total %d)", nodes, total)
	}

	nodes, total, _, err = db.GetNodes(NodeQuery{Limit: 50, Region: "SJC", RegionPubkeys: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || nodes == nil || len(nodes) != 0 {
		t.Errorf("an empty set matches nothing, and replaces the SQL region filter: got %v (total %d)", nodes, total)
	}
}

func TestHandleNodesRegionUsesStore(t *testing.T) {
	srv, router := setupTestServer(t)

	get := func() []string {
		req := httptest.NewRequest(http.MethodGet, "/api/nodes?region=SJC", nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		var body struct {
			Nodes []map[string]interface{} `json:"nodes"`
			Total int                      `json:"total"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		var pks []string
		for _, n := range body.Nodes {
			pks = append(pks, n["public_key"].(string))
		}
		sort.Strings(pks)
		return pks
	}

	// The seeded advert from aabbccdd11223344 was heard by obs1 (SJC).
	if got := get(); fmt.Sprint(got) != "[aabbccdd11223344]" {
		t.Fatalf("region SJC: got %v", got)
	}

	// An advert only the store knows about: if the handler still asked SQL,
	// the companion could not appear.
	addToStore(srv.store, mkRegionAdvert(7001, "eeff00112233aabb", "SJC"))
	srv.store.regionNodesMu.Lock()
	srv.store.regionNodesCache = map[string]regionNodesEntry{}
	srv.store.regionNodesMu.Unlock()
	if got := get(); fmt.Sprint(got) != "[aabbccdd11223344 eeff00112233aabb]" {
		t.Errorf("region SJC must come from the store: got %v", got)
	}
}

func TestRegionNodePubkeysCacheIsBounded(t *testing.T) {
	ps := NewPacketStore(nil, nil)
	addToStore(ps, mkRegionAdvert(1, "pk_a", "SJC"))
	// The region parameter comes from the client, so every distinct value is
	// a new cache key. The cache must not grow with them.
	for i := 0; i < 1000; i++ {
		ps.RegionNodePubkeys(fmt.Sprintf("X%04d", i))
	}
	ps.regionNodesMu.Lock()
	n := len(ps.regionNodesCache)
	ps.regionNodesMu.Unlock()
	if n > regionNodesCacheMax {
		t.Errorf("cache holds %d entries, bound is %d", n, regionNodesCacheMax)
	}
}

// BenchmarkRegionNodePubkeys measures one uncached scan at the scale of a
// large deployment: 220k adverts (staging held 219,750 on 2026-10-05) from
// 4,000 nodes, each heard by 8 observers across 10 regions.
func BenchmarkRegionNodePubkeys(b *testing.B) {
	ps := NewPacketStore(nil, nil)
	regions := []string{"ANR", "BRU", "GNE", "HEP", "KJK", "LGG", "MST", "NRW", "OBL", "OST"}
	txs := make([]*StoreTx, 0, 220000)
	for i := 0; i < 220000; i++ {
		iatas := make([]string, 8)
		for j := range iatas {
			iatas[j] = regions[(i+j*3)%len(regions)]
		}
		txs = append(txs, mkRegionAdvert(i+1, fmt.Sprintf("pk%04d", i%4000), iatas...))
	}
	addToStore(ps, txs...)
	ps.RegionNodePubkeys("BRU") // parse each heard advert's JSON once, as a running server has
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ps.regionNodesMu.Lock()
		ps.regionNodesCache = map[string]regionNodesEntry{}
		ps.regionNodesMu.Unlock()
		if keys, _ := ps.RegionNodePubkeys("BRU"); len(keys) == 0 {
			b.Fatal("no keys")
		}
	}
}
