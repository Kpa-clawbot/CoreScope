package main

import (
	"testing"
)

// TestHandleMessageAdvertForeign_FlagModeStoresWithFlag asserts that when an
// ADVERT comes from a node whose GPS is OUTSIDE the configured geofilter,
// the ingestor (in default "flag" mode) stores the node and marks it foreign,
// instead of silently dropping it (#730).
func TestHandleMessageAdvertForeign_FlagModeStoresWithFlag(t *testing.T) {
	store, source := newTestContext(t)

	// Real ADVERT raw hex from existing TestHandleMessageAdvertGeoFiltered.
	// Decoder will produce a node with a known GPS — the test below just
	// asserts that with a tight geofilter that EXCLUDES that GPS, the node
	// is still stored AND tagged as foreign.
	rawHex := "120046D62DE27D4C5194D7821FC5A34A45565DCC2537B300B9AB6275255CEFB65D840CE5C169C94C9AED39E8BCB6CB6EB0335497A198B33A1A610CD3B03D8DCFC160900E5244280323EE0B44CACAB8F02B5B38B91CFA18BD067B0B5E63E94CFC85F758A8530B9240933402E0E6B8F84D5252322D52"

	latMin, latMax := -1.0, 1.0
	lonMin, lonMax := -1.0, 1.0
	gf := &GeoFilterConfig{
		LatMin: &latMin, LatMax: &latMax,
		LonMin: &lonMin, LonMax: &lonMax,
	}

	msg := &mockMessage{
		topic:   "meshcore/SJC/obs1/packets",
		payload: []byte(`{"raw":"` + rawHex + `"}`),
	}
	// Default mode (no ForeignAdverts.Mode set) MUST be "flag", per #730 design.
	handleMessage(store, "test", source, msg, nil, &Config{GeoFilter: gf})

	var nodeCount int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&nodeCount); err != nil {
		t.Fatal(err)
	}
	if nodeCount != 1 {
		t.Fatalf("nodes=%d, want 1 (foreign advert should be stored, not dropped, in flag mode)", nodeCount)
	}

	var foreign int
	if err := store.db.QueryRow("SELECT foreign_advert FROM nodes").Scan(&foreign); err != nil {
		t.Fatalf("foreign_advert column missing or unreadable: %v", err)
	}
	if foreign != 1 {
		t.Errorf("foreign_advert=%d, want 1", foreign)
	}
}

// TestHandleMessageAdvertForeign_DropModeStillDrops asserts the legacy
// drop-on-foreign behavior is preserved when ForeignAdverts.Mode = "drop".
func TestHandleMessageAdvertForeign_DropModeStillDrops(t *testing.T) {
	store, source := newTestContext(t)

	rawHex := "120046D62DE27D4C5194D7821FC5A34A45565DCC2537B300B9AB6275255CEFB65D840CE5C169C94C9AED39E8BCB6CB6EB0335497A198B33A1A610CD3B03D8DCFC160900E5244280323EE0B44CACAB8F02B5B38B91CFA18BD067B0B5E63E94CFC85F758A8530B9240933402E0E6B8F84D5252322D52"

	latMin, latMax := -1.0, 1.0
	lonMin, lonMax := -1.0, 1.0
	gf := &GeoFilterConfig{
		LatMin: &latMin, LatMax: &latMax,
		LonMin: &lonMin, LonMax: &lonMax,
	}

	msg := &mockMessage{
		topic:   "meshcore/SJC/obs1/packets",
		payload: []byte(`{"raw":"` + rawHex + `"}`),
	}
	cfg := &Config{
		GeoFilter:      gf,
		ForeignAdverts: &ForeignAdvertConfig{Mode: "drop"},
	}
	handleMessage(store, "test", source, msg, nil, cfg)

	var nodeCount int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&nodeCount); err != nil {
		t.Fatal(err)
	}
	if nodeCount != 0 {
		t.Errorf("nodes=%d, want 0 (drop mode preserves legacy silent-drop behavior)", nodeCount)
	}
}

// TestHandleMessageAdvertInRegion_NotFlaggedForeign asserts in-region
// adverts are NOT marked foreign.
func TestHandleMessageAdvertInRegion_NotFlaggedForeign(t *testing.T) {
	store, source := newTestContext(t)

	rawHex := "120046D62DE27D4C5194D7821FC5A34A45565DCC2537B300B9AB6275255CEFB65D840CE5C169C94C9AED39E8BCB6CB6EB0335497A198B33A1A610CD3B03D8DCFC160900E5244280323EE0B44CACAB8F02B5B38B91CFA18BD067B0B5E63E94CFC85F758A8530B9240933402E0E6B8F84D5252322D52"

	// Wide-open geofilter: every coord passes.
	latMin, latMax := -90.0, 90.0
	lonMin, lonMax := -180.0, 180.0
	gf := &GeoFilterConfig{
		LatMin: &latMin, LatMax: &latMax,
		LonMin: &lonMin, LonMax: &lonMax,
	}
	msg := &mockMessage{
		topic:   "meshcore/SJC/obs1/packets",
		payload: []byte(`{"raw":"` + rawHex + `"}`),
	}
	handleMessage(store, "test", source, msg, nil, &Config{GeoFilter: gf})

	var foreign int
	err := store.db.QueryRow("SELECT foreign_advert FROM nodes").Scan(&foreign)
	if err != nil {
		t.Fatalf("query foreign_advert: %v", err)
	}
	if foreign != 0 {
		t.Errorf("foreign_advert=%d, want 0 (in-region node)", foreign)
	}
}
