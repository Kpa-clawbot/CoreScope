package main

import "testing"

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
