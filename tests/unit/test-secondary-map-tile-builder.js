/* test-secondary-map-tile-builder.js — secondary map surfaces (analytics
 * subpath map, home mini-map, customizer provider dropdowns + geofilter
 * preview maps) build their basemap through the shared MC_buildTileLayer so
 * they honour the operator's provider choice, and fall back to the old
 * hard-coded URLs when map-tile-providers.js is absent.
 *
 * The real functions are extracted from the real public/*.js files by brace
 * counting and run in a vm sandbox (their IIFE/page closures make them
 * otherwise unreachable). The node-detail/reach/RX helper is covered in
 * test-issue-1470-node-tile-helper.js.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');
const ROOT = path.resolve(__dirname, '..', '..');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}
async function atest(name, fn) {
  try { await fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

function extract(file, re) {
  const src = fs.readFileSync(path.join(ROOT, 'public', file), 'utf8');
  const m = src.match(re);
  if (!m) throw new Error(re + ' not found in ' + file);
  let depth = 0, i = src.indexOf('{', m.index);
  const start = m.index;
  for (; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}' && --depth === 0) { i++; break; }
  }
  return src.slice(start, i);
}

// Leaflet mock recording every layer added to a map.
function makeL() {
  const log = { plain: [], built: [], removed: [], maps: [] };
  const pane = { style: { filter: '' } };
  const mkMap = () => {
    const map = {
      layers: [], pane,
      getPane: () => pane,
      setView() { return map; }, fitBounds() {}, invalidateSize() {}, remove() { map.removed = (map.removed || 0) + 1; log.removed.push(map); },
      removeLayer() {},
    };
    log.maps.push(map);
    return map;
  };
  const addTo = (layer) => (map) => { map.layers.push(layer); return layer; };
  const L = {
    map: () => mkMap(),
    tileLayer(url, opts) { const l = { kind: 'plain', url, opts }; l.addTo = addTo(l); log.plain.push(l); return l; },
    circleMarker: () => ({ bindTooltip() { return this; }, addTo() {} }),
    marker: () => ({ addTo() {} }),
    polyline: () => ({ addTo() {} }),
    latLngBounds: () => ({ pad: () => ({}) }),
  };
  return { L, log, pane };
}

function withBuilder(ctx, desc) {
  ctx.MC_isDarkTheme = () => false;
  ctx.MC_resolveTile = () => desc;
  ctx.MC_buildTileLayer = (d, o) => {
    const l = { kind: 'built', desc: d }; l.addTo = (m) => { m.layers.push(l); return l; };
    l.opts = o; ctx._log.built.push(l); return l;
  };
}

const DESC = { url: 'https://sat.example/{z}/{x}/{y}', maxZoom: 19, invertFilter: 'invert(1)' };

(async () => {
  console.log('── analytics subpath map ──');
  const subSrc = extract('analytics.js', /function renderSubpathDetail\s*\(panel, data\)\s*\{/);
  const data = {
    nodes: [{ name: 'a', lat: 1, lon: 2 }, { name: 'b', lat: 3, lon: 4 }],
    hops: ['aa', 'bb'], hourDistribution: new Array(24).fill(1), signal: {}, observers: [],
    parentPaths: [], firstSeen: null, lastSeen: null, totalMatches: 1,
  };
  function runSubpath(builder) {
    const { L, log, pane } = makeL();
    const ctx = { L, _log: log, esc: String, statusGreen: () => 'g', statusRed: () => 'r', statusYellow: () => 'y',
      formatDistance: String, getTileUrl: () => 'https://fallback/{z}/{x}/{y}', window: {}, Math, Array, Date, Object };
    ctx.window = ctx;
    if (builder) withBuilder(ctx, DESC);
    vm.createContext(ctx);
    vm.runInContext('var _subpathMap = null;' + subSrc + '; renderSubpathDetail({ innerHTML: "" }, __d);', Object.assign(ctx, { __d: data }));
    return { log, pane, ctx };
  }
  test('uses MC_buildTileLayer(MC_resolveTile(...)) when present and applies invertFilter', () => {
    const { log, pane } = runSubpath(true);
    assert.strictEqual(log.built.length, 1);
    assert.strictEqual(log.built[0].desc, DESC);
    assert.strictEqual(log.plain.length, 0, 'no direct tileLayer');
    assert.strictEqual(pane.style.filter, 'invert(1)');
  });
  test('subpath map builds raster-only ({ raster: true })', () => {
    const { log } = runSubpath(true);
    assert.deepStrictEqual(JSON.parse(JSON.stringify(log.built[0].opts)), { raster: true });
  });
  test('a second subpath render removes the first Leaflet map (no leak)', () => {
    const { log, ctx } = runSubpath(true);
    assert.strictEqual(log.removed.length, 0);
    vm.runInContext('renderSubpathDetail({ innerHTML: "" }, __d);', ctx);
    assert.strictEqual(log.maps.length, 2);
    assert.strictEqual(log.removed.length, 1);
    assert.strictEqual(log.removed[0], log.maps[0], 'the previous map was the one removed');
  });
  test('falls back to plain L.tileLayer(getTileUrl()) when the builder is absent', () => {
    const { log } = runSubpath(false);
    assert.strictEqual(log.plain.length, 1);
    assert.strictEqual(log.plain[0].url, 'https://fallback/{z}/{x}/{y}');
  });

  console.log('── home mini-map ──');
  const healthSrc = extract('home.js', /async function loadHealth\s*\(pubkey\)\s*\{/);
  async function runHome(builder) {
    const { L, log, pane } = makeL();
    const card = {
      innerHTML: '', classList: { add() {}, remove() {} },
      querySelector: () => null, querySelectorAll: () => [], scrollIntoView() {},
    };
    const ctx = {
      L, _log: log, Promise, Date, Math, Object, Array, String, JSON, encodeURIComponent,
      setTimeout: () => 0, CLIENT_TTL: {}, _healthGen: 0,
      api: async () => ({ node: { lat: 10, lon: 20, name: 'n', role: 'repeater' }, stats: {}, recentPackets: [], observers: [] }),
      isMyNode: () => true, getHealthStatus: () => 'healthy', timeAgo: () => 'now', truncate: String, snrQuality: () => ({ label: 'n/a', color: 'var(--text-muted)' }),
      escapeHtml: String, escapeAttr: String, miniMap: null, addMyNode() {}, loadMyNodes() {}, showJourney() {},
      document: { getElementById: (id) => id === 'homeHealth' ? card : id === 'healthMap' ? {} : null, querySelector: () => null },
    };
    ctx.window = ctx;
    if (builder) withBuilder(ctx, DESC);
    vm.createContext(ctx);
    vm.runInContext(healthSrc + '; loadHealth("abc");', ctx);
    await new Promise((r) => setImmediate(r));
    return { log, pane, card };
  }
  await atest('uses MC_buildTileLayer for the current theme and applies invertFilter', async () => {
    const { log, pane } = await runHome(true);
    assert.strictEqual(log.built.length, 1);
    assert.strictEqual(log.built[0].desc, DESC);
    assert.strictEqual(log.plain.length, 0);
    assert.strictEqual(pane.style.filter, 'invert(1)');
    assert.deepStrictEqual(JSON.parse(JSON.stringify(log.built[0].opts)), { raster: true }, 'mini-map is raster-only');
  });
  await atest('keeps the OSM URL when the shared tile globals are absent', async () => {
    const { log, card } = await runHome(false);
    assert.ok(!/Failed to load/.test(card.innerHTML), 'loadHealth completed: ' + card.innerHTML.slice(0, 80));
    assert.strictEqual(log.plain.length, 1);
    assert.strictEqual(log.plain[0].url, 'https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png');
  });

  console.log('── customizer ──');
  const selSrc = extract('customize-v2.js', /function _renderTileProviderSelector\s*\(\)\s*\{/);
  const prevSrc = extract('customize-v2.js', /function _geoPreviewTiles\s*\(\)\s*\{/);
  const esc = (s) => String(s == null ? '' : s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  const escAttr = (s) => String(s || '').replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;');
  function selectorHtml(reg) {
    const win = { MC_TILE_PROVIDERS: reg };
    const ctx = { window: win, esc, escAttr };
    vm.createContext(ctx);
    return vm.runInContext(selSrc + '; _renderTileProviderSelector();', ctx);
  }
  function optionsOf(html, id) {
    const m = html.match(new RegExp('id="' + id + '"[^>]*>([\\s\\S]*?)</select>'));
    return m ? [...m[1].matchAll(/value="([^"]*)"/g)].map((x) => x[1]) : null;
  }
  test('dropdowns list "any" entries after the theme\'s own entries, in both lists', () => {
    const html = selectorHtml({
      'any-sat': { type: 'any', label: 'Sat <b>' }, 'l1': { type: 'light', label: 'L1' },
      'd1': { type: 'dark', label: 'D1' }, 'l2': { type: 'light', label: 'L2' },
    });
    assert.deepStrictEqual(optionsOf(html, 'cv2-light-tile-provider'), ['l1', 'l2', 'any-sat']);
    assert.deepStrictEqual(optionsOf(html, 'cv2-dark-tile-provider'), ['d1', 'any-sat']);
    assert.ok(html.includes('Sat &lt;b&gt;'), 'labels still escaped');
  });
  test('only "any" entries still render both dropdowns', () => {
    const html = selectorHtml({ 'any-sat': { type: 'any', label: 'Sat' } });
    assert.deepStrictEqual(optionsOf(html, 'cv2-light-tile-provider'), ['any-sat']);
    assert.deepStrictEqual(optionsOf(html, 'cv2-dark-tile-provider'), ['any-sat']);
  });
  function runPreview(builder, byId) {
    const { L, log } = makeL();
    const ctx = { L, _log: log, window: {} };
    ctx.window = ctx;
    if (builder) withBuilder(ctx, DESC);
    if (byId) ctx.MC_tileUrlById = (id, fb) => 'byid:' + id;
    vm.createContext(ctx);
    vm.runInContext(prevSrc + '; _geoPreviewTiles();', ctx);
    return { log, ctx };
  }
  test('geofilter previews build the operator light provider via MC_buildTileLayer', () => {
    let darkArg = null;
    const { L, log } = makeL();
    const ctx = { L, _log: log };
    ctx.window = ctx;
    withBuilder(ctx, DESC);
    ctx.MC_resolveTile = (d) => { darkArg = d; return DESC; };
    vm.createContext(ctx);
    vm.runInContext(prevSrc + '; _geoPreviewTiles();', ctx);
    assert.strictEqual(darkArg, false, 'resolved for the LIGHT theme');
    assert.strictEqual(log.built.length, 1);
    assert.strictEqual(log.plain.length, 0);
    assert.deepStrictEqual(JSON.parse(JSON.stringify(log.built[0].opts)), { raster: true }, 'preview maps are raster-only');
  });
  test('geofilter previews keep the literal Carto light URL without the shared module', () => {
    const { log } = runPreview(false, false);
    assert.strictEqual(log.plain.length, 1);
    assert.strictEqual(log.plain[0].url, 'https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png');
  });

  console.log(`\nsecondary map tile builder: ${passed} passed, ${failed} failed`);
  process.exit(failed === 0 ? 0 : 1);
})();
