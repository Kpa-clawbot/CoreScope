/* Exercise the real analytics page lifecycle with controllable API promises. */
'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const source = fs.readFileSync(path.join(__dirname, '../../public/analytics.js'), 'utf8');
const settle = () => new Promise(resolve => setImmediate(resolve));
const unhandled = [];
process.on('unhandledRejection', error => unhandled.push(error.message));

function fixture(url, total) {
  switch (url.split('?')[0]) {
    case '/analytics/rf': return { totalTransmissions: total, totalPackets: total, packetsPerHour: [], payloadTypes: [], snr: {}, rssi: {} };
    case '/analytics/topology': return { uniqueNodes: 0, hopDistribution: [] };
    case '/analytics/hash-sizes': return { total: 0, distribution: {}, hourly: [], topHops: [], multiByteNodes: [] };
    case '/analytics/channels': return { activeChannels: 0, decryptable: 0 };
    case '/nodes': return { nodes: [], total };
    case '/nodes/bulk-health':
    case '/api/nodes/clock-skew':
    case '/nodes/clock-skew': return [];
    case '/analytics/subpaths-bulk': return { results: Array.from({ length: 4 }, () => ({ subpaths: [] })) };
    case '/analytics/neighbor-graph': return { nodes: [], edges: [], stats: {} };
    case '/analytics/distance': return { status: 'building', retry_after_seconds: 5 };
    case '/observers/metrics/summary': return { observers: [] };
    case '/analytics/roles': return { roles: [], totalNodes: 0 };
    case '/scope-stats': return { summary: { transportTotal: total, scoped: 0, unscoped: total, unknownScope: 0 } };
    default: return { rows: [] };
  }
}

async function harness(initialTab = '') {
  let page;
  const requests = [], events = new Map(), timers = new Map(), frames = [];
  let timerId = 0;
  function element() {
    let html = '';
    return {
      get innerHTML() { return html; },
      set innerHTML(value) { html = value; this.writes++; },
      writes: 0, value: '', style: {}, listeners: {},
      classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
      addEventListener(name, fn) { this.listeners[name] = fn; },
      setAttribute() {},
      querySelectorAll() { return []; },
      querySelector(selector) { return elements[selector.slice(1)] || null; },
      getContext() { return { scale() {} }; },
    };
  }
  const elements = Object.fromEntries(['analyticsContent', 'analyticsTabs', 'analyticsTimeWindow', 'analyticsRegionFilter', 'analyticsAreaFilter'].map(id => [id, element()]));
  elements.analyticsTabs.querySelector = () => ({ classList: { add() {} } });
  function filter() {
    return { init() {}, onChange(fn) { this.change = fn; }, regionQueryString: () => '', areaQueryString: () => '' };
  }
  const sandbox = {
    window: null, console, URLSearchParams,
    document: { getElementById: id => elements[id] || null, querySelectorAll: () => [], documentElement: {} },
    location: { hash: '#/analytics' + (initialTab ? '?tab=' + initialTab : '') },
    history: { replaceState() {} },
    localStorage: { getItem: () => null, setItem() {} },
    sessionStorage: { getItem: () => null, setItem() {} },
    RegionFilter: filter(), AreaFilter: filter(),
    CLIENT_TTL: { analyticsRF: 1000 },
    initTabBar() {},
    registerPage(name, handlers) { assert.strictEqual(name, 'analytics'); page = handlers; },
    addEventListener(name, fn) { events.set(name, fn); },
    removeEventListener(name) { events.delete(name); },
    requestAnimationFrame(fn) { frames.push(fn); return frames.length; }, cancelAnimationFrame() {},
    setInterval(fn) { timers.set(++timerId, fn); return timerId; },
    setTimeout(fn) { const id = ++timerId; timers.set(id, () => { timers.delete(id); fn(); }); return id; },
    clearInterval(id) { timers.delete(id); }, clearTimeout(id) { timers.delete(id); },
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    getFavorites: () => [],
    api(url) {
      return new Promise((resolve, reject) => requests.push({ url, resolve, reject }));
    },
  };
  sandbox.fetchAllNodes = query => sandbox.api('/nodes?' + query);
  sandbox.fetch = url => sandbox.api(url).then(data => ({ ok: true, json: async () => data }));
  sandbox.window = sandbox;
  vm.runInNewContext(source, sandbox, { filename: 'analytics.js' });
  async function init() {
    elements.analyticsContent = element();
    elements.analyticsContent.innerHTML = 'Loading analytics…';
    await page.init(element());
  }
  await init();
  return {
    page, elements, requests, init, timers, frames,
    element,
    add(...ids) { ids.forEach(id => { elements[id] = element(); }); },
    favorites() { sandbox.getFavorites = () => ['aa'.repeat(32)]; },
    html: () => elements.analyticsContent.innerHTML,
    theme: () => events.get('theme-refresh')?.(),
    tab(tab) {
      const button = { dataset: { tab }, classList: { add() {} } };
      elements.analyticsTabs.listeners.click({ target: { closest: () => button } });
    },
    window(value) {
      elements.analyticsTimeWindow.value = value;
      elements.analyticsTimeWindow.listeners.change();
    },
    refresh: () => sandbox.RegionFilter.change(),
    async resolve(batch, total) {
      requests.slice(batch * 6, batch * 6 + 6).forEach(r => r.resolve(fixture(r.url, total)));
      await settle();
    },
    async reject(batch, message) {
      requests[batch * 6].reject(new Error(message));
      await settle();
    },
  };
}

let passed = 0, failed = 0;
async function test(name, fn) {
  const before = unhandled.length;
  try {
    try { await fn(); } finally { await settle(); }
    assert.deepStrictEqual(unhandled.slice(before), [], 'page lifecycle must not reject in the background');
    passed++;
    console.log('  ✓ ' + name);
  } catch (error) {
    failed++;
    console.error('  ✗ ' + name + ': ' + error.message.split('\n')[0]);
  }
}

(async () => {
  await test('theme refresh and every core tab wait for complete analytics', async () => {
    const h = await harness();
    h.theme();
    for (const tab of ['overview', 'rf', 'topology', 'channels', 'hashsizes', 'collisions']) {
      h.tab(tab);
      h.theme();
      assert.match(h.html(), /Loading analytics/);
    }
    assert.strictEqual(h.requests.length, 6, 'refreshes must not issue extra requests');
  });

  await test('ready data still renders and tabs and theme refresh keep working', async () => {
    const h = await harness();
    await h.resolve(0, 111);
    assert.match(h.html(), /stat-value">111</);
    h.tab('hashsizes');
    assert.match(h.html(), /Hash Size Distribution/);
    h.theme();
    assert.match(h.html(), /Hash Size Distribution/);
    h.tab('overview');
    h.theme();
    assert.match(h.html(), /stat-value">111</);
    assert.strictEqual(h.requests.length, 6);
  });

  await test('newer filter results win even when older requests finish last', async () => {
    const h = await harness();
    h.window('1h');
    assert.match(h.requests[7].url, /window=1h/);
    await h.resolve(1, 222);
    assert.match(h.html(), /stat-value">222</);
    await h.resolve(0, 111);
    h.theme();
    assert.match(h.html(), /stat-value">222</);
  });

  await test('an old failure cannot replace newer results', async () => {
    const h = await harness();
    h.refresh();
    await h.resolve(1, 222);
    await h.reject(0, 'stale failure');
    assert.match(h.html(), /stat-value">222</);
    h.theme();
    assert.match(h.html(), /stat-value">222</);
    assert.doesNotMatch(h.html(), /stale failure/);
  });

  await test('current errors are escaped and survive theme and core tab changes until retry', async () => {
    const h = await harness();
    await h.reject(0, '<img src=x onerror=alert(1)>');
    for (const tab of ['overview', 'hashsizes']) {
      h.tab(tab);
      h.theme();
      assert.match(h.html(), /Failed to load: &lt;img/);
      assert.doesNotMatch(h.html(), /<img/);
    }
    h.tab('overview');
    h.window('24h');
    assert.match(h.html(), /Loading analytics/);
    await h.resolve(1, 333);
    assert.match(h.html(), /stat-value">333</);
  });

  await test('a refresh replaces the previous core view with a loading state', async () => {
    const h = await harness();
    await h.resolve(0, 111);
    h.window('7d');
    h.theme();
    assert.match(h.html(), /Loading analytics/);
    assert.doesNotMatch(h.html(), /stat-value">111</);
  });

  await test('lazy tabs still render while the core requests are pending', async () => {
    const h = await harness();
    h.tab('my-repeaters');
    assert.doesNotMatch(h.html(), /Loading analytics/);
    assert.match(h.html(), /repeater/i);
    h.tab('overview');
    assert.match(h.html(), /Loading analytics/);
  });

  await test('deep-linked lazy tabs do not wait for the core requests', async () => {
    const h = await harness('my-repeaters');
    assert.match(h.html(), /repeater/i);
    assert.doesNotMatch(h.html(), /Loading analytics/);
    assert.strictEqual(h.requests.length, 6);
  });

  await test('core success or failure cannot rebuild an interactive lazy tab', async () => {
    for (const reject of [false, true]) {
      const h = await harness();
      h.tab('my-repeaters');
      const writes = h.elements.analyticsContent.writes;
      if (reject) await h.reject(0, 'core failure');
      else await h.resolve(0, 111);
      assert.strictEqual(h.elements.analyticsContent.writes, writes);
      h.tab('overview');
      assert.match(h.html(), reject ? /Failed to load: core failure/ : /stat-value">111</);
    }
  });

  await test('completion and rejection after destroy cannot render or throw', async () => {
    for (const reject of [false, true]) {
      const h = await harness();
      h.page.destroy();
      delete h.elements.analyticsContent;
      if (reject) await h.reject(0, 'left page');
      else await h.resolve(0, 111);
    }
  });

  await test('old results cannot overwrite a new analytics visit', async () => {
    const h = await harness();
    h.page.destroy();
    await h.init();
    await h.resolve(0, 111);
    assert.match(h.html(), /Loading analytics/);
    await h.resolve(1, 222);
    assert.match(h.html(), /stat-value">222</);
  });

  const lazyTabs = ['nodes', 'subpaths', 'my-repeaters', 'repeater-metrics', 'distance', 'neighbor-graph', 'rf-health', 'clock-health', 'prefix-tool', 'roles', 'scopes'];
  function addLazyElements(h) {
    h.favorites();
    h.add('hideCollisions', 'subpathDetail', 'ngRoleChecks', 'ngMinScore', 'ngMinScoreVal', 'ngConfidence', 'ngStats', 'ngCanvas', 'ngTextList',
      'rfHealthGrid', 'rfHealthDetail', 'scopes-cards', 'scopes-loading', 'scopes-tbody', 'scopes-chart', 'scopes-adverts-by-role');
  }
  async function complete(requests, reject = false, total = 1) {
    requests.forEach(r => reject ? r.reject(new Error('obsolete response')) : r.resolve(fixture(r.url, total)));
    await settle();
  }

  for (const tab of lazyTabs) {
    for (const reject of [false, true]) {
      await test(`${tab}: stale ${reject ? 'failure' : 'success'} cannot write or restart work after a tab switch`, async () => {
        const h = await harness();
        addLazyElements(h);
        const start = h.requests.length;
        h.tab(tab);
        const pending = h.requests.slice(start);
        assert.ok(pending.length, 'exercise the real lazy request');
        h.tab('overview');
        const writes = Object.values(h.elements).reduce((n, el) => n + el.writes, 0);
        const frames = h.frames.length;
        await complete(pending, reject);
        assert.match(h.html(), /Loading analytics/);
        assert.strictEqual(Object.values(h.elements).reduce((n, el) => n + el.writes, 0), writes, 'stale result must not mutate any view');
        assert.strictEqual(h.frames.length, frames, 'stale completion must not schedule layout or graph work');
        assert.strictEqual(h.timers.size, 0, 'stale completion must not restart retries or refresh timers');
      });
    }
  }

  await test('Nodes uses the newest filter response and ignores an older failure', async () => {
    for (const reject of [false, true]) {
      const h = await harness();
      h.tab('nodes');
      const old = h.requests.slice(6);
      const next = h.requests.length;
      h.refresh();
      await complete(h.requests.slice(next, next + 2), false, 222);
      assert.match(h.html(), />222</, h.html());
      await complete(old, reject, 111);
      assert.match(h.html(), />222</);
      assert.doesNotMatch(h.html(), /obsolete response|>111</);
    }
  });

  await test('destroyed lazy requests cannot write into detached content or a new visit', async () => {
    for (const tab of lazyTabs) {
      const h = await harness();
      addLazyElements(h);
      h.tab(tab);
      const pending = h.requests.slice(6);
      const previous = h.elements.analyticsContent;
      h.page.destroy();
      const writes = previous.writes;
      await h.init();
      const currentWrites = h.elements.analyticsContent.writes;
      await complete(pending);
      assert.strictEqual(previous.writes, writes, tab + ' must not write into the detached page');
      assert.strictEqual(h.elements.analyticsContent.writes, currentWrites, tab + ' must not write into the new page');
    }
  });

  await test('current lazy responses render normally and distance retries only while current', async () => {
    for (const tab of lazyTabs) {
      const h = await harness();
      addLazyElements(h);
      h.tab(tab);
      await complete(h.requests.slice(6));
      assert.doesNotMatch(h.html(), /Failed to load|Error loading|Loading node|Loading roles/);
      if (tab === 'distance') {
        assert.match(h.html(), /Building the distance index/);
        const retry = [...h.timers.values()][0];
        const before = h.requests.length;
        retry();
        assert.strictEqual(h.requests.length, before + 1);
        h.tab('overview');
        retry();
        assert.strictEqual(h.requests.length, before + 1, 'a queued stale retry cannot fetch again');
        await complete(h.requests.slice(before));
        assert.strictEqual(h.timers.size, 0);
      }
    }
  });

  await test('Roles refreshes are latest-wins and obsolete timer callbacks cannot restart work', async () => {
    const h = await harness('roles');
    await complete(h.requests.slice(0, 1));
    const refresh = [...h.timers.values()][0];
    assert.ok(refresh, 'current response starts the refresh timer');
    refresh();
    const old = h.requests.at(-1);
    refresh();
    const current = h.requests.at(-1);
    current.resolve({ totalNodes: 222, roles: [{ role: 'repeater', nodeCount: 222 }] });
    await settle();
    old.resolve({ totalNodes: 111, roles: [{ role: 'repeater', nodeCount: 111 }] });
    await settle();
    assert.match(h.html(), /<strong>222<\/strong>/);
    h.tab('overview');
    const before = h.requests.length;
    refresh();
    assert.strictEqual(h.requests.length, before);
    assert.strictEqual(h.timers.size, 0);
  });

  await test('subpath detail selections are latest-wins and stop after a filter change', async () => {
    const h = await harness();
    addLazyElements(h);
    h.tab('subpaths');
    await complete(h.requests.slice(6));
    function select(hops) {
      const row = h.element();
      row.dataset = { hops };
      h.elements.analyticsContent.onclick({ target: { closest: () => row } });
      return h.requests.at(-1);
    }
    function detail(count) {
      return { nodes: [], hops: ['aa', 'bb'], totalMatches: count, hourDistribution: [], signal: {}, observers: [], parentPaths: [] };
    }
    const old = select('aa,bb'), current = select('cc,dd');
    current.resolve(detail(222));
    await settle();
    old.reject(new Error('obsolete detail'));
    await settle();
    assert.match(h.elements.subpathDetail.innerHTML, /222 occurrences/);
    const pending = select('ee,ff');
    h.refresh();
    const writes = h.elements.subpathDetail.writes;
    pending.resolve(detail(111));
    await settle();
    assert.strictEqual(h.elements.subpathDetail.writes, writes);
  });

  await test('RF range and observer selections reject stale successes and failures', async () => {
    const h = await harness();
    addLazyElements(h);
    const range = h.element();
    range.dataset = { range: '1h' };
    h.elements.analyticsContent.querySelectorAll = selector => selector.includes('data-range') ? [range] : [];
    const cells = ['observer-a', 'observer-b'].map(id => Object.assign(h.element(), { dataset: { observer: id } }));
    h.elements.rfHealthGrid.querySelectorAll = () => cells;
    const summary = { observers: cells.map(c => ({ observer_id: c.dataset.observer, sparkline: [-110, -111], sample_count: 2 })) };
    h.tab('rf-health');
    const old = h.requests.at(-1);
    range.listeners.click();
    const current = h.requests.at(-1);
    current.resolve(summary);
    await settle();
    const gridWrites = h.elements.rfHealthGrid.writes;
    old.reject(new Error('obsolete summary'));
    await settle();
    assert.strictEqual(h.elements.rfHealthGrid.writes, gridWrites);
    cells[0].listeners.click();
    const firstDetail = h.requests.at(-1);
    cells[1].listeners.click();
    const secondDetail = h.requests.at(-1);
    secondDetail.resolve({ observer_name: 'Newest observer', metrics: [] });
    await settle();
    firstDetail.resolve({ observer_name: 'Old observer', metrics: [] });
    await settle();
    assert.match(h.elements.rfHealthDetail.innerHTML, /Newest observer/);
    cells[0].listeners.click();
    const pending = h.requests.at(-1);
    range.dataset.range = 'custom';
    range.listeners.click();
    const detailWrites = h.elements.rfHealthDetail.writes;
    pending.reject(new Error('obsolete range detail'));
    await settle();
    assert.strictEqual(h.elements.rfHealthDetail.writes, detailWrites);
  });

  await test('Scopes keeps current window handlers after refresh and ignores overlapping requests', async () => {
    const h = await harness();
    addLazyElements(h);
    const button = Object.assign(h.element(), { dataset: { win: '1h' } });
    h.elements.analyticsContent.querySelectorAll = selector => selector === '[data-win]' ? [button] : [];
    h.tab('scopes');
    const old = h.requests.at(-1);
    h.theme();
    button.onclick();
    const current = h.requests.at(-1);
    assert.match(current.url, /window=1h/);
    current.resolve(fixture(current.url, 222));
    await settle();
    old.reject(new Error('obsolete scopes'));
    await settle();
    assert.match(h.elements['scopes-cards'].innerHTML, />222</);
    assert.strictEqual(h.timers.size, 1);
    const timer = [...h.timers.values()][0];
    timer();
    assert.match(h.requests.at(-1).url, /window=1h/, 'timer uses the selected window from the current handler');
    h.page.destroy();
    const before = h.requests.length;
    timer();
    assert.strictEqual(h.requests.length, before);
  });

  await test('queued graph debounce and animation callbacks are harmless after leaving', async () => {
    const h = await harness();
    addLazyElements(h);
    h.tab('neighbor-graph');
    await complete(h.requests.slice(6));
    const slider = h.elements.ngMinScore;
    slider.value = '80';
    slider.listeners.input.call(slider);
    const debounce = [...h.timers.values()][0];
    const frames = h.frames.slice();
    h.page.destroy();
    delete h.elements.analyticsContent;
    delete h.elements.ngCanvas;
    debounce();
    frames.forEach(fn => fn());
    assert.strictEqual(h.frames.length, frames.length, 'old callbacks cannot schedule more animation or layout');
  });

  console.log(`Analytics loading: ${passed} passed, ${failed} failed`);
  process.exitCode = failed ? 1 : 0;
})();
