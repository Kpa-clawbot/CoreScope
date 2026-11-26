/* test-issue-2461-stale-detail-load.js — the Home health card and the
 * Analytics route detail draw only the newest load (#2461): an older answer
 * or an older failure that lands after a newer load, after destroy() or after
 * the page re-rendered its container writes nothing.
 *
 * Real code: loadHealth / renderHome / showChooser / destroy (home.js) and
 * loadSubpathDetail / renderSubpathDetail / renderTab / destroy
 * (analytics.js), extracted by brace counting (their page closures make them
 * otherwise unreachable) and run in a vm sandbox. `api` is a stub whose
 * promises the test settles by hand; Leaflet is a mock that records maps.
 *
 * Runs via: node tests/unit/test-issue-2461-stale-detail-load.js
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');
const ROOT = path.resolve(__dirname, '..', '..');
const { loadApiBase } = require('./api-base-sandbox');

let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

function extract(file, re) {
  const src = fs.readFileSync(path.join(ROOT, 'public', file), 'utf8');
  const m = src.match(re);
  if (!m) throw new Error(re + ' not found in ' + file);
  let depth = 0, i = src.indexOf('{', m.index + m[0].length - 1);
  for (; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}' && --depth === 0) { i++; break; }
  }
  return src.slice(m.index, i);
}

// A helper another change may add to the page (#2446 adds removeMiniMap and
// removeSubpathMap, which the functions above then call): taken when present,
// so this suite runs on either side of that change.
function extractIfPresent(file, re) {
  const src = fs.readFileSync(path.join(ROOT, 'public', file), 'utf8');
  return re.test(src) ? extract(file, re) : '';
}

const HOME = [
  extractIfPresent('home.js', /function removeMiniMap\s*\(\)\s*\{/),
  extract('home.js', /async function loadHealth\s*\(pubkey\)\s*\{/),
  extract('home.js', /function destroy\s*\(\)\s*\{/),
  extract('home.js', /function renderHome\s*\(container\)\s*\{/),
  extract('home.js', /function showChooser\s*\(container\)\s*\{/),
].join('\n');
const ANALYTICS = [
  extractIfPresent('analytics.js', /function removeSubpathMap\s*\(\)\s*\{/),
  extract('analytics.js', /function renderSubpathDetail\s*\(panel, data\)\s*\{/),
  extract('analytics.js', /async function loadSubpathDetail\s*\(hopsStr\)\s*\{/),
  extract('analytics.js', /function destroy\s*\(\)\s*\{/),
  extract('analytics.js', /async function renderTab\s*\(tab\)\s*\{/),
].join('\n');

// Leaflet mock: each map remembers what it was centred on and whether it was removed.
function makeL() {
  const maps = [];
  const layer = () => { const l = { addTo() { return l; }, bindTooltip() { return l; } }; return l; };
  const L = {
    map() {
      const map = {
        view: null, removed: 0,
        getPane: () => ({ style: {} }), getContainer: () => ({ isConnected: true }),
        setView(ll) { map.view = ll; return map; }, fitBounds() {}, invalidateSize() {}, off() {},
        remove() { map.removed++; },
      };
      maps.push(map);
      return map;
    },
    tileLayer: layer, circleMarker: layer, marker: layer, polyline: layer,
    latLngBounds: () => ({ pad: () => ({}) }),
  };
  return { L, maps };
}

// `api` stub: every call returns a promise the test settles by hand.
function makeApi() {
  const calls = [];
  const api = (url) => new Promise((resolve, reject) => { calls.push({ url, resolve, reject }); });
  return { api, calls };
}

function makeSandbox() {
  const ctx = {
    console, JSON, Date, Math, Object, Array, String, Number, Boolean, Promise, Set, Map, Error,
    encodeURIComponent, URLSearchParams,
    setTimeout() {}, clearTimeout() {}, requestAnimationFrame() {},
    location: { hash: '#/' },
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    addEventListener() {}, removeEventListener() {},
  };
  ctx.window = ctx;
  const leaflet = makeL();
  const stub = makeApi();
  ctx.L = leaflet.L;
  ctx.api = stub.api;
  ctx.MC_safeRemoveMap = (m) => { if (m) m.remove(); };
  vm.createContext(ctx);
  loadApiBase(ctx);
  const s = { ctx, maps: leaflet.maps, calls: stub.calls, run: (code) => vm.runInContext(code, ctx) };
  s.live = () => s.maps.filter((m) => !m.removed);
  return s;
}

// ── Home: the health card ───────────────────────────────────────────────────
const health = (name, lat) => ({ node: { name, lat, lon: 20, role: 'repeater' }, stats: {}, recentPackets: [], observers: [] });
function home() {
  const s = makeSandbox();
  const card = {
    innerHTML: '', classList: { add() {}, remove() {} },
    querySelector: () => null, querySelectorAll: () => [], scrollIntoView() {},
  };
  const other = { addEventListener() {}, classList: { add() {}, remove() {} }, querySelectorAll: () => [] };
  Object.assign(s.ctx, {
    CLIENT_TTL: {}, miniMap: null, searchTimeout: null, searchAbort: null, _themeRefreshHandler: null,
    _healthGen: 0,
    isMyNode: () => true, getHealthStatus: () => 'healthy', timeAgo: () => 'now', truncate: String,
    snrQuality: () => ({ label: 'n/a' }), escapeHtml: String, escapeAttr: String, _escHtml: String,
    _renderHomeGlyph: String, addMyNode() {}, loadMyNodes() {}, showJourney() {},
    isExperienced: () => true, getMyNodes: () => [], checklist: () => '', setupSearch() {}, loadStats() {},
    setLevel() {}, handleOutsideClick() {},
    __page: { innerHTML: '', querySelectorAll: () => [], querySelector: () => null, addEventListener() {} },
  });
  s.ctx.document = {
    getElementById: (id) => (id === 'homeHealth' ? card : id === 'healthMap' ? {} : other),
    querySelector: () => null, querySelectorAll: () => [], addEventListener() {}, removeEventListener() {},
  };
  s.run(HOME);
  s.el = card;
  s.load = (key) => s.run('loadHealth(' + JSON.stringify(key) + ')');
  s.answer = (name, lat) => health(name, lat);
  s.shows = (name) => new RegExp('<strong>' + name + '</strong>').test(card.innerHTML);
  s.errorRe = /Failed to load/;
  s.rerenders = { renderHome: 'renderHome(__page)', showChooser: 'showChooser(__page)' };
  return s;
}

// ── Analytics: the route (subpath) detail ───────────────────────────────────
const route = (name, lat) => ({
  nodes: [{ name, lat, lon: 2 }, { name: name + '2', lat: lat + 1, lon: 4 }],
  hops: ['aa', 'bb'], hourDistribution: new Array(24).fill(1), signal: {}, observers: [],
  parentPaths: [], firstSeen: null, lastSeen: null, totalMatches: 1,
});
function analytics() {
  const s = makeSandbox();
  const panel = { innerHTML: '', classList: { add() {}, remove() {} } };
  Object.assign(s.ctx, {
    _subpathMap: null, _subpathGen: 0, CLIENT_TTL: {}, esc: String, formatDistance: String,
    statusGreen: () => 'g', statusRed: () => 'r', statusYellow: () => 'y', getTileUrl: () => 'tiles',
    _stopRolesRefresh() {}, _stopScopesRefresh() {}, _stopDistanceRetry() {},
    _analyticsData: {}, _loadError: null, _regionChangeHandler: null, _areaChangeHandler: null,
    _channelData: null, _ngState: null, _themeRefreshHandler: null, _currentTab: 'overview',
    BULK_TABS: new Set(['overview']), loadErrorHtml: String,
  });
  s.ctx.document = { getElementById: (id) => (id === 'subpathDetail' ? panel : { innerHTML: '' }) };
  s.run(ANALYTICS);
  s.el = panel;
  s.load = (key) => s.run('loadSubpathDetail(' + JSON.stringify(key) + ')');
  s.answer = (name, lat) => route(name, lat);
  s.shows = (name) => new RegExp('<h4>' + name + ' → ').test(panel.innerHTML);
  s.errorRe = /Error:/;
  s.rerenders = { renderTab: 'renderTab("overview")' };
  return s;
}

async function suite(label, make) {
  console.log('── ' + label + ' ──');

  await test('one load draws its answer and one map', async () => {
    const s = make();
    const p = s.load('A');
    assert.ok(/Loading/.test(s.el.innerHTML), 'shows the loading state');
    s.calls[0].resolve(s.answer('Alpha', 1));
    await p;
    assert.ok(s.shows('Alpha'), s.el.innerHTML.slice(0, 120));
    assert.strictEqual(s.live().length, 1);
  });

  await test('one load that fails shows the error', async () => {
    const s = make();
    const p = s.load('A');
    s.calls[0].reject(new Error('boom'));
    await p;
    assert.ok(s.errorRe.test(s.el.innerHTML), s.el.innerHTML.slice(0, 120));
  });

  await test('A then B, B answers first, A last: B stays, with exactly one map (B\'s)', async () => {
    const s = make();
    const pA = s.load('A');
    const pB = s.load('B');
    assert.strictEqual(s.calls.length, 2);
    s.calls[1].resolve(s.answer('Bravo', 50));
    await pB;
    assert.ok(s.shows('Bravo'), 'B drawn');
    const html = s.el.innerHTML;
    const mapB = s.maps[0];
    s.calls[0].resolve(s.answer('Alpha', 1));
    await pA;
    assert.ok(s.shows('Bravo') && !s.shows('Alpha'), 'the older answer was drawn over the newer one');
    assert.ok(s.el.innerHTML === html, 'the stale answer wrote to the DOM');
    assert.strictEqual(s.maps.length, 1, s.maps.length + ' maps were created');
    assert.ok(s.live().length === 1 && s.live()[0] === mapB, 'B\'s map is the one live map');
  });

  await test('A then B, A answers first (still stale): nothing drawn until B answers', async () => {
    const s = make();
    const pA = s.load('A');
    const pB = s.load('B');
    s.calls[0].resolve(s.answer('Alpha', 1));
    await pA;
    assert.ok(!s.shows('Alpha'), 'the superseded answer was drawn');
    assert.strictEqual(s.maps.length, 0, 'a map was created for the superseded answer');
    s.calls[1].resolve(s.answer('Bravo', 50));
    await pB;
    assert.ok(s.shows('Bravo'));
    assert.strictEqual(s.maps.length, 1);
  });

  await test('A then B, B answers, A fails last: B stays, no error text', async () => {
    const s = make();
    const pA = s.load('A');
    const pB = s.load('B');
    s.calls[1].resolve(s.answer('Bravo', 50));
    await pB;
    const html = s.el.innerHTML;
    s.calls[0].reject(new Error('boom'));
    await pA;
    assert.ok(!s.errorRe.test(s.el.innerHTML), 'the stale failure replaced the newer answer');
    assert.ok(s.el.innerHTML === html, 'the stale failure wrote to the DOM');
    assert.strictEqual(s.live().length, 1, 'B\'s map is still live');
  });

  await test('an answer that lands after destroy() writes nothing', async () => {
    const s = make();
    const p = s.load('A');
    s.run('destroy()');
    const html = s.el.innerHTML;
    s.calls[0].resolve(s.answer('Alpha', 1));
    await p;
    assert.ok(s.el.innerHTML === html, 'wrote to the DOM after destroy');
    assert.strictEqual(s.maps.length, 0, 'created a map after destroy');
  });

  await test('a failure that lands after destroy() writes nothing', async () => {
    const s = make();
    const p = s.load('A');
    s.run('destroy()');
    const html = s.el.innerHTML;
    s.calls[0].reject(new Error('boom'));
    await p;
    assert.ok(s.el.innerHTML === html, 'wrote to the DOM after destroy');
  });

  for (const name of Object.keys(make().rerenders)) {
    await test('an answer that lands after ' + name + '() re-rendered the page writes nothing', async () => {
      const s = make();
      const p = s.load('A');
      await s.run(s.rerenders[name]);
      const html = s.el.innerHTML;
      s.calls[0].resolve(s.answer('Alpha', 1));
      await p;
      assert.ok(s.el.innerHTML === html, 'wrote to the DOM after the re-render');
      assert.strictEqual(s.maps.length, 0, 'created a map after the re-render');
    });
  }
}

(async () => {
  await suite('Home health card (loadHealth)', home);
  await suite('Analytics route detail (loadSubpathDetail)', analytics);
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed ? 1 : 0);
})();
