/**
 * Unit tests for the live (WebSocket) channel filter on the packets page.
 *
 * The initial load honours ?channel= server-side (transmissions.channel_hash:
 * channel name when decrypted, else "enc_<HH>"). Live WS packets don't carry
 * that column, so the WS filter in packets.js used to ignore the channel
 * selection and every new packet leaked into a channel-filtered table.
 *
 * packetMatchesChannel() derives the same value from decoded_json; it is pure
 * and exported as window._packetsMatchesChannelForTest.
 *
 * Covers: no filter, decrypted name match/mismatch, non-GRP_TXT rejection,
 * encrypted "enc_<HH>" match (case-normalised), channel_hash passthrough,
 * already-parsed decoded_json, malformed JSON, and that the WS filter
 * actually calls the helper.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

// Same tolerant browser sandbox as test-packets-local-channels.js: the helper
// is exported before any DOM-dependent code runs.
const noop = () => {};
const fakeEl = {
  addEventListener: noop, removeEventListener: noop, appendChild: noop, remove: noop,
  querySelector: () => null, querySelectorAll: () => [], setAttribute: noop,
  getAttribute: () => null, classList: { add: noop, remove: noop, toggle: noop, contains: () => false },
  style: {}, dataset: {}, textContent: '', innerHTML: '',
};
const doc = {
  readyState: 'complete', createElement: () => ({ ...fakeEl }), head: fakeEl, body: fakeEl,
  getElementById: () => null, querySelector: () => null, querySelectorAll: () => [],
  addEventListener: noop, removeEventListener: noop,
};
const win = {
  addEventListener: noop, removeEventListener: noop,
  matchMedia: () => ({ matches: false, addEventListener: noop, removeEventListener: noop }),
  PayloadLabels: { SHORT_BY_ID: {} },
};
const ctx = {
  window: win, document: doc, console, Date, Math, JSON, Set, Map, Array, Object, Promise, Error,
  setTimeout, clearTimeout, setInterval, clearInterval,
  history: { replaceState: noop, pushState: noop },
  location: { hash: '', href: '', pathname: '/' },
  navigator: { userAgent: 'node' },
  localStorage: { getItem: () => null, setItem: noop, removeItem: noop },
  registerPage: noop,
  api: () => Promise.resolve({}),
  fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
};
ctx.self = ctx;
vm.createContext(ctx);
const src = fs.readFileSync('public/packets.js', 'utf8');
try {
  vm.runInContext(src, ctx);
} catch (e) {
  // Downstream init may throw on the stub DOM — the helper is already exported.
}

const match = win._packetsMatchesChannelForTest;
assert.strictEqual(typeof match, 'function', 'packetMatchesChannel must be exported for tests');

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log('  ✓ ' + name);
}

const grp = (decoded) => ({ payload_type: 5, decoded_json: JSON.stringify(decoded) });

console.log('packetMatchesChannel');

test('no channel filter accepts everything', () => {
  assert.strictEqual(match({ payload_type: 4, decoded_json: '{}' }, undefined), true);
  assert.strictEqual(match(grp({ channel: '#test' }), ''), true);
});

test('decrypted GRP_TXT matches its channel name', () => {
  assert.strictEqual(match(grp({ type: 'CHAN', channel: '#saarland', decryptionStatus: 'decrypted' }), '#saarland'), true);
});

test('decrypted GRP_TXT of another channel is rejected', () => {
  assert.strictEqual(match(grp({ type: 'CHAN', channel: '#test', decryptionStatus: 'decrypted' }), '#saarland'), false);
});

test('non-GRP_TXT packets are rejected while a channel filter is active', () => {
  assert.strictEqual(match({ payload_type: 4, decoded_json: JSON.stringify({ type: 'ADVERT' }) }, '#saarland'), false);
  assert.strictEqual(match({ payload_type: 2, decoded_json: '{}' }, 'enc_5F'), false);
  assert.strictEqual(match(null, '#saarland'), false);
});

test('encrypted GRP_TXT matches enc_<HH> (hex case-normalised)', () => {
  const p = grp({ type: 'GRP_TXT', channelHash: 95, channelHashHex: '5f', decryptionStatus: 'decryption_failed' });
  assert.strictEqual(match(p, 'enc_5F'), true);
  assert.strictEqual(match(p, 'enc_76'), false);
  assert.strictEqual(match(p, '#saarland'), false);
});

test('encrypted channel hash 00 still derives enc_00', () => {
  assert.strictEqual(match(grp({ channelHash: 0, channelHashHex: '00' }), 'enc_00'), true);
});

test('server-provided channel_hash takes precedence', () => {
  assert.strictEqual(match({ payload_type: 5, channel_hash: '#saarland', decoded_json: '{}' }, '#saarland'), true);
  assert.strictEqual(match({ payload_type: 5, channel_hash: '#test', decoded_json: JSON.stringify({ channel: '#saarland' }) }, '#saarland'), false);
});

test('already-parsed decoded_json object is accepted', () => {
  assert.strictEqual(match({ payload_type: 5, decoded_json: { channel: 'Public' } }, 'Public'), true);
});

test('malformed decoded_json does not throw and does not match', () => {
  assert.strictEqual(match({ payload_type: 5, decoded_json: '{not json' }, '#saarland'), false);
  assert.strictEqual(match({ payload_type: 5 }, '#saarland'), false);
});

test('WS live filter applies the channel filter', () => {
  const start = src.indexOf('Check if new packets pass current filters');
  assert.ok(start > 0, 'WS filter block not found');
  const block = src.slice(start, src.indexOf('if (!filtered.length) return;', start));
  assert.ok(/filters\.channel\s*&&\s*!packetMatchesChannel\(p,\s*filters\.channel\)/.test(block),
    'WS filter must reject packets that do not match filters.channel');
});

console.log(`\n${passed} passed`);
