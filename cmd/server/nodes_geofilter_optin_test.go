package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// Regression test: geo_filter's node-list-declutter behavior (#730) is
// preserved by default for every deployment that already had geo_filter
// configured — GeoFilterExemptNodeList (default false) only lets a NEW
// adopter of geo_filter (using it purely for foreign_advert classification/
// analytics) opt OUT of also hiding out-of-polygon nodes from
// GET /api/nodes (and therefore the live map, which lists straight off
// this endpoint). Per-request ?geoFilter=0/1 overrides either default for
// a single call.
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
	get := func(qs string) map[string]bool {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/nodes?limit=50"+qs, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		return names(w)
	}

	t.Run("default request preserves the long-standing #730 filtering (deployments predating this field see no change)", func(t *testing.T) {
		got := get("")
		if !got["InsideNode"] {
			t.Error("expected InsideNode (within polygon) to be present")
		}
		if !got["OutsideTagged"] {
			t.Error("expected OutsideTagged (foreign_advert=1) to be present even though it's outside the polygon — #730")
		}
		if got["OutsideUntagged"] {
			t.Error("expected OutsideUntagged (outside polygon, not foreign-tagged) to be excluded by default, matching the pre-existing #730 behavior")
		}
	})

	t.Run("geoFilter=0 forces the filter off for a single request", func(t *testing.T) {
		got := get("&geoFilter=0")
		if !got["OutsideUntagged"] {
			t.Error("expected OutsideUntagged to be present when explicitly overriding with geoFilter=0")
		}
	})

	t.Run("geoFilter=1 is a no-op restating the default (still excludes untagged, keeps foreign-tagged)", func(t *testing.T) {
		got := get("&geoFilter=1")
		if !got["InsideNode"] || !got["OutsideTagged"] {
			t.Error("expected InsideNode and OutsideTagged to remain present")
		}
		if got["OutsideUntagged"] {
			t.Error("expected OutsideUntagged to still be excluded with geoFilter=1")
		}
	})

	t.Run("geoFilter=true/false are accepted synonyms for 1/0", func(t *testing.T) {
		if !get("&geoFilter=false")["OutsideUntagged"] {
			t.Error("expected OutsideUntagged to be present with geoFilter=false")
		}
		if get("&geoFilter=true")["OutsideUntagged"] {
			t.Error("expected OutsideUntagged to be excluded with geoFilter=true")
		}
	})

	t.Run("an unrecognized geoFilter value falls back to the deployment default instead of silently disabling", func(t *testing.T) {
		got := get("&geoFilter=yes")
		if got["OutsideUntagged"] {
			t.Error("expected geoFilter=yes (not a recognized value) to fall back to the default (filtered), not silently disable filtering")
		}
	})

	t.Run("GeoFilterExemptNodeList=true makes the default request return everything, without needing ?geoFilter=0", func(t *testing.T) {
		srv.cfg.GeoFilterExemptNodeList = true
		defer func() { srv.cfg.GeoFilterExemptNodeList = false }()

		got := get("")
		if !got["InsideNode"] || !got["OutsideTagged"] || !got["OutsideUntagged"] {
			t.Errorf("expected every node present when GeoFilterExemptNodeList=true, got %v", got)
		}
	})

	t.Run("geoFilter=1 still overrides GeoFilterExemptNodeList=true for a single request", func(t *testing.T) {
		srv.cfg.GeoFilterExemptNodeList = true
		defer func() { srv.cfg.GeoFilterExemptNodeList = false }()

		got := get("&geoFilter=1")
		if got["OutsideUntagged"] {
			t.Error("expected OutsideUntagged to be excluded when geoFilter=1 overrides an exempt deployment")
		}
	})
}

func floatPtr(f float64) *float64 { return &f }
