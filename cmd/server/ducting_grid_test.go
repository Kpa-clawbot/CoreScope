package main

import (
	"math"
	"sort"
	"testing"
)

func TestDuctingGridNodeCells(t *testing.T) {
	nodes := [][2]float64{{47.61, -122.33}, {47.70, -122.30}, {40.71, -74.00}}
	cells, step := ductingGrid(nodes, nil, 0.5, 100, 0)
	if step != 0.5 {
		t.Fatalf("step %v", step)
	}
	want := [][2]float64{{40.75, -73.75}, {47.75, -122.25}}
	if len(cells) != len(want) {
		t.Fatalf("cells %v", cells)
	}
	for i := range want {
		if cells[i] != want[i] {
			t.Fatalf("cell %d = %v, want %v", i, cells[i], want[i])
		}
	}
}

func TestDuctingGridSortedDeduped(t *testing.T) {
	nodes := [][2]float64{{10.1, 20.1}, {10.2, 20.2}, {10.1, 5.1}, {-5.1, 30.1}, {10.1, 20.4}}
	cells, _ := ductingGrid(nodes, nil, 0.5, 100, 0)
	if len(cells) != 3 {
		t.Fatalf("cells %v", cells)
	}
	if !sort.SliceIsSorted(cells, func(i, j int) bool {
		if cells[i][0] != cells[j][0] {
			return cells[i][0] < cells[j][0]
		}
		return cells[i][1] < cells[j][1]
	}) {
		t.Fatalf("not sorted: %v", cells)
	}
}

func TestDuctingGridIgnoresBadNodes(t *testing.T) {
	nodes := [][2]float64{
		{0, 0}, {math.NaN(), 10}, {10, math.NaN()}, {math.Inf(1), 0}, {91, 10}, {10, 181}, {-95, 0},
		{50.1, 8.1},
	}
	cells, _ := ductingGrid(nodes, nil, 0.5, 100, 0)
	if len(cells) != 1 || cells[0] != [2]float64{50.25, 8.25} {
		t.Fatalf("cells %v", cells)
	}
	if cells, _ := ductingGrid(nil, nil, 0.5, 10, 0); len(cells) != 0 {
		t.Fatalf("empty: %v", cells)
	}
}

func TestDuctingGridClampsLatitude(t *testing.T) {
	cells, _ := ductingGrid([][2]float64{{89.9, 10.1}, {-89.9, 10.1}}, nil, 0.5, 10, 0)
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
	cells, step := ductingGrid(nil, &area, 0.5, 1000, 0)
	if step != 0.5 || len(cells) != 4*6 {
		t.Fatalf("step %v count %d", step, len(cells))
	}
	if cells[0] != [2]float64{10.25, 20.25} || cells[len(cells)-1] != [2]float64{11.75, 22.75} {
		t.Fatalf("first %v last %v", cells[0], cells[len(cells)-1])
	}
	// unaligned area still fully covered
	area = [4]float64{10.1, 20.1, 10.6, 20.6}
	cells, _ = ductingGrid(nil, &area, 0.5, 1000, 0)
	if len(cells) != 4 {
		t.Fatalf("unaligned: %v", cells)
	}
	// nodes are ignored when an area is given
	cells, _ = ductingGrid([][2]float64{{60, 60}}, &[4]float64{10, 20, 10.5, 20.5}, 0.5, 10, 0)
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
	cells, step := ductingGrid(nodes, nil, 0.5, 5, 0)
	if step != 2 {
		t.Fatalf("step %v, want 2 (cells %v)", step, cells)
	}
	if len(cells) != 5 {
		t.Fatalf("cells %d", len(cells))
	}
	// area too large for step 0.5 doubles too
	_, step = ductingGrid(nil, &[4]float64{0, 0, 10, 10}, 0.5, 50, 0)
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
	cells, step := ductingGrid(nodes, nil, 0.5, 2, 0)
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
	cells, _ = ductingGrid([][2]float64{{60.1, 100.1}, {-60.1, -100.1}, {0.1, 0.1}}, nil, 0.5, 2, 0)
	if len(cells) != 2 || cells[0][0] != -60 || cells[1][0] != 4 {
		t.Fatalf("ties: %v", cells)
	}
}

func TestDuctingGridBadStep(t *testing.T) {
	cells, step := ductingGrid([][2]float64{{10.1, 10.1}}, nil, 0, 10, 0)
	if step != 0.5 || len(cells) != 1 {
		t.Fatalf("%v %v", cells, step)
	}
}

func TestDuctingGridAntimeridian(t *testing.T) {
	cells, _ := ductingGrid([][2]float64{{10.1, 180}, {10.1, -180}}, nil, 0.5, 10, 0)
	if len(cells) != 1 || cells[0] != [2]float64{10.25, -179.75} {
		t.Fatalf("lon 180/-180: %v", cells)
	}
	// wide cell around the antimeridian never yields a centre past 180
	cells, _ = ductingGrid([][2]float64{{10.1, 179}}, nil, 8, 10, 0)
	for _, c := range cells {
		if c[1] > 180 || c[1] < -180 {
			t.Fatalf("out of range: %v", cells)
		}
	}
	for _, step := range []float64{0.25, 0.3, 0.5, 2} {
		cells, _ = ductingGrid(nil, &[4]float64{10, 170, 11, 180}, step, 1000, 0)
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

func TestDuctingGridMarginZeroIsNodeCells(t *testing.T) {
	nodes := [][2]float64{{47.61, -122.33}, {47.70, -122.30}, {40.71, -74.00}}
	a, sa := ductingGrid(nodes, nil, 0.5, 100, 0)
	if sa != 0.5 || len(a) != 2 || a[0] != [2]float64{40.75, -73.75} || a[1] != [2]float64{47.75, -122.25} {
		t.Fatalf("margin 0: %v %v", a, sa)
	}
	// a negative margin behaves as 0
	b, _ := ductingGrid(nodes, nil, 0.5, 100, -3)
	if len(b) != 2 {
		t.Fatalf("negative margin: %v", b)
	}
}

func TestDuctingGridMarginBlock(t *testing.T) {
	cells, step := ductingGrid([][2]float64{{10.1, 20.1}}, nil, 0.5, 100, 1)
	if step != 0.5 || len(cells) != 25 {
		t.Fatalf("step %v, %d cells: %v", step, len(cells), cells)
	}
	checkSortedUnique(t, cells)
	if !hasCell(cells, 10.25, 20.25) || !hasCell(cells, 9.25, 19.25) || !hasCell(cells, 11.25, 21.25) || hasCell(cells, 11.75, 20.25) {
		t.Fatalf("block wrong: %v", cells)
	}
	// margin not a multiple of the step rounds up: 0.6 at 0.5 = 2 rings
	cells, _ = ductingGrid([][2]float64{{10.1, 20.1}}, nil, 0.5, 100, 0.6)
	if len(cells) != 25 {
		t.Fatalf("0.6 margin: %d cells", len(cells))
	}
}

func TestDuctingGridMarginOverlapDeduped(t *testing.T) {
	// adjacent node cells: blocks of 9 overlap in 6 cells -> 12 cells
	cells, _ := ductingGrid([][2]float64{{10.1, 20.1}, {10.1, 20.6}}, nil, 0.5, 100, 0.5)
	if len(cells) != 12 {
		t.Fatalf("%d cells: %v", len(cells), cells)
	}
	checkSortedUnique(t, cells)
}

func TestDuctingGridMarginAntimeridian(t *testing.T) {
	for _, step := range []float64{0.5, 0.3, 8} {
		cells, _ := ductingGrid([][2]float64{{10.1, 179.9}, {10.1, -179.9}}, nil, step, 1000, 1)
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
	cells, _ := ductingGrid([][2]float64{{10.1, 179.9}}, nil, 0.5, 100, 1)
	if len(cells) != 25 || !hasCell(cells, 10.25, -179.75) || !hasCell(cells, 10.25, 179.75) {
		t.Fatalf("%d cells: %v", len(cells), cells)
	}
}

func TestDuctingGridMarginPolarClamp(t *testing.T) {
	cells, _ := ductingGrid([][2]float64{{84.9, 10.1}}, nil, 0.5, 100, 2)
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
	cells, step := ductingGrid([][2]float64{{10.1, 20.1}}, nil, 0.5, 10, 1)
	if step != 1 || len(cells) != 9 {
		t.Fatalf("step %v, %d cells", step, len(cells))
	}
	// maxPoints 5: step 2 gives k=1 -> 9 > 5, step 4 gives k=1 -> 9, step 8 k=1.
	cells, step = ductingGrid([][2]float64{{10.1, 20.1}}, nil, 0.5, 5, 1)
	if step != 8 || len(cells) != 5 {
		t.Fatalf("step %v, %d cells", step, len(cells))
	}
	// margin 1 at step 2 is k=1 (9 cells), margin 1 at step 0.5 is k=2: confirm k recomputed
	cells, step = ductingGrid([][2]float64{{10.1, 20.1}}, nil, 0.5, 9, 1)
	if step != 1 || len(cells) != 9 {
		t.Fatalf("recompute: step %v, %d cells", step, len(cells))
	}
}

func TestDuctingGridMarginTrimKeepsNodeCellsFirst(t *testing.T) {
	// Three far-apart nodes at step 8 with margin 2: 9 cells each, maxPoints 4.
	nodes := [][2]float64{{-60.1, -100.1}, {-60.2, -100.2}, {0.1, 0.1}, {60.1, 100.1}}
	cells, step := ductingGrid(nodes, nil, 0.5, 4, 2)
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
	cells, _ = ductingGrid(nodes, nil, 0.5, 2, 2)
	if len(cells) != 2 || !hasCell(cells, ductingCentre(-60.1, 8), ductingCentre(-100.1, 8)) {
		t.Fatalf("top 2: %v", cells)
	}
}

func TestDuctingGridMarginIgnoredWithArea(t *testing.T) {
	area := [4]float64{10, 20, 12, 22}
	a, sa := ductingGrid(nil, &area, 0.5, 1000, 0)
	b, sb := ductingGrid(nil, &area, 0.5, 1000, 10)
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
	cells, step := ductingGrid(nodes, nil, 0.25, 50, 10)
	if len(cells) == 0 || len(cells) > 50 || step > 8 {
		t.Fatalf("%d cells at step %v", len(cells), step)
	}
	checkSortedUnique(t, cells)
}
