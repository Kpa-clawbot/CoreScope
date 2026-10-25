package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	rfPK1 = "aa00000000000000000000000000000000000000000000000000000000000001"
	rfPK2 = "aa00000000000000000000000000000000000000000000000000000000000002"
	rfPK3 = "aa00000000000000000000000000000000000000000000000000000000000003"
)

var rfNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func rfNode(pk string, daysAgo int, params string) string {
	return rfNodeAt(pk, rfNow.Add(-time.Duration(daysAgo)*24*time.Hour), params)
}

func rfNodeAt(pk string, at time.Time, params string) string {
	ts := at.Format("2006-01-02T15:04:05.000Z")
	p := "null"
	if params != "" {
		p = params
	}
	return fmt.Sprintf(`{"public_key":%q,"type":2,"adv_name":"x","last_advert":%q,"params":%s}`, pk, ts, p)
}

const rfEUNarrow = `{"freq":869.618,"bw":62.5,"cr":8,"sf":8}`

func TestParseRfNodes(t *testing.T) {
	body := "[" + strings.Join([]string{
		rfNode(strings.ToUpper(rfPK1), 1, rfEUNarrow),                          // kept, key lower-cased
		rfNode(rfPK2, 30, rfEUNarrow),                                          // older than max age
		rfNode(rfPK3, 1, ""),                                                   // no params
		rfNode("zz", 1, rfEUNarrow),                                            // not a 64-hex key
		rfNode(rfPK3[:63]+"4", 1, `{"freq":869.618,"bw":62.5,"cr":8,"sf":13}`), // SF out of range
		rfNode(rfPK3[:63]+"5", 1, `{"freq":869.618,"bw":63,"cr":8,"sf":8}`),    // not a LoRa bandwidth
		rfNode(rfPK3[:63]+"6", 1, `{"freq":5000,"bw":62.5,"cr":8,"sf":8}`),     // frequency out of range
		rfNode(rfPK3[:63]+"7", 1, `{"freq":869.618,"bw":62.5,"cr":4,"sf":8}`),  // CR out of range
		rfNode(rfPK3[:63]+"8", 2, `{"freq":433.65,"bw":125,"cr":5,"sf":12}`),   // kept
		rfNodeAt(rfPK3[:63]+"9", rfNow.Add(2*24*time.Hour), rfEUNarrow),        // dated in the future
		rfNodeAt(rfPK3[:63]+"a", rfNow.AddDate(70, 0, 0), rfEUNarrow),          // dated 2096
		rfNodeAt(rfPK3[:63]+"b", rfNow.Add(5*time.Minute), rfEUNarrow),         // kept, within clock skew
	}, ",") + "]"

	got, err := parseRfNodes(strings.NewReader(body), rfNow, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("kept %d nodes, want 3: %v", len(got), got)
	}
	p, ok := got[rfPK1]
	if !ok || p.Freq != 869.618 || p.SF != 8 || p.BW != 62.5 || p.CR != 8 {
		t.Errorf("rfPK1 = %+v, %v", p, ok)
	}
	if p.LastAdvert != "2026-10-13T12:00:00Z" {
		t.Errorf("last_advert = %q", p.LastAdvert)
	}
	if _, ok := got[rfPK3[:63]+"8"]; !ok {
		t.Error("433 MHz / BW125 / SF12 node should be kept")
	}
	if _, ok := got[rfPK3[:63]+"b"]; !ok {
		t.Error("advert 5 min ahead (clock skew) should be kept")
	}
}

func TestRfAdvertFresh(t *testing.T) {
	maxAge := 7 * 24 * time.Hour
	for _, c := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"now", rfNow, true},
		{"at max age", rfNow.Add(-maxAge), true},
		{"past max age", rfNow.Add(-maxAge - time.Second), false},
		{"at the skew tolerance", rfNow.Add(rfAdvertFutureSkew), true},
		{"past the skew tolerance", rfNow.Add(rfAdvertFutureSkew + time.Second), false},
		{"dated 2096", time.Date(2096, 1, 1, 0, 0, 0, 0, time.UTC), false},
	} {
		if got := rfAdvertFresh(c.at, rfNow, maxAge); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseRfNodesSkipsBadEntry(t *testing.T) {
	body := "[" + strings.Join([]string{
		rfNode(rfPK1, 1, `{"freq":869.618,"bw":62.5,"cr":8,"sf":"8"}`), // sf as a string
		`{"public_key":42,"params":{}}`,                                // key as a number
		rfNode(rfPK2, 1, rfEUNarrow),
	}, ",") + "]"
	got, err := parseRfNodes(strings.NewReader(body), rfNow, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("one bad entry failed the whole decode: %v", err)
	}
	if _, ok := got[rfPK2]; !ok || len(got) != 1 {
		t.Errorf("got %v, want only %s", got, rfPK2)
	}
}

func TestRfNextWait(t *testing.T) {
	refresh := 6 * time.Hour
	for _, c := range []struct {
		name     string
		hasData  bool
		failures int
		want     time.Duration
	}{
		{"data: regular refresh", true, 0, refresh},
		{"data after failures: still the regular refresh", true, 3, refresh},
		{"no data, first failure", false, 1, time.Minute},
		{"no data, second failure", false, 2, 2 * time.Minute},
		{"no data, fifth failure", false, 5, 16 * time.Minute},
		{"no data, capped", false, 6, rfRetryMax},
		{"no data, many failures stay capped", false, 60, rfRetryMax},
	} {
		if got := rfNextWait(c.hasData, c.failures, refresh); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if got := rfNextWait(false, 10, 10*time.Minute); got != 10*time.Minute {
		t.Errorf("retry wait must not exceed the refresh interval: got %v", got)
	}
}

func TestParseRfNodesRejectsMalformed(t *testing.T) {
	for name, body := range map[string]string{
		"not an array":  `{"nodes":[]}`,
		"truncated":     "[" + rfNode(rfPK1, 1, rfEUNarrow),
		"empty payload": ``,
	} {
		if _, err := parseRfNodes(strings.NewReader(body), rfNow, 7*24*time.Hour); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestParseRfPresets(t *testing.T) {
	body := `{"config":{"suggested_radio_settings":{"entries":[
		{"title":"EU/UK (Narrow)","frequency":"869.618","spreading_factor":"8","bandwidth":"62.5","coding_rate":"8"},
		{"title":"Switzerland","frequency":"869.618","spreading_factor":"8","bandwidth":"62.5","coding_rate":"8"},
		{"title":"USA/Canada (Recommended)","frequency":"910.525","spreading_factor":"7","bandwidth":"62.5","coding_rate":"5"},
		{"title":"Broken","frequency":"abc","spreading_factor":"8","bandwidth":"62.5","coding_rate":"8"},
		{"title":"","frequency":"869.618","spreading_factor":"9","bandwidth":"62.5","coding_rate":"8"}
	]}}}`
	got, err := parseRfPresets(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []RfPreset{
		{Title: "EU/UK (Narrow) / Switzerland", Freq: 869.618, SF: 8, BW: 62.5, CR: 8},
		{Title: "USA/Canada (Recommended)", Freq: 910.525, SF: 7, BW: 62.5, CR: 5},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("presets = %+v\nwant %+v", got, want)
	}
}

func TestCapReaderRefusesOversize(t *testing.T) {
	body := "[" + rfNode(rfPK1, 1, rfEUNarrow) + "]"
	_, err := parseRfNodes(newCapReader(strings.NewReader(body), int64(len(body)-1)), rfNow, 7*24*time.Hour)
	if !errors.Is(err, errRfTooLarge) {
		t.Errorf("err = %v, want errRfTooLarge", err)
	}
	if _, err := parseRfNodes(newCapReader(strings.NewReader(body), int64(len(body))), rfNow, 7*24*time.Hour); err != nil {
		t.Errorf("body exactly at the cap: %v", err)
	}
}

func rfUpstream(t *testing.T, nodes, presets *string, status *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if *status != 200 {
			w.WriteHeader(*status)
			return
		}
		if r.URL.Path == "/presets" {
			fmt.Fprint(w, *presets)
			return
		}
		fmt.Fprint(w, *nodes)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRfParamsCacheFailSoft(t *testing.T) {
	nodes := "[" + rfNode(rfPK1, 1, rfEUNarrow) + "]"
	presets := `{"config":{"suggested_radio_settings":{"entries":[{"title":"EU/UK (Narrow)","frequency":"869.618","spreading_factor":"8","bandwidth":"62.5","coding_rate":"8"}]}}}`
	status := 200
	up := rfUpstream(t, &nodes, &presets, &status)
	c := newRfParamsCache(&RfParamsConfig{NodesURL: up.URL + "/nodes", PresetsURL: up.URL + "/presets"})
	c.now = func() time.Time { return rfNow }

	if err := c.fetchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := c.load()
	if snap == nil || len(snap.Nodes) != 1 || len(snap.Presets) != 1 || !snap.FetchedAt.Equal(rfNow) {
		t.Fatalf("first fetch snapshot = %+v", snap)
	}

	// Upstream down: the previous snapshot stays.
	status = 503
	if err := c.fetchOnce(context.Background()); err == nil {
		t.Error("want an error on 503")
	}
	if c.load() != snap {
		t.Error("a failed fetch must keep the previous snapshot")
	}

	// Garbage body: same.
	status = 200
	nodes = `<html>maintenance</html>`
	if err := c.fetchOnce(context.Background()); err == nil {
		t.Error("want an error on a non-JSON body")
	}
	if c.load() != snap {
		t.Error("a parse failure must keep the previous snapshot")
	}

	// Presets failing alone keeps the old presets but takes the new nodes.
	nodes = "[" + rfNode(rfPK1, 1, rfEUNarrow) + "," + rfNode(rfPK2, 1, rfEUNarrow) + "]"
	presets = `not json`
	if err := c.fetchOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "presets") {
		t.Errorf("want a presets error, got %v", err)
	}
	if s := c.load(); len(s.Nodes) != 2 || len(s.Presets) != 1 {
		t.Errorf("after presets failure: %d nodes, %d presets", len(s.Nodes), len(s.Presets))
	}
}

func TestRfParamsConfigDefaults(t *testing.T) {
	var nilCfg *RfParamsConfig
	if nilCfg.enabled() {
		t.Error("nil config must be disabled")
	}
	if (&RfParamsConfig{}).enabled() {
		t.Error("empty nodesUrl must be disabled")
	}
	c := &RfParamsConfig{NodesURL: "x"}
	if c.refresh() != 6*time.Hour || c.maxAge() != 7*24*time.Hour {
		t.Errorf("defaults: refresh %v, maxAge %v", c.refresh(), c.maxAge())
	}
	c = &RfParamsConfig{NodesURL: "x", RefreshHours: 12, MaxAgeDays: 3}
	if c.refresh() != 12*time.Hour || c.maxAge() != 3*24*time.Hour {
		t.Errorf("configured: refresh %v, maxAge %v", c.refresh(), c.maxAge())
	}
}

func TestRfParamsRoute(t *testing.T) {
	srv, router := setupTestServer(t)
	get := func() RfParamsResponse {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/rf-params", nil))
		if w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		var resp RfParamsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Disabled: well-formed and empty.
	resp := get()
	if resp.Enabled || resp.FetchedAt != nil || resp.Nodes == nil || len(resp.Nodes) != 0 || resp.Presets == nil ||
		resp.Deviations == nil || len(resp.Deviations) != 0 {
		t.Errorf("disabled response = %+v", resp)
	}

	// Enabled: only nodes this instance knows, and only within max age.
	nodes, _ := srv.store.getCachedNodesAndPM()
	if len(nodes) == 0 {
		t.Fatal("test server has no nodes")
	}
	known := strings.ToLower(nodes[0].PublicKey)
	c := newRfParamsCache(&RfParamsConfig{NodesURL: "http://unused", MaxAgeDays: 7})
	c.ptr.Store(&rfParamsSnapshot{
		FetchedAt: time.Now().UTC(),
		Source:    "http://unused",
		Nodes: map[string]RfParams{
			known: {Freq: 869.618, SF: 8, BW: 62.5, CR: 8, LastAdvert: time.Now().UTC().Format(time.RFC3339)},
			rfPK3: {Freq: 869.618, SF: 8, BW: 62.5, CR: 8, LastAdvert: time.Now().UTC().Format(time.RFC3339)},
		},
		Presets: []RfPreset{{Title: "EU/UK (Narrow)", Freq: 869.618, SF: 8, BW: 62.5, CR: 8}},
	})
	srv.rfParams = c
	resp = get()
	if !resp.Enabled || resp.FetchedAt == nil || len(resp.Presets) != 1 {
		t.Errorf("enabled response = %+v", resp)
	}
	if _, ok := resp.Nodes[known]; !ok || len(resp.Nodes) != 1 {
		t.Errorf("nodes = %v, want only the known node %s", resp.Nodes, known)
	}

	// A snapshot entry that aged past maxAgeDays since the fetch is dropped at serve time.
	snap := c.load()
	old := snap.Nodes[known]
	old.LastAdvert = time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	snap.Nodes[known] = old
	if resp = get(); len(resp.Nodes) != 0 {
		t.Errorf("aged entry still served: %v", resp.Nodes)
	}

	// An entry dated in the future is not served as a fresh advert.
	future := snap.Nodes[known]
	future.LastAdvert = time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	snap.Nodes[known] = future
	if resp = get(); len(resp.Nodes) != 0 {
		t.Errorf("future-dated entry served: %v", resp.Nodes)
	}
}
