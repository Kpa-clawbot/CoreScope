/* Unit tests for the optional user-management UI (public/auth.js). Real file
 * loaded in a vm sandbox, same pattern as test-frontend-helpers.js. */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

const ROOT = path.resolve(__dirname, '..', '..');
let passed = 0, failed = 0;
const pending = [];
function test(name, fn) {
  const p = Promise.resolve().then(fn).then(
    () => { passed++; console.log('  ok   ' + name); },
    (e) => { failed++; console.log('  FAIL ' + name + ': ' + e.message); });
  pending.push(p);
  return p;
}

// escapeHtml comes from the real app.js, not a copy.
function loadEscapeHtml() {
  const src = fs.readFileSync(path.join(ROOT, 'public/app.js'), 'utf8');
  const m = src.match(/function escapeHtml\(s\) \{[\s\S]*?\n\}/);
  assert(m, 'escapeHtml not found in app.js');
  return vm.runInNewContext('(' + m[0].replace(/^function escapeHtml/, 'function') + ')');
}

function makeEnv(routes) {
  const els = {};
  const events = [];
  const mkEl = (id) => ({
    id: id || '', className: '', innerHTML: '', textContent: '', hidden: false,
    classList: { add() {}, remove() {} },
    setAttribute() {}, addEventListener() {}, appendChild() {},
  });
  const wrap = mkEl('accountWrap');
  const right = { insertBefore(el) { els[el.id] = el; } };
  const doc = {
    getElementById(id) {
      if (id === 'hamburger') return null;
      if (id === 'accountWrap') return els.accountWrap || null;
      if (id === 'csAuthToast') return els.csAuthToast || null;
      return els[id] || mkEl(id);
    },
    querySelector(sel) { return sel === '.top-nav .nav-right' ? right : null; },
    createElement() { return mkEl(); },
    addEventListener() {},
    body: { appendChild(el) { els[el.id] = el; } },
  };
  const calls = [];
  const win = {
    addEventListener() {},
    dispatchEvent(e) { events.push(e); },
    MC_USER_MGMT: { enabled: true },
    MeshConfigReady: Promise.resolve(),
  };
  win.window = win;
  const ctx = {
    window: win, document: doc, console, Promise, JSON, String, Object,
    setTimeout, clearTimeout,
    CustomEvent: function (type, init) { this.type = type; this.detail = init && init.detail; },
    escapeHtml: loadEscapeHtml(),
    fetch(url, opts) {
      calls.push({ url, opts });
      const r = routes(url, opts) || { status: 404, body: {} };
      return Promise.resolve({ ok: r.status < 400, status: r.status, json: () => Promise.resolve(r.body) });
    },
  };
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(path.join(ROOT, 'public/auth.js'), 'utf8'), ctx);
  return { win, els, events, calls };
}

const ME = { id: 1, email: 'a@b.c', displayName: 'Ann', role: 'user', csrfToken: 'tok123' };

console.log('auth.js');

test('inert when the server does not advertise userManagement', async () => {
  const calls = [];
  const win = { addEventListener() {}, dispatchEvent() {}, MC_USER_MGMT: null, MeshConfigReady: Promise.resolve() };
  win.window = win;
  const ctx = { window: win, document: { addEventListener() {}, getElementById() { return null; }, querySelector() { return null; } },
    console, Promise, JSON, String, Object, setTimeout, clearTimeout, CustomEvent: function () {},
    escapeHtml: loadEscapeHtml(), fetch() { calls.push(1); return Promise.reject(new Error('no')); } };
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(path.join(ROOT, 'public/auth.js'), 'utf8'), ctx);
  await win.CSAuth.ready();
  assert.strictEqual(win.CSAuth.isEnabled(), false);
  assert.strictEqual(calls.length, 0);
});

test('login state is loaded from /api/auth/me and announced', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : null);
  await env.win.CSAuth.ready();
  assert.strictEqual(env.win.CS_USER.displayName, 'Ann');
  assert(env.events.some((e) => e.type === 'cs-auth-changed'));
});

test('CSRF header only on non-GET requests', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : { status: 200, body: {} });
  await env.win.CSAuth.ready();
  env.calls.length = 0;
  await env.win.CSAuth.request('GET', '/api/x');
  await env.win.CSAuth.request('POST', '/api/x', { a: 1 });
  assert.strictEqual(env.calls[0].opts.headers['X-CS-CSRF'], undefined);
  assert.strictEqual(env.calls[1].opts.headers['X-CS-CSRF'], 'tok123');
  assert.strictEqual(env.calls[1].opts.body, '{"a":1}');
});

test('401 on an authenticated call clears the user and toasts', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : { status: 401, body: { error: 'x' } });
  await env.win.CSAuth.ready();
  const r = await env.win.CSAuth.request('GET', '/api/auth/sessions');
  assert.strictEqual(r.status, 401);
  assert.strictEqual(env.win.CS_USER, null);
  assert.strictEqual(env.els.csAuthToast.textContent, 'You were logged out.');
});

test('401 from the login call itself keeps state and shows no toast', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : { status: 401, body: {} });
  await env.win.CSAuth.ready();
  await env.win.CSAuth.request('POST', '/api/auth/login', { email: 'a', password: 'b' });
  assert.strictEqual(env.win.CS_USER.displayName, 'Ann');
  assert.strictEqual(env.els.csAuthToast, undefined);
});

test('401 without a logged-in user shows no toast', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 401, body: {} } : { status: 401, body: {} });
  await env.win.CSAuth.ready();
  await env.win.CSAuth.request('GET', '/api/x');
  assert.strictEqual(env.els.csAuthToast, undefined);
});

test('display name is escaped in the header label', async () => {
  const payload = '<img src=x onerror=alert(1)>';
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: Object.assign({}, ME, { displayName: payload }) } : null);
  await env.win.CSAuth.ready();
  const html = env.els.accountWrap.innerHTML;
  assert(html.indexOf('<img') === -1, 'raw tag in header HTML: ' + html);
  assert(html.indexOf('&lt;img src=x onerror=alert(1)&gt;') !== -1);
});

test('Users menu entry is shown to admins only', async () => {
  const user = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : null);
  await user.win.CSAuth.ready();
  assert(user.els.accountWrap.innerHTML.indexOf('#/admin/users') === -1);
  const admin = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: Object.assign({}, ME, { role: 'admin' }) } : null);
  await admin.win.CSAuth.ready();
  assert(admin.els.accountWrap.innerHTML.indexOf('#/admin/users') !== -1);
});

test('logged-out header is a login link', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 401, body: {} } : null);
  await env.win.CSAuth.ready();
  assert(env.els.accountWrap.innerHTML.indexOf('href="#/account/login"') !== -1);
});

Promise.all(pending).then(() => {
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed ? 1 : 0);
});
