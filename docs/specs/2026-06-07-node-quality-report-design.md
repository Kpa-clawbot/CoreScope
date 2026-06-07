# Node Quality Report — Design Spec

**Date:** 2026-06-07
**Status:** Draft for review
**Author:** efite (+ Claude)
**Upstream goal:** Build in the fork, fully AGENTS.md-compliant, so it can later be
PR'd to `Kpa-clawbot/CoreScope` (upstream) without rework. Keep it decoupled from
the fork-private matomo/locode/security customizations.

---

## 1. Goal

Give any node a **quality report**: a map of the node and the neighbours it has a
**stable two-way (bidirectional) RF link** with, plus a table of those links rated
by quality, plus a few importance stats. Reachable at a **direct, shareable URL**
and **printable to an offline PDF** (browser Save-as-PDF).

### Success criteria
- A user opens `…/#/nodes/<pubkey>?section=quality`, sees a map with the node +
  its bidirectional neighbours (links coloured by quality) and a table below.
- `Ctrl-P` / Save-as-PDF produces a clean one/two-page offline report.
- The directional/quality numbers match the validated `analysis-output/` toolkit.
- No write to the DB; bounded, cached query; tests + api-spec updated.

### Non-goals (YAGNI)
- No server-side PDF rendering engine (Go PDF / headless browser). Browser print
  covers offline. A `/quality.pdf` endpoint is a possible *later* follow-up.
- No change to the global `neighbor_edges` builder or DB schema (that's the heavier
  "approach C" — explicitly deferred).
- No new npm deps, no build step, no framework.

---

## 2. Approach

**Approach B — on-demand per-node endpoint + frontend report section.**

The existing neighbour feature (`/api/neighbors/graph`, `/api/nodes/:pubkey/neighbors`,
the Leaflet map, spider-fan viz) is built on `neighbor_edges`, which is
**canonical-merged (node_a ≤ node_b)** and therefore directionless — the
`GraphEdge.Bidirectional` field is currently hardcoded `true` (placeholder). This
feature computes **real direction** on demand from raw `observations.path_json`,
for a single node, bounded to a recent window. It does not touch the global builder,
so it is read-only and self-contained (maximally upstreamable).

Rejected alternatives:
- **A (standalone CLI/script):** the validated `analysis-output/` toolkit. Great for
  ad-hoc use but no in-app URL, not what the user wants long-term.
- **C (directional `neighbor_edges`):** teach `cmd/ingestor/neighbor_builder.go` to
  track direction + add columns. More valuable app-wide (fills the placeholder
  everywhere) but touches the `dbschema` source-of-truth invariant (#1321), the
  ingestor, and migrations — much larger review surface. Deferred as a future
  follow-up that upstream can own.

---

## 3. Directionality method (the core algorithm)

A flood path is recorded origin → observer; each repeater appends its own hash
hop. So in a path `[A, B]`, **B received A directly** (B is one RF hop downstream
of A). For the target node N (matched by its reliable token):

| Position of N in path | Meaning | Counts toward |
|---|---|---|
| token **before** N (`[X, N]`) | we received X directly | `we_hear[X] += 1` |
| token **after** N (`[N, X]`) | X received us directly | `they_hear[X] += 1` |
| N is **last** hop | the **observer** heard us directly | `they_hear[observer] += 1` (+SNR) |
| N is **first** hop of an ADVERT | we were first relay of the originator's advert ⇒ we heard it | `we_hear[from_pubkey] += 1` |

- A **link is bidirectional/stable** when both `we_hear>0` and `they_hear>0`.
- **bottleneck = min(we_hear, they_hear)** — the weaker direction rates real
  two-way reliability and drives link colour/width.

### Reliability rule
A node (target or neighbour) is only identified from a path hop when its pubkey
**prefix is unique** at that hop's byte length, across all known pubkeys
(nodes + inactive_nodes + observers). 1-byte prefixes almost always collide and
are auto-excluded; only unique 2–3 byte hops are trusted. The target's reliable
tokens are derived automatically; ambiguous neighbour hops are skipped (never
misattributed — conservative undercount).

This is the exact, validated logic of `analysis-output/analyze_node.py`.

---

## 4. Backend

### 4.1 New endpoint
`GET /api/nodes/:pubkey/quality?days=7`

- `days` — lookback window, default 7, clamped 1–30 (perf bound; "recent quality"
  is also more meaningful than all-time).
- Read-only (`mode=ro`), per #1283. No writes.

### 4.2 Response (named structs in `cmd/server/node_quality.go`)
```go
type NodeQualityResponse struct {
    Node            QualityNode        `json:"node"`
    Window          QualityWindow      `json:"window"`
    ReliableTokens  []string           `json:"reliable_tokens"`
    Importance      QualityImportance  `json:"importance"`
    DirectObservers []QualityObserver  `json:"direct_observers"`
    Links           []QualityLink      `json:"links"`
}
type QualityNode struct { Pubkey, Name, Role string; Lat, Lon *float64; FirstSeen string }
type QualityWindow struct { Days int `json:"days"`; Since string `json:"since"` }
type QualityImportance struct {
    NeighborDegree, DegreeRank, NodesWithEdges int
    RelayObservations, BidirectionalLinks, DirectObservers int
}
type QualityLink struct {
    Pubkey, Name, Role string
    Lat, Lon   *float64
    WeHear     int      `json:"we_hear"`
    TheyHear   int      `json:"they_hear"`
    Bottleneck int      `json:"bottleneck"`
    Bidir      bool     `json:"bidir"`
    DistanceKm *float64 `json:"distance_km,omitempty"`
}
type QualityObserver struct { Pubkey, Name string; Count int; AvgSNR, Lat, Lon, DistanceKm *float64 }
```
No `map[string]interface{}` anywhere (#1383).

### 4.3 Computation & reuse
- Resolve `:pubkey`, derive reliable tokens (unique-prefix check).
- Build a **prefix→pubkey unique index** + a pubkey→info map. Reuse
  `buildNodeInfoMap()` (already merges observers) for names/roles/GPS and
  `haversineKm()` for distance — both already in `cmd/server/neighbor_api.go`.
- Query, bounded by the **timestamp index**:
  `SELECT obs.id, t.from_pubkey, t.payload_type, o.path_json, o.snr
   FROM observations o JOIN transmissions t ON t.id=o.transmission_id
   LEFT JOIN observers obs ON obs.rowid=o.observer_idx
   WHERE o.timestamp >= ? AND (o.path_json LIKE '%"TOK"%' OR …)`
- Run the §3 attribution as a **pure function**
  `attributeDirections(rows, ourTokens, resolve) -> (we, they, observers, relayCount)`
  so it is unit-testable in isolation (DI per AGENTS Testability).
- Importance fields:
  - `neighbor_degree` + `degree_rank` + `nodes_with_edges` from the existing
    `neighbor_edges` degree CTE (already validated). **These are all-time** (the
    edge table is not windowed); the directional `links` are windowed. This mix is
    intentional — degree/rank express standing connectivity, the links express
    *current* two-way quality. Stated in the UI so it isn't misread.
  - `relay_observations` = count of windowed observations in which any reliable
    token of the node appears **anywhere** in the path (its relay throughput).
  - `bidirectional_links` / `direct_observers` = derived counts from the scan.

### 4.4 Performance (AGENTS rule 0 — must justify)
- The leading-wildcard `LIKE` cannot use an index, so cost = rows in the
  **timestamp window** only. Measured: 156k obs (≈1.5 d) scanned + filtered in
  ~0.6 s; a 7-day window is a few × that worst-case.
- **Bound:** `days` clamped ≤30. **Cache** the JSON per `(pubkey, days)` with a TTL
  (≈300 s) via the existing server cache layer, keyed like other analytics.
- Ship a Go benchmark with realistic fixture sizes (before/after timings) per the
  "no proof = no merge" rule.

### 4.5 Errors
- Unknown pubkey → `404 {"error":"Not found"}`.
- `days` out of range → clamp (no error).
- Node with no unique 1–3 byte prefix → `200` with `reliable_tokens: []` and empty
  `links`/`direct_observers`. The UI detects `reliable_tokens.length === 0` and
  shows an explanatory empty-state ("node niet betrouwbaar identificeerbaar in
  paden — alleen 1-byte prefix, botst") rather than a blank report. No extra
  response field needed.

---

## 5. Frontend

### 5.1 Route / deep-link
New section on the existing node detail page: `#/nodes/<pubkey>?section=quality`
(matches the existing `section=node-neighbors` pattern; satisfies AGENTS deep-link
rule). **This hash URL is the shareable "direct URL to the report."**

### 5.2 Layout
- **Importance header**: small stat cards (degree, rank, relay obs, #2-way links,
  #direct observers).
- **Map**: a focused Leaflet map (reuse `map.js` tile config + helpers) showing the
  node (highlighted) and only its **bidirectional** neighbours, links coloured by
  bottleneck. Reuse existing marker/role styling.
- **Table**: the 2-way links sorted by bottleneck — columns: buur, wij horen,
  zij horen ons, bottleneck, afstand. A toggle to also show one-way links
  (default off) for diagnosing asymmetry.

### 5.3 Print / PDF
A print stylesheet (`@media print`) lays out header → map → table on A4 and hides
nav/chrome. A "Print / PDF" button calls `window.print()`. Offline PDF via the
browser's Save-as-PDF.

### 5.4 Conventions
- Link colours via **CSS variables** (`--link-strong/--link-medium/--link-weak`)
  in `style.css`, mapped in the customizer (AGENTS rule 8 — note as later milestone,
  OK to ship with sane defaults).
- Bulk fetch (one call to the new endpoint), filter/sort client-side — no per-item
  calls.
- Code in its own file (e.g. `public/node-quality.js`) or a clearly-bounded section
  module, not entangled with matomo/locode.

---

## 6. API contract
Add the `GET /api/nodes/:pubkey/quality` section to `docs/api-spec.md` **before**
building the UI (contract is authoritative; AGENTS rule 4).

---

## 7. Testing
- **Go unit** (`cmd/server/node_quality_test.go`): drive `attributeDirections`
  with synthetic path arrays covering predecessor/successor/last-hop/first-hop-advert,
  ambiguous-token skipping, and the unique-prefix derivation. Test-first.
- **Go endpoint test**: shape + 404 + clamp, against the test fixture DB.
- **Go benchmark**: scan timing on a realistic window (perf proof).
- **Playwright** (`test-node-quality-*.js`): section loads, map renders bidirectional
  links, table populates, print layout sane, deep-link round-trips.
- `npm test` + local E2E green before any push.

---

## 8. Upstreamability checklist
- [ ] Self-contained files (`cmd/server/node_quality.go`, `public/node-quality.js`),
      no matomo/locode coupling.
- [ ] Read-only (#1283), named structs (#1383), CSS vars, no npm deps.
- [ ] `docs/api-spec.md` updated.
- [ ] Tests + benchmark + browser validation.
- [ ] Deep-linked hash route.
- [ ] Later, when desired: branch off `upstream/master`, cherry-pick the feature
      commits, open a PR per AGENTS.md (clean markdown body, perf proof).

---

## 9. Resolved decisions
1. **Map base** — extract a minimal reusable `renderLinkMap(container, node, links)`
   helper (Leaflet tile config/markers reused from `map.js`); do not duplicate the
   whole map module nor force the full `map.js` instance.
2. **One-way links** — shown in the table behind a toggle (default **off**), so the
   default report is the bidirectional set but asymmetry stays diagnosable.
3. **Placement** — ~~section on the node detail page~~ → **superseded** (see §10):
   a dedicated full-page view at `#/nodes/<pubkey>/quality` with a top "Quality"
   button next to Analytics.

---

## 10. Revisions (post-review, 2026-06-07)

> **Feature renamed Quality → Reach.** "Quality" didn't capture the page (it's
> about a node's two-way RF *reach*). Endpoint `/api/nodes/:pubkey/quality` →
> `/reach`; files `node_quality.go` → `node_reach.go`, `node-quality*.{js,css}` →
> `node-reach*`; route `#/nodes/<pk>/quality` → `/reach`; Go types `Quality*` →
> `NodeReach*` (the bare `Reach*` namespace was already taken by the topology
> per-observer-reach feature). Earlier mentions of "quality" in this doc reflect
> the original name.

After the first staging deploy, review feedback reshaped the frontend:

- **Standalone page, not a buried section.** Registered as page `node-quality`
  (route `#/nodes/<pubkey>/quality`), mirroring `node-analytics.js`, with a
  📈 Quality button next to Analytics on the node detail (full + side pane). The
  inline far-down section was removed. (Reverses §9.3.)
- **English UI** to match the rest of the app.
- **Day selector** (24h / 7d / 14d / 30d) re-fetching the endpoint; default 7d.
- **Importance grouped + explained**: "Network position (all-time)" (Neighbours,
  Rank) vs "Last N days" (Links, Two-way, Relay obs, Direct observers), each card
  carrying a description — clarifies why all-time *Neighbours* (narrow,
  geo-filtered neighbour_edges) can be **less** than windowed *Two-way* (full
  path-adjacency, both directions).
- **Map fixes**: node shown as a default Leaflet pin (was an invisible white
  dot); neighbour dots filled with the link colour; print resizes the map to a
  fixed width + `invalidateSize()` so the whole map prints (was clipped to the
  left half on wide screens).
- **One-way toggle made visible**: a live "showing X of Y (Z two-way)" count,
  one-way rows muted with a direction hint.
- **Clickable neighbours**: each table row links to that node's detail page.
