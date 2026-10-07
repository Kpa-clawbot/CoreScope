/* Unit tests for public/channel-proposals.js
 * (docs/specs/2026-10-07-channel-proposals-design.md), loaded from disk into
 * a vm sandbox (pattern of test-admin-dashboard-ui.js). The validate cases
 * mirror internal/channel/hashtag_test.go. */
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

function loadEscapeHtml() {
  const m = src('public/app.js').match(/function escapeHtml\(s\) \{[\s\S]*?\n\}/);
  assert(m, 'escapeHtml not found in app.js');
  return vm.runInNewContext('(' + m[0].replace(/^function escapeHtml/, 'function') + ')');
}

function makeEl(id) {
  const el = { id, value: '', textContent: '', innerHTML: '', className: '', hidden: false, disabled: false, handlers: {} };
  el.addEventListener = (t, fn) => { el.handlers[t] = fn; };
  return el;
}

function load(opts) {
  opts = opts || {};
  const els = {};
  const document = { getElementById(id) { return els[id] || (els[id] = makeEl(id)); } };
  const calls = [];
  const user = { current: opts.user === undefined ? { id: 2, role: 'user' } : opts.user };
  const routes = opts.routes || (() => ({ ok: true, status: 200, data: {} }));
  const CSAuth = {
    user() { return user.current; },
    request(method, p, body) { calls.push({ method, p, body }); return Promise.resolve().then(() => routes(method, p, body)); },
    say(id, text, ok) { const el = document.getElementById(id); el.textContent = text; el.ok = !!ok; },
    errText(r) { return (r.data && r.data.error) || ('Request failed (HTTP ' + r.status + ')'); },
  };
  const ctx = { document, CSAuth, console, escapeHtml: loadEscapeHtml(),
    MC_USER_MGMT: opts.flag === false ? null : { enabled: true, channelProposals: opts.proposals !== false } };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(src('public/channel-proposals.js'), ctx);
  return { P: ctx.CSProposals, els, calls, user, document };
}

const EMPTY = 'enter a channel name after #';
const LONG = 'a channel name is at most 31 bytes including the # (MeshCore stores 32 with the terminator)';
const PUB = 'Public is the built-in channel and cannot be proposed';
const BAD = 'the name contains invisible or control characters';

console.log('channel-proposals.js: validate');
const V = load().P;
const ok = (raw, want) => { const r = V.validate(raw); assert.strictEqual(r.ok, true, JSON.stringify(raw) + ': ' + r.error); assert.strictEqual(r.name, want); };
const bad = (raw, msg) => { const r = V.validate(raw); assert.strictEqual(r.ok, false, JSON.stringify(raw) + ' accepted'); assert.strictEqual(r.error, msg); };

test('prefixes #, trims like Go strings.TrimSpace, keeps case', () => {
  ok('mycity', '#mycity');
  ok('  #MyCity  ', '#MyCity');
  ok('　#tokyo ', '#tokyo');
  ok('# a', '# a');
  ok('#publicity', '#publicity');
  ok('#a"<b>', '#a"<b>');
});

test('31-byte limit counts UTF-8 bytes', () => {
  ok('#' + 'a'.repeat(30), '#' + 'a'.repeat(30));
  bad('#' + 'a'.repeat(31), LONG);
  ok('#' + 'é'.repeat(15), '#' + 'é'.repeat(15));
  bad('#' + 'é'.repeat(15) + 'a', LONG);
  ok('#' + '€'.repeat(10), '#' + '€'.repeat(10));
  assert.strictEqual(V.utf8Length('#👩‍💻'), 12);
});

test('ZWJ emoji accepted; bidi, zero-width, BOM, controls and separators refused', () => {
  ok('#👩‍💻', '#👩‍💻');
  ['#a‮b', '#a​b', '#a﻿b', '﻿abc', '#a\tb', '#a b', '#a\u0007b', '#‎'].forEach((s) => bad(s, BAD));
});

// Same cases as TestValidateHashtagNameAccepts/Refuses in
// internal/channel/hashtag_test.go (Other_Default_Ignorable, U+2800, Zs).
test('VS16 emoji accepted; invisible fillers and non-ASCII spaces refused', () => {
  ok('#❤️', '#❤️');
  ok('#a️b', '#a️b');
  ['#meshㅤ', '#ㅤ', '#⠀', '#aᅟb', '#aᅠb', '#ﾠ', '#me͏sh', '#a឴b',
    '#my city', '#a　b', '#a b', '#a b', '#a b', '# a'].forEach((s) => bad(s, BAD));
});

test('empty and ZWJ-only names refused', () => {
  ['', '   ', '#', ' # ', '#‍'].forEach((s) => bad(s, EMPTY));
  bad(null, EMPTY);
});

test('Public refused in any case', () => {
  ['#public', 'public', 'Public', '#PUBLIC'].forEach((s) => bad(s, PUB));
});

console.log('channel-proposals.js: mergeApproved');

test('marks existing rows, appends rows without traffic, never mutates the input', () => {
  const rows = [{ hash: '#mesh', name: '#mesh', messageCount: 3 }, { hash: 'enc_ab', name: '', encrypted: true }];
  const out = V.mergeApproved(rows, ['#mesh', '#new', '#new']);
  assert.strictEqual(out.length, 3);
  assert.strictEqual(out[0].approved, true);
  assert.strictEqual(out[0].messageCount, 3);
  assert.strictEqual(rows[0].approved, undefined, 'input row mutated');
  assert.strictEqual(out[1].approved, undefined);
  assert.deepStrictEqual(plain(out[2]), { hash: '#new', name: '#new', messageCount: 0, lastActivityMs: 0, lastSender: '', lastMessage: '', approved: true });
  assert.strictEqual(V.mergeApproved(rows, undefined), rows);
  assert.strictEqual(V.mergeApproved(rows, []), rows);
});

console.log('channel-proposals.js: propose control');

test('hidden when proposals are off, logged out, or user management is off', () => {
  for (const o of [{ proposals: false }, { user: null }, { flag: false }]) {
    const env = load(o);
    env.P.bindPropose(env.document);
    assert.strictEqual(env.els.chHashtagProposeBtn.hidden, true, JSON.stringify(o));
  }
  const on = load();
  on.P.bindPropose(on.document);
  assert.strictEqual(on.els.chHashtagProposeBtn.hidden, false);
});

test('sync re-checks the login (the dialog calls it when it opens)', () => {
  const env = load({ user: null });
  const ui = env.P.bindPropose(env.document);
  env.user.current = { id: 2 };
  ui.sync();
  assert.strictEqual(env.els.chHashtagProposeBtn.hidden, false);
});

test('live validation shows the rule message and nothing for an empty or valid field', () => {
  const env = load();
  env.P.bindPropose(env.document);
  const input = env.els.chHashtagName, msg = env.els.chHashtagProposeMsg;
  input.value = 'public'; input.handlers.input();
  assert.strictEqual(msg.textContent, PUB);
  assert(/\berr\b/.test(msg.className), msg.className);
  input.value = ''; input.handlers.input();
  assert.strictEqual(msg.textContent, '');
  input.value = 'mycity'; input.handlers.input();
  assert.strictEqual(msg.textContent, '');
});

test('submit posts {kind, subject}; an invalid name never reaches the server', async () => {
  const env = load({ routes: (m, p, b) => ({ ok: true, status: 201, data: { id: 1, subject: b.subject, status: 'pending' } }) });
  const ui = env.P.bindPropose(env.document);
  env.els.chHashtagName.value = 'public';
  assert.strictEqual(await ui.submit(), false);
  assert.strictEqual(env.calls.length, 0);
  env.els.chHashtagName.value = ' mycity ';
  assert.strictEqual(await ui.submit(), true);
  assert.deepStrictEqual(plain(env.calls[0]), { method: 'POST', p: '/api/proposals', body: { kind: 'hashtag_channel', subject: '#mycity' } });
  assert.strictEqual(env.els.chHashtagProposeMsg.textContent, 'Proposed #mycity, an admin will review it.');
  assert(/\bok\b/.test(env.els.chHashtagProposeMsg.className));
  assert.strictEqual(env.els.chHashtagProposeBtn.disabled, false);
});

test('submit shows the server error and a network error; names stay text', async () => {
  const refused = load({ routes: () => ({ ok: false, status: 409, data: { error: 'this channel is already approved' } }) });
  const ui = refused.P.bindPropose(refused.document);
  refused.els.chHashtagName.value = 'mycity';
  assert.strictEqual(await ui.submit(), false);
  assert.strictEqual(refused.els.chHashtagProposeMsg.textContent, 'this channel is already approved');
  const offline = load({ routes: () => { throw new Error('offline'); } });
  const ui2 = offline.P.bindPropose(offline.document);
  offline.els.chHashtagName.value = 'mycity';
  assert.strictEqual(await ui2.submit(), false);
  assert.strictEqual(offline.els.chHashtagProposeMsg.textContent, 'Network error, try again.');
  const xss = load({ routes: (m, p, b) => ({ ok: true, status: 201, data: { subject: b.subject } }) });
  const ui3 = xss.P.bindPropose(xss.document);
  xss.els.chHashtagName.value = XSS;
  assert.strictEqual(await ui3.submit(), true);
  assert.strictEqual(xss.els.chHashtagProposeMsg.innerHTML, '', 'message written as HTML');
  assert(xss.els.chHashtagProposeMsg.textContent.indexOf(XSS) !== -1);
});

console.log('channel-proposals.js: My proposals');

test('mineHtml escapes subject and note and labels each status', () => {
  const html = V.mineHtml([
    { id: 1, subject: '#' + XSS, status: 'rejected', note: XSS, createdAt: '2026-10-07T10:00:00Z' },
    { id: 2, subject: '#ok', status: 'pending', note: '', createdAt: '2026-10-07T11:00:00Z' },
  ]);
  assert(html.indexOf('<img') === -1, html);
  assert(html.indexOf('class="um-status um-status-rejected">Rejected</span>') !== -1, html);
  assert(html.indexOf('Waiting for review') !== -1);
  assert(html.indexOf('data-subject="#ok"') !== -1);
  assert(V.mineHtml([]).indexOf('You have not proposed a channel yet') !== -1);
  assert(V.statusChip(XSS).indexOf('<img') === -1);
});

test('loadMine renders the list, or the error in the message box', async () => {
  const env = load({ routes: () => ({ ok: true, status: 200, data: [{ id: 1, subject: '#a', status: 'approved', note: '', createdAt: '2026-10-07T10:00:00Z' }] }) });
  const el = env.document.getElementById('propList');
  await env.P.loadMine(el, 'propMsg');
  assert.strictEqual(env.calls[0].p, '/api/account/proposals');
  assert(el.innerHTML.indexOf('data-subject="#a"') !== -1, el.innerHTML);
  const err = load({ routes: () => ({ ok: false, status: 500, data: { error: 'internal error' } }) });
  await err.P.loadMine(err.document.getElementById('propList'), 'propMsg');
  assert.strictEqual(err.els.propMsg.textContent, 'internal error');
});

Promise.all(pending).then(() => {
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed > 0 ? 1 : 0);
});
