/**
 * Line of sight on the map route view: loads the REAL public/los.js and
 * public/route-los.js in a vm context with a stub window, a minimal fake DOM,
 * a stub Leaflet and a stub fetch.
 *
 * Covers the pure helpers (_internals), the update() flow (no fetch while
 * disabled, waiting on MeshConfigReady, one request per contiguous run,
 * chunking, cache vs. no-cache of errors, 429, chips, obstruction markers,
 * row↔seq mapping) and source assertions for the route-view.js hooks and the
 * colour rules of route-los.css. The panel itself is covered in the browser.
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const REPO_ROOT = path.resolve(__dirname, '..', '..');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}
const read = f => fs.readFileSync(path.join(REPO_ROOT, 'public', f), 'utf8');
const LOS_SRC = read('los.js');
const RLOS_SRC = read('route-los.js');
const flush = (ms = 15) => new Promise(r => setTimeout(r, ms));

// ─── Minimal fake DOM ─────────────────────────────────────────────────────

class FakeText {
  constructor(t) { this.nodeType = 3; this.textContent = t; this.parentNode = null; }
}
class FakeEl {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.nodeType = 1;
    this.childNodes = [];
    this.parentNode = null;
    this.attrs = {};
    this.listeners = {};
    this._classes = new Set();
    this.style = {};
    this.title = '';
  }
  get className() { return Array.from(this._classes).join(' '); }
  set className(v) { this._classes = new Set(String(v).split(/\s+/).filter(Boolean)); }
  get classList() {
    const s = this._classes;
    return {
      add: (...c) => c.forEach(x => s.add(x)),
      remove: (...c) => c.forEach(x => s.delete(x)),
      contains: c => s.has(c),
      toggle: (c, f) => { const on = f === undefined ? !s.has(c) : !!f; if (on) s.add(c); else s.delete(c); return on; }
    };
  }
  get firstChild() { return this.childNodes[0] || null; }
  get isConnected() { return true; }
  get textContent() { return this.childNodes.map(c => c.textContent).join(''); }
  set textContent(v) { this.childNodes = []; if (String(v) !== '') this.appendChild(new FakeText(String(v))); }
  appendChild(n) { if (n.parentNode) n.parentNode.removeChild(n); n.parentNode = this; this.childNodes.push(n); return n; }
  insertBefore(n, ref) {
    if (n.parentNode) n.parentNode.removeChild(n);
    const i = this.childNodes.indexOf(ref);
    n.parentNode = this;
    if (i < 0) this.childNodes.push(n); else this.childNodes.splice(i, 0, n);
    return n;
  }
  removeChild(n) { const i = this.childNodes.indexOf(n); if (i >= 0) this.childNodes.splice(i, 1); n.parentNode = null; return n; }
  setAttribute(k, v) { if (k === 'class') this.className = v; else this.attrs[k] = String(v); }
  getAttribute(k) { return k === 'class' ? this.className : (k in this.attrs ? this.attrs[k] : null); }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(t, fn) { (this.listeners[t] = this.listeners[t] || []).push(fn); }
  // Bubbles to ancestors until stopPropagation().
  dispatch(type, extra) {
    const ev = Object.assign({
      type, target: this, stopped: false, prevented: false,
      stopPropagation() { this.stopped = true; }, preventDefault() { this.prevented = true; }
    }, extra || {});
    for (let n = this; n; n = n.parentNode) {
      (n.listeners && n.listeners[type] || []).forEach(fn => fn(ev));
      if (ev.stopped) break;
    }
    return ev;
  }
  querySelectorAll(sel) {
    const cls = sel.replace(/^\./, '');
    const out = [];
    (function walk(n) {
      n.childNodes.forEach(c => { if (c.nodeType === 1) { if (c._classes.has(cls)) out.push(c); walk(c); } });
    })(this);
    return out;
  }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
  focus() {}
}

function makeRow(name, hopIdx) {
  const li = new FakeEl('li');
  li.className = 'mc-rt-row';
  if (hopIdx != null) li.setAttribute('data-hop-idx', hopIdx);
  const nm = new FakeEl('span');
  nm.className = 'mc-rt-name';
  nm.appendChild(new FakeText(name));
  li.appendChild(nm);
  li.clicks = 0;
  li.addEventListener('click', () => { li.clicks++; });
  return li;
}

// Same DOM shape as route-view.js: pinned-top, list, pinned-bottom.
function makeSidebar(seq, opts) {
  opts = opts || {};
  const sb = new FakeEl('aside');
  const top = new FakeEl('div'); top.className = 'mc-rt-pinned mc-rt-pinned-top';
  const list = new FakeEl('ul'); list.className = 'mc-rt-list';
  const bot = new FakeEl('div'); bot.className = 'mc-rt-pinned mc-rt-pinned-bottom';
  [top, list, bot].forEach(x => sb.appendChild(x));
  const idx = i => opts.noHopIdx ? null : i;
  seq.forEach((p, i) => {
    const row = makeRow(p.name, idx(i));
    if (i === 0) top.appendChild(row);
    else if (i === seq.length - 1) bot.appendChild(row);
    else list.appendChild(row);
  });
  // A 1-position canonical route renders the same row pinned top AND bottom.
  if (seq.length === 1 && !opts.noHopIdx) bot.appendChild(makeRow(seq[0].name, 0));
  if (opts.extraRow) list.appendChild(makeRow('extra', null));
  return sb;
}
const rowsOf = sb => sb.querySelectorAll('.mc-rt-row');
const chipOf = row => row.querySelector('.mc-los-chip');

// ─── Stub Leaflet + map ───────────────────────────────────────────────────

function makeLeaflet() {
  const made = { markers: [], polylines: [] };
  const L = {
    layerGroup() {
      return {
        layers: new Set(), _map: null,
        addTo(m) { this._map = m; m._layers.add(this); return this; },
        addLayer(l) { this.layers.add(l); l._group = this; return this; },
        removeLayer(l) { this.layers.delete(l); return this; },
        hasLayer(l) { return this.layers.has(l); },
        clearLayers() { this.layers.clear(); },
        remove() { if (this._map) this._map._layers.delete(this); this._map = null; }
      };
    },
    marker(ll, opts) {
      const m = {
        ll, opts, tooltip: null, handlers: {}, el: new FakeEl('div'),
        bindTooltip(t) { this.tooltip = t; return this; },
        on(t, fn) { this.handlers[t] = fn; return this; },
        addTo(g) { g.addLayer(this); return this; },
        getElement() { return this.el; },
        setLatLng(x) { this.ll = x; return this; },
        remove() { if (this._group) this._group.removeLayer(this); }
      };
      made.markers.push(m);
      return m;
    },
    divIcon(o) { return o; },
    polyline(lls, o) {
      const p = { lls, o, addTo(g) { g.addLayer(this); return this; }, remove() {} };
      made.polylines.push(p);
      return p;
    },
    latLngBounds(x) { return x; },
    DomEvent: { disableClickPropagation() {}, disableScrollPropagation() {} }
  };
  return { L, made };
}
function makeMap() {
  // No container: the panel (browser-tested) never builds in the fake DOM.
  return { _layers: new Set(), hasLayer(l) { return this._layers.has(l); }, getContainer() { return null; },
    fitBounds() {}, getSize() { return { x: 800, y: 600 }; } };
}
const liveMarkers = (made, layer) => made.markers.filter(m => layer && layer.hasLayer(m));

// ─── Stub fetch + elevation profiles ──────────────────────────────────────

const N = 100;
const flat = h => Array.from({ length: N }, () => h);
const ridge = (base, peak) => Array.from({ length: N }, (_, i) => base + peak * Math.exp(-Math.pow((i - 50) / 6, 2)));

function ptsOf(url) {
  const raw = decodeURIComponent(url.split('pts=')[1]).split(',');
  const pts = [];
  for (let i = 0; i < raw.length; i += 2) pts.push([raw[i], raw[i + 1]]);
  return pts;
}
// legFor(fromPt, toPt) → body leg; status() can force a whole-request status.
function makeFetch(legFor, status) {
  const calls = [];
  const f = url => {
    calls.push(url);
    const st = status ? status(calls.length) : 200;
    if (st !== 200) return Promise.resolve({ ok: false, status: st, json: () => Promise.resolve({ error: 'rate limited' }) });
    const pts = ptsOf(url);
    const legs = [];
    for (let i = 1; i < pts.length; i++) legs.push(legFor(pts[i - 1], pts[i]));
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ samples: N, source: 'api.open-meteo.com', legs }) });
  };
  f.calls = calls;
  return f;
}

// ─── Context loader ───────────────────────────────────────────────────────

const CFG = { frequencyMhz: 910.525, antennaHeightM: 30, kFactor: 1.333, samples: 100, maxPoints: 16, maxLegKm: 300 };

function load(opts) {
  opts = opts || {};
  const { L, made } = makeLeaflet();
  const warnings = [];
  const replaced = [];
  const store = new Map(Object.entries(opts.storage || {}));
  const ctx = {
    console: { log() {}, error() {}, warn: m => warnings.push(String(m)) },
    setTimeout, clearTimeout,
    requestAnimationFrame: fn => setTimeout(fn, 0),
    cancelAnimationFrame: clearTimeout,
    innerWidth: 1280,
    document: { createElement: t => new FakeEl(t), body: new FakeEl('body') },
    location: { hash: opts.hash || '#/map?route=1' },
    history: { state: null, replaceState(s, t, u) { replaced.push(u); ctx.location.hash = u; } },
    localStorage: {
      getItem: k => (store.has(k) ? store.get(k) : null),
      setItem: (k, v) => store.set(k, String(v)),
      removeItem: k => store.delete(k)
    },
    fetch: opts.fetch || makeFetch(() => ({ distance_km: 1, elev_m: flat(10) })),
    Map, Promise
  };
  if (opts.withLeaflet !== false) ctx.L = L;
  if ('cfg' in opts) { if (opts.cfg !== undefined) ctx.MC_LOS_CFG = opts.cfg; } else ctx.MC_LOS_CFG = CFG;
  if (opts.configReady) ctx.MeshConfigReady = opts.configReady;
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(LOS_SRC, ctx);
  vm.runInContext(RLOS_SRC, ctx);
  return { ctx, R: ctx.MeshRouteLOS, I: ctx.MeshRouteLOS._internals, made, warnings, replaced, store };
}

// Real fixture coordinates (San Francisco), plus unplaceable hops.
const A = { name: 'CLTR Repeater', lat: 37.78406, lon: -122.42401, resolved: true };
const B = { name: 'CJS SF Mission', lat: 37.76047, lon: -122.41755, resolved: true };
const C = { name: 'EW-HomeBase', lat: 37.73015, lon: -122.42609, resolved: true };
const D = { name: 'Sunset Reservoir', lat: 37.751379, lon: -122.484909, resolved: true };
const E = { name: 'little russia', lat: 37.77272, lon: -122.485, resolved: true };
const UNRES = { name: 'a1', pubkey: 'a1', resolved: false };
const NOGPS = { name: 'Sensor', pubkey: 'beef', resolved: false, gpsless: true };
const ZERO = { name: 'Zero', lat: 0, lon: 0, resolved: true };
const NOCOORD = { name: 'NoCoord', resolved: true };

async function main() {
  const { I } = load();
  const MeshLOS = load().ctx.MeshLOS;

  // Node names come off the mesh (anyone can name a node). Every HTML
  // builder in route-los.js must render them as inert text.
  console.log('\n=== route-los.js HTML builders keep hostile node names inert ===');
  {
    const EVIL_A = '"><img src=x onerror=alert(1)>';
    const EVIL_B = "' onfocus=alert(2) x='<script>alert(3)</script>";
    const leg = I.buildLegs([
      { name: EVIL_A, lat: 37.78406, lon: -122.42401, resolved: true },
      { name: EVIL_B, lat: 37.76047, lon: -122.41755, resolved: true }
    ])[0];
    const elev = flat(10); elev[50] = 900; // a ridge, so the obstruction sentence renders too
    const an = MeshLOS.analyze({ distanceKm: 2.7, elevM: elev, antennaAM: 5, antennaBM: 5, freqMHz: 910.525, kFactor: 4 / 3 });
    const outputs = {
      title: I.panelTitleHtml(leg),
      body: I.panelBodyHtml(leg, an, null),
      bodyError: I.panelBodyHtml(leg, null, { error: 'upstream' }),
      bodyLoading: I.panelBodyHtml(leg, null, null),
      meta: I.metaHtml(leg, an, null),
      readout: I.readoutHtml(leg, an, 50)
    };
    // Escaped text still contains " onerror=" as characters, so check structure:
    // every real tag must be well formed (double-quoted attribute values only)
    // and carry no on* attribute. A name that broke out of a text node or an
    // attribute value would leave a malformed tag, an extra tag, or an on* attr.
    const TAG_RE = /<[^>]*>/g;
    const WELL_FORMED = /^<\/?[a-zA-Z][\w:-]*(\s+[\w:-]+="[^"<]*")*\s*\/?>$/;
    Object.keys(outputs).forEach(k => {
      const html = outputs[k];
      const tags = html.match(TAG_RE) || [];
      const bad = tags.filter(t => !WELL_FORMED.test(t) || /\son[a-z]+="/i.test(t) || /^<\/?(img|script)\b/i.test(t));
      assert(bad.length === 0, k + ': hostile name produces no live tag or handler' + (bad.length ? ' (got ' + bad[0] + ')' : ''));
    });
    assert(outputs.title.includes('&lt;img src=x onerror=alert(1)&gt;'), 'title shows the name as escaped text');
    assert(outputs.body.includes('&lt;img') && /Terrain rises/.test(outputs.body), 'body escapes the name in the obstruction sentence and SVG');
    assert(outputs.readout.includes('&quot;&gt;&lt;img'), 'readout escapes the name');
  }

  console.log('\n=== buildLegs ===');
  {
    const legs = I.buildLegs([A, B, UNRES, C, D, NOGPS, E, ZERO, A, NOCOORD, B, { name: 'Bad', lat: 'x', lon: 5 }, C]);
    assert(legs.map(l => l.idx).join(',') === '1,4', 'legs only between placed neighbours, at the END row index (got ' + legs.map(l => l.idx).join(',') + ')');
    assert(legs[0].nameA === 'CLTR Repeater' && legs[0].nameB === 'CJS SF Mission', 'leg carries both end names');
    assert(legs[1].a.lat === C.lat && legs[1].b.lon === D.lon, 'leg 4 joins row 3 → row 4');
    assert(I.buildLegs([A, UNRES]).length === 0, 'unresolved end → no leg');
    assert(I.buildLegs([A, NOGPS]).length === 0, 'gpsless end → no leg');
    assert(I.buildLegs([A, ZERO]).length === 0, '(0,0) end → no leg');
    assert(I.buildLegs([A, NOCOORD]).length === 0, 'missing coordinates → no leg');
    assert(I.buildLegs([A, { name: 'S', lat: '', lon: '' }]).length === 0, 'empty-string coordinates → no leg');
    assert(I.buildLegs([A, { name: 'Out', lat: 91, lon: 0 }]).length === 0, 'out-of-range latitude → no leg');
    assert(I.buildLegs([A, { name: 'Bridge', lat: 37.7, lon: -122.4, resolved: false }]).length === 0, 'resolved === false with stray coords → no leg');
    assert(I.buildLegs([A]).length === 0 && I.buildLegs([]).length === 0 && I.buildLegs(null).length === 0, 'fewer than 2 positions → no legs');
    const raw = I.buildLegs([{ name: 'x', lat: 37.123456789, lon: -122.000001, resolved: true }, B])[0];
    assert(raw.a.rawLat === '37.123456789' && raw.a.rawLon === '-122.000001', 'leg keeps the exact coordinate strings for the request');
  }

  console.log('\n=== runsToRequests ===');
  {
    const legs = I.buildLegs([A, B, C, UNRES, D, E]);
    const reqs = I.runsToRequests(legs, 16);
    assert(reqs.length === 2, 'a gap splits legs into 2 runs (got ' + reqs.length + ')');
    assert(reqs[0].legs.length === 2 && reqs[0].pts.length === 3, 'run 1: 2 legs, 3 points');
    assert(reqs[1].legs.length === 1 && reqs[1].pts.length === 2, 'run 2: 1 leg, 2 points');
    const many = [];
    for (let i = 0; i < 20; i++) many.push({ name: 'n' + i, lat: 37 + i * 0.01, lon: -122, resolved: true });
    const chunks = I.runsToRequests(I.buildLegs(many), 16);
    assert(chunks.length === 2, '19 contiguous legs, maxPoints 16 → 2 requests');
    assert(chunks[0].pts.length === 16 && chunks[0].legs.length === 15, 'chunk 1 is capped at maxPoints points');
    assert(chunks[1].pts.length === 5 && chunks[1].legs.length === 4, 'chunk 2 holds the remaining 4 legs');
    assert(chunks[1].pts[0].rawLat === chunks[0].pts[15].rawLat && chunks[1].legs[0].idx === chunks[0].legs[14].idx + 1,
      'chunk 2 starts on chunk 1\'s last point (one-point overlap)');
    assert(chunks.every(c => c.pts.length <= 16), 'no request exceeds maxPoints');
    assert(I.runsToRequests(I.buildLegs([A, B, C]), 2).length === 2, 'maxPoints 2 → one leg per request');
    assert(I.runsToRequests([], 16).length === 0, 'no legs → no requests');
    const url = I.requestUrl(reqs[0]);
    assert(url === '/api/elevation/profile?pts=37.78406,-122.42401,37.76047,-122.41755,37.73015,-122.42609',
      'request URL sends the unrounded coordinates in order (got ' + url + ')');
  }

  console.log('\n=== cacheKey ===');
  {
    assert(I.cacheKey(A, B) !== I.cacheKey(B, A), 'direction-aware: A→B ≠ B→A');
    assert(I.cacheKey(A, B) === I.cacheKey({ lat: 37.784061, lon: -122.424014 }, B), 'rounded to 5 decimals');
    assert(I.cacheKey({ lat: '37.78406', lon: '-122.42401' }, B) === I.cacheKey(A, B), 'string coordinates key the same as numbers');
  }

  console.log('\n=== parseLosParam / withLosParam ===');
  {
    assert(I.parseLosParam('#/map?route=1&los=3') === 3, 'los=3');
    assert(I.parseLosParam('#/map?los=12&packet=ab') === 12, 'los first');
    assert(I.parseLosParam('#/map?route=1') === null, 'absent → null');
    assert(I.parseLosParam('#/map?route=1&los=0') === null, 'row 0 has no incoming leg → null');
    assert(I.parseLosParam('#/map?route=1&los=-1') === null && I.parseLosParam('#/map?los=x') === null, 'garbage → null');
    assert(I.parseLosParam('#/map?route=1&slos=4') === null, 'only the exact key matches');
    assert(I.parseLosParam('') === null && I.parseLosParam(null) === null, 'empty → null');
    assert(I.withLosParam('#/map?packet=ab&obs=3', 2) === '#/map?packet=ab&obs=3&los=2', 'adds los, keeps other params in order');
    assert(I.withLosParam('#/map?packet=ab&los=2&obs=3', 5) === '#/map?packet=ab&obs=3&los=5', 'replaces an existing los');
    assert(I.withLosParam('#/map?route=1&los=2', null) === '#/map?route=1', 'null removes it');
    assert(I.withLosParam('#/map?los=2', null) === '#/map', 'removing the only param drops the "?"');
  }

  console.log('\n=== chip text / title / tone ===');
  {
    const base = { distanceKm: 10, antennaAM: 5, antennaBM: 5, freqMHz: 910.525, kFactor: 4 / 3 };
    const clear = MeshLOS.analyze(Object.assign({}, base, { elevM: flat(100), antennaAM: 40, antennaBM: 40 }));
    const clear60 = MeshLOS.analyze(Object.assign({}, base, { elevM: flat(100), antennaAM: 25, antennaBM: 25 }));
    const marginal = MeshLOS.analyze(Object.assign({}, base, { elevM: flat(100) }));
    const blocked = MeshLOS.analyze(Object.assign({}, base, { elevM: ridge(100, 400) }));
    assert(clear.verdict === 'clear' && clear60.verdict === 'clear60' && marginal.verdict === 'marginal' && blocked.verdict === 'blocked',
      'fixtures produce each verdict');
    assert(I.chipText(null) === 'LOS…' && I.chipTone(null) === 'muted', 'loading: "LOS…", muted');
    assert(I.chipText(clear) === 'LOS clear' && I.chipTone(clear) === 'good', 'clear: "LOS clear", good');
    assert(I.chipText(clear60) === 'LOS clear' && I.chipTone(clear60) === 'good', 'clear60: "LOS clear", good');
    assert(I.chipText(marginal) === 'LOS marginal' && I.chipTone(marginal) === 'warn', 'marginal: "LOS marginal", warn');
    assert(I.chipText(blocked) === 'Terrain in path' && I.chipTone(blocked) === 'warn', 'blocked on a travelled leg: "Terrain in path", warn tone');
    const pct = Math.round(clear.clearance * 100);
    assert(I.chipTitle(clear, 'CLTR') === 'Line of sight from CLTR: Clear — ' + pct + '% of first Fresnel zone at 40 m / 40 m antennas',
      'clear title: "Line of sight from CLTR: Clear — ' + pct + '% of first Fresnel zone at 40 m / 40 m antennas"');
    assert(/: Marginal — \d+% of first Fresnel zone at 5 m \/ 5 m antennas$/.test(I.chipTitle(marginal, 'X')), 'marginal title gives the Fresnel %');
    assert(/: terrain \d+ m above the line at 5 m \/ 5 m antennas\. The packet was heard on this hop anyway: .*mis-resolved\.$/.test(I.chipTitle(blocked, 'X')), 'blocked title gives the obstruction height, the heights, and the heard-anyway note');

    const tooLong = { error: 'too_long', distanceKm: 412.1 };
    const upstream = { error: 'upstream' };
    const unknown = { error: 'unknown_point', distanceKm: 3 };
    const limited = { error: 'rate_limited' };
    [tooLong, upstream, unknown, limited].forEach(e => {
      assert(I.chipText(e) === 'LOS n/a' && I.chipTone(e) === 'muted', e.error + ': "LOS n/a", muted');
    });
    assert(/too far for a terrain check \(412 km\)$/i.test(I.chipTitle(tooLong, 'X')), 'too_long title says too far, with the distance');
    assert(/Elevation service unavailable$/.test(I.chipTitle(upstream, 'X')), 'upstream title says the elevation service is unavailable');
    assert(/Node location unknown to the server$/.test(I.chipTitle(unknown, 'X')), 'unknown_point title: "Node location unknown to the server"');
    assert(/Elevation lookups are rate-limited; try again shortly$/.test(I.chipTitle(limited, 'X')), 'rate_limited title: "Elevation lookups are rate-limited; try again shortly"');
    assert(I.chipTitle(null, 'X') === 'Line of sight from X: checking terrain', 'loading title');
    assert(/^Terrain \d+ m above line of sight at 5 m \/ 5 m antennas, [\d.]+ km from Hill\. Heard on this hop anyway\.$/.test(I.markerText(blocked, 'Hill')), 'obstruction marker tooltip (obstructionM > 0)');
    assert(/^Fresnel zone \d+% clear at 5 m \/ 5 m antennas$/.test(I.markerText(marginal, 'Hill')), 'marginal marker tooltip (no obstruction)');
  }

  console.log('\n=== fmtClearance ===');
  {
    assert(I.fmtClearance(1.24) === '124%' && I.fmtClearance(0) === '0%', 'ratio → whole percent');
    assert(I.fmtClearance(-23.4) === 'blocked', 'negative (terrain above the line) → "blocked", not a negative percent');
    assert(I.fmtClearance(50) === '>999%' && I.fmtClearance(Infinity) === '\u2014', 'huge → ">999%", no samples → dash');
  }

  console.log('\n=== legResultFrom / isCacheable ===');
  {
    const ok = I.legResultFrom({ distance_km: 2.5, elev_m: flat(5) });
    assert(ok.distanceKm === 2.5 && ok.elevM.length === N, 'elev_m leg → terrain result');
    assert(I.legResultFrom({ distance_km: 3, error: 'unknown_point' }).error === 'unknown_point', 'unknown_point kept');
    assert(I.legResultFrom({ distance_km: 400, error: 'too_long' }).error === 'too_long', 'too_long kept');
    assert(I.legResultFrom({ distance_km: 3, error: 'mystery' }).error === 'upstream', 'an unknown code reads as unavailable');
    assert(I.legResultFrom({ distance_km: 3, elev_m: [1, null, 3] }).error === 'upstream', 'non-numeric elevation → unavailable');
    assert(I.legResultFrom(null).error === 'upstream', 'missing leg → unavailable');
    assert(I.isCacheable(ok) && I.isCacheable({ error: 'too_long' }), 'terrain and too_long are cacheable');
    assert(!I.isCacheable({ error: 'upstream' }) && !I.isCacheable({ error: 'unknown_point' }) && !I.isCacheable({ error: 'rate_limited' }),
      'upstream / unknown_point / rate_limited are not cached');
  }

  console.log('\n=== parseSetting ===');
  {
    assert(I.parseSetting('antA', '0') === 0 && I.parseSetting('antB', '300') === 300 && I.parseSetting('antA', '12.5') === 12.5, 'antenna 0–300');
    assert(I.parseSetting('antA', '301') === null && I.parseSetting('antA', '-1') === null && I.parseSetting('antA', '') === null, 'antenna out of range / empty → null');
    assert(I.parseSetting('freq', '868') === 868 && I.parseSetting('freq', '99') === null && I.parseSetting('freq', 'abc') === null, 'frequency 100–6000');
  }

  console.log('\n=== rowsFor (row ↔ seq mapping) ===');
  {
    const canon = makeSidebar([A, B, C]);
    const r = I.rowsFor(canon, [A, B, C]);
    assert(r.length === 3 && r[0] === rowsOf(canon)[0] && r[2] === rowsOf(canon)[2], 'canonical rows map by data-hop-idx');
    const one = makeSidebar([A]);
    assert(rowsOf(one).length === 2 && I.rowsFor(one, [A]).length === 1, '1-hop route: the duplicated pinned row is not a mismatch');
    const iso = makeSidebar([A, C, D], { noHopIdx: true });
    const ri = I.rowsFor(iso, [A, C, D]);
    assert(ri && ri[1] === rowsOf(iso)[1], 'isolated rows (no data-hop-idx) map 1:1 in DOM order');
    assert(I.rowsFor(makeSidebar([A, B], { noHopIdx: true, extraRow: true }), [A, B]) === null, 'isolated rows: count mismatch → null');
  }

  console.log('\n=== update(): disabled ===');
  {
    const f = makeFetch(() => ({ distance_km: 1, elev_m: flat(1) }));
    const t = load({ cfg: null, fetch: f });
    const sb = makeSidebar([A, B, C]);
    t.R.update({ map: makeMap(), sidebar: sb, seq: [A, B, C] });
    await flush();
    assert(f.calls.length === 0, 'MC_LOS_CFG = null → no fetch');
    assert(sb.querySelectorAll('.mc-los-chip').length === 0, 'MC_LOS_CFG = null → no chips');
    const t2 = load({ withLeaflet: false, fetch: f });
    t2.R.update({ map: makeMap(), sidebar: makeSidebar([A, B]), seq: [A, B] });
    await flush();
    assert(f.calls.length === 0, 'no Leaflet → no fetch');
  }

  console.log('\n=== update(): waits once for MeshConfigReady ===');
  {
    let resolveCfg;
    const ready = new Promise(r => { resolveCfg = r; });
    const f = makeFetch(() => ({ distance_km: 1, elev_m: flat(1) }));
    const t = load({ cfg: undefined, configReady: ready, fetch: f });
    const sb = makeSidebar([A, B]);
    t.R.update({ map: makeMap(), sidebar: sb, seq: [A, B] });
    t.R.update({ map: makeMap(), sidebar: sb, seq: [A, B] }); // a second call while waiting
    await flush();
    assert(f.calls.length === 0, 'MC_LOS_CFG undefined → no fetch yet');
    t.ctx.MC_LOS_CFG = CFG;
    resolveCfg();
    await flush();
    assert(f.calls.length === 1, 'retried once MeshConfigReady resolves (got ' + f.calls.length + ' fetches)');
    assert(sb.querySelectorAll('.mc-los-chip').length === 1, 'chip rendered after the retry');
  }

  console.log('\n=== update(): one request per contiguous run, chips, markers ===');
  {
    // A→B flat (clear at 30 m antennas), B→C 400 m ridge (blocked), D→E unknown_point.
    const legFor = (p, q) => {
      if (p[0] === String(B.lat)) return { distance_km: 3.4, elev_m: ridge(20, 400) };
      if (p[0] === String(D.lat)) return { distance_km: 2.4, error: 'unknown_point' };
      return { distance_km: 2.7, elev_m: flat(20) };
    };
    const f = makeFetch(legFor);
    const t = load({ fetch: f });
    const seq = [A, B, C, UNRES, D, E];
    const sb = makeSidebar(seq);
    const map = makeMap();
    t.R.update({ map, sidebar: sb, seq });
    const rows = rowsOf(sb);
    assert(f.calls.length === 2, 'two contiguous runs → exactly 2 fetches (got ' + f.calls.length + ')');
    assert(ptsOf(f.calls[0]).length === 3 && ptsOf(f.calls[1]).length === 2, 'run sizes 3 + 2 points');
    assert(chipOf(rows[0]) === null && chipOf(rows[3]) === null && chipOf(rows[4]) === null, 'no chip on origin, unplaced row, or the row after it');
    assert(chipOf(rows[1]).textContent === 'LOS…' && chipOf(rows[1]).className.indexOf('mc-los-loading') >= 0, 'loading chip while fetching');
    assert(chipOf(rows[1]).type === 'button' && chipOf(rows[1]).tagName === 'BUTTON', 'chip is a <button type=button>');
    await flush();
    const c1 = chipOf(rows[1]), c2 = chipOf(rows[2]), c5 = chipOf(rows[5]);
    assert(c1.textContent === 'LOS clear' && /mc-los-tone-good/.test(c1.className), 'flat leg → "LOS clear", good tone');
    assert(c2.textContent === 'Terrain in path' && /mc-los-tone-warn/.test(c2.className), 'ridge leg → "Terrain in path", warn tone');
    assert(c5.textContent === 'LOS n/a' && /mc-los-tone-muted/.test(c5.className), 'unknown_point leg → "LOS n/a", muted');
    assert(/^Line of sight from CLTR Repeater: Clear — \d+% of first Fresnel zone at 30 m \/ 30 m antennas$/.test(c1.title), 'chip title names the previous hop (' + c1.title + ')');
    assert(/Node location unknown to the server/.test(c5.title), 'unknown_point chip title');
    assert(/heard on this hop anyway/.test(c2.getAttribute('aria-label')) && /Show terrain profile$/.test(c2.getAttribute('aria-label')), 'aria-label carries the verdict');
    assert(rows[1].querySelector('.mc-rt-name').classList.contains('mc-los-has-chip'), 'name cell flagged for the chip layout');
    assert(rows[1].querySelector('.mc-los-name-text').textContent === 'CJS SF Mission', 'name text wrapped for its own ellipsis');
    const layer = t.I.state.layer;
    const mks = liveMarkers(t.made, layer);
    assert(mks.length === 1, 'one obstruction marker (blocked leg only) (got ' + mks.length + ')');
    assert(mks[0].opts.icon.className === 'mc-los-obstruction mc-los-tone-warn', 'marker divIcon classes');
    assert(/#ph-warning/.test(mks[0].opts.icon.html), 'marker uses the phosphor warning glyph');
    assert(/^Terrain \d+ m above line of sight at 30 m \/ 30 m antennas, [\d.]+ km from CJS SF Mission\. Heard on this hop anyway\.$/.test(mks[0].tooltip), 'marker tooltip (' + mks[0].tooltip + ')');
    const mid = t.ctx.MeshLOS.intermediatePoint(B, C, 0.5);
    assert(Math.abs(mks[0].ll[0] - mid.lat) < 1e-3 && Math.abs(mks[0].ll[1] - mid.lon) < 1e-3, 'marker sits at the worst point (ridge peak, mid-leg)');

    // Chip activation must not reach the row's own click (hop detail toggle).
    const ev = c2.dispatch('click');
    assert(ev.stopped && rows[2].clicks === 0, 'chip click stops propagation: row click handler not run');
    const kev = c2.dispatch('keydown', { key: 'Enter' });
    assert(kev.stopped, 'Enter on the chip does not bubble to the row');

    // Same rows again (isolate → restore): cached legs are not refetched,
    // the unknown_point leg (not cached) is.
    t.R.update({ map, sidebar: sb, seq });
    assert(f.calls.length === 3, 'update with the same legs: only the uncached error leg is refetched (got ' + f.calls.length + ')');
    assert(ptsOf(f.calls[2]).length === 2 && ptsOf(f.calls[2])[0][0] === String(D.lat), 'refetch is the D→E leg alone');
    assert(sb.querySelectorAll('.mc-los-chip').length === 3, 'chips replaced, not duplicated');
    assert(chipOf(rows[1]).textContent === 'LOS clear', 'cached leg renders its verdict synchronously');
    await flush();

    // Isolated path A → C → D: new legs fetched, A→B chips gone.
    const iso = [A, C, D];
    const sbIso = makeSidebar(iso, { noHopIdx: true });
    t.R.update({ map, sidebar: sbIso, seq: iso });
    assert(f.calls.length === 4 && ptsOf(f.calls[3]).length === 3, 'isolated path: one request for its run');
    await flush();
    assert(sbIso.querySelectorAll('.mc-los-chip').length === 2, 'isolated path: a chip per leg');

    // A stale response must not land on a newer update.
    const releases = [];
    const slow = url => { f.calls.push(url); return new Promise(r => { releases.push(() => r({ ok: true, status: 200, json: () => Promise.resolve({ legs: [{ distance_km: 1, elev_m: flat(1) }] }) })); }); };
    t.ctx.fetch = slow;
    const P = { name: 'P', lat: 37.5, lon: -122.1, resolved: true }, Q = { name: 'Q', lat: 37.51, lon: -122.1, resolved: true };
    const sbPQ = makeSidebar([P, Q]);
    t.R.update({ map, sidebar: sbPQ, seq: [P, Q] });
    t.R.update({ map, sidebar: sb, seq }); // user moves on before the reply
    releases.forEach(fn => fn());
    await flush();
    assert(sbPQ.querySelectorAll('.mc-los-chip').length === 0, 'stale reply after a newer update() is dropped');
    assert(t.I.cache.has(t.I.cacheKey(P, Q)), '...but its terrain is still cached for next time');

    t.R.clear();
    assert(sb.querySelectorAll('.mc-los-chip').length === 0, 'clear() removes all chips');
    assert(!layer._map, 'clear() removes the LOS layer from the map');
  }

  console.log('\n=== update(): too_long cached, 429 not cached and not retried ===');
  {
    let status = 200;
    const f = makeFetch(() => ({ distance_km: 412.1, error: 'too_long' }), () => status);
    const t = load({ fetch: f });
    const sb = makeSidebar([A, B]);
    const map = makeMap();
    t.R.update({ map, sidebar: sb, seq: [A, B] });
    await flush();
    assert(/too far/i.test(chipOf(rowsOf(sb)[1]).title), 'too_long chip title');
    t.R.update({ map, sidebar: sb, seq: [A, B] });
    assert(f.calls.length === 1, 'too_long is cached (no refetch)');

    status = 429;
    const sb2 = makeSidebar([C, D, E]);
    t.R.update({ map, sidebar: sb2, seq: [C, D, E] });
    await flush(40);
    const c = chipOf(rowsOf(sb2)[2]);
    assert(c.textContent === 'LOS n/a' && /Elevation lookups are rate-limited; try again shortly/.test(c.title), '429 → "LOS n/a", rate-limited title');
    assert(f.calls.length === 2, '429 is not retried automatically');
    status = 200;
    t.R.update({ map, sidebar: sb2, seq: [C, D, E] });
    assert(f.calls.length === 3, 'a later update() tries the rate-limited legs again');
  }

  console.log('\n=== update(): chunked run, cache cap ===');
  {
    const f = makeFetch(() => ({ distance_km: 1, elev_m: flat(1) }));
    const t = load({ fetch: f });
    const many = [];
    for (let i = 0; i < 20; i++) many.push({ name: 'n' + i, lat: 37 + i * 0.01, lon: -122, resolved: true });
    t.R.update({ map: makeMap(), sidebar: makeSidebar(many), seq: many });
    assert(f.calls.length === 2 && ptsOf(f.calls[0]).length === 16 && ptsOf(f.calls[1]).length === 5,
      '19 legs, maxPoints 16 → 2 requests of 16 + 5 points');
    await flush();
    for (let i = 0; i < t.I.CACHE_MAX + 20; i++) {
      t.I.cache.set('k' + i, {});
      while (t.I.cache.size > t.I.CACHE_MAX) t.I.cache.delete(t.I.cache.keys().next().value);
    }
    assert(/var CACHE_MAX = 300;/.test(RLOS_SRC) && /while \(cache\.size >= CACHE_MAX\) cache\.delete\(cache\.keys\(\)\.next\(\)\.value\);/.test(RLOS_SRC),
      'cache is capped at 300 entries, oldest evicted first');
  }

  console.log('\n=== update(): row/seq mismatch, settings ===');
  {
    const f = makeFetch(() => ({ distance_km: 1, elev_m: flat(1) }));
    const t = load({ fetch: f });
    const sb = makeSidebar([A, B], { noHopIdx: true, extraRow: true });
    t.R.update({ map: makeMap(), sidebar: sb, seq: [A, B] });
    t.R.update({ map: makeMap(), sidebar: sb, seq: [A, B] });
    assert(f.calls.length === 0 && sb.querySelectorAll('.mc-los-chip').length === 0, 'row count ≠ seq → nothing rendered, nothing fetched');
    assert(t.warnings.filter(w => /route-los/.test(w)).length === 1, 'mismatch warns once');

    const t2 = load({ fetch: f, storage: { 'mc-los-ant-a': '12', 'mc-los-ant-b': 'abc', 'mc-los-freq': '868' } });
    t2.R.update({ map: makeMap(), sidebar: makeSidebar([A, B]), seq: [A, B] });
    const st = t2.I.state.settings;
    assert(st.antA === 12 && st.antB === 30 && st.freq === 868, 'settings: stored values win, invalid stored value falls back to the config default');
  }

  console.log('\n=== deep link writes use replaceState ===');
  {
    const code = RLOS_SRC.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/[^\n]*/g, '');
    assert(!/location\.hash\s*=[^=]/.test(code), 'route-los.js never assigns location.hash');
    assert(/history\.replaceState\(history\.state, '', next\)/.test(RLOS_SRC), 'the los param is written with history.replaceState');
  }

  console.log('\n=== route-view.js hooks ===');
  {
    const rv = read('route-view.js');
    const upd = seq => 'if (window.MeshRouteLOS) window.MeshRouteLOS.update({ map: mapRef, sidebar: sidebar, seq: ' + seq + ' });';
    const at = s => rv.indexOf(s);
    const renderStart = at('function render(mapRef, layer, positions, opts)');
    const renderEnd = at('window.MeshRouteView = { render: render };');
    const renderHook = rv.lastIndexOf(upd('positions'));
    assert(renderHook > renderStart && renderHook < renderEnd &&
      /window\.MeshRouteLOS\.update\(\{ map: mapRef, sidebar: sidebar, seq: positions \}\);\s*\}\s*window\.MeshRouteView =/.test(rv),
      'render(): update(seq: positions) is the last statement');
    assert(/listEl\.innerHTML = iso\.slice\(1, -1\)[^\n]*\n[^\n]*\n\s*if \(window\.MeshRouteLOS\) window\.MeshRouteLOS\.update\(\{ map: mapRef, sidebar: sidebar, seq: iso \}\);/.test(rv),
      'isolatePath(): update(seq: iso) right after the isolated rows are built');
    assert(/newRowEls\.forEach\(sidebar\._wireRow[^\n]*\n\s*if \(window\.MeshRouteLOS\) window\.MeshRouteLOS\.update\(\{ map: mapRef, sidebar: sidebar, seq: positions \}\);/.test(rv),
      'restoreAllPaths(): update(seq: positions) right after the canonical rows are restored');
    const td = rv.slice(at('function teardownIfNavigatedAway()'), at("window.removeEventListener('hashchange', window.__mc_routeTeardown"));
    assert(/if \(!stillRoute\) \{[\s\S]*if \(window\.MeshRouteLOS\) window\.MeshRouteLOS\.clear\(\);/.test(td), 'teardownIfNavigatedAway(): clear() inside the !stillRoute branch');
    assert((rv.match(/MeshRouteLOS\.update\(/g) || []).length === 3, 'exactly three update() call sites');
    const idx = read('index.html');
    assert(idx.indexOf('src="route-los.js') > 0 && idx.indexOf('src="route-los.js') < idx.indexOf('src="route-view.js'), 'index.html loads route-los.js before route-view.js');
  }

  console.log('\n=== route-los.css ===');
  {
    const css = read('route-los.css');
    const noComments = css.replace(/\/\*[\s\S]*?\*\//g, '');
    const withoutFallbacks = noComments.replace(/var\(--[\w-]+,\s*#[0-9a-fA-F]{3,8}\)/g, '');
    const stray = withoutFallbacks.match(/#[0-9a-fA-F]{3,6}\b/g);
    assert(!stray, 'no hex colours outside var(--x, #fallback)' + (stray ? ' (found ' + stray.join(' ') + ')' : ''));
    assert(!/rgba?\(|hsla?\(/.test(noComments), 'no rgb()/hsl() literals either');
    const style = read('style.css');
    const used = Array.from(new Set((noComments.match(/var\(--[\w-]+/g) || []).map(s => s.slice(4))));
    const missing = used.filter(v => style.indexOf(v + ':') < 0);
    assert(missing.length === 0, 'every variable used is defined in style.css' + (missing.length ? ' (missing ' + missing.join(', ') + ')' : ''));
    ['mc-los-svg', 'mc-los-grid', 'mc-los-tick', 'mc-los-endlabel', 'mc-los-f1', 'mc-los-f60', 'mc-los-ground', 'mc-los-line',
      'mc-los-ant-a', 'mc-los-ant-b', 'mc-los-worst', 'mc-los-cursor', 'mc-los-hit', 'mc-los-chip', 'mc-los-panel',
      'mc-los-obstruction', 'mc-los-cursor-marker', 'mc-los-leg-active'].forEach(c => {
      assert(new RegExp('\\.' + c + '\\b').test(noComments), 'styles .' + c);
    });
    assert(/\.mc-los-open \.mc-rt-map-badge \{ display: none; \}/.test(noComments), 'open panel hides the on-map paths badge');
    assert(/@media \(max-width: 767px\)[\s\S]*\.mc-los-panel \{[\s\S]*top: 8px;[\s\S]*max-height: min\(55vh/.test(noComments), 'phone: panel docked at the top, max-height 55vh');
    assert(/@media \(prefers-reduced-motion: reduce\)/.test(noComments), 'respects prefers-reduced-motion');
  }

  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  if (failed) process.exit(1);
}

main().catch(e => { console.error(e); process.exit(1); });
