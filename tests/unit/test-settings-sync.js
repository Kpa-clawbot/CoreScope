/* Unit tests for public/settings-sync.js (settings sync, sub-project B).
 * The real file runs in a vm sandbox with a fake Storage (a real
 * prototype, so the module's Storage.prototype wrap is exercised), a fake
 * CSAuth backed by a fake server, and fake timers. Tests run one by one. */
/* global setImmediate */
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

// CSAuth.say and CSAuth.errText come from the real auth.js, bound to ctx
// (say reads ctx.document).
function loadAuthHelpers(ctx) {
  const src = fs.readFileSync(path.join(ROOT, 'public/auth.js'), 'utf8');
  const say = src.match(/function say\(id, text, ok\) \{[\s\S]*?\n  \}/);
  const err = src.match(/function errText\(r\) \{.*\}/);
  assert(say && err, 'say/errText not found in auth.js');
  return { say: vm.runInContext('(' + say[0] + ')', ctx), errText: vm.runInContext('(' + err[0] + ')', ctx) };
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
  { key: 'meshcore-cb-preset', kind: 'scalar' },
  { key: 'cs-theme-overrides', kind: 'scalar' },
  { key: 'mc-dark-tile-provider', kind: 'scalar' },
  { key: 'mc-light-tile-provider', kind: 'scalar' },
];

// fakeServer keeps one account document with the semantics of
// cmd/server/settings_handlers.go and internal/users/settings.go: the first
// write starts a new generation, later writes must name it. fail[METHOD]
// queues canned answers ('network' rejects the request).
function fakeServer() {
  const s = { rev: 0, gen: '', gens: 0, doc: null, puts: [], gets: 0, deletes: 0, fail: { GET: [], PUT: [], DELETE: [] } };
  s.handle = (method, p, body) => {
    if (method === 'PUT') s.puts.push(body);
    if (method === 'GET') s.gets++;
    if (s.fail[method].length) return s.fail[method].shift();
    if (method === 'GET') return { status: 200, data: { revision: s.rev, generation: s.gen, doc: s.doc, allowlist: ALLOW } };
    if (method === 'PUT') {
      if (body.baseRevision !== s.rev || (s.rev && body.baseGeneration !== s.gen)) {
        return { status: 409, data: { revision: s.rev, generation: s.gen, doc: s.doc } };
      }
      if (!s.rev) s.gen = 'gen' + (++s.gens);
      s.rev++;
      s.doc = body.doc;
      return { status: 200, data: { revision: s.rev, generation: s.gen } };
    }
    s.deletes++;
    s.rev = 0;
    s.gen = '';
    s.doc = null;
    return { status: 200, data: { ok: true } };
  };
  return s;
}

const AUTO_IDS = ['syncStatus', 'syncNow', 'syncDelete', 'syncMsg'];

// fakeOverlay is the element showDialog creates. Its buttons are read back
// from the rendered HTML; focus() moves doc.activeElement.
function fakeOverlay(doc) {
  const node = { className: '', innerHTML: '', handlers: {}, removed: false, buttons: null };
  node.addEventListener = (t, f) => { node.handlers[t] = f; };
  node.remove = () => { node.removed = true; };
  node.closest = () => null;
  node.querySelectorAll = () => {
    if (!node.buttons) {
      node.buttons = [...node.innerHTML.matchAll(/data-choice="([^"]*)"/g)].map((m) => {
        const b = { getAttribute: () => m[1], focus() { doc.activeElement = b; } };
        b.closest = () => b;
        return b;
      });
    }
    return node.buttons;
  };
  return node;
}

// makeEnv loads the real module. opts: local {key: raw}, server,
// user (default {id: 7}; null = logged out), enabled (default true), hash,
// customizerReady (default true: the customizer finished its init),
// shared (a Map: localStorage contents shared with another env, as two tabs
// of one browser share them; each env keeps its own Storage prototype),
// prefersDark (the OS colour scheme matchMedia reports).
function makeEnv(opts) {
  opts = opts || {};
  function Storage() { this.m = opts.shared || new Map(); }
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
    // env.nextMe: the user /api/auth/me reports now (undefined: unchanged).
    refreshMe() {
      env.refreshes++;
      if (env.nextMe !== undefined) { user = env.nextMe; (winListeners['cs-auth-changed'] || []).forEach((f) => f({ detail: user })); }
      return Promise.resolve(user);
    },
  };
  const env = { navigations: 0, pipelines: 0, resets: 0, presetClears: 0, refreshes: 0, nextMe: undefined };
  const win = {
    localStorage: ls, Storage, CSAuth: auth, MC_USER_MGMT: enabled ? { enabled: true } : null,
    addEventListener(t, f) { (winListeners[t] = winListeners[t] || []).push(f); },
    dispatchEvent(e) { events.push(e); },
    navigate() { env.navigations++; },
    _customizerV2: { initDone: opts.customizerReady !== false, runPipeline() { env.pipelines++; }, resetAll() { env.resets++; } },
    MeshCorePresets: { clearPreset() { env.presetClears++; } },
    matchMedia: (q) => ({ matches: q === '(prefers-color-scheme: dark)' && !!opts.prefersDark }),
  };
  const doc = {
    visibilityState: 'visible',
    activeElement: null,
    body: { children: [], appendChild(n) { this.children.push(n); } },
    createElement() { return fakeOverlay(doc); },
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
  Object.assign(auth, loadAuthHelpers(ctx));
  vm.runInContext(SRC, ctx);
  Object.defineProperty(env, 'logoutHandler', { get: () => logoutHandler });
  Object.assign(env, {
    ls, server, timers, toasts, events, warnings, errors, els, doc, ctx, win, originalSet,
    api: win.CSSettingsSync, t: win.CSSettingsSync._test,
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
// Tests that do not name a generation use 'g0' on both sides: the device
// synced the account's current document.
const BASE = (user, keys, rev, hold, gen) => ({ 'cs-settings-sync-base': J({ user, keys, hold: !!hold, gen: gen === undefined ? (rev ? 'g0' : '') : gen }), 'cs-settings-sync-rev': String(rev) });
const synced = (keys, rev, gen) => Object.assign({}, keys, BASE(7, keys, rev, false, gen));
const serverWith = (rev, keys, gen) => { const s = fakeServer(); s.rev = rev; s.gen = rev ? (gen || 'g0') : ''; s.doc = rev ? { v: 1, keys } : null; return s; };

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
  assert.deepStrictEqual(env.server.puts[0], { baseRevision: 0, baseGeneration: '', doc: { v: 1, keys: { 'meshcore-favorites': J(['a']), 'meshcore-time-window': '60' } } });
  assert.strictEqual(JSON.parse(env.ls.getItem('cs-settings-sync-base')).gen, env.server.gen);
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
  // M3: the first login names the upload, not "updated from another device".
  assert.deepStrictEqual(env.toasts, ['Your settings are now saved to your account.']);
  // b was only here: pushed on top of revision 4.
  assert.strictEqual(server.puts.length, 1);
  assert.strictEqual(server.puts[0].baseRevision, 4);
  assert.strictEqual(server.doc.keys['meshcore-favorites'], J(['a', 'b']));
});

test('first login with nothing new here: the first-login toast, nothing pushed', async () => {
  const server = serverWith(4, { 'meshcore-favorites': J(['a']), 'meshcore-time-window': '180' });
  const env = makeEnv({ user: null, server, local: { 'meshcore-favorites': J(['a']) } });
  await env.login({ id: 7 });
  assert.strictEqual(env.ls.getItem('meshcore-time-window'), '180');
  assert.strictEqual(env.navigations, 1);
  assert.strictEqual(server.puts.length, 0);
  assert.deepStrictEqual(env.toasts, ['Your settings are now saved to your account.']);
});

test('remote tile providers reach the map through a storage event', async () => {
  const server = serverWith(2, { 'mc-dark-tile-provider': 'carto-dark', 'mc-light-tile-provider': 'osm' });
  const env = makeEnv({ server, local: synced({}, 1) });
  await env.timers.advance(0);
  assert(env.events.some((e) => e.type === 'storage' && e.key === 'mc-dark-tile-provider' && e.newValue === 'carto-dark'));
  assert(env.events.some((e) => e.type === 'storage' && e.key === 'mc-light-tile-provider' && e.newValue === 'osm'));
});

// M1 (final review): app.js ignores a storage event without a value, so a
// remote removal applies the default the app starts with: the OS colour
// scheme for the theme, no preset (clearPreset) for the colour-blind preset.
test('a theme removed on another device falls back to the OS colour scheme', async () => {
  for (const prefersDark of [false, true]) {
    const env = makeEnv({ prefersDark, server: serverWith(2, {}), local: synced({ 'meshcore-theme': 'dark' }, 1) });
    await env.timers.advance(0);
    assert.strictEqual(env.ls.getItem('meshcore-theme'), null);
    const ev = env.events.filter((e) => e.type === 'storage' && e.key === 'meshcore-theme');
    assert.deepStrictEqual(ev.map((e) => e.newValue), [prefersDark ? 'dark' : 'light']);
  }
});

test('a colour-blind preset removed on another device is cleared through cb-presets', async () => {
  const env = makeEnv({ server: serverWith(2, {}), local: synced({ 'meshcore-cb-preset': 'wong' }, 1) });
  await env.timers.advance(0);
  assert.strictEqual(env.ls.getItem('meshcore-cb-preset'), null);
  assert.strictEqual(env.presetClears, 1);
  assert(!env.events.some((e) => e.type === 'storage' && e.key === 'meshcore-cb-preset'));
  assert.strictEqual(env.server.puts.length, 0);
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
  const conflict = { status: 409, data: { revision: 1, generation: 'g0', doc: { v: 1, keys } } };
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

// M7 (final review): the baseline is up to 256 KiB; an unchanged pull
// must not rewrite it every minute.
test('a pull with nothing new does not rewrite the baseline', async () => {
  const keys = { 'meshcore-favorites': J(['a']) };
  const shared = new Map();
  const set = shared.set.bind(shared);
  let baseWrites = 0;
  shared.set = (k, v) => { if (k === 'cs-settings-sync-base') baseWrites++; return set(k, v); };
  const server = serverWith(1, keys);
  const env = makeEnv({ server, shared, local: synced(keys, 1) });
  baseWrites = 0;
  await env.timers.advance(180000);
  assert(server.gets >= 4, 'gets ' + server.gets);
  assert.strictEqual(baseWrites, 0);
  server.rev = 2;
  server.doc = { v: 1, keys: { 'meshcore-favorites': J(['a', 'b']) } };
  await env.timers.advance(60000);
  assert.strictEqual(baseWrites, 1);
  assert.strictEqual(shared.get('cs-settings-sync-rev'), '2');
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

// M6 (final review): after a logout and login in another tab this tab's
// CSRF token is stale and every PUT answers 403; retrying never ends.
// Polish 1: a login in another tab rotates the CSRF token; refreshMe stores
// the new one, so the PUT is retried once with it.
test('403 on a push: same user, retried once with the refreshed token', async () => {
  const server = serverWith(1, {});
  const env = makeEnv({ server, local: synced({}, 1) });
  await env.timers.advance(0);
  server.fail.PUT = [{ status: 403, data: { error: 'missing CSRF token' } }];
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(2000);
  assert.strictEqual(env.refreshes, 1);
  assert.strictEqual(server.puts.length, 2);
  assert.strictEqual(server.doc.keys['meshcore-time-window'], '60');
  assert.strictEqual(env.t.state.blocked, null);
  assert.strictEqual(env.t.state.status, 'ok');
});

test('403 on a push: same user, a second 403 stops syncing until a reload', async () => {
  const server = serverWith(1, {});
  const env = makeEnv({ server, local: synced({}, 1) });
  await env.timers.advance(0);
  server.fail.PUT = [{ status: 403, data: { error: 'missing CSRF token' } }, { status: 403, data: { error: 'bad origin' } }];
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(2000);
  assert.strictEqual(env.refreshes, 2);
  assert.strictEqual(server.puts.length, 2);
  assert.strictEqual(env.t.state.blocked, 'forbidden');
  assert.strictEqual(env.t.state.status, 'forbidden');
  assert.strictEqual(env.t.statusText(), 'Not synced: this tab is out of date. Reload the page to sync again.');
  assert.deepStrictEqual(env.timers.delays(), [], 'a retry is still scheduled');
  env.ls.setItem('meshcore-time-window', '15');
  await env.timers.advance(600000);
  assert.strictEqual(server.puts.length, 2);
});

test('403 on a push: another user logged in since re-activates for that user', async () => {
  const server = serverWith(1, {});
  const env = makeEnv({ server, local: synced({}, 1) });
  await env.timers.advance(0);
  server.fail.PUT = [{ status: 403, data: { error: 'missing CSRF token' } }];
  env.nextMe = { id: 8 };
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(2000);
  assert.strictEqual(env.refreshes, 1);
  assert.strictEqual(env.t.state.userId, 8);
  assert.notStrictEqual(env.t.state.status, 'forbidden');
  assert.strictEqual(env.t.state.blocked, null);
});

test('403 on a pull: logged out since deactivates', async () => {
  const server = serverWith(1, {});
  const env = makeEnv({ server, local: synced({}, 1) });
  await env.timers.advance(0);
  server.fail.GET = [{ status: 403, data: { error: 'forbidden' } }];
  env.nextMe = null;
  await env.fireDoc('visibilitychange');
  assert.strictEqual(env.refreshes, 1);
  assert.strictEqual(env.t.state.active, false);
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
  assert.deepStrictEqual(env.server.puts[0], { baseRevision: 0, baseGeneration: '', doc: { v: 1, keys: { 'meshcore-favorites': J(['a', 'b']) } } });
});

// C2 (final review): revisions restart at 1 after a delete, so the baseline
// carries the document's generation; a baseline of another generation
// counts as none (union, nothing dropped).
test('a copy deleted and started again elsewhere: the old baseline drops nothing here', async () => {
  // Synced revision 5 of generation g0, then closed. Meanwhile the copy was
  // deleted and a new phone uploaded [p] as revision 1 of generation g9.
  const server = serverWith(1, { 'meshcore-favorites': J(['p']) }, 'g9');
  const env = makeEnv({ server, local: synced({ 'meshcore-favorites': J(['a', 'b']) }, 5) });
  await env.timers.advance(0);
  assert.strictEqual(env.ls.getItem('meshcore-favorites'), J(['p', 'a', 'b']));
  assert.strictEqual(server.puts.length, 1);
  assert.strictEqual(server.puts[0].baseRevision, 1);
  assert.strictEqual(server.puts[0].baseGeneration, 'g9');
  assert.strictEqual(server.doc.keys['meshcore-favorites'], J(['p', 'a', 'b']));
  assert.strictEqual(JSON.parse(env.ls.getItem('cs-settings-sync-base')).gen, 'g9');
});

test('an offline retry from an old generation at the same revision gets 409 and merges', async () => {
  const server = serverWith(2, { 'meshcore-favorites': J(['a']) });
  const env = makeEnv({ server, local: synced({ 'meshcore-favorites': J(['a']) }, 2) });
  env.doc.visibilityState = 'hidden'; // no pulls: only the retry talks to the server
  await env.timers.advance(0);
  server.fail.PUT = ['network'];
  env.ls.setItem('meshcore-favorites', J(['a', 'b']));
  await env.timers.advance(2000);
  assert.strictEqual(env.t.state.status, 'retrying');
  // Meanwhile: deleted, and a new copy reached revision 2 again.
  server.gen = 'g9';
  server.doc = { v: 1, keys: { 'meshcore-favorites': J(['c', 'd']) } };
  await env.timers.advance(2000);
  const tried = server.puts[1];
  assert.deepStrictEqual([tried.baseRevision, tried.baseGeneration], [2, 'g0']);
  assert.strictEqual(server.rev, 3);
  assert.strictEqual(server.doc.keys['meshcore-favorites'], J(['c', 'd', 'a', 'b']));
  assert.strictEqual(env.ls.getItem('meshcore-favorites'), J(['c', 'd', 'a', 'b']));
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

// holdNext delays the answer to the next request of method until the
// returned release() is called. The fake server still handles it at send
// time, so the held answer reflects the account as it was then.
function holdNext(env, method) {
  const orig = env.win.CSAuth.request;
  let release, used = false;
  const gate = new Promise((r) => { release = r; });
  env.win.CSAuth.request = function (m, p, b) {
    const res = orig.call(this, m, p, b);
    if (m !== method || used) return res;
    used = true;
    return gate.then(() => res);
  };
  return () => { release(); return settle(); };
}

test('a pull answered after a successful push does not revert the local change', async () => {
  const env = makeEnv({ server: serverWith(1, { 'meshcore-time-window': '60' }), local: synced({ 'meshcore-time-window': '60' }, 1) });
  await env.timers.advance(0);
  const release = holdNext(env, 'GET');
  await env.fireDoc('visibilitychange'); // GET sent, answer (revision 1) held
  env.ls.setItem('meshcore-time-window', '180');
  await env.timers.advance(2000);
  assert.strictEqual(env.server.rev, 2);
  await release();
  assert.strictEqual(env.ls.getItem('meshcore-time-window'), '180');
  assert.strictEqual(env.ls.getItem('cs-settings-sync-rev'), '2');
  assert.strictEqual(env.navigations, 0);
  assert.strictEqual(env.toasts.indexOf('Settings updated from another device'), -1);
});

test('a revision-0 pull answered after the first upload does not enter hold', async () => {
  const env = makeEnv({ user: null, local: { 'meshcore-favorites': J(['a']) } });
  await settle();
  const release = holdNext(env, 'GET');
  const loggedIn = env.login({ id: 7 }); // first GET held
  env.fireDoc('visibilitychange'); // second GET answered at once: revision 0, first upload
  await loggedIn;
  assert.strictEqual(env.server.rev, 1);
  await release();
  assert.strictEqual(JSON.parse(env.ls.getItem('cs-settings-sync-base')).hold, false);
  assert.strictEqual(env.ls.getItem('cs-settings-sync-rev'), '1');
  assert.strictEqual(env.t.state.status, 'ok');
});

test('a push answered after logout and login again does not touch the new session', async () => {
  const env = makeEnv({ server: serverWith(1, {}), local: synced({}, 1) });
  await env.timers.advance(0);
  const releaseOld = holdNext(env, 'PUT');
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(2000); // old session PUT held
  await env.logout();
  await env.login({ id: 7 });
  const releaseNew = holdNext(env, 'PUT');
  env.ls.setItem('meshcore-time-window', '15');
  await env.timers.advance(2000); // new session PUT held
  const inFlight = env.t.state.pushing;
  assert(inFlight);
  await releaseOld();
  assert.strictEqual(env.t.state.pushing, inFlight);
  assert.strictEqual(env.t.state.status, 'syncing');
  await releaseNew();
  assert.strictEqual(env.t.state.pushing, null);
  assert.strictEqual(env.ls.getItem('cs-settings-sync-rev'), '3');
  assert.strictEqual(JSON.parse(env.ls.getItem('cs-settings-sync-base')).keys['meshcore-time-window'], '15');
});

// I1 (final review): two tabs share localStorage, so the stored baseline is
// the truth for both; a tab never merges against its own stale copy.
test('two tabs: a removal from another device is not undone by a tab with an old baseline', async () => {
  const keys = { 'meshcore-favorites': J(['a']) };
  const server = serverWith(2, keys);
  const shared = new Map();
  const tab2 = makeEnv({ server, shared, local: synced(keys, 2) });
  await tab2.timers.advance(0);
  const tab1 = makeEnv({ server, shared });
  await tab1.timers.advance(0);
  tab1.ls.setItem('meshcore-favorites', J(['a', 'X']));
  await tab1.timers.advance(2000);
  assert.strictEqual(server.rev, 3);
  // A phone removes X.
  server.rev = 4;
  server.doc = { v: 1, keys: { 'meshcore-favorites': J(['a']) } };
  await tab2.fireDoc('visibilitychange');
  assert.strictEqual(shared.get('meshcore-favorites'), J(['a']));
  assert.strictEqual(server.doc.keys['meshcore-favorites'], J(['a']));
  assert.strictEqual(server.rev, 4);
});

test('two tabs: a pull answered after the other tab pushed does not drop that change', async () => {
  const keys = { 'meshcore-favorites': J(['a']) };
  const server = serverWith(2, keys);
  const shared = new Map();
  const tab2 = makeEnv({ server, shared, local: synced(keys, 2) });
  await tab2.timers.advance(0);
  const tab1 = makeEnv({ server, shared });
  await tab1.timers.advance(0);
  const release = holdNext(tab2, 'GET');
  await tab2.fireDoc('visibilitychange'); // GET sent: revision 2, answer held
  tab1.ls.setItem('meshcore-favorites', J(['a', 'X']));
  await tab1.timers.advance(2000);
  assert.strictEqual(server.rev, 3);
  await release();
  assert.strictEqual(shared.get('meshcore-favorites'), J(['a', 'X']));
  assert.strictEqual(server.doc.keys['meshcore-favorites'], J(['a', 'X']));
});

// ── logout dialog and account section ──
const KEYS = { 'meshcore-favorites': J(['a']) };

test('the logout handler is registered only while active', async () => {
  const off = makeEnv({ enabled: false });
  await settle();
  assert.strictEqual(off.logoutHandler, null);
  const env = makeEnv({ server: serverWith(1, KEYS), local: synced(KEYS, 1) });
  await env.timers.advance(0);
  assert.strictEqual(typeof env.logoutHandler, 'function');
  await env.logout();
  assert.strictEqual(env.logoutHandler, null);
});

test('logout dialog: keep first and focused, channel-key text, three choices', async () => {
  const env = makeEnv({ server: serverWith(1, KEYS), local: synced(KEYS, 1) });
  await env.timers.advance(0);
  let seen = null;
  env.t.useDialog((opts) => { seen = plain(opts); return Promise.resolve('keep'); });
  await env.logoutHandler();
  assert.deepStrictEqual(seen.choices.map((c) => c.id), ['keep', 'remove', 'cancel']);
  assert.strictEqual(seen.choices[0].label, 'Keep my settings on this device');
  assert.strictEqual(seen.choices[0].primary, true);
  assert.strictEqual(seen.choices[1].label, 'Remove my settings from this device');
  assert(seen.text.join(' ').indexOf('Channel keys are never synced') !== -1);
});

test('logout dialog: keep pushes pending changes first and leaves local data', async () => {
  const env = makeEnv({ server: serverWith(1, KEYS), local: synced(KEYS, 1) });
  await env.timers.advance(0);
  env.ls.setItem('meshcore-time-window', '60'); // still in the 2 s debounce
  env.t.useDialog(() => Promise.resolve('keep'));
  const h = plain(await env.logoutHandler());
  assert.deepStrictEqual(h, {});
  assert.strictEqual(env.server.puts.length, 1);
  assert.strictEqual(env.server.doc.keys['meshcore-time-window'], '60');
  assert.strictEqual(env.ls.getItem('meshcore-time-window'), '60');
});

test('logout dialog: remove deletes synced keys and the baseline, never channel keys', async () => {
  const local = Object.assign({ 'meshcore-time-window': '60', corescope_channel_keys: '{"#x":"00"}', 'meshcore-api-key': 'k' }, synced(KEYS, 1));
  const env = makeEnv({ server: serverWith(1, KEYS), local });
  await env.timers.advance(0);
  env.t.useDialog(() => Promise.resolve('remove'));
  const h = await env.logoutHandler();
  assert.strictEqual(typeof h.afterLogout, 'function');
  h.afterLogout();
  for (const k of ['meshcore-favorites', 'meshcore-time-window', 'cs-settings-sync-base', 'cs-settings-sync-rev']) {
    assert.strictEqual(env.ls.getItem(k), null, k + ' left behind');
  }
  assert.strictEqual(env.ls.getItem('corescope_channel_keys'), '{"#x":"00"}');
  assert.strictEqual(env.ls.getItem('meshcore-api-key'), 'k');
});

// M2 (final review): removing the keys alone left the theme and the
// customizer CSS applied until a reload.
test('logout dialog: remove shows the defaults at once', async () => {
  const keys = { 'meshcore-theme': 'dark', 'meshcore-cb-preset': 'wong', 'cs-theme-overrides': '{"x":1}' };
  const env = makeEnv({ server: serverWith(1, keys), local: synced(keys, 1) });
  await env.timers.advance(0);
  env.t.useDialog(() => Promise.resolve('remove'));
  const h = await env.logoutHandler();
  const p0 = env.pipelines;
  h.afterLogout();
  assert.strictEqual(env.resets, 1, 'customizer Reset All teardown not run');
  assert.strictEqual(env.pipelines, p0 + 1);
  assert.strictEqual(env.presetClears, 1);
  assert(env.events.some((e) => e.type === 'storage' && e.key === 'meshcore-theme' && e.newValue === 'light'));
  assert.strictEqual(env.ls.getItem('cs-theme-overrides'), null);
  await env.timers.advance(10000);
  assert.strictEqual(env.server.puts.length, 0, 'the teardown pushed');
});

test('logout dialog: remove before the customizer finished its init runs no pipeline', async () => {
  const env = makeEnv({ customizerReady: false, server: serverWith(1, KEYS), local: synced(KEYS, 1) });
  await env.timers.advance(0);
  env.t.useDialog(() => Promise.resolve('remove'));
  (await env.logoutHandler()).afterLogout();
  assert.strictEqual(env.resets + env.pipelines, 0);
});

test('logout dialog: remove after a failed push keeps the data and says so', async () => {
  const server = serverWith(1, KEYS);
  const env = makeEnv({ server, local: synced(KEYS, 1) });
  await env.timers.advance(0);
  env.ls.setItem('meshcore-time-window', '60');
  server.fail.PUT = ['network'];
  env.t.useDialog(() => Promise.resolve('remove'));
  const h = await env.logoutHandler();
  h.afterLogout();
  assert.strictEqual(env.ls.getItem('meshcore-time-window'), '60');
  assert.strictEqual(env.ls.getItem('meshcore-favorites'), J(['a']));
  assert(env.toasts.indexOf('Your latest settings could not be saved to your account, so they stay on this device.') !== -1);
});

// M5 (final review): a hanging connection must not hold the logout dialog.
test('logout dialog: a push that hangs counts as failed after 5 s', async () => {
  for (const choice of ['remove', 'keep']) {
    const env = makeEnv({ server: serverWith(1, KEYS), local: synced(KEYS, 1) });
    await env.timers.advance(0);
    holdNext(env, 'PUT'); // never released
    env.ls.setItem('meshcore-time-window', '60');
    env.t.useDialog(() => Promise.resolve(choice));
    let h = null;
    env.logoutHandler().then((r) => { h = r; });
    await env.timers.advance(4999);
    assert.strictEqual(h, null, choice + ': resolved before the timeout');
    await env.timers.advance(1);
    assert(h, choice + ': still waiting after 5 s');
    if (choice === 'keep') { assert.deepStrictEqual(plain(h), {}); continue; }
    h.afterLogout();
    assert.strictEqual(env.ls.getItem('meshcore-time-window'), '60');
    assert.strictEqual(env.ls.getItem('meshcore-favorites'), J(['a']));
    assert(env.toasts.indexOf('Your latest settings could not be saved to your account, so they stay on this device.') !== -1);
    assert(env.warnings.some((w) => w.indexOf('5 s') !== -1), 'timeout not logged');
  }
});

test('logout dialog: Cancel, Escape or the backdrop cancel the logout', async () => {
  const env = makeEnv({ server: serverWith(1, KEYS), local: synced(KEYS, 1) });
  await env.timers.advance(0);
  for (const choice of ['cancel', null]) {
    env.t.useDialog(() => Promise.resolve(choice));
    assert.deepStrictEqual(plain(await env.logoutHandler()), { cancel: true });
  }
});

test('account section: status, Sync now, delete with confirmation', async () => {
  const env = makeEnv({ server: serverWith(1, KEYS), local: synced(KEYS, 1) });
  await env.timers.advance(0);
  const el = env.el('syncSection');
  env.api.mountSection(el);
  assert(el.innerHTML.indexOf('id="syncNow"') !== -1 && el.innerHTML.indexOf('id="syncDelete"') !== -1);
  assert(el.innerHTML.indexOf('Delete synced settings from my account') !== -1);
  assert(env.els.syncStatus.textContent.indexOf('Last synced ') === 0, env.els.syncStatus.textContent);
  const g0 = env.server.gets;
  await env.els.syncNow.handlers.click();
  await settle();
  assert.strictEqual(env.server.gets, g0 + 1);
  env.t.useDialog(() => Promise.resolve('cancel'));
  await env.els.syncDelete.handlers.click();
  assert.strictEqual(env.server.deletes, 0);
  env.t.useDialog(() => Promise.resolve('delete'));
  await env.els.syncDelete.handlers.click();
  await settle();
  assert.strictEqual(env.server.deletes, 1);
  assert.strictEqual(env.t.state.hold, true);
  assert.strictEqual(env.els.syncMsg.textContent, 'Synced settings deleted from your account.');
  assert.strictEqual(env.els.syncStatus.textContent, 'No settings saved in your account. Your next change starts a new copy.');
  assert.strictEqual(env.ls.getItem('meshcore-favorites'), J(['a']));
});

test('dialog: escaped, first choice focused, Tab trapped, Escape and backdrop dismiss, focus returns', async () => {
  const env = makeEnv({ enabled: false });
  const prev = { focused: 0, focus() { this.focused++; } };
  const open = (title) => {
    env.doc.activeElement = prev;
    const p = env.t.showDialog({ title, text: ['<b>t</b>'], choices: [{ id: 'a', label: 'A', primary: true }, { id: 'b', label: 'B' }] });
    return { p, ov: env.doc.body.children[env.doc.body.children.length - 1] };
  };
  let { p, ov } = open('<img>');
  assert(ov.innerHTML.indexOf('<img>') === -1 && ov.innerHTML.indexOf('&lt;img&gt;') !== -1);
  assert(ov.innerHTML.indexOf('<b>') === -1);
  assert(ov.innerHTML.indexOf('role="dialog"') !== -1 && ov.innerHTML.indexOf('aria-modal="true"') !== -1);
  const [a, b] = ov.querySelectorAll();
  assert.strictEqual(env.doc.activeElement, a);
  const key = (k, shift) => {
    let prevented = false;
    ov.handlers.keydown({ key: k, shiftKey: !!shift, preventDefault() { prevented = true; }, stopPropagation() {} });
    return prevented;
  };
  assert(key('Tab', true));
  assert.strictEqual(env.doc.activeElement, b);
  assert(key('Tab'));
  assert.strictEqual(env.doc.activeElement, a);
  key('Escape');
  assert.strictEqual(await p, null);
  assert(ov.removed);
  assert.strictEqual(prev.focused, 1);
  ({ p, ov } = open('x'));
  ov.handlers.click({ target: ov });
  assert.strictEqual(await p, null);
  ({ p, ov } = open('x'));
  ov.handlers.click({ target: ov.querySelectorAll()[1] });
  assert.strictEqual(await p, 'b');
});

test('delete while retrying: the pending retry does not recreate the account copy', async () => {
  const server = serverWith(1, KEYS);
  const env = makeEnv({ server, local: synced(KEYS, 1) });
  await env.timers.advance(0);
  server.fail.PUT = ['network'];
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(2000);
  assert.strictEqual(env.t.state.status, 'retrying');
  const puts = server.puts.length;
  const r = await env.t.deleteRemote();
  assert.strictEqual(r.ok, true);
  await env.timers.advance(600000);
  assert.strictEqual(server.puts.length, puts);
  assert.strictEqual(env.t.state.hold, true);
  assert.strictEqual(server.doc, null);
});

test('delete with a PUT in flight: the late answer does not undo the hold', async () => {
  const server = serverWith(1, KEYS);
  const env = makeEnv({ server, local: synced(KEYS, 1) });
  await env.timers.advance(0);
  const release = holdNext(env, 'PUT');
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(2000); // PUT sent, answer held
  const d = env.t.deleteRemote();
  await release();
  assert.strictEqual((await d).ok, true);
  assert.strictEqual(env.t.state.hold, true, 'hold lost when the PUT answer landed');
  assert.strictEqual(env.t.state.rev, 0);
  await env.timers.advance(600000);
  assert.strictEqual(env.t.state.hold, true);
  assert.strictEqual(server.doc, null);
  assert.strictEqual(server.deletes, 1);
  assert.strictEqual(JSON.parse(env.ls.getItem('cs-settings-sync-base')).hold, true);
});

test('a failed delete puts unsynced changes back on the retry backoff', async () => {
  const server = serverWith(1, KEYS);
  const env = makeEnv({ server, local: synced(KEYS, 1) });
  await env.timers.advance(0);
  server.fail.PUT = ['network'];
  env.ls.setItem('meshcore-time-window', '60');
  await env.timers.advance(2000);
  server.fail.DELETE = [{ status: 500, data: { error: 'boom' } }];
  const r = await env.t.deleteRemote();
  assert.strictEqual(r.status, 500);
  assert.strictEqual(env.t.state.hold, false);
  assert.strictEqual(env.t.state.status, 'retrying');
  assert.strictEqual(env.timers.delays().length, 1, 'no retry scheduled');
  await env.timers.advance(600000);
  assert.strictEqual(server.doc.keys['meshcore-time-window'], '60');
  assert.strictEqual(env.t.state.status, 'ok');
});

test('delete confirmation focuses Cancel; the dialog focuses opts.focus', async () => {
  const env = makeEnv({ server: serverWith(1, KEYS), local: synced(KEYS, 1) });
  await env.timers.advance(0);
  env.api.mountSection(env.el('syncSection'));
  let seen = null;
  env.t.useDialog((opts) => { seen = plain(opts); return Promise.resolve('cancel'); });
  await env.els.syncDelete.handlers.click();
  assert.strictEqual(seen.focus, 'cancel');
  const p = env.t.showDialog(Object.assign({}, seen, { title: 'x' }));
  const ov = env.doc.body.children[env.doc.body.children.length - 1];
  const btns = ov.querySelectorAll();
  assert.strictEqual(btns[0].getAttribute(), 'delete');
  assert.strictEqual(env.doc.activeElement, btns[1]);
  ov.handlers.click({ target: ov });
  assert.strictEqual(await p, null);
});

test('status texts', async () => {
  const env = makeEnv({ server: serverWith(1, {}), local: synced({}, 1) });
  await env.timers.advance(0);
  const st = env.t.state;
  st.status = 'retrying';
  assert.strictEqual(env.t.statusText(), 'Not synced: retrying');
  st.status = 'too-large'; st.tooLarge = ['cs-theme-overrides', 'meshcore-my-nodes'];
  assert.strictEqual(env.t.statusText(), 'Not synced: your settings are larger than your account can hold. Largest: cs-theme-overrides, meshcore-my-nodes');
  st.status = 'idle';
  assert.strictEqual(env.t.statusText(), 'Not synced yet');
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
