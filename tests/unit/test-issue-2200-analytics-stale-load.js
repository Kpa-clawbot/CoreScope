/**
 * #2200: the Analytics bulk load must only render for the current page visit
 * and the current filter selection. Loads the real public/analytics.js in a
 * vm sandbox with a held api() so every race runs deterministically.
 *
 * Usage: node tests/unit/test-issue-2200-analytics-stale-load.js
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const SRC = fs.readFileSync(path.join(__dirname, '..', '..', 'public/analytics.js'), 'utf8');
let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}
const rejections = [];
process.on('unhandledRejection', (e) => rejections.push(e));
const flush = () => new Promise((r) => setTimeout(r, 10));

function el() {
  const e = {
    style: {}, dataset: {}, value: '', innerHTML: '', listeners: {},
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
    addEventListener(t, fn) { e.listeners[t] = fn; }, removeEventListener() {},
    querySelector: () => null, querySelectorAll: () => [],
  };
  return e;
}

// One page module per case, so module state never leaks between cases.
function load(hash) {
  const els = {};
  const calls = [];
  const nav = { gen: 0 };
  const ctx = {
    console, Math, JSON, Date, Number, String, Array, Object, Set, Map, RegExp, URLSearchParams, Promise,
    setTimeout, clearTimeout, requestAnimationFrame: () => 0,
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    location: { hash: hash || '#/analytics?tab=hashsizes' },
    addEventListener() {}, removeEventListener() {},
    initTabBar() {}, CLIENT_TTL: {},
    viewGeneration: () => nav.gen,
    api: (url) => new Promise((resolve, reject) => calls.push({ url, resolve, reject })),
    fetchAllNodes: () => ctx.api('/nodes'),
    RegionFilter: { init() {}, onChange: (fn) => fn, offChange() {}, regionQueryString: () => '' },
    AreaFilter: { init() {}, onChange: (fn) => fn, offChange() {}, areaQueryString: () => '' },
  };
  let page = null;
  ctx.registerPage = (name, p) => { if (name === 'analytics') page = p; };
  ctx.window = ctx;
  ctx.document = {
    documentElement: {}, createElement: el,
    addEventListener() {}, removeEventListener() {}, querySelector: () => null, querySelectorAll: () => [],
    getElementById: (id) => els[id] || (els[id] = el()),
  };
  // The deep-linked tab button, so ?tab= selects the tab as in the browser.
  els.analyticsTabs = el();
  els.analyticsTabs.querySelector = () => ({ classList: { add() {}, remove() {} } });
  vm.createContext(ctx);
  vm.runInContext(SRC, ctx);
  const content = () => els.analyticsContent.innerHTML;
  // The seven requests of bulk load k (0-based): the six analytics loads
  // plus the MediaMesh (radio2) summary for the Overview.
  const batch = (k) => calls.slice(k * 7, k * 7 + 7);
  function answer(k, total) {
    for (const c of batch(k)) {
      if (c.url.startsWith('/analytics/hash-sizes')) c.resolve({ distribution: { 1: total }, total, hourly: [], topHops: [], multiByteNodes: [] });
      else c.resolve({});
    }
  }
  function fail(k, msg) { batch(k)[0].reject(new Error(msg)); }
  function changeWindow(v) { els.analyticsTimeWindow.value = v; els.analyticsTimeWindow.listeners.change(); }
  function clickTab(tab) {
    const btn = { dataset: { tab }, classList: { add() {}, remove() {} } };
    els.analyticsTabs.listeners.click({ target: { closest: () => btn } });
  }
  return { page, els, calls, nav, content, batch, answer, fail, changeWindow, clickTab };
}

(async () => {
  console.log('Analytics: a newer selection beats an older one');
  {
    const t = load();
    await t.page.init(el());
    t.changeWindow('7d');
    assert(t.batch(1).length === 7 && t.batch(1)[1].url.includes('window=7d'), 'the window change starts a second bulk load');
    t.answer(1, 222);
    await flush();
    assert(t.content().includes('222 packets'), 'the newer selection renders when it lands first');
    t.answer(0, 111);
    await flush();
    assert(t.content().includes('222 packets') && !t.content().includes('111 packets'),
      'the older selection landing last does not replace it');
  }

  console.log('Analytics: a late rejection of an older selection');
  {
    const t = load();
    await t.page.init(el());
    t.changeWindow('24h');
    t.answer(1, 333);
    await flush();
    t.fail(0, 'old batch failed');
    await flush();
    assert(t.content().includes('333 packets') && !t.content().includes('Failed to load'),
      'does not replace the newer results with an error');
  }

  console.log('Analytics: leaving and re-entering');
  {
    const t = load();
    await t.page.init(el());
    t.page.destroy();
    t.nav.gen++;
    t.els.analyticsContent.innerHTML = 'NEXT PAGE';
    t.answer(0, 444);
    await flush();
    assert(t.content() === 'NEXT PAGE', 'a load from a left visit does not render');
    t.nav.gen++;
    await t.page.init(el());
    t.fail(0, 'late');
    t.answer(1, 555);
    await flush();
    assert(t.content().includes('555 packets'), 'the re-entered visit renders its own load');
  }
  {
    const t = load();
    await t.page.init(el());
    t.page.destroy();
    t.nav.gen++;
    t.els.analyticsContent.innerHTML = 'NEXT PAGE';
    t.fail(0, 'network down');
    await flush();
    assert(t.content() === 'NEXT PAGE', 'an error from a left visit does not render');
  }

  console.log('Analytics: the current load still renders its error');
  {
    const t = load();
    await t.page.init(el());
    t.fail(0, 'server <down>');
    await flush();
    assert(t.content().includes('Failed to load: server &lt;down&gt;'), 'the current rejection renders, escaped');
    t.clickTab('overview');
    await flush();
    assert(t.content().includes('Failed to load'), 'a data tab clicked after the error keeps the error');
  }

  console.log('Analytics: a tab click before the bulk load lands');
  {
    const t = load('#/analytics');
    await t.page.init(el());
    const before = rejections.length;
    t.clickTab('overview');
    await flush();
    t.clickTab('rf');
    await flush();
    assert(rejections.length === before, 'Overview/RF do not throw on missing data (' + (rejections[before] || 'ok') + ')');
    assert(t.content().includes('Loading analytics'), 'and keep the loading state');
    t.clickTab('hashsizes');
    t.answer(0, 666);
    await flush();
    assert(t.content().includes('666 packets'), 'the load renders the active tab once it lands');
  }

  // A tab that loads its own data (here the Prefix Tool) is not the bulk
  // load's to render: it draws at once and stays when the bulk load lands.
  const BULK = /^\/analytics\/(hash-sizes|rf|topology|channels|hash-collisions|relay-airtime-share|mediamesh)\b/;
  const bulkCalls = (t) => t.calls.filter((c) => BULK.test(c.url));
  const ownCalls = (t) => t.calls.filter((c) => !BULK.test(c.url));
  const answerBulk = (t, total) => {
    for (const c of bulkCalls(t)) {
      c.resolve(c.url.startsWith('/analytics/hash-sizes')
        ? { distribution: { 1: total }, total, hourly: [], topHops: [], multiByteNodes: [] } : {});
    }
  };

  console.log('Analytics: a tab with its own data and the bulk load');
  {
    const t = load('#/analytics?tab=prefix-tool');
    await t.page.init(el());
    assert(t.content().includes('Loading prefix data'), 'a deep-linked own-data tab renders without waiting for the bulk load');
    bulkCalls(t)[0].reject(new Error('bulk failed'));
    await flush();
    assert(!t.content().includes('Failed to load'), 'a failed bulk load does not replace it with an error');
  }
  {
    const t = load('#/analytics?tab=prefix-tool');
    await t.page.init(el());
    const own = ownCalls(t).length;
    answerBulk(t, 7);
    await flush();
    assert(ownCalls(t).length === own, 'a successful bulk load does not re-render it (no new fetch)');
    assert(!t.content().includes('7 packets'), 'and does not draw a bulk tab over it');
  }
  {
    const t = load('#/analytics?tab=prefix-tool');
    await t.page.init(el());
    // The Prefix Tool fetches /analytics/hash-sizes too, so count /analytics/rf.
    const rfLoads = () => t.calls.filter((c) => c.url.startsWith('/analytics/rf')).length;
    const own = ownCalls(t).length, rf = rfLoads();
    t.changeWindow('7d');
    assert(ownCalls(t).length > own, 'a filter change re-renders it for the new filters');
    assert(rfLoads() === rf + 1, 'and reloads the bulk data');
  }
  {
    const t = load('#/analytics');
    await t.page.init(el());
    t.clickTab('prefix-tool');
    await flush();
    answerBulk(t, 8);
    await flush();
    assert(!t.content().includes('8 packets'), 'switching to it while the bulk load is pending: the load does not draw Overview over it');
  }

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})();
