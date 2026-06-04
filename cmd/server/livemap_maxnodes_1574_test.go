package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// Behavior test (#1574): /api/config/client must expose `liveMapMaxNodes`
// so the frontend can honor the operator-configured live-map node cap
// instead of the hardcoded 2000 in public/live.js. Default is 2000;
// operators tune via `liveMap.maxNodes` in config.json. Server clamps to
// [100, 20000] to defang misconfig.
func TestConfigClientExposesLiveMapMaxNodes(t *testing.T) {
	_, router := setupTestServer(t)
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
	v, present := body["liveMapMaxNodes"]
	if !present {
		t.Fatal("expected liveMapMaxNodes in /api/config/client response")
	}
	n, ok := v.(float64)
	if !ok {
		t.Fatalf("expected liveMapMaxNodes to be a number, got %T", v)
	}
	if int(n) != 2000 {
		t.Errorf("expected default liveMapMaxNodes=2000, got %d", int(n))
	}
}

// Server-side clamp: operator misconfig (negative, zero, absurdly large)
// must be coerced to safe bounds [100, 20000]. Default (unset) is 2000.
func TestLiveMapMaxNodesClamp(t *testing.T) {
	cases := []struct {
		name string
		set  int
		want int
	}{
		{"default-when-unset", 0, 2000},
		{"negative-clamps-to-default", -42, 2000},
		{"below-min-clamps-up", 50, 100},
		{"in-range-passthrough", 4300, 4300},
		{"above-max-clamps-down", 99999, 20000},
		{"exact-min", 100, 100},
		{"exact-max", 20000, 20000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.LiveMap.MaxNodes = tc.set
			got := cfg.LiveMapMaxNodes()
			if got != tc.want {
				t.Errorf("LiveMapMaxNodes() with set=%d: want %d, got %d",
					tc.set, tc.want, got)
			}
		})
	}
}
