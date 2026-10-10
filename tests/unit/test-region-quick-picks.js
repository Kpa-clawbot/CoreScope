/**
 * Region quick picks: config.json names groups of region (IATA) codes
 * (regionQuickPicks) and the shared region filter offers each as a one-tap
 * choice. Runs the real public/region-filter.js in jsdom against faked
 * /api/config/regions and /api/config/region-quick-picks responses.
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const fs = require('fs');
const assert = require('assert');
const { JSDOM } = require('jsdom');

const SRC = fs.readFileSync(REPO_ROOT + '/public/region-filter.js', 'utf8');
let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}

const SIX = { SFO: 'San Francisco', SJC: 'San Jose', LAX: 'Los Angeles', BRU: 'Brussels', ANR: 'Antwerp', MRY: 'Monterey' };
const PICKS = [
  { name: 'California', description: 'California observers', regions: ['SFO', 'SJC', 'LAX', 'OAK'] }, // OAK has no observer
  { name: 'Belgium', regions: ['BRU', 'ANR'] },
  { name: 'Nowhere', regions: ['XXX'] },                                                       // no observer at all
];

async function setup(regions, picks, opts) {
  opts = opts || {};
  const dom = new JSDOM('<!doctype html><body><div id="rf"></div></body>', { url: 'http://localhost/', runScripts: 'outside-only' });
  const w = dom.window;
  if (opts.stored) w.localStorage.setItem('meshcore-region-filter', JSON.stringify(opts.stored));
  w.fetch = async (url) => {
    if (String(url).indexOf('region-quick-picks') !== -1) {
      if (opts.picksFail) throw new Error('offline');
      return { json: async () => ({ quickPicks: picks }) };
    }
    return { json: async () => regions };
  };
  w.eval(SRC);
  const rf = w.RegionFilter;
  const calls = [];
  rf.onChange((sel) => calls.push(sel === null ? null : Array.from(sel).sort()));
  const el = w.document.getElementById('rf');
  await rf.init(el, opts.dropdown ? { dropdown: true } : undefined);
  return { w, rf, el, calls };
}
const pickButtons = (el) => Array.from(el.querySelectorAll('.region-quick-pick'));
function pick(el, name) {
  const b = pickButtons(el).find((x) => x.textContent.trim() === name);
  assert.ok(b, 'expected a quick pick button named ' + JSON.stringify(name));
  return b;
}
const trigger = (el) => el.querySelector('.region-dropdown-trigger');

(async () => {
  console.log('Region quick picks');

  await test('configured picks render, and a pick with no observer today is left out', async () => {
    const { el } = await setup(SIX, PICKS);
    assert.deepStrictEqual(pickButtons(el).map((b) => b.textContent.trim()), ['California', 'Belgium']);
  });

  await test('a pick selects exactly its codes that have an observer, and the page re-queries once', async () => {
    const { el, rf, calls } = await setup(SIX, PICKS);
    pick(el, 'California').click();
    assert.deepStrictEqual(Array.from(rf.getSelected()).sort(), ['LAX', 'SFO', 'SJC']);
    assert.deepStrictEqual(calls, [['LAX', 'SFO', 'SJC']]);
  });

  await test('the selector names the active pick instead of counting regions', async () => {
    const { el } = await setup(SIX, PICKS);
    pick(el, 'California').click();
    assert.ok(trigger(el).textContent.indexOf('California') !== -1, 'trigger: ' + trigger(el).textContent);
    assert.strictEqual(pick(el, 'California').getAttribute('aria-pressed'), 'true');
    assert.strictEqual(pick(el, 'Belgium').getAttribute('aria-pressed'), 'false');
  });

  await test('tapping the active pick again goes back to all regions', async () => {
    const { el, rf, calls } = await setup(SIX, PICKS);
    pick(el, 'California').click();
    pick(el, 'California').click();
    assert.strictEqual(rf.getSelected(), null);
    assert.deepStrictEqual(calls, [['LAX', 'SFO', 'SJC'], null]);
  });

  await test('selecting the same codes by hand marks the pick as active', async () => {
    const { el } = await setup(SIX, PICKS, { stored: ['BRU', 'ANR'] });
    assert.strictEqual(pick(el, 'Belgium').getAttribute('aria-pressed'), 'true');
    assert.ok(trigger(el).textContent.indexOf('Belgium') !== -1);
  });

  await test('picks are offered in the pill layout too (four regions or fewer)', async () => {
    const { el, rf } = await setup({ SFO: 'San Francisco', SJC: 'San Jose', BRU: 'Brussels' }, PICKS);
    assert.ok(el.querySelector('.region-filter-bar'), 'pill layout expected');
    pick(el, 'California').click();
    assert.deepStrictEqual(Array.from(rf.getSelected()).sort(), ['SFO', 'SJC']);
  });

  await test('without configured picks the filter renders exactly as before', async () => {
    const { el } = await setup(SIX, []);
    assert.strictEqual(pickButtons(el).length, 0);
    assert.strictEqual(el.querySelectorAll('.region-quick-picks').length, 0);
    assert.ok(trigger(el), 'dropdown still rendered');
  });

  await test('if the picks cannot be fetched the filter still works', async () => {
    const { el } = await setup(SIX, PICKS, { picksFail: true });
    assert.strictEqual(pickButtons(el).length, 0);
    assert.ok(trigger(el));
  });

  await test('pick names and descriptions are escaped', async () => {
    const { el } = await setup(SIX, [{ name: '<img src=x onerror=alert(1)>', description: '"><b>x</b>', regions: ['SFO'] }]);
    assert.strictEqual(el.querySelectorAll('img').length, 0);
    assert.strictEqual(el.querySelectorAll('b').length, 0);
    assert.strictEqual(pickButtons(el).length, 1, 'expected the pick to render');
    assert.strictEqual(pickButtons(el)[0].textContent.trim(), '<img src=x onerror=alert(1)>');
  });

  console.log(`\n${passed} passed, ${failed} failed`);
  if (failed) process.exit(1);
})();
