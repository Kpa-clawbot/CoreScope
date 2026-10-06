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
// user (default {id: 7}; null = logged out), enabled (default true), hash.
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
    _customizerV2: { runPipeline() { env.pipelines++; } },
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
  ['set: the same list spelled differently is not a local change',
    { fav: '["a", "b"]' }, { fav: '["a","b"]' }, { fav: '["a","b"]' }, { fav: '["a", "b"]' }, [], true],
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

(async () => {
  let passed = 0, failed = 0;
  for (const t of tests) {
    try { await t.fn(); passed++; console.log('  ok   ' + t.name); }
    catch (e) { failed++; console.log('  FAIL ' + t.name + ': ' + (e && e.stack || e)); }
  }
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed ? 1 : 0);
})();
