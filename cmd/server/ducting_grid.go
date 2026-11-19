package main

import (
	"math"
	"sort"
)

const (
	ductingMaxAbsLat = 85.0 // cells are clamped to |lat| <= 85
	ductingMaxStep   = 8.0

	// With at least ductingTrimMinNodes nodes, the bounding box ignores nodes
	// farther from the nodes' median position than max(ductingTrimFloorKm,
	// ductingTrimMADs x the median distance from it), so a few far-away (or
	// misconfigured, even co-located) nodes cannot stretch the grid across
	// an ocean. Fewer nodes are too few for a median to mean anything.
	ductingTrimMinNodes = 5
	ductingTrimFloorKm  = 500.0
	ductingTrimMADs     = 4.0
)

type ductingCell struct{ lat, lon float64 }

func ductingCentre(coord, step float64) float64 {
	return math.Floor(coord/step)*step + step/2
}

// ductingWrapLon maps lon into [-180, 180): -180.25 -> 179.75, 180 -> -180.
func ductingWrapLon(lon float64) float64 {
	lon = math.Mod(lon+180, 360)
	if lon < 0 {
		lon += 360
	}
	return lon - 180
}

// ductingTidyCoord rounds away float noise from the lattice arithmetic, so
// the same centre reached two ways compares equal.
func ductingTidyCoord(v float64) float64 {
	return math.Round(v*1e9) / 1e9
}

// ductingNormalizeCells wraps every centre's longitude into [-180, 180),
// sorts (lat, lon) and drops duplicates, in place.
func ductingNormalizeCells(cells [][2]float64) [][2]float64 {
	for i := range cells {
		cells[i] = [2]float64{ductingTidyCoord(cells[i][0]), ductingTidyCoord(ductingWrapLon(cells[i][1]))}
		if cells[i][1] >= 180 { // rounding up from just below 180
			cells[i][1] = -180
		}
	}
	ductingSortCells(cells)
	out := cells[:0]
	for i, c := range cells {
		if i == 0 || c != cells[i-1] {
			out = append(out, c)
		}
	}
	return out
}

func ductingSortCells(c [][2]float64) {
	sort.Slice(c, func(i, j int) bool {
		if c[i][0] != c[j][0] {
			return c[i][0] < c[j][0]
		}
		return c[i][1] < c[j][1]
	})
}

// ductingRanked is a candidate cell with what the 8-degree trim ranks on.
type ductingRanked struct {
	lat, lon float64
	count    int // nodes inside the cell; 0 for margin-only cells
	dist     int // Chebyshev ring distance to the nearest node cell
}

// ductingDilateBound caps the dilated cell set at this multiple of maxPoints
// so a large margin cannot allocate without limit; past it a step counts as
// "too many".
const ductingDilateBound = 50

// ductingGridInfo describes a grid for the refresh log line.
type ductingGridInfo struct {
	box     [4]float64 // south, west, north, east covered; west > east when it crosses 180
	area    bool       // box is the configured area
	hasBox  bool       // false: no area and no usable node
	nodes   int        // nodes inside the box (after trimming)
	trimmed int        // nodes left out of the box as outliers
}

// ductingGrid is ductingGridWithInfo without the info.
func ductingGrid(nodes [][2]float64, area *[4]float64, step float64, maxPoints int, marginDeg float64) (cells [][2]float64, usedStep float64, fellBack bool) {
	cells, usedStep, fellBack, _ = ductingGridWithInfo(nodes, area, step, maxPoints, marginDeg)
	return cells, usedStep, fellBack
}

// ductingGridWithInfo returns the cell centres to query, sorted (lat, lon),
// de-duplicated and with longitudes in [-180, 180). nodes are [lat, lon];
// area is [south, west, north, east]. With an area, all cells covering it
// (marginDeg is ignored). Otherwise the bounding box of the nodes (with
// ductingTrimMinNodes or more, only those ductingTrimOutliers keeps), grown
// by marginDeg on every side, is filled edge to edge (marginDeg 0 = the
// bounding box itself). If there are more than maxPoints cells the step
// doubles (up to 8 degrees). If the box still
// does not fit at 8 degrees, it falls back to the cells containing a node plus
// marginDeg of cells around each (recounted per step; past 8 degrees the
// maxPoints best cells are kept: most nodes first, then nearest a node cell)
// and fellBack is true; that fallback covers every node, outliers included.
// Returns the step actually used.
func ductingGridWithInfo(nodes [][2]float64, area *[4]float64, step float64, maxPoints int, marginDeg float64) (cells [][2]float64, usedStep float64, fellBack bool, info ductingGridInfo) {
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
	inBox, trimmed := ductingTrimOutliers(valid)
	boxes := ductingNodeBoxes(inBox, marginDeg)
	switch {
	case area != nil:
		info = ductingGridInfo{box: *area, area: true, hasBox: true}
	case len(boxes) > 0:
		info = ductingGridInfo{
			box:    [4]float64{boxes[0][0], boxes[0][1], boxes[0][2], boxes[len(boxes)-1][3]},
			hasBox: true, nodes: len(inBox), trimmed: trimmed,
		}
	}
	startStep := step
	useBox := area == nil && len(valid) > 0
	for {
		var ranked []ductingRanked
		switch {
		case area != nil:
			cells = ductingNormalizeCells(ductingAreaCells(*area, step, maxPoints))
		case useBox:
			cells = ductingBoxCells(boxes, step, maxPoints)
			if len(cells) > maxPoints {
				if step*2 <= ductingMaxStep {
					step *= 2
				} else {
					// Even at 8 degrees the box is too big: cover the regions
					// around the nodes instead, from the configured step.
					useBox, fellBack, step = false, true, startStep
				}
				continue
			}
			return cells, step, false, info
		default:
			k := 0
			if marginDeg > 0 {
				k = int(math.Ceil(marginDeg/step - 1e-9))
			}
			var ok bool
			ranked, ok = ductingDilate(valid, step, k, ductingDilateBound*maxPoints)
			if !ok {
				if step*2 <= ductingMaxStep {
					step *= 2
					continue
				}
				// Last step and still huge: fall back to node cells only.
				ranked, _ = ductingDilate(valid, step, 0, math.MaxInt)
			}
			cells = make([][2]float64, len(ranked))
			for i, r := range ranked {
				cells[i] = [2]float64{r.lat, r.lon}
			}
			cells = ductingNormalizeCells(cells)
		}
		if len(cells) <= maxPoints {
			return cells, step, fellBack, info
		}
		if step*2 <= ductingMaxStep {
			step *= 2
			continue
		}
		if area != nil {
			// Area grid has no node counts: keep the first maxPoints in order.
			return cells[:maxPoints], step, false, info
		}
		sort.Slice(ranked, func(i, j int) bool {
			a, b := ranked[i], ranked[j]
			if a.count != b.count {
				return a.count > b.count
			}
			if a.dist != b.dist {
				return a.dist < b.dist
			}
			if a.lat != b.lat {
				return a.lat < b.lat
			}
			return a.lon < b.lon
		})
		top := make([][2]float64, maxPoints)
		for i := range top {
			top[i] = [2]float64{ranked[i].lat, ranked[i].lon}
		}
		return ductingNormalizeCells(top), step, fellBack, info
	}
}

// ductingNodeBoxes returns the [south, west, north, east] boxes covering the
// nodes plus margin degrees on every side: one box, or two when it crosses
// the antimeridian (west..180 and -180..east). The longitude span is the
// smallest one containing every node: the complement of the largest gap
// between consecutive longitudes (the wrap gap included). Latitudes are
// clamped to +-85; a span of 360 degrees or more becomes -180..180.
func ductingNodeBoxes(valid [][2]float64, margin float64) [][4]float64 {
	if len(valid) == 0 {
		return nil
	}
	if !(margin > 0) {
		margin = 0
	}
	south, north := valid[0][0], valid[0][0]
	lons := make([]float64, len(valid))
	for i, n := range valid {
		south, north = math.Min(south, n[0]), math.Max(north, n[0])
		lons[i] = n[1]
	}
	sort.Float64s(lons)
	west, east := ductingLonArc(lons)
	south = math.Max(-ductingMaxAbsLat, south-margin)
	north = math.Min(ductingMaxAbsLat, north+margin)
	west, east = west-margin, east+margin
	if east-west >= 360 {
		west, east = -180, 180
	} else if west < -180 {
		west, east = west+360, east+360
	}
	if east <= 180 {
		return [][4]float64{{south, west, north, east}}
	}
	return [][4]float64{{south, west, north, 180}, {south, -180, north, east - 360}}
}

// ductingLonArc returns the smallest longitude arc containing every one of
// lons (sorted ascending, non-empty) as an unwrapped [west, east]: the
// complement of the largest gap between consecutive longitudes (the wrap gap
// included, which wins ties). east may exceed 180 when the arc crosses the
// antimeridian; every lon is in [west, east] once lons below west get +360.
func ductingLonArc(lons []float64) (west, east float64) {
	last := len(lons) - 1
	west, east = lons[0], lons[last]
	best := lons[0] + 360 - lons[last] // the wrap gap: no crossing
	for i := 0; i < last; i++ {
		if g := lons[i+1] - lons[i]; g > best {
			best, west, east = g, lons[i+1], lons[i]+360
		}
	}
	return west, east
}

// ductingMedian is the median of v (non-empty); v is sorted in place.
func ductingMedian(v []float64) float64 {
	sort.Float64s(v)
	k := len(v) / 2
	if len(v)%2 == 1 {
		return v[k]
	}
	return (v[k-1] + v[k]) / 2
}

// ductingTrimOutliers drops the nodes farther (great circle) from the nodes'
// median position than max(ductingTrimFloorKm, ductingTrimMADs x the median
// of those distances), and returns how many it dropped. The median position
// is the median latitude and the median longitude, the latter measured on
// the unwrapped axis of the smallest arc containing every node, so a mesh
// across the antimeridian is one cluster, not two ends of the world. Medians
// ignore any minority of nodes, so several nodes advertising the same wrong
// position are dropped as surely as one; the floor keeps a compact mesh
// (median distance near 0) from losing its outer nodes. With fewer than
// ductingTrimMinNodes nodes it returns them all.
// ductingTrimMaxShare is the largest share of nodes the outlier filter may
// drop (but always at least one): a bigger group far from the median cannot
// be told apart from a real second area by position alone, so it is kept.
const ductingTrimMaxShare = 0.10

func ductingTrimOutliers(valid [][2]float64) (kept [][2]float64, trimmed int) {
	if len(valid) < ductingTrimMinNodes {
		return valid, 0
	}
	lats := make([]float64, len(valid))
	lons := make([]float64, len(valid))
	for i, n := range valid {
		lats[i], lons[i] = n[0], n[1]
	}
	sort.Float64s(lons)
	west, _ := ductingLonArc(lons)
	for i, n := range valid {
		lons[i] = n[1]
		if lons[i] < west {
			lons[i] += 360
		}
	}
	medLat, medLon := ductingMedian(lats), ductingMedian(lons)
	dist := make([]float64, len(valid))
	for i, n := range valid {
		dist[i] = haversineKm(medLat, medLon, n[0], n[1])
	}
	limit := math.Max(ductingTrimFloorKm, ductingTrimMADs*ductingMedian(append([]float64(nil), dist...)))
	kept = make([][2]float64, 0, len(valid))
	for i, n := range valid {
		if dist[i] <= limit {
			kept = append(kept, n)
		}
	}
	// Outliers are a few bad positions, not a region: if more than
	// ductingTrimMaxShare of the nodes (or more than one, in a small mesh)
	// would go, the mesh really spans several areas, so keep them all (the
	// box, or its fallback, covers it).
	allowed := math.Max(1, math.Floor(ductingTrimMaxShare*float64(len(valid))))
	if trimmed = len(valid) - len(kept); float64(trimmed) > allowed {
		return valid, 0
	}
	return kept, trimmed
}

// ductingBoxCells fills the boxes with the area lattice (same cell centres as
// ductingAreaCells), normalized by ductingNormalizeCells (a centre on 180
// becomes -180). It stops once it has more than limit cells. A zero-width box (one node, margin 0) still yields its cell.
func ductingBoxCells(boxes [][4]float64, step float64, limit int) [][2]float64 {
	var cells [][2]float64
	for _, b := range boxes {
		if !(b[2] > b[0]) {
			b[2] = b[0] + 1e-6
		}
		if !(b[3] > b[1]) {
			b[3] = b[1] + 1e-6
		}
		cells = append(cells, ductingAreaCells(b, step, limit)...)
		if len(cells) > limit {
			break
		}
	}
	return ductingNormalizeCells(cells)
}

type ductingIdx struct{ lat, lon int64 }

// ductingDilate returns the node cells at step plus k rings of cells around
// each (a (2k+1)x(2k+1) block). With k == 0 these are exactly the cells
// containing a node. Cell centres share ductingCentre's lattice; latitude
// centres beyond +-85 are dropped from the rings and longitudes wrap into
// [-180,180) so cells stay valid and de-duplicated across the antimeridian.
// ok is false when the set grows past limit cells.
func ductingDilate(valid [][2]float64, step float64, k int, limit int) (out []ductingRanked, ok bool) {
	if k == 0 {
		counts := make(map[ductingCell]int)
		for _, n := range valid {
			lon := ductingTidyCoord(ductingWrapLon(ductingCentre(n[1], step)))
			counts[ductingCell{ductingCentre(n[0], step), lon}]++
		}
		out = make([]ductingRanked, 0, len(counts))
		for c, n := range counts {
			out = append(out, ductingRanked{lat: c.lat, lon: c.lon, count: n})
		}
		return out, true
	}
	// Cells per full turn of longitude; when whole, indices wrap exactly.
	period := 360 / step
	whole := math.Abs(period-math.Round(period)) < 1e-9
	p := int64(math.Round(period))
	reduce := func(j int64) int64 {
		if !whole {
			return j
		}
		return ((j % p) + p) % p
	}
	counts := make(map[ductingIdx]int)
	for _, n := range valid {
		counts[ductingIdx{int64(math.Floor(n[0] / step)), reduce(int64(math.Floor(n[1] / step)))}]++
	}
	dist := make(map[ductingIdx]int)
	k64 := int64(k)
	for c := range counts {
		for di := -k64; di <= k64; di++ {
			lat := float64(c.lat+di)*step + step/2
			for dj := -k64; dj <= k64; dj++ {
				d := int(max(abs64(di), abs64(dj)))
				if d > 0 && math.Abs(lat) > ductingMaxAbsLat {
					continue
				}
				key := ductingIdx{c.lat + di, reduce(c.lon + dj)}
				if old, seen := dist[key]; !seen || d < old {
					dist[key] = d
				}
				if len(dist) > limit {
					return nil, false
				}
			}
		}
	}
	out = make([]ductingRanked, 0, len(dist))
	for key, d := range dist {
		lon := ductingWrapLon(float64(key.lon)*step + step/2)
		out = append(out, ductingRanked{
			lat: float64(key.lat)*step + step/2, lon: lon, count: counts[key], dist: d,
		})
	}
	return out, true
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// ductingAreaCells lists the cell centres covering area, longitudes wrapped
// into [-180, 180) (a centre past 180 is the cell across the antimeridian);
// unsorted, and may hold duplicates: callers normalize. It stops once it has
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
			cells = append(cells, [2]float64{lat, ductingWrapLon(lon)})
			if len(cells) > limit {
				return cells
			}
		}
	}
	return cells
}
