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

  // --- pubkey: known node name link; unknown key 8 chars in preview, full in detail ---
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

  test('detail: unknown pubkey renders the full key, no link', () => {
    // The row preview stays short (#1868); the field table is the one place
    // the responder key is readable outside the hex dump.
    const html = api.buildFieldTable(CTRL_PKT, { type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlPubKey: UNKNOWN_KEY }, [], []);
    assert(html.includes(UNKNOWN_KEY), 'full key missing: ' + html);
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

// Offset / name / value / description of every field row, in order.
function fieldRows(html) {
  const rows = [];
  const re = /<tr><td class="mono">(\d+)<\/td><td>(.*?)<\/td><td class="mono">(.*?)<\/td><td class="text-muted">(.*?)<\/td><\/tr>/g;
  let m;
  while ((m = re.exec(html))) rows.push({ off: Number(m[1]), name: m[2], value: m[3], desc: m[4] });
  return rows;
}
function payloadRows(html) {
  return fieldRows(html).filter(r => r.name !== 'Header Byte' && r.name !== 'Path Length');
}
function rowsSummary(rows) {
  return rows.map(r => r.off + ':' + r.name).join(' | ');
}

console.log('\n=== #1868 review: field offsets, trailing bytes, filter bits ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;
  ctx.HopResolver.init([{ public_key: KNOWN_KEY, name: 'Known Repeater', role: 'repeater' }]);
  const TAG = 'efbeadde';

  // --- offsets: zero-hop direct packet, payload starts at byte 2 ---
  test('detail: DISCOVER_REQ rows sit at offsets 2/3/4/8', () => {
    const decoded = { type: 'CONTROL', ctrlSubtype: 'DISCOVER_REQ', ctrlFlags: '80', ctrlFilter: 4, ctrlTag: 0xDEADBEEF, ctrlSince: 1700000000 };
    const pkt = { raw_hex: '2e00' + '80' + '04' + TAG + '00f15365', route_type: 2, payload_type: 11 };
    const rows = payloadRows(api.buildFieldTable(pkt, decoded, [], []));
    assert.strictEqual(rowsSummary(rows), '2:Subtype | 3:Type Filter (1B) | 4:Tag (4B) | 8:Since (4B)');
  });

  test('detail: DISCOVER_RESP rows sit at offsets 2/2/3/4/8', () => {
    const decoded = { type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlFlags: '92', ctrlNodeType: 2, ctrlSNR: 16, ctrlTag: 0xDEADBEEF, ctrlPubKey: UNKNOWN_KEY };
    const pkt = { raw_hex: '2e00' + '92' + '10' + TAG + UNKNOWN_KEY, route_type: 2, payload_type: 11 };
    const rows = payloadRows(api.buildFieldTable(pkt, decoded, [], []));
    assert.strictEqual(rowsSummary(rows), '2:Subtype | 2:Node Type | 3:SNR (1B) | 4:Tag (4B) | 8:Public Key (32B)');
  });

  test('detail: DISCOVER_REQ since row renders the epoch value', () => {
    const decoded = { type: 'CONTROL', ctrlSubtype: 'DISCOVER_REQ', ctrlFlags: '80', ctrlFilter: 4, ctrlTag: 1, ctrlSince: 1700000000 };
    const rows = fieldRows(api.buildFieldTable(CTRL_PKT, decoded, [], []));
    const since = rows.find(r => r.name === 'Since (4B)');
    assert(since, 'Since row missing: ' + rowsSummary(rows));
    assert.strictEqual(since.value, '1700000000');
  });

  test('detail: DISCOVER_REQ since=0 renders as no filter', () => {
    // Firmware examples/simple_repeater/MyMesh.cpp:814 defaults since to 0,
    // and :817 answers when discovery_mod_timestamp >= since, so 0 filters nothing.
    const decoded = { type: 'CONTROL', ctrlSubtype: 'DISCOVER_REQ', ctrlFlags: '80', ctrlFilter: 4, ctrlTag: 1, ctrlSince: 0 };
    const since = fieldRows(api.buildFieldTable(CTRL_PKT, decoded, [], [])).find(r => r.name === 'Since (4B)');
    assert(since, 'Since row missing');
    assert.strictEqual(since.value, '0 (no filter)');
  });

  test('detail: DISCOVER_REQ subtype row decodes prefix_only (flags bit 0)', () => {
    const on = fieldRows(api.buildFieldTable(CTRL_PKT, { type: 'CONTROL', ctrlSubtype: 'DISCOVER_REQ', ctrlFlags: '81', ctrlFilter: 4, ctrlTag: 1 }, [], []))
      .find(r => r.name === 'Subtype');
    const off = fieldRows(api.buildFieldTable(CTRL_PKT, { type: 'CONTROL', ctrlSubtype: 'DISCOVER_REQ', ctrlFlags: '80', ctrlFilter: 4, ctrlTag: 1 }, [], []))
      .find(r => r.name === 'Subtype');
    assert(on.desc.includes('prefix_only=1'), 'got: ' + on.desc);
    assert(off.desc.includes('prefix_only=0'), 'got: ' + off.desc);
  });

  // --- pubkey length label ---
  test('detail: 64-hex key is labelled 32B, 16-hex key 8B prefix', () => {
    const full = fieldRows(api.buildFieldTable(CTRL_PKT, { type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlPubKey: UNKNOWN_KEY }, [], []));
    const pre = fieldRows(api.buildFieldTable(CTRL_PKT, { type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlPubKey: UNKNOWN_KEY.slice(0, 16) }, [], []));
    assert(full.some(r => r.name === 'Public Key (32B)'), rowsSummary(full));
    assert(pre.some(r => r.name === 'Public Key (8B prefix)'), rowsSummary(pre));
  });

  // --- UNKNOWN subtype keeps its body ---
  test('detail: UNKNOWN subtype shows a Raw row with the body bytes at offset 3', () => {
    const decoded = { type: 'CONTROL', ctrlSubtype: 'UNKNOWN', ctrlFlags: 'a0' };
    const rows = payloadRows(api.buildFieldTable({ raw_hex: '2e00a00102', route_type: 2, payload_type: 11 }, decoded, [], []));
    const raw = rows.find(r => r.name === 'Raw');
    assert(raw, 'Raw row missing: ' + rowsSummary(rows));
    assert.strictEqual(raw.off, 3);
    assert.strictEqual(raw.value, '0102');
  });

  // --- bytes past the last decoded field (decoder.go decodeControl length gates) ---
  test('detail: truncated DISCOVER_REQ (80 04 12) keeps 0412 in a Raw row at offset 3', () => {
    const decoded = { type: 'CONTROL', ctrlSubtype: 'DISCOVER_REQ', ctrlFlags: '80' };
    const rows = payloadRows(api.buildFieldTable({ raw_hex: '2e00800412', route_type: 2, payload_type: 11 }, decoded, [], []));
    const raw = rows.find(r => r.name === 'Raw');
    assert(raw, 'Raw row missing: ' + rowsSummary(rows));
    assert.strictEqual(raw.off, 3);
    assert.strictEqual(raw.value, '0412');
  });

  test('detail: DISCOVER_REQ with 2 bytes after the tag (no since) keeps them at offset 8', () => {
    const decoded = { type: 'CONTROL', ctrlSubtype: 'DISCOVER_REQ', ctrlFlags: '80', ctrlFilter: 4, ctrlTag: 0xDEADBEEF };
    const rows = payloadRows(api.buildFieldTable({ raw_hex: '2e00' + '8004' + TAG + 'aabb', route_type: 2, payload_type: 11 }, decoded, [], []));
    const raw = rows.find(r => r.name === 'Raw');
    assert(raw, 'Raw row missing: ' + rowsSummary(rows));
    assert.strictEqual(raw.off, 8);
    assert.strictEqual(raw.value, 'aabb');
  });

  test('detail: truncated DISCOVER_RESP (5 bytes) keeps its body in a Raw row at offset 3', () => {
    const decoded = { type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlFlags: '92', ctrlNodeType: 2 };
    const rows = payloadRows(api.buildFieldTable({ raw_hex: '2e00' + '92' + '10efbead', route_type: 2, payload_type: 11 }, decoded, [], []));
    const raw = rows.find(r => r.name === 'Raw');
    assert(raw, 'Raw row missing: ' + rowsSummary(rows));
    assert.strictEqual(raw.off, 3);
    assert.strictEqual(raw.value, '10efbead');
  });

  test('detail: DISCOVER_RESP with 10 key bytes shows the 8B prefix and the 2 extra bytes at offset 16', () => {
    const keyBytes = UNKNOWN_KEY.slice(0, 20);
    const decoded = { type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlFlags: '92', ctrlNodeType: 2, ctrlSNR: 16, ctrlTag: 0xDEADBEEF, ctrlPubKey: keyBytes.slice(0, 16) };
    const rows = payloadRows(api.buildFieldTable({ raw_hex: '2e00' + '9210' + TAG + keyBytes, route_type: 2, payload_type: 11 }, decoded, [], []));
    const raw = rows.find(r => r.name === 'Raw');
    assert(raw, 'Raw row missing: ' + rowsSummary(rows));
    assert.strictEqual(raw.off, 16);
    assert.strictEqual(raw.value, keyBytes.slice(16));
  });

  test('detail: DISCOVER_RESP with 3 bytes beyond the 32B key keeps them at offset 40', () => {
    const decoded = { type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlFlags: '92', ctrlNodeType: 2, ctrlSNR: 16, ctrlTag: 0xDEADBEEF, ctrlPubKey: UNKNOWN_KEY };
    const rows = payloadRows(api.buildFieldTable({ raw_hex: '2e00' + '9210' + TAG + UNKNOWN_KEY + '010203', route_type: 2, payload_type: 11 }, decoded, [], []));
    const raw = rows.find(r => r.name === 'Raw');
    assert(raw, 'Raw row missing: ' + rowsSummary(rows));
    assert.strictEqual(raw.off, 40);
    assert.strictEqual(raw.value, '010203');
  });

  test('detail: fully decoded DISCOVER_RESP has no Raw row', () => {
    const decoded = { type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlFlags: '92', ctrlNodeType: 2, ctrlSNR: 16, ctrlTag: 0xDEADBEEF, ctrlPubKey: UNKNOWN_KEY };
    const rows = payloadRows(api.buildFieldTable({ raw_hex: '2e00' + '9210' + TAG + UNKNOWN_KEY, route_type: 2, payload_type: 11 }, decoded, [], []));
    assert(!rows.some(r => r.name === 'Raw'), rowsSummary(rows));
  });

  // --- filter bits outside ADV_TYPE 1..4 (AdvertDataHelpers.h:7 NONE=0, :12 FUTURE 5..15) ---
  test('preview: filter 0x24 shows the unknown bit next to Repeater', () => {
    const html = api.getDetailPreview({ type: 'CONTROL', ctrlSubtype: 'DISCOVER_REQ', ctrlFilter: 0x24 });
    assert(html.includes('filter=Repeater+0x20'), 'got: ' + html);
  });

  test('preview: filter 0x04 has no unknown-bit suffix', () => {
    const html = api.getDetailPreview({ type: 'CONTROL', ctrlSubtype: 'DISCOVER_REQ', ctrlFilter: 0x04 });
    assert(/filter=Repeater(\s|<)/.test(html), 'got: ' + html);
  });

  test('detail: filter 0x25 names Repeater and keeps unknown bits 0x21', () => {
    const decoded = { type: 'CONTROL', ctrlSubtype: 'DISCOVER_REQ', ctrlFlags: '80', ctrlFilter: 0x25, ctrlTag: 1 };
    const row = fieldRows(api.buildFieldTable(CTRL_PKT, decoded, [], [])).find(r => r.name === 'Type Filter (1B)');
    assert.strictEqual(row.value, '0x25');
    assert.strictEqual(row.desc, 'Requesting: Repeater +0x21');
  });

  // --- XSS: flags come from decoded_json ---
  test('detail: DISCOVER_REQ with non-hex flags stays escaped', () => {
    const decoded = { type: 'CONTROL', ctrlSubtype: 'DISCOVER_REQ', ctrlFlags: '<b>', ctrlFilter: 4, ctrlTag: 1 };
    const html = api.buildFieldTable(CTRL_PKT, decoded, [], []);
    assert(!html.includes('<b>'), 'got: ' + html);
  });
}

console.log('\n=== #1868 review: HopResolver.nodeForKey ambiguous 8-byte prefix ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;
  const A = '1122334455667788' + 'aa'.repeat(24);
  const B = '1122334455667788' + 'bb'.repeat(24);
  ctx.HopResolver.init([
    { public_key: A, name: 'Node A', role: 'repeater' },
    { public_key: B, name: 'Node B', role: 'repeater' },
  ]);

  test('nodeForKey: shared 8-byte prefix returns null', () => {
    assert.strictEqual(ctx.HopResolver.nodeForKey('1122334455667788'), null);
  });

  test('nodeForKey: full keys still resolve when their prefix is ambiguous', () => {
    assert.strictEqual(ctx.HopResolver.nodeForKey(A).name, 'Node A');
    assert.strictEqual(ctx.HopResolver.nodeForKey(B.toUpperCase()).name, 'Node B');
  });

  test('detail: ambiguous 8-byte prefix is not linked to either node', () => {
    const html = api.buildFieldTable(CTRL_PKT, { type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlPubKey: '1122334455667788' }, [], []);
    assert(!html.includes('#/nodes/'), 'got: ' + html);
    assert(!html.includes('Node A') && !html.includes('Node B'), 'got: ' + html);
  });
}

console.log('\n=== #1868 review: renderDetail loads the node index for CONTROL ===');
(async () => {
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;
  const nodeFetches = [];
  ctx.fetchAllNodes = (q) => { nodeFetches.push(q); return Promise.resolve({ nodes: [{ public_key: KNOWN_KEY, name: 'Known Repeater', role: 'repeater' }] }); };
  ctx.api = (path) => Promise.resolve(path === '/observers' ? { observers: [] } : {});
  const panel = { innerHTML: '', querySelectorAll: () => [], querySelector: () => null, addEventListener: () => {} };
  const decoded = { type: 'CONTROL', ctrlSubtype: 'DISCOVER_RESP', ctrlFlags: '92', ctrlNodeType: 2, ctrlSNR: 16, ctrlTag: 0xDEADBEEF, ctrlPubKey: KNOWN_KEY };
  const data = {
    packet: { id: 1, hash: 'h1', raw_hex: '2e00' + '9210efbeadde' + KNOWN_KEY, route_type: 2, payload_type: 11, decoded_json: JSON.stringify(decoded), path_json: '[]' },
    observations: [],
  };
  try {
    assert.strictEqual(typeof api.renderDetail, 'function', 'renderDetail must be exposed on _packetsTestAPI');
    assert.strictEqual(ctx.HopResolver.ready(), false, 'precondition: node index not loaded');
    await api.renderDetail(panel, data);
    assert(nodeFetches.length >= 1, 'renderDetail did not load the node index');
    assert(panel.innerHTML.includes('>Known Repeater</a>'), 'responder name not resolved in the field table');
    passed++; console.log('  ✅ renderDetail: zero-hop DISCOVER_RESP loads the node index and names the responder');
  } catch (e) {
    failed++; console.log('  ❌ renderDetail: zero-hop DISCOVER_RESP loads the node index and names the responder: ' + e.message);
  }
  finish();
})();

function finish() {
  console.log('');
  if (failed > 0) {
    console.error(`❌ ${failed} test(s) failed, ${passed} passed`);
    process.exit(1);
  }
  console.log(`✅ All ${passed} tests passed`);
}
