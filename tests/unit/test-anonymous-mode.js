/* The anonymous static build (deployment.js auth:false on another origin)
 * sends no cookies, so nothing in it may offer a login or an admin action.
 * roles.js turns every account feature off there; auth.js links to the
 * classic site's login instead; the account and admin pages say where to go. */
'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const { loadApiBase } = require('./api-base-sandbox');

const ROOT = path.join(__dirname, '..', '..');
const src = (f) => fs.readFileSync(path.join(ROOT, f), 'utf8');
const ANON = { channel: 'beta', apiBase: 'https://analyzer.example', auth: false, classicUrl: 'https://analyzer.example' };

let passed = 0, failed = 0;
let chain = Promise.resolve();
function test(name, fn) {
  chain = chain.then(fn).then(
    () => { passed++; console.log('  ✅ ' + name); },
    (e) => { failed++; console.log('  ❌ ' + name + ': ' + (e && e.stack || e)); });
}
function section(title) { chain = chain.then(() => console.log(title)); }

// beta-ui.js in the same sandbox, for CSBeta.classicOnlyHtml.
function loadBetaUi(ctx) { vm.runInContext(src('public/beta-ui.js'), ctx); }

section('roles.js');

async function rolesWith(deployment, cfg) {
  const ctx = {
    console, Date, Infinity,
    document: { readyState: 'complete', createElement: () => ({ id: '', style: {} }), head: { appendChild() {} },
      getElementById: () => null, querySelector: () => null, querySelectorAll: () => [], addEventListener() {},
      documentElement: { style: { setProperty() {} } } },
    location: { protocol: 'https:', host: 'beta.example', hash: '' },
    fetch: () => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(cfg) }),
  };
  ctx.window = {};
  vm.createContext(ctx);
  loadApiBase(ctx, deployment);
  vm.runInContext(src('public/roles.js'), ctx);
  await ctx.window.MeshConfigReady;
  return ctx.window;
}
const UM_ON = { userManagement: { enabled: true, channelProposals: true, notifications: true, observerLogins: true } };

test('the embedded UI keeps user management as the server reports it', async () => {
  const win = await rolesWith(undefined, UM_ON);
  assert.strictEqual(win.MC_USER_MGMT.enabled, true);
  assert.strictEqual(win.MC_USER_MGMT.channelProposals, true);
  assert.strictEqual(win.MC_ACCOUNTS_ON_CLASSIC, false);
});

test('an anonymous build turns every account feature off, and remembers classic has them', async () => {
  const win = await rolesWith(ANON, UM_ON);
  assert.strictEqual(win.MC_USER_MGMT, null);
  assert.strictEqual(win.MC_ACCOUNTS_ON_CLASSIC, true);
});

test('an anonymous build on a server without accounts has nothing to link to', async () => {
  const win = await rolesWith(ANON, {});
  assert.strictEqual(win.MC_USER_MGMT, null);
  assert.strictEqual(win.MC_ACCOUNTS_ON_CLASSIC, false);
});

test('a static build with logins (auth: true) keeps user management', async () => {
  const win = await rolesWith(Object.assign({}, ANON, { auth: true }), UM_ON);
  assert.strictEqual(win.MC_USER_MGMT.enabled, true);
  assert.strictEqual(win.MC_ACCOUNTS_ON_CLASSIC, false);
});

section('auth.js');

function authWith(deployment, accountsOnClassic) {
  const inserted = [];
  const events = [];
  const fetches = [];
  const mkEl = () => {
    const el = { id: '', className: '', innerHTML: '', title: '', href: '', children: [] };
    el.appendChild = (c) => { el.children.push(c); return c; };
    return el;
  };
  const doc = {
    getElementById: (id) => inserted.find((e) => e.id === id) || null,
    querySelector: (sel) => (sel === '.top-nav .nav-right' ? { insertBefore(el) { inserted.push(el); } } : null),
    createElement: mkEl,
    addEventListener() {},
  };
  const win = { MC_USER_MGMT: null, MC_ACCOUNTS_ON_CLASSIC: accountsOnClassic, MeshConfigReady: Promise.resolve(),
    addEventListener() {}, dispatchEvent: (e) => events.push(e.type) };
  const ctx = { window: win, document: doc, console, Promise, JSON, String, Object, setTimeout, clearTimeout,
    location: { protocol: 'https:', host: 'beta.example', hash: '#/nodes' },
    CustomEvent: function (type) { this.type = type; },
    fetch: (u) => { fetches.push(u); return Promise.reject(new Error('no fetch expected')); } };
  vm.createContext(ctx);
  loadApiBase(ctx, deployment);
  vm.runInContext(src('public/auth.js'), ctx);
  return { win, inserted, events, fetches };
}

test('anonymous build on a server with accounts: "Log in on classic", and no session request', async () => {
  const env = authWith(ANON, true);
  await env.win.CSAuth.ready();
  assert.strictEqual(env.win.CSAuth.isEnabled(), false);
  assert.deepStrictEqual(env.fetches, []);
  const wrap = env.inserted.find((e) => e.id === 'accountWrap');
  assert.ok(wrap, 'nav control rendered');
  const link = wrap.children[0];
  assert.strictEqual(link.href, 'https://analyzer.example/#/account/login');
  assert.ok(link.innerHTML.includes('Log in on classic'), link.innerHTML);
  assert.deepStrictEqual(env.events, ['cs-auth-control-added']);
});

test('anonymous build without a classic URL shows no login control at all', async () => {
  const env = authWith(Object.assign({}, ANON, { classicUrl: null }), true);
  await env.win.CSAuth.ready();
  assert.strictEqual(env.inserted.length, 0);
  assert.deepStrictEqual(env.fetches, []);
});

test('nothing renders when classic has no accounts either', async () => {
  const env = authWith(ANON, false);
  await env.win.CSAuth.ready();
  assert.strictEqual(env.inserted.length, 0);
});

section('account.js and admin.js');

function pageWith(file, name, deployment) {
  const pages = {};
  const ctx = { console, Promise, String, Object, URLSearchParams,
    location: { protocol: 'https:', host: 'beta.example', hash: '#/' + name },
    document: { readyState: 'complete', getElementById: () => null, querySelector: () => null, addEventListener() {} },
    history: { replaceState() {} }, escapeHtml: (s) => String(s), viewGeneration: () => 0,
    registerPage: (n, m) => { pages[n] = m; },
    CSAuth: { ready: () => { throw new Error('anonymous pages must not wait for a session'); } },
    addEventListener() {} };
  ctx.window = ctx;
  vm.createContext(ctx);
  loadApiBase(ctx, deployment);
  loadBetaUi(ctx);
  vm.runInContext(src(file), ctx);
  return pages[name];
}

test('#/account on an anonymous build points to the classic login', () => {
  const app = { innerHTML: '' };
  pageWith('public/account.js', 'account', ANON).init(app, 'login');
  assert.ok(app.innerHTML.includes('Logging in is available on the classic site.'), app.innerHTML);
  assert.ok(app.innerHTML.includes('href="https://analyzer.example/#/account"'), app.innerHTML);
});

test('#/admin on an anonymous build points to classic', async () => {
  const app = { innerHTML: '' };
  await pageWith('public/admin.js', 'admin', ANON).init(app);
  assert.ok(app.innerHTML.includes('Administration is available on the classic site.'), app.innerHTML);
});

chain.then(() => {
  console.log(`\nanonymous mode: ${passed} passed, ${failed} failed`);
  if (failed) process.exit(1);
});
