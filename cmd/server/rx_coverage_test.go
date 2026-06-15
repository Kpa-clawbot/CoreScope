package main

import (
	"encoding/json"
	"math"
	"testing"
)

func covF(f float64) *float64 { return &f }

func TestAggregateCoverageBucketsBestSNR(t *testing.T) {
	rows := []coverageRow{
		{Lat: 51.05000, Lon: 3.72000, SNR: covF(-12)},
		{Lat: 51.05001, Lon: 3.72001, SNR: covF(-6)}, // same cell, stronger
	}
	fc := aggregateCoverage(rows, 9, nil)
	if len(fc.Features) != 1 {
		t.Fatalf("expected 1 cell, got %d", len(fc.Features))
	}
	if p := fc.Features[0].Properties; p.BestSNR == nil || *p.BestSNR != -6 || p.Count != 2 || !p.HasSig {
		t.Fatalf("bad props: %+v", fc.Features[0].Properties)
	}
	if g := fc.Features[0].Geometry; g.Type != "Polygon" || len(g.Coordinates) != 1 {
		t.Fatalf("bad geometry: %+v", g)
	}
	if _, err := json.Marshal(fc); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}

func TestAggregateCoverageGreyWhenNoSignal(t *testing.T) {
	fc := aggregateCoverage([]coverageRow{{Lat: 51.05, Lon: 3.72}}, 9, nil)
	if len(fc.Features) != 1 || fc.Features[0].Properties.HasSig {
		t.Fatalf("expected one grey (no-sig) cell, got %+v", fc.Features)
	}
}

// TestAggregateCoverageNodeBreakdown covers the per-cell node list: each heard node
// keeps its latest SNR (by rx_at) and reception count, sorted strongest-first with
// heard-without-signal nodes last.
func TestAggregateCoverageNodeBreakdown(t *testing.T) {
	rows := []coverageRow{
		// node A: two receptions; the later one (t2) has the weaker SNR -10.
		{Lat: 51.05, Lon: 3.72, SNR: covF(-4), HeardKey: "aabb", RxAt: "2026-06-01T10:00:00Z"},
		{Lat: 51.05001, Lon: 3.72001, SNR: covF(-10), HeardKey: "aabb", RxAt: "2026-06-02T10:00:00Z"},
		// node B: single reception, strongest latest SNR.
		{Lat: 51.05, Lon: 3.72, SNR: covF(-6), HeardKey: "ccdd", RxAt: "2026-06-01T10:00:00Z"},
		// node C: heard without a signal metric.
		{Lat: 51.05, Lon: 3.72, HeardKey: "eeff", RxAt: "2026-06-01T10:00:00Z"},
	}
	fc := aggregateCoverage(rows, 9, nil)
	if len(fc.Features) != 1 {
		t.Fatalf("expected 1 cell, got %d", len(fc.Features))
	}
	nodes := fc.Features[0].Properties.Nodes
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d (%+v)", len(nodes), nodes)
	}
	if nodes[0].Prefix != "ccdd" || nodes[0].SNR == nil || *nodes[0].SNR != -6 {
		t.Errorf("node[0] want ccdd@-6 (strongest), got %+v", nodes[0])
	}
	if nodes[1].Prefix != "aabb" || nodes[1].SNR == nil || *nodes[1].SNR != -10 || nodes[1].Count != 2 {
		t.Errorf("node[1] want aabb latest -10 count 2, got %+v", nodes[1])
	}
	if nodes[2].Prefix != "eeff" || nodes[2].SNR != nil {
		t.Errorf("node[2] want eeff no-signal (last), got %+v", nodes[2])
	}
}

// TestResolveHeardKey covers heard_key → (pubkey, name) resolution: a unique match
// returns the canonical pubkey + name; an ambiguous prefix (>1 node) and an
// unknown/empty key return the key itself with an empty name.
func TestResolveHeardKey(t *testing.T) {
	db := seedCoverageDB(t)
	mustExecDB(t, db, `INSERT INTO nodes (public_key,name,role) VALUES ('aabbccdd11223344','Alice','repeater')`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key,name,role) VALUES ('aabbcc99887766aa','Bob','repeater')`)
	srv := &Server{db: db}
	if k, n := srv.resolveHeardKey("aabbccdd"); k != "aabbccdd11223344" || n != "Alice" {
		t.Errorf("unique prefix → (pubkey,Alice), got (%q,%q)", k, n)
	}
	if k, n := srv.resolveHeardKey("aabbcc"); k != "aabbcc" || n != "" {
		t.Errorf("ambiguous prefix → (key,\"\"), got (%q,%q)", k, n)
	}
	if k, n := srv.resolveHeardKey("ffff"); k != "ffff" || n != "" {
		t.Errorf("unknown prefix → (key,\"\"), got (%q,%q)", k, n)
	}
	if k, n := srv.resolveHeardKey(""); k != "" || n != "" {
		t.Errorf("empty prefix → (\"\",\"\"), got (%q,%q)", k, n)
	}
}

// TestAggregateCoverageMergesResolvedNodes verifies that the same node heard under
// two different heard_keys (e.g. a 3-byte prefix and the full pubkey) collapses into a
// single entry — summed count, latest SNR — when the resolver maps both to one node.
func TestAggregateCoverageMergesResolvedNodes(t *testing.T) {
	rows := []coverageRow{
		{Lat: 51.05, Lon: 3.72, SNR: covF(-4), HeardKey: "aabbcc", RxAt: "2026-06-01T10:00:00Z"},
		{Lat: 51.05, Lon: 3.72, SNR: covF(-9), HeardKey: "aabbccdd11223344", RxAt: "2026-06-03T10:00:00Z"},
		{Lat: 51.05, Lon: 3.72, SNR: covF(-7), HeardKey: "aabbcc", RxAt: "2026-06-02T10:00:00Z"},
	}
	resolve := func(hk string) (string, string) { return "aabbccdd11223344", "Alice" }
	fc := aggregateCoverage(rows, 9, resolve)
	if len(fc.Features) != 1 {
		t.Fatalf("expected 1 cell, got %d", len(fc.Features))
	}
	nodes := fc.Features[0].Properties.Nodes
	if len(nodes) != 1 {
		t.Fatalf("expected 1 merged node, got %d (%+v)", len(nodes), nodes)
	}
	n := nodes[0]
	if n.Name != "Alice" || n.Count != 3 || n.SNR == nil || *n.SNR != -9 {
		t.Errorf("merged node want Alice count 3 latest -9, got %+v (snr=%v)", n, n.SNR)
	}
}

func TestZoomToHexRes(t *testing.T) {
	// Resolution tracks zoom 1:1 within [3,18], clamped at the edges (z=0 is the
	// missing-param case).
	cases := map[int]int{0: 3, 3: 3, 8: 8, 16: 16, 18: 18, 25: 18}
	for z, want := range cases {
		if got := zoomToHexRes(z); got != want {
			t.Fatalf("zoomToHexRes(%d)=%d, want %d", z, got, want)
		}
	}
}

// TestHexSizeRendersConstantPx verifies the core fix: a hex sized for resolution
// res renders at a constant ~hexTargetPx on screen at the corresponding zoom level,
// instead of the old fixed-meter buckets that were ~2px when zoomed out.
func TestHexSizeRendersConstantPx(t *testing.T) {
	for res := 4; res <= 16; res++ {
		// On-screen point-to-point height = 2*circumradius / mercUnitsPerPixel(zoom),
		// where mercUnitsPerPixel = mercUPPZ0 / 2^zoom and zoom == res.
		px := 2 * hexSizeForRes(res) * math.Pow(2, float64(res)) / mercUPPZ0
		if math.Abs(px-hexTargetPx) > 0.001 {
			t.Fatalf("res %d renders %.2fpx, want %.2fpx", res, px, hexTargetPx)
		}
		// Size must halve each zoom step (finer grid as you zoom in).
		if ratio := hexSizeForRes(res) / hexSizeForRes(res+1); math.Abs(ratio-2) > 1e-9 {
			t.Fatalf("res %d→%d size ratio %.4f, want 2", res, res+1, ratio)
		}
	}
}
