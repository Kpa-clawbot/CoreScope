/**
 * HB (col-hashsize) must read the route type from the row's own raw_hex header.
 *
 * One transmission stores one route_type (from its first observation), but the
 * packet hash excludes the route bits, so the same packet can be heard as FLOOD
 * by one observer and as TRANSPORT_FLOOD by another. The table used the
 * transmission's route_type to find path_len in an observation's raw_hex, read a
 * transport code byte instead (0xE2 >> 6 = 3) and showed HB 4, a hash size the
 * firmware rejects (Packet::isValidPathLen, path mode 3 is reserved).
 *
 * The fix renders all three HB sites through pathHashSize (app.js), which takes
 * the route type from the header byte of the same raw_hex.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

// Sandbox construction mirrors test-issue-1849-trace-hashbytes.js.
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
  vm.runInContext(`
    window.HopDisplay = {
      renderHop: function(h, entry, opts) { return '<span>' + h + '</span>'; },
      _showFromBtn: function() {}
    };
  `, ctx);
  loadInCtx(ctx, 'public/packets.js');
  return ctx;
}

function hbCell(html) {
  const m = html.match(/<td class="col-hashsize[^"]*"[^>]*>([^<]*)<\/td>/);
  assert(m, 'col-hashsize cell must be present. Got: ' + html.slice(0, 500));
  return m[1].trim();
}

// Observation heard as TRANSPORT_FLOOD REQ (header 0x00): transport codes E2E0 0000,
// path_len 0x1D (1-byte hashes, 29 hops). Shape taken from a live observation whose
// transmission was stored as FLOOD (route_type 1).
const TRANSPORT_OBS_HEX = '00E2E000001D3A646A2A3C5A8660B8';
// FLOOD REQ (header 0x01), path_len 0x42 = 2-byte hashes, 2 hops.
const FLOOD_2BYTE_HEX = '0142AABBCCDD';

console.log('\n=== HB column takes the route type from the raw_hex header ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;
  assert(api, '_packetsTestAPI must be exposed');

  test('hashBytesText: transport header, FLOOD transmission → 1, not 4', () => {
    assert.strictEqual(api.hashBytesText(TRANSPORT_OBS_HEX), 1);
  });

  test('hashBytesText: flood header with 2-byte hashes → 2', () => {
    assert.strictEqual(api.hashBytesText(FLOOD_2BYTE_HEX), 2);
  });

  test('hashBytesText: reserved path mode 3 → —, never 4', () => {
    assert.strictEqual(api.hashBytesText('01C1AA'), '—');
  });

  test('hashBytesText: missing raw_hex → —', () => {
    assert.strictEqual(api.hashBytesText(null), '—');
  });

  test('buildFlatRowHtml: transport raw_hex on a FLOOD transmission → 1', () => {
    const html = api.buildFlatRowHtml({
      id: 1, hash: 'hb1', timestamp: '', observer_id: null,
      raw_hex: TRANSPORT_OBS_HEX, payload_type: 0, route_type: 1,
      decoded_json: '{}', path_json: '[]',
    });
    assert.strictEqual(hbCell(html), '1');
  });

  test('buildFlatRowHtml: flood raw_hex on a TRANSPORT_FLOOD transmission → 2', () => {
    const html = api.buildFlatRowHtml({
      id: 2, hash: 'hb2', timestamp: '', observer_id: null,
      raw_hex: FLOOD_2BYTE_HEX, payload_type: 0, route_type: 0,
      decoded_json: '{}', path_json: '[]',
    });
    assert.strictEqual(hbCell(html), '2');
  });

  test('buildGroupRowHtml: transport raw_hex on a FLOOD transmission → 1', () => {
    const html = api.buildGroupRowHtml({
      hash: 'hb3', count: 1, latest: '', observer_id: null,
      raw_hex: TRANSPORT_OBS_HEX, payload_type: 0, route_type: 1,
      decoded_json: '{}', path_json: '[]', observation_count: 1, observer_count: 1,
    });
    assert.strictEqual(hbCell(html), '1');
  });
}

console.log('');
if (failed > 0) {
  console.error(`❌ ${failed} test(s) failed, ${passed} passed`);
  process.exit(1);
}
console.log(`✅ All ${passed} tests passed`);
