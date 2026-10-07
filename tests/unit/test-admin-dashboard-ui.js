/* Unit tests for the admin area (docs/specs/2026-10-07-admin-dashboard-design.md):
 * public/admin-audit.js, public/admin-overview.js and public/admin.js, each
 * loaded from disk into a vm sandbox (same pattern as
 * test-user-management-ui.js). */
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
const tick = () => new Promise((r) => setTimeout(r, 5));
const src = (f) => fs.readFileSync(path.join(ROOT, f), 'utf8');
const XSS = '<img src=x onerror=alert(1)>';

// escapeHtml comes from the real app.js, not a copy.
function loadEscapeHtml() {
  const m = src('public/app.js').match(/function escapeHtml\(s\) \{[\s\S]*?\n\}/);
  assert(m, 'escapeHtml not found in app.js');
  return vm.runInNewContext('(' + m[0].replace(/^function escapeHtml/, 'function') + ')');
}

// Elements are created on first lookup and remember handlers and content.
function makeDom() {
  const els = {};
  const mk = (id) => {
    const el = { id, value: '', checked: false, textContent: '', innerHTML: '', hidden: false, handlers: {} };
    el.addEventListener = (t, fn) => { el.handlers[t] = fn; };
    el.insertAdjacentHTML = (pos, html) => { el.innerHTML += html; };
    el.querySelector = (sel) => els[id + ' ' + sel] || (els[id + ' ' + sel] = mk(id + ' ' + sel));
    return el;
  };
  const document = {
    visibilityState: 'visible',
    getElementById(id) { return els[id] || (els[id] = mk(id)); },
    querySelector(sel) { return els[sel] || (els[sel] = mk(sel)); },
  };
  return { els, mk, document };
}

// loadTab runs the given public files with window === the sandbox global,
// a fake CSAuth whose request() answers from routes(path), a recording
// history.replaceState and recordable intervals.
function loadTab(files, hash, routes, extra) {
  const dom = makeDom();
  const loc = { hash };
  const replaced = [];
  const calls = [];
  const timers = [];
  const CSAuth = {
    request(method, p) { calls.push(p); return Promise.resolve().then(() => routes(p)); },
    say(id, text, ok) { const el = dom.document.getElementById(id); el.textContent = text; el.ok = !!ok; },
    errText(r) { return (r.data && r.data.error) || ('Request failed (HTTP ' + r.status + ')'); },
  };
  const ctx = Object.assign({
    document: dom.document, location: loc, URLSearchParams, Promise, String, Number, Object, Math, Date, JSON, console, CSAuth,
    history: { replaceState(a, b, h) { replaced.push(h); loc.hash = h; } },
    escapeHtml: loadEscapeHtml(),
    setInterval(fn, ms) { timers.push({ fn, ms, cleared: false }); return timers.length; },
    clearInterval(id) { if (timers[id - 1]) timers[id - 1].cleared = true; },
  }, extra || {});
  ctx.window = ctx;
  vm.createContext(ctx);
  [].concat(files).forEach((f) => vm.runInContext(src(f), ctx));
  return { ctx, dom, els: dom.els, loc, replaced, calls, timers };
}

const NOW = Date.UTC(2026, 9, 7, 12, 0, 0);
const OK = (data) => ({ ok: true, status: 200, data });

console.log('admin-audit.js');

const E = (o) => Object.assign({ id: 10, at: '2026-10-07T10:00:00Z', action: 'user.login.failed', actor: null,
  target: { id: 7, displayName: 'Eve', email: 'eve@example.org' }, detail: { reason: 'wrong_password' } }, o);
const auditEnv = (hash, routes) => loadTab('public/admin-audit.js', hash, routes);
const auditT = () => auditEnv('', () => OK({ entries: [], next: null })).ctx.CSAdminAudit._test;
const rows = (html) => (html.match(/<tr /g) || []).length;

test('readHash keeps only known actions, numeric users and known periods', () => {
  const t = auditT();
  const rh = (h) => JSON.parse(JSON.stringify(t.readHash(h)));
  assert.deepStrictEqual(rh('#/admin?tab=audit&action=user.login.failed&user=12&period=7d'),
    { action: 'user.login.failed', user: '12', period: '7d' });
  assert.deepStrictEqual(rh('#/admin?tab=audit&action=drop&user=1x&period=1y'), { action: '', user: '', period: '' });
  assert.strictEqual(rh('#/admin?tab=audit&action=user.login.*').action, 'user.login.*');
});

test('hashFor writes tab=audit and only the set filters', () => {
  const t = auditT();
  assert.strictEqual(t.hashFor({ action: 'user.login.*', user: '12', period: '' }), '#/admin?tab=audit&action=user.login.*&user=12');
  assert.strictEqual(t.hashFor({ action: '', user: '', period: '30d' }), '#/admin?tab=audit&period=30d');
});

test('apiPath turns the period into from, adds before and the page size', () => {
  const t = auditT();
  const from = encodeURIComponent(new Date(NOW - 864e5).toISOString());
  assert.strictEqual(t.apiPath({ action: 'user.login.failed', user: '12', period: '24h' }, null, NOW),
    '/api/admin/audit?action=user.login.failed&user=12&from=' + from + '&limit=100');
  assert.strictEqual(t.apiPath({ action: '', user: '', period: '' }, 55, NOW), '/api/admin/audit?before=55&limit=100');
});

test('rows escape actions, names, emails and details; deleted and system refs', () => {
  const t = auditT();
  const html = t.rowHtml(E({ action: XSS, actor: { id: 3, displayName: XSS, email: XSS }, detail: { [XSS]: XSS } }));
  assert(html.indexOf('<img') === -1, 'raw markup: ' + html);
  assert(html.indexOf('&lt;img src=x onerror=alert(1)&gt;') !== -1);
  assert.strictEqual(t.refHtml({ id: 5, deleted: true }), '#5 <span class="account-hint">(deleted)</span>');
  assert.strictEqual(t.refHtml(null), '<span class="account-hint">system</span>');
  assert(t.refHtml({ id: 7, displayName: 'Eve', email: 'eve@example.org' }).indexOf('href="#/admin?tab=users&amp;id=7"') !== -1);
  assert.strictEqual(t.detailText({ b: '2', a: '1' }), 'a=1, b=2');
});

test('mount reads the hash, renders rows, and Load more appends with before', async () => {
  const env = auditEnv('#/admin?tab=audit&action=user.login.failed&user=7', (p) =>
    p.indexOf('before=9') !== -1 ? OK({ entries: [E({ id: 8 })], next: null }) : OK({ entries: [E({ id: 10 }), E({ id: 9 })], next: 9 }));
  env.ctx.CSAdminAudit.mount(env.dom.mk('c'));
  await tick();
  assert(env.calls[0].indexOf('action=user.login.failed&user=7') !== -1, env.calls[0]);
  assert.strictEqual(env.els.auditAction.value, 'user.login.failed');
  assert.strictEqual(rows(env.els.auditBody.innerHTML), 2);
  assert(env.els.auditBody.innerHTML.indexOf('data-action="user.login.failed"') !== -1);
  assert.strictEqual(env.els.auditMore.hidden, false);
  env.els.auditMore.handlers.click();
  await tick();
  assert(env.calls[1].indexOf('before=9') !== -1, env.calls[1]);
  assert.strictEqual(rows(env.els.auditBody.innerHTML), 3);
  assert.strictEqual(env.els.auditMore.hidden, true);
});

test('an empty last page keeps the rows and hides Load more', async () => {
  const env = auditEnv('#/admin?tab=audit', (p) =>
    p.indexOf('before=9') !== -1 ? OK({ entries: [], next: null }) : OK({ entries: [E({ id: 10 }), E({ id: 9 })], next: 9 }));
  env.ctx.CSAdminAudit.mount(env.dom.mk('c'));
  await tick();
  env.els.auditMore.handlers.click();
  await tick();
  assert.strictEqual(rows(env.els.auditBody.innerHTML), 2);
  assert(env.els.auditBody.innerHTML.indexOf('No entries match') === -1);
  assert.strictEqual(env.els.auditMore.hidden, true);
});

test('no entries shows a message row', async () => {
  const env = auditEnv('#/admin?tab=audit', () => OK({ entries: [], next: null }));
  env.ctx.CSAdminAudit.mount(env.dom.mk('c'));
  await tick();
  assert(env.els.auditBody.innerHTML.indexOf('No entries match.') !== -1);
});

test('filter changes rewrite the hash with replaceState and reload; the user id input is validated', async () => {
  const env = auditEnv('#/admin?tab=audit&action=user.login.failed&user=7', () => OK({ entries: [E()], next: null }));
  env.ctx.CSAdminAudit.mount(env.dom.mk('c'));
  await tick();
  assert.strictEqual(env.els.auditUser.value, '7');
  env.els.auditPeriod.handlers.change({ target: { value: '7d' } });
  await tick();
  assert.strictEqual(env.loc.hash, '#/admin?tab=audit&action=user.login.failed&user=7&period=7d');
  assert.strictEqual(env.replaced.length, 1);
  assert(env.calls[1].indexOf('from=') !== -1, env.calls[1]);
  env.els.auditUser.handlers.change({ target: { value: '12' } });
  await tick();
  assert.strictEqual(env.loc.hash, '#/admin?tab=audit&action=user.login.failed&user=12&period=7d');
  assert(env.calls[2].indexOf('user=12') !== -1, env.calls[2]);
  const n = env.calls.length;
  const ev = { target: env.els.auditUser };
  env.els.auditUser.value = '1x';
  env.els.auditUser.handlers.change(ev);
  await tick();
  assert.strictEqual(env.calls.length, n, 'invalid input must not reload');
  assert.strictEqual(env.els.auditUser.value, '12');
  env.els.auditUser.handlers.change({ target: { value: '' } });
  await tick();
  assert.strictEqual(env.loc.hash, '#/admin?tab=audit&action=user.login.failed&period=7d');
  assert(env.calls[env.calls.length - 1].indexOf('user=') === -1);
});

test('a refused or failed request shows its error in auditMsg', async () => {
  const refused = auditEnv('#/admin?tab=audit', () => ({ ok: false, status: 500, data: { error: 'boom' } }));
  refused.ctx.CSAdminAudit.mount(refused.dom.mk('c'));
  await tick();
  assert.strictEqual(refused.els.auditMsg.textContent, 'boom');
  const broken = auditEnv('#/admin?tab=audit', () => Promise.reject(new Error('net')));
  broken.ctx.CSAdminAudit.mount(broken.dom.mk('c'));
  await tick();
  assert.strictEqual(broken.els.auditMsg.textContent, 'Network error, try again.');
});

test('unmount drops a response that arrives later', async () => {
  let release;
  const env = auditEnv('#/admin?tab=audit', () => new Promise((r) => { release = r; }));
  env.ctx.CSAdminAudit.mount(env.dom.mk('c'));
  await tick();
  env.ctx.CSAdminAudit.unmount();
  release(OK({ entries: [E()], next: null }));
  await tick();
  assert.strictEqual(env.els.auditBody.innerHTML, '');
});

Promise.all(pending).then(() => {
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed ? 1 : 0);
});
