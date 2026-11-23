# Map performance harness (#2428)

Measures every Leaflet map surface (map, live, planner, node detail, node
reach, analytics route map, RX coverage, home mini map) with tile requests
stubbed, so a baseline and a candidate build can be compared without touching
third-party tile servers.

| File | Role |
|------|------|
| `tests/e2e/lib/map-harness.js` | Tile stub, theme/provider helpers, in-page collectors, `waitForTiles`, `scriptedPanZoom`, `screenshot` |
| `scenarios.js` | Data table of scenarios and surfaces |
| `run.js` | Runs scenarios, writes `results.json` + screenshots |
| `budgets.js` | Pass/fail rules |
| `report.js` | Self-contained HTML report (baseline, or baseline vs candidate) |
| `gallery.js` | Live-tile screenshot gallery of every map type (see Gallery) |

## Tile stubbing

Default `--mode stub` answers every tile, elevation and style request locally:

- raster tiles: an SVG naming the provider key and `z/x/y` on a per-provider colour, so a screenshot shows which provider drew it
- `/terrarium/` elevation: a real PNG of a synthetic, deterministic terrain
- MapLibre style JSON (OpenFreeMap `/styles/`, `*style.json`): a minimal v8 style with one background layer

Any other request to a non-local host is aborted and recorded as
`requestsExternal`. `--mode live` passes tiles through, at most two at a time.
Use it only for a one-off check, never in a loop: the tile hosts forbid bulk
automated fetching.

## Baseline vs candidate

Start the two servers on the synthetic dataset (see `serve-dataset.sh`). The
dataset is not part of this repository: `SEED_DB` is any CoreScope
`analyzer.db` (a large one makes the numbers meaningful) and `SHIFT_SH` a
script that moves its timestamps up to now (`shift.sh <db> <unix-now>`), both
set by environment variable; the defaults point at a local synthetic-dataset
checkout.
upstream master on :13590, the branch on :13591. Without a Carto key, Carto
tiles come back stamped "API KEY REQUIRED"; for screenshots with real Carto
tiles, start the servers with `CARTO_KEY_CONFIG=/path/to/a/config.json`
(only its `map.tiles.providers.carto.key` is copied into the run config under
`~/.cache/corescope-map-perf/`; never commit a key). Then:

```bash
npm ci   # once, in the worktree

node scripts/map-perf/run.js --base-url http://localhost:13590 \
  --label baseline-master --out test-results/map-perf --runs 9
node scripts/map-perf/run.js --base-url http://localhost:13591 \
  --label candidate --out test-results/map-perf --runs 9

node scripts/map-perf/report.js \
  --baseline test-results/map-perf/baseline-master \
  --candidate test-results/map-perf/candidate \
  --out test-results/map-perf/report.html
```

Leave out `--candidate` for a baseline-only report. Keep `report.html` next to
the result directories: it links the screenshots by relative path.

Options:

- `--runs N`: measured runs per scenario (default 5). Each run uses a new browser context.
- `--warmup N`: runs per scenario to throw away first (default 1). They warm the server caches.
- `--scenarios a,b`: run only these scenarios. A `provider: 'each'` scenario can be named by its base name (for example `map-provider-dark`).
- `--mode stub|live`.

Run baseline and candidate on the same machine, one at a time, with nothing
else heavy running, and use `--runs 9` for both: on the reference machine the
p95/median spread of tile and long-task times is 1.05-1.25, wider than the
10% budget.

### How a regression is decided

The report compares **medians**. A lower-is-better metric whose candidate
median is more than 10% worse than the baseline median is:

- **red (fail)** only when the candidate median is also above the baseline
  **p95**, i.e. outside the baseline's own run-to-run range;
- **amber ("within noise")** otherwise: it moved more than 10% but no further
  than the baseline already varies between runs. Not a failure; rerun both
  sides if it matters.

Budget rules in `budgets.js` use the same test (`kind: 'relative'`). Absolute
budgets (heap growth, hillshade per-tile time, no MapLibre) are plain limits.

`results.json` is rewritten after every scenario, so an interrupted run keeps
the scenarios it finished.

## Output

```
test-results/map-perf/<label>/results.json   meta + per scenario: status, skip reason,
                                             raw runs, summary {n, median, p95, min, max, mean}
test-results/map-perf/<label>/shots/*.png     one viewport screenshot per scenario (first run)
```

A scenario whose route or prerequisite is missing is recorded as `skipped`
with a reason. For example, the RX coverage page is opt-in
(`clientRxCoverage.enabled`) and needs client RX data, the planner needs
`planner.enabled`, node reach needs a repeater with links in the last 7 days,
and the live VCR needs packets within the last 24 h (its scopes are relative
to now).

The planner scenarios measure the basemap and markers only: its terrain /
profile API runs when points are placed, which no scenario does.

## Metrics

All times are ms from navigation start unless stated otherwise.

- `firstTileMs`, `allTilesMs`: first tile loaded; last load among the tiles visible once loading settles
- `mapCreatedMs`, `allTilesSinceMapMs`, `firstMarkerSinceMapMs`: when the first `L.Map` was constructed, and the tile/marker times measured from it. Use these for surfaces that need a click before the map appears (analytics, home).
- `firstMarkerMs`: first `.leaflet-marker-icon` / `.marker-cluster` in the DOM, or the first marker layer added (canvas markers)
- `longTaskCount`, `longTaskTotalMs`, `longTaskMaxMs`: PerformanceObserver `longtask`
- `heapUsedMB`: CDP `JSHeapUsedSize` after `HeapProfiler.collectGarbage`
- pan/zoom: `frameP50Ms`, `frameP95Ms`, `frameMaxMs` (requestAnimationFrame intervals), `framesDropped` (intervals over 50 ms), `panzoomLongTask*`
- 50 switches: `heapBeforeMB`, `heapAfterMB`, `heapGrowthPct`, `layersBefore`, `layersAfter`, `tileElsAfter`, `switchMsMedian`, `switchMsP95`. One full warm-up pass through the providers runs before the "before" heap is taken.
- VCR: `vcrFrame*` and `vcrLongTask*` over a fixed 10 s window of 4× replay
- `hillshadeTileMsMedian`: canvas tiles of a layer whose URL looks like terrarium/elevation, from `createTile` to `tileload`
- `maplibreRequests`: requests whose URL contains `maplibre`
- `apiSlowestMs`: slowest same-site `/api/` request of the run (the three slowest URLs are in each run's `requests.slowestApi`). Use it to tell a slow server response from slow map code when a run is an outlier.

### Known sources of noise

- Home and analytics re-render once on the app's load-time `theme-refresh` (app.js fires it about 300 ms after `/api/config/theme` resolves). The runner waits for that event before clicking a My Nodes card or a route-pattern row. Without the wait, the health map was wiped and the run stalled for 15-31 s.
- Analytics route patterns: the first run of a scenario searches for a row whose subpath has 2+ located hops, up to 8 s per row tried. Later runs click that row directly. The default `--warmup 1` absorbs the search.

## Extending

- **New provider**: nothing to do. `map-provider-<theme>` expands to one scenario per id in `window.MC_TILE_PROVIDERS`, `map-switch50-<theme>` cycles through the theme's raster providers (comparable with a baseline that has no vector maps), and the candidate-only `map-switch50-all-<theme>` through all of them, vector included. If the provider uses a new tile host, add a row to `TILE_HOSTS` in `map-harness.js` so its stubs and screenshots carry a readable key. Unknown hosts are still stubbed, under the key `host:<hostname>`.
- **Overlays**: add a scenario with `overlays: ['<id>']`. The runner presets `mc-tile-overlays`.
- **Vector maps**: add a scenario with `provider: '<id>'` and `format: 'vector'`. Raster-only scenarios fail the budget if MapLibre is ever requested.
- **New surface**: add an entry to `SURFACES` in `scenarios.js` (route, optional `prepare` hook, `unavailable` selector).

## Candidate-only scenarios

`scenarios.js` carries scenarios for the #2428 map types: gray (`osmfr-gray`,
`osmfr-gray-dark`), satellite (`eox-s2-2025`), vector (`openfreemap-liberty`
light, `openfreemap-dark` dark) and the `hillshade` / `eox-labels` overlays on
the map and the planner. They name the ids they need in `needs`; a build that
does not register one records the scenario as `skipped` with that reason, so
the upstream baseline run skips them cleanly.

- Gray, satellite and overlay scenarios are raster: the `raster-no-maplibre` budget fails them if `vendor/maplibre-gl` is requested.
- Vector scenarios add `maplibreLoadMs` (first MapLibre request to `window.maplibregl`), `glMapCreatedMs`, `glFirstIdleMs` / `glIdleSinceMapMs` (first time the style and tiles are loaded and the map is not moving; 20 ms polling) and `vectorFallbackUsed` (budget: 0). In stub mode the style is a background-only stub, so these time the library and GL setup, not real tile decoding.
- Hillshade scenarios add `hillshadeKernel256MsMedian` / `hillshadeKernel512MsMedian`: the shading kernel alone on a synthetic tile, median of 50 runs. The budget (5 ms) applies to the 256 px figure. `hillshadeTileMsMedian` (createTile to tileload) also contains the elevation fetch and decode: reported, not budgeted.

## Gallery

```bash
node scripts/map-perf/gallery.js --base-url http://localhost:13591 \
  --out test-results/map-perf/gallery [--only id,id]
```

One screenshot (1280x800; the open-picker entries are 1100 px tall) of every
map type on the dataset, with **real tiles**: live mode, at most two tile
requests in flight, one page load per entry, 1 s between entries, no
pan/zoom. The data table `E` in `gallery.js` lists route, theme, provider,
overlays and view per entry. Views come from `/api/nodes?limit=2000`: the
median node position inside a bounding box, Denver / Front Range for topo,
satellite, hillshade and the planner, New York for street, gray and vector
(edit `VIEWS` if the dataset has no nodes there). The theme, provider pick,
overlays and `map-view` are set in localStorage before load.

Output: `index.json` (file, title, category, theme, provider, overlays, view,
tiles loaded/failed, vector path used, failures), `<id>.png`, and
`thumbs/<id>.png` (320 px wide, downscaled in the browser). An entry
**fails** (the run continues; exit code 1) when a tile failed or answered
HTTP 400+, the settle timed out, a vector map used the raster fallback, a
raster entry requested MapLibre, or the resolved provider is not the one
asked for. An id the build does not register (OpenTopoMap is opt-in) is
`skipped`.

The `custom` entries inject one raster provider by rewriting
`/api/config/client` and answer its `/__fake-tiles/{z}/{x}/{y}.png` requests
from labelled SVG tiles inside Playwright (no network).

## Self-test

```bash
node tests/unit/test-map-harness-png.js                        # encoders, URL classifier
BASE_URL=http://localhost:13581 node tests/e2e/test-map-providers-harness-e2e.js
```
