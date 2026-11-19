/* test-map-custom-providers.js — #2428 built-in keyless providers, operator
 * custom tile entries (map.tiles.custom, S1/S3) and the grouped picker.
 *
 * Runs the real public/map-tile-providers.js in a vm sandbox:
 *   - optIn groups (eox, osmfr) vs the unchanged opt-in/opt-out groups
 *   - fields of each new built-in, categories of the existing ones
 *   - the S3 URL gate (table-driven, hostile inputs) and the field whitelist
 *   - S1: custom attribution stored HTML-escaped; labels stay text
 *   - rejection warnings: one per entry, id only, never the URL
 *   - picker: Street, Topographic, Satellite groups with legends; Auto first
 *
 * Runs via: node tests/unit/test-map-custom-providers.js
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const vm   = require('vm');
const fs   = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

const SRC = fs.readFileSync(path.join(REPO_ROOT, 'public', 'map-tile-providers.js'), 'utf8');

function makeStorage() {
  const store = {};
  return {
    getItem(k)    { return Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null; },
    setItem(k, v) { store[k] = String(v); },
    removeItem(k) { delete store[k]; },
  };
}

function makePane() {
  const attrs = {};
  return {
    style: { filter: '' },
    setAttribute(k, v) { attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(attrs, k) ? attrs[k] : null; },
    removeAttribute(k) { delete attrs[k]; },
  };
}

// Small DOM with real tree semantics (appendChild/insertBefore move nodes),
// so the picker's group structure can be read back in document order.
function makeEl(tag, stats) {
  let text = '';
  const el = {
    tagName: tag, children: [], title: '', className: '', parentNode: null, attrs: {},
    appendChild(c) { detach(c); el.children.push(c); c.parentNode = el; return c; },
    insertBefore(c, ref) {
      detach(c);
      const i = el.children.indexOf(ref);
      if (i < 0) el.children.push(c); else el.children.splice(i, 0, c);
      c.parentNode = el; return c;
    },
    querySelectorAll(sel) { return sel === 'span' ? el.children.filter(c => c.tagName === 'span') : []; },
    setAttribute(k, v) { el.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(el.attrs, k) ? el.attrs[k] : null; },
  };
  Object.defineProperty(el, 'textContent', {
    get() { return text + el.children.map(c => c.textContent).join(''); },
    set(v) { text = String(v); el.children = []; },
  });
  Object.defineProperty(el, 'innerHTML', {
    get() { return text; },
    set(v) { stats.innerHTMLWrites++; text = String(v); el.children = []; },
  });
  return el;
}
function detach(c) {
  if (c.parentNode) {
    const sib = c.parentNode.children;
    const i = sib.indexOf(c);
    if (i >= 0) sib.splice(i, 1);
    c.parentNode = null;
  }
}

// Fake L.control.layers: renders one <label><span><input><span> id</span></span></label>
// per base layer into a list, like Leaflet; _update re-renders from raw ids.
function makeLeaflet(stats) {
  function evented(o) { o._events = {}; o.on = (e, cb) => { o._events[e] = cb; return o; }; return o; }
  return {
    tileLayer(url, opts) {
      const t = evented({ kind: 'tile', _url: url, options: Object.assign({}, opts) });
      t.addTo = (m) => { m.addLayer(t); return t; };
      return t;
    },
    layerGroup(layers) {
      const g = evented({ kind: 'group', _layers: (layers || []).slice() });
      g.addLayer = (l) => { g._layers.push(l); return g; };
      g.removeLayer = (l) => { g._layers = g._layers.filter(x => x !== l); return g; };
      g.getLayers = () => g._layers.slice();
      g.addTo = (m) => { m.addLayer(g); return g; };
      return g;
    },
    control: {
      layers(baseMaps, overlays, opts) {
        const list = makeEl('div', stats);
        const c = {
          baseMaps, opts, list, updates: 0,
          addTo(m) { c.map = m; c._update(); return c; },
          remove() { c.removed = true; return c; },
          getContainer() {
            return { querySelectorAll: () => {
              const out = [];
              (function walk(n) { n.children.forEach(ch => { if (ch.tagName === 'label') out.push(ch); else walk(ch); }); })(list);
              return out;
            } };
          },
          _update() {
            c.updates++;
            list.children.slice().forEach(detach);
            Object.keys(baseMaps).forEach((id) => {
              const label = makeEl('label', stats);
              const holder = makeEl('span', stats);
              holder.appendChild(makeEl('input', stats));
              const name = makeEl('span', stats);
              name.textContent = ' ' + id;
              holder.appendChild(name);
              label.appendChild(holder);
              label.querySelectorAll = (sel) => (sel === 'span' ? [holder, name] : []);
              list.appendChild(label);
            });
            return c;
          },
        };
        stats.controls.push(c);
        return c;
      },
    },
  };
}

function makeMap() {
  const pane = makePane();
  const m = {
    _layers: [],
    getPane: (n) => (n === 'tilePane' ? pane : null),
    addLayer(l) { if (!m._layers.includes(l)) m._layers.push(l); return m; },
    removeLayer(l) { m._layers = m._layers.filter(x => x !== l); return m; },
    hasLayer(l) { return m._layers.includes(l); },
    on() {}, off() {},
  };
  return m;
}

// The #2428 groups are opt-in (off unless enabled:true); the suites that
// exercise them start from a config that turns them all on.
const ALL_ON = () => ({ eox: { enabled: true }, osmfr: { enabled: true }, openfreemap: { enabled: true },
  versatiles: { enabled: true }, hillshade: { enabled: true } });

function makeSandbox(mapCfg) {
  const listeners = {};
  const warns = [];
  const stats = { innerHTMLWrites: 0, controls: [] };
  const docPane = makePane();
  const ctx = {
    console: { log: console.log, error: console.error, warn: (...a) => warns.push(a.join(' ')) },
    JSON, Date, Math, Object, Array, String, Number, Boolean, isFinite,
    document: {
      documentElement: { getAttribute: (k) => (k === 'data-theme' ? 'light' : null) },
      querySelector: (sel) => (sel === '.leaflet-tile-pane' ? docPane : null),
      createElement: (tag) => makeEl(tag, stats),
    },
    window: {
      addEventListener(type, fn) { (listeners[type] = listeners[type] || []).push(fn); },
      removeEventListener(type, fn) { listeners[type] = (listeners[type] || []).filter(f => f !== fn); },
      dispatchEvent(ev) { (listeners[ev.type] || []).slice().forEach(fn => fn(ev)); return true; },
      matchMedia: () => ({ matches: false }),
      localStorage: makeStorage(),
    },
    CustomEvent: function (type, init) { this.type = type; this.detail = (init && init.detail) || null; },
  };
  ctx.localStorage = ctx.window.localStorage;
  ctx.L = ctx.window.L = makeLeaflet(stats);
  vm.createContext(ctx);
  ctx.window.document = ctx.document;
  ctx.window.MC_MAP_CFG = mapCfg !== undefined ? mapCfg : { tiles: { providers: ALL_ON() } };
  vm.runInContext(SRC, ctx);
  for (const k of Object.keys(ctx.window)) if (!(k in ctx)) ctx[k] = ctx.window[k];
  const w = ctx.window;
  return {
    ctx, w, warns, stats,
    reg: () => w.MC_TILE_PROVIDERS,
    // Re-register with a new config, like /api/config/client arriving.
    load(cfg) { w.MC_MAP_CFG = cfg; w.MC_initTileRegistry(false); return w.MC_TILE_PROVIDERS; },
  };
}

function custom(entries, extra) {
  return { tiles: Object.assign({ providers: ALL_ON(), custom: entries }, extra || {}) };
}
const OK_URL = 'https://tiles.example.org/{z}/{x}/{y}.png';

const NEW_IDS = ['eox-s2-2025', 'eox-s2-2016', 'eox-terrain-light', 'osmfr-hot', 'osmfr-cyclosm'];
const EOX_IDS = NEW_IDS.slice(0, 3);
const OSMFR_IDS = NEW_IDS.slice(3);

console.log('── #2428 custom + keyless providers ──');

// ─── optIn semantics ────────────────────────────────────────────────────────

test('optIn groups are off with no config, an empty config and a config without the group', () => {
  const s = makeSandbox(null);
  for (const id of NEW_IDS) assert.ok(!s.reg()[id], id + ' off without MC_MAP_CFG');
  for (const cfg of [{ tiles: {} }, { tiles: { providers: {} } }, { tiles: { providers: { carto: { enabled: true } } } },
    { tiles: { providers: { eox: {} } } }, { tiles: { providers: { eox: { enabled: false }, osmfr: { enabled: false } } } }]) {
    const reg = s.load(cfg);
    for (const id of NEW_IDS) assert.ok(!reg[id], id + ' off for ' + JSON.stringify(cfg));
  }
});

test('only the literal enabled:true turns an optIn group on, and only that group', () => {
  const s = makeSandbox(null);
  let reg = s.load({ tiles: { providers: { eox: { enabled: true } } } });
  for (const id of EOX_IDS) assert.ok(reg[id], id + ' on');
  for (const id of OSMFR_IDS) assert.ok(!reg[id], id + ' still off');
  reg = s.load({ tiles: { providers: { osmfr: { enabled: true } } } });
  for (const id of OSMFR_IDS) assert.ok(reg[id], id + ' on');
  for (const id of EOX_IDS) assert.ok(!reg[id], id + ' still off');
  // Any other value stays off.
  for (const v of ['true', 1, 'yes', null, {}, [], undefined]) {
    reg = s.load({ tiles: { providers: { eox: { enabled: v }, osmfr: { enabled: v } } } });
    for (const id of NEW_IDS) assert.ok(!reg[id], id + ' off for enabled:' + JSON.stringify(v));
  }
  // A non-object group entry is off too.
  reg = s.load({ tiles: { providers: { eox: true, osmfr: 'true' } } });
  for (const id of NEW_IDS) assert.ok(!reg[id], id + ' off for a non-object group');
});

test('each new group follows its own flag: eox, osmfr, openfreemap, versatiles, hillshade (base, vector, overlays)', () => {
  const s = makeSandbox(null);
  const groups = { eox: ['eox-s2-2025', 'eox-terrain-light'], osmfr: ['osmfr-hot', 'osmfr-gray'],
    openfreemap: ['openfreemap-liberty'], versatiles: ['versatiles-colorful'] };
  for (const g of Object.keys(groups)) {
    for (const v of [undefined, {}, { enabled: false }, { enabled: 'true' }]) {
      const cfg = { tiles: { providers: {} } };
      if (v !== undefined) cfg.tiles.providers[g] = v;
      const reg = s.load(cfg);
      for (const id of groups[g]) assert.ok(!reg[id], id + ' off for ' + JSON.stringify(v));
      assert.ok(!s.w.MC_TILE_OVERLAYS || !s.w.MC_TILE_OVERLAYS.hillshade, 'no hillshade overlay');
      if (g === 'eox') assert.ok(!s.w.MC_TILE_OVERLAYS || !s.w.MC_TILE_OVERLAYS['eox-labels'], 'no eox label overlay');
    }
    const cfg = { tiles: { providers: {} } };
    cfg.tiles.providers[g] = { enabled: true };
    const reg = s.load(cfg);
    for (const id of groups[g]) assert.ok(reg[id], id + ' on for enabled:true');
    for (const other of Object.keys(groups)) {
      if (other !== g) for (const id of groups[other]) assert.ok(!reg[id], id + ' stays off');
    }
  }
  // Overlays follow the same flag.
  s.load({ tiles: { providers: { eox: { enabled: true } } } });
  assert.ok(s.w.MC_TILE_OVERLAYS && s.w.MC_TILE_OVERLAYS['eox-labels'], 'eox-labels with eox on');
  assert.ok(!s.w.MC_TILE_OVERLAYS.hillshade, 'hillshade only with its own flag');
  s.load({ tiles: { providers: { hillshade: { enabled: true } } } });
  assert.ok(!s.w.MC_TILE_OVERLAYS['eox-labels'], 'no eox-labels with eox off');
});

test('existing groups keep their rules: osm/usgs/opentopomap/stamen opt-in, carto opt-out, esri always', () => {
  const s = makeSandbox();
  let reg = s.reg();
  for (const id of ['osm-standard', 'osm-dark', 'usgs-topo', 'usgs-imagery', 'opentopomap', 'stamen-toner-lite', 'stamen-toner-dark']) {
    assert.ok(!reg[id], id + ' off by default');
  }
  assert.ok(reg['carto-dark'] && reg['carto-light'], 'carto on by default');
  assert.ok(reg['esri-darkgray-labels'], 'esri always');
  reg = s.load({ tiles: { providers: { osm: { enabled: true }, usgs: { enabled: true }, opentopomap: { enabled: true },
    stamen: { enabled: true }, carto: { enabled: false } } } });
  assert.ok(reg['osm-standard'] && reg['usgs-topo'] && reg['usgs-imagery'] && reg.opentopomap, 'opt-in groups on');
  assert.ok(!reg['stamen-toner-lite'], 'stamen still needs a token');
  assert.ok(!reg['carto-dark'], 'carto opt-out');
  assert.ok(reg['esri-darkgray-labels'], 'esri still there');
});

// ─── New built-ins and categories ───────────────────────────────────────────

test('each new built-in has the exact fields', () => {
  const reg = makeSandbox().reg();
  const eox = (layer) => 'https://tiles.maps.eox.at/wmts/1.0.0/' + layer + '/default/g/{z}/{y}/{x}.jpg';
  const want = {
    'eox-s2-2025': { provider: 'eox', label: 'Satellite 2025 (EOX Sentinel-2)', category: 'satellite', type: 'any',
      url: eox('s2cloudless-2025_3857'), maxZoom: 19, maxNativeZoom: 15,
      attribution: 'Sentinel-2 cloudless 2025 - s2maps.eu by EOX IT Services GmbH (Contains modified Copernicus Sentinel data 2025), CC BY-NC-SA 4.0' },
    'eox-s2-2016': { provider: 'eox', label: 'Satellite 2016 (EOX Sentinel-2)', category: 'satellite', type: 'any',
      url: eox('s2cloudless_3857'), maxZoom: 19, maxNativeZoom: 14,
      attribution: 'Sentinel-2 cloudless - s2maps.eu by EOX IT Services GmbH (Contains modified Copernicus Sentinel data 2016 &amp; 2017), CC BY 4.0' },
    'eox-terrain-light': { provider: 'eox', label: 'Terrain Light (EOX)', category: 'topo', type: 'light',
      url: eox('terrain-light_3857'), maxZoom: 19, maxNativeZoom: 15,
      attribution: 'Terrain Light: Data © OpenStreetMap contributors and others, Rendering © EOX' },
    'osmfr-hot': { provider: 'osmfr', label: 'Humanitarian (OSM France)', category: 'street', type: 'light',
      url: 'https://{s}.tile.openstreetmap.fr/hot/{z}/{x}/{y}.png', subdomains: 'abc', maxZoom: 19,
      attribution: '© OpenStreetMap contributors, style by Humanitarian OpenStreetMap Team, hosted by OpenStreetMap France' },
    'osmfr-cyclosm': { provider: 'osmfr', label: 'CyclOSM (topographic)', category: 'topo', type: 'light',
      url: 'https://{s}.tile-cyclosm.openstreetmap.fr/cyclosm/{z}/{x}/{y}.png', subdomains: 'abc', maxZoom: 19,
      attribution: 'CyclOSM, map data © OpenStreetMap contributors' },
  };
  for (const id of Object.keys(want)) {
    const p = reg[id];
    assert.ok(p, id + ' registered');
    for (const k of Object.keys(want[id])) assert.strictEqual(p[k], want[id][k], id + '.' + k);
    assert.strictEqual(p.optIn, true, id + ' optIn');
    assert.ok(!p.invertFilter, id + ' never inverted');
  }
});

test("existing entries' categories: topo, satellite, street default", () => {
  const s = makeSandbox();
  const reg = s.load({ tiles: { providers: { osm: { enabled: true }, usgs: { enabled: true }, opentopomap: { enabled: true } } } });
  assert.strictEqual(reg.opentopomap.category, 'topo');
  assert.strictEqual(reg['usgs-topo'].category, 'topo');
  assert.strictEqual(reg['usgs-imagery'].category, 'satellite');
  for (const id of ['carto-dark', 'carto-light', 'osm-standard', 'esri-darkgray-labels']) {
    assert.strictEqual(reg[id].category || 'street', 'street', id);
  }
});

test('MC_resolveTile carries a new built-in through (satellite any, maxNativeZoom, subdomains)', () => {
  const s = makeSandbox();
  s.w.MC_setDarkTileProvider('eox-s2-2025');
  let r = s.w.MC_resolveTile(true);
  assert.strictEqual(r.id, 'eox-s2-2025');
  assert.strictEqual(r.category, 'satellite');
  assert.strictEqual(r.maxNativeZoom, 15);
  assert.strictEqual(r.invertFilter, null);
  s.w.MC_setLightTileProvider('osmfr-hot');
  r = s.w.MC_resolveTile(false);
  assert.strictEqual(r.subdomains, 'abc');
  assert.ok(!s.w.MC_setDarkTileProvider('osmfr-hot'), 'a light entry is not a dark pick');
});

// ─── S3 URL gate ────────────────────────────────────────────────────────────

const URL_CASES = [
  // [url, valid, why]
  ['https://tiles.example.org/{z}/{x}/{y}.png', true, 'absolute https'],
  ['http://tiles.example.org/{z}/{x}/{y}.png', true, 'absolute http'],
  ['https://{s}.tiles.example.org/{z}/{x}/{-y}{r}.png', true, 'all placeholders'],
  ['https://tiles.example.org:8443/a/{z}/{x}/{y}.png', true, 'port'],
  ['https://tiles.example.org/style.json', true, 'no placeholders (vector style)'],
  ['/tiles/{z}/{x}/{y}.png', true, 'same-site path'],
  ['https://tiles.example.org/p@th/{z}/{x}/{y}.png', true, "'@' outside the authority"],
  ['https://tiles-{s}.example/{z}/{x}/{y}.png', true, '{s} inside a host label'],
  ['https://{s}.tiles.example/{z}/{x}/{y}.png', true, '{s} as a host label'],
  // Leaflet turns {r} into '@2x' on retina screens: a{r}b.example becomes
  // the userinfo 'a' at host '2xb.example'. Only {s} may sit in the authority.
  ['https://a{r}b.example/{z}/{x}/{y}.png', false, '{r} in the host (becomes userinfo)'],
  ['https://{z}.example/{z}/{x}/{y}.png', false, '{z} in the host'],
  ['https://t.example:{x}/{z}/{x}/{y}.png', false, '{x} in the port'],
  ['https://{-y}.example/{z}/{x}/{y}.png', false, '{-y} in the host'],
  ['https://h{y}.example/{z}/{x}/{y}.png', false, '{y} in the host'],
  ['javascript:alert(1)', false, 'javascript:'],
  ['JAVASCRIPT:alert(1)//{z}/{x}/{y}', false, 'javascript: upper case'],
  ['data:image/png;base64,x', false, 'data:'],
  ['//evil.example/{z}/{x}/{y}.png', false, 'protocol-relative'],
  ['/\\evil.example/{z}', false, "'/\\' becomes protocol-relative"],
  ['https://user:pass@host/{z}/{x}/{y}.png', false, 'userinfo'],
  ['https://evil.example\\@good.example/{z}/{x}/{y}.png', false, "'\\' in the authority"],
  ['https:///{z}/{x}/{y}.png', false, 'empty authority'],
  ['https://host/{z}/{x}/{y}.png?k={apikey}', false, 'unknown placeholder'],
  ['https://host/{z}/{x}/{y}.png?{', false, 'stray brace'],
  ['https://host/{{z}}/{x}/{y}.png', false, 'nested braces'],
  ['https://ho st/{z}/{x}/{y}.png', false, 'space in host'],
  ['https://host/{z}/{x}/{y}.png ', false, 'trailing space'],
  [' https://host/{z}/{x}/{y}.png', false, 'leading space'],
  ['https://host/{z}/{x}/\t{y}.png', false, 'tab'],
  ['https://host/{z}/{x}/{y}.png\n', false, 'newline'],
  ['https://ho\u0000st/{z}/{x}/{y}.png', false, 'NUL'],
  ['https://host/\u0085{z}/{x}/{y}.png', false, 'C1 control'],
  ['https://host/ {z}/{x}/{y}.png', false, 'line separator'],
  ['ftp://host/{z}/{x}/{y}.png', false, 'ftp'],
  ['tiles/{z}/{x}/{y}.png', false, 'relative path'],
  ['', false, 'empty'],
  ['https://host/' + 'a'.repeat(2048) + '/{z}/{x}/{y}', false, 'too long'],
  [42, false, 'not a string'],
  [null, false, 'null'],
];

test('MC__validTileUrl: table of valid and hostile URLs', () => {
  const s = makeSandbox();
  const bad = URL_CASES.filter(([u, ok]) => s.w.MC__validTileUrl(u) !== ok);
  assert.deepStrictEqual(bad.map(c => c[2]), [], 'mismatched cases');
});

test('registration goes through the same gate: hostile urls never reach the registry', () => {
  const s = makeSandbox();
  const entries = URL_CASES.slice(0, 20).map(([u], i) => ({ id: 'u' + i, url: u, format: 'vector' }));
  const reg = s.load(custom(entries));
  URL_CASES.slice(0, 20).forEach(([u, ok, why], i) => {
    assert.strictEqual(!!reg['custom-u' + i], ok, why);
  });
});

test('a raster url needs {z}, {x} and {y} (or {-y}); a vector style url needs none', () => {
  const s = makeSandbox();
  const reg = s.load(custom([
    { id: 'no-z', url: 'https://h/{x}/{y}.png' },
    { id: 'tms', url: 'https://h/{z}/{x}/{-y}.png' },
    { id: 'style', url: 'https://h/style.json', format: 'vector' },
    { id: 'style-raster', url: 'https://h/style.json' },
  ]));
  assert.ok(!reg['custom-no-z']);
  assert.ok(reg['custom-tms']);
  assert.ok(reg['custom-style'] && reg['custom-style'].format === 'vector');
  assert.ok(!reg['custom-style-raster']);
});

// ─── Field whitelist ────────────────────────────────────────────────────────

test('a minimal entry takes the defaults and is registered as custom-<id>', () => {
  const s = makeSandbox();
  const p = s.load(custom([{ id: 'ours', url: OK_URL }]))['custom-ours'];
  assert.ok(p);
  assert.strictEqual(p.provider, 'custom');
  assert.strictEqual(p.label, 'ours');
  assert.strictEqual(p.type, 'light');
  assert.strictEqual(p.category, 'street');
  assert.strictEqual(p.format, 'raster');
  assert.strictEqual(p.url, OK_URL);
  assert.strictEqual(p.attribution, '');
  assert.strictEqual(p.maxZoom, 19);
  assert.ok(!('maxNativeZoom' in p) && !('subdomains' in p));
  assert.strictEqual(s.warns.length, 0);
});

test('S1: custom attribution is stored HTML-escaped once; label markup stays text', () => {
  const s = makeSandbox();
  const p = s.load(custom([{ id: 'x', url: OK_URL, label: '<b>Ours</b> & co',
    attribution: '<img src=x onerror=alert(1)> "q" \'s\' &amp;' }]))['custom-x'];
  assert.strictEqual(p.attribution, '&lt;img src=x onerror=alert(1)&gt; &quot;q&quot; &#39;s&#39; &amp;amp;');
  assert.strictEqual(p.label, '<b>Ours</b> & co', 'label is stored as given; the picker sets it as text');
  // Re-registration starts from the config again: never double-escaped.
  const again = s.load(custom([{ id: 'x', url: OK_URL, attribution: 'A & B' }]))['custom-x'];
  assert.strictEqual(again.attribution, 'A &amp; B');
  // MC_resolveTile hands the stored (escaped) text on unchanged.
  s.w.MC_setLightTileProvider('custom-x');
  assert.strictEqual(s.w.MC_resolveTile(false).attribution, 'A &amp; B');
});

test('unknown keys are never copied (refUrl, invertFilter, provider, optIn, ...)', () => {
  const s = makeSandbox();
  const p = s.load(custom([{ id: 'x', url: OK_URL, type: 'dark', refUrl: 'https://evil/{z}/{x}/{y}', invertFilter: 'url(javascript:1)',
    provider: 'carto', defaultOn: false, optIn: false, baseUrl: 'javascript:1', onload: 'x', __proto__: { polluted: 1 } }]))['custom-x'];
  assert.deepStrictEqual(Object.keys(p).sort(),
    ['attribution', 'category', 'format', 'invertFilter', 'label', 'maxZoom', 'provider', 'type', 'url']);
  assert.strictEqual(p.provider, 'custom');
  assert.strictEqual(p.invertFilter, null);
  assert.ok(!p.refUrl && !p.baseUrl && !p.polluted);
  assert.ok(Object.isFrozen(p), 'registered entry is frozen');
});

test('inherited values are ignored: only own config properties count', () => {
  const s = makeSandbox();
  const proto = { label: 'Inherited', url: OK_URL };
  const entry = Object.create(proto);
  entry.id = 'x';
  const reg = s.load(custom([entry]));
  assert.ok(!reg['custom-x'], 'url inherited from the prototype does not count');
});

test('maxZoom / maxNativeZoom are clamped to 0-24; maxNativeZoom is whole; non-numbers reject', () => {
  const s = makeSandbox();
  const reg = s.load(custom([
    { id: 'hi', url: OK_URL, maxZoom: 99, maxNativeZoom: 30 },
    { id: 'lo', url: OK_URL, maxZoom: -5, maxNativeZoom: -1 },
    { id: 'frac', url: OK_URL, maxZoom: 18.5, maxNativeZoom: 16.7 },
    { id: 'str', url: OK_URL, maxZoom: '19' },
    { id: 'inf', url: OK_URL, maxNativeZoom: Infinity },
    { id: 'nan', url: OK_URL, maxZoom: NaN },
  ]));
  assert.strictEqual(reg['custom-hi'].maxZoom, 24);
  assert.strictEqual(reg['custom-hi'].maxNativeZoom, 24);
  assert.strictEqual(reg['custom-lo'].maxZoom, 0);
  assert.strictEqual(reg['custom-lo'].maxNativeZoom, 0);
  assert.strictEqual(reg['custom-frac'].maxZoom, 18.5);
  assert.strictEqual(reg['custom-frac'].maxNativeZoom, 16);
  for (const id of ['str', 'inf', 'nan']) assert.ok(!reg['custom-' + id], id + ' rejected');
});

test('enums, subdomains, label and attribution limits are checked', () => {
  const s = makeSandbox();
  const reg = s.load(custom([
    { id: 'any-sat', url: OK_URL, type: 'any', category: 'satellite' },
    { id: 'bad-type', url: OK_URL, type: 'night' },
    { id: 'proto-type', url: OK_URL, type: 'constructor' },
    { id: 'bad-cat', url: OK_URL, category: 'hybrid' },
    { id: 'proto-cat', url: OK_URL, category: '__proto__' },
    { id: 'bad-format', url: OK_URL, format: 'pbf' },
    { id: 'subs', url: 'https://{s}.h/{z}/{x}/{y}.png', subdomains: '1234' },
    { id: 'bad-subs', url: OK_URL, subdomains: 'a.b' },
    { id: 'long-subs', url: OK_URL, subdomains: 'abcdefghi' },
    { id: 'arr-subs', url: OK_URL, subdomains: ['a', 'b'] },
    { id: 'label-64', url: OK_URL, label: 'x'.repeat(64) },
    { id: 'label-65', url: OK_URL, label: 'x'.repeat(65) },
    { id: 'label-num', url: OK_URL, label: 5 },
    { id: 'label-blank', url: OK_URL, label: '   ' },
    { id: 'attr-200', url: OK_URL, attribution: 'a'.repeat(200) },
    { id: 'attr-201', url: OK_URL, attribution: 'a'.repeat(201) },
    { id: 'attr-obj', url: OK_URL, attribution: { toString() { return 'x'; } } },
    { id: 'overlay-str', url: OK_URL, overlay: 'yes' },
    { id: 'overlay-false', url: OK_URL, overlay: false },
  ]));
  const has = (id) => !!reg['custom-' + id];
  assert.ok(has('any-sat') && reg['custom-any-sat'].type === 'any' && reg['custom-any-sat'].category === 'satellite');
  for (const id of ['bad-type', 'proto-type', 'bad-cat', 'proto-cat', 'bad-format', 'bad-subs', 'long-subs', 'arr-subs',
    'label-65', 'label-num', 'attr-201', 'attr-obj', 'overlay-str']) {
    assert.ok(!has(id), id + ' rejected');
  }
  assert.strictEqual(reg['custom-subs'].subdomains, '1234');
  assert.ok(has('label-64') && has('attr-200') && has('overlay-false'));
  assert.strictEqual(reg['custom-label-blank'].label, 'label-blank', 'blank label falls back to the id');
});

// ─── List handling ──────────────────────────────────────────────────────────

test('ids: regex enforced, duplicates after the first skipped', () => {
  const s = makeSandbox();
  const reg = s.load(custom([
    { id: 'a', url: OK_URL, label: 'First' },
    { id: 'a', url: OK_URL, label: 'Second' },
    { id: 'z9-x', url: OK_URL },
    { id: 'x'.repeat(32), url: OK_URL },
    { id: 'x'.repeat(33), url: OK_URL },
    { id: '-lead', url: OK_URL },
    { id: 'Upper', url: OK_URL },
    { id: 'under_score', url: OK_URL },
    { id: '__proto__', url: OK_URL },
    { id: 'a<b', url: OK_URL },
    { id: 7, url: OK_URL },
    { url: OK_URL },
  ]));
  assert.strictEqual(reg['custom-a'].label, 'First');
  assert.ok(reg['custom-z9-x'] && reg['custom-' + 'x'.repeat(32)]);
  const customIds = Object.keys(reg).filter(k => k.indexOf('custom-') === 0).sort();
  assert.deepStrictEqual(customIds, ['custom-a', 'custom-' + 'x'.repeat(32), 'custom-z9-x'].sort());
});

test('at most 20 entries are considered; one warn for the rest', () => {
  const s = makeSandbox();
  const entries = [];
  for (let i = 0; i < 21; i++) entries.push({ id: 'e' + i, url: OK_URL });
  const reg = s.load(custom(entries));
  assert.strictEqual(Object.keys(reg).filter(k => k.indexOf('custom-') === 0).length, 20);
  assert.ok(reg['custom-e19'] && !reg['custom-e20']);
  assert.strictEqual(s.warns.length, 1);
  assert.ok(/21 entries; only the first 20/.test(s.warns[0]), s.warns[0]);
});

test('overlay:true entries register as overlays, not base providers (Sprint 3)', () => {
  const s = makeSandbox();
  const reg = s.load(custom([{ id: 'shade', url: OK_URL, overlay: true }]));
  assert.ok(!reg['custom-shade'], 'not a base provider');
  assert.ok(s.w.MC_TILE_OVERLAYS['custom-shade'], 'registered as an overlay');
  assert.strictEqual(s.warns.length, 0);
});

test('non-array custom is ignored with one warn; absent custom is silent', () => {
  for (const bad of [{ id: 'x', url: OK_URL }, 'https://x/{z}/{x}/{y}', 3, true]) {
    const s = makeSandbox();
    const reg = s.load(custom(bad));
    assert.ok(!Object.keys(reg).some(k => k.indexOf('custom-') === 0));
    assert.strictEqual(s.warns.length, 1, JSON.stringify(bad));
    assert.ok(/not a list/.test(s.warns[0]));
  }
  const s = makeSandbox();
  s.load({ tiles: { providers: {} } });
  s.load(custom(null));
  assert.strictEqual(s.warns.length, 0);
});

test('every rejection warns once, quoting the id only and never the URL', () => {
  const s = makeSandbox();
  const secret = 'https://user:SECRETKEY@tiles.example.org/{z}/{x}/{y}.png?token=SECRETKEY';
  const entries = [
    { id: 'creds', url: secret },
    { id: 'apikey', url: 'https://h/{z}/{x}/{y}.png?k={SECRETKEY}' },
    { id: 'bad-attr', url: secret.replace('user:SECRETKEY@', ''), attribution: 5 },
    { id: 'BAD SECRETKEY', url: OK_URL },
    'SECRETKEY',
    { id: 'dup', url: OK_URL }, { id: 'dup', url: secret },
    { id: 'ovl', url: secret, overlay: true },
  ];
  s.load(custom(entries));
  assert.strictEqual(s.warns.length, entries.length - 1, 'one warn per rejected entry:\n' + s.warns.join('\n'));
  for (const w of s.warns) {
    assert.ok(w.indexOf('SECRETKEY') < 0, 'no URL or bad id in: ' + w);
    assert.ok(w.indexOf('tiles.example.org') < 0, 'no host in: ' + w);
  }
  assert.ok(s.warns.some(w => /"creds"/.test(w)), 'names the id');
  // The registry re-initialises when /api/config/client arrives: no repeats.
  s.load(custom(entries));
  assert.strictEqual(s.warns.length, entries.length - 1, 'warnings are not repeated on re-registration');
});

test('darkDefault / lightDefault may name custom-* ids', () => {
  const s = makeSandbox(custom([
    { id: 'night', url: OK_URL, type: 'dark' },
    { id: 'day', url: 'https://day.example/{z}/{x}/{y}.png', type: 'light', attribution: '<i>Day</i>' },
  ], { darkDefault: 'custom-night', lightDefault: 'custom-day' }));
  assert.strictEqual(s.w.MC_getDarkTileProvider(), 'custom-night');
  assert.strictEqual(s.w.MC_getLightTileProvider(), 'custom-day');
  const r = s.w.MC_resolveTile(false);
  assert.strictEqual(r.id, 'custom-day');
  assert.strictEqual(r.url, 'https://day.example/{z}/{x}/{y}.png');
  assert.strictEqual(r.attribution, '&lt;i&gt;Day&lt;/i&gt;');
  // A default naming a rejected entry falls back to the shipped Carto ids.
  const t = makeSandbox(custom([{ id: 'gone', url: 'javascript:1' }], { darkDefault: 'custom-gone' }));
  assert.strictEqual(t.w.MC_getDarkTileProvider(), 'carto-dark');
});

test('a custom vector entry registers and builds the raster fallback layer', () => {
  const s = makeSandbox(custom([{ id: 'vec', url: 'https://h/style.json', format: 'vector', type: 'light' }]));
  const p = s.reg()['custom-vec'];
  assert.strictEqual(p.format, 'vector');
  const layer = s.w.MC_buildTileLayer(p);
  assert.ok(/basemaps\.cartocdn\.com\/light_all/.test(layer._url), 'fell back to Carto light: ' + layer._url);
});

// ─── Picker grouping ────────────────────────────────────────────────────────

function pickerStructure(c) {
  // Document order: Auto label first, then one fieldset per group.
  return c.list.children.map((n) => {
    if (n.tagName === 'label') return { label: n.getAttribute('data-tile-id') };
    const kids = n.children;
    return {
      tag: n.tagName, className: n.className,
      heading: kids[0].tagName + ':' + kids[0].className + ':' + kids[0].textContent,
      items: kids.slice(1).map(k => (k.tagName === 'label' ? k.getAttribute('data-tile-id') : '|' + k.className)),
    };
  });
}

test('picker: Auto first, then Street, Topographic, Satellite with legends; light, dark, any inside each', () => {
  const s = makeSandbox(custom([
    { id: 'sat', url: OK_URL, type: 'any', category: 'satellite', label: 'Our sat' },
    { id: 'night', url: OK_URL, type: 'dark', category: 'topo' },
  ], { providers: Object.assign(ALL_ON(), { usgs: { enabled: true } }) }));
  s.w.MC_createLayerControl(makeMap(), s.ctx.L.layerGroup(), 'topleft');
  const c = s.stats.controls[0];
  const st = pickerStructure(c);
  assert.deepStrictEqual(st[0], { label: '__auto__' });
  assert.deepStrictEqual(st.slice(1).map(g => g.heading), [
    'legend:mc-tile-group-heading:Street', 'legend:mc-tile-group-heading:Topographic', 'legend:mc-tile-group-heading:Satellite']);
  st.slice(1).forEach(g => { assert.strictEqual(g.tag, 'fieldset'); assert.strictEqual(g.className, 'mc-tile-group'); });
  const sep = '|leaflet-control-layers-separator';
  // Inside a type, plain entries come before style:'gray' ones (Sprint 3).
  assert.deepStrictEqual(st[1].items, ['carto-voyager', 'osmfr-hot',
    'openfreemap-liberty', 'openfreemap-bright', 'versatiles-colorful', 'versatiles-muted',
    'carto-light', 'osmfr-gray', 'openfreemap-positron', 'versatiles-gray', sep,
    'carto-voyager-dark', 'openfreemap-fiord', 'versatiles-colorful-dark',
    'carto-dark', 'positron-dark', 'esri-darkgray-labels', 'osmfr-gray-dark', 'openfreemap-dark', 'versatiles-gray-dark']);
  assert.deepStrictEqual(st[2].items, ['usgs-topo', 'eox-terrain-light', 'osmfr-cyclosm', sep, 'custom-night']);
  assert.deepStrictEqual(st[3].items, ['usgs-imagery', 'eox-s2-2025', 'eox-s2-2016', 'custom-sat'],
    "no separator before 'any' entries");
  // Labels are text (S2) and every label carries data-tile-id.
  const all = c.getContainer().querySelectorAll();
  assert.strictEqual(all.length, Object.keys(c.baseMaps).length);
  all.forEach(l => assert.ok(l.getAttribute('data-tile-id')));
  const satLabel = all.find(l => l.getAttribute('data-tile-id') === 'custom-sat');
  assert.strictEqual(satLabel.querySelectorAll('span')[1].textContent, ' Our sat');
  assert.strictEqual(s.stats.innerHTMLWrites, 0);
});

test('picker: empty groups are skipped; a group with no light entries has no separator', () => {
  const s = makeSandbox({ tiles: { providers: { eox: { enabled: false }, osmfr: { enabled: false } } } });
  s.w.MC_createLayerControl(makeMap(), s.ctx.L.layerGroup(), 'topleft');
  const st = pickerStructure(s.stats.controls[0]);
  assert.deepStrictEqual(st.slice(1).map(g => g.heading), ['legend:mc-tile-group-heading:Street']);
  const t = makeSandbox(custom([{ id: 'd', url: OK_URL, type: 'dark', category: 'satellite' }]));
  t.w.MC_createLayerControl(makeMap(), t.ctx.L.layerGroup(), 'topleft');
  const sat = pickerStructure(t.stats.controls[0]).find(g => /Satellite/.test(g.heading || ''));
  assert.deepStrictEqual(sat.items, ['custom-d', 'eox-s2-2025', 'eox-s2-2016']);
});

test("picker: a label with markup is set as text; a Leaflet re-render keeps the groups", () => {
  const s = makeSandbox(custom([{ id: 'x', url: OK_URL, label: '<img src=x onerror=alert(1)>' }]));
  s.w.MC_createLayerControl(makeMap(), s.ctx.L.layerGroup(), 'topleft');
  const c = s.stats.controls[0];
  const before = JSON.stringify(pickerStructure(c));
  c._update(); // what Leaflet does when a listed layer is added/removed programmatically
  assert.strictEqual(JSON.stringify(pickerStructure(c)), before, 'groups re-applied after _update');
  const x = c.getContainer().querySelectorAll().find(l => l.getAttribute('data-tile-id') === 'custom-x');
  const span = x.querySelectorAll('span')[1];
  assert.strictEqual(span.textContent, ' <img src=x onerror=alert(1)>');
  assert.strictEqual(span.children.length, 0);
  assert.strictEqual(s.stats.innerHTMLWrites, 0);
});

console.log('map custom providers: ' + passed + ' passed, ' + failed + ' failed');
if (failed) process.exit(1);
