/* Unit tests for the map harness encoders (tests/e2e/lib/map-harness.js):
 * PNG writer, Terrarium elevation encoding, deterministic stub tiles and the
 * tile-URL classifier. Decodes the harness's own PNG output with zlib. */
'use strict';
const assert = require('assert');
const zlib = require('zlib');
const H = require('../e2e/lib/map-harness');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// Minimal PNG reader: verifies signature + every chunk CRC, inflates IDAT,
// undoes filter type 0 (the only one the encoder writes).
function decodePNG(buf) {
  assert.deepStrictEqual([...buf.subarray(0, 8)], [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a], 'PNG signature');
  let off = 8, ihdr = null; const idat = []; let sawEnd = false; const order = [];
  while (off < buf.length) {
    const len = buf.readUInt32BE(off);
    const type = buf.toString('ascii', off + 4, off + 8);
    const data = buf.subarray(off + 8, off + 8 + len);
    const crc = buf.readUInt32BE(off + 8 + len);
    assert.strictEqual(crc, H.crc32(buf.subarray(off + 4, off + 8 + len)), `CRC of ${type}`);
    order.push(type);
    if (type === 'IHDR') ihdr = { w: data.readUInt32BE(0), h: data.readUInt32BE(4), depth: data[8], color: data[9], interlace: data[12] };
    else if (type === 'IDAT') idat.push(data);
    else if (type === 'IEND') { sawEnd = true; off += 12 + len; break; }
    off += 12 + len;
  }
  assert.ok(sawEnd, 'IEND present');
  assert.strictEqual(off, buf.length, 'no trailing bytes after IEND');
  assert.strictEqual(order[0], 'IHDR', 'IHDR first');
  const ch = ihdr.color === 6 ? 4 : ihdr.color === 2 ? 3 : 0;
  assert.ok(ch, 'colour type 2 or 6');
  const raw = zlib.inflateSync(Buffer.concat(idat));
  const stride = ihdr.w * ch;
  assert.strictEqual(raw.length, (stride + 1) * ihdr.h, 'inflated size');
  const px = Buffer.alloc(stride * ihdr.h);
  for (let y = 0; y < ihdr.h; y++) {
    assert.strictEqual(raw[y * (stride + 1)], 0, 'filter byte 0');
    raw.copy(px, y * stride, y * (stride + 1) + 1, (y + 1) * (stride + 1));
  }
  return { ihdr, ch, px };
}

console.log('\n=== CRC / PNG encoder ===');

test('crc32 matches the standard check value', () => {
  assert.strictEqual(H.crc32(Buffer.from('123456789')), 0xcbf43926);
  assert.strictEqual(H.crc32(Buffer.alloc(0)), 0);
});

test('RGB PNG round-trips through zlib', () => {
  const w = 5, h = 3, px = Buffer.alloc(w * h * 3);
  for (let i = 0; i < px.length; i++) px[i] = (i * 37) & 255;
  const d = decodePNG(H.encodePNG(w, h, px, 3));
  assert.deepStrictEqual({ w: d.ihdr.w, h: d.ihdr.h, depth: d.ihdr.depth, color: d.ihdr.color, interlace: d.ihdr.interlace }, { w: 5, h: 3, depth: 8, color: 2, interlace: 0 });
  assert.ok(d.px.equals(px), 'pixels identical');
});

test('RGBA PNG round-trips (colour type 6)', () => {
  const px = Buffer.from([1, 2, 3, 4, 250, 251, 252, 253]);
  const d = decodePNG(H.encodePNG(2, 1, px, 4));
  assert.strictEqual(d.ihdr.color, 6);
  assert.ok(d.px.equals(px));
});

test('encoder rejects wrong buffer size and channel count', () => {
  assert.throws(() => H.encodePNG(2, 2, Buffer.alloc(11), 3), /size mismatch/);
  assert.throws(() => H.encodePNG(1, 1, Buffer.alloc(2), 2), /channels/);
});

test('encoder accepts a Uint8Array view with a non-zero byteOffset', () => {
  const backing = Buffer.alloc(16, 9);
  const view = new Uint8Array(backing.buffer, backing.byteOffset + 4, 6);
  for (let i = 0; i < 6; i++) view[i] = i + 10;
  const d = decodePNG(H.encodePNG(2, 1, view, 3));
  assert.deepStrictEqual([...d.px], [10, 11, 12, 13, 14, 15]);
});

console.log('\n=== Terrarium encoding ===');

test('terrariumEncode/Decode round-trip within 1/256 m', () => {
  for (const m of [-32768, -500.5, -0.004, 0, 0.5, 1, 255.996, 256, 1234.56, 8848.86, 32767]) {
    const [r, g, b] = H.terrariumEncode(m);
    for (const c of [r, g, b]) assert.ok(Number.isInteger(c) && c >= 0 && c <= 255, `byte range for ${m}`);
    const back = H.terrariumDecode(r, g, b);
    assert.ok(back <= m + 1e-9 && m - back < 1 / 256 + 1e-9, `${m} -> ${back}`);
  }
});

test('terrariumEncode matches the contract formula (R*256 + G + B/256 - 32768)', () => {
  assert.deepStrictEqual(H.terrariumEncode(0), [128, 0, 0]);
  assert.deepStrictEqual(H.terrariumEncode(1.5), [128, 1, 128]);
  assert.deepStrictEqual(H.terrariumEncode(-1), [127, 255, 0]);
});

test('terrariumEncode clamps out-of-range elevations', () => {
  assert.deepStrictEqual(H.terrariumEncode(-40000), [0, 0, 0]);
  assert.deepStrictEqual(H.terrariumEncode(40000), [255, 255, 255]);
});

test('terrarium tile decodes to the synthetic terrain at every pixel', () => {
  const z = 9, x = 82, y = 197;
  const d = decodePNG(H.terrariumTile(z, x, y));
  assert.strictEqual(d.ihdr.w, 256); assert.strictEqual(d.ihdr.h, 256); assert.strictEqual(d.ch, 3);
  let maxErr = 0, lo = Infinity, hi = -Infinity;
  for (let j = 0; j < 256; j++) {
    for (let i = 0; i < 256; i++) {
      const o = (j * 256 + i) * 3;
      const got = H.terrariumDecode(d.px[o], d.px[o + 1], d.px[o + 2]);
      const want = H.syntheticElevation(z, x * 256 + i, y * 256 + j);
      maxErr = Math.max(maxErr, Math.abs(got - want));
      lo = Math.min(lo, got); hi = Math.max(hi, got);
    }
  }
  assert.ok(maxErr < 1 / 256 + 1e-9, `max error ${maxErr}`);
  assert.ok(hi - lo > 1, 'tile is not flat (shading has relief to work with)');
});

test('terrain is deterministic and continuous across zoom levels', () => {
  assert.ok(H.terrariumTile(5, 3, 7).equals(H.terrariumTile(5, 3, 7)), 'same bytes on repeat');
  // pixel (gx,gy) at z equals (2gx,2gy) at z+1 — same world point
  assert.ok(Math.abs(H.syntheticElevation(6, 1000, 2000) - H.syntheticElevation(7, 2000, 4000)) < 1e-9);
});

test('synthetic terrain spans negative and positive elevations', () => {
  let lo = Infinity, hi = -Infinity;
  for (let i = 0; i < 4096; i++) { const e = H.syntheticElevation(0, i * 0.0625, (i * 7919 % 4096) * 0.0625); lo = Math.min(lo, e); hi = Math.max(hi, e); }
  assert.ok(lo < 0 && hi > 1500, `range ${lo}..${hi}`);
});

console.log('\n=== Stub tiles / styles ===');

test('SVG stub names the provider and z/x/y on the provider colour', () => {
  const s = H.svgTile('carto-dark', 3, 4, 5);
  assert.ok(s.startsWith('<svg') && s.includes('carto-dark') && s.includes('3/4/5'));
  assert.ok(s.includes(H.providerColor('carto-dark')));
  assert.notStrictEqual(H.providerColor('carto-dark'), H.providerColor('carto-light'));
  assert.ok(/^#[0-9a-f]{6}$/.test(H.providerColor('x')));
});

test('SVG stub escapes the key', () => {
  assert.ok(!H.svgTile('<b>&', 0, 0, 0).includes('<b>'));
});

test('MapLibre style stub is a valid v8 style with no sources', () => {
  const st = H.maplibreStyle('openfreemap-style');
  assert.strictEqual(st.version, 8);
  assert.deepStrictEqual(st.sources, {});
  assert.strictEqual(st.layers.length, 1);
  assert.strictEqual(st.layers[0].type, 'background');
});

console.log('\n=== URL classification ===');

const C = (u, o) => H.classifyUrl(u, o);
test('known hosts map to provider keys with z/x/y', () => {
  assert.deepStrictEqual(C('https://a.basemaps.cartocdn.com/dark_all/5/9/12@2x.png'), { key: 'carto-dark', kind: 'raster', z: 5, x: 9, y: 12 });
  assert.strictEqual(C('https://b.basemaps.cartocdn.com/light_all/1/0/1.png?key=k').key, 'carto-light');
  assert.strictEqual(C('https://c.basemaps.cartocdn.com/rastertiles/voyager/2/1/1.png').key, 'carto-voyager');
  assert.strictEqual(C('https://tile.openstreetmap.org/3/1/2.png').key, 'osm');
  assert.strictEqual(C('https://a.tile.openstreetmap.fr/hot/3/1/2.png').key, 'osmfr');
  assert.strictEqual(C('https://tiles.maps.eox.at/wmts/1.0.0/s2cloudless-2020_3857/default/g/4/5/6.jpg').key, 'eox');
});

test('ArcGIS /tile/{z}/{y}/{x} is reordered to z/x/y', () => {
  const c = C('https://server.arcgisonline.com/ArcGIS/rest/services/Canvas/World_Dark_Gray_Base/MapServer/tile/4/6/3');
  assert.deepStrictEqual(c, { key: 'esri', kind: 'raster', z: 4, x: 3, y: 6 });
  assert.strictEqual(C('https://server.arcgisonline.com/ArcGIS/rest/services/Canvas/World_Dark_Gray_Reference/MapServer/tile/4/6/3').key, 'esri-ref');
  assert.strictEqual(C('https://basemap.nationalmap.gov/arcgis/rest/services/USGSImageryTopo/MapServer/tile/2/1/0').key, 'usgs-imagery');
  assert.strictEqual(C('https://basemap.nationalmap.gov/arcgis/rest/services/USGSTopo/MapServer/tile/2/1/0').key, 'usgs-topo');
});

test('terrarium, OpenFreeMap styles and vector tiles', () => {
  assert.deepStrictEqual(C('https://s3.amazonaws.com/elevation-tiles-prod/terrarium/10/163/395.png'), { key: 'terrarium', kind: 'terrarium', z: 10, x: 163, y: 395 });
  assert.strictEqual(C('https://tiles.openfreemap.org/styles/liberty').kind, 'style');
  assert.strictEqual(C('https://tiles.openfreemap.org/planet/20250101_001001_pt/5/1/2.pbf').kind, 'vector');
});

test('same-site custom tile paths are caught, API and app assets are not', () => {
  const base = { baseUrl: 'http://localhost:13581' };
  assert.deepStrictEqual(C('http://localhost:13581/tiles/7/20/49.png', base), { key: 'custom', kind: 'raster', z: 7, x: 20, y: 49 });
  assert.strictEqual(C('http://localhost:13581/api/nodes/1/2/3', base), null);
  assert.strictEqual(C('http://localhost:13581/app.js?v=123', base), null);
  assert.strictEqual(C('http://localhost:13581/vendor/leaflet/leaflet.js', base), null);
});

test('unknown external XYZ hosts and registered style URLs are recognised', () => {
  assert.strictEqual(C('https://tiles.example.net/base/3/2/1.webp').key, 'host:tiles.example.net');
  assert.strictEqual(C('https://maps.example.org/style.json').kind, 'style');
  assert.deepStrictEqual(C('https://x.example/custom-style', { styleUrls: [{ key: 'mine', test: /custom-style/ }] }), { key: 'mine', kind: 'style', z: null, x: null, y: null });
  assert.strictEqual(C('https://cdn.example.com/lib.js'), null);
  assert.strictEqual(C('data:image/png;base64,AAAA'), null);
});

test('extraHosts rows take precedence over the built-in table', () => {
  assert.strictEqual(C('https://tile.openstreetmap.org/1/0/0.png', { extraHosts: [{ key: 'osm-test', kind: 'raster', test: /openstreetmap\.org/ }] }).key, 'osm-test');
});

test('redactUrl hides credentials in tile URLs and error strings', () => {
  assert.strictEqual(H.redactUrl('https://a.basemaps.cartocdn.com/x/1/2/3.png?key=s3cr3t'), 'https://a.basemaps.cartocdn.com/x/1/2/3.png?key=REDACTED');
  assert.strictEqual(H.redactUrl('https://t.example/1/2/3.png?lang=en&api_key=abc&x=1'), 'https://t.example/1/2/3.png?lang=en&api_key=REDACTED&x=1');
  assert.strictEqual(H.redactUrl('net::ERR_FAILED at https://m.example/s.json?access_token=pk.zz#f'), 'net::ERR_FAILED at https://m.example/s.json?access_token=REDACTED#f');
  assert.strictEqual(H.redactUrl('https://tile.openstreetmap.org/1/0/0.png'), 'https://tile.openstreetmap.org/1/0/0.png');
  assert.strictEqual(H.redactUrl('https://x.example/?monkey=1'), 'https://x.example/?monkey=1');
  assert.strictEqual(H.redactUrl(null), null);
});

// waitForTiles against a fake Playwright page: the predicate runs in Node
// with a fake window.__mcHarness whose tileState() the test controls.
function fakePage() {
  return {
    async waitForFunction(fn, arg, opts) {
      const t0 = Date.now();
      for (;;) {
        if (fn(arg)) return true;
        if (Date.now() - t0 > opts.timeout) throw new Error('Timeout ' + opts.timeout + 'ms exceeded.');
        await new Promise(r => setTimeout(r, 5));
      }
    },
    async evaluate(fn) { return fn(); },
  };
}
async function withWindow(state, fn) {
  const prev = global.window;
  global.window = { __mcHarness: { tileState: () => Object.assign({}, state) } };
  try { return await fn(); } finally { global.window = prev; }
}
async function atest(name, fn) {
  try { await fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

(async () => {
  await atest('waitForTiles settles on a loaded vector basemap (canvas, no .leaflet-tile)', async () => {
    const st = { visible: 0, loaded: 0, failed: 0, pending: 0, maplibreCanvases: 1, maplibreMaps: 1 };
    const r = await withWindow(st, () => H.waitForTiles(fakePage(), { timeout: 300 }));
    assert.strictEqual(r.timedOut, false, 'vector map with nothing pending must not wait for the timeout');
  });
  await atest('waitForTiles still waits while a vector basemap has tiles pending', async () => {
    const st = { visible: 0, loaded: 0, failed: 0, pending: 1, maplibreCanvases: 1, maplibreMaps: 1 };
    const r = await withWindow(st, () => H.waitForTiles(fakePage(), { timeout: 150 }));
    assert.strictEqual(r.timedOut, true);
  });
  await atest('waitForTiles without a vector map still requires a visible raster tile', async () => {
    const st = { visible: 0, loaded: 0, failed: 0, pending: 0, maplibreCanvases: 0, maplibreMaps: 0 };
    const r = await withWindow(st, () => H.waitForTiles(fakePage(), { timeout: 150 }));
    assert.strictEqual(r.timedOut, true);
  });
  await atest('waitForTiles settles on loaded raster tiles', async () => {
    const st = { visible: 6, loaded: 6, failed: 0, pending: 0, maplibreCanvases: 0, maplibreMaps: 0 };
    const r = await withWindow(st, () => H.waitForTiles(fakePage(), { timeout: 300 }));
    assert.strictEqual(r.timedOut, false);
  });
  console.log(`\n${passed} passed, ${failed} failed`);
  if (failed) process.exit(1);
})();
