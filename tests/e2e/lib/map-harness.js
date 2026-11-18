/* map-harness.js — deterministic map test/perf harness for the #2428 map
 * provider work. CommonJS; used by tests/e2e/test-map-providers-harness-e2e.js
 * and scripts/map-perf/run.js.
 *
 * Third-party tile hosts (OSM, Carto, USGS, EOX, OpenFreeMap, AWS terrarium…)
 * forbid bulk automated fetching, so the default mode ('stub') answers every
 * tile, elevation and MapLibre style request locally:
 *   - raster tiles   -> SVG naming the matched provider key + z/x/y on a
 *                       per-provider background colour (screenshots prove
 *                       which provider drew)
 *   - terrarium      -> real RGB PNG encoding a synthetic deterministic terrain
 *   - style JSON     -> minimal MapLibre style (version 8, one background layer)
 * Any other request to a non-local host is aborted and recorded, so a stub run
 * never touches the network. 'live' mode passes tiles through, at most two in
 * flight at a time, and records them.
 *
 * Adding a provider: append a row to TILE_HOSTS (or pass opts.extraHosts).
 * Same-site custom tile paths (map.tiles.custom '/tiles/{z}/{x}/{y}.png') are
 * caught by the generic {z}/{x}/{y} matcher without any table change.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const zlib = require('zlib');

// ── Tile host table ─────────────────────────────────────────────────────────
// First match wins; `key` names the provider in stubs, logs and screenshots.
// `kind`: 'raster' (SVG stub) | 'terrarium' (PNG elevation) | 'style' (MapLibre
// style JSON) | 'vector' (vector tile; answered empty — stub styles have no
// sources, so these only appear with a test-registered style).
const TILE_HOSTS = [
  { key: 'terrarium', kind: 'terrarium', test: /\/terrarium\/\d+\/\d+\/\d+/ },
  { key: 'openfreemap-style', kind: 'style', test: /tiles\.openfreemap\.org\/styles\// },
  { key: 'openfreemap', kind: 'vector', test: /tiles\.openfreemap\.org\// },
  { key: 'carto-dark', kind: 'raster', test: /\.cartocdn\.com\/dark_(all|nolabels|only_labels)\// },
  { key: 'carto-light', kind: 'raster', test: /\.cartocdn\.com\/light_(all|nolabels|only_labels)\// },
  { key: 'carto-voyager', kind: 'raster', test: /\.cartocdn\.com\/rastertiles\/voyager/ },
  { key: 'carto', kind: 'raster', test: /\.cartocdn\.com\// },
  { key: 'osmfr', kind: 'raster', test: /tile\.openstreetmap\.fr\// },
  { key: 'osm', kind: 'raster', test: /tile\.openstreetmap\.org\// },
  { key: 'opentopomap', kind: 'raster', test: /tile\.opentopomap\.org\// },
  { key: 'stamen', kind: 'raster', test: /stadiamaps\.com\// },
  { key: 'thunderforest', kind: 'raster', test: /thunderforest\.com\// },
  { key: 'maptiler', kind: 'raster', test: /api\.maptiler\.com\// },
  { key: 'mapbox', kind: 'raster', test: /api\.mapbox\.com\// },
  { key: 'usgs-imagery', kind: 'raster', test: /nationalmap\.gov\/.*USGSImagery/ },
  { key: 'usgs-topo', kind: 'raster', test: /nationalmap\.gov\// },
  { key: 'esri-ref', kind: 'raster', test: /arcgisonline\.com\/.*_Reference\// },
  { key: 'esri', kind: 'raster', test: /arcgisonline\.com\// },
  { key: 'eox', kind: 'raster', test: /maps\.eox\.at\// },
];

// Generic XYZ shape: /z/x/y(.ext)? — catches same-site custom tiles and any
// host not yet in the table. ArcGIS-style /tile/z/y/x is handled by yxOrder.
const XYZ_RE = /\/(\d{1,2})\/(\d{1,7})\/(\d{1,7})(?:@2x)?(?:\.(png|jpe?g|webp|pbf|mvt|svg))?(?:[?#]|$)/i;
const STYLE_RE = /(?:\/styles?\/[^?#]*|style)\.json(?:[?#]|$)|\/styles\/[^/?#]+\/?(?:[?#]|$)/i;
const LOCAL_HOSTS = new Set(['localhost', '127.0.0.1', '[::1]', '::1']);

function _hash32(str) {
  let h = 2166136261;
  for (let i = 0; i < str.length; i++) { h ^= str.charCodeAt(i); h = Math.imul(h, 16777619); }
  return h >>> 0;
}

/** Deterministic per-provider background colour (HSL hex). */
function providerColor(key) {
  const h = _hash32(String(key)) % 360;
  return _hslToHex(h, 55, 42);
}
function _hslToHex(h, s, l) {
  s /= 100; l /= 100;
  const k = n => (n + h / 30) % 12;
  const a = s * Math.min(l, 1 - l);
  const f = n => Math.round(255 * (l - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)))));
  return '#' + [f(0), f(8), f(4)].map(v => v.toString(16).padStart(2, '0')).join('');
}

function _xmlEsc(s) { return String(s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }

/** Deterministic 256x256 SVG tile labelled with provider key and z/x/y. */
function svgTile(key, z, x, y) {
  const bg = providerColor(key);
  return '<svg xmlns="http://www.w3.org/2000/svg" width="256" height="256" viewBox="0 0 256 256">' +
    '<rect width="256" height="256" fill="' + bg + '"/>' +
    '<rect x="0.5" y="0.5" width="255" height="255" fill="none" stroke="#000" stroke-opacity="0.35"/>' +
    '<text x="128" y="118" font-family="monospace" font-size="20" fill="#fff" text-anchor="middle">' + _xmlEsc(key) + '</text>' +
    '<text x="128" y="148" font-family="monospace" font-size="16" fill="#fff" text-anchor="middle">' + _xmlEsc(z + '/' + x + '/' + y) + '</text>' +
    '</svg>';
}

/** Minimal valid MapLibre style: no sources, one background layer. */
function maplibreStyle(key) {
  return {
    version: 8,
    name: 'harness-' + key,
    sources: {},
    layers: [{ id: 'background', type: 'background', paint: { 'background-color': providerColor(key) } }],
  };
}

// ── PNG / Terrarium ─────────────────────────────────────────────────────────
const CRC_TABLE = (() => {
  const t = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = (c & 1) ? (0xedb88320 ^ (c >>> 1)) : (c >>> 1);
    t[n] = c >>> 0;
  }
  return t;
})();
function crc32(buf) {
  let c = 0xffffffff;
  for (let i = 0; i < buf.length; i++) c = CRC_TABLE[(c ^ buf[i]) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}
function _chunk(type, data) {
  const len = Buffer.alloc(4); len.writeUInt32BE(data.length, 0);
  const td = Buffer.concat([Buffer.from(type, 'ascii'), data]);
  const crc = Buffer.alloc(4); crc.writeUInt32BE(crc32(td), 0);
  return Buffer.concat([len, td, crc]);
}

/**
 * Encode an 8-bit RGB (channels=3) or RGBA (channels=4) PNG.
 * pixels: Buffer/Uint8Array of width*height*channels bytes, row-major.
 */
function encodePNG(width, height, pixels, channels) {
  channels = channels || 3;
  if (channels !== 3 && channels !== 4) throw new Error('channels must be 3 or 4');
  if (pixels.length !== width * height * channels) throw new Error('pixel buffer size mismatch');
  const stride = width * channels;
  const raw = Buffer.alloc((stride + 1) * height);
  for (let y = 0; y < height; y++) {
    raw[y * (stride + 1)] = 0; // filter: none
    Buffer.from(pixels.buffer, pixels.byteOffset + y * stride, stride).copy(raw, y * (stride + 1) + 1);
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0); ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8; ihdr[9] = channels === 4 ? 6 : 2; ihdr[10] = 0; ihdr[11] = 0; ihdr[12] = 0;
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    _chunk('IHDR', ihdr),
    _chunk('IDAT', zlib.deflateSync(raw)),
    _chunk('IEND', Buffer.alloc(0)),
  ]);
}

/** Terrarium: elevation m = R*256 + G + B/256 - 32768. Returns [r,g,b]. */
function terrariumEncode(meters) {
  let v = meters + 32768;
  if (v < 0) v = 0;
  if (v > 65535 + 255 / 256) v = 65535 + 255 / 256;
  const r = Math.floor(v / 256);
  const g = Math.floor(v) % 256;
  const b = Math.min(255, Math.floor((v - Math.floor(v)) * 256));
  return [r, g, b];
}
function terrariumDecode(r, g, b) { return r * 256 + g + b / 256 - 32768; }

/**
 * Synthetic deterministic terrain in metres at global pixel (gx, gy) of zoom z
 * (256 px tiles). Smooth ridges + a basin; range roughly -200..2400 m, so both
 * signs and the fractional byte are exercised.
 */
function syntheticElevation(z, gx, gy) {
  // Normalise to world units [0,1) so neighbouring zooms agree.
  const scale = 256 * Math.pow(2, z);
  const u = gx / scale, v = gy / scale;
  return 1100 +
    900 * Math.sin(u * 2 * Math.PI * 40) * Math.cos(v * 2 * Math.PI * 30) +
    350 * Math.sin((u + v) * 2 * Math.PI * 170) -
    250 * Math.cos(u * 2 * Math.PI * 7) +
    0.37;
}

/** 256x256 Terrarium PNG for tile z/x/y. */
function terrariumTile(z, x, y, size) {
  size = size || 256;
  const px = Buffer.alloc(size * size * 3);
  const k = 256 / size;
  for (let j = 0; j < size; j++) {
    for (let i = 0; i < size; i++) {
      const [r, g, b] = terrariumEncode(syntheticElevation(z, (x * size + i) * k, (y * size + j) * k));
      const o = (j * size + i) * 3;
      px[o] = r; px[o + 1] = g; px[o + 2] = b;
    }
  }
  return encodePNG(size, size, px, 3);
}

// ── URL classification ──────────────────────────────────────────────────────
/**
 * Classify a request URL. Returns null for "not a map asset", otherwise
 * { key, kind, z, x, y }. extraHosts / styleUrls extend the table per test.
 */
function classifyUrl(url, opts) {
  opts = opts || {};
  let u;
  try { u = new URL(url); } catch (_) { return null; }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return null;
  const local = _isLocal(u, opts);
  if (local && /^\/api\//.test(u.pathname)) return null;
  const styleUrls = opts.styleUrls || [];
  for (const s of styleUrls) {
    const re = s.test instanceof RegExp ? s.test : s;
    if (re.test(url)) return { key: s.key || 'style', kind: 'style', z: null, x: null, y: null };
  }
  const table = (opts.extraHosts || []).concat(TILE_HOSTS);
  let hit = null;
  for (const h of table) { if (h.test.test(url)) { hit = h; break; } }
  const m = XYZ_RE.exec(u.pathname + (u.search || ''));
  if (hit) {
    if (hit.kind === 'style') return { key: hit.key, kind: 'style', z: null, x: null, y: null };
    const xyz = _xyz(u, m);
    if (hit.kind === 'vector' && STYLE_RE.test(u.pathname)) return { key: hit.key, kind: 'style', z: null, x: null, y: null };
    return Object.assign({ key: hit.key, kind: hit.kind }, xyz);
  }
  if (m) {
    // Unknown host / same-site custom path with tile shape.
    const ext = (m[4] || '').toLowerCase();
    const kind = (ext === 'pbf' || ext === 'mvt') ? 'vector' : 'raster';
    const key = local ? 'custom' : ('host:' + u.hostname);
    return Object.assign({ key: key, kind: kind }, _xyz(u, m));
  }
  if (!local && STYLE_RE.test(u.pathname)) return { key: 'style:' + u.hostname, kind: 'style', z: null, x: null, y: null };
  return null;
}
function _xyz(u, m) {
  if (!m) return { z: null, x: null, y: null };
  const a = +m[1], b = +m[2], c = +m[3];
  // ArcGIS MapServer/tile/{z}/{y}/{x}
  if (/\/MapServer\/tile\//i.test(u.pathname)) return { z: a, x: c, y: b };
  return { z: a, x: b, y: c };
}
function _isLocal(u, opts) {
  if (LOCAL_HOSTS.has(u.hostname)) return true;
  if (opts.baseUrl) { try { if (new URL(opts.baseUrl).host === u.host) return true; } catch (_) {} }
  return false;
}

// ── Stub installation ───────────────────────────────────────────────────────
const _state = new WeakMap(); // BrowserContext -> { log, opts }

function _newLog() { return { intercepted: [], external: [], failed: [], maplibre: [], api: [], counts: {} }; }
const LOG_CAP = 20000;
// Credentials in tile URLs (Carto ?key=, Maptiler ?key=, access_token=...)
// must never reach results.json, reports or logs: every logged URL and
// error string passes through redactUrl.
const SECRET_PARAM_RE = /([?&](?:key|api_?key|apikey|access_token|token)=)[^&#\s"']*/gi;
function redactUrl(s) { return typeof s === 'string' ? s.replace(SECRET_PARAM_RE, '$1REDACTED') : s; }
function _push(arr, item) {
  if (arr.length >= LOG_CAP) return;
  if (typeof item === 'string') item = redactUrl(item);
  else if (item && typeof item === 'object') {
    item = Object.assign({}, item);
    if (item.url) item.url = redactUrl(item.url);
    if (item.error) item.error = redactUrl(item.error);
  }
  arr.push(item);
}

/**
 * Route every map-tile / elevation / style request of the page's context.
 * opts: { mode: 'stub'|'live', baseUrl, extraHosts:[{key,kind,test}],
 *         styleUrls:[RegExp|{key,test}] }
 * Call before page.goto. Routes on the context so worker-initiated fetches
 * (MapLibre) are caught too; create contexts with serviceWorkers:'block'.
 */
async function installTileStub(page, opts) {
  opts = Object.assign({ mode: 'stub' }, opts || {});
  if (opts.mode !== 'stub' && opts.mode !== 'live') throw new Error('installTileStub: mode must be stub|live');
  const ctx = page.context();
  if (_state.has(ctx)) throw new Error('installTileStub: already installed on this context');
  const st = { log: _newLog(), opts: opts, inflight: 0, queue: [] };
  _state.set(ctx, st);

  const matcher = (url) => {
    let u;
    try { u = new URL(url.toString()); } catch (_) { return false; }
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return false;
    if (!_isLocal(u, opts)) return true; // every external request is ours to decide
    return !!classifyUrl(u.toString(), opts);
  };

  await ctx.route(matcher, async (route) => {
    const req = route.request();
    const url = req.url();
    const cls = classifyUrl(url, opts);
    const t0 = Date.now();
    if (!cls) {
      // External, not a map asset: never let it reach the network.
      _push(st.log.external, { url: url, resourceType: req.resourceType(), blocked: opts.mode === 'stub' });
      if (opts.mode === 'stub') return route.abort('blockedbyclient').catch(() => {});
      return route.continue().catch(() => {});
    }
    st.log.counts[cls.key] = (st.log.counts[cls.key] || 0) + 1;
    if (opts.mode === 'live') return _livePass(st, route, url, cls, t0);
    try {
      const res = _stubResponse(cls);
      await route.fulfill(res);
      _push(st.log.intercepted, { url: url, key: cls.key, kind: cls.kind, z: cls.z, x: cls.x, y: cls.y, status: res.status });
    } catch (e) {
      _push(st.log.failed, { url: url, key: cls.key, error: String(e && e.message || e) });
    }
  });

  page.on('request', (req) => { if (/maplibre/i.test(req.url())) _push(st.log.maplibre, req.url()); });
  // Same-site API durations (Playwright timing: responseEnd is ms after the
  // request started), so a slow run can be
  // attributed to the server rather than to the map code.
  page.on('requestfinished', (req) => {
    let u;
    try { u = new URL(req.url()); } catch (_) { return; }
    if (!_isLocal(u, opts) || !/^\/api\//.test(u.pathname)) return;
    const t = req.timing();
    if (!t || t.startTime < 0) return;
    _push(st.log.api, { url: u.pathname + u.search, startEpochMs: t.startTime, ms: t.responseEnd >= 0 ? t.responseEnd : null });
  });
  page.on('requestfailed', (req) => {
    const url = req.url();
    const f = req.failure();
    const err = f ? f.errorText : 'failed';
    // Aborted non-map externals are already listed under `external`.
    if (/blockedbyclient|BLOCKED_BY_CLIENT/i.test(err)) return;
    _push(st.log.failed, { url: url, error: err });
  });
  return st.log;
}

const CORS = { 'access-control-allow-origin': '*', 'cache-control': 'no-store' };
function _stubResponse(cls) {
  if (cls.kind === 'terrarium') {
    const z = cls.z == null ? 0 : cls.z, x = cls.x || 0, y = cls.y || 0;
    return { status: 200, headers: Object.assign({ 'content-type': 'image/png' }, CORS), body: terrariumTile(z, x, y) };
  }
  if (cls.kind === 'style') {
    return { status: 200, headers: Object.assign({ 'content-type': 'application/json' }, CORS), body: JSON.stringify(maplibreStyle(cls.key)) };
  }
  if (cls.kind === 'vector') {
    // Empty MVT (zero layers) is a valid tile.
    return { status: 200, headers: Object.assign({ 'content-type': 'application/x-protobuf' }, CORS), body: Buffer.alloc(0) };
  }
  return { status: 200, headers: Object.assign({ 'content-type': 'image/svg+xml' }, CORS), body: svgTile(cls.key, cls.z, cls.x, cls.y) };
}

const LIVE_MAX_INFLIGHT = 2;
async function _livePass(st, route, url, cls, t0) {
  if (st.inflight >= LIVE_MAX_INFLIGHT) await new Promise(r => st.queue.push(r));
  st.inflight++;
  try {
    const resp = await route.fetch();
    await route.fulfill({ response: resp });
    _push(st.log.intercepted, { url: url, key: cls.key, kind: cls.kind, z: cls.z, x: cls.x, y: cls.y, status: resp.status(), live: true, ms: Date.now() - t0 });
  } catch (e) {
    _push(st.log.failed, { url: url, key: cls.key, error: String(e && e.message || e) });
    await route.abort().catch(() => {});
  } finally {
    st.inflight--;
    const next = st.queue.shift();
    if (next) next();
  }
}

/** { intercepted, external, failed, maplibre, counts } for the page's context. */
function getRequestLog(page) {
  const st = _state.get(page.context());
  if (!st) return _newLog();
  const l = st.log;
  return { intercepted: l.intercepted.slice(), external: l.external.slice(), failed: l.failed.slice(), maplibre: l.maplibre.slice(), api: l.api.slice(), counts: Object.assign({}, l.counts) };
}

// ── Theme / provider ────────────────────────────────────────────────────────
const THEME_KEY = 'meshcore-theme';
const PROVIDER_KEYS = { dark: 'mc-dark-tile-provider', light: 'mc-light-tile-provider' };

/**
 * Preset theme / provider / overlays in localStorage before the app boots
 * (cold-load scenarios). provider: id or 'auto' (= clear the pick).
 */
async function presetStorage(page, o) {
  o = o || {};
  await page.addInitScript((a) => {
    try {
      if (a.theme) localStorage.setItem(a.themeKey, a.theme);
      if (a.provider && a.theme) {
        const k = a.keys[a.theme];
        if (a.provider === 'auto') localStorage.removeItem(k); else localStorage.setItem(k, a.provider);
      }
      if (a.overlays) localStorage.setItem('mc-tile-overlays', JSON.stringify(a.overlays));
      if (a.extra) for (const k of Object.keys(a.extra)) localStorage.setItem(k, a.extra[k]);
    } catch (_) {}
  }, { theme: o.theme, provider: o.provider, overlays: o.overlays, extra: o.extra, themeKey: THEME_KEY, keys: PROVIDER_KEYS });
}

/**
 * Switch the app theme at runtime through its own toggle (checkbox 'change'
 * or button 'click' — the handlers in app.js), falling back to data-theme +
 * localStorage when the toggle is absent. Resolves once data-theme matches.
 */
async function setTheme(page, theme) {
  if (theme !== 'light' && theme !== 'dark') throw new Error('setTheme: light|dark');
  await page.evaluate((t) => {
    const root = document.documentElement;
    if (root.getAttribute('data-theme') === t) return;
    const cb = document.getElementById('darkModeCheckbox');
    if (cb) {
      cb.checked = t === 'dark';
      cb.dispatchEvent(new Event('change', { bubbles: true }));
    } else {
      const btn = document.getElementById('darkModeToggle');
      if (btn) btn.click();
    }
    if (root.getAttribute('data-theme') !== t) {
      root.setAttribute('data-theme', t);
      try { localStorage.setItem('meshcore-theme', t); } catch (_) {}
    }
  }, theme);
  await page.waitForFunction((t) => document.documentElement.getAttribute('data-theme') === t, theme);
}

/**
 * Pick a tile provider for a theme through MC_set{Dark,Light}TileProvider
 * (persists to localStorage and fires mc-tile-provider-changed). id 'auto'
 * clears the pick and re-syncs. Returns the setter's boolean.
 */
async function setTileProvider(page, o) {
  const theme = o && o.theme, id = o && o.id;
  if (theme !== 'light' && theme !== 'dark') throw new Error('setTileProvider: theme light|dark');
  return page.evaluate((a) => {
    if (a.id === 'auto' || a.id == null) {
      try { localStorage.removeItem(a.key); } catch (_) {}
      window.dispatchEvent(new CustomEvent('mc-tile-provider-changed', { detail: { auto: true } }));
      return true;
    }
    const fn = a.theme === 'dark' ? window.MC_setDarkTileProvider : window.MC_setLightTileProvider;
    if (typeof fn !== 'function') return false;
    return !!fn(a.id);
  }, { theme: theme, id: id, key: PROVIDER_KEYS[theme] });
}

/** Registered providers: [{ id, type, label, format, category }]. */
async function listProviders(page) {
  return page.evaluate(() => {
    const reg = window.MC_TILE_PROVIDERS || {};
    return Object.keys(reg).map(id => ({ id: id, type: reg[id].type, label: reg[id].label, format: reg[id].format || 'raster', category: reg[id].category || 'street' }));
  });
}

// ── In-page collectors (addInitScript) ──────────────────────────────────────
// Installed before any app script. Hooks Leaflet as soon as window.L is
// assigned (property trap) so every L.Map / GridLayer is instrumented from
// construction; also a capture-phase load listener as a fallback for tiles.
function _collectorScript() {
  /* eslint-disable no-var */
  var H = window.__mcHarness = {
    maps: [],
    firstMapMs: null,
    firstTileMs: null,
    tileLoadMs: new WeakMap(),
    tileStartMs: new WeakMap(),
    canvasTiles: [],          // { ms, hillshade }
    firstMarkerDomMs: null,
    firstMarkerLayerMs: null,
    longTasks: [],
    themeRefreshes: 0,
    hooked: false,
  };
  var now = function () { return performance.now(); };
  // app.js fires one 'theme-refresh' ~300 ms after /api/config/theme resolves
  // on every load, and pages re-render on it (home.js rebuilds its cards).
  window.addEventListener('theme-refresh', function () { H.themeRefreshes++; });
  function markTile(el) {
    if (!el || H.tileLoadMs.has(el)) return;
    var t = now();
    H.tileLoadMs.set(el, t);
    if (H.firstTileMs == null) H.firstTileMs = t;
  }
  document.addEventListener('load', function (e) {
    var el = e.target;
    if (el && el.tagName === 'IMG' && el.classList && el.classList.contains('leaflet-tile')) markTile(el);
  }, true);

  try {
    new PerformanceObserver(function (list) {
      list.getEntries().forEach(function (en) {
        if (H.longTasks.length < 20000) H.longTasks.push({ start: en.startTime, dur: en.duration });
      });
    }).observe({ type: 'longtask', buffered: true });
  } catch (_) {}

  var MARKER_SEL = '.leaflet-marker-icon, .marker-cluster';
  var mo = new MutationObserver(function (muts) {
    if (H.firstMarkerDomMs != null) return;
    for (var i = 0; i < muts.length; i++) {
      var added = muts[i].addedNodes;
      for (var j = 0; j < added.length; j++) {
        var n = added[j];
        if (n.nodeType === 1 && ((n.matches && n.matches(MARKER_SEL)) || (n.querySelector && n.querySelector(MARKER_SEL)))) {
          H.firstMarkerDomMs = now();
          mo.disconnect();
          return;
        }
      }
    }
  });
  function startMo() { mo.observe(document.documentElement, { childList: true, subtree: true }); }
  if (document.documentElement) startMo(); else document.addEventListener('DOMContentLoaded', startMo);

  function hook(L) {
    if (H.hooked || !L || !L.Map || !L.GridLayer) return;
    H.hooked = true;
    L.Map.addInitHook(function () {
      var map = this;
      H.maps.push(map);
      map.__mcCreatedMs = now();
      if (H.firstMapMs == null) H.firstMapMs = map.__mcCreatedMs;
      map.on('layeradd', function (e) {
        if (H.firstMarkerLayerMs != null) return;
        var ly = e.layer;
        if ((L.Marker && ly instanceof L.Marker) || (L.CircleMarker && ly instanceof L.CircleMarker)) H.firstMarkerLayerMs = now();
      });
      map.on('unload', function () { var i = H.maps.indexOf(map); if (i >= 0) H.maps.splice(i, 1); });
    });
    L.GridLayer.addInitHook(function () {
      var layer = this;
      var orig = layer.createTile;
      if (typeof orig === 'function') {
        layer.createTile = function (coords, done) {
          var t0 = now();
          var el = orig.apply(this, arguments);
          if (el) H.tileStartMs.set(el, t0);
          return el;
        };
      }
      layer.on('tileload', function (e) {
        markTile(e.tile);
        if (e.tile && e.tile.tagName === 'CANVAS') {
          var s = H.tileStartMs.get(e.tile);
          var u = String(layer._url || (layer.options && layer.options.url) || '');
          var hs = /terrarium|elevation|hillshade/i.test(u) || !!(layer.options && (layer.options.kind === 'hillshade' || layer.options.hillshade));
          if (s != null && H.canvasTiles.length < 20000) H.canvasTiles.push({ ms: now() - s, hillshade: hs });
        }
      });
    });
  }
  var _L;
  try {
    Object.defineProperty(window, 'L', {
      configurable: true, enumerable: true,
      get: function () { return _L; },
      set: function (v) { _L = v; try { hook(v); } catch (_) {} },
    });
  } catch (_) {
    document.addEventListener('DOMContentLoaded', function () { hook(window.L); });
  }

  function liveMaps() {
    // Prune here too: the app removes maps via MC_safeRemoveMap, which calls
    // map.off() before remove(), so the 'unload' splice above never runs and
    // H.maps would otherwise retain every removed map (and its page DOM).
    H.maps = H.maps.filter(function (m) { return !m._mcRemoved && m._container && document.contains(m._container); });
    return H.maps;
  }
  H.liveMaps = liveMaps;
  function inView(el) {
    var r = el.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) return false;
    if (r.right <= 0 || r.bottom <= 0 || r.left >= innerWidth || r.top >= innerHeight) return false;
    var c = el.closest('.leaflet-container');
    if (c) {
      var cr = c.getBoundingClientRect();
      if (r.right <= cr.left || r.bottom <= cr.top || r.left >= cr.right || r.top >= cr.bottom) return false;
    }
    var cs = getComputedStyle(el);
    if (cs.visibility === 'hidden' || cs.display === 'none') return false;
    return true;
  }
  function libreMaps() {
    var out = [];
    liveMaps().forEach(function (m) {
      m.eachLayer(function (ly) {
        if (typeof ly.getMaplibreMap === 'function') { try { var g = ly.getMaplibreMap(); if (g) out.push(g); } catch (_) {} }
      });
    });
    return out;
  }
  H.tileState = function () {
    var tiles = Array.prototype.slice.call(document.querySelectorAll('.leaflet-tile'));
    var vis = tiles.filter(inView);
    var loaded = 0, failed = 0, pending = 0, lastLoad = null;
    vis.forEach(function (el) {
      var isLoaded = el.classList.contains('leaflet-tile-loaded');
      if (el.tagName === 'IMG') {
        if (isLoaded && el.naturalWidth > 0) loaded++;
        else if (el.complete && el.naturalWidth === 0 && el.getAttribute('src')) failed++;
        else pending++;
      } else if (isLoaded) loaded++;
      else pending++;
      var t = H.tileLoadMs.get(el);
      if (isLoaded && t != null && (lastLoad == null || t > lastLoad)) lastLoad = t;
    });
    var gl = libreMaps();
    var glPending = gl.filter(function (g) { try { return !(g.loaded() && g.areTilesLoaded()); } catch (_) { return true; } }).length;
    var canvases = document.querySelectorAll('.maplibregl-canvas').length;
    return { visible: vis.length, loaded: loaded, failed: failed, pending: pending + glPending, lastLoadMs: lastLoad, maplibreCanvases: canvases, maplibreMaps: gl.length };
  };
  H.layerCount = function () {
    var n = 0;
    liveMaps().forEach(function (m) { m.eachLayer(function () { n++; }); });
    return { maps: liveMaps().length, layers: n, tileEls: document.querySelectorAll('.leaflet-tile').length };
  };
  H.primaryMap = function () {
    var best = null, area = -1;
    liveMaps().forEach(function (m) {
      var r = m._container.getBoundingClientRect();
      var a = r.width * r.height;
      if (a > area) { area = a; best = m; }
    });
    return best;
  };
  H.longTaskStats = function (from, to) {
    var c = 0, tot = 0, mx = 0;
    H.longTasks.forEach(function (t) {
      if (from != null && t.start < from) return;
      if (to != null && t.start > to) return;
      c++; tot += t.dur; if (t.dur > mx) mx = t.dur;
    });
    return { count: c, totalMs: tot, maxMs: mx };
  };
  H.frames = null;
  H.startFrames = function () {
    var f = H.frames = { deltas: [], last: null, run: true, start: now() };
    function tick(t) {
      if (!f.run) return;
      if (f.last != null && f.deltas.length < 100000) f.deltas.push(t - f.last);
      f.last = t;
      requestAnimationFrame(tick);
    }
    requestAnimationFrame(tick);
  };
  H.stopFrames = function () {
    var f = H.frames; if (!f) return null;
    f.run = false;
    var d = f.deltas.slice().sort(function (a, b) { return a - b; });
    var q = function (p) { return d.length ? d[Math.min(d.length - 1, Math.floor(p * d.length))] : null; };
    return { frames: d.length, p50: q(0.5), p95: q(0.95), max: d.length ? d[d.length - 1] : null, dropped: d.filter(function (x) { return x > 50; }).length, from: f.start, to: now() };
  };
}

/** Install the in-page perf collectors. Call before page.goto. */
async function installCollectors(page) {
  await page.addInitScript(_collectorScript);
}

/**
 * Wait until every visible .leaflet-tile (and every MapLibre map, when one
 * exists) has loaded or failed, stable across two polls, with ≥minTiles
 * visible tiles (default 1) or a MapLibre canvas.
 * Returns { loaded, failed, pending, visible, ms, timedOut, lastLoadMs }.
 */
async function waitForTiles(page, o) {
  const timeout = (o && o.timeout) || 30000;
  const minTiles = (o && o.minTiles != null) ? o.minTiles : 1;
  const t0 = Date.now();
  let timedOut = false;
  try {
    await page.waitForFunction((min) => {
      const H = window.__mcHarness;
      const s = H ? H.tileState() : (function () {
        const v = Array.prototype.filter.call(document.querySelectorAll('.leaflet-tile'), el => el.getBoundingClientRect().width > 0);
        const loaded = v.filter(el => el.classList.contains('leaflet-tile-loaded')).length;
        return { visible: v.length, loaded: loaded, failed: 0, pending: v.length - loaded };
      })();
      // A MapLibre basemap draws into a canvas, not .leaflet-tile elements;
      // its tile loading is already counted in s.pending (glPending).
      const ok = (s.visible >= min || s.maplibreCanvases > 0) && s.pending === 0;
      window.__mcTileStable = ok ? (window.__mcTileStable || 0) + 1 : 0;
      return window.__mcTileStable >= 2;
    }, minTiles, { timeout: timeout, polling: 100 });
  } catch (e) {
    if (!/Timeout/i.test(String(e && e.message))) throw e;
    timedOut = true;
  }
  const s = await page.evaluate(() => (window.__mcHarness ? window.__mcHarness.tileState() : null));
  return Object.assign({ loaded: 0, failed: 0, pending: 0, visible: 0, lastLoadMs: null }, s || {}, { ms: Date.now() - t0, timedOut: timedOut });
}

/** Wait for the first marker (DOM icon, cluster, or marker layer). */
async function waitForMarkers(page, o) {
  const timeout = (o && o.timeout) || 30000;
  try {
    await page.waitForFunction(() => {
      const H = window.__mcHarness;
      return !!H && (H.firstMarkerDomMs != null || H.firstMarkerLayerMs != null);
    }, null, { timeout: timeout, polling: 100 });
    return true;
  } catch (e) {
    if (!/Timeout/i.test(String(e && e.message))) throw e;
    return false;
  }
}

/**
 * Wait for the app's load-time 'theme-refresh' (see the collector). Pages
 * that re-render on it (home, analytics) discard anything opened earlier, so
 * click-to-open steps must come after it. Resolves false on timeout (e.g. a
 * build that no longer fires it) rather than throwing.
 */
async function waitForThemeRefresh(page, o) {
  try {
    await page.waitForFunction(() => window.__mcHarness && window.__mcHarness.themeRefreshes > 0, null, { timeout: (o && o.timeout) || 10000, polling: 50 });
    return true;
  } catch (e) {
    if (!/Timeout/i.test(String(e && e.message))) throw e;
    return false;
  }
}

/** Snapshot of the in-page metrics (ms are relative to navigation start). */
async function collectMetrics(page) {
  return page.evaluate(() => {
    const H = window.__mcHarness;
    const nav = performance.getEntriesByType('navigation')[0];
    const ts = H ? H.tileState() : {};
    const lt = H ? H.longTaskStats() : { count: 0, totalMs: 0, maxMs: 0 };
    const mk = H ? [H.firstMarkerDomMs, H.firstMarkerLayerMs].filter(v => v != null) : [];
    const hs = H ? H.canvasTiles.filter(t => t.hillshade).map(t => t.ms) : [];
    return {
      domContentLoadedMs: nav ? nav.domContentLoadedEventEnd : null,
      loadEventMs: nav ? nav.loadEventEnd : null,
      // Creation time of the map on screen now (a surface may rebuild its map).
      mapCreatedMs: H ? (H.primaryMap() ? H.primaryMap().__mcCreatedMs : H.firstMapMs) : null,
      firstTileMs: H ? H.firstTileMs : null,
      allTilesMs: ts.lastLoadMs != null ? ts.lastLoadMs : null,
      firstMarkerMs: mk.length ? Math.min.apply(null, mk) : null,
      longTaskCount: lt.count,
      longTaskTotalMs: lt.totalMs,
      longTaskMaxMs: lt.maxMs,
      tilesVisible: ts.visible, tilesLoaded: ts.loaded, tilesFailed: ts.failed,
      maplibreCanvases: ts.maplibreCanvases || 0,
      hillshadeTiles: hs.length,
      hillshadeTileMsMedian: hs.length ? hs.slice().sort((a, b) => a - b)[Math.floor(hs.length / 2)] : null,
      layers: H ? H.layerCount() : null,
    };
  });
}

/** JS heap used (bytes) after a forced GC, via CDP. */
async function measureHeap(page) {
  const cdp = await page.context().newCDPSession(page);
  try {
    // Drop the harness's own references to removed maps first, so the
    // number measures the app, not this harness.
    await page.evaluate(() => { if (window.__mcHarness && window.__mcHarness.liveMaps) window.__mcHarness.liveMaps(); });
    await cdp.send('Performance.enable');
    await cdp.send('HeapProfiler.collectGarbage');
    await cdp.send('HeapProfiler.collectGarbage');
    const { metrics } = await cdp.send('Performance.getMetrics');
    const m = metrics.find(x => x.name === 'JSHeapUsedSize');
    return m ? m.value : null;
  } finally {
    await cdp.detach().catch(() => {});
  }
}

/** Leaflet layer / tile element count across live maps. */
async function layerCount(page) {
  return page.evaluate(() => (window.__mcHarness ? window.__mcHarness.layerCount() : null));
}

// Fixed deterministic pan/zoom path (pixels / zoom deltas on the primary map).
const DEFAULT_PANZOOM_PATH = [
  { pan: [400, 0] }, { pan: [0, 300] }, { zoom: 1 }, { zoom: 1 }, { pan: [-500, 0] },
  { pan: [0, -300] }, { zoom: -1 }, { pan: [300, 200] }, { zoom: -1 }, { pan: [-200, -200] },
];

/**
 * Drive the primary (largest) Leaflet map along a fixed path with animated
 * moves, waiting for moveend and tiles after each step. Returns frame stats
 * (rAF deltas p50/p95/max, dropped >50 ms) and long tasks in the window.
 */
async function scriptedPanZoom(page, steps, o) {
  steps = steps || DEFAULT_PANZOOM_PATH;
  const tileTimeout = (o && o.tileTimeout) || 15000;
  const has = await page.evaluate(() => !!(window.__mcHarness && window.__mcHarness.primaryMap()));
  if (!has) throw new Error('scriptedPanZoom: no Leaflet map on page');
  await page.evaluate(() => window.__mcHarness.startFrames());
  const t0 = Date.now();
  const stepMs = [];
  for (const s of steps) {
    const s0 = Date.now();
    await page.evaluate((st) => new Promise((resolve) => {
      const m = window.__mcHarness.primaryMap();
      let done = false;
      const fin = () => { if (!done) { done = true; resolve(); } };
      m.once('moveend', fin);
      if (st.pan) m.panBy(st.pan, { animate: true, duration: 0.25 });
      else if (st.zoom) {
        const z = Math.max(m.getMinZoom(), Math.min(m.getMaxZoom(), m.getZoom() + st.zoom));
        if (z === m.getZoom()) { m.off('moveend', fin); fin(); return; }
        m.setZoom(z, { animate: true });
      }
    }), s);
    await waitForTiles(page, { timeout: tileTimeout, minTiles: 0 });
    stepMs.push(Date.now() - s0);
  }
  const frames = await page.evaluate(() => window.__mcHarness.stopFrames());
  const lt = await page.evaluate((f) => window.__mcHarness.longTaskStats(f.from, f.to), frames);
  return { steps: steps.length, durationMs: Date.now() - t0, stepMs: stepMs, frames: frames, longTasks: lt };
}

/** Full-viewport PNG; returns the written path. */
async function screenshot(page, outDir, name) {
  fs.mkdirSync(outDir, { recursive: true });
  const safe = String(name).replace(/[^A-Za-z0-9._-]+/g, '_');
  const p = path.join(outDir, safe.endsWith('.png') ? safe : safe + '.png');
  await page.screenshot({ path: p, fullPage: false });
  return p;
}

module.exports = {
  TILE_HOSTS, DEFAULT_PANZOOM_PATH,
  installTileStub, getRequestLog, classifyUrl, redactUrl,
  presetStorage, setTheme, setTileProvider, listProviders,
  installCollectors, waitForTiles, waitForMarkers, waitForThemeRefresh, collectMetrics, measureHeap, layerCount,
  scriptedPanZoom, screenshot,
  // encoders (unit-tested)
  providerColor, svgTile, maplibreStyle, encodePNG, crc32,
  terrariumEncode, terrariumDecode, syntheticElevation, terrariumTile,
};
