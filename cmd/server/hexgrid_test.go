package main

import (
	"math"
	"testing"
)

// TestHexCellAtClampsPolarLatitude verifies #17: latitudes past the Web Mercator
// limit are clamped, so near-polar submissions bin to the edge cell and produce
// finite geometry instead of NaN rings.
func TestHexCellAtClampsPolarLatitude(t *testing.T) {
	for _, lat := range []float64{89.9, 90.0, -89.9, -90.0} {
		cell := hexCellAt(lat, 3.72, 9)
		clamped := math.Copysign(hexMaxLat, lat)
		if want := hexCellAt(clamped, 3.72, 9); cell != want {
			t.Fatalf("lat %.1f should clamp to %q, got %q", lat, want, cell)
		}
		ring := hexBoundary(cell)
		if ring == nil {
			t.Fatalf("lat %.1f produced no ring", lat)
		}
		for _, pt := range ring {
			if math.IsNaN(pt[0]) || math.IsNaN(pt[1]) || math.IsInf(pt[0], 0) || math.IsInf(pt[1], 0) {
				t.Fatalf("lat %.1f produced non-finite ring point %v", lat, pt)
			}
		}
	}
}

func TestHexCellAtStableAndDistinct(t *testing.T) {
	a := hexCellAt(51.0500, 3.7200, 9)
	b := hexCellAt(51.0500, 3.7200, 9)
	if a == "" || a != b {
		t.Fatalf("stable cell expected, got %q %q", a, b)
	}
	c := hexCellAt(51.2000, 3.7200, 9) // ~17 km away
	if c == a {
		t.Fatalf("distant point should differ, both %q", a)
	}
}

func TestHexBoundaryClosedRing(t *testing.T) {
	cell := hexCellAt(51.05, 3.72, 9)
	ring := hexBoundary(cell)
	if len(ring) != 7 {
		t.Fatalf("expected 7 points (closed hex), got %d", len(ring))
	}
	if ring[0] != ring[6] {
		t.Fatalf("ring not closed: %v vs %v", ring[0], ring[6])
	}
	if hexBoundary("garbage") != nil {
		t.Fatalf("malformed cell should return nil")
	}
}
