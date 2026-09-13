/* #1851: each channel message shows the region scope it was sent with.
 *
 * scope_name has three states (transmissions.scope_name): null = the packet
 * carried no transport code, '' = it carried one that no configured region key
 * matched, '#name' = matched region. A message reaches the Channels view by
 * three routes (REST /api/channels/{hash}/messages, the WebSocket broadcast,
 * and client-side decryption of /api/packets rows), so each route must keep
 * the field, and the render must keep the '' state apart from null.
 *
 * Loads the real public/channels.js in a vm sandbox and reads the HTML it
 * writes into #chMessages.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
const pending = [];
function test(name, fn) {
  const done = (err) => {
    if (err) { failed++; console.log(`  ❌ ${name}: ${err.message}`); }
    else { passed++; console.log(`  ✅ ${name}`); }
  };
  try {
    const out = fn();
    if (out && typeof out.then === 'function') { pending.push(out.then(() => done(), done)); return; }
    done();
  } catch (e) { done(e); }
}

function makeChannelsSandbox(apiImpl) {
  const dom = {};
  function makeEl(id) {
    if (dom[id]) return dom[id];
    dom[id] = {
      id, innerHTML: '', textContent: '', value: '',
      scrollTop: 0, scrollHeight: 100, clientHeight: 80,
      style: {}, dataset: {},
      classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
      addEventListener() {}, removeEventListener() {},
      querySelector() { return null; }, querySelectorAll() { return []; },
      getBoundingClientRect() { return { left: 0, bottom: 0, width: 0 }; },
      setAttribute() {}, removeAttribute() {}, focus() {},
    };
    return dom[id];
  }
  const headerText = { textContent: '' };
  makeEl('chHeader').querySelector = (sel) => (sel === '.ch-header-text' ? headerText : null);
  ['chMessages', 'chList', 'chScrollBtn', 'chAriaLive', 'chBackBtn', 'chRegionFilter'].forEach(makeEl);
  const layout = { classList: { add() {}, remove() {}, contains() { return false; } } };
  const appEl = {
    innerHTML: '',
    querySelector(sel) { return sel === '.ch-layout' ? layout : makeEl(sel); },
    addEventListener() {},
  };
  const storage = {};
  const ctx = {
    window: { addEventListener() {}, dispatchEvent() {} },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '' }),
      head: { appendChild() {} },
      body: { appendChild() {}, removeChild() {}, contains() { return false; } },
      documentElement: { getAttribute: () => null, setAttribute() {} },
      getElementById: makeEl,
      querySelector: (sel) => (sel === '.ch-layout' ? layout : null),
      querySelectorAll: () => [],
      addEventListener() {}, removeEventListener() {},
    },
    console, Date, Math, Array, Object, String, Number, JSON, RegExp, Error, TypeError,
    Map, Set, Promise, URLSearchParams, Infinity, parseInt, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent,
    setTimeout: () => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {},
    performance: { now: () => Date.now() },
    localStorage: {
      getItem: (k) => (k in storage ? storage[k] : null),
      setItem: (k, v) => { storage[k] = String(v); },
      removeItem: (k) => { delete storage[k]; },
    },
    location: { hash: '' },
    getHashParams: () => new URLSearchParams(''),
    history: { replaceState() {} },
    matchMedia: () => ({ matches: false }),
    MutationObserver: function () { this.observe = () => {}; this.disconnect = () => {}; },
    RegionFilter: { init() {}, onChange() { return () => {}; }, offChange() {}, getRegionParam() { return ''; } },
    debouncedOnWS: (fn) => fn, onWS() {}, offWS() {},
    api: apiImpl,
    CLIENT_TTL: { observers: 120000, channels: 15000, channelMessages: 10000, nodeDetail: 10000 },
    ROLE_EMOJI: {}, ROLE_LABELS: {},
    timeAgo: () => '1m ago',
    registerPage: (name, handlers) => { ctx._pageHandlers = handlers; },
    btoa: (s) => Buffer.from(String(s), 'utf8').toString('base64'),
    atob: (s) => Buffer.from(String(s), 'base64').toString('utf8'),
    crypto: { subtle: require('crypto').webcrypto.subtle },
    TextEncoder, TextDecoder, Uint8Array,
  };
  ctx.window.matchMedia = ctx.matchMedia;
  vm.createContext(ctx);
  for (const file of ['public/channel-decrypt.js', 'public/channels.js']) {
    vm.runInContext(fs.readFileSync(file, 'utf8'), ctx);
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  }
  ctx._pageHandlers.init(appEl);
  return { ctx, dom };
}

function listApi(extra) {
  return (path) => {
    if (path.indexOf('/observers') === 0) return Promise.resolve({ observers: [] });
    if (path.indexOf('/channels') === 0 && path.indexOf('/messages') === -1) {
      return Promise.resolve({ channels: [{ hash: 'general', name: 'general', messageCount: 4, lastActivity: null }] });
    }
    return extra(path);
  };
}

// Splits the rendered message list into one HTML chunk per message so each
// assertion is about a specific message, not about the page as a whole.
function messageChunks(html) {
  return html.split('<div class="ch-msg ch-message">').slice(1);
}

const SCOPE_CHIP = /<span class="sa-chip sa-chip-declared ch-msg-scope"[^>]*>([^<]*)<\/span>/;
const UNKNOWN_CHIP = /<span class="sa-chip sa-chip-unmatched ch-msg-scope"[^>]*>unknown scope<\/span>/;

console.log('\n=== #1851: channel message region scope ===');

test('REST messages render a scope chip per state: name, unknown, none', async () => {
  const { ctx, dom } = makeChannelsSandbox(listApi((path) => {
    if (path.indexOf('/channels/general/messages') === 0) {
      return Promise.resolve({ messages: [
        { sender: 'Alice', text: 'matched', timestamp: '2026-09-01T10:00:00Z', packetHash: 'h1', scope_name: '#belgium' },
        { sender: 'Bob', text: 'unmatched', timestamp: '2026-09-01T10:01:00Z', packetHash: 'h2', scope_name: '' },
        { sender: 'Carol', text: 'plain flood', timestamp: '2026-09-01T10:02:00Z', packetHash: 'h3', scope_name: null },
        { sender: 'Dave', text: 'older server', timestamp: '2026-09-01T10:03:00Z', packetHash: 'h4' },
      ] });
    }
    return Promise.resolve({});
  }));
  for (let i = 0; i < 10; i++) await Promise.resolve();
  await ctx.window._channelsSelectChannelForTest('general');
  const chunks = messageChunks(dom.chMessages.innerHTML);
  assert.strictEqual(chunks.length, 4, 'expected 4 rendered messages');

  const named = chunks[0].match(SCOPE_CHIP);
  assert.ok(named, 'matched-region message must render a scope chip');
  assert.strictEqual(named[1], '#belgium');
  assert.ok(!UNKNOWN_CHIP.test(chunks[0]), 'matched-region message must not say unknown');

  assert.ok(UNKNOWN_CHIP.test(chunks[1]), "scope_name '' must render the unknown-scope chip");
  assert.ok(!SCOPE_CHIP.test(chunks[1]), "scope_name '' must not render a named chip");

  assert.ok(!/ch-msg-scope/.test(chunks[2]), 'scope_name null must render no chip');
  assert.ok(!/ch-msg-scope/.test(chunks[3]), 'a message without scope_name must render no chip');
});

test('scope name is escaped before it reaches innerHTML', async () => {
  const { ctx, dom } = makeChannelsSandbox(listApi((path) => {
    if (path.indexOf('/channels/general/messages') === 0) {
      return Promise.resolve({ messages: [
        { sender: 'Eve', text: 'x', timestamp: '2026-09-01T10:00:00Z', packetHash: 'h1', scope_name: '<img src=x onerror=alert(1)>' },
      ] });
    }
    return Promise.resolve({});
  }));
  for (let i = 0; i < 10; i++) await Promise.resolve();
  await ctx.window._channelsSelectChannelForTest('general');
  const html = dom.chMessages.innerHTML;
  assert.ok(!html.includes('<img'), 'raw markup from scope_name must not be rendered');
  assert.ok(html.includes('&lt;img src=x onerror=alert(1)&gt;'), 'scope_name must be HTML-escaped');
});

test('WebSocket-appended message keeps scope_name, including the empty state', () => {
  const { ctx, dom } = makeChannelsSandbox(listApi(() => Promise.resolve({ messages: [] })));
  ctx.window._channelsSetStateForTest({
    selectedHash: 'general',
    channels: [{ hash: 'general', name: 'general', messageCount: 0, lastActivityMs: 0 }],
    messages: [],
  });
  const packet = (hash, text, scope) => ({
    type: 'packet',
    data: {
      hash, scope_name: scope,
      decoded: { header: { payloadTypeName: 'GRP_TXT' }, payload: { channel: 'general', text } },
      packet: { hash, observer_name: 'Obs', scope_name: scope },
    },
  });
  ctx.window._channelsProcessWSBatchForTest([
    packet('w1', 'Alice: live matched', '#belgium'),
    packet('w2', 'Bob: live unmatched', ''),
    packet('w3', 'Carol: live plain', null),
  ], null);
  const state = ctx.window._channelsGetStateForTest();
  assert.deepStrictEqual(Array.from(state.messages, (m) => m.scope_name), ['#belgium', '', null]);
  const chunks = messageChunks(dom.chMessages.innerHTML);
  assert.strictEqual(chunks.length, 3, 'expected 3 rendered messages');
  assert.strictEqual((chunks[0].match(SCOPE_CHIP) || [])[1], '#belgium');
  assert.ok(UNKNOWN_CHIP.test(chunks[1]), 'live message with scope_name "" must render the unknown chip');
  assert.ok(!/ch-msg-scope/.test(chunks[2]), 'live message with scope_name null must render no chip');
});

test('client-side decrypted channel keeps scope_name from the /api/packets row', async () => {
  const { ctx, dom } = makeChannelsSandbox(listApi((path) => {
    if (path.indexOf('/packets?') === 0) {
      return Promise.resolve({ packets: [
        { id: 1, hash: 'd1', first_seen: '2026-09-01T10:00:00Z', scope_name: '#belgium',
          decoded_json: JSON.stringify({ type: 'CHAN', channel: '#priv', sender: 'Alice', text: 'Alice: decrypted' }) },
        { id: 2, hash: 'd2', first_seen: '2026-09-01T10:01:00Z', scope_name: '',
          decoded_json: JSON.stringify({ type: 'CHAN', channel: '#priv', sender: 'Bob', text: 'Bob: decrypted' }) },
      ] });
    }
    return Promise.resolve({});
  }));
  for (let i = 0; i < 10; i++) await Promise.resolve();
  await ctx.window._channelsSelectChannelForTest('user:#priv', {
    userKey: '00112233445566778899aabbccddeeff', channelHashByte: 1, channelName: '#priv',
  });
  const state = ctx.window._channelsGetStateForTest();
  assert.deepStrictEqual(Array.from(state.messages, (m) => m.scope_name), ['#belgium', '']);
  const chunks = messageChunks(dom.chMessages.innerHTML);
  assert.strictEqual((chunks[0].match(SCOPE_CHIP) || [])[1], '#belgium');
  assert.ok(UNKNOWN_CHIP.test(chunks[1]), 'decrypted message with scope_name "" must render the unknown chip');
});

Promise.all(pending).then(() => {
  console.log(`\n${passed} passed, ${failed} failed`);
  if (failed > 0) process.exit(1);
});
