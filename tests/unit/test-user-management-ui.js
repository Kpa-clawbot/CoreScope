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

test('401 from the activate call (wrong password) keeps the session and shows no toast', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : { status: 401, body: { error: 'wrong password for this account' } });
  await env.win.CSAuth.ready();
  await env.win.CSAuth.request('POST', '/api/auth/activate', { token: 't', password: 'p' });
  assert.strictEqual(env.win.CS_USER.displayName, 'Ann');
  assert.strictEqual(env.els.csAuthToast, undefined);
});

console.log('mobile nav account entry');

// Slice the real route tables and builders out of the two files; markers
// failing to match throws instead of silently testing nothing.
function sliceBetween(file, from, to) {
  const src = fs.readFileSync(path.join(ROOT, 'public', file), 'utf8');
  const a = src.indexOf(from), b = src.indexOf(to);
  assert(a !== -1 && b > a, 'markers moved in ' + file);
  return src.slice(a, b);
}
function loadNav(file, from, to, fn, win) {
  const sb = { window: Object.assign({ addEventListener() {} }, win), console };
  vm.createContext(sb);
  vm.runInContext(sliceBetween(file, from, to) + '\nthis.fn = ' + fn + ';', sb);
  return sb.fn;
}
const NAVS = [
  { name: 'bottom-nav moreRoutes', file: 'bottom-nav.js', from: 'var MORE_ROUTES = [', to: 'var SHEET_ID', fn: 'moreRoutes' },
  { name: 'nav-drawer routes', file: 'nav-drawer.js', from: 'var ROUTES = [', to: 'function phIconHTML', fn: 'routes' },
];
NAVS.forEach((n) => {
  const get = (win) => loadNav(n.file, n.from, n.to, n.fn, win)();
  const acct = (list) => list.filter((r) => r.route === 'account');
  test(n.name + ': no account entry when user management is off', () => {
    assert.strictEqual(acct(get({})).length, 0);
  });
  test(n.name + ': Log in entry when logged out', () => {
    const a = acct(get({ MC_USER_MGMT: { enabled: true } }));
    assert.strictEqual(a.length, 1);
    assert.strictEqual(a[0].hash, '#/account/login');
    assert.strictEqual(a[0].label, 'Log in');
  });
  test(n.name + ': My account entry when logged in', () => {
    const a = acct(get({ MC_USER_MGMT: { enabled: true }, CS_USER: { id: 1 } }));
    assert.strictEqual(a.length, 1);
    assert.strictEqual(a[0].hash, '#/account');
    assert.strictEqual(a[0].label, 'My account');
  });
  test(n.name + ': coverage insert still present after analytics', () => {
    const list = get({ MC_CLIENT_RX_COVERAGE: true, MC_USER_MGMT: { enabled: true } });
    const i = list.findIndex((r) => r.route === 'analytics');
    assert.strictEqual(list[i + 1].route, 'rx-coverage');
    assert.strictEqual(list.length, get({}).length + 2);
  });
});

console.log('account.js');

function loadAccount(hash, routes) {
  const els = {};
  const mk = () => {
    const el = { value: '', textContent: '', handlers: {}, cls: {}, html: '' };
    el.classList = { toggle(c, on) { el.cls[c] = !!on; } };
    el.addEventListener = (t, fn) => { el.handlers[t] = fn; };
    el.querySelector = () => null;
    el.insertAdjacentHTML = (pos, html) => { els.renewLink = { html }; };
    return el;
  };
  const doc = { getElementById(id) { return els[id] || (id === 'renewLink' ? null : (els[id] = mk())); } };
  let pages = {};
  const calls = [];
  const user = { current: null };
  const CSAuth = {
    request(method, p, body) { calls.push({ method, p, body }); return Promise.resolve(routes(p, body)); },
    setUser(u) { user.current = u; },
    user() { return user.current; },
    notify() {}, refreshMe() { return Promise.resolve(); },
  };
  const loc = { hash };
  const ctx = { window: { CSAuth }, document: doc, CSAuth, location: loc, URLSearchParams, Promise, String,
    escapeHtml: loadEscapeHtml(), registerPage(n, m) { pages[n] = m; }, console };
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(path.join(ROOT, 'public/account.js'), 'utf8'), ctx);
  return { doc, t: ctx.window.CSAccount._test, els, calls, loc, user, pages };
}
const submitForm = async (env, formId) => {
  await env.els[formId].handlers.submit({ preventDefault() {} });
};

test('profile view escapes email and session fields', () => {
  const env = loadAccount('#/account', () => ({}));
  const payload = '<img src=x onerror=alert(1)>';
  const html = env.t.profileHtml({ email: payload, role: 'admin', displayName: payload });
  assert(html.indexOf('<img') === -1, 'raw tag in profile HTML');
  assert(html.indexOf('&lt;img src=x onerror=alert(1)&gt;') !== -1);
  const sess = env.t.sessionsHtml([{ id: '1"><b>', userAgent: payload, lastSeenAt: 'x', current: false }]);
  assert(sess.indexOf('<img') === -1 && sess.indexOf('<b>') === -1, 'raw tag in sessions HTML: ' + sess);
});

test('sessions list marks the current device without a logout button', () => {
  const env = loadAccount('#/account', () => ({}));
  const h = env.t.sessionsHtml([{ id: 1, userAgent: 'A', lastSeenAt: '2026-01-01T00:00:00Z', current: true },
    { id: 2, userAgent: '', lastSeenAt: '2026-01-01T00:00:00Z', current: false }]);
  assert.strictEqual((h.match(/data-sess=/g) || []).length, 1);
  assert(h.indexOf('data-sess="2"') !== -1 && h.indexOf('Unknown device') !== -1);
});

test('activate posts token and password, logs in on success', async () => {
  const env = loadAccount('#/account/activate?token=T0K', () => ({ ok: true, status: 200, data: { id: 1, displayName: 'Ann' } }));
  env.t.views.activate({});
  env.doc.getElementById('actPassword').value = 'hunter2hunter2';
  await submitForm(env, 'activateForm');
  assert.strictEqual(JSON.stringify(env.calls[0]), JSON.stringify({ method: 'POST', p: '/api/auth/activate', body: { token: 'T0K', password: 'hunter2hunter2' } }));
  assert.strictEqual(env.user.current.displayName, 'Ann');
  assert.strictEqual(env.loc.hash, '#/account');
});

test('activate 401 shows the wrong-password message and stays on the form', async () => {
  const env = loadAccount('#/account/activate?token=T', () => ({ ok: false, status: 401, data: { error: 'wrong password for this account' } }));
  env.t.views.activate({});
  await submitForm(env, 'activateForm');
  assert.strictEqual(env.els.accountMsg.textContent, 'Wrong password for this account');
  assert.strictEqual(env.user.current, null);
  assert.strictEqual(env.loc.hash, '#/account/activate?token=T');
});

test('activate 410 offers a new registration link', async () => {
  const env = loadAccount('#/account/activate?token=T', () => ({ ok: false, status: 410, data: { error: 'this link has expired' } }));
  env.t.views.activate({});
  await submitForm(env, 'activateForm');
  assert(env.els.renewLink.html.indexOf('href="#/account/register"') !== -1);
  assert(env.els.renewLink.html.indexOf('Register again to get a new link') !== -1);
});

test('reset 410 offers the forgot-password link', async () => {
  const env = loadAccount('#/account/reset?token=T', () => ({ ok: false, status: 410, data: { error: 'this link has expired' } }));
  env.t.views.reset({});
  env.doc.getElementById('resetPassword').value = 'abcdefghijkl';
  env.doc.getElementById('resetPassword2').value = 'abcdefghijkl';
  await submitForm(env, 'resetForm');
  assert.strictEqual(JSON.stringify(env.calls[0].body), JSON.stringify({ token: 'T', password: 'abcdefghijkl' }));
  assert(env.els.renewLink.html.indexOf('href="#/account/forgot"') !== -1);
});

test('reset refuses mismatching passwords without a request', async () => {
  const env = loadAccount('#/account/reset?token=T', () => ({ ok: true, status: 200, data: {} }));
  env.t.views.reset({});
  env.doc.getElementById('resetPassword').value = 'abcdefghijkl';
  env.doc.getElementById('resetPassword2').value = 'different-pass';
  await submitForm(env, 'resetForm');
  assert.strictEqual(env.calls.length, 0);
  assert.strictEqual(env.els.accountMsg.textContent, 'The passwords do not match.');
});

test('profile view without a user redirects to login', () => {
  const env = loadAccount('#/account', () => ({}));
  env.t.views.profile({});
  assert.strictEqual(env.loc.hash, '#/account/login');
});

Promise.all(pending).then(() => {
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed ? 1 : 0);
});
