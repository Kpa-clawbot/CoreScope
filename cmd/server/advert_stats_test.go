package main

import (
	"testing"
	"time"
)

func advertTS(hoursAgo float64) string {
	return time.Now().UTC().Add(-time.Duration(hoursAgo * float64(time.Hour))).Format("2006-01-02T15:04:05.000Z")
}

// Flood adverts inside the window count; zero-hop (DIRECT) and out-of-window
// ones do not; duplicate hashes collapse; broken timestamps are skipped.
func TestCountFloodAdverts(t *testing.T) {
	now := time.Now()
	entries := []floodAdvertEntry{
		{ts: advertTS(1), rt: advertRouteTypeFlood, hash: "a1"},
		{ts: advertTS(2), rt: advertRouteTypeFlood, hash: "a1"}, // dup hash: one advert, two rows
		{ts: advertTS(3), rt: advertRouteTypeFlood, hash: "a2"},
		{ts: advertTS(4), rt: 0, hash: "a3"},                         // zero-hop (DIRECT): excluded
		{ts: advertTS(9 * 24), rt: advertRouteTypeFlood, hash: "a4"}, // outside 7d window
		{ts: "not-a-time", rt: advertRouteTypeFlood, hash: "a5"},     // unparseable: skipped
		{ts: advertTS(5), rt: -1, hash: "a6"},                        // route type absent: excluded
	}
	if got := countFloodAdverts(entries, now, 7*24); got != 2 {
		t.Fatalf("want 2 flood adverts in window, got %d", got)
	}
	// Hash-less entries dedup by timestamp instead of collapsing into one.
	hashless := []floodAdvertEntry{
		{ts: advertTS(1), rt: advertRouteTypeFlood},
		{ts: advertTS(2), rt: advertRouteTypeFlood},
	}
	if got := countFloodAdverts(hashless, now, 7*24); got != 2 {
		t.Fatalf("want 2 hash-less flood adverts, got %d", got)
	}
}
