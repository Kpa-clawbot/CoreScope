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

// CSAuth.say and CSAuth.errText come from the real auth.js, bound to ctx
// (say reads ctx.document).
function loadAuthHelpers(ctx) {
  const src = fs.readFileSync(path.join(ROOT, 'public/auth.js'), 'utf8');
  const say = src.match(/function say\(id, text, ok\) \{[\s\S]*?\n  \}/);
  const err = src.match(/function errText\(r\) \{.*\}/);
  assert(say && err, 'say/errText not found in auth.js');
  return { say: vm.runInContext('(' + say[0] + ')', ctx), errText: vm.runInContext('(' + err[0] + ')', ctx) };
}

function makeEnv(routes) {
  const els = {};
  const events = [];
  const mkEl = (id) => ({
    id: id || '', className: '', innerHTML: '', textContent: '', hidden: false,
    classList: { add() {}, remove() {} },
    style: {}, listeners: {}, setAttribute() {}, appendChild() {},
    addEventListener(t, f) { this.listeners[t] = f; },
  });
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
    addEventListener(t, f) { (this.listeners[t] = this.listeners[t] || []).push(f); },
    innerWidth: 1000, listeners: {},
    dispatchEvent(e) { events.push(e); },
    MC_USER_MGMT: { enabled: true },
    MeshConfigReady: Promise.resolve(),
  };
  win.window = win;
  const loc = { hash: '#/account' };
  const ctx = {
    window: win, document: doc, console, Promise, JSON, String, Object, location: loc,
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
  return { win, els, events, calls, loc };
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

test('account menu opens as fixed, positioned from the toggle rect, and follows a resize', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : null);
  await env.win.CSAuth.ready();
  const toggle = { id: 'accountToggle', listeners: {}, setAttribute() {}, addEventListener(t, f) { this.listeners[t] = f; },
    getBoundingClientRect: () => ({ bottom: 50, right: 900 }) };
  const menu = { id: 'accountMenu', hidden: true, style: {}, listeners: {}, addEventListener() {} };
  env.els.accountToggle = toggle; env.els.accountMenu = menu; env.els.accountLogout = { addEventListener() {} };
  env.win.CSAuth._test.renderControl();
  toggle.listeners.click({ stopPropagation() {} });
  assert.strictEqual(menu.hidden, false);
  assert.strictEqual(menu.style.top, '54px');
  assert.strictEqual(menu.style.right, '100px');
  assert.strictEqual(menu.style.left, 'auto');
  toggle.getBoundingClientRect = () => ({ bottom: 60, right: 800 });
  env.win.listeners.resize.forEach((f) => f());
  assert.strictEqual(menu.style.top, '64px');
  assert.strictEqual(menu.style.right, '200px');
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

test('Admin menu entry is shown to admins only', async () => {
  const user = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : null);
  await user.win.CSAuth.ready();
  assert(user.els.accountWrap.innerHTML.indexOf('#/admin') === -1);
  const admin = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: Object.assign({}, ME, { role: 'admin' }) } : null);
  await admin.win.CSAuth.ready();
  assert(admin.els.accountWrap.innerHTML.indexOf('<a role="menuitem" href="#/admin">Admin</a>') !== -1, admin.els.accountWrap.innerHTML);
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

test('logout posts with CSRF, moves to the next view, then clears the user', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : { status: 200, body: { ok: true } });
  await env.win.CSAuth.ready();
  env.calls.length = 0;
  let hashAtClear = null;
  env.win.dispatchEvent = (e) => { if (e.type === 'cs-auth-changed') hashAtClear = env.loc.hash; };
  const r = await env.win.CSAuth.logout('#/account/login');
  assert.strictEqual(r.ok, true);
  assert.strictEqual(env.calls[0].url, '/api/auth/logout');
  assert.strictEqual(env.calls[0].opts.method, 'POST');
  assert.strictEqual(env.calls[0].opts.headers['X-CS-CSRF'], 'tok123');
  assert.strictEqual(env.win.CS_USER, null);
  assert.strictEqual(hashAtClear, '#/account/login', 'hash must change before cs-auth-changed fires');
});

test('a refused logout keeps the user and the view', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : { status: 403, body: { error: 'request origin not allowed' } });
  await env.win.CSAuth.ready();
  const r = await env.win.CSAuth.logout('#/account/login');
  assert.strictEqual(r.status, 403);
  assert.strictEqual(env.win.CS_USER.displayName, 'Ann');
  assert.strictEqual(env.loc.hash, '#/account');
});

test('logout handler: cancel keeps the session and posts nothing', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : { status: 200, body: { ok: true } });
  await env.win.CSAuth.ready();
  env.calls.length = 0;
  env.win.CSAuth.setLogoutHandler(() => Promise.resolve({ cancel: true }));
  const r = await env.win.CSAuth.logout('#/account/login');
  assert.strictEqual(r.cancelled, true);
  assert.strictEqual(r.ok, false);
  assert.strictEqual(env.calls.length, 0);
  assert.strictEqual(env.win.CS_USER.displayName, 'Ann');
  assert.strictEqual(env.loc.hash, '#/account');
});

test('logout handler: afterLogout runs after the POST, before the user is cleared', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : { status: 200, body: { ok: true } });
  await env.win.CSAuth.ready();
  env.calls.length = 0;
  let userAtAfter = 'not called', postedBefore = false;
  env.win.CSAuth.setLogoutHandler(() => Promise.resolve({ afterLogout() { userAtAfter = env.win.CS_USER; postedBefore = env.calls.length === 1; } }));
  const r = await env.win.CSAuth.logout('#/account/login');
  assert.strictEqual(r.ok, true);
  assert.strictEqual(postedBefore, true);
  assert.strictEqual(userAtAfter.displayName, 'Ann');
  assert.strictEqual(env.win.CS_USER, null);
});

// M4 (final review): a second Log out click while the dialog is open must
// not stack a second dialog or send a second POST.
test('logout: a second call while one is running opens no second dialog', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : { status: 200, body: { ok: true } });
  await env.win.CSAuth.ready();
  env.calls.length = 0;
  let dialogs = 0, choose;
  env.win.CSAuth.setLogoutHandler(() => { dialogs++; return new Promise((r) => { choose = r; }); });
  const first = env.win.CSAuth.logout('#/account/login');
  const second = env.win.CSAuth.logout('#/account/login');
  await new Promise((r) => setTimeout(r, 0));
  assert.strictEqual(dialogs, 1);
  assert.strictEqual((await second).cancelled, true);
  choose({});
  assert.strictEqual((await first).ok, true);
  assert.strictEqual(env.calls.filter((c) => c.url === '/api/auth/logout').length, 1);
  // Finished: the next logout runs again.
  const third = env.win.CSAuth.logout('#/account/login');
  assert.strictEqual(dialogs, 2);
  choose({ cancel: true });
  assert.strictEqual((await third).cancelled, true);
});

test('logout handler: a refused POST does not run afterLogout', async () => {
  const env = makeEnv((u) => u === '/api/auth/me' ? { status: 200, body: ME } : { status: 403, body: { error: 'no' } });
  await env.win.CSAuth.ready();
  let ran = false;
  env.win.CSAuth.setLogoutHandler(() => Promise.resolve({ afterLogout() { ran = true; } }));
  await env.win.CSAuth.logout('#/account/login');
  assert.strictEqual(ran, false);
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

function loadAccount(hash, routes, extraWin) {
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
  const logouts = [];
  let logoutResult = { ok: true, status: 200, data: { ok: true } };
  let override = null;
  const loc = { hash, replace(h) { loc.hash = h; } };
  const replaced = [];
  const listeners = {};
  const CSAuth = {
    request(method, p, body) { calls.push({ method, p, body }); return override ? override(method, p, body) : Promise.resolve(routes(p, body)); },
    setUser(u) { user.current = u; },
    user() { return user.current; },
    logout(next) { logouts.push(next); return logoutResult instanceof Error ? Promise.reject(logoutResult) : Promise.resolve(logoutResult); },
    ready() { return Promise.resolve(); }, isEnabled() { return true; },
    notify() {}, refreshMe() { return Promise.resolve(); },
  };
  const win = Object.assign({ CSAuth, addEventListener(t, fn) { listeners[t] = fn; } }, extraWin || {});
  const ctx = { window: win, document: doc, CSAuth, location: loc, URLSearchParams, Promise, String, Object,
    history: { replaceState(a, b, h) { replaced.push(h); loc.hash = h; } },
    escapeHtml: loadEscapeHtml(), registerPage(n, m) { pages[n] = m; }, console };
  vm.createContext(ctx);
  Object.assign(CSAuth, loadAuthHelpers(ctx));
  vm.runInContext(fs.readFileSync(path.join(ROOT, 'public/account.js'), 'utf8'), ctx);
  return { setRequest(f) { override = f; }, doc, t: ctx.window.CSAccount._test, els, calls, loc, user, pages, logouts, replaced,
    setLogout(r) { logoutResult = r; },
    fire(detail) { user.current = detail; if (listeners['cs-auth-changed']) listeners['cs-auth-changed']({ detail }); } };
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
  assert.strictEqual(env.loc.hash, '#/account/activate'); // token stripped, form kept
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

test('activate without a token renders no form and no password input', () => {
  const env = loadAccount('#/account/activate', () => ({}));
  let html = '';
  env.t.views.activate({ set innerHTML(v) { html = v; } });
  assert(html.indexOf('<form') === -1 && html.indexOf('type="password"') === -1, html);
  assert(html.indexOf('href="#/account/login"') !== -1);
});

test('no account input carries a name attribute (no native submit leak)', () => {
  const env = loadAccount('#/account', () => ({}));
  assert(!/<input[^>]* name=/.test(env.t.profileHtml({ email: 'a', role: 'user' })));
  let html = '';
  env.t.views.register({ set innerHTML(v) { html = v; } });
  assert(!/<input[^>]* name=/.test(html));
});

const flush = () => new Promise((r) => setTimeout(r, 5));

const render = (env, name) => { let html = ''; env.t.views[name]({ set innerHTML(v) { html = v; } }); return html; };

test('register success goes to check-mail, which shows the escaped email and no form', async () => {
  const payload = '<img src=x onerror=alert(1)>@e.c';
  const env = loadAccount('#/account/register', () => ({ ok: true, status: 200, data: { message: 'x' } }));
  env.t.views.register({ set innerHTML(v) {} });
  env.doc.getElementById('regEmail').value = payload;
  await submitForm(env, 'registerForm');
  await flush();
  assert.strictEqual(env.loc.hash, '#/account/check-mail');
  const html = render(env, 'check-mail');
  assert(html.indexOf('Check your mailbox') !== -1, html);
  assert(html.indexOf('<form') === -1, 'form present');
  assert(html.indexOf('<img') === -1 && html.indexOf('&lt;img') !== -1, html);
  assert(html.indexOf('expires in 48 hours') !== -1);
  assert(html.indexOf('href="#/account/login"') !== -1 && html.indexOf('href="#/account/register"') !== -1);
  assert(html.indexOf('tabindex="-1"') !== -1 && html.indexOf('ph-envelope-simple') !== -1);
  assert(env.els.mailSentHeading, 'heading not looked up for focus');
});

test('check-mail without state (refresh) shows generic text and no address', () => {
  const env = loadAccount('#/account/check-mail', () => ({ ok: true, status: 200, data: {} }));
  const html = render(env, 'check-mail');
  assert(html.indexOf('If the address can be used, we sent you a link. Check your mailbox.') !== -1, html);
  assert(html.indexOf('<strong>') === -1 && html.indexOf('<form') === -1);
});

test('forgot success goes to check-mail with the escaped email and 1 hour text', async () => {
  const payload = '<img src=x onerror=alert(1)>@e.c';
  const env = loadAccount('#/account/forgot', () => ({ ok: true, status: 200, data: { message: 'x' } }));
  env.t.views.forgot({ set innerHTML(v) {} });
  env.doc.getElementById('forgotEmail').value = payload;
  await submitForm(env, 'forgotForm');
  await flush();
  assert.strictEqual(env.loc.hash, '#/account/check-mail');
  const html = render(env, 'check-mail');
  assert(html.indexOf('Check your mailbox') !== -1 && html.indexOf('<form') === -1, html);
  assert(html.indexOf('<img') === -1 && html.indexOf('&lt;img') !== -1, html);
  assert(html.indexOf('expires in 1 hour') !== -1 && html.indexOf('Back to log in') !== -1);
});

test('register 400 keeps the form and shows the error', async () => {
  const env = loadAccount('#/account/register', () => ({ ok: false, status: 400, data: { error: 'Bad password' } }));
  let html = '';
  env.t.views.register({ set innerHTML(v) { html = v; } });
  await submitForm(env, 'registerForm');
  await flush();
  assert(html.indexOf('<form') !== -1 && html.indexOf('Check your mailbox') === -1);
  assert.strictEqual(env.loc.hash, '#/account/register');
  assert.strictEqual(env.els.accountMsg.textContent, 'Bad password');
});


test('a rejected fetch on a profile form shows the error in its own box', async () => {
  const env = loadAccount('#/account', () => ({}));
  env.user.current = { email: 'a@b.c', displayName: 'Ann', role: 'user' };
  let n = 0;
  env.calls.length = 0;
  const req = () => (++n === 1 ? Promise.resolve({ ok: true, status: 200, data: [] }) : Promise.reject(new Error('net')));
  env.setRequest(req);
  env.t.views.profile({ set innerHTML(v) {} });
  await new Promise((r) => setTimeout(r, 5));
  await submitForm(env, 'pwForm');
  assert.strictEqual(env.els.pwMsg.textContent, 'Network error, try again.');
});

test('a rejected sessions load shows the error in sessMsg', async () => {
  const env = loadAccount('#/account', () => ({}));
  env.user.current = { email: 'a@b.c', displayName: 'Ann', role: 'user' };
  env.setRequest(() => Promise.reject(new Error('net')));
  env.t.views.profile({ set innerHTML(v) {} });
  await new Promise((r) => setTimeout(r, 5));
  assert.strictEqual(env.els.sessMsg.textContent, 'Network error, try again.');
});

test('profile view without a user redirects to login', () => {
  const env = loadAccount('#/account', () => ({}));
  env.t.views.profile({});
  assert.strictEqual(env.loc.hash, '#/account/login');
});

test('profile view: Log out button logs out to the login view', async () => {
  const env = loadAccount('#/account', () => ({ ok: true, status: 200, data: [] }));
  env.user.current = { id: 1, email: 'a@b.c', displayName: 'Ann', role: 'user' };
  env.t.views.profile({ set innerHTML(v) {} });
  await env.els.accountPageLogout.handlers.click();
  assert.deepStrictEqual(env.logouts, ['#/account/login']);
});

test('profile view: a refused logout shows the error next to the button', async () => {
  const env = loadAccount('#/account', () => ({ ok: true, status: 200, data: [] }));
  env.user.current = { id: 1, email: 'a@b.c', displayName: 'Ann', role: 'user' };
  env.setLogout({ ok: false, status: 403, data: { error: 'request origin not allowed' } });
  env.t.views.profile({ set innerHTML(v) {} });
  await env.els.accountPageLogout.handlers.click();
  assert.strictEqual(env.els.logoutMsg.textContent, 'request origin not allowed');
  env.setLogout(new Error('net'));
  await env.els.accountPageLogout.handlers.click();
  assert.strictEqual(env.els.logoutMsg.textContent, 'Network error, try again.');
});

test('profile view: a cancelled logout shows no message', async () => {
  const env = loadAccount('#/account', () => ({ ok: true, status: 200, data: [] }));
  env.user.current = { id: 1, email: 'a@b.c', displayName: 'Ann', role: 'user' };
  env.setLogout({ ok: false, cancelled: true, status: 0, data: {} });
  env.t.views.profile({ set innerHTML(v) {} });
  await env.els.accountPageLogout.handlers.click();
  assert.strictEqual(env.doc.getElementById('logoutMsg').textContent, '');
});

test('profile view mounts the settings sync section only when the module is loaded', () => {
  const mounted = [];
  const env = loadAccount('#/account', () => ({ ok: true, status: 200, data: [] }), { CSSettingsSync: { mountSection(el) { mounted.push(el); } } });
  env.user.current = { id: 1, email: 'a@b.c', displayName: 'Ann', role: 'user' };
  const app = { innerHTML: '' };
  env.t.views.profile(app);
  assert(app.innerHTML.indexOf('<h3>Settings sync</h3><div id="syncSection"></div>') !== -1);
  assert.strictEqual(mounted.length, 1);
  assert.strictEqual(mounted[0], env.els.syncSection);
  const without = loadAccount('#/account', () => ({}));
  assert.strictEqual(without.t.profileHtml({ email: 'a', role: 'user', displayName: 'A' }).indexOf('syncSection'), -1);
});

test('profile view: Log out button for everyone, Admin link for admins only', () => {
  const env = loadAccount('#/account', () => ({}));
  const user = env.t.profileHtml({ email: 'a', role: 'user', displayName: 'A' });
  const admin = env.t.profileHtml({ email: 'a', role: 'admin', displayName: 'A' });
  assert(user.indexOf('id="accountPageLogout"') !== -1 && admin.indexOf('id="accountPageLogout"') !== -1);
  assert(user.indexOf('#/admin') === -1);
  assert(admin.indexOf('href="#/admin">Admin</a>') !== -1, admin);
});

test('profile view follows auth changes: logout redirects, another user re-renders', () => {
  const env = loadAccount('#/account', () => ({ ok: true, status: 200, data: [] }));
  env.user.current = { id: 1, email: 'a@b.c', displayName: 'Ann', role: 'user' };
  let renders = 0;
  env.t.views.profile({ set innerHTML(v) { renders++; } });
  env.fire({ id: 1, email: 'a@b.c', displayName: 'Ann B', role: 'user' }); // same user (name saved): no re-render
  assert.strictEqual(renders, 1);
  env.fire({ id: 2, email: 'b@b.c', displayName: 'Bob', role: 'user' });
  assert.strictEqual(renders, 2);
  assert.strictEqual(env.els.profName.value, 'Bob');
  env.fire(null);
  assert.strictEqual(env.loc.hash, '#/account/login');
});

test('auth changes on other views are ignored', () => {
  const env = loadAccount('#/account', () => ({ ok: true, status: 200, data: [] }));
  env.user.current = { id: 1, email: 'a@b.c', displayName: 'Ann', role: 'user' };
  env.t.views.profile({ set innerHTML(v) {} });
  env.loc.hash = '#/home';
  env.fire(null);
  assert.strictEqual(env.loc.hash, '#/home');
});

test('unknown and inherited view names fall back to the profile view', async () => {
  for (const name of ['constructor', '__proto__', 'toString', 'nope']) {
    const env = loadAccount('#/account/' + name, () => ({}));
    env.pages.account.init({ set innerHTML(v) {} }, name);
    await tick();
    assert.strictEqual(env.loc.hash, '#/account/login', name + ' did not reach the profile view');
  }
});

test('tokens are stripped from the hash once read', async () => {
  const act = loadAccount('#/account/activate?token=T0K', () => ({ ok: true, status: 200, data: { id: 1 } }));
  act.t.views.activate({ set innerHTML(v) {} });
  assert.strictEqual(act.loc.hash, '#/account/activate');
  await submitForm(act, 'activateForm');
  assert.strictEqual(act.calls[0].body.token, 'T0K');
  const reset = loadAccount('#/account/reset?token=R1', () => ({ ok: true, status: 200, data: { message: 'ok' } }));
  reset.t.views.reset({ set innerHTML(v) {} });
  assert.strictEqual(reset.loc.hash, '#/account/reset');
  const conf = loadAccount('#/account/confirm-email?token=C1', () => ({ ok: true, status: 200, data: { message: 'ok' } }));
  conf.t.views['confirm-email']({ set innerHTML(v) {} });
  assert.strictEqual(conf.loc.hash, '#/account/confirm-email');
  assert.strictEqual(conf.calls[0].body.token, 'C1');
});

test('reset success clears the client user (the reset ended every session)', async () => {
  const env = loadAccount('#/account/reset?token=T', () => ({ ok: true, status: 200, data: { message: 'Password changed.' } }));
  env.user.current = { id: 1 };
  env.t.views.reset({ set innerHTML(v) {} });
  env.doc.getElementById('resetPassword').value = 'abcdefghijkl';
  env.doc.getElementById('resetPassword2').value = 'abcdefghijkl';
  await submitForm(env, 'resetForm');
  assert.strictEqual(env.user.current, null);
  assert.strictEqual(env.els.accountMsg.textContent, 'Password changed.');
});

test('activate 410 for an already active account offers no registration link', async () => {
  const env = loadAccount('#/account/activate?token=T', () => ({ ok: false, status: 410, data: { error: 'this account is already activated, log in instead' } }));
  env.t.views.activate({ set innerHTML(v) {} });
  await submitForm(env, 'activateForm');
  assert.strictEqual(env.els.renewLink, undefined);
  assert.strictEqual(env.els.accountMsg.textContent, 'this account is already activated, log in instead');
});

console.log('admin-users.js');

function loadAdmin(hash, routes) {
  const els = {};
  const mk = (id) => {
    const el = { id, value: '', checked: false, textContent: '', innerHTML: '', hidden: false, handlers: {}, cls: {}, focused: 0 };
    el.classList = { toggle(c, on) { el.cls[c] = !!on; } };
    el.addEventListener = (t, fn) => { el.handlers[t] = fn; };
    el.focus = () => { el.focused++; };
    el.querySelector = (sel) => els[id + ' ' + sel] || (els[id + ' ' + sel] = mk(id + ' ' + sel));
    return el;
  };
  const doc = {
    getElementById(id) { return els[id] || (els[id] = mk(id)); },
    querySelector(sel) { return els[sel] || (els[sel] = mk(sel)); },
  };
  const calls = [];
  const replaced = [];
  const loc = { hash };
  let me = { id: 1, role: 'admin' };
  let refreshed = 0;
  const CSAuth = {
    isAdmin() { return !!me && me.role === 'admin'; },
    user() { return me; },
    request(method, p, body) { calls.push({ method, p, body }); return Promise.resolve(routes(method, p, body)); },
    refreshMe() { refreshed++; me = { id: 1, role: 'user' }; return Promise.resolve(); },
  };
  const ctx = { window: { CSAuth }, document: doc, CSAuth, location: loc, URLSearchParams, Promise, String,
    history: { replaceState(a, b, h) { replaced.push(h); loc.hash = h; } },
    confirm() { return true; }, debounce(fn) { return fn; },
    escapeHtml: loadEscapeHtml(), console };
  vm.createContext(ctx);
  Object.assign(CSAuth, loadAuthHelpers(ctx));
  vm.runInContext(fs.readFileSync(path.join(ROOT, 'public/admin-users.js'), 'utf8'), ctx);
  const app = { innerHTML: '', querySelector() { return els.umPage || (els.umPage = mk('umPage')); } };
  const um = ctx.window.CSAdminUsers;
  return { t: um._test, um, els, calls, replaced, loc, app, refreshed: () => refreshed };
}
const tick = () => new Promise((r) => setTimeout(r, 5));
const U = (o) => Object.assign({ id: 2, email: 'u@x.y', displayName: 'U', role: 'user', status: 'active', configAdmin: false }, o);
const OK = (data) => ({ ok: true, status: 200, data: data });
const acts = (env, u, me) => (env.t.actionsFor(u, me).match(/data-act="(\w+)"/g) || []).map((x) => x.slice(10, -1));
const adminEnv = () => loadAdmin('#/admin?tab=users', () => OK([]));

test('actionsFor: pending offers activate/resend, no disable and no role buttons', () => {
  assert.deepStrictEqual(acts(adminEnv(), U({ status: 'pending' }), { id: 1 }), ['detail', 'activate', 'resend', 'delete']);
});
test('actionsFor: active user offers disable, promote, delete', () => {
  assert.deepStrictEqual(acts(adminEnv(), U(), { id: 1 }), ['detail', 'disable', 'promote', 'delete']);
});
test('actionsFor: active admin offers demote', () => {
  assert.deepStrictEqual(acts(adminEnv(), U({ role: 'admin' }), { id: 1 }), ['detail', 'disable', 'demote', 'delete']);
});
test('actionsFor: disabled offers enable, never disable', () => {
  assert.deepStrictEqual(acts(adminEnv(), U({ status: 'disabled' }), { id: 1 }), ['detail', 'enable', 'promote', 'delete']);
});
test('actionsFor: own row has no disable or delete but can change role', () => {
  assert.deepStrictEqual(acts(adminEnv(), U({ id: 1, role: 'admin' }), { id: 1 }), ['detail', 'demote']);
});
test('actionsFor: config admin has no disable, role or delete', () => {
  assert.deepStrictEqual(acts(adminEnv(), U({ role: 'admin', configAdmin: true }), { id: 1 }), ['detail']);
});
test('actionsFor: buttons use account-btn classes', () => {
  const h = adminEnv().t.actionsFor(U(), { id: 1 });
  assert(h.indexOf('class="account-btn account-btn-secondary"') !== -1 && h.indexOf('btn-primary') === -1);
});

test('row and detail rendering escape every dynamic field', () => {
  const env = adminEnv();
  const p = '<img src=x onerror=alert(1)>';
  const row = env.t.rowHtml(U({ id: '1"><b>', email: p, displayName: p, role: p, status: p, lastMail: { lastEvent: p, lastReason: p }, createdAt: p }), { id: 1 });
  assert(row.indexOf('<img') === -1 && row.indexOf('<b>') === -1, 'raw markup in row: ' + row);
  const det = env.t.detailHtml({ user: { id: 1, displayName: p, email: p }, sessions: [],
    mail: [{ id: '3"><i>', purpose: p, to: p, sentAt: p, lastEvent: p, lastReason: p, events: [{ event: p, at: p, reason: p }] }],
    audit: [{ at: p, action: p, actorUserId: p }] });
  assert(det.indexOf('<img') === -1 && det.indexOf('<i>') === -1, 'raw markup in detail: ' + det);
  assert(det.indexOf('&lt;img src=x onerror=alert(1)&gt;') !== -1);
});

test('deep link: filters, bouncing and detail id round-trip through the hash', () => {
  const env = adminEnv();
  const rh = (h) => JSON.parse(JSON.stringify(env.t.readHash(h)));
  assert.deepStrictEqual(rh('#/admin?tab=users&status=pending&role=admin&q=a%20b&id=7'),
    { status: 'pending', role: 'admin', q: 'a b', bouncing: false, id: '7' });
  assert.strictEqual(rh('#/admin?tab=users&bouncing=1').bouncing, true);
  assert.strictEqual(rh('#/admin?tab=users&bouncing=yes').bouncing, false);
  assert.strictEqual(env.t.hashFor({ status: 'pending', role: '', q: 'a b', bouncing: false }, '7'), '#/admin?tab=users&status=pending&q=a+b&id=7');
  assert.strictEqual(env.t.hashFor({ status: '', role: '', q: '', bouncing: true }, null), '#/admin?tab=users&bouncing=1');
  assert.strictEqual(env.t.hashFor({ status: '', role: '', q: '', bouncing: false }, null), '#/admin?tab=users');
});

test('mount reads the hash into the request and detail; filter changes use replaceState', async () => {
  const env = loadAdmin('#/admin?tab=users&status=pending&id=7', (m, p) =>
    OK(p.indexOf('/api/admin/users/7') === 0 ? { user: U({ id: 7 }), sessions: [], mail: [], audit: [] } : []));
  env.um.mount(env.app);
  await tick();
  assert(env.calls.some((c) => c.p === '/api/admin/users?status=pending'), JSON.stringify(env.calls));
  assert(env.calls.some((c) => c.p === '/api/admin/users/7'));
  assert.strictEqual(env.els.umStatus.value, 'pending');
  env.els.umRole.handlers.change({ target: { value: 'admin' } });
  assert.strictEqual(env.loc.hash, '#/admin?tab=users&status=pending&role=admin&id=7');
  assert(env.replaced.length >= 1);
});

test('bouncing filter is applied in the browser and kept in the hash', async () => {
  const env = loadAdmin('#/admin?tab=users&bouncing=1', () => OK([U({ id: 2, email: 'b@x.y', emailBouncing: true }), U({ id: 3, email: 'c@x.y' })]));
  env.um.mount(env.app);
  await tick();
  assert(env.calls.some((c) => c.p === '/api/admin/users?'), JSON.stringify(env.calls));
  assert.strictEqual(env.els.umBouncing.checked, true);
  assert(env.els.umBody.innerHTML.indexOf('b@x.y') !== -1 && env.els.umBody.innerHTML.indexOf('c@x.y') === -1, env.els.umBody.innerHTML);
  env.els.umBouncing.handlers.change({ target: { checked: false } });
  await tick();
  assert.strictEqual(env.loc.hash, '#/admin?tab=users');
  assert(env.els.umBody.innerHTML.indexOf('c@x.y') !== -1);
});

test('a rejected list fetch shows an error in umMsg', async () => {
  const env = loadAdmin('#/admin?tab=users', () => Promise.reject(new Error('net')));
  env.um.mount(env.app);
  await tick();
  assert.strictEqual(env.els.umMsg.textContent, 'Network error, try again.');
});

test('a rejected action shows an error in umMsg', async () => {
  let n = 0;
  const env = loadAdmin('#/admin?tab=users', () => (++n === 1 ? OK([]) : Promise.reject(new Error('net'))));
  env.um.mount(env.app);
  await tick();
  env.els.umPage.handlers.click({ target: { closest: () => ({ getAttribute: (a) => ({ 'data-act': 'disable', 'data-id': '2' })[a] }) } });
  await tick();
  assert.strictEqual(env.els.umMsg.textContent, 'Network error, try again.');
});

test('demoting yourself refreshes the session and leaves the page', async () => {
  const env = loadAdmin('#/admin?tab=users', () => OK([]));
  env.um.mount(env.app);
  await tick();
  env.els.umPage.handlers.click({ target: { closest: () => ({ getAttribute: (a) => ({ 'data-act': 'demote', 'data-id': '1' })[a] }) } });
  await tick();
  assert.strictEqual(env.refreshed(), 1);
  assert.strictEqual(env.loc.hash, '#/account');
});

test('readHash accepts only numeric ids and known status and role values', () => {
  const env = adminEnv();
  const h = (s) => JSON.parse(JSON.stringify(env.t.readHash('#/admin?tab=users&' + s)));
  assert.deepStrictEqual(h('status=bogus&role=root&id=7%3Bx&q=x'), { status: '', role: '', q: 'x', bouncing: false, id: '' });
  assert.deepStrictEqual(h('id=abc'), { status: '', role: '', q: '', bouncing: false, id: '' });
  assert.deepStrictEqual(h('status=disabled&role=user&id=12'), { status: 'disabled', role: 'user', q: '', bouncing: false, id: '12' });
});

const clickAct = (env, act, id) => env.els.umPage.handlers.click({ target: { closest: () => ({ getAttribute: (a) => ({ 'data-act': act, 'data-id': id })[a] }) } });

test('detail panel has a Close button; opening focuses the panel, closing returns focus', async () => {
  const env = loadAdmin('#/admin?tab=users', (m, p) =>
    OK(p.indexOf('/api/admin/users/7') === 0 ? { user: U({ id: 7 }), sessions: [], mail: [], audit: [] } : []));
  assert(env.t.detailHtml({ user: U({ id: 7 }), sessions: [], mail: [], audit: [] }).indexOf('data-act="close"') !== -1);
  env.um.mount(env.app);
  await tick();
  clickAct(env, 'detail', '7');
  await tick();
  assert.strictEqual(env.els.umDetail.hidden, false);
  assert.strictEqual(env.els['umDetail h3'].focused, 1);
  assert.strictEqual(env.loc.hash, '#/admin?tab=users&id=7');
  clickAct(env, 'close', null);
  assert.strictEqual(env.els.umDetail.hidden, true);
  assert.strictEqual(env.loc.hash, '#/admin?tab=users');
  assert.strictEqual(env.els['button[data-act="detail"][data-id="7"]'].focused, 1);
});

test('a deep-linked detail does not steal focus on load', async () => {
  const env = loadAdmin('#/admin?tab=users&id=7', (m, p) =>
    OK(p.indexOf('/api/admin/users/7') === 0 ? { user: U({ id: 7 }), sessions: [], mail: [], audit: [] } : []));
  env.um.mount(env.app);
  await tick();
  assert.strictEqual(env.els.umDetail.hidden, false);
  assert.strictEqual((env.els['umDetail h3'] || { focused: 0 }).focused, 0);
});

test('unmount drops a list response that arrives later', async () => {
  let release;
  const env = loadAdmin('#/admin?tab=users', () => new Promise((r) => { release = r; }));
  env.um.mount(env.app);
  env.um.unmount();
  release(OK([U({ email: 'late@x.y' })]));
  await tick();
  assert.strictEqual(env.els.umBody.innerHTML, '');
});

console.log('perf.js reset');

function loadPerf() {
  const ctx = { window: { addEventListener() {} }, document: { getElementById() { return null; }, addEventListener() {} },
    console, Date, Math, Array, Object, String, Number, JSON, RegExp, Error, Promise, Map, Set,
    parseInt, parseFloat, isNaN, isFinite, setTimeout() {}, clearTimeout() {}, setInterval() { return 0; }, clearInterval() {},
    performance: { now: () => 0 }, registerPage() {} };
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(path.join(ROOT, 'public/perf.js'), 'utf8'), ctx);
  return ctx;
}
function fakeFetch(status) {
  const calls = [];
  const fn = (url, opts) => { calls.push({ url, opts }); return Promise.resolve({ ok: status < 400, status }); };
  fn.calls = calls;
  return fn;
}

test('feature off: reset is the old plain POST, no alert, local stats cleared', async () => {
  const ctx = loadPerf();
  const f = fakeFetch(401);
  const alerts = [];
  const ok = await ctx.resetPerfStats({ MC_USER_MGMT: null, CSAuth: { adminHeaders: () => ({}) } }, f, (m) => alerts.push(m));
  assert.strictEqual(ok, true);
  assert.strictEqual(alerts.length, 0);
  assert.strictEqual(JSON.stringify(f.calls[0]), JSON.stringify({ url: '/api/perf/reset', opts: { method: 'POST' } }));
});

test('feature on: an admin session authorises the reset', async () => {
  const ctx = loadPerf();
  const f = fakeFetch(200);
  const ok = await ctx.resetPerfStats({ MC_USER_MGMT: { enabled: true }, CSAuth: { adminHeaders: () => ({ 'X-CS-CSRF': 'c' }) } }, f, () => {});
  assert.strictEqual(ok, true);
  assert.strictEqual(f.calls[0].opts.headers['X-CS-CSRF'], 'c');
});

test('feature on: a refused reset alerts and keeps the stats', async () => {
  const ctx = loadPerf();
  const alerts = [];
  const ok = await ctx.resetPerfStats({ MC_USER_MGMT: { enabled: true }, CSAuth: { adminHeaders: () => ({}) } }, fakeFetch(403), (m) => alerts.push(m));
  assert.strictEqual(ok, false);
  assert.deepStrictEqual(alerts, ['Reset failed: HTTP 403']);
});

Promise.all(pending).then(() => {
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed ? 1 : 0);
});
