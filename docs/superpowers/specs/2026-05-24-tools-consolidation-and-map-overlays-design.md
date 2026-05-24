# Tools Consolidation & Map Overlays — Design Spec

**Date:** 2026-05-24
**Status:** Approved
**Inspired by:** [meshcore-mqtt-live-map/dev](https://github.com/yellowcooln/meshcore-mqtt-live-map/tree/dev)

---

## Overview

Four coordinated changes to CoreScope:

1. **Tools Page Consolidation** — merge LOS, RF Coverage, and MC-Keygen into the existing `#/tools` sub-routing; remove their direct nav entries.
2. **Theming Fix** — correct CSS variable usage in `los.js` and `rf-coverage.js` so they respect dark mode.
3. **24-Hour Route History** — new backend API + Analytics tab + live-map overlay showing edge traffic volume over time.
4. **Map Overlays** — weather (radar + wind) and MeshMapper community coverage as toggleable layers on the live map (and area map for MeshMapper).

| # | Change | Backend | Frontend |
|---|--------|---------|----------|
| 1 | Tools consolidation | — | `app.js`, `nav-routes.js`, `main.go` |
| 2 | Theming fix | — | `los.js`, `rf-coverage.js` |
| 3 | Route History | `cmd/server/route_history.go` | `analytics.js`, `map.js` |
| 4 | Map Overlays | `cmd/server/meshmapper.go` | `map-overlays.js`, `map.js`, `area-map` |

---

## 1. Tools Page Consolidation

### Goal

All operator tools are reachable via `#/tools/*`. Direct nav entries for LOS, RF Coverage, and MC-Keygen are removed. The tools landing page becomes a proper hub.

### Routing

| URL | Module loaded |
|-----|---------------|
| `#/tools` | `tools-landing` (hub card grid) |
| `#/tools/path-inspector` | `path-inspector` |
| `#/tools/trace/<hash>` | `traces` |
| `#/tools/los` | `los` |
| `#/tools/rf-coverage` | `rf-coverage` |
| `#/tools/mc-keygen` | `mc-keygen` |

### `app.js` changes

**`tools-landing` page** — replace the current 2-card grid with a 5-card grid:

```
🔍 Path Inspector   — Resolve prefix paths to candidate pubkey routes
📡 Trace Viewer     — View detailed packet traces by hash
🔭 LOS Analyzer     — Check line-of-sight between two points
📡 RF Coverage      — Compute terrain-aware RF coverage polygon
🔑 MC-Keygen        — Generate MeshCore keypairs
```

Cards use CSS variables only (`--surface-1`, `--border`, `--text`, `--text-muted`, `--accent`). No hardcoded colors.

**Sub-routing** — extend the `basePage === 'tools'` block:

```javascript
if (routeParam === 'los')         { basePage = 'los'; routeParam = null; }
if (routeParam === 'rf-coverage') { basePage = 'rf-coverage'; routeParam = null; }
if (routeParam === 'mc-keygen')   { basePage = 'mc-keygen'; routeParam = null; }
```

**Active nav state** — extend the `data-route === 'tools'` active condition to include `'los'`, `'rf-coverage'`, `'mc-keygen'`.

### `nav-routes.js` changes

Remove entries: `mc-keygen`, `los`, `rf-coverage`.

### `main.go` changes

Remove desktop nav `<a>` tags for `mc-keygen`, `los`, `rf-coverage`.

---

## 2. Theming Fix — los.js & rf-coverage.js

### Problem

Both files use:
- Wrong token names: `--surface1` instead of `--surface-1`
- Hardcoded light-mode fallbacks: `var(--surface1, #fff)`, `var(--text,#1a1a2e)`
- No tile-layer theme switching (always loads OSM light tiles)

### Fix

**Inline styles:** Replace all `var(--token, hardcoded-value)` with `var(--token)`. Correct token names to match `style.css` (`--surface-1`, `--surface-2`, `--text`, `--text-muted`, `--border`, `--input-bg`, `--accent`).

**Tile-layer theme switching:** Both files init a Leaflet map with a single `L.tileLayer`. Replace with a `setMapTiles(map)` helper that:

1. Reads `document.documentElement.dataset.theme` (or `prefers-color-scheme` media query if no explicit theme is set) to determine current mode.
2. Uses OSM standard tiles for light mode, OSM dark (`https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png`) for dark mode — same tile sources already used in `map.js`.
3. Attaches a `MutationObserver` on `document.documentElement` watching `data-theme` attribute changes to swap tile layers live — mirrors the `_mapThemeObs` pattern in `map.js`.
4. Observer is stored and disconnected in `destroy()`.

---

## 3. 24-Hour Route History

### Backend — `cmd/server/route_history.go`

**Endpoint:** `GET /api/route-history?hours=N` (default 24, max 168, min 1)

**Query logic:**
1. Select all `transmissions` rows with `first_seen >= now - N hours` where `path_json` parses to an array of ≥ 2 hops.
2. For each transmission, walk consecutive hop pairs `(hops[i], hops[i+1])`. Normalize the pair by sorting pubkeys alphabetically so A→B and B→A merge.
3. For each normalized edge, accumulate: `count`, `last_seen` (max timestamp), `samples` (up to 5 distinct packet hashes).
4. Join to the `nodes` table to resolve `lat`, `lon`, `name` for each pubkey. Edges where either node has no GPS coordinates are excluded.
5. Return sorted by `count` descending.

**Response:**

```json
{
  "edges": [
    {
      "node_a": "aabbcc...",
      "node_b": "ddeeff...",
      "name_a": "Node Alpha",
      "name_b": "Node Beta",
      "lat_a": 52.1, "lon_a": 4.5,
      "lat_b": 52.3, "lon_b": 4.7,
      "count": 47,
      "last_seen": "2026-05-24T10:00:00Z",
      "samples": ["hash1", "hash2"]
    }
  ],
  "hours": 24,
  "total_edges": 12
}
```

**Error handling:** Invalid `hours` param → 400. DB error → 500 with `{"error": "..."}`.

**Performance:** The query touches `transmissions.first_seen` (already indexed) and `transmissions.path_json`. For large installs the JSON parsing happens in Go (not SQL), so the query fetches only `hash`, `path_json`, `first_seen` columns. Result is not cached (the endpoint is only called on user demand or overlay toggle).

**Tests (`cmd/server/route_history_test.go`):**
- Edge normalization: A→B and B→A collapse to one edge
- Edges with missing GPS excluded
- `hours` param validation (0 → 400, 200 → 400, 24 → 200)
- Integration: seed transmissions + nodes, verify edge count and `count` values

### Analytics page — new "Route History" tab

Added as the last tab in `analytics.js`. Tab label: **📈 Route History**.

**Tab contents:**

```
Time window: [6h] [12h] [24h] [48h] [7d]    [↻ Refresh]
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
[Leaflet map — full tab width, 400px tall]
  Edges drawn as colored/weighted polylines
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Total edges: N  |  Highest volume: Node A ↔ Node B (47 pkts)
```

**Edge rendering:**

- Color: count mapped through green→yellow→red gradient. Thresholds: ≥50 = `#22c55e`, 20–49 = `#84cc16`, 10–19 = `#eab308`, 3–9 = `#f97316`, 1–2 = `#ef4444`.
- Weight: `2 + Math.min(count / 10, 6)` pixels (range 2–8px).
- Click popup: node names, count, last seen, up to 5 sample hashes as links to `#/tools/trace/<hash>`.

Map uses the same tile-switching pattern as the theming fix (dark/light aware). Tab init fetches data once; Refresh button re-fetches.

### Live map overlay toggle

New **Overlays** `<fieldset>` added to the `map-controls` panel in `map.js`, positioned after the existing Filters section:

```html
<fieldset class="mc-section">
  <legend class="mc-label">Overlays</legend>
  <label><input type="checkbox" id="mcRouteHistory"> 📈 Route History</label>
  <label><input type="checkbox" id="mcRadar"> 🌧️ Radar</label>
  <label><input type="checkbox" id="mcWind"> 💨 Wind</label>
  <label><input type="checkbox" id="mcMeshMapper"> 📶 MeshMapper</label>
</fieldset>
```

When **Route History** is toggled on: fetches `/api/route-history?hours=24`, draws polylines on the live map using the same color/weight logic. Clearing/re-toggling removes and re-fetches. State saved to `localStorage('meshcore-overlay-route-history')`.

---

## 4. Map Overlays

### 4a. Shared module — `public/map-overlays.js`

IIFE exposing `window.MapOverlays` with methods:

```javascript
MapOverlays.initRouteHistory(map)     // draw route history edges
MapOverlays.destroyRouteHistory(map)
MapOverlays.initRadar(map)            // start radar tile layer + refresh timer
MapOverlays.destroyRadar(map)
MapOverlays.initWind(map)             // start wind arrow markers + refresh timer
MapOverlays.destroyWind(map)
MapOverlays.initMeshMapper(map)       // fetch + draw coverage rectangles
MapOverlays.destroyMeshMapper(map)
```

Each method is self-contained and idempotent. All `init*` methods return a cleanup function (also stored internally). `destroy*` tears down layers and cancels timers.

### 4b. Weather Overlay — Radar

**Data source:** RainViewer public API — `https://api.rainviewer.com/public/weather-maps.json` (no key required).

**Logic:**
1. Fetch the JSON to get the latest radar frame path.
2. Build tile URL: `https://tilecache.rainviewer.com{path}/256/{z}/{x}/{y}/2/1_1.png`
3. Add as `L.tileLayer` with `opacity: 0.5`.
4. Refresh every 10 minutes.

**Country-boundary clipping:**
- Fetch GeoJSON once from `https://raw.githubusercontent.com/datasets/geo-countries/master/data/countries.geojson`.
- If a country code is configured (read from `GET /api/config/geo-filter`, field `country`), filter to that country's polygon and render it as an `L.geoJSON` mask layer using a canvas renderer with `mix-blend-mode: destination-in` over the radar tile layer.
- If no country is configured, skip clipping.
- GeoJSON is cached in `sessionStorage` to avoid re-fetching on map reinit.

**Error states:** If the RainViewer fetch fails, the Radar checkbox label changes to "🌧️ Radar (unavailable)" and the checkbox is disabled.

### 4c. Weather Overlay — Wind

**Data source:** Open-Meteo API — `https://api.open-meteo.com/v1/forecast` (no key required).

**Logic:**
1. On toggle-on and on significant map move/zoom (debounced 1.5s), divide the viewport into a 5×5 grid of sample points.
2. Fetch windspeed (`windspeed_10m`) and direction (`winddirection_10m`) for all 25 points in one request using Open-Meteo's CSV latitude/longitude format.
3. For each point, render an `L.divIcon` containing an arrow SVG rotated to the wind direction, with speed (km/h) as a label below.
4. Arrow color: calm (<20 km/h) = `#22c55e`, moderate (20–50) = `#eab308`, strong (>50) = `#ef4444`.
5. Refresh every 15 minutes and on significant viewport change.

Markers are collected in a layer group; `destroyWind` removes the group entirely.

### 4d. MeshMapper Coverage Layer

**Backend — `cmd/server/meshmapper.go`**

**Config additions to `config.go`:**

```go
type MeshMapperConfig struct {
    APIUrl        string `json:"apiUrl"`
    APIKey        string `json:"apiKey"`
    CacheTTLSecs  int    `json:"cacheTTLSeconds"`
}
```

Added as `MeshMapper *MeshMapperConfig` field on `Config`. Env var overrides: `MESHMAPPER_API_URL`, `MESHMAPPER_API_KEY`, `MESHMAPPER_CACHE_TTL_SECONDS`. Defaults: URL = `https://meshmapper.net/api/coverage`, key = `""`, TTL = 300s.

**Endpoint:** `GET /api/coverage/meshmapper`

- If `apiKey` is empty → 503 `{"error":"meshmapper_not_configured"}`.
- If cached and TTL not expired → serve cached response.
- Otherwise fetch from MeshMapper API with `Authorization: Bearer <key>` header, cache result, serve.
- On fetch error → 502 `{"error":"meshmapper_unavailable"}`.

Cache is a single struct `{data []byte, fetchedAt time.Time}` protected by `sync.RWMutex`.

**Tests (`cmd/server/meshmapper_test.go`):**
- Missing API key → 503
- Mock server returning valid JSON → 200, subsequent call within TTL served from cache
- Mock server error → 502

**Frontend — `MapOverlays.initMeshMapper(map)`**

1. Fetch `/api/coverage/meshmapper`.
2. If 503, hide the MeshMapper checkbox and show a small note: *"MeshMapper key not configured"*.
3. On success, draw one `L.rectangle` per coverage square using `square.bounds` (`{south, west, north, east}`).

**Coverage type colors (data-semantic, not theme tokens):**

| Type | Fill color |
|------|-----------|
| BIDIR | `#22c55e` |
| DISC / TRACE | `#84cc16` |
| TX | `#eab308` |
| RX | `#f97316` |
| DEAD | `#ef4444` |
| DROP | `#6b7280` |

Fill opacity: 0.35. Border opacity: 0.6. Click popup: type, SNR, timestamp (human-readable), grid ID.

**Conflict resolution:** When squares overlap, higher-priority type wins (BIDIR > DISC > TX > RX > DEAD > DROP). Resolved in JS before rendering — lower-priority overlapping squares are skipped.

**Toggle placement:** MeshMapper checkbox in the **Overlays** fieldset in `map.js` (live map) and `area-map.html`. State saved to `localStorage('meshcore-overlay-meshmapper')`. The `MapOverlays` module is loaded via `<script defer src="map-overlays.js?v=__BUST__">` in `index.html`.

---

## File Map

| File | Action | Notes |
|------|--------|-------|
| `app.js` | Modify | Expand tools-landing, extend sub-routing, update active-state detection |
| `nav-routes.js` | Modify | Remove `mc-keygen`, `los`, `rf-coverage` entries |
| `cmd/server/main.go` | Modify | Remove mc-keygen, los, rf-coverage desktop nav links |
| `public/los.js` | Modify | Fix CSS var token names, add tile-layer theme switching |
| `public/rf-coverage.js` | Modify | Fix CSS var token names, add tile-layer theme switching |
| `public/map-overlays.js` | **Create** | Shared overlay module (route history, radar, wind, meshmapper) |
| `public/map.js` | Modify | Add Overlays fieldset + wire all 4 toggles via MapOverlays |
| `public/analytics.js` | Modify | Add Route History tab with Leaflet map |
| `public/index.html` | Modify | Add `<script defer src="map-overlays.js?v=__BUST__">` |
| `cmd/server/route_history.go` | **Create** | GET /api/route-history handler |
| `cmd/server/route_history_test.go` | **Create** | Unit + handler tests |
| `cmd/server/meshmapper.go` | **Create** | GET /api/coverage/meshmapper handler + cache |
| `cmd/server/meshmapper_test.go` | **Create** | Unit + handler tests |
| `cmd/server/config.go` | Modify | Add `MeshMapper *MeshMapperConfig` field + accessors |
| `cmd/server/routes.go` | Modify | Register new routes |
| `public/style.css` | Modify | Raise nav-action-btn collapse breakpoints (icon-only ≤1600px, hidden ≤1400px) |

---

## 5. Nav Action Button Collapse Fix

### Problem

The "Support Us" and "Discord" buttons (`.nav-action-btn`) in `.nav-right` have `flex-shrink: 0`, so they never shrink. The current breakpoints — icon-only at ≤1400px, hidden at ≤1200px — are too wide, causing them to still be visible and block left-side nav links at mid-range desktop widths (1200–1400px viewport).

### Fix

In `public/style.css`, update the two `.nav-action-btn` breakpoint rules:

| Before | After |
|--------|-------|
| Icon-only at `max-width: 1400px` | Icon-only at `max-width: 1600px` |
| Hidden at `max-width: 1200px` | Hidden at `max-width: 1400px` |

```css
@media (max-width: 1600px) { .top-nav .nav-action-label { display: none; } .top-nav .nav-action-btn { padding: 6px 10px; } }
@media (max-width: 1400px) { .top-nav .nav-action-btn { display: none; } }
```

This ensures the action buttons collapse to icon-only well before the nav becomes crowded, and disappear entirely at the same width where `.nav-link` density already forces the Priority+ overflow menu.

**File:** `public/style.css` — modify 2 lines, no other changes needed.

---

## Out of Scope

- Animated radar playback (single latest frame only)
- Wind forecast beyond current conditions
- MeshMapper write-back or data submission
- Retrofitting area-map with weather overlays (MeshMapper only)
- Route history persistence across server restarts
- Alert/notification on high-traffic edges

---

## Theming Invariants (for all new code)

- Use CSS variable tokens from `style.css` with no hardcoded fallback colors.
- Map tile layers must respond to `[data-theme="dark"]` and `prefers-color-scheme: dark` via a `MutationObserver`, matching the pattern in `map.js`.
- Data-semantic overlay colors (route edges, coverage type rectangles, wind arrows) are hardcoded constants — they are not theme tokens.
