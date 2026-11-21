/* test-map-hillshade.js — client-side hillshade overlay (#2428 map providers).
 *
 * Covers, against the real public/map-hillshade.js in a vm sandbox:
 *   - MC_hillshadeShade kernel: flat -> transparent, NW vs SE lighting,
 *     steepness, no-data/ocean, borders, finite output, zoom/latitude
 *     scaling, timing budget
 *   - MC_createHillshadeLayer with a fake L.GridLayer / Image / canvas:
 *     option defaults, url template, crossOrigin before src (S6), size
 *     guard, tainted canvas (single warn, layer disabled), load error,
 *     tile unload cancels the download
 *
 * Runs via: node tests/unit/test-map-hillshade.js
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const vm   = require('vm');
const fs   = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
const queue = [];
function test(name, fn) { queue.push([name, fn]); }

const SRC = fs.readFileSync(path.join(REPO_ROOT, 'public', 'map-hillshade.js'), 'utf8');

// ── Kernel sandbox (no Leaflet: the kernel must still load) ─────────────────
function loadKernel() {
  const ctx = { console };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(SRC, ctx, { filename: 'map-hillshade.js' });
  return ctx;
}
const K = loadKernel();
const shade = K.MC_hillshadeShade;

function grid(w, h, fn) {
  const e = new Float32Array(w * h);
  for (let y = 0; y < h; y++) for (let x = 0; x < w; x++) e[y * w + x] = fn(x, y);
  return e;
}
function run(elev, w, h, zoom, lat) {
  const out = new Uint8ClampedArray(w * h * 4);
  shade(elev, w, h, zoom, lat, out);
  return out;
}
function px(out, w, x, y) {
  const o = (y * w + x) * 4;
  return { r: out[o], g: out[o + 1], b: out[o + 2], a: out[o + 3] };
}
// Plane rising toward the NW (top-left) or SE, `s` metres per pixel.
const riseNW = s => (x, y) => 1000 + s * ((64 - x) + (64 - y));
const riseSE = s => (x, y) => 1000 + s * (x + y);
const ZOOM = 12, LAT = 40;   // ~29.97 m/px

test('kernel loads without Leaflet and leaves the layer factory undefined', () => {
  assert.strictEqual(typeof shade, 'function');
  assert.strictEqual(K.MC_createHillshadeLayer, undefined);
});

test('flat terrain is fully transparent (any elevation)', () => {
  for (const z of [0, 1000, 8848, -100]) {
    const out = run(grid(32, 32, () => z), 32, 32, ZOOM, LAT);
    for (let i = 3; i < out.length; i += 4) assert.strictEqual(out[i], 0, 'alpha at ' + i + ' for z=' + z);
  }
});

test('NW-rising slope is shadow, SE-rising slope is highlight, similar strength', () => {
  const nw = px(run(grid(64, 64, riseNW(5)), 64, 64, ZOOM, LAT), 64, 32, 32);
  const se = px(run(grid(64, 64, riseSE(5)), 64, 64, ZOOM, LAT), 64, 32, 32);
  assert.deepStrictEqual([nw.r, nw.g, nw.b], [0, 0, 0], 'NW-rising face is black');
  assert.deepStrictEqual([se.r, se.g, se.b], [255, 255, 255], 'SE-rising face is white');
  assert.ok(nw.a > 20 && se.a > 20, 'both visible: ' + nw.a + ' / ' + se.a);
  assert.ok(Math.abs(nw.a - se.a) / Math.max(nw.a, se.a) < 0.25, 'similar alpha: ' + nw.a + ' vs ' + se.a);
});

test('steeper slope gives a stronger shadow', () => {
  let prev = -1;
  for (const s of [0.5, 2, 5, 15, 40]) {
    const a = px(run(grid(64, 64, riseNW(s)), 64, 64, ZOOM, LAT), 64, 32, 32).a;
    assert.ok(a > prev, 'slope ' + s + ': ' + a + ' > ' + prev);
    prev = a;
  }
});

test('slope parallel to the light-perpendicular axis (rising NE) is near neutral', () => {
  const a = px(run(grid(64, 64, (x, y) => 1000 + 2 * (x + (64 - y))), 64, 64, ZOOM, LAT), 64, 32, 32).a;
  const lit = px(run(grid(64, 64, riseNW(2)), 64, 64, ZOOM, LAT), 64, 32, 32).a;
  assert.ok(a < lit / 2, 'NE-rising ' + a + ' much weaker than NW-rising ' + lit);
});

test('no-data and deep ocean are transparent; coastline does not read as a cliff', () => {
  const w = 32, h = 32;
  const e = grid(w, h, x => (x < 10 ? -32768 : (x < 16 ? -600 : 200)));
  const out = run(e, w, h, ZOOM, LAT);
  for (let x = 0; x < 16; x++) assert.strictEqual(px(out, w, x, 10).a, 0, 'no-data col ' + x);
  assert.strictEqual(px(out, w, 16, 10).a, 0, 'flat land next to ocean stays flat');
  // NaN elevation is treated as no-data too.
  const n = grid(8, 8, () => NaN);
  const o2 = run(n, 8, 8, ZOOM, LAT);
  for (let i = 0; i < o2.length; i++) assert.strictEqual(o2[i], 0);
});

test('output is finite everywhere and in 0..255, length untouched (plain array sink)', () => {
  const w = 40, h = 30;
  const e = grid(w, h, (x, y) => (x * y) % 7 === 0 ? -32768 : 500 + 300 * Math.sin(x / 3) * Math.cos(y / 4));
  for (const [zoom, lat] of [[0, 0], [15, 85], [19, -89.9], [3, 90]]) {
    const out = new Array(w * h * 4).fill(-1);
    shade(e, w, h, zoom, lat, out);
    assert.strictEqual(out.length, w * h * 4);
    out.forEach((v, i) => assert.ok(Number.isFinite(v) && v >= 0 && v <= 255, 'z' + zoom + ' lat ' + lat + ' [' + i + ']=' + v));
  }
  // Degenerate 1x1 tile.
  const one = new Array(4).fill(-1);
  shade(new Float32Array([100]), 1, 1, 10, 10, one);
  assert.deepStrictEqual(one.map(Number.isFinite), [true, true, true, true]);
  assert.strictEqual(one[3], 0);
});

test('border pixels use a one-sided difference (no edge band on a plane)', () => {
  const out = run(grid(64, 64, riseNW(3)), 64, 64, ZOOM, LAT);
  const mid = px(out, 64, 32, 32).a;
  for (const [x, y] of [[0, 0], [63, 0], [0, 63], [63, 63], [0, 32], [32, 0], [63, 32], [32, 63]]) {
    assert.ok(Math.abs(px(out, 64, x, y).a - mid) <= 1, 'edge ' + x + ',' + y + ': ' + px(out, 64, x, y).a + ' vs ' + mid);
  }
});

test('same elevation grid: alpha grows with zoom and with latitude (smaller m/px)', () => {
  // Plain-array sink keeps the unrounded alpha so low zooms still compare.
  const e = grid(64, 64, riseNW(0.5));
  const alphaAt = (zoom, lat) => { const o = new Array(64 * 64 * 4); shade(e, 64, 64, zoom, lat, o); return o[(32 * 64 + 32) * 4 + 3]; };
  let prev = -1;
  for (let z = 2; z <= 18; z++) {
    const a = alphaAt(z, LAT);
    assert.ok(a > prev, 'zoom ' + z + ': ' + a + ' vs ' + prev);
    prev = a;
  }
  prev = -1;
  for (const lat of [0, 20, 40, 60, 75]) {
    const a = alphaAt(12, lat);
    assert.ok(a > prev, 'lat ' + lat + ': ' + a + ' vs ' + prev);
    prev = a;
  }
  assert.strictEqual(alphaAt(12, -40), alphaAt(12, 40), 'southern hemisphere mirrors the northern');
});

test('a gentle real-world plain stays nearly invisible at mid zoom', () => {
  // ~1 m rise over 30 m: a typical farmland grade.
  const a = px(run(grid(64, 64, riseNW(1 / 30 * 29.97)), 64, 64, 11, LAT), 64, 32, 32).a;
  assert.ok(a < 40, 'plain alpha ' + a);
});

test('timing: 256x256 tile median <= 15 ms', () => {
  const w = 256, h = 256;
  const e = grid(w, h, (x, y) => 2000 + 400 * Math.sin(x / 17) * Math.cos(y / 23) + 30 * Math.sin(x * y / 50));
  const out = new Uint8ClampedArray(w * h * 4);
  // Fresh copy: the plain-Array sinks above make the shared instance
  // polymorphic, which the browser (typed arrays only) never sees.
  const fresh = loadKernel().MC_hillshadeShade;
  for (let i = 0; i < 5; i++) fresh(e, w, h, 12, LAT, out);   // warm-up
  const t = [];
  for (let i = 0; i < 41; i++) {
    const s = process.hrtime.bigint();
    fresh(e, w, h, 12, LAT, out);
    t.push(Number(process.hrtime.bigint() - s) / 1e6);
  }
  t.sort((a, b) => a - b);
  const med = t[t.length >> 1];
  console.log('     MC_hillshadeShade 256x256 median ' + med.toFixed(2) + ' ms');
  assert.ok(med <= 15, 'median ' + med);
});

// ── Layer sandbox (fake Leaflet, Image and canvas) ──────────────────────────
function makeEnv(opts) {
  opts = opts || {};
  const env = { images: [], warns: [], errors: [], gets: 0, puts: [], canvases: [] };
  env.getImageData = opts.getImageData || ((w, h) => {
    const data = new Uint8ClampedArray(w * h * 4);
    for (let i = 0; i < w * h; i++) {
      const m = 1000 + (i % w) * 3 + 32768;   // elevation rising east
      data[i * 4] = m >> 8; data[i * 4 + 1] = m & 255; data[i * 4 + 2] = 0; data[i * 4 + 3] = 255;
    }
    return { width: w, height: h, data };
  });
  function makeCanvas() {
    const c = { width: 0, height: 0, ctxOpts: null };
    const ctx = {
      drawImage() {}, clearRect() {},
      getImageData(x, y, w, h) { env.gets++; return env.getImageData(w, h); },
      createImageData(w, h) { return { width: w, height: h, data: new Uint8ClampedArray(w * h * 4) }; },
      putImageData(img) { env.puts.push({ canvas: c, img }); },
    };
    c.getContext = (kind, o) => { c.ctxOpts = o || null; return ctx; };
    env.canvases.push(c);
    return c;
  }
  class FakeImage {
    constructor() {
      this._src = ''; this.crossOrigin = null; this.naturalWidth = 0; this.naturalHeight = 0;
      this.onload = null; this.onerror = null; this.srcLog = [];
      env.images.push(this);
    }
    set src(v) { this.srcLog.push({ v, crossOrigin: this.crossOrigin }); this._src = v; }
    get src() { return this._src; }
    load(w, h) { this.naturalWidth = w; this.naturalHeight = h === undefined ? w : h; if (this.onload) this.onload(); }
    fail() { if (this.onerror) this.onerror(); }
  }
  const L = {
    setOptions(obj, options) {
      if (!Object.prototype.hasOwnProperty.call(obj, 'options')) obj.options = obj.options ? Object.create(obj.options) : {};
      for (const k in options) obj.options[k] = options[k];
      return obj.options;
    },
    GridLayer: {
      extend(proto) {
        function Layer() { this._handlers = {}; if (this.initialize) this.initialize.apply(this, arguments); }
        Layer.prototype = Object.assign({
          options: {},
          on(ev, fn, ctx) { (this._handlers[ev] = this._handlers[ev] || []).push([fn, ctx]); return this; },
          fire(ev, data) { (this._handlers[ev] || []).forEach(([fn, ctx]) => fn.call(ctx || this, data)); return this; },
          getTileSize() { return { x: 256, y: 256 }; },
        }, proto);
        Layer.prototype.options = Object.assign({ tileSize: 256, opacity: 1, zIndex: 1, pane: 'tilePane' }, proto.options || {});
        return Layer;
      },
    },
  };
  const sandbox = {
    L, Image: FakeImage, setTimeout,
    document: { createElement(tag) { assert.strictEqual(tag, 'canvas'); return makeCanvas(); } },
    console: {
      log() {}, warn(...a) { env.warns.push(a.join(' ')); }, error(...a) { env.errors.push(a.join(' ')); },
    },
  };
  sandbox.window = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(SRC, sandbox, { filename: 'map-hillshade.js' });
  env.create = sandbox.MC_createHillshadeLayer;
  return env;
}
const URL_T = 'https://tiles.example/terrarium/{z}/{x}/{y}.png';
const C = { z: 10, x: 210, y: 388 };
function spyDone() {
  const calls = [];
  const fn = (err, tile) => calls.push({ err, tile });
  fn.calls = calls;
  return fn;
}
const tick = () => new Promise(r => setTimeout(r, 5));

test('options: defaults and overrides; url kept out of options', () => {
  const env = makeEnv();
  const l = env.create({ url: URL_T, attribution: 'x' });
  assert.strictEqual(l.options.opacity, 0.35);
  assert.strictEqual(l.options.maxNativeZoom, 15);
  assert.strictEqual(l.options.maxZoom, 19);
  assert.strictEqual(l.options.zIndex, 250);
  assert.strictEqual(l.options.attribution, 'x');
  assert.strictEqual(l.options.url, undefined);
  const m = env.create({ url: URL_T, opacity: 0.5, maxNativeZoom: 12, zIndex: 300, pane: 'hs', maxZoom: undefined });
  assert.strictEqual(m.options.opacity, 0.5);
  assert.strictEqual(m.options.maxNativeZoom, 12);
  assert.strictEqual(m.options.zIndex, 300);
  assert.strictEqual(m.options.pane, 'hs');
  assert.strictEqual(m.options.maxZoom, 19, 'undefined does not clobber a default');
});

test('url template substitutes {z}/{x}/{y} (every occurrence)', () => {
  const env = makeEnv();
  assert.strictEqual(env.create({ url: URL_T }).getTileUrl(C), 'https://tiles.example/terrarium/10/210/388.png');
  assert.strictEqual(env.create({ url: 'h/{y}-{x}-{z}?z={z}' }).getTileUrl(C), 'h/388-210-10?z=10');
});

test('crossOrigin is anonymous before src is set (S6)', () => {
  const env = makeEnv();
  const l = env.create({ url: URL_T });
  l.createTile(C, spyDone());
  assert.strictEqual(env.images.length, 1);
  assert.deepStrictEqual(env.images[0].srcLog, [{ v: 'https://tiles.example/terrarium/10/210/388.png', crossOrigin: 'anonymous' }]);
});

test('valid 256 and 512 tiles: shaded and done(null, tile) after load', () => {
  for (const n of [256, 512]) {
    const env = makeEnv();
    const l = env.create({ url: URL_T });
    const done = spyDone();
    const tile = l.createTile(C, done);
    assert.strictEqual(done.calls.length, 0, 'async');
    env.images[0].load(n);
    assert.strictEqual(done.calls.length, 1);
    assert.strictEqual(done.calls[0].err, null);
    assert.strictEqual(done.calls[0].tile, tile);
    assert.strictEqual(env.puts.length, 1);
    assert.strictEqual(env.puts[0].canvas, tile);
    assert.strictEqual(tile.width, n);
    assert.strictEqual(env.puts[0].img.width, n);
    // Elevation rising east: lit from the west, so the face is shadowed and visible.
    const d = env.puts[0].img.data;
    assert.ok(d[(10 * n + 10) * 4 + 3] > 0, 'relief drawn');
  }
});

test('decode keeps the B channel (1/256 m): a smooth ramp has no terraces', () => {
  // 0.25 m/px rise east, carried mostly in B: dropping B would leave 1 m steps
  // every 4 px, i.e. alternating flat (alpha 0) and steep pixels.
  const env = makeEnv({
    getImageData(w, h) {
      const data = new Uint8ClampedArray(w * h * 4);
      for (let i = 0; i < w * h; i++) {
        const m = (1000 + 32768) * 256 + (i % w) * 64;
        data[i * 4] = m >> 16; data[i * 4 + 1] = (m >> 8) & 255; data[i * 4 + 2] = m & 255; data[i * 4 + 3] = 255;
      }
      return { width: w, height: h, data };
    },
  });
  const l = env.create({ url: URL_T });
  l.createTile({ z: 15, x: 9000, y: 12400 }, spyDone());
  env.images[0].load(256);
  const d = env.puts[0].img.data;
  const row = [];
  for (let x = 1; x < 255; x++) row.push(d[(100 * 256 + x) * 4 + 3]);
  const min = Math.min.apply(null, row), max = Math.max.apply(null, row);
  assert.ok(min > 0, 'every pixel shaded, min ' + min);
  assert.ok(max - min <= 1, 'uniform shading ' + min + '..' + max);
});

test('scratch canvas and buffers are reused across tiles', () => {
  const env = makeEnv();
  const l = env.create({ url: URL_T });
  l.createTile(C, spyDone()); env.images[0].load(256);
  const before = env.canvases.length;
  const img1 = env.puts[0].img;
  l.createTile(C, spyDone()); env.images[1].load(256);
  assert.strictEqual(env.canvases.length, before + 1, 'only the new tile canvas');
  assert.strictEqual(env.puts[1].img, img1);
});

test('wrong-size image: blank tile, no decode', () => {
  for (const [w, h] of [[300, 300], [256, 512], [128, 128], [0, 0]]) {
    const env = makeEnv();
    const l = env.create({ url: URL_T });
    const done = spyDone();
    const tile = l.createTile(C, done);
    env.images[0].load(w, h);
    assert.strictEqual(done.calls.length, 1);
    assert.strictEqual(done.calls[0].err, null);
    assert.strictEqual(done.calls[0].tile, tile);
    assert.strictEqual(env.gets, 0, w + 'x' + h + ' not decoded');
    assert.strictEqual(env.puts.length, 0);
  }
});

test('tainted canvas: blank tile, one warning, layer disabled for later tiles', async () => {
  const env = makeEnv({
    getImageData() { const e = new Error('tainted'); e.name = 'SecurityError'; throw e; },
  });
  const l = env.create({ url: URL_T });
  const d1 = spyDone(), d2 = spyDone();
  const t1 = l.createTile(C, d1);
  l.createTile({ z: 10, x: 211, y: 388 }, d2);   // already in flight
  env.images[0].load(256);
  assert.deepStrictEqual(d1.calls.map(c => [c.err, c.tile === t1]), [[null, true]]);
  assert.strictEqual(env.puts.length, 0);
  assert.strictEqual(env.warns.length, 1);
  assert.strictEqual(l._failed, true);
  env.images[1].load(256);
  assert.strictEqual(d2.calls.length, 1);
  assert.strictEqual(env.gets, 1, 'in-flight tile not decoded after failure');
  // New tiles: no download at all, done still called (asynchronously).
  const d3 = spyDone();
  const t3 = l.createTile({ z: 10, x: 212, y: 388 }, d3);
  assert.strictEqual(env.images.length, 2, 'no new Image');
  assert.strictEqual(d3.calls.length, 0, 'not synchronous (Leaflet registers the tile after createTile)');
  await tick();
  assert.deepStrictEqual(d3.calls.map(c => [c.err, c.tile === t3]), [[null, true]]);
  assert.strictEqual(env.warns.length, 1, 'warned once');
  assert.strictEqual(env.errors.length, 0);
});

test('load error: done(null, blank tile), no console output', () => {
  const env = makeEnv();
  const l = env.create({ url: URL_T });
  const done = spyDone();
  const tile = l.createTile(C, done);
  env.images[0].fail();
  assert.deepStrictEqual(done.calls.map(c => [c.err, c.tile === tile]), [[null, true]]);
  assert.strictEqual(env.puts.length, 0);
  assert.strictEqual(env.warns.length + env.errors.length, 0);
  assert.strictEqual(l._failed, false, 'one 404 does not disable the layer');
});

test('tileunload cancels the download and never draws into the removed canvas', () => {
  const env = makeEnv();
  const l = env.create({ url: URL_T });
  const done = spyDone();
  const tile = l.createTile(C, done);
  const img = env.images[0];
  l.fire('tileunload', { tile });
  assert.strictEqual(img.src, '', 'src cleared');
  assert.strictEqual(img.onload, null);
  assert.strictEqual(img.onerror, null);
  img.load(256);   // a late load event has nothing to call
  assert.strictEqual(done.calls.length, 0);
  assert.strictEqual(env.puts.length, 0);
  // Unloading a finished tile or a foreign one is harmless.
  const t2 = l.createTile(C, spyDone());
  env.images[1].load(256);
  l.fire('tileunload', { tile: t2 });
  l.fire('tileunload', {});
  assert.notStrictEqual(env.images[1].src, '', 'finished tile keeps its src');
});

(async () => {
  for (const [name, fn] of queue) {
    try { await fn(); passed++; console.log('  ✅ ' + name); }
    catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
  }
  console.log('\ntest-map-hillshade: ' + passed + ' passed, ' + failed + ' failed');
  if (failed) process.exit(1);
})();
