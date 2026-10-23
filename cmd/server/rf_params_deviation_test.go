package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

type rfTestNode struct {
	pk       string
	lat, lon float64
	p        *RfParams
}

func rfDevInput(ns []rfTestNode) (map[string]RfParams, []nodeInfo) {
	params := map[string]RfParams{}
	nodes := make([]nodeInfo, 0, len(ns))
	for _, n := range ns {
		nodes = append(nodes, nodeInfo{PublicKey: strings.ToUpper(n.pk), Lat: n.lat, Lon: n.lon, HasGPS: n.lat != 0 || n.lon != 0})
		if n.p != nil {
			params[n.pk] = *n.p
		}
	}
	return params, nodes
}

func rfp(freq float64, sf int, bw float64, cr int) *RfParams {
	return &RfParams{Freq: freq, SF: sf, BW: bw, CR: cr}
}

func devKeys(d map[string]RfDeviation) []string {
	out := make([]string, 0, len(d))
	for k := range d {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestRfDeviations(t *testing.T) {
	eu := rfp(869.618, 8, 62.5, 8)
	// A cluster around Brussels, all within a few km.
	ns := []rfTestNode{
		{"a1", 50.85, 4.35, eu},
		{"a2", 50.86, 4.36, eu},
		{"a3", 50.87, 4.34, eu},
		{"a4", 50.84, 4.37, rfp(869.618, 8, 62.5, 5)},  // CR only: never a deviation
		{"sf7", 50.85, 4.36, rfp(869.618, 7, 62.5, 8)}, // SF differs
		{"frq", 50.86, 4.35, rfp(869.432, 8, 62.5, 8)}, // freq differs within the band
		{"bw", 50.85, 4.34, rfp(869.618, 8, 125, 8)},   // BW differs
		// A 433 MHz network in the same area: a separate band, compared only
		// with itself, so neither side flags the other.
		{"u1", 50.85, 4.35, rfp(433.65, 8, 62.5, 8)},
		{"u2", 50.86, 4.35, rfp(433.65, 8, 62.5, 8)},
		{"u3", 50.87, 4.35, rfp(433.65, 8, 62.5, 8)},
		{"u4", 50.84, 4.35, rfp(433.65, 8, 62.5, 8)},
		// Far away (Lisbon): no peers within the radius.
		{"far", 38.72, -9.14, rfp(869.618, 7, 62.5, 5)},
		// No GPS and no params: ignored.
		{"nogps", 0, 0, rfp(869.618, 7, 62.5, 5)},
		{"noparams", 50.85, 4.35, nil},
	}
	params, nodes := rfDevInput(ns)
	got := computeRfDeviations(params, nodes, 50, 3)

	if k := devKeys(got); fmt.Sprint(k) != "[bw frq sf7]" {
		t.Fatalf("deviating nodes = %v, want [bw frq sf7]", k)
	}
	d := got["sf7"]
	if d.Majority.Freq != 869.618 || d.Majority.SF != 8 || d.Majority.BW != 62.5 {
		t.Errorf("sf7 majority = %+v", d.Majority)
	}
	// Peers of sf7 in the 868 band: a1 a2 a3 a4 frq bw = 6; a1-a4 share 869.618/SF8/BW62.5.
	if d.Peers != 6 || d.Agree != 4 {
		t.Errorf("sf7 agree/peers = %d/%d, want 4/6", d.Agree, d.Peers)
	}
	if fmt.Sprint(d.Fields) != "[sf]" {
		t.Errorf("sf7 fields = %v", d.Fields)
	}
	if fmt.Sprint(got["frq"].Fields) != "[freq]" || fmt.Sprint(got["bw"].Fields) != "[bw]" {
		t.Errorf("frq fields %v, bw fields %v", got["frq"].Fields, got["bw"].Fields)
	}
}

func TestRfDeviationsNeedsAClearMajority(t *testing.T) {
	// Two settings split 2-2 around the node: no majority, no flag.
	ns := []rfTestNode{
		{"x", 50.85, 4.35, rfp(869.618, 9, 62.5, 8)},
		{"p1", 50.86, 4.35, rfp(869.618, 8, 62.5, 8)},
		{"p2", 50.84, 4.35, rfp(869.618, 8, 62.5, 8)},
		{"p3", 50.85, 4.36, rfp(869.618, 7, 62.5, 5)},
		{"p4", 50.85, 4.34, rfp(869.618, 7, 62.5, 5)},
	}
	params, nodes := rfDevInput(ns)
	if got := computeRfDeviations(params, nodes, 50, 3); got["x"].Peers != 0 {
		t.Errorf("x flagged without a clear majority: %+v", got["x"])
	}

	// Fewer than minPeers same-band nodes nearby: no flag.
	ns = []rfTestNode{
		{"x", 50.85, 4.35, rfp(869.618, 7, 62.5, 8)},
		{"p1", 50.86, 4.35, rfp(869.618, 8, 62.5, 8)},
		{"p2", 50.84, 4.35, rfp(869.618, 8, 62.5, 8)},
	}
	params, nodes = rfDevInput(ns)
	if got := computeRfDeviations(params, nodes, 50, 3); len(got) != 0 {
		t.Errorf("flagged with 2 peers: %v", devKeys(got))
	}
}

func TestRfDeviationsRadius(t *testing.T) {
	// Peers about 70 km away count at 100 km, not at 50 km.
	ns := []rfTestNode{
		{"x", 50.85, 4.35, rfp(869.618, 7, 62.5, 8)},
		{"p1", 51.48, 4.35, rfp(869.618, 8, 62.5, 8)},
		{"p2", 51.48, 4.36, rfp(869.618, 8, 62.5, 8)},
		{"p3", 51.49, 4.35, rfp(869.618, 8, 62.5, 8)},
	}
	params, nodes := rfDevInput(ns)
	if got := computeRfDeviations(params, nodes, 50, 3); len(got) != 0 {
		t.Errorf("50 km: flagged %v", devKeys(got))
	}
	if got := computeRfDeviations(params, nodes, 100, 3); len(got) != 1 {
		t.Errorf("100 km: flagged %v, want [x]", devKeys(got))
	}
}

func TestRfDeviationsAcrossLongitudeCells(t *testing.T) {
	// At 60°N a degree of longitude is ~55 km: peers 40 km east sit in a
	// different grid column and must still be found.
	ns := []rfTestNode{
		{"x", 60.0, 10.0, rfp(869.618, 7, 62.5, 8)},
		{"p1", 60.0, 10.72, rfp(869.618, 8, 62.5, 8)},
		{"p2", 60.01, 10.72, rfp(869.618, 8, 62.5, 8)},
		{"p3", 60.0, 10.71, rfp(869.618, 8, 62.5, 8)},
	}
	params, nodes := rfDevInput(ns)
	if got := computeRfDeviations(params, nodes, 50, 3); len(got) != 1 {
		t.Errorf("flagged %v, want [x]", devKeys(got))
	}
}

func TestRfCompareRadiusDefault(t *testing.T) {
	if r := (&RfParamsConfig{NodesURL: "x"}).compareRadiusKm(); r != 50 {
		t.Errorf("default radius = %v", r)
	}
	if r := (&RfParamsConfig{NodesURL: "x", CompareRadiusKm: 20}).compareRadiusKm(); r != 20 {
		t.Errorf("configured radius = %v", r)
	}
}

// BenchmarkRfDeviations1k: 1,100 nodes over Belgium and the southern
// Netherlands, about the size of our own instance.
func BenchmarkRfDeviations1k(b *testing.B) {
	r := rand.New(rand.NewSource(1))
	ns := make([]rfTestNode, 1100)
	for i := range ns {
		ns[i] = rfTestNode{fmt.Sprintf("%064x", i), 49.5 + r.Float64()*2.5, 2.5 + r.Float64()*4, rfp(869.618, 8, 62.5, 8)}
	}
	params, nodes := rfDevInput(ns)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		computeRfDeviations(params, nodes, 50, 3)
	}
}

// BenchmarkRfDeviations10k: 10,000 nodes with params spread over a
// Belgium-sized box, the worst case /api/rf-params computes per request.
func BenchmarkRfDeviations10k(b *testing.B) {
	r := rand.New(rand.NewSource(1))
	ns := make([]rfTestNode, 10000)
	for i := range ns {
		sf := 8
		if r.Intn(50) == 0 {
			sf = 7
		}
		ns[i] = rfTestNode{fmt.Sprintf("%064x", i), 49.5 + r.Float64()*2, 2.5 + r.Float64()*4, rfp(869.618, sf, 62.5, 8)}
	}
	params, nodes := rfDevInput(ns)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		computeRfDeviations(params, nodes, 50, 3)
	}
}

func TestRfParamsRouteDeviations(t *testing.T) {
	srv, router := setupTestServer(t)
	// Four nodes close together; the first runs SF7, the others SF8.
	pks := []string{rfPK1, rfPK2, rfPK3, rfPK3[:63] + "9"}
	for i, pk := range pks {
		mustExecDB(t, srv.db, fmt.Sprintf(`INSERT INTO nodes (public_key, name, role, lat, lon) VALUES ('%s', 'rfdev%d', 'repeater', %f, 4.35)`, pk, i, 50.85+float64(i)*0.01))
	}
	srv.store.InvalidateNodeCache()
	now := time.Now().UTC().Format(time.RFC3339)
	snapNodes := map[string]RfParams{}
	for i, pk := range pks {
		sf := 8
		if i == 0 {
			sf = 7
		}
		snapNodes[pk] = RfParams{Freq: 869.618, SF: sf, BW: 62.5, CR: 8, LastAdvert: now}
	}
	c := newRfParamsCache(&RfParamsConfig{NodesURL: "http://unused"})
	c.ptr.Store(&rfParamsSnapshot{FetchedAt: time.Now().UTC(), Nodes: snapNodes, Presets: []RfPreset{}})
	srv.rfParams = c

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/rf-params", nil))
	var resp RfParamsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Nodes) != 4 {
		t.Fatalf("nodes served = %d, want 4", len(resp.Nodes))
	}
	d, ok := resp.Deviations[rfPK1]
	if len(resp.Deviations) != 1 || !ok || d.Majority.SF != 8 || d.Agree != 3 || d.Peers != 3 {
		t.Errorf("deviations = %+v", resp.Deviations)
	}
}

func TestRfDeviationCache(t *testing.T) {
	var c rfDeviationCache
	calls := 0
	compute := func() map[string]RfDeviation { calls++; return map[string]RfDeviation{} }
	s1, s2 := &rfParamsSnapshot{}, &rfParamsSnapshot{}
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	c.get(s1, t0, compute)
	c.get(s1, t0.Add(time.Minute), compute)
	if calls != 1 {
		t.Errorf("same snapshot within TTL computed %d times, want 1", calls)
	}
	c.get(s2, t0.Add(2*time.Minute), compute)
	if calls != 2 {
		t.Errorf("new snapshot: %d computations, want 2", calls)
	}
	c.get(s2, t0.Add(2*time.Minute+rfDeviationTTL), compute)
	if calls != 3 {
		t.Errorf("after TTL: %d computations, want 3", calls)
	}
}

// An even split flags nobody. Before the node's own setting was counted, each
// side saw the other as "at least half" of its peers and all ten were flagged.
func TestRfDeviationsEvenSplitFlagsNobody(t *testing.T) {
	var ns []rfTestNode
	for i := 0; i < 5; i++ {
		ns = append(ns,
			rfTestNode{fmt.Sprintf("a%d", i), 50.85 + float64(i)*0.01, 4.35, rfp(869.618, 7, 62.5, 8)},
			rfTestNode{fmt.Sprintf("b%d", i), 50.85 + float64(i)*0.01, 4.36, rfp(869.618, 8, 62.5, 8)})
	}
	params, nodes := rfDevInput(ns)
	if got := computeRfDeviations(params, nodes, 50, 3); len(got) != 0 {
		t.Errorf("5/5 split flagged %v, want none", devKeys(got))
	}

	// 2 of 4 is half, not more: x (SF9) with peers SF8, SF8, SF7 is not flagged.
	ns = []rfTestNode{
		{"x", 50.85, 4.35, rfp(869.618, 9, 62.5, 8)},
		{"p1", 50.86, 4.35, rfp(869.618, 8, 62.5, 8)},
		{"p2", 50.84, 4.35, rfp(869.618, 8, 62.5, 8)},
		{"p3", 50.85, 4.36, rfp(869.618, 7, 62.5, 8)},
	}
	params, nodes = rfDevInput(ns)
	if got := computeRfDeviations(params, nodes, 50, 3); len(got) != 0 {
		t.Errorf("2 of 4 flagged %v, want none", devKeys(got))
	}
}

// Frequencies within BW/4 are one channel: a 1 kHz offset is not a deviation,
// and offset peers add up into one majority. 32 kHz at BW 62.5 (BW/4 =
// 15.625 kHz) is a deviation.
func TestRfDeviationsFrequencyTolerance(t *testing.T) {
	ns := []rfTestNode{
		{"near", 50.85, 4.35, rfp(869.617, 8, 62.5, 8)},
		{"edge", 50.85, 4.355, rfp(869.633, 8, 62.5, 8)}, // 15 kHz off: still compatible
		{"off", 50.85, 4.36, rfp(869.650, 8, 62.5, 8)},   // 32 kHz off
		{"p1", 50.86, 4.35, rfp(869.618, 8, 62.5, 8)},
		{"p2", 50.84, 4.35, rfp(869.618, 8, 62.5, 8)},
		{"p3", 50.85, 4.34, rfp(869.619, 8, 62.5, 8)},
	}
	params, nodes := rfDevInput(ns)
	got := computeRfDeviations(params, nodes, 50, 3)
	if k := devKeys(got); fmt.Sprint(k) != "[off]" {
		t.Fatalf("deviating nodes = %v, want [off]", k)
	}
	d := got["off"]
	if fmt.Sprint(d.Fields) != "[freq]" || d.Agree != 5 || d.Peers != 5 || d.Majority.Freq != 869.618 {
		t.Errorf("off = %+v, want freq differing from 869.618 with 5 of 5 compatible", d)
	}
	if !rfFreqCompatible(869.618, 869.618+0.015625, 62.5) || rfFreqCompatible(869.618, 869.618+0.0157, 62.5) {
		t.Errorf("BW/4 boundary at 62.5 kHz is not 15.625 kHz")
	}
}

// At 60°N a longitude degree is about 55.7 km, so the 50 km radius spans two
// grid columns. A node near the east edge of its cell has peers two columns
// over; a column search of ±1 would miss them.
func TestRfDeviationsTwoColumnsAway(t *testing.T) {
	cellDeg := 50 / 110.57
	lon := (math.Floor(10/cellDeg) + 0.95) * cellDeg
	peerLon := lon + 45/(111.32*math.Cos(60*math.Pi/180)) // 45 km east
	if c0, c1 := math.Floor(lon/cellDeg), math.Floor(peerLon/cellDeg); c1-c0 != 2 {
		t.Fatalf("setup: peers %v columns away, want 2", c1-c0)
	}
	ns := []rfTestNode{
		{"x", 60.0, lon, rfp(869.618, 7, 62.5, 8)},
		{"p1", 60.0, peerLon, rfp(869.618, 8, 62.5, 8)},
		{"p2", 60.001, peerLon, rfp(869.618, 8, 62.5, 8)},
		{"p3", 59.999, peerLon, rfp(869.618, 8, 62.5, 8)},
	}
	params, nodes := rfDevInput(ns)
	if got := computeRfDeviations(params, nodes, 50, 3); fmt.Sprint(devKeys(got)) != "[x]" {
		t.Errorf("flagged %v, want [x]", devKeys(got))
	}
}

// A peer just inside the radius due north must be found by the ±1 row search.
// With rows of radius/111 degrees (0.996 × radius), a node at the top of its
// row had such a peer two rows up; rows are now radius/110.57 degrees, never
// narrower than the radius. The node sits where the old grid failed.
func TestRfDeviationsNorthEdgeOfRadius(t *testing.T) {
	oldCellDeg := 50 / 111.0
	lat := (math.Floor(50.5/oldCellDeg) + 0.999) * oldCellDeg
	peerLat := lat + 49.9/110.57
	ns := []rfTestNode{
		{"x", lat, 4.35, rfp(869.618, 7, 62.5, 8)},
		{"p1", peerLat, 4.35, rfp(869.618, 8, 62.5, 8)},
		{"p2", peerLat, 4.3501, rfp(869.618, 8, 62.5, 8)},
		{"p3", peerLat, 4.3499, rfp(869.618, 8, 62.5, 8)},
	}
	params, nodes := rfDevInput(ns)
	if got := computeRfDeviations(params, nodes, 50, 3); fmt.Sprint(devKeys(got)) != "[x]" {
		t.Errorf("flagged %v, want [x]", devKeys(got))
	}
}
