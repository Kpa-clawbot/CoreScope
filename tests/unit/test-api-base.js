/* public/api-base.js and public/deployment.js: where every /api request and
 * the WebSocket go, and what each request may carry, for the embedded UI and
 * for a static build on another origin. Also api()'s two cross-origin errors
 * in public/app.js: a blocked request (network_or_cors) and an HTML answer
 * from a wrong API base (not_json). */
'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const { loadApiBase } = require('./api-base-sandbox');

const ROOT = path.join(__dirname, '..', '..');
let passed = 0, failed = 0;
let chain = Promise.resolve();
function test(name, fn) {
  chain = chain.then(fn).then(
    () => { passed++; console.log('  ✅ ' + name); },
    (e) => { failed++; console.log('  ❌ ' + name + ': ' + (e && e.stack || e)); });
}
function section(title) { chain = chain.then(() => console.log(title)); }

function sandbox(deployment, loc) {
  const calls = [];
  const ctx = {
    location: loc || { protocol: 'http:', host: 'analyzer.test', hash: '#/packets' },
    fetch(url, opts) { calls.push({ url, opts }); return Promise.resolve({ ok: true, status: 200 }); },
  };
  ctx.window = ctx;
  const api = loadApiBase(ctx, deployment);
  return { api, calls };
}

section('deployment.js');

test('the embedded UI ships the classic descriptor: same origin, logins on', () => {
  const ctx = {};
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(path.join(ROOT, 'public/deployment.js'), 'utf8'), ctx);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(ctx.CS_DEPLOYMENT)),
    { channel: 'classic', commit: null, version: null, apiBase: '', auth: true, classicUrl: null });
});

section('api-base.js: classic (same origin)');

test('URLs stay relative and the WebSocket is on this host', () => {
  const { api } = sandbox();
  assert.strictEqual(api.url('/api/nodes?limit=50'), '/api/nodes?limit=50');
  assert.strictEqual(api.wsUrl('/ws'), 'ws://analyzer.test/ws');
  assert.strictEqual(api.crossOrigin, false);
  assert.strictEqual(api.anonymous, false);
  assert.strictEqual(api.channel, 'classic');
});

test('the WebSocket follows the page to wss on https', () => {
  const { api } = sandbox({}, { protocol: 'https:', host: 'analyzer.test:8443', hash: '' });
  assert.strictEqual(api.wsUrl('/ws'), 'wss://analyzer.test:8443/ws');
});

test('fetch keeps the caller options, adds same-origin credentials and does not mutate the options', async () => {
  const { api, calls } = sandbox();
  const opts = { method: 'PUT', headers: { 'X-CS-CSRF': 't' } };
  await api.fetch('/api/account', opts);
  assert.strictEqual(calls[0].url, '/api/account');
  assert.strictEqual(calls[0].opts.method, 'PUT');
  assert.strictEqual(calls[0].opts.headers['X-CS-CSRF'], 't');
  assert.strictEqual(calls[0].opts.credentials, 'same-origin');
  assert.strictEqual(opts.credentials, undefined);
});

test('a path that is not absolute is a programming error', () => {
  const { api } = sandbox();
  assert.throws(() => api.url('api/nodes'), /must start with \//);
  assert.throws(() => api.url('https://elsewhere.test/api'), /must start with \//);
});

test('classicHref is null without a classic URL', () => {
  const { api } = sandbox();
  assert.strictEqual(api.classicHref(), null);
});

section('api-base.js: static build with logins (auth: true)');

test('URLs and the WebSocket go to the API origin, and cookies ride along', async () => {
  const { api, calls } = sandbox({ channel: 'beta', apiBase: 'https://analyzer.example/', auth: true });
  assert.strictEqual(api.base, 'https://analyzer.example');
  assert.strictEqual(api.url('/api/stats'), 'https://analyzer.example/api/stats');
  assert.strictEqual(api.wsUrl('/ws'), 'wss://analyzer.example/ws');
  assert.strictEqual(api.crossOrigin, true);
  assert.strictEqual(api.anonymous, false);
  await api.fetch('/api/stats');
  assert.strictEqual(calls[0].opts.credentials, 'include');
});

section('api-base.js: anonymous static build (auth: false)');

test('no request carries cookies: the embed CORS tier would block an include request', async () => {
  const { api, calls } = sandbox({ channel: 'beta', apiBase: 'https://analyzer.example', auth: false });
  assert.strictEqual(api.anonymous, true);
  await api.fetch('/api/stats');
  assert.strictEqual(calls[0].url, 'https://analyzer.example/api/stats');
  assert.strictEqual(calls[0].opts.credentials, 'omit');
});

test('an explicit credentials option still wins', async () => {
  const { api, calls } = sandbox({ apiBase: 'https://analyzer.example', auth: false });
  await api.fetch('/api/stats', { credentials: 'same-origin' });
  assert.strictEqual(calls[0].opts.credentials, 'same-origin');
});

test('postJSON sends a JSON body as text/plain, so the POST needs no preflight', async () => {
  const { api, calls } = sandbox({ apiBase: 'https://analyzer.example', auth: false });
  await api.postJSON('/api/decode', { hex: 'ab' });
  const c = calls[0];
  assert.strictEqual(c.url, 'https://analyzer.example/api/decode');
  assert.strictEqual(c.opts.method, 'POST');
  assert.strictEqual(c.opts.headers['Content-Type'], 'text/plain;charset=UTF-8');
  assert.deepStrictEqual(JSON.parse(c.opts.body), { hex: 'ab' });
  assert.strictEqual(c.opts.credentials, 'omit');
});

test('postJSON is the same request on the embedded UI', async () => {
  const { api, calls } = sandbox();
  await api.postJSON('/api/paths/inspect', { prefixes: ['aa'] });
  assert.strictEqual(calls[0].url, '/api/paths/inspect');
  assert.strictEqual(calls[0].opts.headers['Content-Type'], 'text/plain;charset=UTF-8');
  assert.strictEqual(calls[0].opts.credentials, 'same-origin');
});

test('classicHref opens the current or a given route on the classic site', () => {
  const { api } = sandbox({ apiBase: 'https://analyzer.example', auth: false, classicUrl: 'https://analyzer.example/' },
    { protocol: 'https:', host: 'beta.example', hash: '#/nodes/abc?section=x' });
  assert.strictEqual(api.classicHref(), 'https://analyzer.example/#/nodes/abc?section=x');
  assert.strictEqual(api.classicHref('#/account/login'), 'https://analyzer.example/#/account/login');
  assert.strictEqual(api.classicHref('/packets'), 'https://analyzer.example/#/packets');
});

section('app.js api(): cross-origin failures');

function appCtx(deployment, fetchImpl) {
  const notices = [];
  const ctx = {
    console, Date, Math, Promise, Error, TypeError, isFinite, parseInt, JSON, Map, Set,
    performance: { now: () => 0 },
    document: { readyState: 'complete', body: { appendChild() {} }, createElement: () => ({ style: {} }) },
    location: { protocol: 'https:', host: 'beta.example', hash: '' },
    fetch: fetchImpl,
    setTimeout: (fn) => { fn(); return 0; },
    clearTimeout() {}, setInterval: () => 0, clearInterval() {},
  };
  ctx.window = ctx;
  ctx.CSAuth = { notify: (m) => notices.push(m) };
  loadApiBase(ctx, deployment);
  try { vm.runInContext(fs.readFileSync(path.join(ROOT, 'public/app.js'), 'utf8'), ctx); } catch (_) { /* only api() is needed */ }
  return { ctx, notices };
}
const json = (body, ct) => ({ ok: true, status: 200, headers: { get: (k) => (k.toLowerCase() === 'content-type' ? ct : null) }, json: async () => body });

test('a blocked request on another origin becomes network_or_cors, with one notice', async () => {
  const { ctx, notices } = appCtx({ apiBase: 'https://analyzer.example', auth: false }, (url) =>
    /config\/cache/.test(url) ? Promise.resolve(json({})) : Promise.reject(new TypeError('Failed to fetch')));
  let err = null;
  try { await ctx.api('/stats'); } catch (e) { err = e; }
  assert.ok(err, 'api() must reject');
  assert.strictEqual(err.code, 'network_or_cors');
  try { await ctx.api('/nodes'); } catch (_) { /* second failure */ }
  assert.deepStrictEqual(notices, ["Can't reach the CoreScope API"]);
});

test('the same failure on the embedded UI keeps its original error', async () => {
  const { ctx, notices } = appCtx({}, (url) =>
    /config\/cache/.test(url) ? Promise.resolve(json({})) : Promise.reject(new TypeError('Failed to fetch')));
  let err = null;
  try { await ctx.api('/stats'); } catch (e) { err = e; }
  assert.ok(err instanceof ctx.TypeError || err.name === 'TypeError');
  assert.strictEqual(err.code, undefined);
  assert.deepStrictEqual(notices, []);
});

test('an HTML answer (a static host page) becomes not_json, not a parse error', async () => {
  const { ctx, notices } = appCtx({ apiBase: 'https://wrong.example', auth: false }, async () =>
    json(null, 'text/html; charset=utf-8'));
  let err = null;
  try { await ctx.api('/stats'); } catch (e) { err = e; }
  assert.ok(err, 'api() must reject');
  assert.strictEqual(err.code, 'not_json');
  assert.strictEqual(err.status, 200);
  assert.deepStrictEqual(notices, ['Server returned an unexpected response']);
});

test('JSON answers parse, including handlers that leave Content-Type to sniffing', async () => {
  for (const ct of ['application/json', 'application/json; charset=utf-8', 'text/plain; charset=utf-8', null]) {
    const { ctx } = appCtx({}, async () => json({ ok: ct }, ct));
    const data = await ctx.api('/stats');
    assert.strictEqual(data.ok, ct, 'Content-Type ' + ct);
  }
});

chain.then(() => {
  console.log(`\napi-base: ${passed} passed, ${failed} failed`);
  if (failed) process.exit(1);
});
