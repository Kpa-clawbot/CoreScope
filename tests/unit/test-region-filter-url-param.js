/**
 * RegionFilter reads ?region= from the page's hash URL.
 *
 * Only the packets page honored ?region= (with its own one-off code); every
 * other page with a region filter (live, nodes, observers, channels,
 * analytics, scope audit) ignored it, so a link like #/live?region=SJC opened
 * with whatever region the visitor last picked. RegionFilter now applies the
 * param itself: it overrides the saved selection and is saved like a click in
 * the control. On a fresh load it's in place when the script runs, before any
 * page reads it (live fetches nodes before calling init). On in-app
 * navigation init() applies it and, if that changed the selection, notifies
 * listeners so a page that already fetched reloads. Unknown codes are dropped
 * once regions load.
 */
'use strict';
const REPO_ROOT = require('node:path').resolve(__dirname, '..', '..');
const vm = require('node:vm');
const fs = require('node:fs');
const assert = require('node:assert');

let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log(`  ✓ ${name}`); }
  catch (e) { failed++; console.error(`  ✗ ${name}: ${e.message}`); }
}

const REGIONS = { SJC: 'San Jose', SFO: 'San Francisco', OAK: 'Oakland' };

function buildCtx(hash, initialStorage) {
  const storage = Object.assign(Object.create(null), initialStorage || {});
  const ctx = {
    window: {},
    location: { hash: hash || '' },
    document: { addEventListener() {}, removeEventListener() {} },
    localStorage: {
      getItem: (k) => (k in storage ? storage[k] : null),
      setItem: (k, v) => { storage[k] = String(v); },
      removeItem: (k) => { delete storage[k]; },
    },
    fetch: async (url) => ({ json: async () => (url === '/api/config/regions' ? REGIONS : {}) }),
    URLSearchParams, console, setTimeout, clearTimeout,
  };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(REPO_ROOT + '/public/region-filter.js', 'utf8'), ctx);
  return { rf: ctx.window.RegionFilter, storage, location: ctx.location };
}

function container() {
  return { innerHTML: '', style: {}, querySelector: () => null, contains: () => false };
}

(async () => {
  console.log('RegionFilter applies a ?region= URL param');

  await test('URL region overrides the saved selection', async () => {
    const { rf } = buildCtx('#/live?region=SJC', { 'meshcore-region-filter': '["SFO"]' });
    await rf.init(container());
    assert.deepStrictEqual(Array.from(rf.getSelected()), ['SJC']);
  });

  await test('comma-separated codes are all selected, whitespace trimmed', async () => {
    const { rf } = buildCtx('#/nodes?region=SJC,%20SFO');
    await rf.init(container());
    assert.deepStrictEqual(Array.from(rf.getSelected()), ['SJC', 'SFO']);
  });

  await test('fresh load: selection is in place before init is even called', () => {
    const { rf } = buildCtx('#/live?region=SFO', { 'meshcore-region-filter': '["SJC"]' });
    assert.deepStrictEqual(Array.from(rf.getSelected()), ['SFO']);
    assert.strictEqual(rf.regionQueryString(), '&region=SFO');
  });

  await test('fresh load: init does not notify (nothing changed since the script ran)', async () => {
    const { rf } = buildCtx('#/live?region=SFO');
    const calls = [];
    const p = rf.init(container());
    rf.onChange((sel) => calls.push(sel && Array.from(sel)));
    await p;
    assert.deepStrictEqual(calls, []);
  });

  await test('in-app navigation: a changed region notifies listeners once regions load', async () => {
    const { rf, location } = buildCtx('#/packets', { 'meshcore-region-filter': '["SJC"]' });
    location.hash = '#/live?region=OAK';
    const calls = [];
    // live registers its listener right after calling init(), without awaiting
    const p = rf.init(container());
    rf.onChange((sel) => calls.push(sel && Array.from(sel)));
    assert.deepStrictEqual(Array.from(rf.getSelected()), ['OAK']);
    await p;
    assert.deepStrictEqual(calls, [['OAK']]);
  });

  await test('in-app navigation: the same region as already selected does not notify', async () => {
    const { rf, location } = buildCtx('#/packets', { 'meshcore-region-filter': '["OAK"]' });
    location.hash = '#/live?region=OAK';
    const calls = [];
    const p = rf.init(container());
    rf.onChange((sel) => calls.push(sel && Array.from(sel)));
    await p;
    assert.deepStrictEqual(calls, []);
  });

  await test('selection is in place before init resolves (callers do not await it)', () => {
    const { rf } = buildCtx('#/nodes?region=OAK');
    rf.init(container());
    assert.deepStrictEqual(Array.from(rf.getSelected()), ['OAK']);
    assert.strictEqual(rf.regionQueryString(), '&region=OAK');
  });

  await test('URL selection is saved like a click in the control', async () => {
    const { rf, storage } = buildCtx('#/live?region=SFO');
    await rf.init(container());
    assert.strictEqual(storage['meshcore-region-filter'], '["SFO"]');
  });

  await test('unknown codes are dropped once regions load', async () => {
    const { rf } = buildCtx('#/live?region=SJC,XXX');
    await rf.init(container());
    assert.deepStrictEqual(Array.from(rf.getSelected()), ['SJC']);
  });

  // fetchRegions() only cleans the selection on its first call, so on in-app
  // navigation applyUrlRegion() must drop unknown codes itself.
  await test('in-app navigation: unknown codes are dropped', async () => {
    const { rf, location, storage } = buildCtx('#/packets');
    await rf.init(container());
    location.hash = '#/nodes?region=SJC,XXX';
    await rf.init(container());
    assert.deepStrictEqual(Array.from(rf.getSelected()), ['SJC']);
    assert.strictEqual(rf.regionQueryString(), '&region=SJC');
    assert.strictEqual(storage['meshcore-region-filter'], '["SJC"]');
  });

  await test('in-app navigation: only unknown codes means all regions', async () => {
    const { rf, location, storage } = buildCtx('#/packets', { 'meshcore-region-filter': '["SFO"]' });
    await rf.init(container());
    location.hash = '#/nodes?region=XXX';
    await rf.init(container());
    assert.strictEqual(rf.getSelected(), null);
    assert.strictEqual(rf.regionQueryString(), '');
    assert.ok(!('meshcore-region-filter' in storage));
  });

  await test('no region param keeps the saved selection', async () => {
    const { rf } = buildCtx('#/live?node=abc', { 'meshcore-region-filter': '["SFO"]' });
    await rf.init(container());
    assert.deepStrictEqual(Array.from(rf.getSelected()), ['SFO']);
  });

  await test('an empty region param keeps the saved selection', async () => {
    const { rf } = buildCtx('#/live?region=', { 'meshcore-region-filter': '["SFO"]' });
    await rf.init(container());
    assert.deepStrictEqual(Array.from(rf.getSelected()), ['SFO']);
  });

  await test('no hash at all keeps "all regions"', async () => {
    const { rf } = buildCtx('');
    await rf.init(container());
    assert.strictEqual(rf.getSelected(), null);
  });

  // packets.js registered its region/area listeners without keeping a handle,
  // so they outlived the page: after visiting packets, a region change on any
  // other page reloaded packets and replaceState'd the URL to #/packets. The
  // in-app notify above makes that fire on every region link, so packets must
  // remove both listeners in destroy().
  console.log('packets.js removes its region/area listeners on destroy');
  const pktSrc = fs.readFileSync(REPO_ROOT + '/public/packets.js', 'utf8');
  const destroySrc = pktSrc.slice(pktSrc.indexOf('  function destroy() {'), pktSrc.indexOf('\n  }\n', pktSrc.indexOf('  function destroy() {')));

  await test('listeners are registered with a kept handle', () => {
    assert.match(pktSrc, /_regionChangeHandler = RegionFilter\.onChange\(/);
    assert.match(pktSrc, /_areaChangeHandler = AreaFilter\.onChange\(/);
  });

  await test('destroy() removes both listeners', () => {
    assert.match(destroySrc, /RegionFilter\.offChange\(_regionChangeHandler\)/);
    assert.match(destroySrc, /AreaFilter\.offChange\(_areaChangeHandler\)/);
  });

  console.log(`\n${passed} passed, ${failed} failed`);
  if (failed) process.exit(1);
})();
