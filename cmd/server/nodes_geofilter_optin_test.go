package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// Regression test: configuring geo_filter alone must not change what
// GET /api/nodes returns. Before this fix, setting geo_filter silently
// hid every node outside the polygon that hadn't yet been re-tagged
// foreign_advert=1 by the ingestor (which only happens on that node's
// next ADVERT) — including the live map, which lists straight off this
// endpoint. The filter is now opt-in via ?geoFilter=1.
func TestHandleNodes_GeoFilterExcludedByDefault(t *testing.T) {
	apiKey := "a-strong-api-key-for-testing"
	srv, router, _ := setupGeoFilterServer(t, apiKey)
	srv.setGeoFilter(&GeoFilterConfig{
		LatMin: floatPtr(53.0), LatMax: floatPtr(59.0),
		LonMin: floatPtr(6.0), LonMax: floatPtr(15.0),
	})

	mustExecDB(t, srv.db, `INSERT INTO nodes (public_key, name, lat, lon, foreign_advert) VALUES ('pk-inside', 'InsideNode', 55.7, 10.5, 0)`)
	mustExecDB(t, srv.db, `INSERT INTO nodes (public_key, name, lat, lon, foreign_advert) VALUES ('pk-outside-untagged', 'OutsideUntagged', 44.4, 26.1, 0)`)
	mustExecDB(t, srv.db, `INSERT INTO nodes (public_key, name, lat, lon, foreign_advert) VALUES ('pk-outside-tagged', 'OutsideTagged', 52.4, 10.8, 1)`)

	names := func(w *httptest.ResponseRecorder) map[string]bool {
		var body map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		nodes, ok := body["nodes"].([]interface{})
		if !ok {
			t.Fatal("expected nodes array")
		}
		out := make(map[string]bool, len(nodes))
		for _, n := range nodes {
			m := n.(map[string]interface{})
			out[m["name"].(string)] = true
		}
		return out
	}

	t.Run("default request returns every node regardless of geo_filter", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes?limit=50", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		got := names(w)
		for _, want := range []string{"InsideNode", "OutsideUntagged", "OutsideTagged"} {
			if !got[want] {
				t.Errorf("expected %s in default (unfiltered) response, got %v", want, got)
			}
		}
	})

	t.Run("geoFilter=1 excludes untagged out-of-polygon nodes but keeps foreign-tagged ones", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes?limit=50&geoFilter=1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		got := names(w)
		if !got["InsideNode"] {
			t.Error("expected InsideNode (within polygon) to be present")
		}
		if !got["OutsideTagged"] {
			t.Error("expected OutsideTagged (foreign_advert=1) to be present even though it's outside the polygon — #730")
		}
		if got["OutsideUntagged"] {
			t.Error("expected OutsideUntagged (outside polygon, not yet foreign-tagged) to be excluded when geoFilter=1")
		}
	})

	t.Run("GeoFilterAppliesToNodeList=true restores the pre-opt-in always-on behavior without needing ?geoFilter=1", func(t *testing.T) {
		srv.cfg.GeoFilterAppliesToNodeList = true
		defer func() { srv.cfg.GeoFilterAppliesToNodeList = false }()

		req := httptest.NewRequest("GET", "/api/nodes?limit=50", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		got := names(w)
		if !got["InsideNode"] {
			t.Error("expected InsideNode (within polygon) to be present")
		}
		if !got["OutsideTagged"] {
			t.Error("expected OutsideTagged (foreign_advert=1) to be present even though it's outside the polygon — #730")
		}
		if got["OutsideUntagged"] {
			t.Error("expected OutsideUntagged to be excluded by default when GeoFilterAppliesToNodeList=true, without needing ?geoFilter=1")
		}
	})
}

func floatPtr(f float64) *float64 { return &f }
