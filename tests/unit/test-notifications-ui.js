/* Unit tests for public/notifications.js
 * (docs/specs/2026-10-07-node-notifications-design.md), loaded from disk into
 * a vm sandbox (pattern of test-channel-proposals-ui.js). */
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
const src = (f) => fs.readFileSync(path.join(ROOT, f), 'utf8');
const plain = (v) => JSON.parse(JSON.stringify(v));
const XSS = '<img src=x onerror=alert(1)>';
const PK = 'ab'.repeat(32);

function loadEscapeHtml() {
  const m = src('public/app.js').match(/function escapeHtml\(s\) \{[\s\S]*?\n\}/);
  assert(m, 'escapeHtml not found in app.js');
  return vm.runInNewContext('(' + m[0].replace(/^function escapeHtml/, 'function') + ')');
}

// makeEl is a stand-in DOM node: innerHTML is a string, querySelector finds
// the [data-...] markers notifications.js uses (one object per marker and
// innerHTML, so a re-render yields a new element).
function makeEl(id) {
  const el = { id, innerHTML: '', textContent: '', checked: false, disabled: false, handlers: {}, attrs: {}, kids: {} };
  el.addEventListener = (t, fn) => { el.handlers[t] = fn; };
  el.getAttribute = (n) => (n in el.attrs ? el.attrs[n] : null);
  el.querySelector = (sel) => {
    const m = /^\[([a-z-]+)\]$/.exec(sel);
    if (!m || el.innerHTML.indexOf(m[1]) === -1) return null;
    const key = sel + '|' + el.innerHTML;
    if (!el.kids[key]) {
      const k = makeEl(sel);
      const pressed = /aria-pressed="(true|false)"/.exec(el.innerHTML);
      if (pressed) k.attrs['aria-pressed'] = pressed[1];
      k.disabled = m[1] === 'data-notify-toggle' && / disabled[ >]/.test(el.innerHTML);
      el.kids[key] = k;
    }
    return el.kids[key];
  };
  return el;
}

const STATE = (o) => Object.assign({ enabled: true, events: ['node.offline', 'node.battery'],
  availableEvents: ['node.offline', 'node.battery'], watches: [],
  limits: { maxWatches: 50, perUserPerDay: 20, mailsLast24h: 0 } }, o);

function load(opts) {
  opts = opts || {};
  const els = {};
  const document = { getElementById(id) { return els[id] || (els[id] = makeEl(id)); } };
  const calls = [];
  const listeners = {};
  const user = { current: opts.user === undefined ? { id: 2, role: 'user' } : opts.user };
  const routes = opts.routes || (() => ({ ok: true, status: 200, data: STATE() }));
  const CSAuth = {
    ready() { return opts.ready || Promise.resolve(); },
    user() { return user.current; },
    request(method, p, body) { calls.push({ method, p, body }); return Promise.resolve().then(() => routes(method, p, body)); },
    say(id, text, ok) { const el = document.getElementById(id); el.textContent = text; el.ok = !!ok; },
    errText(r) { return (r.data && r.data.error) || ('Request failed (HTTP ' + r.status + ')'); },
  };
  const ctx = { document, CSAuth, console, escapeHtml: loadEscapeHtml(),
    addEventListener(t, fn) { listeners[t] = fn; },
    MC_USER_MGMT: opts.flag === false ? null : { enabled: true, notifications: opts.notifications !== false } };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(src('public/notifications.js'), ctx);
  return { N: ctx.CSNotify, els, calls, user, document, fire(t) { if (listeners[t]) listeners[t]({}); } };
}

console.log('notifications.js: toggle');

test('no toggle and no request when the feature is off or nobody is logged in', async () => {
  for (const opts of [{ notifications: false }, { flag: false }, { user: null }]) {
    const env = load(opts);
    const slot = makeEl('s');
    slot.innerHTML = 'old';
    await env.N.mount(slot, PK);
    assert.strictEqual(slot.innerHTML, '', JSON.stringify(opts));
    assert.strictEqual(env.calls.length, 0, JSON.stringify(opts));
  }
});

test('node data arriving before auth is ready still gets the toggle', async () => {
  let release;
  const ready = new Promise((res) => { release = res; });
  const env = load({ ready, user: null });
  const slot = makeEl('s');
  const p = env.N.mount(slot, PK);
  await Promise.resolve();
  assert.strictEqual(env.calls.length, 0, 'no request before ready');
  env.user.current = { id: 2, role: 'user' };
  release();
  await p;
  assert.strictEqual(env.calls.length, 1);
  assert(slot.innerHTML.indexOf('aria-pressed="false"') !== -1, slot.innerHTML);
});

test('toggle states and markup: off, on, full, hidden', () => {
  const N = load().N;
  assert.strictEqual(N.toggleState(null, PK), 'hidden');
  assert.strictEqual(N.toggleState(STATE(), PK), 'off');
  assert.strictEqual(N.toggleState(STATE({ watches: [{ pubkey: PK }] }), PK.toUpperCase()), 'on');
  assert.strictEqual(N.toggleState(STATE({ watches: [{ pubkey: 'cd'.repeat(32) }], limits: { maxWatches: 1 } }), PK), 'full');
  const off = N.toggleHtml('off', STATE());
  assert(off.indexOf('aria-pressed="false"') !== -1 && off.indexOf('Notify me') !== -1 && off.indexOf(' disabled') === -1, off);
  const on = N.toggleHtml('on', STATE());
  assert(on.indexOf('aria-pressed="true"') !== -1 && on.indexOf('Notifying') !== -1, on);
  const full = N.toggleHtml('full', STATE({ limits: { maxWatches: XSS } }));
  assert(full.indexOf(' disabled') !== -1 && full.indexOf('<img') === -1 && full.indexOf('&lt;img') !== -1, full);
  assert.strictEqual(N.toggleHtml('hidden', null), '');
});

test('two mounts share one GET; a click watches with a PUT and the answer replaces the cache', async () => {
  const watched = STATE({ watches: [{ pubkey: PK, name: 'N', known: true }] });
  const env = load({ routes: (m) => ({ ok: true, status: 200, data: m === 'GET' ? STATE() : watched }) });
  const a = makeEl('a'), b = makeEl('b');
  await Promise.all([env.N.mount(a, PK.toUpperCase()), env.N.mount(b, PK)]);
  assert.strictEqual(env.calls.length, 1);
  assert(a.innerHTML.indexOf('aria-pressed="false"') !== -1, a.innerHTML);
  await a.querySelector('[data-notify-toggle]').handlers.click();
  assert.deepStrictEqual(plain(env.calls[1]), { method: 'PUT', p: '/api/account/notifications/watches/' + PK });
  assert(a.innerHTML.indexOf('aria-pressed="true"') !== -1, a.innerHTML);
  await env.N.mount(b, PK);
  assert.strictEqual(env.calls.length, 2, 'mounted again from the cache');
  assert(b.innerHTML.indexOf('aria-pressed="true"') !== -1, b.innerHTML);
});

test('a click on Notifying unwatches with a DELETE', async () => {
  const env = load({ routes: (m) => ({ ok: true, status: 200, data: m === 'GET' ? STATE({ watches: [{ pubkey: PK }] }) : STATE() }) });
  const slot = makeEl('s');
  await env.N.mount(slot, PK);
  await slot.querySelector('[data-notify-toggle]').handlers.click();
  assert.strictEqual(env.calls[1].method, 'DELETE');
  assert(slot.innerHTML.indexOf('aria-pressed="false"') !== -1, slot.innerHTML);
});

test('a refused change shows the server message and re-enables the button', async () => {
  const msg = 'you watch the maximum of 50 nodes; remove one first';
  const env = load({ routes: (m) => (m === 'GET' ? { ok: true, status: 200, data: STATE() } : { ok: false, status: 409, data: { error: msg } }) });
  const slot = makeEl('s');
  await env.N.mount(slot, PK);
  const btn = slot.querySelector('[data-notify-toggle]');
  await btn.handlers.click();
  assert.strictEqual(btn.disabled, false);
  assert.strictEqual(slot.querySelector('[data-notify-msg]').textContent, msg);
});

test('another user or an auth change drops the cached state', async () => {
  const env = load();
  await env.N.mount(makeEl('a'), PK);
  env.user.current = { id: 3, role: 'user' };
  await env.N.mount(makeEl('b'), PK);
  assert.strictEqual(env.calls.length, 2);
  env.fire('cs-auth-changed');
  await env.N.mount(makeEl('c'), PK);
  assert.strictEqual(env.calls.length, 3);
});

test('nodes.js mounts the toggle on both views; roles.js and index.html wire the module', () => {
  const nodes = src('public/nodes.js');
  assert(nodes.indexOf('id="nodeNotifySlot"') !== -1 && nodes.indexOf("CSNotify.mount(document.getElementById('nodeNotifySlot'), n.public_key)") !== -1);
  assert(nodes.indexOf('id="nodeNotifySlotFull"') !== -1 && nodes.indexOf("CSNotify.mount(document.getElementById('nodeNotifySlotFull'), n.public_key)") !== -1);
  assert(/notifications: !!cfg\.userManagement\.notifications/.test(src('public/roles.js')));
  assert(src('public/index.html').indexOf('<script src="notifications.js?v=__BUST__"></script>') !== -1);
});

console.log('notifications.js: account section');

test('section escapes names and shows events, watches and limits', () => {
  const N = load().N;
  const html = N.sectionHtml(STATE({ availableEvents: ['node.offline', 'node.battery', 'foreign.new', 'observer.offline'],
    events: ['node.offline', 'foreign.new'], limits: { maxWatches: 50, perUserPerDay: 20, mailsLast24h: 3 },
    watches: [{ pubkey: PK, name: XSS, known: true }, { pubkey: 'cd'.repeat(32), name: '', known: false }] }));
  assert(html.indexOf('<img') === -1 && html.indexOf('&lt;img') !== -1, 'name not escaped');
  assert(html.indexOf('id="notifyEv-foreign-new" data-notify-event="foreign.new" checked') !== -1, html);
  assert(html.indexOf('id="notifyEv-node-battery" data-notify-event="node.battery">') !== -1, html);
  assert(html.indexOf('id="notifyEnabled" checked') !== -1);
  assert(html.indexOf('no longer in the database') !== -1);
  assert(html.indexOf('Watched nodes (2 of 50)') !== -1);
  assert(html.indexOf('At most 20 mails per 24 hours; sent in the last 24 hours: 3.') !== -1);
  assert(html.indexOf('data-unwatch="' + PK + '"') !== -1 && html.indexOf('href="#/nodes/' + PK + '"') !== -1);
  assert(N.sectionHtml(STATE()).indexOf('You watch no nodes yet') !== -1);
});

test('Save sends enabled and the checked events; the answer redraws', async () => {
  const env = load({ routes: (m) => ({ ok: true, status: 200, data: m === 'GET' ? STATE() : STATE({ enabled: false, events: ['node.battery'] }) }) });
  await env.N.mountSection(env.document.getElementById('notifySection'), 'notifyMsg');
  assert(env.els.notifySection.innerHTML.indexOf('notifyForm') !== -1);
  env.document.getElementById('notifyEnabled').checked = false;
  env.document.getElementById('notifyEv-node-offline').checked = false;
  env.document.getElementById('notifyEv-node-battery').checked = true;
  await env.els.notifyForm.handlers.submit({ preventDefault() {} });
  assert.deepStrictEqual(plain(env.calls[1]), { method: 'PUT', p: '/api/account/notifications', body: { enabled: false, events: ['node.battery'] } });
  assert.strictEqual(env.els.notifyMsg.textContent, 'Saved.');
  assert(env.els.notifySection.innerHTML.indexOf('id="notifyEnabled">') !== -1, 'not redrawn from the answer');
});

test('Remove unwatches; Watch my nodes reports the counts', async () => {
  const env = load({ routes: (m, p) => {
    if (m === 'GET') return { ok: true, status: 200, data: STATE({ watches: [{ pubkey: PK, name: 'N', known: true }] }) };
    if (m === 'DELETE') return { ok: true, status: 200, data: STATE() };
    return { ok: true, status: 200, data: { added: 2, already: 1, skipped: 3, account: STATE() } };
  } });
  await env.N.mountSection(env.document.getElementById('notifySection'), 'notifyMsg');
  await env.els.notifyWatches.handlers.click({ target: { getAttribute: (n) => (n === 'data-unwatch' ? PK : null) } });
  assert.deepStrictEqual(plain(env.calls[1]), { method: 'DELETE', p: '/api/account/notifications/watches/' + PK });
  assert.strictEqual(env.els.notifyMsg.textContent, 'Removed.');
  await env.els.notifyMyNodes.handlers.click();
  assert.deepStrictEqual(plain(env.calls[2]), { method: 'POST', p: '/api/account/notifications/watch-my-nodes' });
  assert.strictEqual(env.els.notifyMsg.textContent, 'Added 2, already watched 1, skipped 3.');
});

test('Watch my nodes with an empty synced list says how to fill it', async () => {
  const env = load({ routes: (m) => ({ ok: true, status: 200, data: m === 'GET' ? STATE() : { added: 0, already: 0, skipped: 0, account: STATE() } }) });
  await env.N.mountSection(env.document.getElementById('notifySection'), 'notifyMsg');
  await env.els.notifyMyNodes.handlers.click();
  assert(env.els.notifyMsg.textContent.indexOf('My nodes list is empty') !== -1, env.els.notifyMsg.textContent);
});

test('a failed load shows the server message', async () => {
  const env = load({ routes: () => ({ ok: false, status: 500, data: { error: 'internal error' } }) });
  await env.N.mountSection(env.document.getElementById('notifySection'), 'notifyMsg');
  assert.strictEqual(env.els.notifyMsg.textContent, 'internal error');
});

test('a second click while a request is in flight sends nothing', async () => {
  let release;
  const gate = new Promise((res) => { release = res; });
  const env = load({ routes: (m) => (m === 'GET' ? { ok: true, status: 200, data: STATE() } : gate.then(() => ({ ok: true, status: 200, data: STATE() }))) });
  await env.N.mountSection(env.document.getElementById('notifySection'), 'notifyMsg');
  const a = env.els.notifyForm.handlers.submit({ preventDefault() {} });
  const b = env.els.notifyForm.handlers.submit({ preventDefault() {} });
  release();
  await Promise.all([a, b]);
  assert.strictEqual(env.calls.filter((c) => c.method === 'PUT').length, 1);
});

Promise.all(pending).then(() => {
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed > 0 ? 1 : 0);
});
