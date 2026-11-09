/* public/build-info.js: which builds are running. The embedded UI is the
 * server's own build; a UI served apart from the server (static host, web
 * container) is a build of its own, named by deployment.js, and the server
 * may run another version. describe() pairs the two, and flags two different
 * builds only when it can tell: by commit when both are git commits,
 * otherwise by release tag. */
'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const { loadApiBase } = require('./api-base-sandbox');

const SRC = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'build-info.js'), 'utf8');
const SPLIT = { channel: 'static', apiBase: '', auth: true, commit: 'dac8d9f5', version: 'v3.16.0-21-gdac8d9f' };

let passed = 0, failed = 0;
let chain = Promise.resolve();
function test(name, fn) {
  chain = chain.then(fn).then(
    () => { passed++; console.log('  ✅ ' + name); },
    (e) => { failed++; console.log('  ❌ ' + name + ': ' + (e && e.message)); });
}

function load(deployment, fetchImpl) {
  const ctx = { console, fetch: fetchImpl || (() => Promise.reject(new Error('no fetch in this case'))) };
  ctx.window = ctx;
  loadApiBase(ctx, deployment);
  vm.runInContext(SRC, ctx);
  return ctx.BuildInfo;
}
// Plain data out of the sandbox realm, for deepStrictEqual.
const plain = (v) => JSON.parse(JSON.stringify(v));

test('the embedded UI is the server build: one build, never a mismatch', () => {
  const info = plain(load(undefined).describe({ version: 'v3.16.0', commit: 'abc1234' }));
  assert.deepStrictEqual(info, {
    separate: false, mismatch: false,
    ui: { version: 'v3.16.0', commit: 'abc1234' }, server: { version: 'v3.16.0', commit: 'abc1234' },
  });
});

test('a separate UI on the same commit is not a mismatch (short and long shas)', () => {
  const info = load(SPLIT).describe({ version: 'edge', commit: 'dac8d9f' });
  assert.strictEqual(info.separate, true);
  assert.strictEqual(info.mismatch, false);
  assert.deepStrictEqual(plain(info.ui), { version: 'v3.16.0-21-gdac8d9f', commit: 'dac8d9f5' });
  assert.deepStrictEqual(plain(info.server), { version: 'edge', commit: 'dac8d9f' });
});

test('a separate UI on another commit is a mismatch', () => {
  assert.strictEqual(load(SPLIT).describe({ version: 'edge', commit: 'abc1234' }).mismatch, true);
});

test('without two commits, two release tags decide', () => {
  const ui = Object.assign({}, SPLIT, { commit: 'build-1794206204', version: 'v3.17.0' });
  assert.strictEqual(load(ui).describe({ version: 'v3.16.0', commit: 'unknown' }).mismatch, true);
  assert.strictEqual(load(ui).describe({ version: 'v3.17.0', commit: 'unknown' }).mismatch, false);
});

test('no flag when it cannot tell: an id that is not a commit, an edge server, no health', () => {
  const ui = Object.assign({}, SPLIT, { commit: 'build-1794206204', version: null });
  assert.strictEqual(load(ui).describe({ version: 'edge', commit: 'abc1234' }).mismatch, false);
  assert.strictEqual(load(Object.assign({}, SPLIT, { commit: 'e2e' })).describe({ version: 'v3.16.0', commit: 'abc1234' }).mismatch, false);
  const none = load(SPLIT).describe(null);
  assert.strictEqual(none.mismatch, false);
  assert.deepStrictEqual(plain(none.server), { version: null, commit: null });
});

test('"unknown" counts as not known', () => {
  const info = load(undefined).describe({ version: 'unknown', commit: 'unknown' });
  assert.deepStrictEqual(plain(info.server), { version: null, commit: null });
});

test('label: the version, else a short commit, else the id as is, else null', () => {
  const B = load(undefined);
  assert.strictEqual(B.label({ version: 'v3.16.0', commit: 'abc1234def' }), 'v3.16.0');
  assert.strictEqual(B.label({ version: null, commit: 'abc1234def0123' }), 'abc1234');
  assert.strictEqual(B.label({ version: null, commit: 'build-1794206204' }), 'build-1794206204');
  assert.strictEqual(B.label({ version: null, commit: null }), null);
});

test('health() asks the API host once per page, without credentials across origins', async () => {
  const calls = [];
  const B = load({ channel: 'beta', apiBase: 'https://api.example', auth: false, commit: 'abc1234' }, (url, opts) => {
    calls.push([url, opts && opts.credentials]);
    return Promise.resolve({ ok: true, json: () => Promise.resolve({ version: 'v3.16.0', commit: 'abc1234' }) });
  });
  const [a, b] = await Promise.all([B.health(), B.health()]);
  assert.deepStrictEqual(plain(calls), [['https://api.example/api/health', 'omit']]);
  assert.strictEqual(a, b);
  assert.strictEqual(a.version, 'v3.16.0');
});

test('health() settles to null when the request fails, throws or is not ok', async () => {
  assert.strictEqual(await load(undefined, () => Promise.reject(new Error('offline'))).health(), null);
  assert.strictEqual(await load(undefined, () => { throw new TypeError('sync'); }).health(), null);
  assert.strictEqual(await load(undefined, () => Promise.resolve({ ok: false, json: () => Promise.resolve({}) })).health(), null);
});

chain.then(() => {
  console.log(`\nbuild-info: ${passed} passed, ${failed} failed`);
  if (failed) process.exit(1);
});
