package main

// Viewshed raster for the planner's Coverage tab: for one candidate site,
// each cell of a grid around it gets a Users class (handheld reach curve,
// lowered in deep terrain shadow) and a Repeaters class (the same LOS
// verdict a Chain leg site→cell would get).

import (
	"context"
	"math"
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
	SkipUsers                             bool // Users nibble stays 0: skips its shadow walk
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

func verdictClass(verdict string) byte {
	switch verdict {
	case "clear":
		return 4
	case "clear60":
		return 3
	case "marginal":
		return 2
	}
	return 1
}

// computeViewshed profiles site→cell for every cell in range: two
// clearance walks per cell (receiver at RxM for Repeaters, at
// viewshedUserRxM for the Users shadow test). progress gets the fraction of
// rows done. Errors: ctx.Err() only.
func computeViewshed(ctx context.Context, v terrainView, p viewshedParams, progress func(float64)) (viewshedGrid, error) {
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
	e := newPlanLinkEval(ctx, v, p.FreqMHz, p.KFactor)
	dLat := (bb.N - bb.S) / float64(rows)
	dLon := (bb.E - bb.W) / float64(cols)
	for r := 0; r < rows; r++ {
		lat := bb.N - (float64(r)+0.5)*dLat
		for c := 0; c < cols; c++ {
			cell := geoPoint{Lat: lat, Lon: bb.W + (float64(c)+0.5)*dLon}
			d := geoDistKm(p.Site, cell)
			if d > p.RangeKm {
				continue
			}
			cr, ok := e.clearance(p.Site, cell, d, p.SiteM, p.RxM, math.Inf(-1))
			if !ok {
				continue // no terrain on the path (or ctx cancelled: checked below)
			}
			var users byte
			if !p.SkipUsers {
				users = userReachClass(userReachP(d, p.Reach))
				// Same path as above, so terrain is present; the walk stops at
				// the first sample below the shadow threshold.
				if cu, ok := e.clearance(p.Site, cell, d, p.SiteM, viewshedUserRxM, viewshedShadowClearance); !ok && cu < viewshedShadowClearance {
					if users > viewshedShadowDrop {
						users -= viewshedShadowDrop
					} else {
						users = 1
					}
				}
			}
			g.Cells[r*cols+c] = users | verdictClass(losVerdict(cr))<<4
		}
		if e.err != nil {
			return viewshedGrid{}, e.err
		}
		if progress != nil {
			progress(float64(r+1) / float64(rows))
		}
	}
	if err := ctx.Err(); err != nil {
		return viewshedGrid{}, err
	}
	return g, nil
}
