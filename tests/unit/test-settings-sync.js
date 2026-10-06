/* Unit tests for public/settings-sync.js (settings sync, sub-project B).
 * The real file runs in a vm sandbox with a fake Storage (a real
 * prototype, so the module's Storage.prototype wrap is exercised), a fake
 * CSAuth backed by a fake server, and fake timers. Tests run one by one. */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

const ROOT = path.resolve(__dirname, '..', '..');
const SRC = fs.readFileSync(path.join(ROOT, 'public/settings-sync.js'), 'utf8');
const J = JSON.stringify;
// Objects made inside the vm have another Object.prototype; compare plain copies.
const plain = (x) => JSON.parse(JSON.stringify(x));
const settle = () => new Promise((r) => setImmediate(r));

const tests = [];
function test(name, fn) { tests.push({ name, fn }); }

// escapeHtml comes from the real app.js, not a copy.
function loadEscapeHtml() {
  const src = fs.readFileSync(path.join(ROOT, 'public/app.js'), 'utf8');
  const m = src.match(/function escapeHtml\(s\) \{[\s\S]*?\n\}/);
  assert(m, 'escapeHtml not found in app.js');
  return vm.runInNewContext('(' + m[0].replace(/^function escapeHtml/, 'function') + ')');
}

function fakeTimers() {
  let now = 0, seq = 0;
  const list = [];
  const api = {
    setTimeout(fn, ms) { list.push({ id: ++seq, at: now + (ms || 0), fn }); return seq; },
    clearTimeout(id) { const i = list.findIndex((t) => t.id === id); if (i >= 0) list.splice(i, 1); },
    setInterval(fn, ms) { list.push({ id: ++seq, at: now + ms, fn, every: ms }); return seq; },
    clearInterval(id) { api.clearTimeout(id); },
    async advance(ms) {
      const end = now + ms;
      for (;;) {
        await settle();
        list.sort((a, b) => a.at - b.at);
        const t = list[0];
        if (!t || t.at > end) break;
        now = t.at;
        if (t.every) t.at += t.every; else list.shift();
        t.fn();
      }
      now = end;
      await settle();
    },
    // Pending one-shot timers, as delays from now.
    delays() { return list.filter((t) => !t.every).map((t) => t.at - now).sort((a, b) => a - b); },
  };
  return api;
}

const ALLOW = [
  { key: 'meshcore-favorites', kind: 'set' },
  { key: 'meshcore-my-nodes', kind: 'set', id: 'pubkey' },
  { key: 'meshcore-time-window', kind: 'scalar' },
  { key: 'meshcore-theme', kind: 'scalar' },
  { key: 'cs-theme-overrides', kind: 'scalar' },
  { key: 'mc-dark-tile-provider', kind: 'scalar' },
  { key: 'mc-light-tile-provider', kind: 'scalar' },
];

// fakeServer keeps one account document with the semantics of
// cmd/server/settings_handlers.go. fail[METHOD] queues canned answers
// ('network' rejects the request).
function fakeServer() {
  const s = { rev: 0, doc: null, puts: [], gets: 0, deletes: 0, fail: { GET: [], PUT: [], DELETE: [] } };
  s.handle = (method, p, body) => {
    if (method === 'PUT') s.puts.push(body);
    if (method === 'GET') s.gets++;
    if (s.fail[method].length) return s.fail[method].shift();
    if (method === 'GET') return { status: 200, data: { revision: s.rev, doc: s.doc, allowlist: ALLOW } };
    if (method === 'PUT') {
      if (body.baseRevision !== s.rev) return { status: 409, data: { revision: s.rev, doc: s.doc } };
      s.rev++;
      s.doc = body.doc;
      return { status: 200, data: { revision: s.rev } };
    }
    s.deletes++;
    s.rev = 0;
    s.doc = null;
    return { status: 200, data: { ok: true } };
  };
  return s;
}

const AUTO_IDS = ['syncStatus', 'syncNow', 'syncDelete', 'syncMsg'];

// makeEnv loads the real module. opts: local {key: raw}, server,
// user (default {id: 7}; null = logged out), enabled (default true), hash,
// customizerReady (default true: the customizer finished its init).
function makeEnv(opts) {
  opts = opts || {};
  function Storage() { this.m = new Map(); }
  Storage.prototype.getItem = function (k) { return this.m.has(k) ? this.m.get(k) : null; };
  Storage.prototype.setItem = function (k, v) { this.m.set(k, String(v)); };
  Storage.prototype.removeItem = function (k) { this.m.delete(k); };
  const originalSet = Storage.prototype.setItem;
  const ls = new Storage();
  Object.keys(opts.local || {}).forEach((k) => ls.m.set(k, opts.local[k]));
  const server = opts.server || fakeServer();
  const timers = fakeTimers();
  const enabled = opts.enabled !== false;
  let user = opts.user === undefined ? { id: 7 } : opts.user;
  let logoutHandler = null;
  const winListeners = {}, docListeners = {}, els = {};
  const toasts = [], events = [], warnings = [], errors = [];
  const mkEl = (id) => {
    const el = { id, innerHTML: '', textContent: '', handlers: {}, cls: {} };
    el.classList = { toggle(c, on) { el.cls[c] = !!on; } };
    el.addEventListener = (t, fn) => { el.handlers[t] = fn; };
    return el;
  };
  const auth = {
    isEnabled: () => enabled,
    user: () => user,
    ready: () => Promise.resolve(enabled ? user : null),
    request(method, p, body) {
      const r = server.handle(method, p, body === undefined ? undefined : JSON.parse(JSON.stringify(body)));
      if (r === 'network') return Promise.reject(new Error('offline'));
      return Promise.resolve({ ok: r.status < 400, status: r.status, data: r.data || {} });
    },
    notify(m) { toasts.push(m); },
    setLogoutHandler(fn) { logoutHandler = fn; },
  };
  const env = { navigations: 0, pipelines: 0 };
  const win = {
    localStorage: ls, Storage, CSAuth: auth, MC_USER_MGMT: enabled ? { enabled: true } : null,
    addEventListener(t, f) { (winListeners[t] = winListeners[t] || []).push(f); },
    dispatchEvent(e) { events.push(e); },
    navigate() { env.navigations++; },
    _customizerV2: { initDone: opts.customizerReady !== false, runPipeline() { env.pipelines++; } },
  };
  const doc = {
    visibilityState: 'visible',
    addEventListener(t, f) { docListeners[t] = f; },
    removeEventListener(t, f) { if (docListeners[t] === f) delete docListeners[t]; },
    getElementById(id) { return els[id] || (AUTO_IDS.indexOf(id) !== -1 ? (els[id] = mkEl(id)) : null); },
  };
  const ctx = {
    window: win, document: doc, location: { hash: opts.hash || '#/packets' },
    console: { warn: (m) => warnings.push(String(m)), error: (m) => errors.push(String(m)), log() {} },
    Promise,
    StorageEvent: function (type, init) { this.type = type; this.key = init.key; this.newValue = init.newValue; },
    escapeHtml: loadEscapeHtml(),
    setTimeout: timers.setTimeout, clearTimeout: timers.clearTimeout,
    setInterval: timers.setInterval, clearInterval: timers.clearInterval,
  };
  vm.createContext(ctx);
  vm.runInContext(SRC, ctx);
  Object.assign(env, {
    ls, server, timers, toasts, events, warnings, errors, els, doc, ctx, win, originalSet,
    api: win.CSSettingsSync, t: win.CSSettingsSync._test,
    get logoutHandler() { return logoutHandler; },
    login(u) { user = u; (winListeners['cs-auth-changed'] || []).forEach((f) => f({ detail: u })); return settle(); },
    logout() { user = null; (winListeners['cs-auth-changed'] || []).forEach((f) => f({ detail: null })); return settle(); },
    fireDoc(t) { if (docListeners[t]) docListeners[t](); return settle(); },
    el(id) { return els[id] || (els[id] = mkEl(id)); },
  });
  return env;
}

// ── mergeDocs ──
const M_ALLOW = [{ key: 'fav', kind: 'set' }, { key: 'nodes', kind: 'set', id: 'pubkey' }, { key: 'tw', kind: 'scalar' }];
const n = (pubkey, name) => ({ pubkey, name });
const MERGE_CASES = [
  // name, local, profile, base, keys, localChanges, differsFromProfile
  ['set: an item added here since the last sync is kept',
    { fav: J(['a', 'b']) }, { fav: J(['a']) }, { fav: J(['a']) }, { fav: J(['a', 'b']) }, [], true],
  ['set: an item removed here since the last sync is removed',
    { fav: J(['a']) }, { fav: J(['a', 'b']) }, { fav: J(['a', 'b']) }, { fav: J(['a']) }, [], true],
  ['set: an item added on another device arrives',
    { fav: J(['a']) }, { fav: J(['a', 'c']) }, { fav: J(['a']) }, { fav: J(['a', 'c']) }, ['fav'], false],
  ['set: an item removed on another device is not brought back',
    { fav: J(['a', 'b']) }, { fav: J(['a']) }, { fav: J(['a', 'b']) }, { fav: J(['a']) }, ['fav'], false],
  ['set: additions on both sides are both kept',
    { fav: J(['a', 'x']) }, { fav: J(['a', 'y']) }, { fav: J(['a']) }, { fav: J(['a', 'y', 'x']) }, ['fav'], true],
  ['set: without a baseline the lists merge by union',
    { fav: J(['x']) }, { fav: J(['y']) }, {}, { fav: J(['y', 'x']) }, ['fav'], true],
  ['set: same identity edited only here keeps the local version',
    { nodes: J([n('k1', 'new')]) }, { nodes: J([n('k1', 'old')]) }, { nodes: J([n('k1', 'old')]) }, { nodes: J([n('k1', 'new')]) }, [], true],
  ['set: same identity edited on both sides keeps the profile version',
    { nodes: J([n('k1', 'new')]) }, { nodes: J([n('k1', 'other')]) }, { nodes: J([n('k1', 'old')]) }, { nodes: J([n('k1', 'other')]) }, ['nodes'], false],
  ['set: same identity edited only in the profile arrives',
    { nodes: J([n('k1', 'old')]) }, { nodes: J([n('k1', 'other')]) }, { nodes: J([n('k1', 'old')]) }, { nodes: J([n('k1', 'other')]) }, ['nodes'], false],
  ['scalar: changed only here wins',
    { tw: '60' }, { tw: '15' }, { tw: '15' }, { tw: '60' }, [], true],
  ['scalar: changed in the profile wins',
    { tw: '15' }, { tw: '180' }, { tw: '15' }, { tw: '180' }, ['tw'], false],
  ['scalar: changed on both sides, the profile wins',
    { tw: '60' }, { tw: '180' }, { tw: '15' }, { tw: '180' }, ['tw'], false],
  ['scalar: removed here wins',
    {}, { tw: '15' }, { tw: '15' }, {}, [], true],
  ['scalar: removed in the profile is removed here',
    { tw: '15' }, {}, { tw: '15' }, {}, ['tw'], false],
  ['set: the same list spelled differently takes the profile spelling and does not differ from it',
    { fav: '["a", "b"]' }, { fav: '["a","b"]' }, { fav: '["a","b"]' }, { fav: '["a","b"]' }, ['fav'], false],
  ['set: an unparseable baseline counts as none, so lists merge by union',
    { fav: J(['a', 'x']) }, { fav: J(['a', 'y']) }, { fav: 'garbage' }, { fav: J(['a', 'y', 'x']) }, ['fav'], true],
  ['set: an item edited here but deleted on another device stays deleted',
    { nodes: J([n('k1', 'new')]) }, { nodes: J([]) }, { nodes: J([n('k1', 'old')]) }, { nodes: J([]) }, ['nodes'], false],
  ['set: duplicate items on this device dedupe by identity',
    { fav: J(['a', 'b', 'a']) }, { fav: J(['a']) }, { fav: J(['a']) }, { fav: J(['a', 'b']) }, ['fav'], true],
  ['set: objects without the id field fall back to JSON identity',
    { nodes: J([{ name: 'x' }]) }, { nodes: J([{ name: 'y' }]) }, { nodes: J([]) }, { nodes: J([{ name: 'y' }, { name: 'x' }]) }, ['nodes'], true],
  ['keys outside the allowlist are ignored',
    { other: '1', tw: '15' }, { other: '2', tw: '15' }, {}, { tw: '15' }, [], false],
];

MERGE_CASES.forEach(([name, l, p, b, keys, changes, differs]) => {
  test('merge ' + name, () => {
    const env = makeEnv({ enabled: false });
    const r = plain(env.t.mergeDocs(l, p, b, M_ALLOW));
    assert.deepStrictEqual(r.keys, keys);
    assert.deepStrictEqual(r.localChanges, changes);
    assert.strictEqual(r.differsFromProfile, differs);
  });
});

test('merge: a set value that is not a JSON list merges as a scalar, with a warning', () => {
  const env = makeEnv({ enabled: false });
  const r = plain(env.t.mergeDocs({ fav: 'not json' }, { fav: J(['a']) }, { fav: J(['a']) }, M_ALLOW));
  assert.deepStrictEqual(r.keys, { fav: 'not json' });
  assert.strictEqual(env.warnings.length, 1);
  assert(env.warnings[0].indexOf('fav') !== -1, env.warnings[0]);
});

// ── engine ──
const BASE = (user, keys, rev, hold) => ({ 'cs-settings-sync-base': J({ user, keys, hold: !!hold }), 'cs-settings-sync-rev': String(rev) });
const synced = (keys, rev) => Object.assign({}, keys, BASE(7, keys, rev));
const serverWith = (rev, keys) => { const s = fakeServer(); s.rev = rev; s.doc = rev ? { v: 1, keys } : null; return s; };

test('feature off: no request and no interception', async () => {
  const env = makeEnv({ enabled: false });
  await env.timers.advance(120000);
  assert.strictEqual(env.server.gets + env.server.puts.length, 0);
  assert.strictEqual(env.win.Storage.prototype.setItem, env.originalSet);
});

test('logged out: no request and no interception', async () => {
  const env = makeEnv({ user: null });
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(120000);
  assert.strictEqual(env.server.gets + env.server.puts.length, 0);
  assert.strictEqual(env.win.Storage.prototype.setItem, env.originalSet);
});

test('first login with an empty profile uploads this device (never channel keys)', async () => {
  const env = makeEnv({ user: null, local: { 'meshcore-favorites': J(['a']), 'meshcore-time-window': '60', corescope_channel_keys: '{"#x":"00"}', 'meshcore-api-key': 'k' } });
  await env.login({ id: 7 });
  assert.strictEqual(env.server.puts.length, 1);
  assert.deepStrictEqual(env.server.puts[0], { baseRevision: 0, doc: { v: 1, keys: { 'meshcore-favorites': J(['a']), 'meshcore-time-window': '60' } } });
  assert.deepStrictEqual(env.toasts, ['Your settings are now saved to your account.']);
  assert.strictEqual(env.ls.getItem('cs-settings-sync-rev'), '1');
  assert.strictEqual(JSON.parse(env.ls.getItem('cs-settings-sync-base')).user, 7);
});

test('first login with nothing to upload sends nothing', async () => {
  const env = makeEnv({ user: null });
  await env.login({ id: 7 });
  assert.strictEqual(env.server.puts.length, 0);
  assert.strictEqual(env.t.state.status, 'idle');
});

test('login on another device merges the profile, applies theme and customizer, re-renders', async () => {
  const server = serverWith(4, { 'meshcore-favorites': J(['a']), 'meshcore-theme': 'dark', 'cs-theme-overrides': '{"x":1}' });
  const env = makeEnv({ user: null, server, local: { 'meshcore-favorites': J(['b']) } });
  await env.login({ id: 7 });
  assert.strictEqual(env.ls.getItem('meshcore-favorites'), J(['a', 'b']));
  assert.strictEqual(env.ls.getItem('meshcore-theme'), 'dark');
  assert(env.events.some((e) => e.type === 'storage' && e.key === 'meshcore-theme' && e.newValue === 'dark'));
  assert.strictEqual(env.pipelines, 1);
  assert.strictEqual(env.navigations, 1);
  assert(env.toasts.indexOf('Settings updated from another device') !== -1);
  // b was only here: pushed on top of revision 4.
  assert.strictEqual(server.puts.length, 1);
  assert.strictEqual(server.puts[0].baseRevision, 4);
  assert.strictEqual(server.doc.keys['meshcore-favorites'], J(['a', 'b']));
});

test('remote tile providers reach the map through a storage event', async () => {
  const server = serverWith(2, { 'mc-dark-tile-provider': 'carto-dark', 'mc-light-tile-provider': 'osm' });
  const env = makeEnv({ server, local: synced({}, 1) });
  await env.timers.advance(0);
  assert(env.events.some((e) => e.type === 'storage' && e.key === 'mc-dark-tile-provider' && e.newValue === 'carto-dark'));
  assert(env.events.some((e) => e.type === 'storage' && e.key === 'mc-light-tile-provider' && e.newValue === 'osm'));
});

test('customizer not initialised yet: its pipeline is not run (its init reads the new values)', async () => {
  const server = serverWith(2, { 'cs-theme-overrides': '{"x":1}' });
  const env = makeEnv({ server, local: synced({}, 1), customizerReady: false });
  await env.timers.advance(0);
  assert.strictEqual(env.ls.getItem('cs-theme-overrides'), '{"x":1}');
  assert.strictEqual(env.pipelines, 0);
  assert.strictEqual(env.navigations, 1);
});

test('interception: allowlisted writes push once, 2 s after the last; other keys never', async () => {
  const keys = { 'meshcore-favorites': J(['a']) };
  const env = makeEnv({ server: serverWith(1, keys), local: synced(keys, 1) });
  await env.timers.advance(0);
  env.ls.setItem('meshcore-time-window', '60');
  env.ls.setItem('meshcore-time-window', '180');
  env.ls.setItem('corescope_channel_keys', '{"#x":"00"}');
  env.ls.setItem('meshcore-api-key', 'k');
  env.ls.setItem('panel-drag-packets', '1');
  await env.timers.advance(1999);
  assert.strictEqual(env.server.puts.length, 0);
  await env.timers.advance(1);
  assert.strictEqual(env.server.puts.length, 1);
  assert.deepStrictEqual(env.server.puts[0].doc.keys, { 'meshcore-favorites': J(['a']), 'meshcore-time-window': '180' });
  env.ls.removeItem('meshcore-time-window');
  await env.timers.advance(2000);
  assert.strictEqual(env.server.puts.length, 2);
  env.ls.setItem('corescope_channel_keys', '{}');
  await env.timers.advance(10000);
  assert.strictEqual(env.server.puts.length, 2);
});

test('writing the same value again does not push', async () => {
  const keys = { 'meshcore-theme': 'dark' };
  const env = makeEnv({ server: serverWith(1, keys), local: synced(keys, 1) });
  await env.timers.advance(0);
  env.ls.setItem('meshcore-theme', 'dark');
  env.ls.removeItem('meshcore-time-window');
  await env.timers.advance(10000);
  assert.strictEqual(env.server.puts.length, 0);
});

test('values applied from the account do not trigger a push', async () => {
  const env = makeEnv({ server: serverWith(2, { 'meshcore-time-window': '180' }), local: synced({ 'meshcore-time-window': '60' }, 1) });
  await env.timers.advance(10000);
  assert.strictEqual(env.ls.getItem('meshcore-time-window'), '180');
  assert.strictEqual(env.server.puts.length, 0);
});

test('409: merges the returned document and retries on top of it', async () => {
  const keys = { 'meshcore-favorites': J(['a']) };
  const server = serverWith(1, keys);
  const env = makeEnv({ server, local: synced(keys, 1) });
  await env.timers.advance(0);
  server.rev = 2;
  server.doc = { v: 1, keys: { 'meshcore-favorites': J(['a', 'c']) } }; // another device
  env.ls.setItem('meshcore-favorites', J(['a', 'b']));
  await env.timers.advance(2000);
  assert.strictEqual(server.puts.length, 2);
  assert.strictEqual(server.puts[1].baseRevision, 2);
  assert.strictEqual(server.doc.keys['meshcore-favorites'], J(['a', 'c', 'b']));
  assert.strictEqual(env.ls.getItem('meshcore-favorites'), J(['a', 'c', 'b']));
});

test('409 more than three times falls back to the retry backoff', async () => {
  const keys = { 'meshcore-favorites': J(['a']) };
  const server = serverWith(1, keys);
  const env = makeEnv({ server, local: synced(keys, 1) });
  await env.timers.advance(0);
  const conflict = { status: 409, data: { revision: 1, doc: { v: 1, keys } } };
  server.fail.PUT = [conflict, conflict, conflict, conflict];
  env.ls.setItem('meshcore-favorites', J(['a', 'b']));
  await env.timers.advance(2000);
  assert.strictEqual(server.puts.length, 4);
  assert.strictEqual(env.t.state.status, 'retrying');
  assert.deepStrictEqual(env.timers.delays(), [2000]);
});

test('network errors, 5xx and 429 back off from 2 s doubling to 5 min; success resets', async () => {
  const keys = { 'meshcore-favorites': J(['a']) };
  const server = serverWith(1, keys);
  const env = makeEnv({ server, local: synced(keys, 1) });
  env.doc.visibilityState = 'hidden'; // no minute pulls in between
  await env.timers.advance(0);
  const expected = [2000, 4000, 8000, 16000, 32000, 64000, 128000, 256000, 300000];
  server.fail.PUT = expected.map((_, i) => ['network', { status: 503, data: {} }, { status: 429, data: {} }][i % 3]);
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(2000);
  for (const d of expected) {
    assert.deepStrictEqual(env.timers.delays(), [d]);
    assert.strictEqual(env.t.state.status, 'retrying');
    await env.timers.advance(d);
  }
  assert.strictEqual(env.t.state.status, 'ok');
  assert.strictEqual(env.t.state.backoff, 2000);
  assert.strictEqual(server.doc.keys['meshcore-time-window'], '60');
});

test('tab focus pulls; the minute pull runs only while visible', async () => {
  const env = makeEnv({ server: serverWith(1, {}), local: synced({}, 1) });
  await env.timers.advance(0);
  const g0 = env.server.gets;
  await env.fireDoc('visibilitychange');
  assert.strictEqual(env.server.gets, g0 + 1);
  await env.timers.advance(60000);
  assert.strictEqual(env.server.gets, g0 + 2);
  env.doc.visibilityState = 'hidden';
  await env.timers.advance(180000);
  assert.strictEqual(env.server.gets, g0 + 2);
});

test('413 stops pushing and names the largest keys; Sync now tries again', async () => {
  const server = serverWith(1, {});
  const env = makeEnv({ server, local: synced({}, 1) });
  await env.timers.advance(0);
  server.fail.PUT = [{ status: 413, data: { error: 'too large' } }];
  env.ls.setItem('cs-theme-overrides', 'x'.repeat(100));
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(2000);
  assert.strictEqual(env.t.state.status, 'too-large');
  assert.strictEqual(env.t.state.tooLarge[0], 'cs-theme-overrides');
  env.ls.setItem('meshcore-time-window', '15');
  await env.timers.advance(10000);
  assert.strictEqual(server.puts.length, 1);
  await env.api.syncNow();
  await settle();
  assert.strictEqual(server.puts.length, 2);
  assert.strictEqual(env.t.state.status, 'ok');
});

test('400 stops pushing until reload and logs the reason', async () => {
  const server = serverWith(1, {});
  const env = makeEnv({ server, local: synced({}, 1) });
  await env.timers.advance(0);
  server.fail.PUT = [{ status: 400, data: { error: 'key "x" is not a synced setting' } }];
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(2000);
  assert.strictEqual(env.t.state.status, 'rejected');
  assert.strictEqual(env.errors.length, 1);
  env.ls.setItem('meshcore-time-window', '15');
  await env.api.syncNow();
  await env.timers.advance(10000);
  assert.strictEqual(server.puts.length, 1);
});

test('account copy deleted elsewhere: values stay, no upload until the next change', async () => {
  const keys = { 'meshcore-favorites': J(['a']) };
  const env = makeEnv({ server: serverWith(0, null), local: synced(keys, 3) });
  await env.timers.advance(60000);
  assert.strictEqual(env.server.puts.length, 0);
  assert.strictEqual(env.ls.getItem('meshcore-favorites'), J(['a']));
  assert.strictEqual(JSON.parse(env.ls.getItem('cs-settings-sync-base')).hold, true);
  assert.strictEqual(env.ls.getItem('cs-settings-sync-rev'), '0');
  env.ls.setItem('meshcore-favorites', J(['a', 'b']));
  await env.timers.advance(2000);
  assert.strictEqual(env.server.puts.length, 1);
  assert.deepStrictEqual(env.server.puts[0], { baseRevision: 0, doc: { v: 1, keys: { 'meshcore-favorites': J(['a', 'b']) } } });
});

test("another user's baseline counts as none: nothing is removed", async () => {
  const local = Object.assign({ 'meshcore-favorites': J(['a', 'b']) }, BASE(99, { 'meshcore-favorites': J(['a', 'b', 'c']) }, 5));
  const env = makeEnv({ server: serverWith(2, { 'meshcore-favorites': J(['c']) }), local });
  await env.timers.advance(0);
  assert.strictEqual(env.ls.getItem('meshcore-favorites'), J(['c', 'a', 'b']));
});

test('mid-edit: on an account page or with the geofilter editor open, no re-render', async () => {
  const env = makeEnv({ hash: '#/account', server: serverWith(2, { 'meshcore-time-window': '180' }), local: synced({ 'meshcore-time-window': '60' }, 1) });
  await env.timers.advance(0);
  assert.strictEqual(env.ls.getItem('meshcore-time-window'), '180');
  assert.strictEqual(env.navigations, 0);
  assert(env.toasts.indexOf('Settings updated from another device') !== -1);
  env.ctx.location.hash = '#/packets';
  assert.strictEqual(env.t.midEdit(), false);
  env.el('cv2-gf-modal-overlay');
  assert.strictEqual(env.t.midEdit(), true);
  env.ctx.location.hash = '#/accounts-other';
  delete env.els['cv2-gf-modal-overlay'];
  assert.strictEqual(env.t.midEdit(), false);
});

test('logout makes the module inert: wrap removed, no timers, no requests', async () => {
  const env = makeEnv({ server: serverWith(1, {}), local: synced({}, 1) });
  await env.timers.advance(0);
  assert.notStrictEqual(env.win.Storage.prototype.setItem, env.originalSet);
  await env.logout();
  assert.strictEqual(env.win.Storage.prototype.setItem, env.originalSet);
  const before = env.server.gets + env.server.puts.length;
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(180000);
  assert.strictEqual(env.server.gets + env.server.puts.length, before);
  assert.deepStrictEqual(env.timers.delays(), []);
});

(async () => {
  let passed = 0, failed = 0;
  for (const t of tests) {
    try { await t.fn(); passed++; console.log('  ok   ' + t.name); }
    catch (e) { failed++; console.log('  FAIL ' + t.name + ': ' + (e && e.stack || e)); }
  }
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed ? 1 : 0);
})();
