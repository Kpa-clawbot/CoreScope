/**
 * Seeded race-condition stress suite for the #2428 map-provider code
 * (map-tile-providers.js, map-vector.js, map-hillshade.js and the surfaces
 * that use MC_attachAutoTiles / _applyTilesToNodeMap).
 *
 * A seeded PRNG drives every action order and timing jitter, so a failing
 * (seed, scenario, iteration) triple replays the same sequence. Each
 * iteration derives its own sub-seed from (seed, scenario, iteration), so
 * MAP_STRESS_ONLY=c:7 replays iteration 7 of scenario c alone.
 *
 * Scenarios:
 *   a  late /api/config/client (0-3 s), incl. a config that disables the
 *      stored provider's group and adds custom entries
 *   b  slow MapLibre (1-6 s): vector pick then raster / theme flip /
 *      navigate away / another vector / map->planner; plus, once per run,
 *      the 15 s load-timeout path followed by a successful pick
 *   c  rapid toggling: 30-60 picker/theme/overlay/Auto actions, 0-120 ms apart
 *   d  hillshade under fast zoom/pan with slow terrarium tiles, navigate away
 *      mid-burst; JS heap after forced GC per iteration: total growth
 *      after iteration 0 <= 10 MB, and <= 3 MB over the second half of the
 *      run (>= 10 iterations). Never traced: tracing retains page heap.
 *   e  two tabs: overlay/provider changes mirror through 'storage', no loops
 *   f  navigation churn over map, live, planner, node detail, node reach,
 *      home (cycles = clamp(ITER * 2.5, 5, 50)). A visit to the route the
 *      page is already on rebuilds nothing, so a basemap picked on the last
 *      visit is still expected.
 *
 * The planner is opt-in: when /api/config/client has no `planner` block (the
 * CI fixture server), planner steps navigate to #/map instead.
 *
 * Invariants after every iteration: no page errors / console errors; one
 * layers control, one live auto tile group and one overlay pane per picker
 * map; no auto group left alive for a removed map; the picker's radio, the
 * layer on the map, the tile-pane filter and the overlays all match the last
 * requested state; live WebGL contexts <= maps showing a vector basemap;
 * window listener counts back at the post-first-load level after returning
 * to the same page.
 *
 * Env:
 *   BASE_URL          default http://localhost:13581
 *   MAP_STRESS_ITER   iterations per scenario (default 2; 20 for the gate)
 *   MAP_STRESS_SEED   integer or string (default 20261118)
 *   MAP_STRESS_ONLY   'a,c' or 'c:7' (scenario[:iteration])
 *   MAP_STRESS_TRACE  directory: record a Playwright trace per scenario
 *                     (except d) and keep it when the scenario fails
 *   CHROMIUM_PATH     optional browser binary
 * Results: test-results/map-perf/stress/<seed>.json
 *
 * Usage: BASE_URL=http://localhost:13581 MAP_STRESS_ITER=20 node tests/e2e/test-map-race-stress-e2e.js
 */
'use strict';
const fs = require('fs');
const path = require('path');
const { chromium } = require('playwright');
const MH = require('./lib/map-harness');

const BASE = (process.env.BASE_URL || 'http://localhost:13581').replace(/\/+$/, '');
const ITER = Math.max(1, parseInt(process.env.MAP_STRESS_ITER || '2', 10) || 2);
const SEED_RAW = process.env.MAP_STRESS_SEED || '20261118';
const SEED = /^\d+$/.test(SEED_RAW) ? (Number(SEED_RAW) >>> 0) : hash32(SEED_RAW);
const TRACE_DIR = process.env.MAP_STRESS_TRACE || '';
const OUT_DIR = path.join(__dirname, '..', '..', 'test-results', 'map-perf', 'stress');
const CHURN_CYCLES = Math.min(50, Math.max(5, Math.floor(ITER * 2.5)));
const ONLY = parseOnly(process.env.MAP_STRESS_ONLY || '');

// ── Seeded PRNG ─────────────────────────────────────────────────────────────
function hash32(str) {
  let h = 2166136261;
  for (let i = 0; i < str.length; i++) { h ^= str.charCodeAt(i); h = Math.imul(h, 16777619); }
  return h >>> 0;
}
function mulberry32(a) {
  return function () {
    a = (a + 0x6D2B79F5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
function makeRng(...parts) {
  const r = mulberry32(hash32(String(SEED) + ':' + parts.join(':')));
  const rng = {
    next: r,
    int: (lo, hi) => lo + Math.floor(r() * (hi - lo + 1)),
    pick: (arr) => arr[Math.floor(r() * arr.length)],
    chance: (p) => r() < p,
    shuffle: (arr) => { const a = arr.slice(); for (let i = a.length - 1; i > 0; i--) { const j = Math.floor(r() * (i + 1)); [a[i], a[j]] = [a[j], a[i]]; } return a; },
  };
  return rng;
}

function parseOnly(s) {
  if (!s.trim()) return null;
  const out = {};
  s.split(',').map(x => x.trim()).filter(Boolean).forEach((tok) => {
    const [sc, it] = tok.split(':');
    out[sc] = out[sc] || [];
    if (it != null && it !== '') out[sc].push(parseInt(it, 10));
  });
  return out;
}
function iterationsFor(sc) {
  if (ONLY && !ONLY[sc]) return [];
  if (ONLY && ONLY[sc].length) return ONLY[sc];
  return Array.from({ length: ITER }, (_, i) => i);
}

const sleep = (ms) => new Promise(r => setTimeout(r, ms));

// ── In-page instrumentation (runs before any app script) ────────────────────
function stressInit() {
  /* eslint-disable no-var */
  if (window.__mcStress) return;
  var TYPES = { 'mc-tile-provider-changed': 1, 'mc-tile-overlays-changed': 1, 'storage': 1 };
  var WATCH_KEYS = { 'mc-tile-overlays': 1, 'mc-dark-tile-provider': 1, 'mc-light-tile-provider': 1 };
  var S = window.__mcStress = {
    listeners: {},      // type -> Map(fn -> { caps: {true/false: 1}, site })
    events: {},         // dispatched on window, by type
    storageIn: {},      // storage events received, by key
    setItem: {},        // localStorage.setItem calls, by watched key
    gl: [],             // { ctx: WeakRef, canvas: WeakRef }
    glCreated: 0,
    autoHandles: [],    // { map, destroyed, group }
    controls: [],       // L.Control.Layers instances
    maps: [],           // every L.Map created
    warns: [],          // console.warn texts from the map modules
  };
  var origAdd = EventTarget.prototype.addEventListener;
  var origRemove = EventTarget.prototype.removeEventListener;
  var origDispatch = EventTarget.prototype.dispatchEvent;
  function capOf(o) { return typeof o === 'boolean' ? o : !!(o && o.capture); }
  function site() {
    var lines = String(new Error().stack || '').split('\n').slice(2);
    for (var i = 0; i < lines.length; i++) {
      if (/__mcStress|stressInit|addEventListener/.test(lines[i])) continue;
      return lines[i].trim().replace(/\?v=\d+/, '');
    }
    return '?';
  }
  window.addEventListener = function (type, fn, opts) {
    var target = this || window;
    if (target === window && TYPES[type] && fn && !(opts && opts.once)) {
      var m = S.listeners[type] || (S.listeners[type] = new Map());
      var e = m.get(fn) || { caps: {}, site: site() };
      e.caps[capOf(opts)] = 1;
      m.set(fn, e);
    }
    return origAdd.call(target, type, fn, opts);
  };
  window.removeEventListener = function (type, fn, opts) {
    var target = this || window;
    if (target === window && TYPES[type] && S.listeners[type]) {
      var m = S.listeners[type];
      var e = m.get(fn);
      if (e) { delete e.caps[capOf(opts)]; if (!Object.keys(e.caps).length) m.delete(fn); }
    }
    return origRemove.call(target, type, fn, opts);
  };
  window.dispatchEvent = function (ev) {
    var target = this || window;
    if (target === window && ev && TYPES[ev.type]) S.events[ev.type] = (S.events[ev.type] || 0) + 1;
    return origDispatch.call(target, ev);
  };
  origAdd.call(window, 'storage', function (e) { S.storageIn[e.key] = (S.storageIn[e.key] || 0) + 1; });
  var origSet = Storage.prototype.setItem;
  Storage.prototype.setItem = function (k, v) {
    if (this === window.localStorage && WATCH_KEYS[k]) S.setItem[k] = (S.setItem[k] || 0) + 1;
    return origSet.apply(this, arguments);
  };
  var origWarn = console.warn;
  console.warn = function () {
    try { var t = Array.prototype.map.call(arguments, String).join(' '); if (/map-vector|\[tiles\]|hillshade/i.test(t) && S.warns.length < 200) S.warns.push(t); } catch (_) {}
    return origWarn.apply(this, arguments);
  };

  var glSeen = new WeakSet();
  var origGet = HTMLCanvasElement.prototype.getContext;
  HTMLCanvasElement.prototype.getContext = function (type) {
    var ctx = origGet.apply(this, arguments);
    if (ctx && /webgl/i.test(String(type)) && !glSeen.has(ctx)) {
      glSeen.add(ctx);
      S.glCreated++;
      S.gl.push({ ctx: new WeakRef(ctx), canvas: new WeakRef(this) });
    }
    return ctx;
  };
  S.glState = function () {
    var live = 0, attached = 0, detached = 0;
    S.gl = S.gl.filter(function (g) { return !!g.ctx.deref(); });
    S.gl.forEach(function (g) {
      var c = g.ctx.deref();
      var lost = true;
      try { lost = c.isContextLost(); } catch (_) {}
      if (lost) return;
      live++;
      var cv = g.canvas.deref();
      if (cv && cv.isConnected) attached++; else detached++;
    });
    return { live: live, attached: attached, detached: detached, created: S.glCreated };
  };
  S.listenerCounts = function () {
    var out = {};
    Object.keys(TYPES).forEach(function (t) {
      var n = 0;
      if (S.listeners[t]) S.listeners[t].forEach(function (e) { n += Object.keys(e.caps).length; });
      out[t] = n;
    });
    return out;
  };
  S.listenerSites = function (type) {
    var out = {};
    if (S.listeners[type]) S.listeners[type].forEach(function (e) { out[e.site] = (out[e.site] || 0) + Object.keys(e.caps).length; });
    return out;
  };

  // Hooks that need the app's globals: installed before app.js's own
  // DOMContentLoaded handler (registered first), i.e. before any route runs.
  origAdd.call(document, 'DOMContentLoaded', function () {
    var L = window.L;
    if (L && L.Map && L.Control && L.Control.Layers) {
      // Flag removal on the prototype: MC_safeRemoveMap calls map.off() first,
      // so an 'unload' listener would never fire.
      var origMapRemove = L.Map.prototype.remove;
      L.Map.prototype.remove = function () { this._mcRemoved = true; return origMapRemove.apply(this, arguments); };
      L.Map.addInitHook(function () { S.prune(); S.maps.push(this); });
      L.Control.Layers.addInitHook(function () { S.controls.push(this); });
    }
    var attach = window.MC_attachAutoTiles;
    if (typeof attach === 'function') {
      window.MC_attachAutoTiles = function (map, opts) {
        var h = attach.apply(this, arguments);
        var rec = { map: map, destroyed: false, group: h && h.group, picker: !!(opts && opts.controlPosition) };
        if (h && h.group) h.group.__mcStressAuto = rec;
        if (h && typeof h.destroy === 'function') {
          var d = h.destroy;
          h.destroy = function () { rec.destroyed = true; return d.apply(this, arguments); };
        }
        S.prune();
        S.autoHandles.push(rec);
        return h;
      };
    }
  }, true);

  function isLive(m) { return !!(m && !m._destroyed && !m._mcRemoved && m._container && m._container.isConnected); }
  S.liveMaps = function () { return S.maps.filter(isLive); };
  // The recording arrays must not pin removed maps (and the hillshade scratch
  // buffers their layers hold): that would inflate the heap being measured.
  // Only entries that can no longer take part in any invariant are dropped:
  // destroyed auto handles, controls detached from a removed/dead map, and
  // maps that were removed (or were connected and no longer are) with no
  // still-undestroyed auto handle (that one is a leak globalCheck must see).
  S.prune = function () {
    S.autoHandles = S.autoHandles.filter(function (r) { return !r.destroyed; });
    S.maps.forEach(function (m) { if (isLive(m)) m._mcSeen = true; });
    S.controls = S.controls.filter(function (c) {
      if (c._map) return isLive(c._map) || S.maps.indexOf(c._map) < 0;
      return !(c._container && !c._container.isConnected);
    });
    S.maps = S.maps.filter(function (m) {
      if (isLive(m)) return true;
      if (!(m._mcRemoved || m._mcSeen)) return true; // not attached yet
      return S.autoHandles.some(function (r) { return r.map === m; }) ||
        S.controls.some(function (c) { return c._map === m; });
    });
  };
  function controlOf(m) { return S.controls.filter(function (c) { return c._map === m; }); }
  S.pickerMaps = function () {
    return S.liveMaps().filter(function (m) {
      return S.autoHandles.some(function (r) { return r.map === m && r.picker && !r.destroyed; });
    });
  };

  function leaves(l, out) {
    out = out || [];
    if (!l) return out;
    if (typeof l.getMaplibreMap === 'function') { out.push({ kind: 'gl', url: l.options && l.options.style, mounted: !!l.getMaplibreMap(), layer: l }); return out; }
    if (window.L && l instanceof L.TileLayer) { out.push({ kind: 'raster', url: l._url, layer: l }); return out; }
    if (window.L && l instanceof L.GridLayer) { out.push({ kind: 'grid', url: l._url, layer: l }); return out; }
    if (typeof l.getLayers === 'function') { l.getLayers().forEach(function (c) { leaves(c, out); }); return out; }
    out.push({ kind: 'other', layer: l });
    return out;
  }
  function urlOf(p) { var u = p && (p.url || p.baseUrl); return typeof u === 'function' ? u() : u; }
  var normEl = null;
  function normFilter(f) {
    normEl = normEl || document.createElement('div');
    normEl.style.filter = '';
    normEl.style.filter = f || '';
    return normEl.style.filter;
  }
  function isDarkNow() { return typeof window.MC_isDarkTheme === 'function' ? !!window.MC_isDarkTheme() : document.documentElement.getAttribute('data-theme') === 'dark'; }

  // The basemap a picker map should show for `intent`
  // ({ mode: 'auto'|'explicit', id, overlays: [ids]|null, allowFallback }).
  // Returns a list of human-readable problems (empty = consistent).
  S.checkMap = function (m, intent) {
    var probs = [];
    var REG = window.MC_TILE_PROVIDERS || {};
    var dark = isDarkNow();
    var tag = (m._container.id || m._container.className.split(' ')[0] || 'map') + ': ';
    var ctrls = controlOf(m);
    var domCtrls = m._container.querySelectorAll('.leaflet-control-layers').length;
    if (ctrls.length !== 1 || domCtrls !== 1) probs.push(tag + 'layers controls instances=' + ctrls.length + ' dom=' + domCtrls);
    var autos = S.autoHandles.filter(function (r) { return r.map === m && !r.destroyed; });
    if (autos.length !== 1) probs.push(tag + 'live auto groups=' + autos.length);
    var panes = m._container.querySelectorAll('.leaflet-mcOverlay-pane').length;
    if (panes !== 1) probs.push(tag + 'overlay panes=' + panes);
    var autoOnMap = [];
    m.eachLayer(function (l) { if (l.__mcStressAuto) autoOnMap.push(l); });
    var c = ctrls[0];
    if (!c || !autos.length) return probs;
    var want = intent.mode === 'explicit' ? intent.id : '__auto__';
    var bases = c._layers.filter(function (x) { return !x.overlay && m.hasLayer(x.layer); }).map(function (x) { return x.name; });
    if (bases.length !== 1 || bases[0] !== want) probs.push(tag + 'base layers on map ' + JSON.stringify(bases) + ' want ' + want);
    var radios = Array.prototype.filter.call(c.getContainer().querySelectorAll('.leaflet-control-layers-base input'), function (i) { return i.checked; })
      .map(function (i) { var lab = i.closest('label'); return lab && lab.getAttribute('data-tile-id'); });
    if (radios.length !== 1 || radios[0] !== want) probs.push(tag + 'checked radio ' + JSON.stringify(radios) + ' want ' + want);
    if ((want === '__auto__') !== (autoOnMap.length === 1) || autoOnMap.length > 1) probs.push(tag + 'auto groups on map=' + autoOnMap.length + ' mode=' + intent.mode);

    // What should be drawn.
    var exp, entry, layer;
    if (want === '__auto__') {
      var r = window.MC_resolveTile(dark);
      entry = REG[r.id] || null;
      exp = { url: r.url, format: r.format, id: r.id, refUrl: r.refUrl };
      layer = autos[0].group;
    } else {
      entry = REG[want];
      if (!entry) { probs.push(tag + 'explicit id ' + want + ' not registered'); return probs; }
      exp = { url: urlOf(entry), format: entry.format || 'raster', id: want, refUrl: entry.refUrl || null };
      var item = c._layers.filter(function (x) { return x.name === want; })[0];
      layer = item && item.layer;
    }
    var lv = leaves(layer);
    if (exp.format === 'vector') {
      var gl = lv.filter(function (x) { return x.kind === 'gl'; });
      var ras = lv.filter(function (x) { return x.kind === 'raster'; });
      if (gl.length === 1 && ras.length === 0) {
        if (gl[0].url !== exp.url) probs.push(tag + 'GL style ' + gl[0].url + ' want ' + exp.url);
        if (!gl[0].mounted) probs.push(tag + 'GL layer not mounted');
      } else if (gl.length === 0 && ras.length === 1 && intent.allowFallback) {
        // raster fallback accepted for this intent
      } else {
        probs.push(tag + 'vector ' + exp.id + ' shows ' + JSON.stringify(lv.map(function (x) { return x.kind + ':' + x.url; })));
      }
    } else {
      var wantN = exp.refUrl ? 2 : 1;
      var urls = lv.map(function (x) { return x.kind + ':' + x.url; });
      if (lv.length !== wantN || lv[0].kind !== 'raster' || lv[0].url !== exp.url) probs.push(tag + 'raster ' + exp.id + ' shows ' + JSON.stringify(urls) + ' want ' + exp.url);
    }
    lv.forEach(function (x) { if (x.layer && !m.hasLayer(x.layer)) probs.push(tag + 'leaf ' + x.kind + ' not on map'); });

    // Tile-pane CSS filter.
    var pane = m.getPane('tilePane');
    var wantF;
    if (want === '__auto__') {
      var aid = dark ? window.MC_getDarkTileProvider() : window.MC_getLightTileProvider();
      var ap = REG[aid];
      wantF = !ap ? '' : (ap.filter || ((dark && ap.type !== 'any' && ap.invertFilter) ? ap.invertFilter : ''));
    } else {
      wantF = entry.filter || (entry.type === 'any' ? '' : (entry.invertFilter || ''));
    }
    if (normFilter(pane.style.filter) !== normFilter(wantF)) probs.push(tag + 'tile-pane filter "' + pane.style.filter + '" want "' + wantF + '"');
    var explicitAttr = pane.getAttribute('data-explicit-layer') === 'true';
    if (explicitAttr !== (want !== '__auto__')) probs.push(tag + 'data-explicit-layer=' + explicitAttr + ' mode=' + intent.mode);

    // Overlays.
    var stored = (window.MC_getTileOverlays() || []).slice().sort();
    if (intent.overlays) {
      var w = intent.overlays.slice().sort();
      if (JSON.stringify(stored) !== JSON.stringify(w)) probs.push(tag + 'stored overlays ' + JSON.stringify(stored) + ' want ' + JSON.stringify(w));
    }
    var ovOn = c._layers.filter(function (x) { return x.overlay && m.hasLayer(x.layer); }).map(function (x) { return x.name; }).sort();
    if (JSON.stringify(ovOn) !== JSON.stringify(stored)) probs.push(tag + 'overlays on map ' + JSON.stringify(ovOn) + ' stored ' + JSON.stringify(stored));
    var ovChecked = Array.prototype.filter.call(c.getContainer().querySelectorAll('.leaflet-control-layers-overlays input'), function (i) { return i.checked; })
      .map(function (i) { var lab = i.closest('label'); return lab && lab.getAttribute('data-tile-overlay-id'); }).sort();
    if (JSON.stringify(ovChecked) !== JSON.stringify(stored)) probs.push(tag + 'overlay checkboxes ' + JSON.stringify(ovChecked) + ' stored ' + JSON.stringify(stored));
    // Every overlay layer on the map is one the control lists (no orphan stacked overlays).
    var paneEl = m.getPane('mcOverlayPane');
    if (paneEl) {
      var listed = c._layers.filter(function (x) { return x.overlay && m.hasLayer(x.layer); }).length;
      var inPane = 0;
      m.eachLayer(function (l) { if (l.options && l.options.pane === 'mcOverlayPane') inPane++; });
      if (inPane !== listed) probs.push(tag + 'layers in overlay pane=' + inPane + ' listed=' + listed);
    }
    var glc = m._container.querySelectorAll('canvas.maplibregl-canvas').length;
    if (glc > 1) probs.push(tag + 'GL canvases in map=' + glc);
    return probs;
  };

  // Page-wide invariants (independent of intent).
  S.globalCheck = function () {
    var probs = [];
    var live = S.liveMaps();
    S.autoHandles.forEach(function (r) {
      if (!r.destroyed && !isLive(r.map)) probs.push('auto tile group alive for a removed map');
    });
    S.prune();
    var pick = S.pickerMaps();
    var panes = 0;
    pick.forEach(function (m) { panes += m._container.querySelectorAll('.leaflet-mcOverlay-pane').length; });
    if (panes !== pick.length) probs.push('overlay panes=' + panes + ' picker maps=' + pick.length);
    var vecMaps = 0;
    live.forEach(function (m) {
      var has = false;
      m.eachLayer(function (l) { if (typeof l.getMaplibreMap === 'function' && l.getMaplibreMap()) has = true; });
      if (has) vecMaps++;
    });
    var gs = S.glState();
    if (gs.live > vecMaps) probs.push('live WebGL contexts=' + gs.live + ' (attached ' + gs.attached + ', detached ' + gs.detached + ') > maps showing vector=' + vecMaps);
    var nts = window.__mcNodeTileSync;
    if (nts && nts.maps) {
      var stale = nts.maps.filter(function (rec) { return !isLive(rec.map); }).length;
      if (stale) probs.push('node tile-sync records for removed maps=' + stale);
    }
    // Detached GL controls / layers controls stuck in the DOM outside a live map.
    var orphanCtrl = Array.prototype.filter.call(document.querySelectorAll('.leaflet-control-layers'), function (el) {
      return !live.some(function (m) { return m._container.contains(el); });
    }).length;
    if (orphanCtrl) probs.push('layers controls outside a live map=' + orphanCtrl);
    return probs;
  };

  S.snapshot = function () {
    S.prune();
    var live = S.liveMaps();
    var vecMaps = 0;
    live.forEach(function (m) { var h = false; m.eachLayer(function (l) { if (typeof l.getMaplibreMap === 'function' && l.getMaplibreMap()) h = true; }); if (h) vecMaps++; });
    return {
      listeners: S.listenerCounts(),
      gl: S.glState(),
      vecMaps: vecMaps,
      maps: live.length,
      pickers: S.pickerMaps().length,
      controls: document.querySelectorAll('.leaflet-control-layers').length,
      overlayPanes: document.querySelectorAll('.leaflet-mcOverlay-pane').length,
      autoLive: S.autoHandles.filter(function (r) { return !r.destroyed; }).length,
      nodeTileRecs: (window.__mcNodeTileSync && window.__mcNodeTileSync.maps) ? window.__mcNodeTileSync.maps.length : 0,
      tileEls: document.querySelectorAll('.leaflet-tile').length,
      events: Object.assign({}, S.events),
      storageIn: Object.assign({}, S.storageIn),
      setItem: Object.assign({}, S.setItem),
    };
  };

  // Picker actions (the input's own click handler, as Leaflet wires it).
  function pickerOf(idx) { var p = S.pickerMaps(); var m = p[idx || 0]; return m ? controlOf(m)[0] : null; }
  S.baseIds = function () {
    var c = pickerOf(0); if (!c) return [];
    return Array.prototype.map.call(c.getContainer().querySelectorAll('.leaflet-control-layers-base label[data-tile-id]'), function (l) { return l.getAttribute('data-tile-id'); });
  };
  S.overlayIds = function () {
    var c = pickerOf(0); if (!c) return [];
    return Array.prototype.map.call(c.getContainer().querySelectorAll('.leaflet-control-layers-overlays label[data-tile-overlay-id]'), function (l) { return l.getAttribute('data-tile-overlay-id'); });
  };
  S.clickBase = function (id) {
    var c = pickerOf(0); if (!c) return 'no-picker';
    var lab = c.getContainer().querySelector('.leaflet-control-layers-base label[data-tile-id="' + CSS.escape(id) + '"]');
    var inp = lab && lab.querySelector('input');
    if (!inp) return 'missing';
    if (inp.disabled) return 'disabled';
    inp.click();
    return 'ok';
  };
  S.clickOverlay = function (id) {
    var c = pickerOf(0); if (!c) return null;
    var lab = c.getContainer().querySelector('.leaflet-control-layers-overlays label[data-tile-overlay-id="' + CSS.escape(id) + '"]');
    var inp = lab && lab.querySelector('input');
    if (!inp || inp.disabled) return null;
    inp.click();
    return inp.checked;
  };
  S.flipTheme = function () {
    var root = document.documentElement;
    var next = root.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
    var cb = document.getElementById('darkModeCheckbox');
    if (cb) { cb.checked = next === 'dark'; cb.dispatchEvent(new Event('change', { bubbles: true })); }
    else { var btn = document.getElementById('darkModeToggle'); if (btn) btn.click(); }
    if (root.getAttribute('data-theme') !== next) { root.setAttribute('data-theme', next); try { localStorage.setItem('meshcore-theme', next); } catch (_) {} }
    return next;
  };
  S.vectorIds = function () { var R = window.MC_TILE_PROVIDERS || {}; return Object.keys(R).filter(function (k) { return R[k].format === 'vector'; }); };
  S.fits = function (theme) { var R = window.MC_TILE_PROVIDERS || {}; return Object.keys(R).filter(function (k) { return R[k].type === theme || R[k].type === 'any'; }); };
}

// ── Page plumbing ───────────────────────────────────────────────────────────
const IGNORED_CONSOLE = [
  // Every non-map external request is aborted by the stub (by design).
  /net::ERR_BLOCKED_BY_CLIENT/,
];

// opts.trace === false: never trace this context. Playwright tracing with
// snapshots retains page heap (~0.75 MB per navigation), so a context whose
// heap is measured must not be traced.
// The #2428 groups are opt-in (off unless enabled:true); every page here
// runs with them on so the scenarios keep exercising vector, EOX, OSM
// France and hillshade. A group the config already sets is left as it is.
const OPT_IN_GROUPS = ['eox', 'osmfr', 'openfreemap', 'versatiles', 'hillshade'];
function withOptInGroups(cfg) {
  cfg.map = cfg.map || {};
  const tiles = cfg.map.tiles = cfg.map.tiles || {};
  tiles.providers = tiles.providers || {};
  for (const g of OPT_IN_GROUPS) {
    if (!tiles.providers[g] || tiles.providers[g].enabled === undefined) tiles.providers[g] = Object.assign({}, tiles.providers[g], { enabled: true });
  }
  return cfg;
}
async function routeOptInConfig(target) {
  await target.route('**/api/config/client', async (route) => {
    try {
      const resp = await route.fetch();
      const body = JSON.stringify(withOptInGroups(JSON.parse(await resp.text())));
      await route.fulfill({ status: resp.status(), headers: Object.assign({}, resp.headers(), { 'content-length': String(Buffer.byteLength(body)) }), body });
    } catch (_) { /* page closed */ }
  });
}

async function openContext(browser, label, opts) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, serviceWorkers: 'block' });
  await routeOptInConfig(context);
  context.__tracing = !!TRACE_DIR && !(opts && opts.trace === false);
  if (context.__tracing) await context.tracing.start({ screenshots: true, snapshots: true, sources: false });
  context.__label = label;
  context.__stubbed = false;
  return context;
}

async function openPage(context, opts) {
  opts = opts || {};
  const page = await context.newPage();
  page.setDefaultTimeout(20000);
  page.__errors = [];
  page.on('pageerror', (e) => page.__errors.push('pageerror: ' + (e && (e.stack || e.message) || e)));
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return;
    const t = msg.text();
    if (IGNORED_CONSOLE.some(re => re.test(t))) return;
    const loc = msg.location && msg.location();
    // Data-API status noise (e.g. live.js probing /api/nodes/reported -> 404)
    // is the shared server's business, not the map code's.
    if (/^Failed to load resource: the server responded/.test(t) && loc && /\/api\//.test(loc.url || '')) return;
    page.__errors.push('console.error: ' + t + (loc && loc.url ? ' @' + loc.url.replace(BASE, '') + ':' + loc.lineNumber : ''));
  });
  await MH.installCollectors(page);
  await page.addInitScript(stressInit);
  if (!context.__stubbed) {
    await MH.installTileStub(page, { mode: 'stub', baseUrl: BASE });
    context.__stubbed = true;
  }
  if (opts.preset) await MH.presetStorage(page, opts.preset);
  return page;
}

async function closeContext(context, failed, name) {
  if (context.__tracing) {
    try {
      if (failed) {
        fs.mkdirSync(TRACE_DIR, { recursive: true });
        const p = path.join(TRACE_DIR, `trace-${SEED}-${name}.zip`);
        await context.tracing.stop({ path: p });
        console.log(`      trace: ${p}`);
      } else {
        await context.tracing.stop();
      }
    } catch (_) { /* context gone */ }
  }
  await context.close().catch(() => {});
}

const PICKER_ROUTES = ['#/map', '#/live', '#/tools/planner'];

async function gotoFirst(page, route) {
  route = effectiveRoute(route);
  await page.goto(`${BASE}/${route}`, { waitUntil: 'domcontentloaded' });
}
// The planner is opt-in (planner.enabled): /api/config/client carries a
// `planner` block only when it is on (the CI fixture server has it off). When
// it is off, planner steps go to the map page instead; the seeded action
// sequence is the same either way.
let PLANNER = true;
const PLANNER_ROUTE = '#/tools/planner';
async function detectPlanner() {
  const r = await fetch(BASE + '/api/config/client', { signal: AbortSignal.timeout(30000) });
  if (!r.ok) throw new Error('/api/config/client ' + r.status);
  const cfg = await r.json();
  PLANNER = !!(cfg && cfg.planner);
  if (!PLANNER) results.notes.push('planner disabled on this server: planner steps use #/map');
}
function effectiveRoute(route) {
  return (!PLANNER && route === PLANNER_ROUTE) ? '#/map' : route;
}
async function hashNav(page, route) {
  await page.evaluate((h) => { location.hash = h; }, effectiveRoute(route));
}
/** Wait for a picker surface: exactly one picker map with its control. */
async function waitPicker(page, timeout) {
  await page.waitForFunction(() => {
    const S = window.__mcStress;
    return !!S && S.pickerMaps().length === 1 && S.baseIds().length > 0;
  }, null, { timeout: timeout || 25000, polling: 50 });
}
async function waitAnyMap(page, timeout) {
  try {
    await page.waitForFunction(() => window.__mcStress && window.__mcStress.liveMaps().length > 0, null, { timeout: timeout || 20000, polling: 100 });
    return true;
  } catch (e) {
    if (!/Timeout/i.test(String(e && e.message))) throw e;
    return false;
  }
}

/**
 * Settle then verify: waits minMs, then polls the in-page checks until they
 * pass or maxMs elapses. Returns { problems, convergedMs }.
 */
async function settleCheck(page, intent, o) {
  o = o || {};
  const minMs = o.minMs != null ? o.minMs : 2000;
  const maxMs = o.maxMs != null ? o.maxMs : 8000;
  await sleep(minMs);
  const t0 = Date.now();
  let problems = [];
  for (;;) {
    problems = await page.evaluate((it) => {
      const S = window.__mcStress;
      const p = S.globalCheck();
      const maps = S.pickerMaps();
      if (it && it.pickers != null && maps.length !== it.pickers) p.push('picker maps=' + maps.length + ' want ' + it.pickers);
      if (it && it.theme && document.documentElement.getAttribute('data-theme') !== it.theme) p.push('data-theme=' + document.documentElement.getAttribute('data-theme') + ' want ' + it.theme);
      if (it) maps.forEach((m) => { p.push.apply(p, S.checkMap(m, it)); });
      return p;
    }, intent || null);
    if (!problems.length || Date.now() - t0 >= maxMs) break;
    await sleep(150);
  }
  try { noteMax(await snapshot(page)); } catch (_) { /* page gone */ }
  return { problems, convergedMs: minMs + (Date.now() - t0) };
}

async function snapshot(page) { return page.evaluate(() => window.__mcStress.snapshot()); }
async function gcGl(page) {
  const cdp = await page.context().newCDPSession(page);
  try { await cdp.send('HeapProfiler.collectGarbage'); } finally { await cdp.detach().catch(() => {}); }
  return page.evaluate(() => window.__mcStress.glState());
}

// ── Results bookkeeping ─────────────────────────────────────────────────────
const results = {
  seed: SEED, seedRaw: SEED_RAW, iterations: ITER, churnCycles: CHURN_CYCLES, base: BASE,
  only: process.env.MAP_STRESS_ONLY || null, startedAt: new Date().toISOString(),
  scenarios: {}, maxima: { listeners: {}, glLive: 0, glCreated: 0 }, notes: [],
};
function scen(name, title) {
  if (!results.scenarios[name]) results.scenarios[name] = { title, iterations: 0, passed: 0, failed: 0, failures: [], durationMs: 0, details: {} };
  return results.scenarios[name];
}
function noteMax(snap) {
  if (!snap) return;
  Object.keys(snap.listeners || {}).forEach((k) => { results.maxima.listeners[k] = Math.max(results.maxima.listeners[k] || 0, snap.listeners[k]); });
  if (snap.gl) {
    results.maxima.glLive = Math.max(results.maxima.glLive, snap.gl.live);
    results.maxima.glCreated = Math.max(results.maxima.glCreated, snap.gl.created);
  }
}

/** Run one iteration: fn(ctx) returns problems[]; context closed after. */
async function runIteration(browser, name, it, fn) {
  const sc = results.scenarios[name];
  const rng = makeRng(name, it);
  const context = await openContext(browser, `${name}:${it}`);
  const t0 = Date.now();
  let problems = [];
  const pages = [];
  const meta = {};
  try {
    problems = await fn({ context, rng, it, pages, meta, open: async (o) => { const p = await openPage(context, o); pages.push(p); return p; } });
  } catch (e) {
    problems = ['exception: ' + (e && e.stack ? e.stack.split('\n').slice(0, 4).join(' | ') : e)];
  }
  for (const p of pages) {
    if (p.__errors.length) problems = problems.concat(p.__errors.slice(0, 10));
    try { noteMax(await snapshot(p)); } catch (_) { /* closed */ }
  }
  sc.iterations++;
  const ms = Date.now() - t0;
  if (problems.length && Object.keys(meta).length) problems.push('meta: ' + JSON.stringify(meta));
  if (problems.length) {
    sc.failed++;
    sc.failures.push({ iteration: it, repro: `MAP_STRESS_SEED=${SEED_RAW} MAP_STRESS_ONLY=${name}:${it}`, problems: problems.slice(0, 25) });
    console.log(`  ❌ ${name}#${it} (${ms} ms)`);
    problems.slice(0, 12).forEach(p => console.log('       - ' + String(p).slice(0, 400)));
  } else {
    sc.passed++;
    console.log(`  ✅ ${name}#${it} (${ms} ms)`);
  }
  await closeContext(context, problems.length > 0, `${name}-${it}`);
  return problems;
}

/** Listener counts back to the post-first-load level after leaving and returning. */
async function returnCheck(page, route, baseline, intent) {
  await hashNav(page, '#/tools');
  await page.waitForFunction(() => window.__mcStress.liveMaps().length === 0, null, { timeout: 15000, polling: 50 });
  await hashNav(page, route);
  await waitPicker(page);
  const r = await settleCheck(page, intent, { minMs: 600, maxMs: 6000 });
  const snap = await snapshot(page);
  const probs = r.problems.map(p => 'after return: ' + p);
  for (const k of Object.keys(baseline.listeners)) {
    if (snap.listeners[k] !== baseline.listeners[k]) {
      const sites = await page.evaluate((t) => window.__mcStress.listenerSites(t), k);
      probs.push(`listeners '${k}' after return=${snap.listeners[k]} first load=${baseline.listeners[k]} sites=${JSON.stringify(sites)}`);
    }
  }
  return probs;
}

// ── Scenario a: late config ─────────────────────────────────────────────────
const CUSTOM_CFG = [
  { id: 'stress-light', label: 'Stress Light', type: 'light', category: 'street', format: 'raster', url: 'https://stress-tiles.invalid/light/{z}/{x}/{y}.png', attribution: 'stress', maxZoom: 19 },
  { id: 'stress-dark', label: 'Stress Dark', type: 'dark', category: 'street', format: 'raster', url: 'https://stress-tiles.invalid/dark/{z}/{x}/{y}.png', attribution: 'stress', maxZoom: 19 },
  { id: 'stress-ov', label: 'Stress Overlay', overlay: true, url: 'https://stress-tiles.invalid/ov/{z}/{x}/{y}.png', attribution: 'stress', maxZoom: 19 },
];

async function scenarioA(browser) {
  const sc = scen('a', 'late /api/config/client');
  const t0 = Date.now();
  for (const it of iterationsFor('a')) {
    await runIteration(browser, 'a', it, async ({ rng, open }) => {
      const theme = rng.pick(['light', 'dark']);
      const mutate = rng.chance(0.5);
      const delay = rng.int(0, 3000);
      const land = rng.pick(PICKER_ROUTES);
      const hop = rng.chance(0.35) ? rng.pick(PICKER_ROUTES.filter(r => r !== land)) : null;
      const hopAt = rng.int(0, 1200);
      // Stored picks the mutated config disables (osmfr group) or ordinary built-ins.
      const stored = mutate
        ? { light: 'osmfr-hot', dark: 'osmfr-gray-dark' }
        : { light: rng.pick(['carto-voyager', 'osmfr-hot', 'eox-terrain-light', 'osmfr-gray', 'eox-s2-2025', 'carto-light']), dark: rng.pick(['positron-dark', 'osmfr-gray-dark', 'esri-darkgray-labels', 'eox-s2-2016', 'carto-dark']) };
      const overlays = mutate ? ['eox-labels', 'hillshade'] : rng.shuffle(['hillshade', 'eox-labels', 'eox-labels-dark']).slice(0, rng.int(0, 2));
      const page = await open({ preset: { theme, extra: { 'mc-light-tile-provider': stored.light, 'mc-dark-tile-provider': stored.dark, 'mc-tile-overlays': JSON.stringify(overlays) } } });
      let served = false;
      await page.route('**/api/config/client', async (route) => {
        try {
          const resp = await route.fetch();
          // route.fetch() bypasses the context's route: add the groups here too.
          let body = JSON.stringify(withOptInGroups(JSON.parse(await resp.text())));
          if (mutate) {
            const cfg = JSON.parse(body);
            const tiles = cfg.map.tiles;
            tiles.providers = tiles.providers || {};
            tiles.providers.osmfr = Object.assign({}, tiles.providers.osmfr, { enabled: false });
            tiles.providers.eox = Object.assign({}, tiles.providers.eox, { enabled: false });
            tiles.custom = CUSTOM_CFG;
            tiles.lightDefault = 'custom-stress-light';
            tiles.darkDefault = 'custom-stress-dark';
            body = JSON.stringify(cfg);
          }
          await sleep(delay);
          await route.fulfill({ status: resp.status(), headers: Object.assign({}, resp.headers(), { 'content-length': String(Buffer.byteLength(body)) }), body });
          served = true;
        } catch (_) { /* page closed */ }
      });
      await gotoFirst(page, land);
      let finalRoute = land;
      if (hop) {
        await sleep(hopAt);
        await hashNav(page, hop);
        finalRoute = hop;
      }
      await waitPicker(page);
      await page.waitForFunction(() => window.MeshConfigReady && window.MC_MAP_CFG, null, { timeout: 15000 });
      if (!served) return ['config route never served'];
      const expOverlays = mutate ? ['hillshade'] : overlays;
      const intent = { mode: 'auto', theme, overlays: expOverlays, pickers: 1 };
      const r = await settleCheck(page, intent, { minMs: 800, maxMs: 6000 });
      const probs = r.problems.slice();
      // The mutated config must show the custom default and hide the disabled groups.
      const ids = await page.evaluate(() => ({ base: window.__mcStress.baseIds(), ov: window.__mcStress.overlayIds(), res: window.MC_resolveTile(window.MC_isDarkTheme()).id }));
      if (mutate) {
        if (ids.res !== 'custom-stress-' + theme) probs.push(`resolved ${ids.res} want custom-stress-${theme}`);
        if (ids.base.some(x => /^osmfr-|^eox-/.test(x))) probs.push('picker lists disabled groups: ' + ids.base.filter(x => /^osmfr-|^eox-/.test(x)).join(','));
        if (!ids.base.includes('custom-stress-light') || !ids.base.includes('custom-stress-dark')) probs.push('picker lacks custom entries');
        if (!ids.ov.includes('custom-stress-ov') || ids.ov.some(x => /^eox-/.test(x))) probs.push('overlay list wrong: ' + ids.ov.join(','));
      } else if (ids.res !== stored[theme]) {
        probs.push(`resolved ${ids.res} want stored ${stored[theme]}`);
      }
      const dupBase = ids.base.length !== new Set(ids.base).size;
      if (dupBase) probs.push('duplicate picker entries');
      if (probs.length) return probs;
      const base = await snapshot(page);
      return probs.concat(await returnCheck(page, finalRoute, base, intent));
    });
  }
  sc.durationMs = Date.now() - t0;
}

// ── Scenario b: slow MapLibre ───────────────────────────────────────────────
const MAPLIBRE_JS = '**/vendor/maplibre-gl-5.24.0/maplibre-gl-csp.js';

async function delayMapLibre(page, ms) {
  const st = { requests: 0 };
  await page.route(MAPLIBRE_JS, async (route) => {
    st.requests++;
    const d = typeof ms === 'function' ? ms(st.requests) : ms;
    await sleep(d);
    await route.continue().catch(() => {});
  });
  return st;
}
async function waitLibrary(page, timeout) {
  await page.waitForFunction(() => !!(window.maplibregl && window.L && typeof window.L.maplibreGL === 'function'), null, { timeout, polling: 100 });
}

async function scenarioB(browser) {
  const sc = scen('b', 'slow MapLibre');
  const t0 = Date.now();
  const variants = ['to-raster', 'flip-theme', 'nav-away', 'other-vector', 'map-to-planner'];
  // The 15 s timeout path runs once per run, in parallel with the iterations.
  const timeoutRun = (!ONLY || (ONLY.b && !ONLY.b.length) || ONLY['b-timeout'])
    ? (async () => {
      const st = scen('b-timeout', 'MapLibre 15 s timeout then a successful pick');
      const tt = Date.now();
      await runIteration(browser, 'b-timeout', 0, async ({ rng, open }) => {
        const theme = 'light';
        const page = await open({ preset: { theme, provider: 'auto' } });
        await delayMapLibre(page, (n) => (n === 1 ? 17000 : 0));
        await gotoFirst(page, '#/map');
        await waitPicker(page);
        const vec = await page.evaluate(() => window.__mcStress.vectorIds());
        const v1 = rng.pick(vec);
        const v2 = rng.pick(vec.filter(v => v !== v1));
        const t1 = Date.now();
        if ((await page.evaluate((id) => window.__mcStress.clickBase(id), v1)) !== 'ok') return ['could not pick ' + v1];
        try {
          await page.waitForFunction(() => window.__mcStress.warns.some(w => /load-(failed|timeout)/.test(w)), null, { timeout: 20000, polling: 200 });
        } catch (e) {
          const st = await page.evaluate(() => ({ warns: window.__mcStress.warns, tags: document.querySelectorAll('script[data-mc-maplibre]').length, lib: !!window.maplibregl, gl: window.__mcStress.glState() }));
          return [`no load-failed/load-timeout fallback within 20 s of picking ${v1}: ${JSON.stringify(st)}`];
        }
        const tookMs = Date.now() - t1;
        const probs = [];
        if (tookMs < 14000 || tookMs > 17500) probs.push('fallback after ' + tookMs + ' ms (want ~15000)');
        let r = await settleCheck(page, { mode: 'explicit', id: v1, theme, allowFallback: true, pickers: 1 }, { minMs: 300, maxMs: 3000 });
        probs.push(...r.problems.map(p => 'after timeout: ' + p));
        await sleep(Math.max(0, 17500 - (Date.now() - t1))); // the delayed script lands (tags already removed)
        if ((await page.evaluate((id) => window.__mcStress.clickBase(id), v2)) !== 'ok') return probs.concat(['could not pick ' + v2]);
        await waitLibrary(page, 15000).catch(() => probs.push('MapLibre never loaded on retry'));
        r = await settleCheck(page, { mode: 'explicit', id: v2, theme, pickers: 1 }, { minMs: 1000, maxMs: 10000 });
        probs.push(...r.problems.map(p => 'retry: ' + p));
        // Informational: does re-picking the timed-out entry now draw GL?
        await page.evaluate((id) => window.__mcStress.clickBase(id), v1);
        await sleep(1500);
        const again = await page.evaluate((id) => {
          const S = window.__mcStress; const m = S.pickerMaps()[0]; let gl = 0;
          m.eachLayer((l) => { if (typeof l.getMaplibreMap === 'function') gl++; });
          return gl;
        }, v1);
        sc.details.repickAfterTimeoutShowsGl = again > 0;
        const tags = await page.evaluate(() => document.querySelectorAll('script[data-mc-maplibre]').length);
        if (tags > 2) probs.push('maplibre script tags=' + tags);
        return probs;
      });
      st.durationMs = Date.now() - tt;
    })()
    : Promise.resolve();

  for (const it of iterationsFor('b')) {
    await runIteration(browser, 'b', it, async ({ rng, open, meta }) => {
      const variant = rng.chance(0.25) ? rng.pick(variants) : variants[it % variants.length];
      const theme = rng.pick(['light', 'dark']);
      const delay = rng.int(1000, 6000);
      const gap = rng.int(0, 1500);
      Object.assign(meta, { variant, theme, delay, gap });
      const page = await open({ preset: { theme, provider: 'auto' } });
      await delayMapLibre(page, delay);
      const land = variant === 'map-to-planner' ? '#/map' : rng.pick(['#/map', '#/tools/planner']);
      await gotoFirst(page, land);
      await waitPicker(page);
      const info = await page.evaluate((th) => ({ vec: window.__mcStress.vectorIds(), fits: window.__mcStress.fits(th) }), theme);
      const vecFit = info.vec.filter(v => info.fits.includes(v));
      const v1 = rng.pick(info.vec);
      const probs = [];
      const picked = Date.now();
      let intent;
      if (variant === 'map-to-planner') {
        const v = rng.pick(vecFit);
        await page.evaluate((a) => (a.theme === 'dark' ? window.MC_setDarkTileProvider(a.id) : window.MC_setLightTileProvider(a.id)), { id: v, theme });
        await sleep(rng.int(0, 800));
        await hashNav(page, '#/tools/planner');
        if (rng.chance(0.4)) { await sleep(rng.int(0, 600)); await hashNav(page, '#/map'); }
        await waitPicker(page);
        intent = { mode: 'auto', theme, pickers: 1 };
      } else {
        if ((await page.evaluate((id) => window.__mcStress.clickBase(id), v1)) !== 'ok') return ['could not pick ' + v1];
        await sleep(gap);
        if (variant === 'to-raster') {
          const ras = (await page.evaluate(() => window.__mcStress.baseIds())).filter(id => !info.vec.includes(id) && id !== '__auto__');
          const r = rng.pick(ras);
          await page.evaluate((id) => window.__mcStress.clickBase(id), r);
          intent = { mode: 'explicit', id: r, theme, pickers: 1 };
        } else if (variant === 'flip-theme') {
          // Auto mode with the vector stored for this theme, then the theme flips mid-load.
          await page.evaluate(() => window.__mcStress.clickBase('__auto__'));
          const v = rng.pick(vecFit);
          await page.evaluate((a) => (a.theme === 'dark' ? window.MC_setDarkTileProvider(a.id) : window.MC_setLightTileProvider(a.id)), { id: v, theme });
          await sleep(rng.int(0, 1500));
          const next = await page.evaluate(() => window.__mcStress.flipTheme());
          intent = { mode: 'auto', theme: next, pickers: 1 };
        } else if (variant === 'nav-away') {
          await hashNav(page, rng.pick(['#/tools', '#/packets', '#/home']));
          intent = null;
        } else {
          const v2 = rng.pick(info.vec.filter(v => v !== v1));
          await page.evaluate((id) => window.__mcStress.clickBase(id), v2);
          intent = { mode: 'explicit', id: v2, theme, pickers: 1 };
        }
      }
      // Let the delayed library land before judging.
      const remain = delay - (Date.now() - picked) + 300;
      if (remain > 0) await sleep(remain);
      await waitLibrary(page, 15000).catch(() => { /* raster-only end state never needs it */ });
      if (intent) {
        const r = await settleCheck(page, intent, { minMs: 1000, maxMs: 10000 });
        probs.push(...r.problems);
      } else {
        await sleep(1500);
        const g = await page.evaluate(() => ({ maps: window.__mcStress.liveMaps().length, gl: window.__mcStress.glState(), glob: window.__mcStress.globalCheck() }));
        if (g.gl.live) {
          const after = await gcGl(page);
          probs.push(`after navigating away: live WebGL contexts=${g.gl.live} (attached ${g.gl.attached}); after GC ${after.live}`);
        }
        probs.push(...g.glob);
      }
      sc.details[variant] = (sc.details[variant] || 0) + 1;
      return probs;
    });
  }
  await timeoutRun;
  sc.durationMs = Date.now() - t0;
}

// ── Scenario c: rapid toggling ──────────────────────────────────────────────
async function scenarioC(browser) {
  const sc = scen('c', 'rapid toggling');
  const t0 = Date.now();
  for (const it of iterationsFor('c')) {
    await runIteration(browser, 'c', it, async ({ rng, open }) => {
      const theme0 = rng.pick(['light', 'dark']);
      const land = rng.pick(PICKER_ROUTES);
      const page = await open({ preset: { theme: theme0, provider: 'auto', overlays: [] } });
      await gotoFirst(page, land);
      await waitPicker(page);
      await page.waitForFunction(() => window.MC_MAP_CFG, null, { timeout: 15000 });
      await sleep(300);
      const first = await snapshot(page);
      const ids = await page.evaluate(() => ({ base: window.__mcStress.baseIds(), ov: window.__mcStress.overlayIds(), vec: window.__mcStress.vectorIds() }));
      const n = rng.int(30, 60);
      let mode = 'auto', explicitId = null, theme = theme0;
      const ov = new Set();
      const log = [];
      for (let i = 0; i < n; i++) {
        const roll = rng.next();
        if (roll < 0.40) {
          // Vector picks are rarer: each one costs a GL map.
          let id = rng.pick(ids.base.filter(x => x !== '__auto__'));
          if (ids.vec.includes(id) && rng.chance(0.5)) id = rng.pick(ids.base.filter(x => x !== '__auto__' && !ids.vec.includes(x)));
          const real = rng.chance(0.15);
          let res;
          if (real) {
            await page.hover('.leaflet-control-layers').catch(() => {});
            res = await page.click(`.leaflet-control-layers-base label[data-tile-id="${id}"]`, { timeout: 2000 }).then(() => 'ok', () => 'click-failed');
          } else {
            res = await page.evaluate((x) => window.__mcStress.clickBase(x), id);
          }
          if (res === 'ok') { mode = 'explicit'; explicitId = id; }
          log.push(`pick ${id}${real ? ' (mouse)' : ''} -> ${res}`);
        } else if (roll < 0.55) {
          theme = await page.evaluate(() => window.__mcStress.flipTheme());
          log.push('theme ' + theme);
        } else if (roll < 0.85 && ids.ov.length) {
          const id = rng.pick(ids.ov);
          const on = await page.evaluate((x) => window.__mcStress.clickOverlay(x), id);
          if (on === true) ov.add(id); else if (on === false) ov.delete(id);
          log.push(`overlay ${id} -> ${on}`);
        } else {
          const res = await page.evaluate(() => window.__mcStress.clickBase('__auto__'));
          if (res === 'ok') { mode = 'auto'; explicitId = null; }
          log.push('auto -> ' + res);
        }
        await sleep(rng.int(0, 120));
      }
      const intent = { mode, id: explicitId, theme, overlays: Array.from(ov), pickers: 1 };
      const r = await settleCheck(page, intent, { minMs: 2000, maxMs: 10000 });
      const probs = r.problems.slice();
      if (probs.length) probs.push('actions: ' + log.slice(-12).join(' | '));
      if (r.convergedMs > 2100) sc.details.slowConvergeMs = Math.max(sc.details.slowConvergeMs || 0, r.convergedMs);
      if (probs.length) return probs;
      // The pick is saved: the picker comes back on it (or Auto if Auto was last).
      return returnCheck(page, land, first, intent);
    });
  }
  sc.durationMs = Date.now() - t0;
}

// ── Scenario d: hillshade under motion ──────────────────────────────────────
async function scenarioD(browser) {
  const sc = scen('d', 'hillshade under motion');
  const t0 = iterationsFor('d');
  if (!t0.length) return;
  const start = Date.now();
  // One page across iterations so the heap can be compared.
  const rng0 = makeRng('d', 'page');
  const context = await openContext(browser, 'd', { trace: false });
  const page = await openPage(context, { preset: { theme: rng0.pick(['light', 'dark']), provider: 'auto', overlays: ['hillshade'] } });
  let terrHits = 0;
  await page.route(/\/terrarium\/\d+\/\d+\/\d+/, async (route) => {
    terrHits++;
    const url = route.request().url();
    const m = /\/terrarium\/(\d+)\/(\d+)\/(\d+)/.exec(url);
    const d = hash32(SEED + ':' + url) % 801; // deterministic per tile
    await sleep(d);
    await route.fulfill({ status: 200, headers: { 'content-type': 'image/png', 'access-control-allow-origin': '*', 'cache-control': 'no-store' }, body: MH.terrariumTile(+m[1], +m[2], +m[3]) }).catch(() => {});
  });
  await gotoFirst(page, '#/tools');
  await page.waitForFunction(() => window.MC_MAP_CFG, null, { timeout: 15000 });
  const heap = [];
  let anyFail = false;
  for (const it of t0) {
    const sc2 = sc;
    const rng = makeRng('d', it);
    const errBefore = page.__errors.length;
    const it0 = Date.now();
    let probs = [];
    try {
      const route = rng.pick(['#/map', '#/tools/planner', '#/map']);
      await hashNav(page, route);
      await waitPicker(page);
      // Hillshade restored from the stored preference.
      let r = await settleCheck(page, { mode: 'auto', overlays: ['hillshade'], pickers: 1 }, { minMs: 300, maxMs: 6000 });
      probs.push(...r.problems);
      const steps = [];
      const nSteps = rng.int(10, 25);
      const navAt = rng.int(3, nSteps - 1);
      for (let i = 0; i < nSteps; i++) {
        const kind = rng.next();
        steps.push(kind < 0.45 ? { zoom: rng.pick([-2, -1, 1, 1, 2]), animate: rng.chance(0.6) } : { pan: [rng.int(-400, 400), rng.int(-300, 300)], animate: rng.chance(0.5) });
      }
      const gaps = steps.map(() => rng.int(0, 150));
      const away = rng.pick(['#/tools', '#/packets', '#/home']);
      const done = await page.evaluate((a) => new Promise((resolve) => {
        const S = window.__mcStress;
        const m = S.pickerMaps()[0];
        let i = 0;
        function step() {
          if (i === a.navAt) { location.hash = a.away; resolve({ steps: i, navigated: true }); return; }
          if (i >= a.steps.length) { resolve({ steps: i, navigated: false }); return; }
          const s = a.steps[i++];
          if (s.zoom) {
            const z = Math.max(m.getMinZoom(), Math.min(Math.min(m.getMaxZoom(), 15), m.getZoom() + s.zoom));
            m.setZoom(z, { animate: s.animate });
          } else {
            m.panBy(s.pan, { animate: s.animate, duration: 0.2 });
          }
          setTimeout(step, a.gaps[i - 1]);
        }
        step();
      }), { steps, gaps, navAt, away });
      if (!done.navigated) probs.push('burst ended without navigating away');
      await page.waitForFunction(() => window.__mcStress.liveMaps().length === 0 || location.hash === '#/home', null, { timeout: 15000 });
      await sleep(1000); // in-flight terrarium tiles land on a removed layer
      if (away !== '#/tools') { await hashNav(page, '#/tools'); await page.waitForFunction(() => window.__mcStress.liveMaps().length === 0, null, { timeout: 15000 }); }
      probs.push(...(await page.evaluate(() => window.__mcStress.globalCheck())));
      await page.evaluate(() => window.__mcStress.prune());
      const h = await MH.measureHeap(page);
      heap.push(h);
      const snap = await snapshot(page);
      noteMax(snap);
      sc2.details.lastSnapshot = { listeners: snap.listeners, gl: snap.gl, maps: snap.maps };
      if (sc2.details.toolsListeners && JSON.stringify(sc2.details.toolsListeners) !== JSON.stringify(snap.listeners)) {
        probs.push('listeners on #/tools changed: ' + JSON.stringify(snap.listeners) + ' was ' + JSON.stringify(sc2.details.toolsListeners));
      }
      sc2.details.toolsListeners = sc2.details.toolsListeners || snap.listeners;
    } catch (e) {
      probs.push('exception: ' + (e && e.message));
    }
    probs = probs.concat(page.__errors.slice(errBefore, errBefore + 10));
    sc.iterations++;
    if (probs.length) {
      anyFail = true;
      sc.failed++;
      sc.failures.push({ iteration: it, repro: `MAP_STRESS_SEED=${SEED_RAW} MAP_STRESS_ONLY=d (page reused across iterations; run from 0)`, problems: probs.slice(0, 25) });
      console.log(`  ❌ d#${it} (${Date.now() - it0} ms)`);
      probs.slice(0, 12).forEach(p => console.log('       - ' + String(p).slice(0, 400)));
    } else {
      sc.passed++;
      console.log(`  ✅ d#${it} (${Date.now() - it0} ms) heap ${(heap[heap.length - 1] / 1048576).toFixed(1)} MB`);
    }
  }
  // Heap: iteration 0 warms caches; compare the rest.
  const MB = 1048576;
  const tail = heap.slice(1);
  const growth = tail.length >= 2 ? tail[tail.length - 1] - tail[0] : 0;
  sc.details.heapMB = heap.map(h => +(h / MB).toFixed(2));
  sc.details.heapGrowthMB = +(growth / MB).toFixed(2);
  sc.details.terrariumRequests = terrHits;
  const limit = Math.max(10 * MB, 0.25 * (tail[0] || 0));
  if (growth > limit) {
    sc.failed++;
    sc.failures.push({ iteration: 'heap', problems: [`heap grew ${(growth / MB).toFixed(1)} MB over ${tail.length} iterations (limit ${(limit / MB).toFixed(1)} MB): ${sc.details.heapMB.join(', ')}`] });
    console.log(`  ❌ d heap growth ${(growth / MB).toFixed(1)} MB`);
    anyFail = true;
  }
  // Steady state: caches fill in the first iterations (a ~5 MB step), then
  // the heap must level off. A leak of ~0.3 MB per page visit (master's
  // map-page leak before the teardown fixes) shows here even when the total
  // stays under the limit above. Needs >= 10 iterations.
  if (heap.length >= 10) {
    const half = heap.slice(Math.floor(heap.length / 2));
    const lateGrowth = half[half.length - 1] - half[0];
    sc.details.heapLateGrowthMB = +(lateGrowth / MB).toFixed(2);
    if (lateGrowth > 3 * MB) {
      sc.failed++;
      sc.failures.push({ iteration: 'heap', problems: [`heap still growing in the second half: ${(lateGrowth / MB).toFixed(1)} MB over ${half.length} iterations (limit 3 MB): ${sc.details.heapMB.join(', ')}`] });
      console.log(`  ❌ d heap late growth ${(lateGrowth / MB).toFixed(1)} MB`);
      anyFail = true;
    }
  }
  await closeContext(context, anyFail, 'd');
  sc.durationMs = Date.now() - start;
}

// ── Scenario e: two tabs ────────────────────────────────────────────────────
async function scenarioE(browser) {
  const sc = scen('e', 'cross-tab mirroring');
  const t0 = Date.now();
  for (const it of iterationsFor('e')) {
    await runIteration(browser, 'e', it, async ({ rng, open }) => {
      const theme = rng.pick(['light', 'dark']);
      const A = await open({ preset: { theme, provider: 'auto', overlays: [] } });
      const B = await open({});
      await gotoFirst(A, '#/map');
      await waitPicker(A);
      await gotoFirst(B, rng.pick(['#/map', '#/live']));
      await waitPicker(B);
      await Promise.all([A, B].map(p => p.waitForFunction(() => window.MC_MAP_CFG, null, { timeout: 15000 })));
      await sleep(300);
      const ids = await A.evaluate((th) => ({ ov: window.__mcStress.overlayIds(), fits: window.__mcStress.fits(th), vec: window.__mcStress.vectorIds() }), theme);
      const rasterFits = ids.fits.filter(x => !ids.vec.includes(x));
      const before = { A: await snapshot(A), B: await snapshot(B) };
      const writes = { A: 0, B: 0 };
      const ov = new Set();
      const probs = [];
      const nActs = rng.int(8, 16);
      const plannerAt = rng.int(2, nActs - 2);
      for (let i = 0; i < nActs; i++) {
        const who = rng.chance(0.75) ? 'A' : 'B';
        const P = who === 'A' ? A : B;
        if (i === plannerAt) { await hashNav(B, '#/tools/planner'); await waitPicker(B); }
        if (rng.chance(0.6) && ids.ov.length) {
          const id = rng.pick(ids.ov);
          const on = await P.evaluate((x) => window.__mcStress.clickOverlay(x), id);
          if (on === true) ov.add(id); else if (on === false) ov.delete(id);
          if (on !== null) writes[who]++;
        } else {
          const id = rng.pick(rasterFits);
          await P.evaluate((a) => (a.theme === 'dark' ? window.MC_setDarkTileProvider(a.id) : window.MC_setLightTileProvider(a.id)), { id, theme });
        }
        await sleep(rng.int(0, 200));
      }
      const intent = { mode: 'auto', theme, overlays: Array.from(ov), pickers: 1 };
      for (const [name, P] of [['A', A], ['B', B]]) {
        const r = await settleCheck(P, intent, { minMs: name === 'A' ? 800 : 0, maxMs: 5000 });
        probs.push(...r.problems.map(p => `tab ${name}: ${p}`));
      }
      const after = { A: await snapshot(A), B: await snapshot(B) };
      const d = (s, k, f) => ((after[s][f] || {})[k] || 0) - ((before[s][f] || {})[k] || 0);
      for (const s of ['A', 'B']) {
        const other = s === 'A' ? 'B' : 'A';
        const w = d(s, 'mc-tile-overlays', 'setItem');
        if (w !== writes[s]) probs.push(`tab ${s} wrote mc-tile-overlays ${w}x for ${writes[s]} own clicks (echo loop)`);
        const got = d(s, 'mc-tile-overlays', 'storageIn');
        if (got > writes[other]) probs.push(`tab ${s} received ${got} overlay storage events for ${writes[other]} writes in tab ${other}`);
        const disp = d(s, 'mc-tile-overlays-changed', 'events');
        if (disp !== writes[s]) probs.push(`tab ${s} dispatched mc-tile-overlays-changed ${disp}x for ${writes[s]} own clicks`);
      }
      sc.details.maxWrites = Math.max(sc.details.maxWrites || 0, writes.A + writes.B);
      return probs;
    });
  }
  sc.durationMs = Date.now() - t0;
}

// ── Scenario f: navigation churn ────────────────────────────────────────────
async function findNodes() {
  const out = { node: null, repeater: null };
  const get = async (u) => { const r = await fetch(BASE + u, { signal: AbortSignal.timeout(30000) }); if (!r.ok) throw new Error(u + ' ' + r.status); return r.json(); };
  const d = await get('/api/nodes?limit=50');
  const located = (d.nodes || []).filter(n => typeof n.lat === 'number' && typeof n.lon === 'number' && (n.lat || n.lon));
  located.sort((a, b) => (b.advert_count || 0) - (a.advert_count || 0) || String(a.public_key).localeCompare(String(b.public_key)));
  out.node = located[0] || null;
  for (const n of located.filter(x => x.role === 'repeater').slice(0, 10)) {
    try {
      const r = await get('/api/nodes/' + encodeURIComponent(n.public_key) + '/reach?days=7');
      if (r && Array.isArray(r.links) && r.links.length && r.node && r.node.lat != null) { out.repeater = n; break; }
    } catch (_) { /* next */ }
  }
  return out;
}

async function scenarioF(browser) {
  if (ONLY && !ONLY.f) return;
  const sc = scen('f', 'navigation churn');
  const t0 = Date.now();
  const nodes = await findNodes();
  const pagesDef = [
    { key: 'map', route: '#/map', picker: true },
    { key: 'live', route: '#/live', picker: true },
    { key: 'planner', route: PLANNER_ROUTE, picker: true },
    { key: 'home', route: '#/home', picker: false },
  ].filter(p => PLANNER || p.key !== 'planner');
  if (nodes.node) pagesDef.push({ key: 'node-detail', route: '#/nodes/' + encodeURIComponent(nodes.node.public_key), picker: false, map: true });
  else results.notes.push('f: no located node; node detail skipped');
  if (nodes.repeater) pagesDef.push({ key: 'node-reach', route: '#/nodes/' + encodeURIComponent(nodes.repeater.public_key) + '/reach', picker: false, map: true });
  else results.notes.push('f: no repeater with 7-day reach links; node reach skipped');
  sc.details.pages = pagesDef.map(p => p.key);
  const cycles = (ONLY && ONLY.f && ONLY.f.length) ? Math.max.apply(null, ONLY.f) + 1 : CHURN_CYCLES;
  sc.details.cycles = cycles;

  const rng = makeRng('f', 'run');
  const context = await openContext(browser, 'f');
  const page = await openPage(context, { preset: { theme: rng.pick(['light', 'dark']), provider: 'auto', overlays: [] } });
  let overlays = [];
  let anyFail = false;
  await gotoFirst(page, '#/tools');
  await page.waitForFunction(() => window.MC_MAP_CFG, null, { timeout: 15000 });
  const baseline = {};
  const KEYS = ['listeners', 'maps', 'pickers', 'controls', 'overlayPanes', 'autoLive', 'nodeTileRecs'];

  // A basemap picked in any picker is saved (mc-tile-pinned): every picker
  // page visited after it opens on it (nothing here picks Auto again).
  let pinned = null;
  async function visit(def, cyc) {
    const probs = [];
    await hashNav(page, def.route);
    let explicit = def.picker ? pinned : null;
    if (def.picker) {
      await waitPicker(page);
      if (rng.chance(0.25)) {
        const ids = await page.evaluate(() => window.__mcStress.baseIds().filter(x => x !== '__auto__' && !window.__mcStress.vectorIds().includes(x)));
        const id = rng.pick(ids);
        if ((await page.evaluate((x) => window.__mcStress.clickBase(x), id)) === 'ok') { explicit = id; pinned = id; }
      }
      if (rng.chance(0.25)) {
        const ov = await page.evaluate(() => window.__mcStress.overlayIds());
        const id = rng.pick(ov);
        const on = await page.evaluate((x) => window.__mcStress.clickOverlay(x), id);
        if (on === true) overlays = overlays.concat([id]); else if (on === false) overlays = overlays.filter(x => x !== id);
      }
    } else if (def.map) {
      if (!(await waitAnyMap(page, 20000))) probs.push(`${def.key}: no map rendered`);
    } else {
      await sleep(400);
    }
    // Random shared state change through the public setters / theme toggle.
    const roll = rng.next();
    if (roll < 0.15) await page.evaluate(() => window.__mcStress.flipTheme());
    else if (roll < 0.35) {
      await page.evaluate((x) => {
        const dark = window.MC_isDarkTheme();
        const ids = window.__mcStress.fits(dark ? 'dark' : 'light').filter(i => window.MC_TILE_PROVIDERS[i].format !== 'vector');
        const id = ids[Math.floor(x * ids.length)];
        return dark ? window.MC_setDarkTileProvider(id) : window.MC_setLightTileProvider(id);
      }, rng.next());
    }
    const intent = def.picker ? { mode: explicit ? 'explicit' : 'auto', id: explicit, overlays, pickers: 1 } : { pickers: 0 };
    const r = await settleCheck(page, intent, { minMs: 300, maxMs: 5000 });
    probs.push(...r.problems.map(p => `${def.key}: ${p}`));
    const snap = await snapshot(page);
    noteMax(snap);
    if (cyc === 1) baseline[def.key] = snap;
    else if (cyc > 1 && baseline[def.key]) {
      for (const k of KEYS) {
        const a = JSON.stringify(snap[k]), b = JSON.stringify(baseline[def.key][k]);
        if (a !== b) {
          let extra = '';
          if (k === 'listeners') extra = ' sites=' + JSON.stringify(await page.evaluate(() => ({ p: window.__mcStress.listenerSites('mc-tile-provider-changed'), o: window.__mcStress.listenerSites('mc-tile-overlays-changed'), s: window.__mcStress.listenerSites('storage') })));
          probs.push(`${def.key}: ${k} ${a} != cycle-1 ${b}${extra}`);
        }
      }
    }
    return probs;
  }

  const errStart = () => page.__errors.length;
  // Cycle 0 warms every page (wire-once listeners, caches); cycle 1 is the baseline.
  for (let cyc = 0; cyc <= cycles; cyc++) {
    const e0 = errStart();
    const c0 = Date.now();
    const order = cyc === 0 ? pagesDef : rng.shuffle(pagesDef);
    let probs = [];
    for (const def of order) {
      try { probs.push(...(await visit(def, cyc))); } catch (e) { probs.push(`${def.key}: exception ${e && e.message}`); }
    }
    probs = probs.concat(page.__errors.slice(e0, e0 + 10));
    if (cyc === 0) {
      if (probs.length) { console.log('  ⚠ f warm-up cycle problems:'); probs.forEach(p => console.log('       - ' + p)); }
      if (probs.length) { anyFail = true; sc.failed++; sc.failures.push({ iteration: 'warmup', problems: probs.slice(0, 25) }); }
      continue;
    }
    sc.iterations++;
    if (probs.length) {
      anyFail = true;
      sc.failed++;
      sc.failures.push({ iteration: cyc, repro: `MAP_STRESS_SEED=${SEED_RAW} MAP_STRESS_ONLY=f:${cyc} (replays cycles 0..${cyc})`, problems: probs.slice(0, 25) });
      console.log(`  ❌ f cycle ${cyc} (${Date.now() - c0} ms)`);
      probs.slice(0, 12).forEach(p => console.log('       - ' + String(p).slice(0, 400)));
    } else {
      sc.passed++;
      if (cyc === 1 || cyc === cycles || cyc % 10 === 0) console.log(`  ✅ f cycle ${cyc} (${Date.now() - c0} ms)`);
    }
  }
  const last = await snapshot(page);
  sc.details.final = { listeners: last.listeners, gl: last.gl };
  sc.details.baseline = Object.fromEntries(Object.entries(baseline).map(([k, v]) => [k, { listeners: v.listeners, maps: v.maps, nodeTileRecs: v.nodeTileRecs }]));
  await closeContext(context, anyFail, 'f');
  sc.durationMs = Date.now() - t0;
}

// ── Main ────────────────────────────────────────────────────────────────────
async function run() {
  const tStart = Date.now();
  console.log(`\nMap race stress — ${BASE} seed=${SEED} (${SEED_RAW}) iter=${ITER} churn=${CHURN_CYCLES}${results.only ? ' only=' + results.only : ''}\n`);
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  try {
    await detectPlanner();
    console.log(`planner ${PLANNER ? 'enabled' : 'disabled (planner steps use #/map)'}`);
    const table = [['a', scenarioA], ['b', scenarioB], ['c', scenarioC], ['d', scenarioD], ['e', scenarioE], ['f', scenarioF]];
    for (const [k, fn] of table) {
      if (ONLY && !ONLY[k] && !(k === 'b' && ONLY['b-timeout'])) continue;
      console.log(`--- ${k} ---`);
      await fn(browser);
    }
  } finally {
    await browser.close().catch(() => {});
  }
  results.durationMs = Date.now() - tStart;
  results.finishedAt = new Date().toISOString();
  let failed = 0, passed = 0;
  console.log('\nSummary');
  for (const [k, s] of Object.entries(results.scenarios)) {
    failed += s.failed; passed += s.passed;
    console.log(`  ${k.padEnd(10)} ${s.failed ? 'FAIL' : 'pass'}  iterations=${s.iterations} passed=${s.passed} failed=${s.failed} ${(s.durationMs / 1000).toFixed(1)} s`);
  }
  console.log(`  max listeners ${JSON.stringify(results.maxima.listeners)}  max live GL ${results.maxima.glLive}`);
  if (results.scenarios.d) console.log(`  heap growth (d) ${results.scenarios.d.details.heapGrowthMB} MB, second half ${results.scenarios.d.details.heapLateGrowthMB != null ? results.scenarios.d.details.heapLateGrowthMB + ' MB' : 'n/a (< 10 iterations)'}`);
  console.log(`  total ${(results.durationMs / 1000).toFixed(1)} s`);
  results.passed = passed;
  results.failed = failed;
  fs.mkdirSync(OUT_DIR, { recursive: true });
  const out = path.join(OUT_DIR, `${SEED}.json`);
  fs.writeFileSync(out, JSON.stringify(results, null, 2));
  console.log(`  results: ${path.relative(process.cwd(), out)}\n`);
  process.exit(failed ? 1 : 0);
}

run().catch((e) => { console.error(e); process.exit(1); });
