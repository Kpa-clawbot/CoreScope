/**
 * #1868: CONTROL DISCOVER_REQ / DISCOVER_RESP fields rendered for humans.
 *
 * Firmware (meshcore-dev/MeshCore):
 *   docs/payloads.md:266-282                      DISCOVER_REQ/RESP layout, "snr: signed, SNR*4"
 *   examples/simple_repeater/MyMesh.cpp:820-821   data[0] = RESP | ADV_TYPE_x; data[1] = packet->_snr
 *   src/Dispatcher.cpp:206                        _snr = getLastSNR() * 4.0f
 *   src/Packet.h:51,92                            int8_t _snr; getSNR() = _snr / 4.0f
 *   src/helpers/AdvertDataHelpers.h:7-11          ADV_TYPE_* values
 *
 * The ingestor (cmd/ingestor/decoder.go decodeControl) emits ctrlNodeType,
 * ctrlFilter, ctrlSNR (raw int8, SNR*4) and ctrlPubKey (64 or 16 hex chars).
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
  loadInCtx(ctx, 'public/hop-resolver.js');
  vm.runInContext(`
    window.HopDisplay = {
      renderHop: function(h, entry, opts) { return '<span>' + h + '</span>'; },
      _showFromBtn: function() {}
    };
  `, ctx);
  loadInCtx(ctx, 'public/packets.js');
  return ctx;
}

const KNOWN_KEY = 'ab12cd34'.repeat(8);
const UNKNOWN_KEY = 'fe98dc76'.repeat(8);
const EVIL_KEY = '0badc0de'.repeat(8);
// Zero-hop direct CONTROL: header 0x2E (payload CONTROL 0x0B << 2 | route DIRECT 2), path_len 0x00.
const CTRL_PKT = { raw_hex: '2e00', route_type: 2, payload_type: 11 };

console.log('\n=== #1868: CONTROL DISCOVER fields rendered for humans ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;
  assert(api, '_packetsTestAPI must be exposed');
  ctx.HopResolver.init([
    { public_key: KNOWN_KEY, name: 'Known Repeater', role: 'repeater' },
    { public_key: EVIL_KEY, name: '<img src=x onerror=alert(1)>', role: 'repeater' },
  ]);

  // --- type by name ---
  test('preview: DISCOVER_RESP node type 2 renders as Repeater, not a number', () => {
    const html = api.getDetailPreview({ type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlNodeType: 2 });
    assert(html.includes('type=Repeater'), 'got: ' + html);
    assert(!/type=2\b/.test(html), 'raw number leaked: ' + html);
  });

  test('preview: DISCOVER_RESP node type 4 renders as Sensor', () => {
    const html = api.getDetailPreview({ type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlNodeType: 4 });
    assert(html.includes('type=Sensor'), 'got: ' + html);
  });

  test('preview: DISCOVER_REQ filter bitmask renders type names', () => {
    // (1 << ADV_TYPE_REPEATER) | (1 << ADV_TYPE_SENSOR) = 0x04 | 0x10
    const html = api.getDetailPreview({ type: 'CONTROL', ctrlSubtype: 'DISCOVER_REQ', ctrlFilter: 0x14 });
    assert(html.includes('filter=Repeater+Sensor'), 'got: ' + html);
  });

  test('detail: DISCOVER_RESP node type row shows the name', () => {
    const html = api.buildFieldTable(CTRL_PKT, { type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlFlags: '92', ctrlNodeType: 2 }, [], []);
    assert(/<td>Node Type<\/td><td class="mono">Repeater<\/td>/.test(html), 'got: ' + html);
    assert(!html.includes('>Raw<'), 'CONTROL must not fall through to the Raw row: ' + html);
  });

  test('detail: DISCOVER_REQ filter row keeps hex and names the requested types', () => {
    const html = api.buildFieldTable(CTRL_PKT, { type: 'CONTROL', ctrlSubtype: 'DISCOVER_REQ', ctrlFlags: '80', ctrlFilter: 4, ctrlTag: 0xDEADBEEF }, [], []);
    assert(html.includes('0x04'), 'got: ' + html);
    assert(html.includes('Repeater'), 'got: ' + html);
    assert(html.includes('DEADBEEF'), 'got: ' + html);
  });

  // --- SNR from the wire value (int8, SNR*4) ---
  test('preview: SNR wire 16 renders as 4.00dB', () => {
    const html = api.getDetailPreview({ type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlSNR: 16 });
    assert(html.includes('snr=4.00dB'), 'got: ' + html);
    assert(!/snr=16\b/.test(html), 'raw wire value leaked: ' + html);
  });

  test('preview: negative SNR wire -21 renders as -5.25dB', () => {
    const html = api.getDetailPreview({ type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlSNR: -21 });
    assert(html.includes('snr=-5.25dB'), 'got: ' + html);
  });

  test('detail: negative SNR wire -21 renders as -5.25 dB', () => {
    const html = api.buildFieldTable(CTRL_PKT, { type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlSNR: -21 }, [], []);
    assert(html.includes('-5.25 dB'), 'got: ' + html);
  });

  // --- pubkey: known node name link, unknown 8-char prefix ---
  test('preview: known full pubkey renders the node name, not the key', () => {
    const html = api.getDetailPreview({ type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlPubKey: KNOWN_KEY });
    assert(html.includes('Known Repeater'), 'got: ' + html);
    assert(!html.includes(KNOWN_KEY), 'full key leaked: ' + html);
  });

  test('detail: known full pubkey renders a link to the node', () => {
    const html = api.buildFieldTable(CTRL_PKT, { type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlPubKey: KNOWN_KEY }, [], []);
    assert(html.includes('href="#/nodes/' + KNOWN_KEY + '"'), 'got: ' + html);
    assert(html.includes('>Known Repeater</a>'), 'got: ' + html);
  });

  test('detail: known 8-byte prefix (prefix_only) links to the full node key', () => {
    const html = api.buildFieldTable(CTRL_PKT, { type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlPubKey: KNOWN_KEY.slice(0, 16) }, [], []);
    assert(html.includes('href="#/nodes/' + KNOWN_KEY + '"'), 'got: ' + html);
    assert(html.includes('>Known Repeater</a>'), 'got: ' + html);
  });

  test('preview: unknown pubkey renders the first 8 hex chars only', () => {
    const html = api.getDetailPreview({ type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlPubKey: UNKNOWN_KEY });
    assert(html.includes('pubkey=fe98dc76'), 'got: ' + html);
    assert(!html.includes(UNKNOWN_KEY.slice(0, 9)), 'more than 8 chars leaked: ' + html);
  });

  test('detail: unknown pubkey renders the first 8 hex chars, no link', () => {
    const html = api.buildFieldTable(CTRL_PKT, { type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlPubKey: UNKNOWN_KEY }, [], []);
    assert(html.includes('fe98dc76'), 'got: ' + html);
    assert(!html.includes(UNKNOWN_KEY.slice(0, 9)), 'more than 8 chars leaked: ' + html);
    assert(!html.includes('#/nodes/'), 'unknown key must not be linked: ' + html);
  });

  test('node names from the node list are escaped in preview and detail', () => {
    const decoded = { type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlPubKey: EVIL_KEY };
    const preview = api.getDetailPreview(decoded);
    const detail = api.buildFieldTable(CTRL_PKT, decoded, [], []);
    assert(!preview.includes('<img'), 'preview not escaped: ' + preview);
    assert(!detail.includes('<img'), 'detail not escaped: ' + detail);
    assert(detail.includes('&lt;img'), 'detail should carry the escaped name: ' + detail);
  });
}

console.log('');
if (failed > 0) {
  console.error(`❌ ${failed} test(s) failed, ${passed} passed`);
  process.exit(1);
}
console.log(`✅ All ${passed} tests passed`);
