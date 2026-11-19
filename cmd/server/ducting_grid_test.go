package main

import (
	"math"
	"sort"
	"testing"
)

func TestDuctingGridNodeCells(t *testing.T) {
	nodes := [][2]float64{{47.61, -122.33}, {47.70, -122.30}, {40.71, -74.00}}
	cells, step := ductingGrid(nodes, nil, 0.5, 100)
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
	cells, _ := ductingGrid(nodes, nil, 0.5, 100)
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
	cells, _ := ductingGrid(nodes, nil, 0.5, 100)
	if len(cells) != 1 || cells[0] != [2]float64{50.25, 8.25} {
		t.Fatalf("cells %v", cells)
	}
	if cells, _ := ductingGrid(nil, nil, 0.5, 10); len(cells) != 0 {
		t.Fatalf("empty: %v", cells)
	}
}

func TestDuctingGridClampsLatitude(t *testing.T) {
	cells, _ := ductingGrid([][2]float64{{89.9, 10.1}, {-89.9, 10.1}}, nil, 0.5, 10)
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
	cells, step := ductingGrid(nil, &area, 0.5, 1000)
	if step != 0.5 || len(cells) != 4*6 {
		t.Fatalf("step %v count %d", step, len(cells))
	}
	if cells[0] != [2]float64{10.25, 20.25} || cells[len(cells)-1] != [2]float64{11.75, 22.75} {
		t.Fatalf("first %v last %v", cells[0], cells[len(cells)-1])
	}
	// unaligned area still fully covered
	area = [4]float64{10.1, 20.1, 10.6, 20.6}
	cells, _ = ductingGrid(nil, &area, 0.5, 1000)
	if len(cells) != 4 {
		t.Fatalf("unaligned: %v", cells)
	}
	// nodes are ignored when an area is given
	cells, _ = ductingGrid([][2]float64{{60, 60}}, &[4]float64{10, 20, 10.5, 20.5}, 0.5, 10)
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
	cells, step := ductingGrid(nodes, nil, 0.5, 5)
	if step != 2 {
		t.Fatalf("step %v, want 2 (cells %v)", step, cells)
	}
	if len(cells) != 5 {
		t.Fatalf("cells %d", len(cells))
	}
	// area too large for step 0.5 doubles too
	_, step = ductingGrid(nil, &[4]float64{0, 0, 10, 10}, 0.5, 50)
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
	cells, step := ductingGrid(nodes, nil, 0.5, 2)
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
	cells, _ = ductingGrid([][2]float64{{60.1, 100.1}, {-60.1, -100.1}, {0.1, 0.1}}, nil, 0.5, 2)
	if len(cells) != 2 || cells[0][0] != -60 || cells[1][0] != 4 {
		t.Fatalf("ties: %v", cells)
	}
}

func TestDuctingGridBadStep(t *testing.T) {
	cells, step := ductingGrid([][2]float64{{10.1, 10.1}}, nil, 0, 10)
	if step != 0.5 || len(cells) != 1 {
		t.Fatalf("%v %v", cells, step)
	}
}

func TestDuctingGridAntimeridian(t *testing.T) {
	cells, _ := ductingGrid([][2]float64{{10.1, 180}, {10.1, -180}}, nil, 0.5, 10)
	if len(cells) != 1 || cells[0] != [2]float64{10.25, -179.75} {
		t.Fatalf("lon 180/-180: %v", cells)
	}
	// wide cell around the antimeridian never yields a centre past 180
	cells, _ = ductingGrid([][2]float64{{10.1, 179}}, nil, 8, 10)
	for _, c := range cells {
		if c[1] > 180 || c[1] < -180 {
			t.Fatalf("out of range: %v", cells)
		}
	}
	for _, step := range []float64{0.25, 0.3, 0.5, 2} {
		cells, _ = ductingGrid(nil, &[4]float64{10, 170, 11, 180}, step, 1000)
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
