/**
 * #1849 — "Hop Bytes" (col-hashsize) column shows misleading `1` for TRACE packets.
 *
 * TRACE packets (payload_type=9) use header path bytes as per-hop SNR readings, not
 * truncated hop hashes (see internal/packetpath/route.go PathBytesAreHops(TRACE)=false).
 * The high-2-bit "hash_size" derivation is meaningless for TRACE.
 *
 * Fix: at the 3 render sites in public/packets.js (buildGroupRowHtml header, its child
 * rows, buildFlatRowHtml), when payload_type === 9 render `—` in col-hashsize with an
 * explanatory title tooltip. Non-TRACE behavior unchanged.
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

// Reuse the sandbox construction from test-packets.js (kept in sync intentionally).
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

console.log('\n=== #1849: TRACE col-hashsize renders — not misleading integer ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;
  assert(api, '_packetsTestAPI must be exposed');

  test('buildFlatRowHtml: TRACE (payload_type=9) renders — in col-hashsize with title tooltip', () => {
    const p = {
      id: 100, hash: 'trace1', timestamp: '', observer_id: null,
      raw_hex: '00090000000000000000000102030405', payload_type: 9,
      route_type: 0, decoded_json: '{}', path_json: '[]'
    };
    const html = api.buildFlatRowHtml(p);
    const m = html.match(/<td class="col-hashsize[^"]*"([^>]*)>([^<]*)<\/td>/);
    assert(m, 'col-hashsize cell must be present. Got: ' + html.slice(0, 500));
    assert.strictEqual(m[2].trim(), '—',
      'TRACE col-hashsize must be — (em-dash), got: ' + JSON.stringify(m[2]));
    assert(/title="[^"]*TRACE[^"]*"/.test(m[1]),
      'col-hashsize <td> must carry a title attribute mentioning TRACE. Got attrs: ' + m[1]);
  });

  test('buildFlatRowHtml: non-TRACE (payload_type=0) still renders numeric hashBytes', () => {
    const p = {
      id: 101, hash: 'adv1', timestamp: '', observer_id: null,
      raw_hex: '00000000000000000000C100', payload_type: 0,
      route_type: 0, decoded_json: '{}', path_json: '[]'
    };
    const html = api.buildFlatRowHtml(p);
    const m = html.match(/<td class="col-hashsize[^"]*"[^>]*>([^<]*)<\/td>/);
    assert(m, 'col-hashsize cell must be present');
    assert(/^\d+$/.test(m[1].trim()),
      'non-TRACE col-hashsize must be numeric, got: ' + JSON.stringify(m[1]));
  });

  test('buildGroupRowHtml: TRACE header row (payload_type=9) renders — in col-hashsize with title', () => {
    const p = {
      hash: 'trg1', count: 1, latest: '',
      observer_id: null, raw_hex: '00090000000000000000000102030405',
      payload_type: 9, route_type: 0, decoded_json: '{}', path_json: '[]',
      observation_count: 1, observer_count: 1,
    };
    const html = api.buildGroupRowHtml(p);
    const m = html.match(/<td class="col-hashsize[^"]*"([^>]*)>([^<]*)<\/td>/);
    assert(m, 'col-hashsize cell must be present');
    assert.strictEqual(m[2].trim(), '—',
      'TRACE group header col-hashsize must be —, got: ' + JSON.stringify(m[2]));
    assert(/title="[^"]*TRACE[^"]*"/.test(m[1]),
      'col-hashsize <td> must carry title mentioning TRACE');
  });

  test('buildGroupRowHtml: non-TRACE header row still numeric', () => {
    const p = {
      hash: 'grp1', count: 1, latest: '',
      observer_id: null, raw_hex: '00000000000000000000C100',
      payload_type: 0, route_type: 0, decoded_json: '{}', path_json: '[]',
      observation_count: 1, observer_count: 1,
    };
    const html = api.buildGroupRowHtml(p);
    const m = html.match(/<td class="col-hashsize[^"]*"[^>]*>([^<]*)<\/td>/);
    assert(m, 'col-hashsize cell must be present');
    assert(/^\d+$/.test(m[1].trim()),
      'non-TRACE group col-hashsize must be numeric, got: ' + JSON.stringify(m[1]));
  });
}

console.log('');
if (failed > 0) {
  console.error(`❌ ${failed} test(s) failed, ${passed} passed`);
  process.exit(1);
}
console.log(`✅ All ${passed} tests passed`);
