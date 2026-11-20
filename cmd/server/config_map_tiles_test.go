package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The server must hand map.tiles to the browser untouched: the browser
// validates it, so a server-side refactor that drops or rewrites a field would
// silently break operators' self-hosted tile servers.

const mapTilesFullJSON = `{
  "darkDefault": "custom-ours-vec",
  "lightDefault": "custom-ours",
  "providers": {
    "carto": {"enabled": true, "key": "k", "domain": "corp"},
    "eox": {"enabled": false},
    "osmfr": {"enabled": true},
    "openfreemap": {"enabled": false},
    "hillshade": {"enabled": true, "url": "https://mirror.example.org/terrarium/{z}/{x}/{y}.png"}
  },
  "custom": [
    {"id": "ours", "label": "Our tile server", "type": "light", "category": "street",
     "format": "raster", "url": "https://{s}.tiles.example.org/{z}/{x}/{y}.png",
     "attribution": "(c) OpenStreetMap contributors", "maxZoom": 19, "maxNativeZoom": 18,
     "subdomains": "abc", "overlay": false},
    {"id": "ours-vec", "label": "Our vector style", "type": "dark", "category": "topo",
     "format": "vector", "url": "/styles/dark.json",
     "attribution": "(c) Example", "maxZoom": 22, "maxNativeZoom": 14,
     "subdomains": "", "overlay": true},
    {"id": "<b>x</b>", "label": "<img src=x onerror=alert(1)>", "type": "bogus", "category": "??",
     "format": "raster", "url": "javascript:alert(1)",
     "attribution": "<script>alert(1)</script><a href=\"javascript:1\">a</a>",
     "maxZoom": -5, "maxNativeZoom": 1e9, "subdomains": "}{", "overlay": "yes", "extra": {"n": null}}
  ]
}`

// clientConfigFor loads a config the way the server does (LoadConfig, which
// runs migrateDeprecatedConfig) and fetches /api/config/client.
func clientConfigFor(t *testing.T, configJSON string) map[string]interface{} {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	srv, router := setupTestServer(t)
	srv.cfg = cfg
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/config/client", nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func mustDecodeObject(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return m
}

func tilesOf(t *testing.T, resp map[string]interface{}) map[string]interface{} {
	t.Helper()
	m, ok := resp["map"].(map[string]interface{})
	if !ok {
		t.Fatalf("response map is %T, want object", resp["map"])
	}
	tiles, ok := m["tiles"].(map[string]interface{})
	if !ok {
		t.Fatalf("response map.tiles is %T, want object", m["tiles"])
	}
	return tiles
}

func TestClientConfigMapTilesPassThrough(t *testing.T) {
	cases := []struct {
		name  string
		tiles string
	}{
		{"full new shape with hostile entry", mapTilesFullJSON},
		{"empty custom list", `{"custom": []}`},
		{"providers only", `{"providers": {"eox": {"enabled": false}, "hillshade": {"enabled": false}}}`},
		{"unknown future keys", `{"futureKey": {"a": [1, 2, 3]}, "custom": [{"id": "z", "newField": true}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := mustDecodeObject(t, tc.tiles)
			resp := clientConfigFor(t, `{"map": {"center": [1, 2], "zoom": 7, "tiles": `+tc.tiles+`}}`)
			got := tilesOf(t, resp)
			if !reflect.DeepEqual(got, want) {
				g, _ := json.MarshalIndent(got, "", " ")
				w, _ := json.MarshalIndent(want, "", " ")
				t.Fatalf("map.tiles changed.\n got: %s\nwant: %s", g, w)
			}
			m := resp["map"].(map[string]interface{})
			if m["zoom"] != float64(7) {
				t.Errorf("map.zoom = %v, want 7", m["zoom"])
			}
		})
	}
}

func TestClientConfigLegacyTilesMigrateWithoutClobberingCustom(t *testing.T) {
	custom := `[{"id": "ours", "label": "Ours", "format": "raster", "url": "https://t.example.org/{z}/{x}/{y}.png"}]`
	cfgJSON := `{
	  "mapDarkTileProvider": "legacy-dark",
	  "tiles": {"lightDefault": "legacy-light", "custom": [{"id": "legacy"}], "legacyOnly": "x"},
	  "map": {"tiles": {"lightDefault": "new-light", "custom": ` + custom + `}}
	}`
	tiles := tilesOf(t, clientConfigFor(t, cfgJSON))

	wantCustom := mustDecodeObject(t, `{"c": `+custom+`}`)["c"]
	if !reflect.DeepEqual(tiles["custom"], wantCustom) {
		t.Errorf("map.tiles.custom clobbered by legacy tiles: %v", tiles["custom"])
	}
	if tiles["lightDefault"] != "new-light" {
		t.Errorf("explicit map.tiles.lightDefault overridden: %v", tiles["lightDefault"])
	}
	if tiles["darkDefault"] != "legacy-dark" {
		t.Errorf("mapDarkTileProvider not migrated to darkDefault: %v", tiles["darkDefault"])
	}
	if tiles["legacyOnly"] != "x" {
		t.Errorf("legacy tiles key not migrated: %v", tiles["legacyOnly"])
	}
}

func TestClientConfigLegacyTilesOnlyMigrate(t *testing.T) {
	tiles := tilesOf(t, clientConfigFor(t, `{"mapDarkTileProvider": "legacy-dark", "tiles": {"lightDefault": "legacy-light"}}`))
	if tiles["darkDefault"] != "legacy-dark" || tiles["lightDefault"] != "legacy-light" {
		t.Errorf("legacy keys not migrated into map.tiles: %v", tiles)
	}
}

// Documents today's behaviour: with no map block the server still answers with
// a map object whose tiles is an empty object (migrateDeprecatedConfig).
func TestClientConfigAbsentMapStillUsable(t *testing.T) {
	resp := clientConfigFor(t, `{}`)
	tiles := tilesOf(t, resp)
	if len(tiles) != 0 {
		t.Errorf("absent map: tiles = %v, want empty object", tiles)
	}
}
