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
    default: return { rows: [] };
  }
}

async function harness(initialTab = '') {
  let page;
  const requests = [], events = new Map();
  function element() {
    let html = '';
    return {
      get innerHTML() { return html; },
      set innerHTML(value) { html = value; this.writes++; },
      writes: 0, value: '', style: {}, listeners: {},
      classList: { add() {}, remove() {} },
      addEventListener(name, fn) { this.listeners[name] = fn; },
      querySelectorAll() { return []; },
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
    localStorage: { getItem: () => null },
    RegionFilter: filter(), AreaFilter: filter(),
    CLIENT_TTL: { analyticsRF: 1000 },
    initTabBar() {},
    registerPage(name, handlers) { assert.strictEqual(name, 'analytics'); page = handlers; },
    addEventListener(name, fn) { events.set(name, fn); },
    removeEventListener(name) { events.delete(name); },
    requestAnimationFrame: () => 0, cancelAnimationFrame() {},
    clearInterval() {}, clearTimeout() {},
    api(url) {
      return new Promise((resolve, reject) => requests.push({ url, resolve, reject }));
    },
  };
  sandbox.window = sandbox;
  vm.runInNewContext(source, sandbox, { filename: 'analytics.js' });
  async function init() {
    elements.analyticsContent = element();
    elements.analyticsContent.innerHTML = 'Loading analytics…';
    await page.init(element());
  }
  await init();
  return {
    page, elements, requests, init,
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

  console.log(`Analytics loading: ${passed} passed, ${failed} failed`);
  process.exitCode = failed ? 1 : 0;
})();
