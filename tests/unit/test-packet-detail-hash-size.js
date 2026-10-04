/**
 * The packet page reads a packet's path hash size with the same rule as the
 * Channels view (pathHashSize in public/app.js), so the two pages one click
 * apart cannot disagree.
 *
 * Firmware (meshcore-dev/MeshCore):
 *   src/Mesh.cpp sendFlood    setPathHashSizeAndCount(size, 0) before the first
 *                             hop, so a flood heard at 0 hops still carries it
 *   src/Mesh.cpp sendZeroHop  path_len = 0 on a DIRECT route: no size at all
 *
 * Before this, packets.js hid the size whenever the hop count was 0, for every
 * route type: a 0-hop flood channel message showed "2-bytes" on the Channels
 * page and no Hash Size row on its packet page.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

function makeSandbox() {
  const registeredPages = {};
  const ctx = {
    window: {
      addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => {},
      innerWidth: 1200, PacketFilter: null,
    },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '', className: '', style: {},
        appendChild: () => {}, setAttribute: () => {}, addEventListener: () => {},
        querySelectorAll: () => [], querySelector: () => null,
        classList: { add: () => {}, remove: () => {}, contains: () => false } }),
      head: { appendChild: () => {} }, getElementById: () => null,
      addEventListener: () => {}, removeEventListener: () => {},
      querySelectorAll: () => [], querySelector: () => null, body: { appendChild: () => {} },
    },
    console, Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp,
    Error, TypeError, RangeError, parseInt, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent,
    setTimeout: () => {}, clearTimeout: () => {}, setInterval: () => {}, clearInterval: () => {},
    fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    localStorage: (() => { const s = {}; return {
      getItem: k => s[k] || null, setItem: (k, v) => { s[k] = String(v); }, removeItem: k => { delete s[k]; },
    }; })(),
    location: { hash: '' }, history: { replaceState: () => {} },
    CustomEvent: class CustomEvent {}, Map, Set, Promise, URLSearchParams,
    addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => {},
    requestAnimationFrame: (cb) => setTimeout(cb, 0),
    registerPage: (name, handler) => { registeredPages[name] = handler; },
  };
  vm.createContext(ctx);
  return ctx;
}

function loadInCtx(ctx, file) {
  vm.runInContext(fs.readFileSync(file, 'utf8'), ctx, { filename: file });
  for (const k of Object.keys(ctx.window)) { ctx[k] = ctx.window[k]; }
}

function loadPacketsSandbox() {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/payload-labels.js');
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  loadInCtx(ctx, 'public/packet-helpers.js');
  loadInCtx(ctx, 'public/hop-resolver.js');
  vm.runInContext(`
    window.HopDisplay = {
      renderHop: function(h) { return '<span>' + h + '</span>'; },
      _showFromBtn: function() {}
    };
  `, ctx);
  loadInCtx(ctx, 'public/packets.js');
  ctx.fetchAllNodes = () => Promise.resolve({ nodes: [] });
  ctx.api = (path) => Promise.resolve(path === '/observers' ? { observers: [] } : {});
  return ctx;
}

const PAYLOAD = 'AA'.repeat(24);
// Header byte = (payload_type << 2) | route_type. GRP_TXT=5, ADVERT=4.
const GRP_TXT = { type: 'CHAN', channel: '#test', sender: 'Alice', text: 'Alice: hi' };
const PACKETS = {
  floodHeardDirect2: { raw_hex: '1540' + PAYLOAD, route_type: 1, payload_type: 5 },
  flood1Hop2: { raw_hex: '1541AABB' + PAYLOAD, route_type: 1, payload_type: 5, path_json: '["AABB"]' },
  directZeroHop: { raw_hex: '1600' + PAYLOAD, route_type: 2, payload_type: 5 },
  directNoHopsSizeBits: { raw_hex: '1640' + PAYLOAD, route_type: 2, payload_type: 5 },
};

async function detailHashSizeRow(pkt) {
  const ctx = loadPacketsSandbox();
  const panel = { innerHTML: '', querySelectorAll: () => [], querySelector: () => null, addEventListener: () => {} };
  await ctx._packetsTestAPI.renderDetail(panel, {
    packet: Object.assign({ id: 1, hash: 'h1', decoded_json: JSON.stringify(GRP_TXT), path_json: '[]' }, pkt),
    observations: [],
  });
  assert.ok(panel.innerHTML.includes('detail-meta'), 'renderDetail did not render the meta list');
  const m = panel.innerHTML.match(/<dt>Hash Size<\/dt><dd>([^<]*)<\/dd>/);
  return m ? m[1] : null;
}

function fieldTable(pkt, decoded) {
  return loadPacketsSandbox()._packetsTestAPI.buildFieldTable(pkt, decoded || {}, [], []);
}

(async () => {
  console.log('\n=== packet detail: Hash Size row ===');

  await test('0-hop flood: Hash Size row shows the size the sender set', async () => {
    assert.strictEqual(await detailHashSizeRow(PACKETS.floodHeardDirect2), '2 bytes');
  });
  await test('relayed flood: Hash Size row unchanged', async () => {
    assert.strictEqual(await detailHashSizeRow(PACKETS.flood1Hop2), '2 bytes');
  });
  await test('direct zero-hop: no Hash Size row', async () => {
    assert.strictEqual(await detailHashSizeRow(PACKETS.directZeroHop), null);
  });
  await test('direct with no hops but size bits set: no Hash Size row', async () => {
    assert.strictEqual(await detailHashSizeRow(PACKETS.directNoHopsSizeBits), null);
  });

  console.log('\n=== packet detail: hex breakdown Path Length ===');

  await test('0-hop flood: Path Length gives the size, not "direct advert"', () => {
    const html = fieldTable(PACKETS.floodHeardDirect2);
    assert.ok(html.includes('hash_size=2 bytes, hash_count=0'), 'got: ' + html);
    assert.ok(!html.includes('direct advert'), 'a channel message is not an advert');
  });
  await test('direct zero-hop: Path Length says no size is encoded', () => {
    const html = fieldTable(PACKETS.directZeroHop);
    assert.ok(html.includes('hash_count=0 (no hash size encoded)'), 'got: ' + html);
    assert.ok(!html.includes('hash_size='), 'no size to show');
  });
  await test('relayed flood: Path Length unchanged', () => {
    assert.ok(fieldTable(PACKETS.flood1Hop2).includes('hash_size=2 bytes, hash_count=1'));
  });

  console.log('\n=== packet detail: Advertised Hash Size (ADVERT) ===');

  const pubKey = 'C0DEDAD4'.padEnd(64, '0');
  const advert = (pathByte, routeType) => ({
    raw_hex: ((4 << 2) | routeType).toString(16).padStart(2, '0') + pathByte + pubKey + '00000000' + '0'.repeat(128),
    route_type: routeType, payload_type: 4,
  });
  const advertSize = (html) => {
    const m = html.match(/Advertised Hash Size<\/td><td[^>]*>([^<]*)</);
    return m ? m[1] : null;
  };

  await test('0-hop flood advert: Advertised Hash Size shown', () => {
    assert.strictEqual(advertSize(fieldTable(advert('80', 1), { type: 'ADVERT', pubKey })), '3 bytes');
  });
  await test('direct zero-hop advert: no Advertised Hash Size', () => {
    assert.strictEqual(advertSize(fieldTable(advert('00', 2), { type: 'ADVERT', pubKey })), null);
  });

  console.log('');
  if (failed > 0) {
    console.error(`❌ ${failed} test(s) failed, ${passed} passed`);
    process.exit(1);
  }
  console.log(`✅ All ${passed} tests passed`);
})();
