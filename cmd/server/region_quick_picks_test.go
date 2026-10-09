package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

// Region quick picks: named groups of region (IATA) codes offered as one-tap
// choices in the region filter, configured in config.json. One deployment
// injected these with an nginx sub_filter and a script that wrapped
// RegionFilter; this makes them a configured feature.

func TestNormalizedRegionQuickPicks_CleansConfig(t *testing.T) {
	var cfg Config
	raw := `{"regionQuickPicks": [
		{"name": " California ", "description": "California observers", "regions": ["sfo", " SJC", "SFO", "", "lax"]},
		{"name": "", "regions": ["BRU"]},
		{"name": "Empty", "regions": []},
		{"name": "Monterey", "regions": ["mry"]}
	]}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	got := cfg.NormalizedRegionQuickPicks()
	want := []RegionQuickPick{
		{Name: "California", Description: "California observers", Regions: []string{"SFO", "SJC", "LAX"}},
		{Name: "Monterey", Regions: []string{"MRY"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestNormalizedRegionQuickPicks_CapsCounts(t *testing.T) {
	var cfg Config
	for i := 0; i < maxRegionQuickPicks+5; i++ {
		codes := make([]string, maxRegionQuickPickRegions+10)
		for j := range codes {
			codes[j] = fmt.Sprintf("C%03d", j)
		}
		cfg.RegionQuickPicks = append(cfg.RegionQuickPicks, RegionQuickPick{Name: fmt.Sprintf("Pick %d", i), Regions: codes})
	}
	got := cfg.NormalizedRegionQuickPicks()
	if len(got) != maxRegionQuickPicks {
		t.Fatalf("got %d picks, want the cap of %d", len(got), maxRegionQuickPicks)
	}
	if len(got[0].Regions) != maxRegionQuickPickRegions {
		t.Fatalf("got %d regions in a pick, want the cap of %d", len(got[0].Regions), maxRegionQuickPickRegions)
	}
}

func TestNormalizedRegionQuickPicks_NilConfigIsEmpty(t *testing.T) {
	if got := (*Config)(nil).NormalizedRegionQuickPicks(); got == nil || len(got) != 0 {
		t.Fatalf("got %#v, want an empty non-nil slice", got)
	}
}

func TestHandleConfigRegionQuickPicks(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	cfg := &Config{Port: 3000, RegionQuickPicks: []RegionQuickPick{{Name: "California", Regions: []string{"sfo", "sjc"}}}}
	srv := NewServer(db, cfg, NewHub())
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/config/region-quick-picks", nil))
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	body := strings.TrimSpace(w.Body.String())
	if body != `{"quickPicks":[{"name":"California","regions":["SFO","SJC"]}]}` {
		t.Fatalf("body = %s", body)
	}

	srv.cfg = &Config{Port: 3000}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/config/region-quick-picks", nil))
	if got := strings.TrimSpace(w.Body.String()); got != `{"quickPicks":[]}` {
		t.Fatalf("without quick picks, body = %s, want an empty list", got)
	}
}
