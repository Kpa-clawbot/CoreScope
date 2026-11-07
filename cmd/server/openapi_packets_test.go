package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type packetQueryDoc struct {
	Name        string `json:"name"`
	In          string `json:"in"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Schema      struct {
		Type string `json:"type"`
	} `json:"schema"`
}

func packetQueryDocs(t *testing.T, router http.Handler) map[string]packetQueryDoc {
	t.Helper()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/spec", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/spec: status %d", w.Code)
	}
	var spec struct {
		Paths map[string]struct {
			Get struct {
				Parameters []packetQueryDoc `json:"parameters"`
			} `json:"get"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}
	params := make(map[string]packetQueryDoc)
	for _, param := range spec.Paths["/api/packets"].Get.Parameters {
		if _, duplicate := params[param.Name]; duplicate {
			t.Errorf("duplicate packet query parameter %q", param.Name)
		}
		params[param.Name] = param
	}
	return params
}

func TestOpenAPIPacketQueryParameters(t *testing.T) {
	_, router := setupTestServer(t)
	params := packetQueryDocs(t, router)
	want := map[string]string{
		"limit": "integer", "offset": "integer", "order": "string",
		"type": "integer", "route": "integer", "observer": "string",
		"hash": "string", "since": "string", "until": "string",
		"region": "string", "node": "string", "nodes": "string",
		"channel": "string", "area": "string", "expand": "string",
		"radio": "string", "groupByHash": "boolean", "excludeTx": "boolean",
	}
	for name, typ := range want {
		param, ok := params[name]
		if !ok {
			t.Errorf("GET /api/packets is missing supported query parameter %q", name)
			continue
		}
		if param.In != "query" || param.Required || param.Schema.Type != typ || param.Description == "" {
			t.Errorf("%s: want optional, described query parameter of type %s; got %+v", name, typ, param)
		}
	}
	for name := range params {
		if _, supported := want[name]; !supported {
			t.Errorf("GET /api/packets documents unsupported query parameter %q", name)
		}
	}
}

// Couple the served documentation to observable route behavior, including the
// mode switches that bypass otherwise-supported packet filters (#1670).
func TestOpenAPIPacketQueryBehavior(t *testing.T) {
	srv, router := setupTestServer(t)
	params := packetQueryDocs(t, router)
	get := func(t *testing.T, query url.Values) PacketResult {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/packets?"+query.Encode(), nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/packets?%s: status %d: %s", query.Encode(), w.Code, w.Body.String())
		}
		var result PacketResult
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	described := func(t *testing.T, name string, phrases ...string) {
		t.Helper()
		param, ok := params[name]
		if !ok {
			t.Errorf("working query parameter %q is missing from served spec", name)
		}
		for _, phrase := range phrases {
			if !strings.Contains(param.Description, phrase) {
				t.Errorf("%s description must include %q; got %q", name, phrase, param.Description)
			}
		}
	}
	assertHashes := func(t *testing.T, result PacketResult, want ...string) {
		t.Helper()
		got := make([]string, len(result.Packets))
		for i, packet := range result.Packets {
			got[i], _ = packet["hash"].(string)
		}
		if !slices.Equal(got, want) {
			t.Errorf("packet hashes = %v, want %v", got, want)
		}
	}
	const advert = "abc123def4567890"
	const message = "1234567890abcdef"
	const olderAdvert = "def456abc1230099"
	cutoff := time.Now().UTC().Add(-12 * time.Hour).Format(time.RFC3339)

	// Use the existing seeded nodes and transmissions, with one configured area.
	srv.store.config = &Config{Areas: map[string]AreaEntry{
		"TEST":  {Polygon: [][2]float64{{37.45, -122.05}, {37.55, -122.05}, {37.55, -121.95}, {37.45, -121.95}}},
		"OTHER": {Polygon: [][2]float64{{37.55, -122.15}, {37.65, -122.15}, {37.65, -122.05}, {37.55, -122.05}}},
	}}
	for _, tc := range []struct {
		name, value string
		want        []string
	}{
		{"hash", strings.ToUpper(advert), []string{advert}},
		{"hash", advert[:8], nil}, // Exact hash, not a prefix search.
		{"type", "5", []string{message}},
		{"route", "2", nil},
		{"observer", "missing, obs2", []string{advert}},
		{"region", "SFO", []string{advert}},
		{"node", "TestRepeater", []string{olderAdvert, advert}},
		{"nodes", "missing, TestRepeater", []string{olderAdvert, advert}},
		{"channel", "#test", []string{message}},
		{"area", "TEST", []string{olderAdvert, advert}},
		{"area", "OTHER", nil},
		{"since", cutoff, []string{advert}},
		{"until", cutoff, []string{olderAdvert, message}},
		{"order", "asc", []string{advert, message, olderAdvert}},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			result := get(t, url.Values{tc.name: {tc.value}})
			assertHashes(t, result, tc.want...)
			described(t, tc.name)
		})
	}

	t.Run("defaults and pagination", func(t *testing.T) {
		result := get(t, nil)
		assertHashes(t, result, olderAdvert, message, advert)
		described(t, "limit", fmt.Sprintf("Defaults to %d", result.Limit))
		described(t, "offset", fmt.Sprintf("Defaults to %d", result.Offset))
		described(t, "order", "Defaults to desc", "ingest ID")
		described(t, "groupByHash", "Defaults to false")
		if _, grouped := result.Packets[0]["count"]; grouped {
			t.Error("default packet list unexpectedly uses grouped summaries")
		}
		page := get(t, url.Values{"limit": {"1"}, "offset": {"1"}})
		assertHashes(t, page, message)
		if page.Limit != 1 || page.Offset != 1 || page.Total != result.Total {
			t.Errorf("unexpected pagination metadata: %+v", page)
		}
		for _, value := range []string{"0", "-1", "invalid"} {
			if got := get(t, url.Values{"limit": {value}}).Limit; got != result.Limit {
				t.Errorf("limit=%s: got %d, want default %d", value, got, result.Limit)
			}
		}
		described(t, "limit", "non-positive", "invalid", "listLimits.packetsMax", strconv.Itoa(srv.cfg.ListLimits.PacketsMax))
		originalMax := srv.cfg.ListLimits.PacketsMax
		t.Cleanup(func() { srv.cfg.ListLimits.PacketsMax = originalMax })
		srv.cfg.ListLimits.PacketsMax = 2
		capped := get(t, url.Values{"limit": {"99999"}})
		if capped.Limit != 2 || len(capped.Packets) != 2 {
			t.Errorf("configured cap not applied: %+v", capped)
		}
	})

	t.Run("grouping and expansion", func(t *testing.T) {
		for _, expand := range []string{"", "observations", "other"} {
			result := get(t, url.Values{"hash": {advert}, "expand": {expand}})
			assertHashes(t, result, advert)
			_, expanded := result.Packets[0]["observations"]
			if expanded != (expand == "observations") {
				t.Errorf("expand=%q: observations present = %v", expand, expanded)
			}
		}
		described(t, "expand", "observations", "in-memory", "ungrouped")
		grouped := get(t, url.Values{"hash": {advert}, "groupByHash": {"true"}, "order": {"asc"}, "expand": {"observations"}})
		assertHashes(t, grouped, advert)
		if grouped.Packets[0]["count"] != float64(2) || grouped.Packets[0]["observations"] != nil {
			t.Errorf("unexpected grouped summary: %+v", grouped.Packets[0])
		}
		described(t, "groupByHash", "latest observation", "order", "expand")
		for _, order := range []string{"asc", "desc"} {
			grouped := get(t, url.Values{"groupByHash": {"true"}, "order": {order}})
			if grouped.Packets[0]["hash"] != advert {
				t.Errorf("grouped order=%s: latest observation must be first", order)
			}
		}
	})

	t.Run("time bounds depend on query mode", func(t *testing.T) {
		boundary := get(t, url.Values{"hash": {advert}}).Packets[0]["first_seen"].(string)
		store := srv.store
		t.Cleanup(func() { srv.store = store })
		for _, tc := range []struct {
			mode, param string
			want        []string
		}{
			{"memory", "since", nil},
			{"memory", "until", []string{olderAdvert, message}},
			{"SQL", "since", []string{advert}},
			{"SQL", "until", []string{olderAdvert, message, advert}},
			{"nodes", "since", []string{advert}},
			{"nodes", "until", []string{olderAdvert, advert}},
		} {
			t.Run(tc.mode+"/"+tc.param, func(t *testing.T) {
				srv.store = store
				query := url.Values{tc.param: {boundary}}
				if tc.mode == "SQL" {
					srv.store = nil
				} else if tc.mode == "nodes" {
					query.Set("nodes", "TestRepeater")
				}
				assertHashes(t, get(t, query), tc.want...)
			})
		}
		described(t, "since", "first_seen > since", "observations >= since", "first_seen >= since")
		described(t, "until", "first_seen < until", "observations <= until", "first_seen <= until")
	})

	t.Run("multi-node mode", func(t *testing.T) {
		result := get(t, url.Values{"nodes": {"TestRepeater"}, "hash": {"missing"}, "type": {"5"}, "groupByHash": {"true"}})
		assertHashes(t, result, olderAdvert, advert)
		described(t, "nodes", fmt.Sprintf("%d non-empty", maxMultiNodePubkeys), "only limit, offset, order, since and until")
		tooMany := url.Values{"nodes": {strings.Repeat("missing,", maxMultiNodePubkeys) + "missing"}}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/packets?"+tooMany.Encode(), nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("over-cap nodes list: status %d, want 400", w.Code)
		}
	})

	t.Run("SQL fallback limitations", func(t *testing.T) {
		assertHashes(t, get(t, url.Values{"hash": {advert}, "area": {"OTHER"}}))
		store := srv.store
		t.Cleanup(func() { srv.store = store })
		srv.store = nil
		result := get(t, url.Values{"hash": {strings.ToUpper(advert)}, "area": {"OTHER"}, "expand": {"observations"}})
		assertHashes(t, result, advert)
		if result.Packets[0]["observations"] != nil {
			t.Error("SQL result unexpectedly expanded observations")
		}
		described(t, "area", "SQL", "ignored")
		described(t, "expand", "SQL", "ignored")
	})
}
