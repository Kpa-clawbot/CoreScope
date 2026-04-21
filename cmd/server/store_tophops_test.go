package main

import (
	"testing"
)

func f64(v float64) *float64 { return &v }

func TestDedupeTopHopsByPair(t *testing.T) {
	hops := []distHopRecord{
		{FromPk: "AAA", ToPk: "BBB", FromName: "A", ToName: "B", Dist: 100, Type: "R↔R", SNR: f64(5.0), Hash: "h1", Timestamp: "t1"},
		{FromPk: "AAA", ToPk: "BBB", FromName: "A", ToName: "B", Dist: 90, Type: "R↔R", SNR: f64(8.0), Hash: "h2", Timestamp: "t2"},
		{FromPk: "BBB", ToPk: "AAA", FromName: "B", ToName: "A", Dist: 80, Type: "R↔R", SNR: f64(3.0), Hash: "h3", Timestamp: "t3"},
		{FromPk: "AAA", ToPk: "BBB", FromName: "A", ToName: "B", Dist: 70, Type: "R↔R", SNR: f64(6.0), Hash: "h4", Timestamp: "t4"},
		{FromPk: "AAA", ToPk: "BBB", FromName: "A", ToName: "B", Dist: 60, Type: "R↔R", SNR: f64(4.0), Hash: "h5", Timestamp: "t5"},
		{FromPk: "CCC", ToPk: "DDD", FromName: "C", ToName: "D", Dist: 50, Type: "C↔R", SNR: f64(7.0), Hash: "h6", Timestamp: "t6"},
	}

	result := dedupeHopsByPair(hops, 20)

	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}

	// First entry: A↔B pair, max distance = 100, obsCount = 5
	ab := result[0]
	if ab["dist"].(float64) != 100 {
		t.Errorf("expected dist 100, got %v", ab["dist"])
	}
	if ab["obsCount"].(int) != 5 {
		t.Errorf("expected obsCount 5, got %v", ab["obsCount"])
	}
	if ab["hash"].(string) != "h1" {
		t.Errorf("expected hash h1 (from max-dist record), got %v", ab["hash"])
	}
	if ab["bestSnr"].(float64) != 8.0 {
		t.Errorf("expected bestSnr 8.0, got %v", ab["bestSnr"])
	}
	// medianSnr of [3,4,5,6,8] = 5.0
	if ab["medianSnr"].(float64) != 5.0 {
		t.Errorf("expected medianSnr 5.0, got %v", ab["medianSnr"])
	}

	// Second entry: C↔D pair
	cd := result[1]
	if cd["dist"].(float64) != 50 {
		t.Errorf("expected dist 50, got %v", cd["dist"])
	}
	if cd["obsCount"].(int) != 1 {
		t.Errorf("expected obsCount 1, got %v", cd["obsCount"])
	}
}

func TestDedupeTopHopsReversePairMerges(t *testing.T) {
	hops := []distHopRecord{
		{FromPk: "BBB", ToPk: "AAA", FromName: "B", ToName: "A", Dist: 50, Type: "R↔R", Hash: "h1"},
		{FromPk: "AAA", ToPk: "BBB", FromName: "A", ToName: "B", Dist: 80, Type: "R↔R", Hash: "h2"},
	}
	result := dedupeHopsByPair(hops, 20)
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
	if result[0]["obsCount"].(int) != 2 {
		t.Errorf("expected obsCount 2, got %v", result[0]["obsCount"])
	}
	if result[0]["dist"].(float64) != 80 {
		t.Errorf("expected dist 80, got %v", result[0]["dist"])
	}
}

func TestDedupeTopHopsNilSNR(t *testing.T) {
	hops := []distHopRecord{
		{FromPk: "AAA", ToPk: "BBB", FromName: "A", ToName: "B", Dist: 100, Type: "R↔R", SNR: nil, Hash: "h1"},
		{FromPk: "AAA", ToPk: "BBB", FromName: "A", ToName: "B", Dist: 90, Type: "R↔R", SNR: nil, Hash: "h2"},
	}
	result := dedupeHopsByPair(hops, 20)
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
	if result[0]["bestSnr"] != nil {
		t.Errorf("expected bestSnr nil, got %v", result[0]["bestSnr"])
	}
	if result[0]["medianSnr"] != nil {
		t.Errorf("expected medianSnr nil, got %v", result[0]["medianSnr"])
	}
}

func TestDedupeTopHopsLimit(t *testing.T) {
	// Generate 25 unique pairs, verify limit=20 caps output
	hops := make([]distHopRecord, 25)
	for i := range hops {
		hops[i] = distHopRecord{
			FromPk: "A", ToPk: string(rune('a' + i)),
			Dist: float64(i), Type: "R↔R", Hash: "h",
		}
	}
	result := dedupeHopsByPair(hops, 20)
	if len(result) != 20 {
		t.Errorf("expected 20 entries, got %d", len(result))
	}
}

func TestDedupeTopHopsEvenMedian(t *testing.T) {
	// Even count: median = avg of two middle values
	hops := []distHopRecord{
		{FromPk: "A", ToPk: "B", Dist: 10, Type: "R↔R", SNR: f64(2.0), Hash: "h1"},
		{FromPk: "A", ToPk: "B", Dist: 20, Type: "R↔R", SNR: f64(4.0), Hash: "h2"},
		{FromPk: "A", ToPk: "B", Dist: 30, Type: "R↔R", SNR: f64(6.0), Hash: "h3"},
		{FromPk: "A", ToPk: "B", Dist: 40, Type: "R↔R", SNR: f64(8.0), Hash: "h4"},
	}
	result := dedupeHopsByPair(hops, 20)
	// sorted SNR: [2,4,6,8], median = (4+6)/2 = 5.0
	if result[0]["medianSnr"].(float64) != 5.0 {
		t.Errorf("expected medianSnr 5.0, got %v", result[0]["medianSnr"])
	}
}
