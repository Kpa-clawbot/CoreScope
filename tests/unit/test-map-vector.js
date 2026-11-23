/* test-map-vector.js — vector basemap loader + layer (#2428).
 *
 * Runs the real public/map-vector.js in a vm sandbox with a fake DOM, fake
 * Leaflet, fake MapLibre and fake timers, and covers:
 *   - MC_loadMapLibre: tags injected once with constant paths + SRI +
 *     crossOrigin, worker URL resolved against document.baseURI (set once),
 *     promise cached, never rejected on time, pending tags never removed or
 *     re-injected, one retry after a script error and no more, a stale
 *     binding re-queued behind MapLibre
 *   - per-layer 15 s timeout; a late library serves later layers
 *   - MC_buildVectorLayer: WebGL probe (no library request without WebGL),
 *     fallback on load failure / style error / context loss / init failure,
 *     exactly once; no mount after the group left the map; explicit MapLibre
 *     options only; attribution handed over to the fallback
 *   - transformRequest scheme policy and page-origin refusal for
 *     cross-origin styles (S4), no setRTLTextPlugin (S4)
 *   - the binding's deferred _transitionEnd frame is a no-op after removal
 *   - mcFallbackReason / MC_vectorLayerStale: transient fallbacks become
 *     stale once MapLibre is ready, permanent ones never
 *
 * The zoom-limit tests against the real vendored Leaflet need jsdom, so they
 * live in tests/e2e/test-map-vector-leaflet-zoom.js (the unit gate has no
 * node_modules).
 *
 * Runs via: node tests/unit/test-map-vector.js
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

const SRC = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'map-vector.js'), 'utf8');
const ATTR = '&copy; OpenFreeMap &copy; OpenMapTiles';
const STYLE = 'https://tiles.openfreemap.org/styles/liberty';

let passed = 0, failed = 0;
const queue = [];
function test(name, fn) { queue.push([name, fn]); }
const flush = () => new Promise((r) => setImmediate(r));

// ── fakes ────────────────────────────────────────────────────────────────
function makeEvented(obj) {
  const handlers = {};
  obj.on = function (type, fn) { (handlers[type] = handlers[type] || []).push(fn); return obj; };
  obj.once = function (type, fn) {
    const w = function (e) { obj.off(type, w); fn(e); };
    return obj.on(type, w);
  };
  obj.off = function (type, fn) { handlers[type] = (handlers[type] || []).filter((h) => h !== fn); return obj; };
  obj.fire = function (type, e) { (handlers[type] || []).slice().forEach((h) => h(e || {})); return obj; };
  obj.listeners = (type) => (handlers[type] || []).length;
  return obj;
}

function makeSandbox(opts) {
  opts = opts || {};
  const protocol = opts.protocol || 'https:';
  const base = protocol + '//example.org/sub/';
  const stats = {
    glMaps: [], glLayers: [], gl: [], probeLost: 0, contexts: 0, warns: [], fallbacks: 0, rasters: [],
    libInjections: 0, bindingInjections: 0, libExecs: 0, setWorkerUrl: 0, removedPending: 0,
  };

  // timers
  let now = 0;
  let timers = [];
  function setTimeout(fn, ms) { const t = { fn, at: now + ms, id: timers.length + 1 }; timers.push(t); return t.id; }
  function clearTimeout(id) { timers = timers.filter((t) => t.id !== id); }
  function advance(ms) {
    now += ms;
    const due = timers.filter((t) => t.at <= now);
    timers = timers.filter((t) => t.at > now);
    due.forEach((t) => t.fn());
  }

  function makeEl(tag) {
    const attrs = {};
    return {
      tagName: tag.toUpperCase(), parentNode: null,
      setAttribute(k, v) { attrs[k] = String(v); },
      getAttribute(k) { return Object.prototype.hasOwnProperty.call(attrs, k) ? attrs[k] : null; },
      hasAttribute(k) { return Object.prototype.hasOwnProperty.call(attrs, k); },
      getContext(type) {
        stats.contexts++;
        if (opts.noWebGL || !/^webgl2?$/.test(type)) return null;
        return { getExtension: (n) => (n === 'WEBGL_lose_context' ? { loseContext() { stats.probeLost++; } } : null) };
      },
    };
  }
  const head = {
    children: [],
    appendChild(el) {
      if (el.tagName === 'SCRIPT' && /maplibre-gl-csp\.js$/.test(el.src)) stats.libInjections++;
      if (el.tagName === 'SCRIPT' && /leaflet-maplibre-gl\.js$/.test(el.src)) stats.bindingInjections++;
      head.children.push(el); el.parentNode = head; return el;
    },
    removeChild(el) {
      // Removing a pending script does not cancel it in a browser.
      if (el.tagName === 'SCRIPT' && !el.done) stats.removedPending++;
      head.children = head.children.filter((c) => c !== el); el.parentNode = null; return el;
    },
  };
  function matches(el, sel) {
    const m = sel.match(/^(\w+)\[([\w-]+)\]$/);
    return m && el.tagName === m[1].toUpperCase() && el.hasAttribute(m[2]);
  }
  const document = {
    head, baseURI: base,
    createElement: makeEl,
    getElementsByTagName: () => [head],
    querySelectorAll: (sel) => head.children.filter((el) => matches(el, sel)),
    querySelector: (sel) => head.children.find((el) => matches(el, sel)) || null,
  };

  // Leaflet: layer groups and a map with an attribution control.
  const L = {};
  L.layerGroup = function (layers, options) {
    const g = makeEvented({ options: options || {}, _layers: [], _map: null });
    g.addLayer = function (l) { g._layers.push(l); if (g._map) l._addTo(g._map); return g; };
    g.removeLayer = function (l) { g._layers = g._layers.filter((x) => x !== l); if (g._map) l._removeFrom(g._map); return g; };
    g.getLayers = () => g._layers.slice();
    g.eachLayer = (fn) => g._layers.forEach(fn);
    g._addTo = function (map) { g._map = map; g._layers.forEach((l) => l._addTo(map)); g.fire('add'); };
    g._removeFrom = function (map) { g._layers.forEach((l) => l._removeFrom(map)); g._map = null; g.fire('remove'); };
    return g;
  };
  // Leaflet's frame queue: callbacks run only when the test calls runFrames().
  let frames = [];
  L.Util = {
    requestAnimFrame(fn, ctx) { frames.push([fn, ctx]); return frames.length; },
  };
  const realRequestAnimFrame = L.Util.requestAnimFrame;
  function runFrames() {
    const due = frames;
    frames = [];
    due.forEach(([fn, ctx]) => fn.call(ctx));
  }
  // Mirrors leaflet-maplibre-gl: the MapLibre object is captured when the
  // binding script executes; a new MapLibre map on every add, map.remove()
  // and _map = null on remove; _transitionEnd defers its work to a frame
  // that dereferences this._map and this._glMap.
  function maplibreGL(options, lib) {
    const layer = makeEvented({ options, _glMap: null, _map: null, kind: 'gl', jumps: 0 });
    layer.getMaplibreMap = () => layer._glMap;
    layer._transitionEnd = function () {
      L.Util.requestAnimFrame(function () {
        this._map.getZoom();
        this._glMap.jumpTo({});
        this.jumps++;
      }, this);
    };
    layer._addTo = function (map) {
      if (!lib) throw new TypeError('binding executed before MapLibre: maplibre_gl.Map is not a constructor');
      if (opts.glCtorThrows) throw new Error('Failed to initialize WebGL');
      const glMap = makeEvented({ removed: 0, options, lib, workerUrl: lib.workerUrl });
      glMap.remove = function () { glMap.removed++; };
      glMap.jumpTo = function () {};
      stats.glMaps.push(glMap);
      layer._map = map;
      layer._glMap = glMap;
      layer.fire('add');
    };
    layer._removeFrom = function () { layer._glMap.remove(); layer._glMap = null; layer._map = null; };
    stats.glLayers.push(layer);
    return layer;
  }
  const attributions = [];
  const map = {
    attributionControl: {
      addAttribution(t) { attributions.push('+' + t); },
      removeAttribution(t) { attributions.push('-' + t); },
    },
    addLayer(g) { g._addTo(map); },
    removeLayer(g) { g._removeFrom(map); },
    getZoom() { return 5; },
  };

  const window = {
    location: { protocol, origin: protocol + '//example.org' }, L,
    maplibregl: undefined,
  };
  const ctx = {
    window, document, L, URL, Promise, setTimeout, clearTimeout,
    console: { warn: (...a) => stats.warns.push(a.map(String).join(' ')), log() {}, error() {} },
  };
  window.document = document;
  vm.createContext(ctx);
  vm.runInContext(SRC, ctx, { filename: 'map-vector.js' });

  // Simulate the browser finishing one injected script: 'error' fires its
  // error event (it never executes); 'ok' executes it and fires load.
  function exec(tag, outcome) {
    assert.ok(!tag.done, 'script finished twice: ' + tag.src);
    tag.done = true;
    if (outcome === 'error') { if (tag.onerror) tag.onerror(); return; }
    if (/maplibre-gl-csp\.js$/.test(tag.src)) {
      stats.libExecs++;
      const lib = { setWorkerUrl(u) { stats.setWorkerUrl++; lib.workerUrl = u; } };
      window.maplibregl = lib;
    } else {
      const captured = window.maplibregl;
      L.maplibreGL = (o) => maplibreGL(o, captured);
    }
    if (tag.onload) tag.onload();
  }
  function pending(re) {
    return head.children.filter((t) => t.tagName === 'SCRIPT' && !t.done && (!re || re.test(t.src)));
  }
  const pendingLib = () => pending(/maplibre-gl-csp\.js$/)[0];
  const pendingBinding = () => pending(/leaflet-maplibre-gl\.js$/)[0];
  // Run every pending script in insertion order (async=false). 'error'
  // fails the first one; the ones after it still execute, as in a browser.
  function runScripts(outcome) {
    pending().forEach((t, i) => exec(t, outcome === 'error' && i === 0 ? 'error' : 'ok'));
  }
  function fallback() {
    stats.fallbacks++;
    const raster = { kind: 'raster', _addTo() {}, _removeFrom() {} };
    stats.rasters.push(raster);
    return raster;
  }
  function build(desc) {
    return window.MC_buildVectorLayer(Object.assign({ url: STYLE, attribution: ATTR, maxZoom: 19, type: 'light' }, desc), { fallback });
  }
  return {
    window, document, head, L, map, stats, attributions, advance, runScripts, build,
    exec, pending, pendingLib, pendingBinding, runFrames, realRequestAnimFrame,
  };
}

// ── loader ───────────────────────────────────────────────────────────────
const TABLE = {};
(SRC.match(/VENDOR-INTEGRITY-BEGIN([\s\S]*?)VENDOR-INTEGRITY-END/)[1].match(/path: '[^']+', integrity: '[^']+'/g) || [])
  .forEach((s) => { const m = s.match(/path: '([^']+)', integrity: '([^']+)'/); TABLE[m[1]] = m[2]; });

test('loader injects the stylesheet and both scripts once, with constant paths, SRI and crossOrigin', async () => {
  const sb = makeSandbox();
  const p1 = sb.window.MC_loadMapLibre();
  const p2 = sb.window.MC_loadMapLibre();
  assert.strictEqual(p1, p2, 'promise cached');
  const tags = sb.head.children;
  assert.deepStrictEqual(tags.map((t) => t.tagName + ' ' + (t.src || t.href)), [
    'LINK vendor/maplibre-gl-5.24.0/maplibre-gl.css',
    'SCRIPT vendor/maplibre-gl-5.24.0/maplibre-gl-csp.js',
    'SCRIPT vendor/maplibre-gl-leaflet-0.1.4/leaflet-maplibre-gl.js',
  ]);
  tags.forEach((t) => {
    assert.strictEqual(t.integrity, TABLE[t.src || t.href], 'integrity from the table');
    assert.ok(/^sha384-/.test(t.integrity));
    assert.strictEqual(t.crossOrigin, 'anonymous');
  });
  assert.strictEqual(tags[0].rel, 'stylesheet');
  assert.strictEqual(tags[1].async, false, 'scripts execute in order');
  assert.strictEqual(tags[2].async, false);
  sb.runScripts('ok');
  const lib = await p1;
  assert.strictEqual(lib, sb.window.maplibregl);
  assert.strictEqual(lib.workerUrl, 'https://example.org/sub/vendor/maplibre-gl-5.24.0/maplibre-gl-csp-worker.js');
  assert.strictEqual(sb.window.MC_loadMapLibre(), p1, 'still cached after success');
  assert.strictEqual(sb.head.children.length, 3, 'no second injection');
});

test('loader is not triggered at page load', () => {
  const sb = makeSandbox();
  assert.strictEqual(sb.head.children.length, 0);
});

test('a style URL from config never reaches a script or link URL', () => {
  const sb = makeSandbox();
  sb.build({ url: 'https://evil.example/x.js' });
  sb.head.children.forEach((t) => assert.ok(/^vendor\//.test(t.src || t.href), t.src || t.href));
});

test('library promise never rejects on time alone; pending tags are never removed', async () => {
  const sb = makeSandbox();
  let settled = null;
  sb.window.MC_loadMapLibre().then(() => { settled = 'ok'; }, (e) => { settled = e; });
  sb.advance(60000); await flush();
  assert.strictEqual(settled, null, 'still pending after 60 s');
  assert.strictEqual(sb.pending().length, 2, 'both scripts still in the document');
  assert.strictEqual(sb.stats.removedPending, 0);
  sb.window.MC_loadMapLibre();
  assert.strictEqual(sb.stats.libInjections, 1, 'no second injection');
  sb.runScripts('ok'); await flush();
  assert.strictEqual(settled, 'ok', 'late arrival resolves the shared promise');
});

test('timeout is per layer: a timed-out layer keeps its fallback, a later layer mounts the late library, one injection', async () => {
  const sb = makeSandbox();
  const g1 = sb.build();
  sb.map.addLayer(g1);
  sb.advance(14999); await flush();
  assert.strictEqual(sb.stats.fallbacks, 0, 'still waiting at 14.999 s');
  sb.advance(1); await flush();
  assert.strictEqual(sb.stats.fallbacks, 1, 'layer 1 falls back at 15 s');
  assert.ok(sb.stats.warns.some((w) => /load-timeout/.test(w)));
  assert.deepStrictEqual(g1.getLayers(), sb.stats.rasters);
  assert.strictEqual(sb.stats.removedPending, 0, 'pending tags left alone');
  assert.strictEqual(sb.pending().length, 2);

  const g2 = sb.build();
  sb.map.addLayer(g2);
  assert.strictEqual(sb.stats.libInjections, 1, 'no second MapLibre injection');
  assert.strictEqual(sb.stats.bindingInjections, 1, 'no second binding injection');
  sb.advance(5000);
  sb.runScripts('ok'); await flush();
  assert.strictEqual(sb.stats.libExecs, 1, 'MapLibre executed once');
  assert.strictEqual(sb.stats.glMaps.length, 1, 'layer 2 mounts GL');
  assert.strictEqual(g2.getLayers()[0].kind, 'gl');
  assert.deepStrictEqual(g1.getLayers(), [sb.stats.rasters[0]], 'layer 1 stays on its fallback');
  assert.strictEqual(sb.stats.fallbacks, 1);
  sb.advance(60000); await flush();
  assert.strictEqual(sb.stats.fallbacks, 1, 'layer 2 timer cleared');

  const g3 = sb.build();
  sb.map.addLayer(g3); await flush();
  assert.strictEqual(sb.stats.glMaps.length, 2, 'session not poisoned: layer 3 mounts GL');
  assert.strictEqual(sb.stats.setWorkerUrl, 1, 'setWorkerUrl called exactly once');
  sb.stats.glMaps.forEach((m) => {
    assert.strictEqual(m.lib, sb.window.maplibregl, 'binding uses the one MapLibre copy');
    assert.ok(/maplibre-gl-csp-worker\.js$/.test(m.workerUrl), 'worker URL set before the map was built');
  });
});

test('a layer that joins at 14 s gets its own full 15 s', async () => {
  const sb = makeSandbox();
  const g1 = sb.build();
  sb.map.addLayer(g1);
  sb.advance(14000);
  const g2 = sb.build();
  sb.map.addLayer(g2);
  sb.advance(1000); await flush();
  assert.strictEqual(sb.stats.fallbacks, 1, 'only layer 1 timed out');
  assert.strictEqual(g2.getLayers().length, 0, 'layer 2 still waiting');
  sb.advance(13999); await flush();
  assert.strictEqual(sb.stats.fallbacks, 1, 'layer 2 still waiting at 28.999 s');
  sb.advance(1); await flush();
  assert.strictEqual(sb.stats.fallbacks, 2, 'layer 2 falls back at 29 s');
  assert.strictEqual(sb.stats.libInjections, 1);
});

test('script error: one retry with fresh tags, second error falls back, no third injection', async () => {
  const sb = makeSandbox();
  const g1 = sb.build();
  sb.map.addLayer(g1);
  sb.runScripts('error'); await flush();
  assert.strictEqual(sb.stats.fallbacks, 1, 'layer 1 falls back on the error event');
  assert.ok(sb.stats.warns.some((w) => /load-failed/.test(w)));

  const g2 = sb.build();
  sb.map.addLayer(g2);
  assert.strictEqual(sb.stats.libInjections, 2, 'retry injects MapLibre again (the first copy never executed)');
  assert.strictEqual(sb.stats.bindingInjections, 2, 'and a binding that runs after it');
  assert.strictEqual(sb.document.querySelectorAll('link[data-mc-maplibre]').length, 1, 'stylesheet not duplicated');
  sb.runScripts('error'); await flush();
  assert.strictEqual(sb.stats.fallbacks, 2, 'layer 2 falls back');

  const g3 = sb.build();
  sb.map.addLayer(g3); await flush();
  assert.strictEqual(sb.stats.fallbacks, 3, 'layer 3 falls back at once');
  assert.strictEqual(sb.stats.libInjections, 2, 'no third injection');
  const p = sb.window.MC_loadMapLibre();
  assert.strictEqual(p, sb.window.MC_loadMapLibre(), 'cached rejection');
  await assert.rejects(p);
  assert.strictEqual(sb.stats.removedPending, 0);
});

test('loader retry after a failure can succeed', async () => {
  const sb = makeSandbox();
  const p1 = sb.window.MC_loadMapLibre();
  sb.runScripts('error');
  await assert.rejects(p1);
  const p2 = sb.window.MC_loadMapLibre();
  assert.notStrictEqual(p1, p2);
  sb.runScripts('ok');
  assert.strictEqual(await p2, sb.window.maplibregl);
  assert.strictEqual(sb.stats.setWorkerUrl, 1);
});

test('retry after only the binding failed reuses the loaded MapLibre', async () => {
  const sb = makeSandbox();
  const p1 = sb.window.MC_loadMapLibre();
  sb.exec(sb.pendingLib(), 'ok');
  sb.exec(sb.pendingBinding(), 'error');
  await assert.rejects(p1);
  const p2 = sb.window.MC_loadMapLibre();
  assert.strictEqual(sb.stats.libInjections, 1, 'MapLibre not injected again');
  assert.strictEqual(sb.stats.bindingInjections, 2);
  sb.runScripts('ok');
  assert.strictEqual(await p2, sb.window.maplibregl);
  assert.strictEqual(sb.stats.libExecs, 1);
});

test('retry while the old binding is still pending: the stale binding is replaced after the new MapLibre', async () => {
  const sb = makeSandbox();
  const p1 = sb.window.MC_loadMapLibre();
  sb.exec(sb.pendingLib(), 'error');
  await assert.rejects(p1);
  const oldBinding = sb.pendingBinding();
  const p2 = sb.window.MC_loadMapLibre();
  assert.strictEqual(sb.stats.libInjections, 2);
  assert.strictEqual(sb.pending(/leaflet-maplibre-gl\.js$/).length, 1, 'pending binding reused, not duplicated');
  sb.exec(oldBinding, 'ok'); // runs before the new MapLibre: captures nothing
  await flush();
  assert.strictEqual(sb.stats.bindingInjections, 2, 'a fresh binding queued behind the new MapLibre');
  sb.runScripts('ok');
  const lib = await p2;
  const g = sb.build();
  sb.map.addLayer(g); await flush();
  assert.strictEqual(sb.stats.glMaps.length, 1, 'mounted with a binding that captured MapLibre');
  assert.strictEqual(sb.stats.glMaps[0].lib, lib);
  assert.strictEqual(sb.stats.setWorkerUrl, 1);
});

// ── layer ────────────────────────────────────────────────────────────────
test('no WebGL: fallback at once, library never requested, probe checked first', () => {
  const sb = makeSandbox({ noWebGL: true });
  const g = sb.build();
  assert.strictEqual(sb.head.children.length, 0, 'no tags injected');
  assert.strictEqual(sb.stats.fallbacks, 1);
  assert.deepStrictEqual(g.getLayers(), sb.stats.rasters);
  assert.ok(sb.stats.warns.some((w) => /webgl-unavailable/.test(w)));
  sb.build();
  assert.strictEqual(sb.stats.warns.length, 1, 'warned once per reason');
});

test('WebGL probe hands its context back', () => {
  const sb = makeSandbox();
  sb.build();
  assert.strictEqual(sb.stats.probeLost, 1);
  sb.build();
  assert.strictEqual(sb.stats.contexts, 1, 'probe result cached');
});

test('mounts the GL layer once loaded and on a map; group carries the attribution and maxZoom', async () => {
  const sb = makeSandbox();
  const g = sb.build();
  assert.strictEqual(g.getAttribution(), ATTR);
  assert.strictEqual(g.options.maxZoom, 19);
  sb.map.addLayer(g);
  sb.runScripts('ok'); await flush();
  assert.strictEqual(sb.stats.glLayers.length, 1);
  assert.deepStrictEqual(g.getLayers(), sb.stats.glLayers);
  assert.strictEqual(sb.stats.glMaps.length, 1);
  assert.strictEqual(sb.stats.fallbacks, 0);
});

test('explicit MapLibre options only; nothing leaks from desc', async () => {
  const sb = makeSandbox();
  const g = sb.build({ attributionControl: true, container: 'x', transformRequest: () => ({ url: 'javascript:x' }), interactive: true, pane: 'markerPane', extra: 1 });
  sb.map.addLayer(g);
  sb.runScripts('ok'); await flush();
  const o = sb.stats.glLayers[0].options;
  assert.deepStrictEqual(Object.keys(o).sort(), ['attributionControl', 'interactive', 'style', 'transformRequest']);
  assert.strictEqual(o.style, STYLE);
  assert.strictEqual(o.attributionControl, false);
  assert.strictEqual(o.interactive, false);
  await assert.rejects(o.transformRequest('javascript:x'), /Blocked/, 'desc.transformRequest ignored');
});

test('load failure: fallback once', async () => {
  const sb = makeSandbox();
  const g = sb.build();
  sb.map.addLayer(g);
  sb.runScripts('error'); await flush();
  assert.strictEqual(sb.stats.fallbacks, 1);
  assert.deepStrictEqual(g.getLayers(), sb.stats.rasters);
  assert.strictEqual(sb.stats.glLayers.length, 0);
  assert.ok(sb.stats.warns.some((w) => /load-failed/.test(w)));
  assert.strictEqual(g.getAttribution(), '', 'vector attribution handed over');
  assert.ok(sb.attributions.includes('-' + ATTR));
});

test('style error before first load: GL removed, fallback once; errors after load ignored', async () => {
  const sb = makeSandbox();
  const g = sb.build();
  sb.map.addLayer(g);
  sb.runScripts('ok'); await flush();
  const glMap = sb.stats.glMaps[0];
  glMap.fire('error', { error: new Error('404') });
  glMap.fire('error', { error: new Error('again') });
  assert.strictEqual(sb.stats.fallbacks, 1);
  assert.strictEqual(glMap.removed, 1, 'MapLibre map removed (context released)');
  assert.deepStrictEqual(g.getLayers(), sb.stats.rasters);

  const sb2 = makeSandbox();
  const g2 = sb2.build();
  sb2.map.addLayer(g2);
  sb2.runScripts('ok'); await flush();
  sb2.stats.glMaps[0].fire('idle');
  sb2.stats.glMaps[0].fire('error', { error: new Error('tile') });
  assert.strictEqual(sb2.stats.fallbacks, 0, 'a tile error after the first render keeps the vector map');
});

test('GL context lost: fallback', async () => {
  const sb = makeSandbox();
  const g = sb.build();
  sb.map.addLayer(g);
  sb.runScripts('ok'); await flush();
  sb.stats.glMaps[0].fire('load');
  sb.stats.glMaps[0].fire('webglcontextlost');
  assert.strictEqual(sb.stats.fallbacks, 1);
  assert.strictEqual(sb.stats.glMaps[0].removed, 1);
  assert.ok(sb.stats.warns.some((w) => /context-lost/.test(w)));
});

test('MapLibre constructor throws: fallback instead of an exception', async () => {
  const sb = makeSandbox({ glCtorThrows: true });
  const g = sb.build();
  sb.map.addLayer(g);
  sb.runScripts('ok'); await flush();
  assert.strictEqual(sb.stats.fallbacks, 1);
  assert.deepStrictEqual(g.getLayers(), sb.stats.rasters);
});

test('group removed before the library arrives: nothing mounted; re-adding mounts', async () => {
  const sb = makeSandbox();
  const g = sb.build();
  sb.map.addLayer(g);
  sb.map.removeLayer(g);
  sb.runScripts('ok'); await flush();
  assert.strictEqual(sb.stats.glLayers.length, 0, 'not mounted');
  assert.strictEqual(sb.stats.glMaps.length, 0);
  sb.map.addLayer(g);
  assert.strictEqual(sb.stats.glMaps.length, 1, 'mounted on re-add');
  sb.map.removeLayer(g);
  assert.strictEqual(sb.stats.glMaps[0].removed, 1, 'GL map removed with the group');
  sb.map.addLayer(g);
  assert.strictEqual(sb.stats.glLayers.length, 1, 'same GL layer reused');
  assert.strictEqual(sb.stats.glMaps.length, 2, 'fresh MapLibre map per add');
  sb.stats.glMaps[1].fire('error', {});
  assert.strictEqual(sb.stats.fallbacks, 1, 'listeners wired on the remounted map');
});

test('style URL that the request policy refuses: fallback, library never requested', () => {
  const sb = makeSandbox();
  ['http://tiles.example/style.json', 'data:application/json,{}', 'javascript:alert(1)', '', null].forEach((url) => sb.build({ url }));
  assert.strictEqual(sb.stats.fallbacks, 5);
  assert.strictEqual(sb.head.children.length, 0);
});

// ── request policy ───────────────────────────────────────────────────────
async function policy(protocol, styleUrl) {
  const sb = makeSandbox({ protocol });
  sb.map.addLayer(sb.build(styleUrl ? { url: styleUrl } : {}));
  sb.runScripts('ok'); await flush();
  return sb.stats.glLayers[0].options.transformRequest;
}
async function verdict(tr, url) {
  const r = tr(url);
  if (r && typeof r.then === 'function') {
    try { await r; return 'allowed?'; } catch (e) { return 'refused'; }
  }
  assert.deepStrictEqual(Object.keys(r), ['url']);
  assert.strictEqual(r.url, url);
  return 'allowed';
}

test('transformRequest: scheme table on an https page', async () => {
  const tr = await policy('https:');
  const table = {
    'https://tiles.openfreemap.org/planet/1/2/3.pbf': 'allowed',
    'http://tiles.example/1/2/3.pbf': 'refused',
    'data:application/x-protobuf;base64,AAAA': 'refused',
    'blob:https://example.org/abc': 'refused',
    'javascript:alert(1)': 'refused',
    'file:///etc/passwd': 'refused',
    'ftp://example.org/x': 'refused',
    '//cdn.example/x.json': 'allowed',
  };
  for (const url of Object.keys(table)) assert.strictEqual(await verdict(tr, url), table[url], url);
});

test('transformRequest: http allowed only on an http page', async () => {
  const tr = await policy('http:', 'styles/local.json');
  assert.strictEqual(await verdict(tr, 'http://tiles.example/1/2/3.pbf'), 'allowed');
  assert.strictEqual(await verdict(tr, 'sprites/basic'), 'allowed');
  assert.strictEqual(await verdict(tr, 'data:,x'), 'refused');
  assert.strictEqual(await verdict(tr, 'file:///x'), 'refused');
});

// MapLibre 5.24 hands style-supplied URLs to transformRequest verbatim (it
// resolves nothing against the style URL; sprites must be absolute), so a
// relative URL is fetched against the page: the page origin, with cookies.
test('transformRequest: a cross-origin style cannot reach the page origin', async () => {
  const tr = await policy('https:'); // STYLE is on tiles.openfreemap.org
  const table = {
    'https://example.org/api/nodes': 'refused',
    'https://example.org:443/api/x': 'refused',
    'HTTPS://EXAMPLE.ORG/api/x': 'refused',
    '/api/nodes': 'refused',
    'sprites/basic': 'refused',
    '//example.org/api/x': 'refused',
    '/fonts/{fontstack}/{range}.pbf': 'refused',
    'https://example.org:8443/api/x': 'allowed', // another origin: no page cookies
    'https://tiles.openfreemap.org/styles/liberty': 'allowed',
    'https://tiles.openfreemap.org/planet/1/2/3.pbf': 'allowed',
    'https://other.example/glyphs/a.pbf': 'allowed',
  };
  for (const url of Object.keys(table)) assert.strictEqual(await verdict(tr, url), table[url], url);
});

test('transformRequest: a same-origin style keeps same-origin resources', async () => {
  for (const style of ['https://example.org/styles/local.json', 'styles/local.json', '/styles/local.json']) {
    const tr = await policy('https:', style);
    assert.strictEqual(await verdict(tr, style), 'allowed', style);
    assert.strictEqual(await verdict(tr, 'https://example.org/tiles/1/2/3.pbf'), 'allowed', style);
    assert.strictEqual(await verdict(tr, '/fonts/{fontstack}/{range}.pbf'), 'allowed', style);
    assert.strictEqual(await verdict(tr, 'sprites/basic'), 'allowed', style);
    assert.strictEqual(await verdict(tr, 'https://tiles.openfreemap.org/planet/1/2/3.pbf'), 'allowed', style);
    assert.strictEqual(await verdict(tr, 'data:,x'), 'refused', style);
    assert.strictEqual(await verdict(tr, 'http://tiles.example/x'), 'refused', style);
  }
});

test('transformRequest: the origin policy is per layer', async () => {
  const sb = makeSandbox();
  sb.map.addLayer(sb.build({ url: 'https://example.org/styles/local.json' }));
  sb.map.addLayer(sb.build());
  sb.runScripts('ok'); await flush();
  const [own, foreign] = sb.stats.glLayers.map((l) => l.options.transformRequest);
  assert.strictEqual(await verdict(own, '/api/x'), 'allowed');
  assert.strictEqual(await verdict(foreign, '/api/x'), 'refused');
});

// ── binding frame guard ──────────────────────────────────────────────────
test('binding _transitionEnd frame after removal does not throw; while mounted it still runs', async () => {
  const sb = makeSandbox();
  const g = sb.build();
  sb.map.addLayer(g);
  sb.runScripts('ok'); await flush();
  const layer = sb.stats.glLayers[0];

  layer._transitionEnd({});
  sb.runFrames(); sb.runFrames();
  assert.strictEqual(layer.jumps, 1, 'frame work runs while mounted');
  assert.strictEqual(sb.L.Util.requestAnimFrame, sb.realRequestAnimFrame, 'Leaflet frame helper restored');

  layer._transitionEnd({}); // transitionend, then the layer leaves before the frame
  sb.map.removeLayer(g);
  assert.strictEqual(layer._map, null);
  assert.doesNotThrow(() => { sb.runFrames(); sb.runFrames(); });
  assert.doesNotThrow(() => { layer._transitionEnd({}); sb.runFrames(); sb.runFrames(); }, 'called after onRemove');
  assert.strictEqual(layer.jumps, 1);
  assert.strictEqual(sb.L.Util.requestAnimFrame, sb.realRequestAnimFrame);
});

test('S4: setRTLTextPlugin is never referenced', () => {
  assert.ok(!/setRTLTextPlugin/.test(SRC), 'map-vector.js mentions setRTLTextPlugin');
});

// ── transient fallback: retry once the library is ready ────────────────
test('fallback reason is exposed; only a transient one is stale, and only once MapLibre is ready', async () => {
  const sb = makeSandbox();
  const stale = sb.window.MC_vectorLayerStale;
  assert.strictEqual(typeof stale, 'function');
  const g = sb.build();
  sb.map.addLayer(g);
  assert.strictEqual(g.mcFallbackReason, null, 'no fallback yet');
  assert.strictEqual(stale(g), false);
  sb.advance(15000); await flush();
  assert.strictEqual(g.mcFallbackReason, 'load-timeout');
  assert.strictEqual(stale(g), false, 'not stale while MapLibre is still missing');
  sb.runScripts('ok'); await flush();
  assert.strictEqual(stale(g), true, 'stale once MapLibre has arrived');
  assert.deepStrictEqual(g.getLayers(), [sb.stats.rasters[0]], 'never rebuilds itself: the caller decides');
  assert.strictEqual(sb.stats.glMaps.length, 0);
  assert.strictEqual(stale(sb.build()), false, 'a fresh layer is not stale');
  assert.strictEqual(stale(null), false);
  assert.strictEqual(stale({}), false, 'a non-vector layer is not stale');
});

test('a load-failed fallback is stale once a later retry loads MapLibre', async () => {
  const sb = makeSandbox();
  const g = sb.build();
  sb.map.addLayer(g);
  sb.runScripts('error'); await flush();
  assert.strictEqual(g.mcFallbackReason, 'load-failed');
  assert.strictEqual(sb.window.MC_vectorLayerStale(g), false);
  sb.window.MC_loadMapLibre();
  sb.runScripts('ok'); await flush();
  assert.strictEqual(sb.window.MC_vectorLayerStale(g), true);
});

test('permanent fallbacks are never stale: no WebGL, refused URL, style error, init failure', async () => {
  const cases = [
    ['webgl-unavailable', { noWebGL: true }, {}],
    ['style-url-refused', {}, { url: 'javascript:alert(1)' }],
    ['init-failed', { glCtorThrows: true }, {}],
  ];
  for (const [reason, sbOpts, desc] of cases) {
    const sb = makeSandbox(sbOpts);
    const g = sb.build(desc);
    sb.map.addLayer(g);
    sb.window.MC_loadMapLibre();
    sb.runScripts('ok'); await flush();
    assert.strictEqual(g.mcFallbackReason, reason);
    assert.strictEqual(sb.window.MC_vectorLayerStale(g), false, reason + ' with MapLibre ready');
  }
  const sb = makeSandbox();
  const g = sb.build();
  sb.map.addLayer(g);
  sb.runScripts('ok'); await flush();
  sb.stats.glMaps[0].fire('error', { error: new Error('404') });
  assert.strictEqual(g.mcFallbackReason, 'style-error');
  assert.strictEqual(sb.window.MC_vectorLayerStale(g), false, 'style-error');
});

(async () => {
  for (const [name, fn] of queue) {
    try { await fn(); passed++; console.log('  ✅ ' + name); }
    catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
  }
  console.log(`\nmap-vector: ${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})();
