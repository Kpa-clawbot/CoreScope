package main

import (
	"math"
	"sort"
)

const (
	ductingMaxAbsLat = 85.0 // cells are clamped to |lat| <= 85
	ductingMaxStep   = 8.0
)

type ductingCell struct{ lat, lon float64 }

func ductingCentre(coord, step float64) float64 {
	return math.Floor(coord/step)*step + step/2
}

func ductingSortCells(c [][2]float64) {
	sort.Slice(c, func(i, j int) bool {
		if c[i][0] != c[j][0] {
			return c[i][0] < c[j][0]
		}
		return c[i][1] < c[j][1]
	})
}

// ductingGrid returns the cell centres to query, sorted (lat, lon) and
// de-duplicated. nodes are [lat, lon]; area is [south, west, north, east].
// With an area, all cells covering it; otherwise the cells containing a
// node. If there are more than maxPoints cells the step doubles (up to 8
// degrees); past that the maxPoints cells holding the most nodes are kept.
// Returns the step actually used.
func ductingGrid(nodes [][2]float64, area *[4]float64, step float64, maxPoints int) ([][2]float64, float64) {
	if !(step > 0) || math.IsInf(step, 0) {
		step = defaultDuctingGridStepDeg
	}
	var valid [][2]float64
	if area == nil {
		for _, n := range nodes {
			lat, lon := n[0], n[1]
			if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) ||
				(lat == 0 && lon == 0) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
				continue
			}
			if lon == 180 {
				lon = -180 // same meridian; keeps cell centres within range
			}
			valid = append(valid, [2]float64{math.Max(-ductingMaxAbsLat, math.Min(ductingMaxAbsLat, lat)), lon})
		}
	}
	var counts map[ductingCell]int
	for {
		var cells [][2]float64
		if area != nil {
			cells = ductingAreaCells(*area, step, maxPoints)
		} else {
			counts = make(map[ductingCell]int)
			for _, n := range valid {
				lon := ductingCentre(n[1], step)
				if lon > 180 { // a wide cell straddling the antimeridian
					lon -= 360
				}
				counts[ductingCell{ductingCentre(n[0], step), lon}]++
			}
			cells = make([][2]float64, 0, len(counts))
			for c := range counts {
				cells = append(cells, [2]float64{c.lat, c.lon})
			}
			ductingSortCells(cells)
		}
		if len(cells) <= maxPoints {
			return cells, step
		}
		if step*2 <= ductingMaxStep {
			step *= 2
			continue
		}
		if area != nil {
			// Area grid has no node counts: keep the first maxPoints in order.
			return cells[:maxPoints], step
		}
		sort.SliceStable(cells, func(i, j int) bool {
			return counts[ductingCell{cells[i][0], cells[i][1]}] > counts[ductingCell{cells[j][0], cells[j][1]}]
		})
		top := cells[:maxPoints]
		ductingSortCells(top)
		return top, step
	}
}

// ductingAreaCells lists the cell centres covering area. It stops once it has
// more than limit cells (the caller only needs to know it is too many) so a
// huge area with a tiny step cannot allocate unbounded memory.
func ductingAreaCells(area [4]float64, step float64, limit int) [][2]float64 {
	south := math.Max(area[0], -ductingMaxAbsLat)
	north := math.Min(area[2], ductingMaxAbsLat)
	west, east := area[1], area[3]
	if !(south < north) || !(west < east) {
		return nil
	}
	var cells [][2]float64
	for lat := ductingCentre(south, step); lat-step/2 < north; lat += step {
		for lon := ductingCentre(west, step); lon-step/2 < east; lon += step {
			if lon > 180 {
				continue // Open-Meteo rejects longitudes beyond 180
			}
			cells = append(cells, [2]float64{lat, lon})
			if len(cells) > limit {
				return cells
			}
		}
	}
	return cells
}
