package main

import (
	"testing"
	"time"
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
	handleMessage(store, "test", source, msg, nil, nil, &Config{GeoFilter: gf})

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
	handleMessage(store, "test", source, msg, nil, nil, cfg)

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
	handleMessage(store, "test", source, msg, nil, nil, &Config{GeoFilter: gf})

	var foreign int
	err := store.db.QueryRow("SELECT foreign_advert FROM nodes").Scan(&foreign)
	if err != nil {
		t.Fatalf("query foreign_advert: %v", err)
	}
	if foreign != 0 {
		t.Errorf("foreign_advert=%d, want 0 (in-region node)", foreign)
	}
}

func TestUpsertNodeKeepsArchivedForeignAdvert(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -30).Format(time.RFC3339)
	returned := now.Add(-time.Hour).Format(time.RFC3339)
	current := now.Format(time.RFC3339)
	for _, key := range []string{"aa01", "aa02", "aa03"} {
		if err := s.UpsertNode(key, "Old", "sensor", nil, nil, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MarkNodeForeign("aa01"); err != nil {
		t.Fatal(err)
	}
	if moved, err := s.MoveStaleNodes(7); err != nil || moved != 3 {
		t.Fatalf("archive moved %d, want 3: %v", moved, err)
	}
	if _, err := s.db.Exec("UPDATE inactive_nodes SET foreign_advert = NULL WHERE public_key = 'aa03'"); err != nil {
		t.Fatal(err)
	}
	// aa04 stays active with its own flag and has no archived history.
	if err := s.UpsertNode("aa04", "Active", "repeater", nil, nil, returned); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeForeign("aa04"); err != nil {
		t.Fatal(err)
	}
	lat, lon := 1.0, 2.0
	for _, tc := range []struct {
		key  string
		want int
	}{{"aa01", 1}, {"aa02", 0}, {"aa03", 0}, {"aa04", 1}, {"aa05", 0}} {
		t.Run(tc.key, func(t *testing.T) {
			if err := s.UpsertNode(tc.key, "Current", "repeater", &lat, &lon, returned); err != nil {
				t.Fatal(err)
			}
			var flag int
			if err := s.db.QueryRow("SELECT foreign_advert FROM nodes WHERE public_key = ?", tc.key).Scan(&flag); err != nil {
				t.Fatal(err)
			}
			if flag != tc.want {
				t.Errorf("first ordinary advert: flag = %d, want %d", flag, tc.want)
			}
			if err := s.IncrementAdvertCount(tc.key); err != nil {
				t.Fatal(err)
			}
			if err := s.UpsertNode(tc.key, "Updated", "companion", nil, nil, current); err != nil {
				t.Fatal(err)
			}
			var name, role, first, last string
			var gotLat, gotLon float64
			var count int
			if err := s.db.QueryRow(`SELECT foreign_advert, name, role, lat, lon, first_seen, last_seen, advert_count
				FROM nodes WHERE public_key = ?`, tc.key).Scan(&flag, &name, &role, &gotLat, &gotLon, &first, &last, &count); err != nil {
				t.Fatal(err)
			}
			if flag != tc.want || name != "Updated" || role != "companion" || gotLat != lat || gotLon != lon || first != returned || last != current || count != 1 {
				t.Errorf("next ordinary advert: flag=%d name=%q role=%q coords=(%v,%v) first=%q last=%q count=%d", flag, name, role, gotLat, gotLon, first, last, count)
			}
		})
	}
}

func TestPreviouslyReturnedForeignNode(t *testing.T) {
	path := tempDBPath(t)
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	// An older ingestor already reinserted these keys with the default flag.
	// One is still active; startup retention will rearchive the other first.
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -30).Format(time.RFC3339)
	current := now.Format(time.RFC3339)
	_, err = s.db.Exec(`
		INSERT INTO nodes (public_key, last_seen) VALUES ('bb01', ?), ('bb02', ?);
		INSERT INTO inactive_nodes (public_key, foreign_advert) VALUES ('bb01', 1), ('bb02', 1)`, current, old)
	if closeErr := s.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if moved, err := s.MoveStaleNodes(7); err != nil || moved != 1 {
		t.Fatalf("startup retention moved %d, want 1: %v", moved, err)
	}
	var flag int
	if err := s.db.QueryRow("SELECT foreign_advert FROM inactive_nodes WHERE public_key = 'bb02'").Scan(&flag); err != nil {
		t.Fatal(err)
	}
	if flag != 1 {
		t.Errorf("startup retention overwrote the archived flag with %d", flag)
	}
	for _, key := range []string{"bb01", "bb02"} {
		if err := s.UpsertNode(key, "Returned", "repeater", nil, nil, current); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow("SELECT foreign_advert FROM nodes WHERE public_key = ?", key).Scan(&flag); err != nil {
			t.Fatal(err)
		}
		if flag != 1 {
			t.Errorf("%s ordinary advert: flag = %d, want 1", key, flag)
		}
	}
}
