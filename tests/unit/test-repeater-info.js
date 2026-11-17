/* Unit tests for public/repeater-info.js (repeater claims + community
 * details section on the node detail views), loaded into a vm sandbox. */
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
const XSS = '<img src=x onerror=alert(1)>';
const PK = 'ab'.repeat(32);

function loadEscapeHtml() {
  const m = src('public/app.js').match(/function escapeHtml\(s\) \{[\s\S]*?\n\}/);
  assert(m, 'escapeHtml not found in app.js');
  return vm.runInNewContext('(' + m[0].replace(/^function escapeHtml/, 'function') + ')');
}

function load(opts) {
  opts = opts || {};
  const calls = [];
  const CSAuth = {
    ready() { return Promise.resolve(); },
    isEnabled() { return opts.enabled !== false; },
    user() { return opts.user === undefined ? { id: 2, role: 'user' } : opts.user; },
    request(method, p, body) {
      calls.push({ method, p, body });
      return Promise.resolve({ ok: true, status: 200, data: opts.info || INFO() });
    },
    errText(r) { return (r.data && r.data.error) || ('Request failed (HTTP ' + r.status + ')'); },
  };
  const listeners = {};
  const ctx = { CSAuth, console, escapeHtml: loadEscapeHtml(), location: { hash: opts.hash || '' },
    addEventListener(t, fn) { (listeners[t] = listeners[t] || []).push(fn); } };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(src('public/repeater-info.js'), ctx);
  const fire = (t) => (listeners[t] || []).forEach((fn) => fn({}));
  return { R: ctx.CSRepeaterInfo, calls, auth: CSAuth, fire };
}

const INFO = (o) => Object.assign({ pubkey: PK, heightM: null, gainDbi: 0, status: null, power: null,
  notes: '', claimers: [], updatedAt: null, updatedBy: null }, o);
const USER = { id: 2, role: 'user' };

// fakeForm: a stand-in root whose querySelector returns [name=...] controls.
function fakeForm(vals, badInput) {
  return { querySelector(sel) {
    const m = /\[name="(\w+)"\]/.exec(sel);
    return m && m[1] in vals ? { value: vals[m[1]], validity: { badInput: !!(badInput && badInput[m[1]]) } } : null;
  } };
}

const T = load().R._test;

test('notes render as text, never markup', () => {
  const h = T.viewHtml(INFO({ notes: XSS, updatedAt: '2026-01-01T00:00:00Z' }), USER);
  assert(h.includes('&lt;img'), h);
  assert(!h.includes('<img'), h);
  assert(h.includes('repeater-info-notes'));
  assert(src('public/style.css').includes('.repeater-info-notes { white-space: pre-wrap; max-height: 12em; overflow-y: auto; }'));
});
test('display names are escaped', () => {
  const h = T.viewHtml(INFO({ claimers: [{ displayName: XSS }] }), USER);
  assert(!h.includes('<img'));
});
test('height 0 is shown, null hides, nothing set says no details', () => {
  assert(T.viewHtml(INFO({ heightM: 0, updatedAt: 'x' }), null).includes('0 m'));
  const h = T.viewHtml(INFO({ heightM: null }), null);
  assert(h.includes('No details yet.'));
  assert(!h.includes('Antenna height'));
});
test('buttons by user and permission', () => {
  const none = T.viewHtml(INFO(), null);
  assert(!/Edit|Claim/.test(none));
  const c = T.viewHtml(INFO({ canEdit: false, claimedByMe: false }), USER);
  assert(c.includes('Claim') && !c.includes('Unclaim') && !c.includes('Edit'));
  const u = T.viewHtml(INFO({ canEdit: true, claimedByMe: true }), USER);
  assert(u.includes('Unclaim') && u.includes('Edit'));
});
test('claimers are comma separated', () => {
  const h = T.viewHtml(INFO({ claimers: [{ displayName: 'A' }, { displayName: 'B' }] }), null);
  assert(h.includes('A, B'), h);
});
test('formHtml has the notice and exact option sets', () => {
  const h = T.formHtml(INFO());
  assert(h.includes('Your display name will be shown publicly as the last editor of this repeater.'));
  assert(!h.includes('planned'));
  ['active', 'maintenance', 'decommissioned', 'mains', 'battery', 'solar'].forEach((v) => assert(h.includes('value="' + v + '"'), v));
  assert(h.includes('maxlength="500"'));
});
test('readForm parses height and gain', () => {
  const a = T.readForm(fakeForm({ heightM: '', gainDbi: '', status: '', power: '', notes: 'x' }));
  assert.strictEqual(a.heightM, null);
  assert.strictEqual(a.gainDbi, 0);
  const b = T.readForm(fakeForm({ heightM: '12.5', gainDbi: '3', status: 'active', power: 'solar', notes: '' }));
  assert.strictEqual(b.heightM, 12.5);
  assert.strictEqual(b.gainDbi, 3);
  assert.strictEqual(b.status, 'active');
});
test('mount is a no-op for non-repeaters and when accounts are off', async () => {
  const el = { innerHTML: '', hidden: true };
  let l = load();
  await l.R.mount(el, { public_key: PK, role: 'companion' });
  assert.strictEqual(l.calls.length, 0);
  l = load({ enabled: false });
  await l.R.mount(el, { public_key: PK, role: 'repeater' });
  assert.strictEqual(l.calls.length, 0);
  assert(el.hidden);
});
test('mount fetches, renders and shows', async () => {
  const el = { innerHTML: '', hidden: true, addEventListener() {}, querySelector() { return null; }, scrollIntoView() {} };
  const l = load({ info: INFO({ heightM: 7, updatedAt: 'x' }) });
  await l.R.mount(el, { public_key: PK.toUpperCase(), role: 'repeater' });
  assert.strictEqual(l.calls[0].method, 'GET');
  assert.strictEqual(l.calls[0].p, '/api/repeaters/' + PK + '/info');
  assert(el.innerHTML.includes('7 m'));
  assert.strictEqual(el.hidden, false);
});

// click-driven flows: the fake el captures the click handler.
function mountFlow(opts, shared) {
  opts = opts || {};
  const l = shared || load(opts);
  const el = { id: opts.id || 'node-repeater-info', innerHTML: '', hidden: true, handlers: {}, msg: { textContent: '' }, form: {} };
  el.addEventListener = (t, fn) => { el.handlers[t] = fn; };
  el.querySelector = (sel) => (sel === '[data-ri-msg]' ? el.msg : sel === '[data-ri-form]' ? fakeForm(opts.form || {}) : null);
  el.scrollIntoView = () => {};
  const click = (attr) => {
    const btn = { disabled: false, hasAttribute: (a) => a === attr };
    el.handlers.click({ target: { closest: () => btn }, preventDefault() {} });
    return btn;
  };
  return { l, el, click };
}
const tick = () => new Promise((r) => setTimeout(r, 0));

test('status, power and updatedBy are escaped', () => {
  const h = T.viewHtml(INFO({ status: XSS, power: XSS, updatedBy: XSS, updatedAt: '2026-01-01T00:00:00Z' }), null);
  assert(!h.includes('<img') && h.includes('&lt;img'), h);
});
test('updatedAt without updatedBy shows Last edited without a name', () => {
  const h = T.viewHtml(INFO({ updatedAt: '2026-01-01T00:00:00Z', updatedBy: null }), null);
  assert(h.includes('Last edited') && !h.includes('Last edited by'), h);
});
test('claim asks for inline confirmation, confirm POSTs', async () => {
  const f = mountFlow({ info: INFO({ canEdit: false, claimedByMe: false }) });
  await f.l.R.mount(f.el, { public_key: PK, role: 'repeater' });
  f.click('data-ri-claim');
  assert(f.el.innerHTML.includes('shown publicly as a maintainer'));
  assert.strictEqual(f.l.calls.length, 1);
  f.click('data-ri-confirm');
  await tick();
  const c = f.l.calls[1];
  assert.strictEqual(c.method, 'POST');
  assert.strictEqual(c.p, '/api/repeaters/' + PK + '/claim');
});
test('save PUTs the form body', async () => {
  const form = { heightM: '12.5', gainDbi: '3', status: 'active', power: 'solar', notes: 'hi' };
  const f = mountFlow({ info: INFO({ canEdit: true }), form });
  await f.l.R.mount(f.el, { public_key: PK, role: 'repeater' });
  f.click('data-ri-edit');
  f.click('data-ri-save');
  await tick();
  const c = f.l.calls[1];
  assert.strictEqual(c.method, 'PUT');
  assert.strictEqual(c.p, '/api/repeaters/' + PK + '/info');
  assert.deepStrictEqual(JSON.parse(JSON.stringify(c.body)), { heightM: 12.5, gainDbi: 3, status: 'active', power: 'solar', notes: 'hi' });
});
test('error response shows its text via textContent; clicks are ignored while busy', async () => {
  const f = mountFlow({ info: INFO({ canEdit: true, claimedByMe: true }) });
  await f.l.R.mount(f.el, { public_key: PK, role: 'repeater' });
  let release;
  const gate = new Promise((r) => { release = r; });
  const orig = f.l.calls;
  const ctxAuth = f.l.auth;
  ctxAuth.request = (m, p, b) => { orig.push({ method: m, p, body: b }); return gate; };
  const b1 = f.click('data-ri-unclaim');
  assert.strictEqual(b1.disabled, true);
  f.click('data-ri-unclaim');
  assert.strictEqual(orig.length, 2, 'second click ignored while busy');
  release({ ok: false, status: 400, data: { error: 'nope <b>' } });
  await tick();
  assert.strictEqual(f.el.msg.textContent, 'nope <b>');
  assert.strictEqual(b1.disabled, false);
});
test('unparsable height or gain is not sent, message shown', async () => {
  const cases = [
    [{ heightM: '', gainDbi: '3x', status: '', power: '', notes: '' }, null, 'Antenna gain must be a number.'],
    [{ heightM: '', gainDbi: '1', status: '', power: '', notes: '' }, { heightM: true }, 'Antenna height must be a number.'],
    [{ heightM: 'abc', gainDbi: '1', status: '', power: '', notes: '' }, null, 'Antenna height must be a number.'],
    [{ heightM: 'Infinity', gainDbi: '1', status: '', power: '', notes: '' }, null, 'Antenna height must be a number.'],
  ];
  for (const [vals, bad, msg] of cases) {
    const f = mountFlow({ info: INFO({ canEdit: true }) });
    f.l.R._test.formError(fakeForm(vals, bad));
    const orig = f.el.querySelector;
    f.el.querySelector = (sel) => (sel === '[data-ri-form]' ? fakeForm(vals, bad) : orig(sel));
    await f.l.R.mount(f.el, { public_key: PK, role: 'repeater' });
    f.click('data-ri-edit');
    f.click('data-ri-save');
    await tick();
    assert.strictEqual(f.l.calls.length, 1, 'only the GET, no PUT');
    assert.strictEqual(f.el.msg.textContent, msg);
  }
});
test('empty height is unknown and still sent', async () => {
  const form = { heightM: '', gainDbi: '', status: '', power: '', notes: '' };
  const f = mountFlow({ info: INFO({ canEdit: true }), form });
  await f.l.R.mount(f.el, { public_key: PK, role: 'repeater' });
  f.click('data-ri-edit');
  f.click('data-ri-save');
  await tick();
  assert.strictEqual(f.l.calls[1].method, 'PUT');
  assert.strictEqual(f.l.calls[1].body.heightM, null);
});
// The node page re-renders on theme-refresh (and so mounts a fresh element);
// an open edit form must come back with what the user typed.
test('a re-mount brings back an open edit form with the typed values', async () => {
  const opts = { info: INFO({ canEdit: true, heightM: 5 }), form: { heightM: '25', gainDbi: '3', status: 'maintenance', power: 'solar', notes: 'typed note' } };
  const a = mountFlow(opts);
  await a.l.R.mount(a.el, { public_key: PK, role: 'repeater' });
  a.click('data-ri-edit');
  a.el.handlers.input({});
  const b = mountFlow(opts, a.l);
  await b.l.R.mount(b.el, { public_key: PK, role: 'repeater' });
  assert(b.el.innerHTML.includes('data-ri-form'), 'form not restored: ' + b.el.innerHTML);
  assert(b.el.innerHTML.includes('value="25"') && b.el.innerHTML.includes('typed note'), b.el.innerHTML);
  assert(b.el.innerHTML.includes('<option value="maintenance" selected>'), b.el.innerHTML);
});
test('no draft after Cancel, a save, a hashchange, another repeater or lost edit rights', async () => {
  const opts = { info: INFO({ canEdit: true }), form: { heightM: '25', gainDbi: '0', status: '', power: '', notes: 'x' } };
  const remountShowsForm = async (l, o, pk) => {
    const b = mountFlow(o || opts, l);
    await l.R.mount(b.el, { public_key: pk || PK, role: 'repeater' });
    return b.el.innerHTML.includes('data-ri-form');
  };
  const open = async (l) => {
    const a = mountFlow(opts, l);
    await l.R.mount(a.el, { public_key: PK, role: 'repeater' });
    a.click('data-ri-edit');
    a.el.handlers.input({});
    return a;
  };
  let l = load(opts); let a = await open(l); a.click('data-ri-cancel');
  assert(!(await remountShowsForm(l)), 'Cancel kept the draft');
  l = load(opts); a = await open(l); a.click('data-ri-save'); await tick();
  assert(!(await remountShowsForm(l)), 'a save kept the draft');
  l = load(opts); await open(l); l.fire('hashchange');
  assert(!(await remountShowsForm(l)), 'hashchange kept the draft');
  l = load(opts); await open(l);
  assert(!(await remountShowsForm(l, null, 'cd'.repeat(32))), 'draft shown on another repeater');
  l = load(opts); await open(l);
  l.auth.request = () => Promise.resolve({ ok: true, status: 200, data: INFO({ canEdit: false }) });
  assert(!(await remountShowsForm(l)), 'draft shown without edit rights');
});
test('anonymous visitor sees one login hint, a logged-in user does not', () => {
  const anon = load({ user: null }).R._test.viewHtml(INFO(), null);
  assert(anon.includes('Log in to claim or edit this repeater.'));
  assert.strictEqual(anon.split('Log in to claim').length, 2);
  assert(!anon.includes('data-ri-claim'));
  const u = load().R._test.viewHtml(INFO(), USER);
  assert(!u.includes('Log in to claim'));
});
test('GET with ok:false leaves el hidden', async () => {
  const f = mountFlow({});
  f.l.auth.request = () => Promise.resolve({ ok: false, status: 404, data: {} });
  await f.l.R.mount(f.el, { public_key: PK, role: 'repeater' });
  assert.strictEqual(f.el.hidden, true);
});

Promise.all(pending).then(() => {
  console.log(passed + ' passed, ' + failed + ' failed');
  process.exit(failed ? 1 : 0);
});
