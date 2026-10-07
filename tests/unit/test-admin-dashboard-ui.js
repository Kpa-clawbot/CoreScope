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

test('a successful load clears an earlier error', async () => {
  let fail = true;
  const env = auditEnv('#/admin?tab=audit', () => fail ? { ok: false, status: 500, data: { error: 'boom' } } : OK({ entries: [E()], next: null }));
  env.ctx.CSAdminAudit.mount(env.dom.mk('c'));
  await tick();
  assert.strictEqual(env.els.auditMsg.textContent, 'boom');
  fail = false;
  env.els.auditPeriod.handlers.change({ target: { value: '7d' } });
  await tick();
  assert.strictEqual(env.els.auditMsg.textContent, '');
});

test('a filter change clears stale rows and the cursor; the old response is dropped', async () => {
  const held = [];
  const env = auditEnv('#/admin?tab=audit', (p) => p.indexOf('from=') !== -1
    ? OK({ entries: [E({ id: 50 })], next: null })
    : (held.length ? Promise.resolve(OK({ entries: [E({ id: 10 })], next: 9 })) : new Promise((r) => { held.push(r); })));
  env.ctx.CSAdminAudit.mount(env.dom.mk('c'));
  await tick();
  env.els.auditPeriod.handlers.change({ target: { value: '7d' } });
  assert.strictEqual(env.els.auditMore.hidden, true);
  assert.strictEqual(env.els.auditBody.innerHTML, '');
  await tick();
  assert.strictEqual(rows(env.els.auditBody.innerHTML), 1);
  held[0](OK({ entries: [E({ id: 10 }), E({ id: 9 })], next: 9 }));
  await tick();
  assert.strictEqual(rows(env.els.auditBody.innerHTML), 1, 'stale response must be dropped');
  assert.strictEqual(env.els.auditMore.hidden, true);
});

test('a failed filter request leaves no stale rows', async () => {
  let fail = false;
  const env = auditEnv('#/admin?tab=audit', () => fail ? { ok: false, status: 500, data: { error: 'boom' } } : OK({ entries: [E()], next: 9 }));
  env.ctx.CSAdminAudit.mount(env.dom.mk('c'));
  await tick();
  fail = true;
  env.els.auditPeriod.handlers.change({ target: { value: '7d' } });
  await tick();
  assert.strictEqual(env.els.auditBody.innerHTML, '');
  assert.strictEqual(env.els.auditMore.hidden, true);
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

console.log('admin-overview.js');

const STATS = { total: 3, active: 2, pending: 1, disabled: 0, admins: 1, stuckPending: 1, bouncing: 2, new7d: 3, new30d: 3,
  newPerDay: [{ day: '2026-10-06', count: 1 }, { day: '2026-10-07', count: 2 }], active7d: 2, active30d: 2, logins24h: 4,
  failedLogins24h: 6, mail7d: { delivered: 1, bounced: 0, blocked: 0, spam: 0, pending: 2, other: 0 },
  guessing: [{ userId: 7, displayName: 'Eve', failed: 6 }] };
const MQTT = { sources: [
  { name: 'ok', connected: true, lastPacketUnix: NOW / 1000 - 60 },
  { name: 'off', connected: false, lastPacketUnix: NOW / 1000 - 60 },
  { name: 'quiet', connected: true, lastPacketUnix: NOW / 1000 - 11 * 60 },
  { name: 'never', connected: true, lastPacketUnix: 0 }] };
const HEALTH = { version: 'v9.9.9', commit: 'abc1234', uptimeHuman: '1h 2m' };
const OBS = { observers: [{ online: true }, { online: false }] };
const observersStub = { ObserversSummary: { computeCounts: (list) => ({ online: list.filter((o) => o.online).length, total: list.length }) } };
const overviewEnv = (routes) => loadTab(['public/mqtt-status-panel.js', 'public/admin-overview.js'], '#/admin', routes, observersStub);
const allOk = (p) => OK({ '/api/admin/stats': STATS, '/api/health': HEALTH, '/api/healthz': { ready: true },
  '/api/mqtt/status': MQTT, '/api/observers': OBS }[p]);
const ovT = () => overviewEnv(allOk).ctx.CSAdminOverview._test;
const resOf = (o) => Object.assign({ stats: { data: STATS }, health: { data: HEALTH }, healthz: { data: { ready: true } },
  mqtt: { data: MQTT }, observers: { data: OBS } }, o);

test('attention items from fixed data: stuck, bouncing, guessing, three MQTT sources down', () => {
  const items = ovT().attentionItems(resOf({}), NOW);
  assert.deepStrictEqual([...items.map((i) => i.href)], ['#/admin?tab=users&status=pending', '#/admin?tab=users&bouncing=1',
    '#/admin?tab=audit&action=user.login.failed&user=7', '#/observers', '#/observers', '#/observers']);
  const mqttText = items.slice(3).map((i) => i.text).join(' | ');
  assert(mqttText.indexOf('off') !== -1 && mqttText.indexOf('quiet') !== -1 && mqttText.indexOf('never') !== -1, mqttText);
  assert(mqttText.indexOf(' ok ') === -1, mqttText);
  assert(items[2].text.indexOf('6 failed logins') !== -1 && items[2].text.indexOf('Eve') !== -1, items[2].text);
});

test('mqttDown: not connected, never a message, or older than 10 minutes', () => {
  const t = ovT();
  assert.strictEqual(t.mqttDown({ connected: true, lastPacketUnix: NOW / 1000 - 9 * 60 }, NOW), false);
  assert.strictEqual(t.mqttDown({ connected: true, lastPacketUnix: NOW / 1000 - 11 * 60 }, NOW), true);
  assert.strictEqual(t.mqttDown({ connected: true, lastPacketUnix: 0 }, NOW), true);
  assert.strictEqual(t.mqttDown({ connected: false, lastPacketUnix: NOW / 1000 }, NOW), true);
});

test('nothing to report renders no attention section; failed sources add no items', () => {
  const t = ovT();
  const calm = Object.assign({}, STATS, { stuckPending: 0, bouncing: 0, guessing: [] });
  assert.strictEqual(t.attentionItems(resOf({ stats: { data: calm }, mqtt: { data: { sources: [MQTT.sources[0]] } } }), NOW).length, 0);
  assert.strictEqual(t.attentionItems(resOf({ stats: { error: true }, mqtt: { error: true } }), NOW).length, 0);
  assert.strictEqual(t.attentionHtml([]), '');
});

test('names, source names and versions are escaped', () => {
  const t = ovT();
  const res = resOf({ stats: { data: Object.assign({}, STATS, { guessing: [{ userId: 7, displayName: XSS, failed: 5 }] }) },
    mqtt: { data: { sources: [{ name: XSS, connected: false, lastPacketUnix: 0 }] } },
    health: { data: { version: XSS, commit: XSS, uptimeHuman: XSS } } });
  const html = t.render(res, NOW);
  assert(html.indexOf('<img') === -1, 'raw markup: ' + html);
  assert(html.indexOf('&lt;img src=x onerror=alert(1)&gt;') !== -1);
});

test('an empty instance renders zeros and no attention section', () => {
  const t = ovT();
  const zero = { total: 0, active: 0, pending: 0, disabled: 0, admins: 0, stuckPending: 0, bouncing: 0, new7d: 0, new30d: 0,
    newPerDay: [{ day: '2026-10-07', count: 0 }], active7d: 0, active30d: 0, logins24h: 0, failedLogins24h: 0,
    mail7d: { delivered: 0, bounced: 0, blocked: 0, spam: 0, pending: 0, other: 0 }, guessing: [] };
  const html = t.render(resOf({ stats: { data: zero }, mqtt: { data: { sources: [] } }, observers: { data: { observers: [] } } }), NOW);
  assert(html.indexOf('adminAttention') === -1, html);
  assert(html.indexOf('data-stat="total">0<') !== -1);
  assert(html.indexOf('No MQTT sources reported.') !== -1);
});

test('fetchAll keeps each source on its own; healthz 503 warming up is data, not an error', async () => {
  const t = ovT();
  // Functions, so the rejected promise only exists for the stats request.
  const answers = {
    '/api/admin/stats': () => Promise.reject(new Error('net')),
    '/api/health': () => OK(HEALTH),
    '/api/healthz': () => ({ ok: false, status: 503, data: { ready: false, reason: 'loading' } }),
    '/api/mqtt/status': () => ({ ok: false, status: 500, data: {} }),
    '/api/observers': () => OK(OBS),
  };
  const res = await t.fetchAll((m, p) => Promise.resolve().then(answers[p]));
  assert.strictEqual(res.stats.error, true);
  assert.strictEqual(res.healthz.data.ready, false);
  assert.strictEqual(res.mqtt.error, true);
  const html = t.render(res, NOW);
  assert(html.indexOf('Could not load user figures') !== -1);
  assert(html.indexOf('Could not load MQTT status') !== -1);
  assert(html.indexOf('warming up') !== -1);
  assert(html.indexOf('v9.9.9') !== -1);
  assert(html.indexOf('Could not load server health') === -1 && html.indexOf('Could not load observers') === -1);
});

test('mount fetches five sources, a timer tick (visible only) skips healthz, Refresh and Retry read it, unmount stops the timer', async () => {
  const env = overviewEnv(allOk);
  const ov = env.ctx.CSAdminOverview;
  ov.mount(env.dom.mk('c'));
  await tick();
  assert.strictEqual(env.calls.length, 5);
  assert(env.els.aoBody.innerHTML.indexOf('data-stat="total">3<') !== -1, env.els.aoBody.innerHTML);
  assert(env.els.aoBody.innerHTML.indexOf('data-stat="observersOnline">1<') !== -1);
  const timer = env.timers[env.timers.length - 1];
  assert.strictEqual(timer.ms, 60000);
  env.dom.document.visibilityState = 'hidden';
  timer.fn();
  await tick();
  assert.strictEqual(env.calls.length, 5);
  env.dom.document.visibilityState = 'visible';
  timer.fn();
  await tick();
  assert.strictEqual(env.calls.length, 9); // the timer skips /api/healthz
  assert.strictEqual(env.calls.filter((p) => p === '/api/healthz').length, 1);
  assert(env.els.aoBody.innerHTML.indexOf('>ready<') !== -1, 'last healthz result kept');
  env.els.aoBody.handlers.click({ target: { closest: () => ({}) } }); // Retry
  await tick();
  assert.strictEqual(env.calls.length, 14);
  assert.strictEqual(env.calls.filter((p) => p === '/api/healthz').length, 2);
  env.els.aoRefresh.handlers.click();
  await tick();
  assert.strictEqual(env.calls.length, 19);
  ov.unmount();
  assert.strictEqual(timer.cleared, true);
});

Promise.all(pending).then(() => {
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed ? 1 : 0);
});
