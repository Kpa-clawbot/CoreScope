# RF Params Enrichment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enrich node data with LoRa RF parameters from map.meshcore.io, shown as a sortable "LoRa" preset column in the nodes table and as an RF Parameters section in node detail.

**Architecture:** A new `RFParamsCache` struct in `rf_params_cache.go` fetches the 33 MB node list every 6 hours and the official preset definitions once at startup, storing both in memory. `handleNodes` and `handleNodeDetail` enrich their responses from the cache. A `/api/rf-presets` endpoint serves the preset map to the frontend. `nodes.js` fetches presets once, resolves labels in `resolveLoraLabel`, and renders a new "LoRa" column and detail section.

**Tech Stack:** Go (`sync`, `net/http`, `encoding/json`, `time`), Node.js JSDOM tests, existing `TableSort` utility

---

## File Map

| Action | File | Responsibility |
|---|---|---|
| Create | `cmd/server/rf_params_cache.go` | Cache struct, preset loading, node index fetch, Lookup |
| Create | `cmd/server/rf_params_cache_test.go` | Go unit tests for cache |
| Modify | `cmd/server/routes.go` | Add `rfCache` to Server, `handleRFPresets`, enrich `handleNodes` + `handleNodeDetail`, register route |
| Modify | `cmd/server/routes_test.go` | Tests for `/api/rf-presets` and `rf_params` enrichment |
| Modify | `cmd/server/main.go` | Start RF cache goroutine |
| Modify | `public/nodes.js` | `_rfPresets`, `resolveLoraLabel`, LoRa column, sort case, detail section |
| Create | `test-rf-params.js` | JS unit tests for `resolveLoraLabel` and null-last sort |
| Modify | `test-all.sh` | Register new JS test |

---

### Task 1: rf_params_cache.go — structs, preset loading

**Files:**
- Create: `cmd/server/rf_params_cache.go`
- Create: `cmd/server/rf_params_cache_test.go`

- [ ] **Step 1: Write failing tests for `buildPresetKey` and `loadPresets`**

Create `cmd/server/rf_params_cache_test.go`:

```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildPresetKey(t *testing.T) {
	cases := []struct {
		freq string
		sf   string
		bw   string
		cr   string
		want string
	}{
		{"869.618", "8", "62.5", "8", "869.618/8/62.5/8"},
		{"910.525", "7", "62.5", "5", "910.525/7/62.5/5"},
		{"915.800", "10", "250", "5", "915.800/10/250.0/5"},
	}
	for _, tc := range cases {
		got := buildPresetKey(tc.freq, tc.sf, tc.bw, tc.cr)
		if got != tc.want {
			t.Errorf("buildPresetKey(%q,%q,%q,%q) = %q, want %q", tc.freq, tc.sf, tc.bw, tc.cr, got, tc.want)
		}
	}
}

func TestLoadPresets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"config": map[string]interface{}{
				"suggested_radio_settings": map[string]interface{}{
					"entries": []map[string]interface{}{
						{"title": "EU/UK (Narrow)", "frequency": "869.618", "spreading_factor": "8", "bandwidth": "62.5", "coding_rate": "8"},
						{"title": "Switzerland", "frequency": "869.618", "spreading_factor": "8", "bandwidth": "62.5", "coding_rate": "8"},
						{"title": "USA/Canada (Recommended)", "frequency": "910.525", "spreading_factor": "7", "bandwidth": "62.5", "coding_rate": "5"},
					},
				},
			},
		})
	}))
	defer srv.Close()

	c := NewRFParamsCache()
	c.configURL = srv.URL
	c.loadPresets()

	presets := c.GetPresets()

	// Switzerland deduped into EU/UK (Narrow)
	label, ok := presets["869.618/8/62.5/8"]
	if !ok {
		t.Fatal("expected key 869.618/8/62.5/8 in presets")
	}
	if label != "EU/UK (Narrow) / Switzerland" {
		t.Errorf("got label %q, want %q", label, "EU/UK (Narrow) / Switzerland")
	}

	// Netherlands injected locally
	nl, ok := presets["869.618/7/62.5/5"]
	if !ok {
		t.Fatal("expected Netherlands (Narrow) key in presets")
	}
	if nl != "Netherlands (Narrow)" {
		t.Errorf("got %q, want Netherlands (Narrow)", nl)
	}

	// Regular preset present
	if presets["910.525/7/62.5/5"] != "USA/Canada (Recommended)" {
		t.Errorf("USA/Canada preset missing or wrong")
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```
cd cmd/server && go test -run "TestBuildPresetKey|TestLoadPresets" -v
```

Expected: `FAIL — buildPresetKey undefined`, `NewRFParamsCache undefined`

- [ ] **Step 3: Create `cmd/server/rf_params_cache.go` with structs, `buildPresetKey`, `loadPresets`, `GetPresets`**

```go
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	rfNodeURL   = "https://map.meshcore.io/api/v1/nodes"
	rfConfigURL = "https://api.meshcore.nz/api/v1/config"
	rfStaleDays = 7
	rfRefreshInterval = 6 * time.Hour
)

type RFParams struct {
	Freq       float64 `json:"freq"`
	SF         int     `json:"sf"`
	CR         int     `json:"cr"`
	BW         float64 `json:"bw"`
	LastAdvert string  `json:"last_advert"`
}

type RFParamsCache struct {
	mu        sync.RWMutex
	index     map[string]*RFParams // pubkey → params (all ages; freshness checked at Lookup)
	presets   map[string]string    // canonical key → label
	nodeURL   string
	configURL string
}

func NewRFParamsCache() *RFParamsCache {
	return &RFParamsCache{
		index:     make(map[string]*RFParams),
		presets:   make(map[string]string),
		nodeURL:   rfNodeURL,
		configURL: rfConfigURL,
	}
}

// buildPresetKey returns a canonical key from string fields as returned by
// the config API: "{freq}/{sf}/{bw}/{cr}". BW is normalised to one decimal
// place so "62.5" stays "62.5" and "250" becomes "250.0".
func buildPresetKey(freq, sf, bw, cr string) string {
	bwF, err := strconv.ParseFloat(bw, 64)
	if err != nil {
		return fmt.Sprintf("%s/%s/%s/%s", freq, sf, bw, cr)
	}
	return fmt.Sprintf("%s/%s/%.1f/%s", freq, sf, bwF, cr)
}

// loadPresets fetches api.meshcore.nz/api/v1/config and builds the preset map.
// Switzerland is deduplicated into EU/UK (Narrow) (they share params).
// Netherlands (Narrow) is injected as a local supplement.
func (c *RFParamsCache) loadPresets() {
	resp, err := http.Get(c.configURL) //nolint:noctx
	if err != nil {
		log.Printf("[rf-params] preset fetch error: %v", err)
		return
	}
	defer resp.Body.Close()

	var raw struct {
		Config struct {
			SuggestedRadioSettings struct {
				Entries []struct {
					Title           string `json:"title"`
					Frequency       string `json:"frequency"`
					SpreadingFactor string `json:"spreading_factor"`
					Bandwidth       string `json:"bandwidth"`
					CodingRate      string `json:"coding_rate"`
				} `json:"entries"`
			} `json:"suggested_radio_settings"`
		} `json:"config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		log.Printf("[rf-params] preset decode error: %v", err)
		return
	}

	presets := make(map[string]string)
	for _, e := range raw.Config.SuggestedRadioSettings.Entries {
		key := buildPresetKey(e.Frequency, e.SpreadingFactor, e.Bandwidth, e.CodingRate)
		if existing, ok := presets[key]; ok {
			// Merge duplicate-param entries into a combined label
			presets[key] = existing + " / " + e.Title
		} else {
			presets[key] = e.Title
		}
	}
	// Local supplement: Netherlands (Narrow) is not in the official preset list
	presets["869.618/7/62.5/5"] = "Netherlands (Narrow)"

	c.mu.Lock()
	c.presets = presets
	c.mu.Unlock()
	log.Printf("[rf-params] loaded %d presets", len(presets))
}

// GetPresets returns a copy of the preset map (key → label).
func (c *RFParamsCache) GetPresets() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]string, len(c.presets))
	for k, v := range c.presets {
		out[k] = v
	}
	return out
}

// Lookup returns fresh RFParams for pubkey, or nil if not found / stale.
func (c *RFParamsCache) Lookup(pubkey string) *RFParams {
	c.mu.RLock()
	p := c.index[strings.ToLower(pubkey)]
	c.mu.RUnlock()
	if p == nil {
		return nil
	}
	t, err := time.Parse("2006-01-02T15:04:05.000Z", p.LastAdvert)
	if err != nil {
		t2, err2 := time.Parse(time.RFC3339, p.LastAdvert)
		if err2 != nil {
			return nil
		}
		t = t2
	}
	if time.Since(t) > rfStaleDays*24*time.Hour {
		return nil
	}
	return p
}
```

- [ ] **Step 4: Run tests to confirm `TestBuildPresetKey` and `TestLoadPresets` pass**

```
cd cmd/server && go test -run "TestBuildPresetKey|TestLoadPresets" -v
```

Expected: both PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/server/rf_params_cache.go cmd/server/rf_params_cache_test.go
git commit -m "feat: RFParamsCache structs, preset loading (task 1)"
```

---

### Task 2: rf_params_cache.go — node index fetch, Lookup freshness, Start

**Files:**
- Modify: `cmd/server/rf_params_cache.go`
- Modify: `cmd/server/rf_params_cache_test.go`

- [ ] **Step 1: Write failing tests for `Lookup` freshness and fetch failure**

Append to `cmd/server/rf_params_cache_test.go`:

```go
func TestLookupFreshness(t *testing.T) {
	c := NewRFParamsCache()

	fresh := time.Now().UTC().Add(-3 * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	stale := time.Now().UTC().Add(-8 * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")

	c.mu.Lock()
	c.index["aabbcc"] = &RFParams{Freq: 869.618, SF: 8, BW: 62.5, CR: 8, LastAdvert: fresh}
	c.index["ddeeff"] = &RFParams{Freq: 869.618, SF: 8, BW: 62.5, CR: 8, LastAdvert: stale}
	c.mu.Unlock()

	if c.Lookup("aabbcc") == nil {
		t.Error("expected fresh entry to be returned, got nil")
	}
	if c.Lookup("ddeeff") != nil {
		t.Error("expected stale entry to return nil, got non-nil")
	}
	if c.Lookup("unknown") != nil {
		t.Error("expected missing key to return nil")
	}
}

func TestRefreshPreservesOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	c := NewRFParamsCache()
	c.nodeURL = srv.URL

	// Pre-populate cache
	c.mu.Lock()
	c.index["aabbcc"] = &RFParams{Freq: 869.618}
	c.mu.Unlock()

	c.refresh()

	c.mu.RLock()
	_, ok := c.index["aabbcc"]
	c.mu.RUnlock()
	if !ok {
		t.Error("cache should be preserved on fetch failure")
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```
cd cmd/server && go test -run "TestLookupFreshness|TestRefreshPreservesOnFailure" -v
```

Expected: FAIL — `c.refresh undefined`

- [ ] **Step 3: Add `refresh` and `Start` to `cmd/server/rf_params_cache.go`**

Append to `rf_params_cache.go`:

```go
// refresh fetches the full node list from map.meshcore.io and rebuilds the
// index. On any error the existing index is kept intact.
func (c *RFParamsCache) refresh() {
	resp, err := http.Get(c.nodeURL) //nolint:noctx
	if err != nil {
		log.Printf("[rf-params] node fetch error: %v", err)
		return
	}
	defer resp.Body.Close()

	type rawNode struct {
		PublicKey  string `json:"public_key"`
		LastAdvert string `json:"last_advert"`
		Params     *struct {
			Freq float64 `json:"freq"`
			SF   int     `json:"sf"`
			CR   int     `json:"cr"`
			BW   float64 `json:"bw"`
		} `json:"params"`
	}

	var nodes []rawNode
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		log.Printf("[rf-params] node decode error: %v", err)
		return
	}

	index := make(map[string]*RFParams, len(nodes))
	for _, n := range nodes {
		if n.PublicKey == "" || n.Params == nil {
			continue
		}
		index[strings.ToLower(n.PublicKey)] = &RFParams{
			Freq:       n.Params.Freq,
			SF:         n.Params.SF,
			CR:         n.Params.CR,
			BW:         n.Params.BW,
			LastAdvert: n.LastAdvert,
		}
	}

	c.mu.Lock()
	c.index = index
	c.mu.Unlock()
	log.Printf("[rf-params] node index refreshed (%d entries)", len(index))
}

// Start loads presets once, then fetches the node index immediately and every
// 6 hours. Returns a stop function; call it to shut down the goroutine.
func (c *RFParamsCache) Start() func() {
	c.loadPresets()
	done := make(chan struct{})
	go func() {
		c.refresh()
		ticker := time.NewTicker(rfRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.refresh()
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}
```

- [ ] **Step 4: Run all cache tests**

```
cd cmd/server && go test -run "TestBuildPresetKey|TestLoadPresets|TestLookupFreshness|TestRefreshPreservesOnFailure" -v
```

Expected: all 4 PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/server/rf_params_cache.go cmd/server/rf_params_cache_test.go
git commit -m "feat: RFParamsCache node index fetch and Lookup (task 2)"
```

---

### Task 3: Wire into Server — `/api/rf-presets` + enrichment in `handleNodes` and `handleNodeDetail`

**Files:**
- Modify: `cmd/server/routes.go`
- Modify: `cmd/server/routes_test.go`
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Write failing route tests**

Append to `cmd/server/routes_test.go`:

```go
func TestRFPresetsEndpoint(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/rf-presets", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var presets map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &presets); err != nil {
		t.Fatalf("response is not a JSON object: %v", err)
	}
}

func TestNodesEnrichedWithRFParams(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/nodes?limit=100", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Nodes []map[string]interface{} `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	// rf_params key must be present on every node (value may be null)
	for _, n := range resp.Nodes {
		if _, ok := n["rf_params"]; !ok {
			t.Errorf("node %v missing rf_params key", n["public_key"])
		}
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```
cd cmd/server && go test -run "TestRFPresetsEndpoint|TestNodesEnrichedWithRFParams" -v
```

Expected: FAIL — 404 on `/api/rf-presets`, `rf_params` key absent

- [ ] **Step 3: Add `rfCache` to `Server` struct in `routes.go` (around line 23)**

In `cmd/server/routes.go`, add the field to the `Server` struct:

```go
// Add after the existing fields (e.g. after "version   string"):
rfCache *RFParamsCache
```

- [ ] **Step 4: Initialize `rfCache` in `NewServer` (routes.go line 77)**

```go
func NewServer(db *DB, cfg *Config, hub *Hub) *Server {
	return &Server{
		db:        db,
		cfg:       cfg,
		hub:       hub,
		startedAt: time.Now(),
		perfStats: NewPerfStats(),
		version:   resolveVersion(),
		commit:    resolveCommit(),
		buildTime: resolveBuildTime(),
		rfCache:   NewRFParamsCache(),
	}
}
```

- [ ] **Step 5: Add `handleRFPresets` handler in `routes.go`**

Add after `handleNodes` (around line 1244):

```go
func (s *Server) handleRFPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.rfCache.GetPresets())
}
```

- [ ] **Step 6: Register the route in `RegisterRoutes` (routes.go around line 165)**

Add after the existing `/api/nodes` route line:

```go
r.HandleFunc("/api/rf-presets", s.handleRFPresets).Methods("GET")
```

- [ ] **Step 7: Enrich nodes in `handleNodes` (routes.go, just before `writeJSON` at line 1243)**

Add the RF params enrichment loop directly before the `writeJSON` call:

```go
// Enrich with RF params from map.meshcore.io cache
for _, node := range nodes {
    if pk, ok := node["public_key"].(string); ok {
        node["rf_params"] = s.rfCache.Lookup(pk)
    }
}
```

- [ ] **Step 8: Enrich node in `handleNodeDetail` (routes.go, just before `writeJSON` at line 1333)**

Add after the existing store enrichment block (after line 1327):

```go
node["rf_params"] = s.rfCache.Lookup(pubkey)
```

- [ ] **Step 9: Wire `Start()` in `main.go` (after `srv.store = store` around line 324)**

```go
stopRFCache := srv.rfCache.Start()
defer stopRFCache()
log.Printf("[rf-params] cache started (refresh every 6h)")
```

- [ ] **Step 10: Run the route tests**

```
cd cmd/server && go test -run "TestRFPresetsEndpoint|TestNodesEnrichedWithRFParams" -v
```

Expected: both PASS

- [ ] **Step 11: Run the full Go test suite**

```
cd cmd/server && go test ./... 2>&1 | tail -20
```

Expected: all PASS

- [ ] **Step 12: Commit**

```bash
git add cmd/server/routes.go cmd/server/routes_test.go cmd/server/main.go
git commit -m "feat: /api/rf-presets endpoint + rf_params enrichment in /api/nodes and /api/nodes/{pubkey} (task 3)"
```

---

### Task 4: Frontend — `resolveLoraLabel` + preset fetch in `loadNodes`

**Files:**
- Create: `test-rf-params.js`
- Modify: `test-all.sh`
- Modify: `public/nodes.js`

- [ ] **Step 1: Create `test-rf-params.js` with failing tests**

```js
'use strict';
const { JSDOM } = require('jsdom');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✓ ${name}`); }
  catch(e) { failed++; console.log(`  ✗ ${name}\n    ${e.message}`); }
}

// Load nodes.js into a minimal DOM so resolveLoraLabel is accessible
function makeEnv(presets) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><div id="app"></div></body></html>`, {
    url: 'http://localhost',
    runScripts: 'dangerously'
  });
  const w = dom.window;
  // Stubs required by nodes.js
  w.localStorage = { getItem: () => null, setItem: () => {} };
  w.ROLE_COLORS = {};
  w.TableSort = { init: () => ({}) };
  w.TableResponsive = { register: () => {} };
  w._rfPresetsForTest = presets || {};
  const src = fs.readFileSync(path.join(__dirname, 'public', 'nodes.js'), 'utf8');
  const el = w.document.createElement('script');
  el.textContent = src;
  w.document.head.appendChild(el);
  return w;
}

console.log('\nresolveLoraLabel');

test('known preset returns label', () => {
  const w = makeEnv({ '869.618/8/62.5/8': 'EU/UK (Narrow) / Switzerland' });
  const result = w.resolveLoraLabelForTest({ freq: 869.618, sf: 8, bw: 62.5, cr: 8 });
  assert.strictEqual(result, 'EU/UK (Narrow) / Switzerland');
});

test('Netherlands (Narrow) preset', () => {
  const w = makeEnv({ '869.618/7/62.5/5': 'Netherlands (Narrow)' });
  const result = w.resolveLoraLabelForTest({ freq: 869.618, sf: 7, bw: 62.5, cr: 5 });
  assert.strictEqual(result, 'Netherlands (Narrow)');
});

test('unknown combo returns raw summary', () => {
  const w = makeEnv({});
  const result = w.resolveLoraLabelForTest({ freq: 915.3, sf: 9, bw: 125, cr: 6 });
  assert.strictEqual(result, '915.3 SF9');
});

test('null rf_params returns empty string', () => {
  const w = makeEnv({});
  const result = w.resolveLoraLabelForTest(null);
  assert.strictEqual(result, '');
});

console.log(`\n${passed + failed} tests, ${passed} passed, ${failed} failed\n`);
process.exit(failed > 0 ? 1 : 0);
```

- [ ] **Step 2: Run test to confirm it fails**

```
node test-rf-params.js
```

Expected: FAIL — `resolveLoraLabelForTest is not a function`

- [ ] **Step 3: Add `_rfPresets` module variable and `resolveLoraLabel` to `nodes.js`**

In `public/nodes.js`, after line 67 (`let statusFilter = ...`), add:

```js
  let _rfPresets = {};

  function resolveLoraLabel(rfParams) {
    if (!rfParams) return '';
    var key = rfParams.freq.toFixed(3) + '/' + rfParams.sf + '/' + rfParams.bw.toFixed(1) + '/' + rfParams.cr;
    if (_rfPresets[key]) return _rfPresets[key];
    return rfParams.freq + ' SF' + rfParams.sf;
  }
  // Test hook — allows test-rf-params.js to inject a preset map and call the function
  window.resolveLoraLabelForTest = function(rfParams) {
    _rfPresets = window._rfPresetsForTest || {};
    return resolveLoraLabel(rfParams);
  };
```

- [ ] **Step 4: Add preset fetch to `loadNodes` in `nodes.js` (around line 1018)**

Replace the `Promise.all` in `loadNodes`:

```js
// Before (existing):
const [data] = await Promise.all([
  api('/nodes?' + params, { ttl: CLIENT_TTL.nodeList }),
  getFleetSkew()
]);
_allNodes = data.nodes || [];

// After:
const presetsPromise = Object.keys(_rfPresets).length
  ? Promise.resolve(null)
  : api('/rf-presets', { ttl: 86400000 }).catch(() => null);
const [data, , presetsData] = await Promise.all([
  api('/nodes?' + params, { ttl: CLIENT_TTL.nodeList }),
  getFleetSkew(),
  presetsPromise
]);
if (presetsData) _rfPresets = presetsData;
_allNodes = data.nodes || [];
```

- [ ] **Step 5: Run test to confirm it passes**

```
node test-rf-params.js
```

Expected: all PASS

- [ ] **Step 6: Add test to `test-all.sh`**

In `test-all.sh`, add after the last `node test-*.js` line before the final echo:

```sh
node test-rf-params.js
```

- [ ] **Step 7: Commit**

```bash
git add test-rf-params.js test-all.sh public/nodes.js
git commit -m "feat: resolveLoraLabel + preset fetch in loadNodes (task 4)"
```

---

### Task 5: Frontend — LoRa column in nodes table

**Files:**
- Modify: `public/nodes.js`

- [ ] **Step 1: Add `lora` sort case to `sortNodes` in `nodes.js` (around line 62, before `return 0`)**

```js
} else if (col === 'lora') {
  va = n.rf_params ? resolveLoraLabel(n.rf_params) : '';
  vb = b.rf_params ? resolveLoraLabel(b.rf_params) : '';  // Note: use b not n
  // Empty string always last regardless of direction
  if (!va && !vb) return 0;
  if (!va) return 1;
  if (!vb) return -1;
  return va < vb ? -dir : va > vb ? dir : 0;
```

Note: the comparator uses the outer `a` and `b` params from `arr.sort(function (a, b) {...})`. Make sure variable names match. The correct expansion of the existing pattern is:

```js
} else if (col === 'lora') {
  va = a.rf_params ? resolveLoraLabel(a.rf_params) : '';
  vb = b.rf_params ? resolveLoraLabel(b.rf_params) : '';
  if (!va && !vb) return 0;
  if (!va) return 1;
  if (!vb) return -1;
  return va < vb ? -dir : va > vb ? dir : 0;
}
```

- [ ] **Step 2: Add `<th>` to the nodes table header in `nodes.js` (line 1137, after Adverts `<th>`)**

```js
// After:
<th scope="col" data-sort-key="advert_count" data-sort-default="desc" data-priority="2">Adverts</th>
// Add:
<th scope="col" data-sort-key="lora" data-priority="3">LoRa</th>
```

- [ ] **Step 3: Add `<td>` to the row template in `renderRows` (line 1269, after `n.advert_count || 0`)**

```js
// After:
<td>${n.advert_count || 0}</td>
// Add:
<td data-value="${n.rf_params ? resolveLoraLabel(n.rf_params) : ''}">${escapeHtml(n.rf_params ? resolveLoraLabel(n.rf_params) : '')}</td>
```

- [ ] **Step 4: Add null-last sort test to `test-rf-params.js`**

Append to `test-rf-params.js` (before the final summary lines):

```js
console.log('\nnull-last sort');

test('nodes without rf_params sort last in asc', () => {
  const w = makeEnv({ '869.618/8/62.5/5': 'EU/UK (Narrow)' });
  // Simulate sortNodes behaviour for lora column
  const nodes = [
    { rf_params: null },
    { rf_params: { freq: 869.618, sf: 8, bw: 62.5, cr: 8 } },
    { rf_params: null },
    { rf_params: { freq: 910.525, sf: 7, bw: 62.5, cr: 5 } },
  ];
  const resolveLabel = (n) => w.resolveLoraLabelForTest(n.rf_params);
  const sorted = nodes.slice().sort((a, b) => {
    const va = resolveLabel(a);
    const vb = resolveLabel(b);
    if (!va && !vb) return 0;
    if (!va) return 1;
    if (!vb) return -1;
    return va < vb ? -1 : va > vb ? 1 : 0;
  });
  // Nodes with rf_params should come before null ones
  const lastTwo = sorted.slice(2);
  assert.ok(lastTwo.every(n => n.rf_params === null), 'null rf_params should be last');
});

test('nodes without rf_params sort last in desc', () => {
  const w = makeEnv({ '869.618/8/62.5/5': 'EU/UK (Narrow)' });
  const nodes = [
    { rf_params: null },
    { rf_params: { freq: 869.618, sf: 8, bw: 62.5, cr: 8 } },
  ];
  const resolveLabel = (n) => w.resolveLoraLabelForTest(n.rf_params);
  const dir = -1; // desc
  const sorted = nodes.slice().sort((a, b) => {
    const va = resolveLabel(a);
    const vb = resolveLabel(b);
    if (!va && !vb) return 0;
    if (!va) return 1;
    if (!vb) return -1;
    return (va < vb ? -1 : va > vb ? 1 : 0) * dir;
  });
  assert.ok(sorted[sorted.length - 1].rf_params === null, 'null should be last even in desc');
});
```

- [ ] **Step 5: Run test suite**

```
node test-rf-params.js
```

Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add public/nodes.js test-rf-params.js
git commit -m "feat: LoRa column + null-last sort in nodes table (task 5)"
```

---

### Task 6: Frontend — RF params section in node detail panel

**Files:**
- Modify: `public/nodes.js`

- [ ] **Step 1: Add RF params section to the detail panel template**

In `public/nodes.js`, in the `loadFullNode` template string, locate the closing `</table>` of `node-stats-table` (around line 560). Add the following section immediately after that `</table>` tag and before the `<div class="node-full-card" id="node-packets">` div:

```js
${n.rf_params ? (() => {
  const label = resolveLoraLabel(n.rf_params);
  const presetRow = _rfPresets[n.rf_params.freq.toFixed(3) + '/' + n.rf_params.sf + '/' + n.rf_params.bw.toFixed(1) + '/' + n.rf_params.cr]
    ? `<tr><td>Preset</td><td>${escapeHtml(label)}</td></tr>` : '';
  const advertAge = (() => {
    try {
      const ms = Date.now() - new Date(n.rf_params.last_advert).getTime();
      const days = Math.floor(ms / 86400000);
      return days === 0 ? 'today' : days === 1 ? 'yesterday' : days + ' days ago';
    } catch { return ''; }
  })();
  return `<table class="node-stats-table" id="node-rf-params">
    <tr><th colspan="2" style="font-weight:600;padding-bottom:4px">RF Parameters</th></tr>
    ${presetRow}
    <tr><td>Frequency</td><td>${n.rf_params.freq} MHz</td></tr>
    <tr><td>Spreading Factor</td><td>SF${n.rf_params.sf}</td></tr>
    <tr><td>Bandwidth</td><td>${n.rf_params.bw} kHz</td></tr>
    <tr><td>Coding Rate</td><td>CR${n.rf_params.cr}</td></tr>
    <tr><td colspan="2" style="font-size:11px;color:var(--text-muted)">via MeshCore Map · last advert ${escapeHtml(advertAge)}</td></tr>
  </table>`;
})() : ''}
```

- [ ] **Step 2: Apply the same section to the slide-over detail template**

The slide-over renders at `nodes.js` around line 1329 inside `selectNode`. Search for the line that assigns `so.innerHTML = ` — it contains a `<table class="node-stats-table"` block with rows for Status, Last Heard, etc. Locate the closing `</table>` of that stats table inside the slide-over template and insert the exact same RF params snippet from Step 1 after it.

Both the full-screen view (`loadFullNode`) and the slide-over (`selectNode`) call `fetchNodeDetail` and use the same `n` object, which now includes `rf_params`. The snippet is identical; only the insertion point in the template string differs.

- [ ] **Step 3: Run full JS test suite**

```
node test-all.sh
```

Expected: all PASS

- [ ] **Step 4: Run Go tests**

```
cd cmd/server && go test ./... 2>&1 | tail -5
```

Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add public/nodes.js
git commit -m "feat: RF params section in node detail panel (task 6)"
```

---

### Task 7: Integration smoke test + docs

**Files:**
- Modify: `docs/api-spec.md`

- [ ] **Step 1: Update `docs/api-spec.md` to document `/api/rf-presets` and the new `rf_params` field**

Find the `/api/nodes` section in `docs/api-spec.md` and add the `rf_params` field to the node object description:

```
rf_params: object|null
  Present when map.meshcore.io has a fresh (≤7 days) entry for this node.
  freq: number — frequency in MHz
  sf: number — spreading factor
  cr: number — coding rate
  bw: number — bandwidth in kHz
  last_advert: string — ISO timestamp of the last advert on the map
```

Add a new endpoint section:

```
GET /api/rf-presets
Returns a JSON object mapping canonical LoRa param keys to preset labels.
Key format: "{freq}/{sf}/{bw}/{cr}" e.g. "869.618/8/62.5/8"
Value: human-readable label e.g. "EU/UK (Narrow) / Switzerland"
Served from the startup-time config fetch; updated only on server restart.
```

- [ ] **Step 2: Commit docs**

```bash
git add docs/api-spec.md
git commit -m "docs: add /api/rf-presets and rf_params field to api-spec.md (task 7)"
```
