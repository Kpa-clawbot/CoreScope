package main

import (
	"math"
	"testing"
)

func TestDuctingGridNodeCells(t *testing.T) {
	// Seattle area and New York: the whole box between them is filled.
	nodes := [][2]float64{{47.61, -122.33}, {47.70, -122.30}, {40.71, -74.00}}
	cells, step, fell := ductingGrid(nodes, nil, 2, 1000, 0)
	if step != 2 || fell {
		t.Fatalf("step %v fell %v", step, fell)
	}
	// lat 40.71..47.70 -> centres 41..47 (4); lon -122.33..-74 -> -123..-73 (25)
	if len(cells) != 4*25 {
		t.Fatalf("%d cells", len(cells))
	}
	checkSortedUnique(t, cells)
	for lat := 41.0; lat <= 47; lat += 2 {
		for lon := -123.0; lon <= -75; lon += 2 {
			if !hasCell(cells, lat, lon) {
				t.Fatalf("gap at %v,%v", lat, lon)
			}
		}
	}
}

func TestDuctingGridSortedDeduped(t *testing.T) {
	// Four nodes: too few for the outlier trim, so all of them place the box.
	nodes := [][2]float64{{10.1, 20.1}, {10.1, 5.1}, {-5.1, 30.1}, {10.1, 20.4}}
	cells, _, _ := ductingGrid(nodes, nil, 0.5, 100000, 0)
	// lat -5.1..10.1 -> 32 rows; lon 5.1..30.1 -> 51 columns
	if len(cells) != 32*51 {
		t.Fatalf("%d cells", len(cells))
	}
	checkSortedUnique(t, cells)
}

func TestDuctingGridIgnoresBadNodes(t *testing.T) {
	nodes := [][2]float64{
		{0, 0}, {math.NaN(), 10}, {10, math.NaN()}, {math.Inf(1), 0}, {91, 10}, {10, 181}, {-95, 0},
		{50.1, 8.1},
	}
	cells, _, _ := ductingGrid(nodes, nil, 0.5, 100, 0)
	if len(cells) != 1 || cells[0] != [2]float64{50.25, 8.25} {
		t.Fatalf("cells %v", cells)
	}
	if cells, _, _ := ductingGrid(nil, nil, 0.5, 10, 0); len(cells) != 0 {
		t.Fatalf("empty: %v", cells)
	}
}

func TestDuctingGridClampsLatitude(t *testing.T) {
	cells, _, _ := ductingGrid([][2]float64{{89.9, 10.1}, {-89.9, 10.1}}, nil, 0.5, 10, 0)
	if len(cells) != 2 {
		t.Fatalf("%v", cells)
	}
	for _, c := range cells {
		if math.Abs(c[0]) > 85.5 {
			t.Fatalf("cell beyond clamp: %v", c)
		}
	}
}

func TestDuctingGridArea(t *testing.T) {
	area := [4]float64{10, 20, 12, 23}
	cells, step, _ := ductingGrid(nil, &area, 0.5, 1000, 0)
	if step != 0.5 || len(cells) != 4*6 {
		t.Fatalf("step %v count %d", step, len(cells))
	}
	if cells[0] != [2]float64{10.25, 20.25} || cells[len(cells)-1] != [2]float64{11.75, 22.75} {
		t.Fatalf("first %v last %v", cells[0], cells[len(cells)-1])
	}
	// unaligned area still fully covered
	area = [4]float64{10.1, 20.1, 10.6, 20.6}
	cells, _, _ = ductingGrid(nil, &area, 0.5, 1000, 0)
	if len(cells) != 4 {
		t.Fatalf("unaligned: %v", cells)
	}
	// nodes are ignored when an area is given
	cells, _, _ = ductingGrid([][2]float64{{60, 60}}, &[4]float64{10, 20, 10.5, 20.5}, 0.5, 10, 0)
	if len(cells) != 1 || cells[0] != [2]float64{10.25, 20.25} {
		t.Fatalf("%v", cells)
	}
}

func TestDuctingGridStepDoubling(t *testing.T) {
	// 10 nodes spaced 1 degree apart in lat: 10 cells at 0.5, 5 at 2... fewer than 6 needs step 2.
	var nodes [][2]float64
	for i := 0; i < 10; i++ {
		nodes = append(nodes, [2]float64{10.1 + float64(i), 20.1})
	}
	cells, step, _ := ductingGrid(nodes, nil, 0.5, 5, 0)
	if step != 2 {
		t.Fatalf("step %v, want 2 (cells %v)", step, cells)
	}
	if len(cells) != 5 {
		t.Fatalf("cells %d", len(cells))
	}
	// area too large for step 0.5 doubles too
	_, step, _ = ductingGrid(nil, &[4]float64{0, 0, 10, 10}, 0.5, 50, 0)
	if step != 2 {
		t.Fatalf("area step %v, want 2", step)
	}
}

func TestDuctingGridTopNFallback(t *testing.T) {
	// Far apart nodes: never merge, even at step 8. Cell A has 3 nodes, B has 2, C/D have 1.
	nodes := [][2]float64{
		{-60.1, -100.1}, {-60.2, -100.2}, {-60.3, -100.3}, // A
		{0.1, 0.1}, {0.2, 0.2}, // B
		{60.1, 100.1}, // C
		{70.1, 150.1}, // D
	}
	cells, step, _ := ductingGrid(nodes, nil, 0.5, 2, 0)
	if step != 8 {
		t.Fatalf("step %v", step)
	}
	if len(cells) != 2 {
		t.Fatalf("cells %v", cells)
	}
	if cells[0] != [2]float64{ductingCentre(-60.1, 8), ductingCentre(-100.1, 8)} || cells[1] != [2]float64{ductingCentre(0.1, 8), ductingCentre(0.1, 8)} {
		t.Fatalf("top cells %v", cells)
	}
	// ties resolve in sort order
	cells, _, _ = ductingGrid([][2]float64{{60.1, 100.1}, {-60.1, -100.1}, {0.1, 0.1}}, nil, 0.5, 2, 0)
	if len(cells) != 2 || cells[0][0] != -60 || cells[1][0] != 4 {
		t.Fatalf("ties: %v", cells)
	}
}

func TestDuctingGridBadStep(t *testing.T) {
	cells, step, _ := ductingGrid([][2]float64{{10.1, 10.1}}, nil, 0, 10, 0)
	if step != 0.5 || len(cells) != 1 {
		t.Fatalf("%v %v", cells, step)
	}
}

func TestDuctingGridAntimeridian(t *testing.T) {
	cells, _, _ := ductingGrid([][2]float64{{10.1, 180}, {10.1, -180}}, nil, 0.5, 10, 0)
	if len(cells) != 1 || cells[0] != [2]float64{10.25, -179.75} {
		t.Fatalf("lon 180/-180: %v", cells)
	}
	// wide cell around the antimeridian never yields a centre past 180
	cells, _, _ = ductingGrid([][2]float64{{10.1, 179}}, nil, 8, 10, 0)
	for _, c := range cells {
		if c[1] > 180 || c[1] < -180 {
			t.Fatalf("out of range: %v", cells)
		}
	}
	for _, step := range []float64{0.25, 0.3, 0.5, 2} {
		cells, _, _ = ductingGrid(nil, &[4]float64{10, 170, 11, 180}, step, 1000, 0)
		if len(cells) == 0 {
			t.Fatalf("step %v: no cells", step)
		}
		for _, c := range cells {
			if c[1] > 180 {
				t.Fatalf("step %v: centre beyond 180: %v", step, c)
			}
		}
	}
}

func hasCell(cells [][2]float64, lat, lon float64) bool {
	for _, c := range cells {
		if math.Abs(c[0]-lat) < 1e-9 && math.Abs(c[1]-lon) < 1e-9 {
			return true
		}
	}
	return false
}

func checkSortedUnique(t *testing.T, cells [][2]float64) {
	t.Helper()
	for i := 1; i < len(cells); i++ {
		a, b := cells[i-1], cells[i]
		if a[0] > b[0] || (a[0] == b[0] && a[1] >= b[1]) {
			t.Fatalf("not sorted/unique at %d: %v %v", i, a, b)
		}
	}
}

func TestDuctingGridMarginZeroIsBoundingBox(t *testing.T) {
	nodes := [][2]float64{{10.1, 20.1}, {11.4, 21.4}}
	a, sa, _ := ductingGrid(nodes, nil, 0.5, 100, 0)
	// lat 10.1..11.4 -> centres 10.25..11.25 (3); lon 20.1..21.4 -> 20.25..21.25 (3)
	if sa != 0.5 || len(a) != 9 || a[0] != [2]float64{10.25, 20.25} || a[8] != [2]float64{11.25, 21.25} {
		t.Fatalf("margin 0: %v %v", a, sa)
	}
	// a negative margin behaves as 0
	b, _, _ := ductingGrid(nodes, nil, 0.5, 100, -3)
	if len(b) != 9 {
		t.Fatalf("negative margin: %v", b)
	}
	// one node, margin 0: just its own cell
	c, _, _ := ductingGrid([][2]float64{{47.61, -122.33}}, nil, 0.5, 100, 0)
	if len(c) != 1 || c[0] != [2]float64{47.75, -122.25} {
		t.Fatalf("single node: %v", c)
	}
	// a node exactly on a cell edge still yields one cell
	d, _, _ := ductingGrid([][2]float64{{10.5, 20.5}}, nil, 0.5, 100, 0)
	if len(d) != 1 {
		t.Fatalf("edge node: %v", d)
	}
}

func TestDuctingDilateNodeCells(t *testing.T) {
	valid := [][2]float64{{47.61, -122.33}, {47.70, -122.30}, {40.71, -74.00}}
	out, ok := ductingDilate(valid, 0.5, 0, 100)
	if !ok || len(out) != 2 {
		t.Fatalf("%v %v", out, ok)
	}
	cells := make([][2]float64, len(out))
	for i, r := range out {
		cells[i] = [2]float64{r.lat, r.lon}
	}
	ductingSortCells(cells)
	if cells[0] != [2]float64{40.75, -73.75} || cells[1] != [2]float64{47.75, -122.25} {
		t.Fatalf("%v", cells)
	}
}

func TestDuctingGridMarginBlock(t *testing.T) {
	cells, step, _ := ductingGrid([][2]float64{{10.1, 20.1}}, nil, 0.5, 100, 1)
	if step != 0.5 || len(cells) != 25 {
		t.Fatalf("step %v, %d cells: %v", step, len(cells), cells)
	}
	checkSortedUnique(t, cells)
	if !hasCell(cells, 10.25, 20.25) || !hasCell(cells, 9.25, 19.25) || !hasCell(cells, 11.25, 21.25) || hasCell(cells, 11.75, 20.25) {
		t.Fatalf("block wrong: %v", cells)
	}
	// the box edges need not be cell multiples: 0.6 around 10.1,20.1 is
	// 9.5..10.7 x 19.5..20.7 = 3x3 cells
	cells, _, _ = ductingGrid([][2]float64{{10.1, 20.1}}, nil, 0.5, 100, 0.6)
	if len(cells) != 9 {
		t.Fatalf("0.6 margin: %d cells", len(cells))
	}
}

func TestDuctingGridMarginOverlapDeduped(t *testing.T) {
	// adjacent node cells: blocks of 9 overlap in 6 cells -> 12 cells
	cells, _, _ := ductingGrid([][2]float64{{10.1, 20.1}, {10.1, 20.6}}, nil, 0.5, 100, 0.5)
	if len(cells) != 12 {
		t.Fatalf("%d cells: %v", len(cells), cells)
	}
	checkSortedUnique(t, cells)
}

func TestDuctingGridMarginAntimeridian(t *testing.T) {
	for _, step := range []float64{0.5, 0.3, 8} {
		cells, _, _ := ductingGrid([][2]float64{{10.1, 179.9}, {10.1, -179.9}}, nil, step, 1000, 1)
		var wrapped bool
		for _, c := range cells {
			if c[1] >= 180 || c[1] < -180 {
				t.Fatalf("step %v: centre out of [-180,180): %v", step, c)
			}
			if c[1] < -179 {
				wrapped = true
			}
		}
		if !wrapped {
			t.Fatalf("step %v: no cells wrapped to the -180 side: %v", step, cells)
		}
		checkSortedUnique(t, cells)
	}
	// both sides of the meridian share one lattice: no duplicates, 25 cells
	cells, _, _ := ductingGrid([][2]float64{{10.1, 179.9}}, nil, 0.5, 100, 1)
	if len(cells) != 25 || !hasCell(cells, 10.25, -179.75) || !hasCell(cells, 10.25, 179.75) {
		t.Fatalf("%d cells: %v", len(cells), cells)
	}
}

func TestDuctingGridMarginPolarClamp(t *testing.T) {
	cells, _, _ := ductingGrid([][2]float64{{84.9, 10.1}}, nil, 0.5, 100, 2)
	if len(cells) == 0 {
		t.Fatal("no cells")
	}
	for _, c := range cells {
		if math.Abs(c[0]) > 85 {
			t.Fatalf("centre beyond 85: %v", c)
		}
	}
	if !hasCell(cells, 84.75, 10.25) || !hasCell(cells, 83.25, 10.25) {
		t.Fatalf("cells %v", cells)
	}
}

func TestDuctingGridMarginStepDoubling(t *testing.T) {
	// margin 1: 25 cells at step 0.5 (k=2) > 10, so step 1 (k=1, 9 cells) fits.
	cells, step, _ := ductingGrid([][2]float64{{10.1, 20.1}}, nil, 0.5, 10, 1)
	if step != 1 || len(cells) != 9 {
		t.Fatalf("step %v, %d cells", step, len(cells))
	}
	// maxPoints 5: step 1 gives 9 > 5, step 2 gives a 2x2 box.
	cells, step, _ = ductingGrid([][2]float64{{10.1, 20.1}}, nil, 0.5, 5, 1)
	if step != 2 || len(cells) != 4 {
		t.Fatalf("step %v, %d cells", step, len(cells))
	}
	// the box is recounted at every step
	cells, step, _ = ductingGrid([][2]float64{{10.1, 20.1}}, nil, 0.5, 9, 1)
	if step != 1 || len(cells) != 9 {
		t.Fatalf("recompute: step %v, %d cells", step, len(cells))
	}
}

func TestDuctingGridMarginTrimKeepsNodeCellsFirst(t *testing.T) {
	// Three far-apart nodes at step 8 with margin 2: 9 cells each, maxPoints 4.
	nodes := [][2]float64{{-60.1, -100.1}, {-60.2, -100.2}, {0.1, 0.1}, {60.1, 100.1}}
	cells, step, _ := ductingGrid(nodes, nil, 0.5, 4, 2)
	if step != 8 || len(cells) != 4 {
		t.Fatalf("step %v, cells %v", step, cells)
	}
	checkSortedUnique(t, cells)
	// all three node cells kept (most-populated first), then one ring-1 cell
	for _, n := range [][2]float64{{-60.1, -100.1}, {0.1, 0.1}, {60.1, 100.1}} {
		if !hasCell(cells, ductingCentre(n[0], 8), ductingCentre(n[1], 8)) {
			t.Fatalf("node cell for %v missing: %v", n, cells)
		}
	}
	// with room for only 2, the 2-node cell wins, then the first by (lat, lon)
	cells, _, _ = ductingGrid(nodes, nil, 0.5, 2, 2)
	if len(cells) != 2 || !hasCell(cells, ductingCentre(-60.1, 8), ductingCentre(-100.1, 8)) {
		t.Fatalf("top 2: %v", cells)
	}
}

func TestDuctingGridMarginIgnoredWithArea(t *testing.T) {
	area := [4]float64{10, 20, 12, 22}
	a, sa, _ := ductingGrid(nil, &area, 0.5, 1000, 0)
	b, sb, _ := ductingGrid(nil, &area, 0.5, 1000, 10)
	if sa != sb || len(a) != len(b) {
		t.Fatalf("area changed by margin: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("cell %d differs", i)
		}
	}
}

func TestDuctingGridMarginBounded(t *testing.T) {
	// Many scattered nodes with a large margin must still return <= maxPoints.
	var nodes [][2]float64
	for i := 0; i < 400; i++ {
		nodes = append(nodes, [2]float64{-60 + float64(i%20)*5.3, -170 + float64(i/20)*16.1})
	}
	cells, step, _ := ductingGrid(nodes, nil, 0.25, 50, 10)
	if len(cells) == 0 || len(cells) > 50 || step > 8 {
		t.Fatalf("%d cells at step %v", len(cells), step)
	}
	checkSortedUnique(t, cells)
}

func TestDuctingGridSingleNodeBox(t *testing.T) {
	// margin 2 at step 0.5: a square of about 2*margin/step cells a side
	// around the node (the lattice adds one when the edges fall mid-cell).
	cells, step, fell := ductingGrid([][2]float64{{51.3, -0.3}}, nil, 0.5, 400, 2)
	if step != 0.5 || fell || len(cells) != 81 {
		t.Fatalf("step %v fell %v, %d cells", step, fell, len(cells))
	}
	for _, c := range cells {
		if c[0] < 49.25 || c[0] > 53.25 || c[1] < -2.25 || c[1] > 1.75 {
			t.Fatalf("cell %v outside the box", c)
		}
	}
	checkSortedUnique(t, cells)
	if !hasCell(cells, 51.25, -0.25) || !hasCell(cells, 49.25, -2.25) || !hasCell(cells, 53.25, 1.75) {
		t.Fatalf("box wrong: %v", cells)
	}
}

func TestDuctingGridTwoNodesFilledWithMargin(t *testing.T) {
	// two distant nodes on one continent: every cell of the box between them
	// and the margin around it, no gaps
	nodes := [][2]float64{{48.1, 2.1}, {52.4, 13.4}}
	cells, step, fell := ductingGrid(nodes, nil, 1, 1000, 3)
	if step != 1 || fell {
		t.Fatalf("step %v fell %v", step, fell)
	}
	// lat 45.1..55.4 -> centres 45.5..55.5 (11); lon -0.9..16.4 -> -0.5..16.5 (18)
	if len(cells) != 11*18 {
		t.Fatalf("%d cells, want %d", len(cells), 11*18)
	}
	for lat := 45.5; lat <= 55.5; lat++ {
		for lon := -0.5; lon <= 16.5; lon++ {
			if !hasCell(cells, lat, lon) {
				t.Fatalf("gap at %v,%v", lat, lon)
			}
		}
	}
	checkSortedUnique(t, cells)
}

func TestDuctingGridBoxStepDoubling(t *testing.T) {
	nodes := [][2]float64{{10.1, 20.1}, {20.1, 40.1}}
	// 0.5: 20x40 = 800 cells; 1: 11x21 = 231 > 200; 2: 6x11 = 66
	cells, step, fell := ductingGrid(nodes, nil, 0.5, 200, 0)
	if step != 2 || fell || len(cells) != 66 {
		t.Fatalf("step %v fell %v, %d cells", step, fell, len(cells))
	}
}

func TestDuctingGridBoxLatitudeClamp(t *testing.T) {
	cells, _, fell := ductingGrid([][2]float64{{84.1, 10.1}, {84.9, 12.1}}, nil, 2, 1000, 10)
	if fell || len(cells) == 0 {
		t.Fatalf("fell %v, %d cells", fell, len(cells))
	}
	var top float64
	for _, c := range cells {
		top = math.Max(top, c[0])
		if math.Abs(c[0]) > 85 {
			t.Fatalf("centre beyond 85: %v", c)
		}
	}
	if top != 85 { // the cell [84,86] has its centre on 85
		t.Fatalf("top row %v", top)
	}
	// both poles with a huge margin: still within +-85
	cells, _, _ = ductingGrid([][2]float64{{89.9, 10.1}, {-89.9, 10.1}}, nil, 8, 1000, 10)
	for _, c := range cells {
		if math.Abs(c[0]) > 85.5 {
			t.Fatalf("beyond clamp: %v", c)
		}
	}
}

func TestDuctingGridBoxAntimeridian(t *testing.T) {
	// nodes at 179 and -179: a narrow box across the meridian, not 358 degrees
	nodes := [][2]float64{{10.1, 179.1}, {10.1, -179.1}}
	cells, step, fell := ductingGrid(nodes, nil, 0.5, 1000, 1)
	if step != 0.5 || fell {
		t.Fatalf("step %v fell %v", step, fell)
	}
	// lon 178.1..180 (4 columns) and -180..-178.1 (4 columns); lat 9.1..11.1 (5 rows)
	if len(cells) != 5*8 {
		t.Fatalf("%d cells: %v", len(cells), cells)
	}
	checkSortedUnique(t, cells)
	for _, c := range cells {
		if c[1] >= 180 || c[1] < -180 {
			t.Fatalf("centre out of [-180,180): %v", c)
		}
		if c[1] > -176 && c[1] < 178 {
			t.Fatalf("cell %v is in the part of the world between the nodes", c)
		}
	}
	if !hasCell(cells, 10.25, 179.75) || !hasCell(cells, 10.25, -179.75) || !hasCell(cells, 10.25, 178.25) || !hasCell(cells, 10.25, -178.25) {
		t.Fatalf("cells %v", cells)
	}
	// a wide step puts a centre on 180: it is the -180 cell, once
	for _, step := range []float64{8, 0.3} {
		cells, _, _ = ductingGrid(nodes, nil, step, 1000, 1)
		checkSortedUnique(t, cells)
		for _, c := range cells {
			if c[1] > 180 || c[1] < -180 {
				t.Fatalf("step %v: out of range %v", step, c)
			}
		}
	}
	// margin pushing a box edge past -180 wraps to the east side
	cells, _, _ = ductingGrid([][2]float64{{10.1, -179.5}, {10.1, -170.1}}, nil, 0.5, 1000, 2)
	if !hasCell(cells, 10.25, 179.25) || !hasCell(cells, 10.25, -171.75) || hasCell(cells, 10.25, 0.25) {
		t.Fatalf("wrapped margin: %v", cells)
	}
	checkSortedUnique(t, cells)
}

func TestDuctingGridBoxFullWorldSpan(t *testing.T) {
	// nodes every 90 degrees: the largest gap is 90, the span 270, and a
	// margin of 50 reaches 360: the full -180..180
	nodes := [][2]float64{{10.1, -170.1}, {10.1, -80.1}, {10.1, 9.9}, {10.1, 99.9}}
	cells, step, fell := ductingGrid(nodes, nil, 8, 10000, 50)
	if fell || step != 8 {
		t.Fatalf("step %v fell %v", step, fell)
	}
	if !hasCell(cells, 12, -180) || !hasCell(cells, 12, 172) {
		t.Fatalf("full span missing the edges: %v", cells)
	}
	for _, c := range cells {
		if c[1] > 180 || c[1] < -180 {
			t.Fatalf("out of range %v", c)
		}
	}
	checkSortedUnique(t, cells)
}

func TestDuctingGridWorldwideFallsBackToNodeCells(t *testing.T) {
	// a worldwide spread cannot fit at 8 degrees: dilated node cells, flagged
	nodes := [][2]float64{{60.1, 100.1}, {-60.1, -100.1}, {0.1, 0.1}, {40.1, -100.1}}
	cells, step, fell := ductingGrid(nodes, nil, 0.5, 400, 2)
	if !fell {
		t.Fatal("fallback not flagged")
	}
	if step > 8 || len(cells) == 0 || len(cells) > 400 {
		t.Fatalf("step %v, %d cells", step, len(cells))
	}
	checkSortedUnique(t, cells)
	// the fallback is the dilated node cells, so the middle of the box is empty
	for _, c := range cells {
		if math.Abs(c[0]-30) < 5 && math.Abs(c[1]-40) < 5 {
			t.Fatalf("cell %v in the middle of the box", c)
		}
	}
	for _, n := range nodes {
		var near bool
		for _, c := range cells {
			if math.Abs(c[0]-n[0]) < 5 && math.Abs(c[1]-n[1]) < 5 {
				near = true
			}
		}
		if !near {
			t.Fatalf("no cell near node %v: %v", n, cells)
		}
	}
}

func TestDuctingGridExplicitAreaNeverFlagsOrChanges(t *testing.T) {
	area := [4]float64{10, 20, 12, 23}
	a, sa, fa := ductingGrid(nil, &area, 0.5, 1000, 5)
	b, sb, fb := ductingGrid([][2]float64{{-60, 100}, {60, -100}}, &area, 0.5, 1000, 0)
	if fa || fb || sa != sb || len(a) != 24 || len(b) != 24 {
		t.Fatalf("%v %v %d %d", fa, fb, len(a), len(b))
	}
	// an area too big for maxPoints is still trimmed, never "fallen back"
	_, _, f := ductingGrid(nil, &[4]float64{-80, -170, 80, 170}, 0.5, 10, 0)
	if f {
		t.Fatal("area flagged as fallback")
	}
}

// ductingTestMesh is rows x cols nodes from (lat0, lon0), 0.1 degree apart;
// lons are wrapped, so a mesh may straddle the antimeridian.
func ductingTestMesh(lat0, lon0 float64, rows, cols int) [][2]float64 {
	var out [][2]float64
	for i := 0; i < rows; i++ {
		for j := 0; j < cols; j++ {
			out = append(out, [2]float64{lat0 + 0.1*float64(i), ductingWrapLon(lon0 + 0.1*float64(j))})
		}
	}
	return out
}

func checkCellsInRange(t *testing.T, cells [][2]float64) {
	t.Helper()
	for i, c := range cells {
		if c[1] < -180 || c[1] >= 180 || math.IsNaN(c[1]) {
			t.Fatalf("centre %v outside [-180,180)", c)
		}
		if i > 0 && math.Abs(c[0]-cells[i-1][0]) < 1e-6 && math.Abs(c[1]-cells[i-1][1]) < 1e-6 {
			t.Fatalf("near-duplicate centres %v %v", cells[i-1], c)
		}
	}
	checkSortedUnique(t, cells)
}

func TestDuctingGridTrimsOutlierNode(t *testing.T) {
	// 25 nodes around Seattle and one in London: the box stays Seattle-sized
	// and keeps the 0.5 degree step.
	nodes := append(ductingTestMesh(47.3, -122.6, 5, 5), [2]float64{51.5, -0.1})
	cells, step, fell, info := ductingGridWithInfo(nodes, nil, 0.5, 450, 2)
	if step != 0.5 || fell {
		t.Fatalf("step %v fell %v", step, fell)
	}
	// lat 45.3..49.7 -> 45.25..49.75 (10); lon -124.6..-120.2 -> -124.75..-120.25 (10)
	if len(cells) != 100 {
		t.Fatalf("%d cells", len(cells))
	}
	for _, c := range cells {
		if c[0] < 45 || c[0] > 50 || c[1] < -125 || c[1] > -120 {
			t.Fatalf("cell %v outside the Seattle box", c)
		}
	}
	if info.nodes != 25 || info.trimmed != 1 || !info.hasBox || info.area {
		t.Errorf("info %+v", info)
	}
}

func TestDuctingGridNoTrimBelowMinNodes(t *testing.T) {
	// 3 Seattle nodes and London: too few to trim, London stays in the box.
	nodes := append(ductingTestMesh(47.3, -122.6, 1, 3), [2]float64{51.5, -0.1})
	kept, trimmed := ductingTrimOutliers(nodes)
	if trimmed != 0 || len(kept) != len(nodes) {
		t.Fatalf("trimmed %d of %d", trimmed, len(nodes))
	}
	cells, step, _, info := ductingGridWithInfo(nodes, nil, 0.5, 450, 0)
	if info.trimmed != 0 || info.nodes != 4 {
		t.Errorf("info %+v", info)
	}
	if !hasCell(cells, ductingCentre(51.5, step), ductingCentre(-0.1, step)) {
		t.Errorf("London not covered at step %v", step)
	}
	// 5 nodes: London is trimmed.
	n5 := append(ductingTestMesh(47.3, -122.6, 1, 4), [2]float64{51.5, -0.1})
	if kept, tr := ductingTrimOutliers(n5); len(n5) != 5 || tr != 1 || hasCell(kept, 51.5, -0.1) {
		t.Errorf("5 nodes: trimmed %d, kept %v", tr, kept)
	}
}

// Several nodes at the same wrong position (a shared bad fix, a default
// coordinate) are dropped as surely as one: the median ignores them.
func TestDuctingGridTrimCoLocatedOutliers(t *testing.T) {
	var nodes [][2]float64
	for i := 0; i < 18; i++ {
		nodes = append(nodes, [2]float64{45 + 0.1*float64(i%6), 10 + 0.15*float64(i/6)})
	}
	nodes = append(nodes, [2]float64{-33, 151}, [2]float64{-33, 151})
	kept, trimmed := ductingTrimOutliers(nodes)
	if trimmed != 2 || len(kept) != 18 {
		t.Fatalf("trimmed %d, kept %d; want the two at -33,151 dropped", trimmed, len(kept))
	}
	cells, step, fell, info := ductingGridWithInfo(nodes, nil, 0.5, 450, 2)
	if step != 0.5 || fell || info.trimmed != 2 || info.nodes != 18 {
		t.Fatalf("step %v fell %v info %+v", step, fell, info)
	}
	for _, c := range cells {
		if c[0] < 42 || c[0] > 48.5 || c[1] < 7.5 || c[1] > 13 {
			t.Fatalf("cell %v outside Europe", c)
		}
	}
	// 8 of 20 at one far spot is more than ductingTrimMaxShare: by position
	// alone that is indistinguishable from a real second area, so all are
	// kept (the box, or its regions-around-nodes fallback, covers both).
	many := append(append([][2]float64{}, nodes[:12]...), make([][2]float64, 0, 8)...)
	for i := 0; i < 8; i++ {
		many = append(many, [2]float64{-33, 151})
	}
	if kept, tr := ductingTrimOutliers(many); tr != 0 || len(kept) != 20 {
		t.Errorf("8 of 20 far away: trimmed %d, kept %d; want all kept", tr, len(kept))
	}
}

// A genuinely spread regional mesh is not trimmed: the limit scales with the
// median distance from the median position.
func TestDuctingGridTrimKeepsSpreadMesh(t *testing.T) {
	// 24 nodes along the Gulf coast from west Texas to Florida, about
	// 2,500 km end to end, denser near Houston.
	var nodes [][2]float64
	for i := 0; i < 24; i++ {
		lon := -104 + 24*float64(i)/23
		lat := 31 - 3*math.Sin(float64(i)/23*math.Pi)
		nodes = append(nodes, [2]float64{lat, lon})
	}
	nodes = append(nodes, [2]float64{29.76, -95.37}, [2]float64{29.9, -95.5}, [2]float64{25.77, -80.19})
	if kept, trimmed := ductingTrimOutliers(nodes); trimmed != 0 || len(kept) != len(nodes) {
		t.Errorf("spread mesh: trimmed %d of %d", trimmed, len(nodes))
	}
	// A compact mesh keeps nodes up to the 500 km floor.
	compact := append(ductingTestMesh(47.3, -122.6, 2, 5), [2]float64{45.5, -122.7}, [2]float64{49.3, -123.1})
	if _, trimmed := ductingTrimOutliers(compact); trimmed != 0 {
		t.Errorf("compact mesh with Portland and Vancouver: trimmed %d", trimmed)
	}
}

func TestDuctingGridTrimAntimeridian(t *testing.T) {
	// 25 nodes around Fiji across the antimeridian (179.7..-179.9), plus a
	// node near Brisbane and one at Tahiti: both trimmed, the box stays a
	// narrow band across 180.
	fiji := ductingTestMesh(-17.3, 179.7, 5, 5)
	nodes := append(append([][2]float64{}, fiji...), [2]float64{-27.5, 153.0}, [2]float64{-17.5, -149.6})
	kept, trimmed := ductingTrimOutliers(nodes)
	if trimmed != 2 || len(kept) != 25 {
		t.Fatalf("trimmed %d, kept %d", trimmed, len(kept))
	}
	cells, step, fell, info := ductingGridWithInfo(nodes, nil, 0.5, 450, 1)
	if step != 0.5 || fell || info.trimmed != 2 {
		t.Fatalf("step %v fell %v info %+v", step, fell, info)
	}
	checkCellsInRange(t, cells)
	var east, west bool
	for _, c := range cells {
		if c[0] < -18.5 || c[0] > -15.5 {
			t.Fatalf("cell %v: latitude stretched by an outlier", c)
		}
		switch {
		case c[1] >= 178:
			west = true
		case c[1] <= -178:
			east = true
		default:
			t.Fatalf("cell %v: longitude stretched by an outlier", c)
		}
	}
	if !east || !west {
		t.Errorf("box does not straddle 180: %v", cells)
	}
	// The box is logged west > east across the antimeridian.
	if info.box[1] < 178 || info.box[3] > -178 {
		t.Errorf("box %v", info.box)
	}
}

func TestDuctingWrapLon(t *testing.T) {
	for in, want := range map[float64]float64{
		-180.25: 179.75, 180: -180, 180.25: -179.75, -180: -180, 0: 0, 179.75: 179.75, 539.5: 179.5, -540: -180,
	} {
		if got := ductingWrapLon(in); math.Abs(got-want) > 1e-9 {
			t.Errorf("ductingWrapLon(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestDuctingGridLongitudeWrap(t *testing.T) {
	for _, step := range []float64{0.7, 1.4} {
		// An area starting at -180: the first column's centre (-180 - a bit)
		// is the cell across the antimeridian, not a longitude past -180.
		for _, area := range [][4]float64{{10, -180, 12, -170}, {-10, -180, 10, 180}, {10, 170, 12, 180}} {
			cells, _, _ := ductingGrid(nil, &area, step, 100000, 0)
			if len(cells) == 0 {
				t.Fatalf("step %v area %v: no cells", step, area)
			}
			checkCellsInRange(t, cells)
		}
		area := [4]float64{10, -180, 12, -170}
		cells, _, _ := ductingGrid(nil, &area, step, 100000, 0)
		// At 0.7 the first column is centred on -180.25: 179.75. (At 1.4 it
		// is -179.9, already in range.)
		first := ductingWrapLon(ductingCentre(-180, step))
		if (step == 0.7 && math.Abs(first-179.75) > 1e-9) || !hasCell(cells, ductingCentre(10, step), first) {
			t.Errorf("step %v: wrapped first column %v missing: %v", step, first, cells)
		}
		// An antimeridian node box, and the regions-around-nodes fallback.
		nodes := [][2]float64{{10.1, 179.1}, {10.1, -179.1}}
		cells, _, _ = ductingGrid(nodes, nil, step, 1000, 1)
		checkCellsInRange(t, cells)
		cells, _, _ = ductingGrid(nodes, nil, step, 1000, 0)
		checkCellsInRange(t, cells)
		fiji := ductingTestMesh(-17.3, 179.7, 5, 5)
		cells, _, _ = ductingGrid(fiji, nil, step, 1000, 3)
		checkCellsInRange(t, cells)
		ranked, _ := ductingDilate(fiji, step, 2, 100000)
		for _, r := range ranked {
			if r.lon < -180 || r.lon >= 180 {
				t.Fatalf("step %v: dilated centre %v,%v outside [-180,180)", step, r.lat, r.lon)
			}
		}
	}
}

func TestDuctingTrimKeepsARealSecondArea(t *testing.T) {
	// Three nodes around Denver, two around Salt Lake City (~600 km): a
	// two-city mesh, not bad positions. Nothing is trimmed.
	nodes := [][2]float64{{39.7, -104.9}, {39.8, -105.0}, {39.6, -104.8}, {40.7, -111.9}, {40.8, -111.8}}
	kept, trimmed := ductingTrimOutliers(nodes)
	if trimmed != 0 || len(kept) != len(nodes) {
		t.Fatalf("trimmed %d of %d, want 0", trimmed, len(nodes))
	}
}

func TestDuctingTrimDropsAFewCoLocatedBadPositions(t *testing.T) {
	// 18 nodes near (45, 10) and 2 at the same wrong fix: 10% of the nodes,
	// within ductingTrimMaxShare, so both go.
	var nodes [][2]float64
	for i := 0; i < 18; i++ {
		nodes = append(nodes, [2]float64{45 + float64(i%6)*0.05, 10 + float64(i/6)*0.05})
	}
	nodes = append(nodes, [2]float64{-33, 151}, [2]float64{-33, 151})
	kept, trimmed := ductingTrimOutliers(nodes)
	if trimmed != 2 || len(kept) != 18 {
		t.Fatalf("trimmed %d, kept %d; want 2 and 18", trimmed, len(kept))
	}
}
