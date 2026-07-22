package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// /api/config/client must expose the configured geo_filter box/polygon so
// the frontend can classify domestic vs foreign nodes directly from
// lat/lon, rather than relying on the `foreign` flag alone — that flag
// only reflects nodes whose ADVERT was classified at ingest time, so a
// node whose last-known GPS predates geo_filter being configured (or just
// hasn't re-advertised since) can be geographically outside the filter
// without ever being flagged. See ClientConfigResponse.GeoFilter doc.
func TestConfigClientExposesGeoFilter(t *testing.T) {
	srv, router := setupTestServer(t)
	latMin, latMax, lonMin, lonMax := 53.0, 59.0, 6.0, 15.0
	srv.cfg.GeoFilter = &GeoFilterConfig{
		LatMin: &latMin, LatMax: &latMax, LonMin: &lonMin, LonMax: &lonMax,
	}

	req := httptest.NewRequest("GET", "/api/config/client", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	gfRaw, present := body["geoFilter"]
	if !present {
		t.Fatal("expected geoFilter in /api/config/client response when configured")
	}
	gf, ok := gfRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("expected geoFilter to be a JSON object, got %T: %+v", gfRaw, gfRaw)
	}
	// Explicit float64 assertions (not a bare `!=` on interface{}) so a
	// shape change — e.g. a field going missing or becoming a string —
	// fails loudly here instead of just comparing unequal to the wrong type.
	wantFields := map[string]float64{"latMin": 53.0, "latMax": 59.0, "lonMin": 6.0, "lonMax": 15.0}
	for field, want := range wantFields {
		got, ok := gf[field].(float64)
		if !ok {
			t.Fatalf("geoFilter[%q] = %T(%v), want a float64", field, gf[field], gf[field])
		}
		if got != want {
			t.Errorf("geoFilter[%q] = %v, want %v", field, got, want)
		}
	}
}

// When no geo_filter is configured, the field must be omitted entirely
// (omitempty), not present-but-null — the frontend treats "field absent"
// as "no geo_filter configured, treat everything as domestic".
func TestConfigClientOmitsGeoFilterWhenUnconfigured(t *testing.T) {
	srv, router := setupTestServer(t)
	srv.cfg.GeoFilter = nil

	req := httptest.NewRequest("GET", "/api/config/client", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if _, present := body["geoFilter"]; present {
		t.Error("expected geoFilter to be omitted from /api/config/client when unconfigured")
	}
}

// TestGetGeoFilter_HomeAreaTakesPriority covers the new HomeArea linkage:
// when cfg.HomeArea names an existing area, its geometry is the effective
// geo_filter -- a single boundary shared with the areas system instead of
// a second copy that can drift out of sync (exactly what happened with
// Germany's box vs. Denmark's area earlier this session).
func TestGetGeoFilter_HomeAreaTakesPriority(t *testing.T) {
	srv, _ := setupTestServer(t)
	standaloneLat := 10.0
	srv.cfg.GeoFilter = &GeoFilterConfig{LatMin: &standaloneLat} // deliberately different from the area
	f := func(v float64) *float64 { return &v }
	srv.cfg.Areas = map[string]AreaEntry{
		"DK": {Label: "Danmark", LatMin: f(54.5), LatMax: f(57.8), LonMin: f(8.0), LonMax: f(15.25)},
	}
	srv.cfg.HomeArea = "DK"

	gf := srv.getGeoFilter()
	if gf == nil || gf.LatMin == nil || *gf.LatMin != 54.5 {
		t.Fatalf("getGeoFilter() = %+v, want DK area's LatMin=54.5 (HomeArea should win over the standalone GeoFilter)", gf)
	}
}

// TestGetGeoFilter_FallsBackWhenHomeAreaUnresolved confirms existing
// deployments (no HomeArea configured, or one naming a since-removed area)
// see no behavior change -- the standalone GeoFilter still applies.
func TestGetGeoFilter_FallsBackWhenHomeAreaUnresolved(t *testing.T) {
	srv, _ := setupTestServer(t)
	standaloneLat := 10.0
	srv.cfg.GeoFilter = &GeoFilterConfig{LatMin: &standaloneLat}

	t.Run("HomeArea unset", func(t *testing.T) {
		srv.cfg.HomeArea = ""
		gf := srv.getGeoFilter()
		if gf == nil || gf.LatMin == nil || *gf.LatMin != 10.0 {
			t.Errorf("getGeoFilter() = %+v, want the standalone GeoFilter (LatMin=10.0)", gf)
		}
	})

	t.Run("HomeArea names a nonexistent area", func(t *testing.T) {
		srv.cfg.HomeArea = "NOPE"
		gf := srv.getGeoFilter()
		if gf == nil || gf.LatMin == nil || *gf.LatMin != 10.0 {
			t.Errorf("getGeoFilter() = %+v, want the standalone GeoFilter (LatMin=10.0)", gf)
		}
	})
}
