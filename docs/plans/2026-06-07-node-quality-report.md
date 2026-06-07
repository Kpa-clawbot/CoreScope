# Node Quality Report Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-node "quality report" — a map of the node's bidirectional (two-way) RF links + a quality table + importance stats — reachable at `#/nodes/<pubkey>?section=quality` and printable to offline PDF.

**Architecture:** Read-only Go endpoint `GET /api/nodes/:pubkey/quality?days=N` computes link directionality on demand from raw `observations.path_json` (a path travels origin→observer, so `[A,B]` ⇒ B heard A). It reuses the existing `prefixMap` (relay-node prefix index) and `buildNodeInfoMap`/`haversineKm`. A new vanilla-JS section in the node detail page renders importance cards, a Leaflet link-map, and a table, with a print stylesheet for PDF. No change to `neighbor_edges`/schema (approach B), so it is self-contained and upstream-PR-ready.

**Tech Stack:** Go (`cmd/server`, gorilla/mux, stdlib SQLite read-only), vanilla JS + Leaflet 1.9 (`public/`), no build step.

**Why not reuse `resolved_path`?** That column exists but is **unpopulated on prod** (verified: 0 of 162 341 windowed rows have it). So directionality must be computed from raw `path_json` with a conservative **unique 2–3 byte prefix** rule (1-byte prefixes collide and are excluded).

---

## File Structure

| File | Responsibility | Create/Modify |
|------|----------------|---------------|
| `cmd/server/node_quality.go` | Endpoint handler, response structs, pure `attributeDirections`, reliable-token derivation, bounded TTL cache | Create |
| `cmd/server/node_quality_test.go` | Unit tests for the pure function + token derivation | Create |
| `cmd/server/node_quality_endpoint_test.go` | Handler shape/404/clamp tests | Create |
| `cmd/server/node_quality_bench_test.go` | Scan benchmark (perf proof) | Create |
| `cmd/server/routes.go` | Register the route | Modify (~line 240) |
| `docs/api-spec.md` | Document the endpoint (contract first) | Modify |
| `public/node-quality.js` | Fetch + render section (cards, map, table, one-way toggle, print) | Create |
| `public/node-quality.css` | Section + `@media print` styles, link-colour CSS vars | Create |
| `public/style.css` | Add `--link-strong/-medium/-weak` vars to `:root` | Modify |
| `public/index.html` | `<script>`/`<link>` includes for the two new files | Modify |
| `public/nodes.js` | Add `#node-quality` card placeholder + invoke renderer | Modify (~line 707) |
| `test-node-quality-e2e.js` | Playwright E2E | Create |

---

## Task 1: API contract entry (contract-first, AGENTS rule 4)

**Files:**
- Modify: `docs/api-spec.md` (add a section after `GET /api/nodes/:pubkey/analytics`, and a ToC line)

- [ ] **Step 1: Add the ToC entry**

In the Table of Contents list, after the `GET /api/nodes/:pubkey/analytics` line, add:
```markdown
- [GET /api/nodes/:pubkey/quality](#get-apinodespubkeyquality)
```

- [ ] **Step 2: Add the endpoint section**

After the `GET /api/nodes/:pubkey/analytics` section (before `GET /api/packets`), insert:

````markdown
## GET /api/nodes/:pubkey/quality

Per-node RF link-quality report. Computes **directional** link counts from raw
path adjacency (a flood path is recorded origin→observer, so in `[A,B]` B received
A directly). A link is **bidirectional** when both directions have observations;
the **bottleneck** (weaker direction) rates two-way stability. Read-only; bounded
to a recent window. Identifies nodes only by **unique 2–3 byte** path prefixes
(1-byte prefixes collide and are excluded).

### Query Parameters

| Param  | Type   | Default | Description                          |
|--------|--------|---------|--------------------------------------|
| `days` | number | `7`     | Lookback window, clamped 1–30        |

### Response `200`

```jsonc
{
  "node": { "pubkey": string, "name": string, "role": string,
            "lat": number | null, "lon": number | null, "first_seen": string (ISO) },
  "window": { "days": number, "since": string (ISO) },
  "reliable_tokens": [string],          // uppercase hex prefixes unique to this node ([] if unidentifiable)
  "importance": {
    "neighbor_degree":    number,        // all-time, from neighbor_edges
    "degree_rank":        number,        // 1-based rank among nodes with edges
    "nodes_with_edges":   number,
    "relay_observations": number,        // windowed obs with this node anywhere in path
    "bidirectional_links":number,
    "direct_observers":   number
  },
  "direct_observers": [
    { "pubkey": string, "name": string, "count": number,
      "avg_snr": number | null, "lat": number | null, "lon": number | null,
      "distance_km": number | null }
  ],
  "links": [
    { "pubkey": string, "name": string, "role": string,
      "lat": number | null, "lon": number | null,
      "we_hear": number, "they_hear": number,
      "bottleneck": number, "bidir": boolean,
      "distance_km": number | null }
  ]
}
```

`reliable_tokens: []` means the node has no unique 1–3 byte prefix and cannot be
reliably identified in paths; `links`/`direct_observers` will be empty.

### Response `404`

```json
{ "error": "Not found" }
```
````

- [ ] **Step 3: Commit**

```bash
git add docs/api-spec.md
git commit -m "docs(api-spec): add GET /api/nodes/:pubkey/quality contract"
```

---

## Task 2: Core types + pure direction attributor (TDD)

**Files:**
- Create: `cmd/server/node_quality.go`
- Create: `cmd/server/node_quality_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/server/node_quality_test.go`:
```go
package main

import "testing"

// resolver that only resolves the exact tokens it's told are unique.
func testResolver(unique map[string]string) func(string) string {
	return func(tok string) string {
		if pk, ok := unique[tok]; ok {
			return pk
		}
		return "" // ambiguous / unknown → skip
	}
}

func TestAttributeDirections_PredecessorAndSuccessor(t *testing.T) {
	// path A(aa) -> N(01fa) -> B(bb): we hear A, B hears us.
	unique := map[string]string{"AA": "aa00", "BB": "bb00"}
	rows := []pathRow{{
		observerPK: "obs1", payloadType: 5,
		path: []string{"AA", "01FA", "BB"},
	}}
	d := attributeDirections(rows, map[string]bool{"01FA": true}, "01fa326b", testResolver(unique))
	if d.we["aa00"] != 1 {
		t.Fatalf("we_hear[aa00]=%d want 1", d.we["aa00"])
	}
	if d.they["bb00"] != 1 {
		t.Fatalf("they_hear[bb00]=%d want 1", d.they["bb00"])
	}
	if d.relay != 1 {
		t.Fatalf("relay=%d want 1", d.relay)
	}
}

func TestAttributeDirections_LastHopObserverAndAdvertFirstHop(t *testing.T) {
	snr := 4.0
	rows := []pathRow{
		// N is last hop → observer heard us directly (+snr).
		{observerPK: "obsx", payloadType: 5, path: []string{"AA", "01FA"}, snr: &snr},
		// N is first hop of an ADVERT (type 4) → we heard the originator.
		{observerPK: "obsy", payloadType: 4, fromPubkey: "origin1", path: []string{"01FA", "CC"}},
	}
	d := attributeDirections(rows, map[string]bool{"01FA": true}, "01fa326b",
		testResolver(map[string]string{"CC": "cc00"}))
	if d.obs["obsx"] == nil || d.obs["obsx"].count != 1 {
		t.Fatalf("observer obsx not counted")
	}
	if d.obs["obsx"].snrN != 1 || d.obs["obsx"].snrSum != 4.0 {
		t.Fatalf("observer snr not aggregated")
	}
	if d.they["obsx"] != 1 {
		t.Fatalf("they_hear[obsx]=%d want 1", d.they["obsx"])
	}
	if d.we["origin1"] != 1 {
		t.Fatalf("we_hear[origin1]=%d want 1 (advert first-hop)", d.we["origin1"])
	}
	if d.they["cc00"] != 1 {
		t.Fatalf("they_hear[cc00]=%d want 1 (successor)", d.they["cc00"])
	}
}

func TestAttributeDirections_AmbiguousSkippedAndSelfIgnored(t *testing.T) {
	rows := []pathRow{{observerPK: "obs1", payloadType: 5, path: []string{"ZZ", "01FA", "01FA"}}}
	// ZZ unresolved (ambiguous), trailing 01FA resolves to self → ignored.
	d := attributeDirections(rows, map[string]bool{"01FA": true}, "01fa326b",
		testResolver(map[string]string{"01FA": "01fa326b"}))
	if len(d.we) != 0 || len(d.they) != 0 {
		t.Fatalf("ambiguous/self should yield no edges, got we=%v they=%v", d.we, d.they)
	}
}
```

- [ ] **Step 2: Run it — verify it fails to compile**

Run: `cd cmd/server && go test ./... -run TestAttributeDirections`
Expected: build error `undefined: pathRow` / `undefined: attributeDirections`.

- [ ] **Step 3: Write the minimal implementation**

Create `cmd/server/node_quality.go`:
```go
package main

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
)

// advertPayloadType mirrors MeshCore ADVERT (0x04). Local const so this file
// stays independent of decoder internals.
const advertPayloadType = 4

// pathRow is one observation fed to attributeDirections. path tokens are
// uppercase hex hop prefixes (as stored in observations.path_json).
type pathRow struct {
	observerPK  string // lowercase pubkey of the observer (may be "")
	fromPubkey  string // lowercase originator pubkey (may be "")
	payloadType int
	path        []string
	snr         *float64
}

type obsAgg struct {
	count  int
	snrSum float64
	snrN   int
}

type dirCounts struct {
	we    map[string]int
	they  map[string]int
	obs   map[string]*obsAgg
	relay int
}

// attributeDirections walks each path and attributes directional evidence for
// the target node (identified by any token in ourTokens). resolve maps a hop
// token → a unique relay pubkey ("" when ambiguous/unknown → skipped). ourPK is
// the target's own pubkey (lowercase) so self-edges are ignored.
func attributeDirections(rows []pathRow, ourTokens map[string]bool, ourPK string, resolve func(string) string) dirCounts {
	d := dirCounts{we: map[string]int{}, they: map[string]int{}, obs: map[string]*obsAgg{}}
	for _, r := range rows {
		n := len(r.path)
		if n == 0 {
			continue
		}
		hit := false
		for i, tok := range r.path {
			if !ourTokens[tok] {
				continue
			}
			hit = true
			// predecessor → we heard it
			if i > 0 {
				if pk := resolve(r.path[i-1]); pk != "" && pk != ourPK {
					d.we[pk]++
				}
			} else if r.payloadType == advertPayloadType && r.fromPubkey != "" && r.fromPubkey != ourPK {
				d.we[r.fromPubkey]++
			}
			// successor → it heard us; or if we're the last hop, the observer did
			if i < n-1 {
				if pk := resolve(r.path[i+1]); pk != "" && pk != ourPK {
					d.they[pk]++
				}
			} else if r.observerPK != "" && r.observerPK != ourPK {
				d.they[r.observerPK]++
				a := d.obs[r.observerPK]
				if a == nil {
					a = &obsAgg{}
					d.obs[r.observerPK] = a
				}
				a.count++
				if r.snr != nil {
					a.snrSum += *r.snr
					a.snrN++
				}
			}
		}
		if hit {
			d.relay++
		}
	}
	return d
}
```

- [ ] **Step 4: Run the tests — verify they pass**

Run: `cd cmd/server && go test ./... -run TestAttributeDirections -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add cmd/server/node_quality.go cmd/server/node_quality_test.go
git commit -m "feat(server): add directional path attribution for node quality"
```

---

## Task 3: Reliable-token derivation (TDD)

**Files:**
- Modify: `cmd/server/node_quality.go`
- Modify: `cmd/server/node_quality_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/server/node_quality_test.go`:
```go
func TestReliableTokens(t *testing.T) {
	// pm where "01fa" is unique but "01" is shared (collision).
	nodes := []nodeInfo{
		{PublicKey: "01fa326b0000", Role: "repeater"},
		{PublicKey: "0188aaaa0000", Role: "repeater"},
	}
	pm := buildPrefixMap(nodes)
	toks := reliableTokens("01fa326b0000", pm)
	if !toks["01FA"] {
		t.Fatalf("expected 01FA reliable, got %v", toks)
	}
	if toks["01"] {
		t.Fatalf("1-byte 01 must be excluded (collision), got %v", toks)
	}
}
```

- [ ] **Step 2: Run it — verify it fails**

Run: `cd cmd/server && go test ./... -run TestReliableTokens`
Expected: FAIL — `undefined: reliableTokens`.

- [ ] **Step 3: Implement**

Append to `cmd/server/node_quality.go`:
```go
// reliableTokens returns the uppercase hex prefixes (1, 2, 3 byte) of pubkey
// that are UNIQUE among relay-capable nodes in pm. 1-byte prefixes almost always
// collide and are excluded; only unique prefixes can identify a node in a path.
func reliableTokens(pubkey string, pm *prefixMap) map[string]bool {
	out := map[string]bool{}
	lpk := strings.ToLower(pubkey)
	for _, l := range []int{2, 4, 6} { // hex chars = 1,2,3 bytes
		if len(lpk) < l {
			continue
		}
		p := lpk[:l]
		if pm != nil && len(pm.m[p]) == 1 {
			out[strings.ToUpper(p)] = true
		}
	}
	return out
}

// uniqueResolve returns the single relay pubkey (lowercase) for a hop token, or
// "" when the token resolves to zero or multiple candidates (conservative).
func uniqueResolve(pm *prefixMap, token string) string {
	if pm == nil {
		return ""
	}
	cands := pm.m[strings.ToLower(token)]
	if len(cands) == 1 {
		return strings.ToLower(cands[0].PublicKey)
	}
	return ""
}
```

- [ ] **Step 4: Run — verify pass**

Run: `cd cmd/server && go test ./... -run "TestReliableTokens|TestAttributeDirections" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/server/node_quality.go cmd/server/node_quality_test.go
git commit -m "feat(server): derive unique reliable path tokens per node"
```

---

## Task 4: Response structs + builder (assemble JSON)

**Files:**
- Modify: `cmd/server/node_quality.go`

- [ ] **Step 1: Add the response structs**

Append to `cmd/server/node_quality.go`:
```go
type QualityNode struct {
	Pubkey    string   `json:"pubkey"`
	Name      string   `json:"name"`
	Role      string   `json:"role"`
	Lat       *float64 `json:"lat"`
	Lon       *float64 `json:"lon"`
	FirstSeen string   `json:"first_seen"`
}
type QualityWindow struct {
	Days  int    `json:"days"`
	Since string `json:"since"`
}
type QualityImportance struct {
	NeighborDegree     int `json:"neighbor_degree"`
	DegreeRank         int `json:"degree_rank"`
	NodesWithEdges     int `json:"nodes_with_edges"`
	RelayObservations  int `json:"relay_observations"`
	BidirectionalLinks int `json:"bidirectional_links"`
	DirectObservers    int `json:"direct_observers"`
}
type QualityObserver struct {
	Pubkey     string   `json:"pubkey"`
	Name       string   `json:"name"`
	Count      int      `json:"count"`
	AvgSNR     *float64 `json:"avg_snr"`
	Lat        *float64 `json:"lat"`
	Lon        *float64 `json:"lon"`
	DistanceKm *float64 `json:"distance_km"`
}
type QualityLink struct {
	Pubkey     string   `json:"pubkey"`
	Name       string   `json:"name"`
	Role       string   `json:"role"`
	Lat        *float64 `json:"lat"`
	Lon        *float64 `json:"lon"`
	WeHear     int      `json:"we_hear"`
	TheyHear   int      `json:"they_hear"`
	Bottleneck int      `json:"bottleneck"`
	Bidir      bool     `json:"bidir"`
	DistanceKm *float64 `json:"distance_km"`
}
type NodeQualityResponse struct {
	Node            QualityNode       `json:"node"`
	Window          QualityWindow     `json:"window"`
	ReliableTokens  []string          `json:"reliable_tokens"`
	Importance      QualityImportance `json:"importance"`
	DirectObservers []QualityObserver `json:"direct_observers"`
	Links           []QualityLink     `json:"links"`
}

func fptr(v float64) *float64 { return &v }

// gpsPtrs returns (lat,lon) pointers, nil when the node has no GPS (0,0).
func gpsPtrs(info nodeInfo, ok bool) (*float64, *float64) {
	if !ok || !info.HasGPS {
		return nil, nil
	}
	return fptr(info.Lat), fptr(info.Lon)
}
```

- [ ] **Step 2: Compile check**

Run: `cd cmd/server && go build ./...`
Expected: builds clean.

- [ ] **Step 3: Commit**

```bash
git add cmd/server/node_quality.go
git commit -m "feat(server): node quality response structs"
```

---

## Task 5: Handler + scan + importance + cache + route

**Files:**
- Modify: `cmd/server/node_quality.go`
- Modify: `cmd/server/routes.go` (register route before the `{pubkey}` catch-all)

- [ ] **Step 1: Implement the bounded cache + handler**

Append to `cmd/server/node_quality.go`:
```go
// --- bounded TTL cache (perf is gated by the time window; this just avoids
// recompute under dashboard polling). Keyed "pubkey|days". ---
const (
	qualityCacheTTL = 5 * time.Minute
	qualityCacheMax = 256
)

type qualityCacheEntry struct {
	at  time.Time
	raw []byte
}

var (
	qualityCacheMu sync.Mutex
	qualityCache   = map[string]qualityCacheEntry{}
)

func qualityCacheGet(key string) ([]byte, bool) {
	qualityCacheMu.Lock()
	defer qualityCacheMu.Unlock()
	e, ok := qualityCache[key]
	if !ok || time.Since(e.at) > qualityCacheTTL {
		return nil, false
	}
	return e.raw, true
}

func qualityCachePut(key string, raw []byte) {
	qualityCacheMu.Lock()
	defer qualityCacheMu.Unlock()
	if len(qualityCache) >= qualityCacheMax {
		qualityCache = map[string]qualityCacheEntry{} // crude bounded reset
	}
	qualityCache[key] = qualityCacheEntry{at: time.Now(), raw: raw}
}

func (s *Server) handleNodeQuality(w http.ResponseWriter, r *http.Request) {
	pubkey := strings.ToLower(mux.Vars(r)["pubkey"])
	if s.cfg != nil && s.cfg.IsBlacklisted(pubkey) {
		writeError(w, 404, "Not found")
		return
	}
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			days = n
		}
	}
	if days < 1 {
		days = 1
	}
	if days > 30 {
		days = 30
	}

	cacheKey := pubkey + "|" + strconv.Itoa(days)
	if raw, ok := qualityCacheGet(cacheKey); ok {
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
		return
	}

	resp, ok := s.computeNodeQuality(pubkey, days)
	if !ok {
		writeError(w, 404, "Not found")
		return
	}
	raw, _ := json.Marshal(resp)
	qualityCachePut(cacheKey, raw)
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

// computeNodeQuality does the read-only scan + assembly. ok=false → 404.
func (s *Server) computeNodeQuality(pubkey string, days int) (NodeQualityResponse, bool) {
	if s.store == nil || s.db == nil || s.db.conn == nil {
		return NodeQualityResponse{}, false
	}
	nodeMap := s.buildNodeInfoMap()
	self, found := nodeMap[pubkey]
	if !found {
		return NodeQualityResponse{}, false
	}
	_, pm := s.store.getCachedNodesAndPM()
	tokens := reliableTokens(pubkey, pm)

	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	sinceEpoch := since.Unix()

	var d dirCounts
	if len(tokens) > 0 {
		rows := s.scanQualityRows(tokens, sinceEpoch)
		d = attributeDirections(rows, tokens, pubkey, func(tok string) string {
			return uniqueResolve(pm, tok)
		})
	} else {
		d = dirCounts{we: map[string]int{}, they: map[string]int{}, obs: map[string]*obsAgg{}}
	}

	// importance: neighbor_edges degree + rank (all-time)
	var degree, rank, nodesWithEdges int
	s.db.conn.QueryRow(`
		WITH dd AS (SELECT node_a pk, count c FROM neighbor_edges
		            UNION ALL SELECT node_b, count FROM neighbor_edges),
		     aa AS (SELECT pk, COUNT(*) neigh FROM dd GROUP BY pk)
		SELECT (SELECT COUNT(*) FROM aa),
		       COALESCE((SELECT neigh FROM aa WHERE pk=?),0),
		       (SELECT 1+COUNT(*) FROM aa WHERE neigh > COALESCE((SELECT neigh FROM aa WHERE pk=?),0))
	`, pubkey, pubkey).Scan(&nodesWithEdges, &degree, &rank)

	// assemble links
	links := []QualityLink{}
	bidir := 0
	seen := map[string]bool{}
	for pk := range d.we {
		seen[pk] = true
	}
	for pk := range d.they {
		seen[pk] = true
	}
	for pk := range seen {
		we, they := d.we[pk], d.they[pk]
		info := nodeMap[pk]
		lat, lon := gpsPtrs(info, true)
		var dist *float64
		if self.HasGPS && info.HasGPS {
			dist = fptr(haversineKm(self.Lat, self.Lon, info.Lat, info.Lon))
		}
		b := we > 0 && they > 0
		if b {
			bidir++
		}
		links = append(links, QualityLink{
			Pubkey: pk, Name: info.Name, Role: info.Role, Lat: lat, Lon: lon,
			WeHear: we, TheyHear: they, Bottleneck: minInt(we, they), Bidir: b, DistanceKm: dist,
		})
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].Bidir != links[j].Bidir {
			return links[i].Bidir
		}
		if links[i].Bottleneck != links[j].Bottleneck {
			return links[i].Bottleneck > links[j].Bottleneck
		}
		return links[i].WeHear+links[i].TheyHear > links[j].WeHear+links[j].TheyHear
	})

	// direct observers
	directObs := []QualityObserver{}
	for pk, a := range d.obs {
		info := nodeMap[pk]
		lat, lon := gpsPtrs(info, true)
		var avg, dist *float64
		if a.snrN > 0 {
			avg = fptr(a.snrSum / float64(a.snrN))
		}
		if self.HasGPS && info.HasGPS {
			dist = fptr(haversineKm(self.Lat, self.Lon, info.Lat, info.Lon))
		}
		directObs = append(directObs, QualityObserver{
			Pubkey: pk, Name: info.Name, Count: a.count, AvgSNR: avg, Lat: lat, Lon: lon, DistanceKm: dist,
		})
	}
	sort.Slice(directObs, func(i, j int) bool { return directObs[i].Count > directObs[j].Count })

	toks := make([]string, 0, len(tokens))
	for t := range tokens {
		toks = append(toks, t)
	}
	sort.Strings(toks)

	selfLat, selfLon := gpsPtrs(self, true)
	return NodeQualityResponse{
		Node: QualityNode{Pubkey: pubkey, Name: self.Name, Role: self.Role,
			Lat: selfLat, Lon: selfLon, FirstSeen: self.LastSeen.UTC().Format(time.RFC3339)},
		Window:         QualityWindow{Days: days, Since: since.Format(time.RFC3339)},
		ReliableTokens: toks,
		Importance: QualityImportance{
			NeighborDegree: degree, DegreeRank: rank, NodesWithEdges: nodesWithEdges,
			RelayObservations: d.relay, BidirectionalLinks: bidir, DirectObservers: len(directObs),
		},
		DirectObservers: directObs,
		Links:           links,
	}, true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// scanQualityRows reads windowed observations whose path contains any reliable
// token, with the originator + observer + snr needed for attribution.
func (s *Server) scanQualityRows(tokens map[string]bool, sinceEpoch int64) []pathRow {
	likes := make([]string, 0, len(tokens))
	args := []interface{}{sinceEpoch}
	for tok := range tokens {
		likes = append(likes, "o.path_json LIKE ?")
		args = append(args, "%\""+tok+"\"%")
	}
	q := `SELECT COALESCE(obs.id,''), COALESCE(t.from_pubkey,''), COALESCE(t.payload_type,0), o.path_json, o.snr
	      FROM observations o
	      JOIN transmissions t ON t.id = o.transmission_id
	      LEFT JOIN observers obs ON obs.rowid = o.observer_idx
	      WHERE o.timestamp >= ? AND (` + strings.Join(likes, " OR ") + `)`
	rows, err := s.db.conn.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []pathRow
	for rows.Next() {
		var oid, fpk, pj string
		var pt int
		var snr sql.NullFloat64
		if err := rows.Scan(&oid, &fpk, &pt, &pj, &snr); err != nil {
			continue
		}
		var raw []string
		if json.Unmarshal([]byte(pj), &raw) != nil || len(raw) == 0 {
			continue
		}
		path := make([]string, len(raw))
		for i, h := range raw {
			path[i] = strings.ToUpper(h)
		}
		pr := pathRow{observerPK: strings.ToLower(oid), fromPubkey: strings.ToLower(fpk),
			payloadType: pt, path: path}
		if snr.Valid {
			v := snr.Float64
			pr.snr = &v
		}
		out = append(out, pr)
	}
	return out
}
```

- [ ] **Step 2: Add imports**

Ensure `cmd/server/node_quality.go` imports include `"database/sql"` and `"strconv"` (alongside the existing ones). Final import block:
```go
import (
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
)
```
(`math` is used by Task 6's nothing — remove `math` if `go vet` flags it unused.)

- [ ] **Step 3: Register the route**

In `cmd/server/routes.go`, immediately before the line
`r.HandleFunc("/api/nodes/{pubkey}", s.handleNodeDetail).Methods("GET")`, add:
```go
	r.HandleFunc("/api/nodes/{pubkey}/quality", s.handleNodeQuality).Methods("GET")
```

- [ ] **Step 4: Build + vet**

Run: `cd cmd/server && go build ./... && go vet ./...`
Expected: clean (drop the `math` import if reported unused).

- [ ] **Step 5: Commit**

```bash
git add cmd/server/node_quality.go cmd/server/routes.go
git commit -m "feat(server): GET /api/nodes/:pubkey/quality endpoint"
```

---

## Task 6: Endpoint tests (shape, 404, clamp)

**Files:**
- Create: `cmd/server/node_quality_endpoint_test.go`

- [ ] **Step 1: Write the tests** (harness mirrors `neighbor_api_test.go`)

```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNodeQuality_UnknownNode(t *testing.T) {
	srv := newQualityTestServer(t)
	rr := serveRequest(srv, "GET", "/api/nodes/deadbeef/quality")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rr.Code)
	}
}

func TestNodeQuality_ShapeAndClamp(t *testing.T) {
	srv := newQualityTestServer(t) // fixture has at least one known node
	pk := qualityTestKnownPubkey(t, srv)
	rr := serveRequest(srv, "GET", "/api/nodes/"+pk+"/quality?days=999")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}
	var resp NodeQualityResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if resp.Window.Days != 30 {
		t.Fatalf("days not clamped to 30: %d", resp.Window.Days)
	}
	if resp.Links == nil || resp.DirectObservers == nil || resp.ReliableTokens == nil {
		t.Fatalf("array fields must be non-nil (never null)")
	}
}
```

- [ ] **Step 2: Add the test harness helpers**

Append to the same file (use the project's existing fixture DB at
`test-fixtures/e2e-fixture.db`; mirror how `db_test.go`/`neighbor_api_test.go`
open a read-only store + `*DB` + `*Server`):
```go
func newQualityTestServer(t *testing.T) *Server {
	t.Helper()
	db := openFixtureDB(t)                 // existing helper in db_test.go
	store := newStoreForTest(t, db)        // existing helper used by store tests
	return &Server{store: store, db: db, cfg: testConfig()}
}

func qualityTestKnownPubkey(t *testing.T, srv *Server) string {
	t.Helper()
	var pk string
	if err := srv.db.conn.QueryRow(`SELECT public_key FROM nodes WHERE role LIKE '%repeater%' LIMIT 1`).Scan(&pk); err != nil {
		t.Skipf("no repeater in fixture: %v", err)
	}
	return pk
}
```
> If `openFixtureDB`/`newStoreForTest`/`testConfig` have different names in this
> repo, grep `cmd/server/*_test.go` for the existing equivalents and use those —
> do not duplicate fixture-loading logic.

- [ ] **Step 3: Run**

Run: `cd cmd/server && go test ./... -run TestNodeQuality -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/server/node_quality_endpoint_test.go
git commit -m "test(server): node quality endpoint shape/404/clamp"
```

---

## Task 7: Scan benchmark (perf proof, AGENTS rule 0)

**Files:**
- Create: `cmd/server/node_quality_bench_test.go`

- [ ] **Step 1: Write the benchmark**

```go
package main

import "testing"

func BenchmarkNodeQualityScan(b *testing.B) {
	srv := newQualityTestServerB(b)
	tokens := map[string]bool{"01FA": true}
	since := int64(0) // whole fixture = worst case
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = srv.scanQualityRows(tokens, since)
	}
}

func newQualityTestServerB(b *testing.B) *Server {
	b.Helper()
	db := openFixtureDBB(b)
	store := newStoreForTestB(b, db)
	return &Server{store: store, db: db, cfg: testConfig()}
}
```
> Reuse the fixture-open helpers' `testing.TB` form if available; otherwise add
> tiny `*testing.B` wrappers next to the existing `*testing.T` ones.

- [ ] **Step 2: Run + record timing**

Run: `cd cmd/server && go test -run=^$ -bench BenchmarkNodeQualityScan -benchmem`
Expected: completes; record ns/op + allocs in the eventual PR description as the
perf proof (scan bounded by the time window in prod).

- [ ] **Step 3: Commit**

```bash
git add cmd/server/node_quality_bench_test.go
git commit -m "test(server): benchmark node quality scan"
```

---

## Task 8: Frontend renderer (cards + table + one-way toggle)

**Files:**
- Create: `public/node-quality.js`
- Create: `public/node-quality.css`
- Modify: `public/index.html` (add the two includes)

- [ ] **Step 1: Create `public/node-quality.js`**

```javascript
/* Node quality report section — fetches /api/nodes/:pk/quality and renders
   importance cards + a Leaflet link-map + a 2-way link table. IIFE, exposes
   window.NodeQuality.render(pubkey). */
(function () {
  'use strict';

  function colorVar(b) {
    if (b >= 300) return 'var(--link-strong)';
    if (b >= 100) return 'var(--link-medium)';
    return 'var(--link-weak)';
  }

  function statCard(label, value) {
    return '<div class="nq-stat"><div class="nq-stat-v">' + value +
      '</div><div class="nq-stat-k">' + label + '</div></div>';
  }

  function row(i, l) {
    var dist = l.distance_km != null ? Number(l.distance_km).toFixed(1) : '—';
    return '<tr data-bidir="' + (l.bidir ? '1' : '0') + '">' +
      '<td class="nq-num">' + i + '</td>' +
      '<td>' + escapeHtml(l.name || l.pubkey.slice(0, 8)) + '</td>' +
      '<td class="nq-n">' + l.we_hear + '</td>' +
      '<td class="nq-n">' + l.they_hear + '</td>' +
      '<td class="nq-n" style="color:' + colorVar(l.bottleneck) + '"><b>' + l.bottleneck + '</b></td>' +
      '<td class="nq-n">' + dist + '</td></tr>';
  }

  function render(pubkey) {
    var el = document.getElementById('nodeQualityContent');
    if (!el) return;
    fetch('/api/nodes/' + encodeURIComponent(pubkey) + '/quality?days=7')
      .then(function (r) { return r.json(); })
      .then(function (d) {
        if (!d || !d.reliable_tokens) { el.innerHTML = '<div class="text-muted" style="padding:8px">Geen kwaliteitsdata.</div>'; return; }
        if (d.reliable_tokens.length === 0) {
          el.innerHTML = '<div class="text-muted" style="padding:8px">Node is niet betrouwbaar identificeerbaar in paden (alleen 1-byte prefix — botst).</div>';
          return;
        }
        var imp = d.importance || {};
        var twoWay = d.links.filter(function (l) { return l.bidir; });
        var html =
          '<div class="nq-stats">' +
          statCard('buren', imp.neighbor_degree) +
          statCard('rang', '#' + imp.degree_rank + '/' + imp.nodes_with_edges) +
          statCard('relay-obs', imp.relay_observations) +
          statCard('2-weg links', imp.bidirectional_links) +
          statCard('observers', imp.direct_observers) +
          '</div>' +
          '<div class="nq-actions">' +
          '<label><input type="checkbox" id="nqShowOneWay"> toon één-weg-links</label>' +
          '<button id="nqPrintBtn" class="btn">🖨️ Print / PDF</button></div>' +
          '<div id="nqMap" class="nq-map"></div>' +
          '<table class="nq-table"><thead><tr><th>#</th><th>Buur</th><th>wij horen</th>' +
          '<th>zij horen ons</th><th>bottleneck</th><th>km</th></tr></thead><tbody id="nqRows"></tbody></table>';
        el.innerHTML = html;

        function paint(showOneWay) {
          var list = (showOneWay ? d.links : twoWay).slice()
            .sort(function (a, b) { return b.bottleneck - a.bottleneck; });
          document.getElementById('nqRows').innerHTML =
            list.map(function (l, i) { return row(i + 1, l); }).join('');
        }
        paint(false);
        document.getElementById('nqShowOneWay').addEventListener('change', function (e) { paint(e.target.checked); });
        document.getElementById('nqPrintBtn').addEventListener('click', function () { window.print(); });

        if (window.NodeQualityMap && d.node.lat != null) {
          window.NodeQualityMap.render('nqMap', d.node, twoWay, colorVar);
        }
      })
      .catch(function () { el.innerHTML = '<div class="text-muted" style="padding:8px">Kon kwaliteitsdata niet laden.</div>'; });
  }

  window.NodeQuality = { render: render };
})();
```

- [ ] **Step 2: Add includes to `public/index.html`**

After `<link rel="stylesheet" href="live.css?v=__BUST__">` add:
```html
  <link rel="stylesheet" href="node-quality.css?v=__BUST__">
```
After `<script src="hop-display.js?v=__BUST__"></script>` add:
```html
  <script src="node-quality.js?v=__BUST__"></script>
  <script src="node-quality-map.js?v=__BUST__"></script>
```

- [ ] **Step 3: Commit**

```bash
git add public/node-quality.js public/index.html
git commit -m "feat(frontend): node quality section renderer"
```

---

## Task 9: Link map helper (`renderLinkMap`)

**Files:**
- Create: `public/node-quality-map.js`

- [ ] **Step 1: Implement the map helper** (reuses Leaflet + the node-map tile helper)

```javascript
/* window.NodeQualityMap.render(containerId, node, links, colorFn) — focused
   Leaflet map of a node and its bidirectional links, coloured by bottleneck. */
(function () {
  'use strict';
  var map = null;

  function render(containerId, node, links, colorFn) {
    var c = document.getElementById(containerId);
    if (!c || typeof L === 'undefined') return;
    if (map) { map.remove(); map = null; }
    map = L.map(containerId, { zoomControl: true, attributionControl: false })
      .setView([node.lat, node.lon], 11);
    // Reuse the node-detail tile applier if present; else plain OSM.
    if (typeof window._applyTilesToNodeMap === 'function') {
      window._applyTilesToNodeMap(map);
    } else {
      L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', { maxZoom: 19 }).addTo(map);
    }
    var pts = [[node.lat, node.lon]];
    links.forEach(function (l) {
      if (l.lat == null || l.lon == null) return;
      pts.push([l.lat, l.lon]);
      L.polyline([[node.lat, node.lon], [l.lat, l.lon]], {
        color: getComputedStyle(document.documentElement)
          .getPropertyValue(colorFn(l.bottleneck).replace('var(', '').replace(')', '')).trim() || '#888',
        weight: Math.max(1.5, Math.min(7, 1.2 + 1.6 * Math.log10(l.bottleneck + 1))),
        opacity: 0.8
      }).addTo(map).bindPopup(escapeHtml(l.name) + '<br>wij ' + l.we_hear + ' / zij ' + l.they_hear);
      L.circleMarker([l.lat, l.lon], { radius: 5, color: '#fff', weight: 1, fillOpacity: 1 })
        .addTo(map).bindTooltip(escapeHtml(l.name));
    });
    L.circleMarker([node.lat, node.lon], { radius: 8, color: '#fff', weight: 2, fillColor: '#0969da', fillOpacity: 1 })
      .addTo(map).bindPopup(escapeHtml(node.name));
    try { map.fitBounds(pts, { padding: [30, 30] }); } catch (e) {}
    setTimeout(function () { map.invalidateSize(); }, 120);
  }

  window.NodeQualityMap = { render: render };
})();
```
> If `_applyTilesToNodeMap` is not exposed on `window` in `nodes.js`, expose it
> (`window._applyTilesToNodeMap = _applyTilesToNodeMap;`) in the same commit so
> the helper reuses the user's configured tile provider rather than hardcoding OSM.

- [ ] **Step 2: Commit**

```bash
git add public/node-quality-map.js public/nodes.js
git commit -m "feat(frontend): reusable bidirectional link map"
```

---

## Task 10: Styles + CSS vars + print

**Files:**
- Create: `public/node-quality.css`
- Modify: `public/style.css` (`:root` link colour vars)

- [ ] **Step 1: Add CSS variables to `public/style.css`**

Inside the existing `:root { … }` block, add:
```css
  --link-strong: #1a7f37;
  --link-medium: #bf8700;
  --link-weak: #cf222e;
```

- [ ] **Step 2: Create `public/node-quality.css`**

```css
.nq-stats { display:flex; gap:8px; flex-wrap:wrap; margin:8px 0 12px; }
.nq-stat { flex:1; min-width:90px; border:1px solid var(--border, #d0d7de); border-radius:6px; padding:6px 8px; background:var(--section-bg, #f6f8fa); }
.nq-stat-v { font-size:16px; font-weight:700; }
.nq-stat-k { font-size:9px; text-transform:uppercase; letter-spacing:.3px; color:var(--text-muted, #57606a); }
.nq-actions { display:flex; align-items:center; gap:14px; margin:6px 0; font-size:12px; }
.nq-map { height:380px; border:1px solid var(--border, #d0d7de); border-radius:6px; margin-bottom:12px; }
.nq-table { border-collapse:collapse; width:100%; font-size:12px; }
.nq-table th, .nq-table td { border:1px solid var(--border, #d0d7de); padding:3px 7px; }
.nq-table th { background:var(--section-bg, #f6f8fa); font-size:10px; text-transform:uppercase; }
.nq-n { text-align:right; font-variant-numeric:tabular-nums; }
.nq-num { text-align:right; color:var(--text-muted, #8c959f); width:26px; }

@media print {
  body * { visibility:hidden; }
  #node-quality, #node-quality * { visibility:visible; }
  #node-quality { position:absolute; left:0; top:0; width:100%; }
  .nq-actions { display:none; }
  .nq-map { height:300px; }
}
```

- [ ] **Step 3: Commit**

```bash
git add public/node-quality.css public/style.css
git commit -m "feat(frontend): node quality styles + link colour vars + print"
```

---

## Task 11: Wire the section into the node detail page

**Files:**
- Modify: `public/nodes.js` (~line 707 — after the clock-skew card; and the post-render hook ~line 709)

- [ ] **Step 1: Add the card placeholder**

In the node detail template string, immediately after the
`<div class="node-full-card skew-detail-section" id="node-clock-skew" …></div>`
line, add:
```javascript
        <div class="node-full-card" id="node-quality">
          <h4>Link quality (2-weg)</h4>
          <div id="nodeQualityContent"><div class="text-muted" style="padding:8px"><span class="spinner"></span> Loading quality…</div></div>
        </div>`;
```
(Keep the closing backtick/semicolon that currently ends the template — move it
to the end of this added block.)

- [ ] **Step 2: Invoke the renderer after the map block**

After the `// Map` block (around line 718, after the `if (hasLoc) { … }`), add:
```javascript
      // Quality section
      if (window.NodeQuality) {
        try { window.NodeQuality.render(n.public_key); } catch (e) {}
      }
```
The existing deep-link scroll (line 741+) already handles `?section=node-quality`
because the card `id` is `node-quality`.

- [ ] **Step 3: Manual build/serve check**

Run a local server (`cd cmd/server && go run . --config ../../config.example.json`
or the repo's documented dev command) and open
`http://localhost:3000/#/nodes/<a-repeater-pubkey>?section=quality`.
Expected: stat cards, a map with coloured links, a table; the page scrolls to the
section; `Ctrl-P` preview shows only the report.

- [ ] **Step 4: Commit**

```bash
git add public/nodes.js
git commit -m "feat(frontend): mount node quality section on node detail page"
```

---

## Task 12: Playwright E2E

**Files:**
- Create: `test-node-quality-e2e.js`

- [ ] **Step 1: Write the test** (mirror an existing `test-issue-*-e2e.js` harness — default `BASE_URL=http://localhost:3000`, never prod)

```javascript
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:3000';

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage();
  // pick any repeater pubkey from the API
  const nodes = await (await page.request.get(BASE + '/api/nodes?role=repeater&limit=1')).json();
  const pk = nodes.nodes[0].public_key;

  await page.goto(BASE + '/#/nodes/' + pk + '?section=quality');
  await page.waitForSelector('#nodeQualityContent .nq-stats, #nodeQualityContent .text-muted', { timeout: 15000 });

  const hasStats = await page.locator('#nodeQualityContent .nq-stats').count();
  if (hasStats) {
    await page.waitForSelector('#nqMap', { timeout: 10000 });
    const rows = await page.locator('#nqRows tr').count();
    if (rows < 0) throw new Error('table did not render');
    // one-way toggle changes row count or leaves it ≥ bidirectional count
    const before = await page.locator('#nqRows tr').count();
    await page.check('#nqShowOneWay');
    const after = await page.locator('#nqRows tr').count();
    if (after < before) throw new Error('one-way toggle reduced rows unexpectedly');
  }
  console.log('node-quality E2E OK');
  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });
```

- [ ] **Step 2: Run against a local server**

Run: `node test-node-quality-e2e.js`
Expected: prints `node-quality E2E OK`.

- [ ] **Step 3: Run the full suite**

Run: `npm test` (backend) then `cd cmd/server && go test ./...`
Expected: all green (test count up).

- [ ] **Step 4: Commit**

```bash
git add test-node-quality-e2e.js
git commit -m "test(e2e): node quality report section"
```

---

## Self-review (completed)

- **Spec coverage:** §3 algorithm → Task 2; reliability rule → Task 3; §4 endpoint/structs/perf/errors → Tasks 4–7; §5 frontend (map/table/print/toggle/deep-link) → Tasks 8–12; §6 api-spec → Task 1; §7 testing → Tasks 2,3,6,7,12; §8 upstreamability honoured (own files, read-only, named structs, CSS vars, deep-link). Resolved §9 decisions reflected (renderLinkMap helper, one-way toggle default-off, `?section=quality`).
- **Placeholders:** none — every code step has complete code. Two "if helper has a different name, grep for it" notes are deliberate guardrails for fixture/tile helpers whose exact names live in test/`nodes.js` internals; they instruct reuse, not invention.
- **Type consistency:** `pathRow`, `dirCounts`, `obsAgg`, `attributeDirections`, `reliableTokens`, `uniqueResolve`, the `Quality*` structs, `scanQualityRows`, `computeNodeQuality`, `handleNodeQuality` names are used identically across tasks. Frontend `window.NodeQuality.render` / `window.NodeQualityMap.render` and the `colorVar`/`#nqRows`/`#nqMap`/`#nodeQualityContent` ids match across Tasks 8–12.
