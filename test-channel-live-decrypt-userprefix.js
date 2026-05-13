/**
 * Regression test: live PSK decrypt for user-added channels (#1029 follow-up).
 *
 * PR #1030 added decryptLivePSKBatch() which rewrites encrypted GRP_TXT
 * WS packets in place when a stored PSK key matches. It sets
 *   payload.channel = dec.channelName  (e.g. "medusa")
 * but user-added channels are stored in channels[] with hash:
 *   "user:medusa"
 * (and selectedHash is also "user:medusa" when viewing).
 *
 * Symptoms in production:
 *  - selectedHash === "user:medusa" but processWSBatch compares
 *    `channelName === selectedHash` ("medusa" !== "user:medusa") so a live
 *    packet for the open channel is NEVER appended to the message list.
 *  - channels.find(c => c.hash === channelName) misses the user channel and
 *    a duplicate plain entry "medusa" is pushed into the sidebar; the real
 *    user-added channel's lastMessage / messageCount / lastActivityMs never
 *    update.
 *  - The unread bumper guards with `chName === prior` (raw name vs prefixed
 *    selectedHash), so an unread badge is added even when the user IS
 *    actively viewing that channel.
 *
 * Fix: have the live decrypt rewrite annotate the payload with the
 * canonical channel hash that channels[] / selectedHash use. A simple,
 * non-breaking shape: keep payload.channel = name (so the rest of
 * processWSBatch keeps working for non-user channels), AND also set
 * payload.channelKey = "user:" + name when a user-added channel exists for
 * that name. processWSBatch then uses channelKey when present for the
 * lookup + selectedHash comparison.
 *
 * This test loads the real channels.js in a vm sandbox, primes a
 * user-added channel, drives an encrypted GRP_TXT through the WS handler
 * and asserts:
 *   1. the open channel's message list grows by 1 (text is decrypted-locally
 *      and visible in the messages array)
 *   2. the user-added channel's messageCount / lastMessage update
 *   3. NO duplicate plain "medusa" entry is added to channels[]
 *   4. unread is NOT bumped on the channel currently being viewed
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const path = require('path');
const { createCipheriv, createHmac, createHash, webcrypto } = require('crypto');

let passed = 0;
let failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

function buildEncryptedGrpTxt(channelName, sender, message) {
  const key = createHash('sha256').update(channelName).digest().slice(0, 16);
  const channelHash = createHash('sha256').update(key).digest()[0];
  const text = `${sender}: ${message}`;
  const inner = 5 + Buffer.byteLength(text, 'utf8') + 1;
  const padded = Math.ceil(inner / 16) * 16;
  const pt = Buffer.alloc(padded);
  pt.writeUInt32LE(Math.floor(Date.now() / 1000), 0);
  pt[4] = 0;
  pt.write(text, 5, 'utf8');
  const cipher = createCipheriv('aes-128-ecb', key, null);
  cipher.setAutoPadding(false);
  const ct = Buffer.concat([cipher.update(pt), cipher.final()]);
  const secret = Buffer.concat([key, Buffer.alloc(16)]);
  const mac = createHmac('sha256', secret).update(ct).digest().slice(0, 2);
  return {
    payload: {
      type: 'GRP_TXT',
      channelHash,
      channelHashHex: channelHash.toString(16).padStart(2, '0'),
      mac: mac.toString('hex'),
      encryptedData: ct.toString('hex'),
      decryptionStatus: 'no_key',
    },
    keyHex: key.toString('hex'),
  };
}

function makeBrowserLikeSandbox() {
  const storage = {};
  const elements = {};
  function makeFakeEl(id) {
    return {
      id: id || '', innerHTML: '', textContent: '', value: '', scrollTop: 0,
      scrollHeight: 0,
      style: {}, dataset: {},
      classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
      addEventListener() {}, removeEventListener() {},
      querySelector() { return makeFakeEl(); },
      querySelectorAll() { return []; },
      getAttribute() { return null; }, setAttribute() {},
      getBoundingClientRect() { return { width: 240, height: 0, top: 0, left: 0, right: 0, bottom: 0 }; },
      appendChild() {}, removeChild() {},
      focus() {}, blur() {},
      checked: false,
    };
  }
  function el(id) {
    if (!elements[id]) elements[id] = makeFakeEl(id);
    return elements[id];
  }
  const ctx = {
    window: {},
    document: {
      readyState: 'complete',
      documentElement: { getAttribute: () => null, setAttribute() {}, classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } } },
      createElement: () => ({ id: '', textContent: '', innerHTML: '', style: {}, classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } }, addEventListener() {}, appendChild() {}, querySelector() { return null; }, querySelectorAll() { return []; } }),
      head: { appendChild() {} },
      body: { appendChild() {} },
      getElementById: el,
      addEventListener() {}, removeEventListener() {},
      querySelector: () => null,
      querySelectorAll: () => [],
    },
    console,
    Date, Math, Array, Object, String, Number, JSON, RegExp, Error, TypeError, Set, Map, Promise,
    parseInt, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent,
    setTimeout: (fn) => { Promise.resolve().then(fn); return 0; },
    clearTimeout: () => {},
    setInterval: () => 0,
    clearInterval: () => {},
    fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    localStorage: {
      getItem: (k) => Object.prototype.hasOwnProperty.call(storage, k) ? storage[k] : null,
      setItem: (k, v) => { storage[k] = String(v); },
      removeItem: (k) => { delete storage[k]; },
    },
    location: { hash: '' },
    history: { replaceState() {}, pushState() {} },
    crypto: webcrypto,
    TextEncoder, TextDecoder,
    Uint8Array, Uint16Array, Uint32Array, Int8Array, Int16Array, Int32Array, ArrayBuffer,
    URLSearchParams,
    CustomEvent: class CustomEvent {},
    MutationObserver: class MutationObserver { observe() {} disconnect() {} },
    requestAnimationFrame: (cb) => setTimeout(cb, 0),
    matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
    addEventListener() {}, dispatchEvent() {},
    getHashParams: () => new URLSearchParams(),
  };
  ctx.self = ctx;
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  return ctx;
}

function loadInCtx(ctx, file) {
  const src = fs.readFileSync(path.join(__dirname, file), 'utf8');
  vm.runInContext(src, ctx, { filename: file });
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
}

async function run() {
  console.log('\n=== Live PSK decrypt: user-added channel (user:* prefix) routing ===');

  const ctx = makeBrowserLikeSandbox();
  ctx.window.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  ctx.window.addEventListener = () => {};
  ctx.btoa = (s) => Buffer.from(String(s), 'binary').toString('base64');
  ctx.atob = (s) => Buffer.from(String(s), 'base64').toString('binary');

  // App.js stubs: provide debouncedOnWS / onWS / offWS / api / debounce /
  // invalidateApiCache / registerPage so channels.js loads cleanly.
  let wsListeners = [];
  ctx.onWS = (fn) => { wsListeners.push(fn); };
  ctx.offWS = (fn) => { wsListeners = wsListeners.filter(f => f !== fn); };
  ctx.debouncedOnWS = function (fn) {
    function handler(msg) { fn([msg]); }
    wsListeners.push(handler);
    return handler;
  };
  ctx.debounce = (fn) => fn;
  ctx.api = () => Promise.resolve({ channels: [], observers: [] });
  ctx.invalidateApiCache = () => {};
  ctx.CLIENT_TTL = { channels: 60000, observers: 600000 };
  ctx.escapeHtml = (s) => String(s == null ? '' : s);
  ctx.truncate = (s, n) => { s = String(s || ''); return s.length > n ? s.slice(0, n) : s; };
  ctx.formatHashHex = (h) => String(h);
  ctx.formatSecondsAgo = () => '';
  ctx.payloadTypeName = () => 'GRP_TXT';
  ctx.RegionFilter = {
    init() {},
    onChange(fn) { return () => {}; },
    offChange() {},
    getRegionParam() { return ''; },
    getSelected() { return null; },
  };
  ctx.ChannelColors = { get() { return null; }, remove() {} };
  ctx.ChannelColorPicker = { open() {} };
  ctx.normalizeObserverNameKey = (s) => String(s || '').toLowerCase();
  let pageMod = null;
  ctx.registerPage = (name, mod) => { if (name === 'channels') pageMod = mod; };

  // Load AES + ChannelDecrypt + channels.js
  loadInCtx(ctx, 'public/vendor/aes-ecb.js');
  loadInCtx(ctx, 'public/channel-decrypt.js');
  loadInCtx(ctx, 'public/channels.js');

  const CD = ctx.window.ChannelDecrypt;
  assert(typeof CD.tryDecryptLive === 'function', 'ChannelDecrypt.tryDecryptLive available');

  const channelName = 'medusa';
  const fixture = buildEncryptedGrpTxt(channelName, 'Alice', 'hello darkness');
  CD.storeKey(channelName, fixture.keyHex);

  // Initialize the channels page so wsHandler is wired up
  const appEl = ctx.document.getElementById('page');
  appEl.innerHTML = '';
  await pageMod.init(appEl, null);
  // pump microtasks
  await new Promise((r) => setTimeout(r, 0));

  ctx.window._channelsSetStateForTest({
    channels: [{
      hash: 'user:' + channelName,
      name: channelName,
      messageCount: 0,
      lastActivityMs: 0,
      lastSender: '',
      lastMessage: 'Encrypted — click to decrypt',
      encrypted: true,
      userAdded: true,
    }],
    messages: [],
    selectedHash: 'user:' + channelName,
  });

  // Drive the WS path — same shape the Go server broadcasts
  const wsMsg = {
    type: 'packet',
    data: {
      id: 12345,
      hash: 'deadbeef',
      observer_name: 'TestObserver',
      packet: { observer_name: 'TestObserver' },
      decoded: {
        header: { payloadTypeName: 'GRP_TXT' },
        payload: fixture.payload,
      },
    },
  };
  for (const fn of wsListeners) fn(wsMsg);
  // Allow async decryptLivePSKBatch + setTimeout chain to settle
  for (let i = 0; i < 20; i++) await new Promise((r) => setTimeout(r, 0));

  const state = ctx.window._channelsGetStateForTest();

  // (1) Message list for the open channel grew
  assert(state.messages.length === 1,
    'open user-added channel receives the live-decrypted message (got ' + state.messages.length + ')');
  if (state.messages[0]) {
    assert(state.messages[0].text === 'hello darkness',
      'decrypted text is rendered (got ' + JSON.stringify(state.messages[0].text) + ')');
    assert(state.messages[0].sender === 'Alice',
      'decrypted sender is rendered (got ' + JSON.stringify(state.messages[0].sender) + ')');
  }

  // (2) The user-added channel's metadata updated
  const userCh = state.channels.find((c) => c.hash === 'user:' + channelName);
  assert(userCh && userCh.messageCount === 1,
    'user-added channel messageCount incremented (got ' + (userCh && userCh.messageCount) + ')');
  assert(userCh && userCh.lastMessage && userCh.lastMessage.indexOf('hello') !== -1,
    'user-added channel lastMessage updated (got ' + (userCh && userCh.lastMessage) + ')');

  // (3) No duplicate plain "medusa" entry was created in the sidebar
  const dupes = state.channels.filter((c) => c.hash === channelName);
  assert(dupes.length === 0,
    'no duplicate non-prefixed channel entry created (got ' + dupes.length + ')');
  assert(state.channels.length === 1,
    'sidebar still has exactly the one user-added channel (got ' + state.channels.length + ')');

  // (4) Unread NOT bumped on the channel actively being viewed
  assert(!userCh || !userCh.unread,
    'unread NOT bumped on the actively-viewed channel (got ' + (userCh && userCh.unread) + ')');

  console.log('\n=== Results ===');
  console.log('Passed: ' + passed + ', Failed: ' + failed);
  process.exit(failed > 0 ? 1 : 0);
}

run().catch((e) => { console.error(e); process.exit(1); });
