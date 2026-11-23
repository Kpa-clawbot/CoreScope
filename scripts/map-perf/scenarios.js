/* scenarios.js — data table of map-perf scenarios (#2428).
 *
 * A scenario is { name, surface, theme, provider, actions, ... }:
 *   surface   key into SURFACES below (route + how to get a map on screen)
 *   theme     'light' | 'dark'
 *   provider  'auto' (theme default) | a provider id | 'each' (expanded at
 *             runtime into one scenario per registered provider of the theme)
 *   overlays  optional array of overlay ids preset in 'mc-tile-overlays'
 *   format    'raster' (default) | 'vector' — raster scenarios must never
 *             request MapLibre (budget)
 *   actions   steps run.js executes in order (see ACTIONS in run.js):
 *             'load' 'markers' 'tiles' 'metrics' 'heap' 'screenshot'
 *             'panzoom' 'switch' 'vcr' 'hillshadeKernel' (shading kernel timing) 'vector' (vector scenarios: MapLibre
 *             load ms, time to first GL idle, raster-fallback flag)
 *   needs     optional { providers: [ids], overlays: [ids] } the build must
 *             register; otherwise the scenario is skipped with a reason (the
 *             upstream baseline registers none of the #2428 ids)
 *   runs      optional per-scenario override of --runs
 *
 * New providers/overlays/vector maps add rows here; run.js stays unchanged.
 */
'use strict';

// Surfaces: how to reach each map. `route` may be a function of the
// prerequisite context (ctx.node = located node, ctx.repeater = located
// repeater with reach links in the last 7 days). `requires` names ctx fields; a missing one skips the scenario.
// `prepare(page, ctx)` runs after navigation to bring the map on screen.
// `markers`: whether the surface is expected to draw markers.
// `unavailable`: selector the page shows instead of a map when the feature is
// off or has no data; its text becomes the skip reason.
const SURFACES = {
  map: { route: '#/map', markers: true },
  live: { route: '#/live', markers: true },
  planner: { route: '#/tools/planner', markers: true, unavailable: '.mc-pl-off-card' },
  'node-detail': { route: (ctx) => '#/nodes/' + encodeURIComponent(ctx.node.public_key), requires: ['node'], markers: true },
  'node-reach': { route: (ctx) => '#/nodes/' + encodeURIComponent(ctx.repeater.public_key) + '/reach', requires: ['repeater'], markers: true, unavailable: '#nq-report .nq-msg, #nq-report .nq-error, #nq-report .nq-empty' },
  'analytics-map': { route: '#/analytics?tab=subpaths', prepare: 'openSubpathMap', markers: true },
  'rx-coverage': { route: '#/rx-coverage', markers: false, unavailable: '.nq-msg' },
  home: {
    route: '#/home', requires: ['node'], prepare: 'openHomeHealth', markers: true,
    storage: (ctx) => ({
      'meshcore-user-level': 'experienced',
      'meshcore-my-nodes': JSON.stringify([{ pubkey: ctx.node.public_key, name: ctx.node.name || 'node', addedAt: 0 }]),
    }),
  },
};

const COLD = ['load', 'markers', 'tiles', 'metrics', 'heap', 'screenshot'];
const THEMES = ['light', 'dark'];

const SCENARIOS = [];

// Cold load of every map surface, light and dark, default provider.
for (const surface of Object.keys(SURFACES)) {
  for (const theme of THEMES) {
    SCENARIOS.push({ name: `${surface}-cold-${theme}`, surface, theme, provider: 'auto', actions: COLD });
  }
}

// Map page and planner: fixed pan/zoom path. The planner's terrain/profile
// API only runs when points are placed, so these measure basemap + markers.
for (const surface of ['map', 'planner']) {
  for (const theme of THEMES) {
    SCENARIOS.push({ name: `${surface}-panzoom-${theme}`, surface, theme, provider: 'auto', actions: ['load', 'markers', 'tiles', 'panzoom', 'metrics', 'screenshot'] });
  }
}

// Map page: 50 provider switches, heap + Leaflet layer count before/after
// (forced GC). map-switch50-<theme> cycles the theme's RASTER providers only,
// so it compares like with like against a baseline without vector maps.
// map-switch50-all-<theme> (candidate only) also cycles the vector ones.
for (const theme of THEMES) {
  SCENARIOS.push({ name: `map-switch50-${theme}`, surface: 'map', theme, provider: 'auto', switches: 50, switchFormats: ['raster'], actions: ['load', 'markers', 'tiles', 'switch', 'screenshot'] });
  const vec = theme === 'dark' ? 'openfreemap-dark' : 'openfreemap-liberty';
  SCENARIOS.push({ name: `map-switch50-all-${theme}`, surface: 'map', theme, provider: 'auto', format: 'vector', switches: 50, switchFormats: ['raster', 'vector'], needs: { providers: [vec] }, actions: ['load', 'markers', 'tiles', 'switch', 'screenshot'] });
}

// Map page cold load once per registered provider (screenshot gallery +
// per-provider tile timings). 'each' is expanded at runtime from
// window.MC_TILE_PROVIDERS, so new providers are covered automatically.
for (const theme of THEMES) {
  SCENARIOS.push({ name: `map-provider-${theme}`, surface: 'map', theme, provider: 'each', actions: COLD });
}

// Live page: VCR replay of the last hour (no WS traffic needed) observed for a
// fixed window: frame pacing + long tasks while markers animate.
for (const theme of THEMES) {
  SCENARIOS.push({ name: `live-vcr-${theme}`, surface: 'live', theme, provider: 'auto', vcrWindowMs: 10000, vcrSpeed: 4, actions: ['load', 'markers', 'tiles', 'vcr', 'metrics', 'screenshot'] });
}

// ── Candidate-only map types (#2428) ────────────────────────────────────────
// Each is skipped (with a reason) on a build that does not register the id, so
// the upstream baseline run skips them cleanly. Gray and satellite are raster:
// the no-MapLibre budget applies. Vector scenarios measure MapLibre load, the
// first GL idle and whether the raster fallback was used; they never run in
// live mode with a pan/zoom sweep (see gallery.js for the live captures).
const PANZOOM = ['load', 'markers', 'tiles', 'panzoom', 'metrics', 'screenshot'];
const MAP_TYPES = [
  { key: 'gray', provider: { light: 'osmfr-gray', dark: 'osmfr-gray-dark' } },
  { key: 'satellite', provider: { light: 'eox-s2-2025', dark: 'eox-s2-2025' } },
];
for (const t of MAP_TYPES) {
  for (const theme of THEMES) {
    const provider = t.provider[theme];
    const needs = { providers: [provider] };
    SCENARIOS.push({ name: `map-${t.key}-cold-${theme}`, surface: 'map', theme, provider, needs, actions: COLD });
    SCENARIOS.push({ name: `map-${t.key}-panzoom-${theme}`, surface: 'map', theme, provider, needs, actions: PANZOOM });
  }
}
for (const theme of THEMES) {
  const provider = theme === 'dark' ? 'openfreemap-dark' : 'openfreemap-liberty';
  const needs = { providers: [provider] };
  const VECTOR_COLD = ['load', 'markers', 'vector', 'tiles', 'metrics', 'heap', 'screenshot'];
  const VECTOR_PANZOOM = ['load', 'markers', 'vector', 'tiles', 'panzoom', 'metrics', 'screenshot'];
  SCENARIOS.push({ name: `map-vector-cold-${theme}`, surface: 'map', theme, provider, format: 'vector', needs, actions: VECTOR_COLD });
  SCENARIOS.push({ name: `map-vector-panzoom-${theme}`, surface: 'map', theme, provider, format: 'vector', needs, actions: VECTOR_PANZOOM });
}

// Overlays over the theme's default basemap, map page and planner (light).
// hillshade records hillshadeTiles + hillshadeTileMsMedian (createTile->tileload,
// includes the elevation fetch: informational) and hillshadeKernel*MsMedian
// (the shading kernel alone: budgeted).
for (const overlay of ['hillshade', 'eox-labels']) {
  for (const surface of ['map', 'planner']) {
    const actions = overlay === 'hillshade' ? COLD.concat(['hillshadeKernel']) : COLD;
    SCENARIOS.push({ name: `${surface}-overlay-${overlay}-light`, surface, theme: 'light', provider: 'auto', overlays: [overlay], needs: { overlays: [overlay] }, actions });
  }
}

module.exports = { SURFACES, SCENARIOS };
