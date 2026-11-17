package main

// Viewshed raster for the planner's Coverage tab: for one candidate site,
// each cell of a grid around it gets a Users class (handheld reach curve,
// lowered in deep terrain shadow) and a Repeaters class (the same LOS
// verdict a Chain leg site→cell would get).

import (
	"context"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
)

const (
	// viewshedUserRxM: handheld height above ground for the shadow test.
	viewshedUserRxM = 1.5
	// viewshedShadowClearance: a ground path whose worst first-Fresnel
	// clearance is below this (the LOS this many F1 radii under terrain)
	// counts as deep shadow.
	viewshedShadowClearance = -1.0
	// viewshedShadowDrop: Users classes dropped in deep shadow (floor 1).
	viewshedShadowDrop = 2
)

type viewshedParams struct {
	Site                                  geoPoint
	SiteM, RxM, RangeKm, FreqMHz, KFactor float64
	Reach                                 PlannerUserReach
}

// viewshedGrid: Rows×Cols cells over BBox, row 0 north, row-major; cell
// (r, c) is centred at the middle of the r-th of Rows equal latitude bands
// (from N) and the c-th of Cols equal longitude bands (from W). Each byte
// is users | repeaters<<4; a nibble of 0 means no data (outside RangeKm, or
// no terrain at the cell, the site or on the path between).
type viewshedGrid struct {
	BBox       geoBBox
	Rows, Cols int
	CellKm     float64
	Cells      []byte
}

func viewshedBBox(site geoPoint, rangeKm float64) geoBBox {
	return plannerExpandBBox(geoBBox{S: site.Lat, W: site.Lon, N: site.Lat, E: site.Lon}, rangeKm)
}

// viewshedCellKm: 200 m, coarser above 30 km so a grid stays near 300×300.
func viewshedCellKm(rangeKm float64) float64 { return math.Max(0.2, rangeKm/150) }

// userReachP is the handheld reach curve P = 1/(1+exp((ln d − ln d50)/s)).
func userReachP(dKm float64, r PlannerUserReach) float64 {
	return 1 / (1 + math.Exp((math.Log(dKm)-math.Log(r.D50Km))/r.Slope))
}

func userReachClass(p float64) byte {
	switch {
	case p < 0.10:
		return 1
	case p < 0.25:
		return 2
	case p < 0.50:
		return 3
	case p < 0.75:
		return 4
	}
	return 5
}

// viewshedNearClearance is the lower bound of the Repeaters "slightly
// blocked" class: the line of sight at most this fraction of the first
// Fresnel radius under terrain. Calibrated loosely on one repeater
// (2026-11-16, 17 heard links): every link heard within 16 km had a clearance
// of 2% to 54%, so the textbook 60%/100% classes never mark working links on
// SRTM; within 20 km, 12 of 15 links with clearance >= 0 were heard against 1
// of 8 below 0; one link was heard at -20%.
const viewshedNearClearance = -0.2

// repeaterClass maps a first-Fresnel clearance (1 = fully clear) to the
// Repeaters class: 3 line of sight (>= 0), 2 slightly blocked (down to
// viewshedNearClearance), 1 blocked.
func repeaterClass(c float64) byte {
	switch {
	case c >= 0:
		return 3
	case c >= viewshedNearClearance:
		return 2
	}
	return 1
}

// viewshedWorkers is how many goroutines computeViewshed spreads rows over.
// The planner runs one Coverage/Viewshed job at a time (plannerCovSlots), so
// this is also the most CPUs a viewshed takes from the live app.
func viewshedWorkers() int { return max(1, min(4, runtime.GOMAXPROCS(0)/2)) }

// clearancePair is two e.clearance walks of the same path a→b in one pass:
// the worst first-Fresnel clearance with the receiver hbR above ground
// (floor −Inf, every sample: crOK=false means no terrain on the path, or ctx
// cancelled with e.err set), and whether the worst clearance with the
// receiver hbU is below shadowFloor. Each result equals that of its own
// e.clearance call.
func (e *planLinkEval) clearancePair(a, b geoPoint, distKm, ha, hbR, hbU, shadowFloor float64) (cr float64, crOK, shadow bool) {
	cr, cu, ok := e.clearanceWalk(a, b, distKm, ha, hbR, math.Inf(-1), hbU, true)
	if !ok {
		return 0, false, false
	}
	return cr, true, cu < shadowFloor
}

// computeViewshed profiles site→cell for every cell in range: two
// clearance walks per cell (receiver at RxM for Repeaters, at
// viewshedUserRxM for the Users shadow test). progress gets the fraction of
// rows done. Errors: ctx.Err() only.
func computeViewshed(ctx context.Context, v terrainView, p viewshedParams, progress func(float64)) (viewshedGrid, error) {
	return computeViewshedN(ctx, v, p, viewshedWorkers(), progress)
}

// computeViewshedN is computeViewshed over up to workers goroutines. Cells are
// independent and each row's bytes are written by one worker only, so the
// grid is identical for any workers. progress is called serially with a
// non-decreasing fraction that ends at exactly 1.
func computeViewshedN(ctx context.Context, v terrainView, p viewshedParams, workers int, progress func(float64)) (viewshedGrid, error) {
	cellKm := viewshedCellKm(p.RangeKm)
	bb := viewshedBBox(p.Site, p.RangeKm)
	cosLat := math.Cos(p.Site.Lat * math.Pi / 180)
	rows := max(1, int(math.Ceil((bb.N-bb.S)*plannerKmPerDeg/cellKm)))
	cols := max(1, int(math.Ceil((bb.E-bb.W)*plannerKmPerDeg*cosLat/cellKm)))
	g := viewshedGrid{BBox: bb, Rows: rows, Cols: cols, CellKm: cellKm, Cells: make([]byte, rows*cols)}
	if err := ctx.Err(); err != nil {
		return viewshedGrid{}, err
	}
	if _, ok := v.ElevationAt(p.Site.Lat, p.Site.Lon); !ok {
		return g, nil // no terrain at the site: every cell is no data
	}
	dLat := (bb.N - bb.S) / float64(rows)
	dLon := (bb.E - bb.W) / float64(cols)
	// row fills row r with e; it reports e.err (ctx cancelled).
	row := func(e *planLinkEval, r int) error {
		lat := bb.N - (float64(r)+0.5)*dLat
		for c := 0; c < cols; c++ {
			cell := geoPoint{Lat: lat, Lon: bb.W + (float64(c)+0.5)*dLon}
			d := geoDistKm(p.Site, cell)
			if d > p.RangeKm {
				continue
			}
			cr, ok, shadow := e.clearancePair(p.Site, cell, d, p.SiteM, p.RxM, viewshedUserRxM, viewshedShadowClearance)
			if !ok {
				continue // no terrain on the path (or ctx cancelled: checked below)
			}
			users := userReachClass(userReachP(d, p.Reach))
			if shadow {
				if users > viewshedShadowDrop {
					users -= viewshedShadowDrop
				} else {
					users = 1
				}
			}
			g.Cells[r*cols+c] = users | repeaterClass(cr)<<4
		}
		return e.err
	}
	workers = max(1, min(workers, rows))
	if workers == 1 {
		e := newPlanLinkEval(ctx, v, p.FreqMHz, p.KFactor)
		for r := 0; r < rows; r++ {
			if err := row(e, r); err != nil {
				return viewshedGrid{}, err
			}
			if progress != nil {
				progress(float64(r+1) / float64(rows))
			}
		}
	} else {
		// Rows are handed out one at a time (they differ in length), each
		// worker with its own planLinkEval, which is not safe to share.
		var (
			next, stop atomic.Int64
			mu         sync.Mutex // serialises progress and the first error
			done       int
			firstErr   error
			panicked   any
			wg         sync.WaitGroup
		)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					// Re-raised below on the caller's goroutine, where the
					// job runner recovers it; here it would kill the process.
					if r := recover(); r != nil {
						mu.Lock()
						if panicked == nil {
							panicked = r
						}
						mu.Unlock()
						stop.Store(1)
					}
				}()
				e := newPlanLinkEval(ctx, v, p.FreqMHz, p.KFactor)
				for stop.Load() == 0 {
					r := int(next.Add(1) - 1)
					if r >= rows {
						return
					}
					err := row(e, r)
					mu.Lock()
					if err != nil && firstErr == nil {
						firstErr = err
					}
					if err == nil {
						done++
						if progress != nil {
							progress(float64(done) / float64(rows))
						}
					}
					mu.Unlock()
					if err != nil {
						stop.Store(1)
						return
					}
				}
			}()
		}
		wg.Wait()
		if panicked != nil {
			panic(panicked)
		}
		if firstErr != nil {
			return viewshedGrid{}, firstErr
		}
	}
	if err := ctx.Err(); err != nil {
		return viewshedGrid{}, err
	}
	return g, nil
}
