/* #2218: exercise the shipped Node Analytics lifecycle with controllable requests. */
'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const source = fs.readFileSync(path.join(__dirname, '../../public/node-analytics.js'), 'utf8');
const settle = () => new Promise(resolve => setImmediate(resolve));
const unhandled = [];
process.on('unhandledRejection', error => unhandled.push(error.message));

function analytics(name = 'Current node') {
  return {
    node: { name, public_key: 'aa'.repeat(32), role: 'repeater' },
    computedStats: {}, peerInteractions: [], activityTimeline: [], snrTrend: [],
    packetTypeBreakdown: [], observerCoverage: [], hopDistribution: [], uptimeHeatmap: [],
  };
}
function battery(mv = 3800) {
  return { status: 'ok', latest_mv: mv, samples: [{ timestamp: '2026-01-01T00:00:00Z', battery_mv: mv }] };
}

function harness() {
  const requests = [], chartInstances = [], hopLoads = [];
  let page, elements = {}, buttons = [];
  const container = { isConnected: true };
  function element(id) {
    return {
      id, innerHTML: '', textContent: '', style: {}, dataset: {}, listeners: {},
      get isConnected() { return container.isConnected && elements[id] === this; },
      addEventListener(name, fn) { this.listeners[name] = fn; },
    };
  }
  let html = '';
  Object.defineProperty(container, 'innerHTML', {
    get() { return html; },
    set(value) {
      html = value;
      elements = Object.fromEntries(Array.from(value.matchAll(/id="([^"]+)"/g), m => [m[1], element(m[1])]));
      buttons = value.includes('id="timeRangeBtns"') ? [1, 7, 30, 365].map(days => {
        const btn = element('range-' + days);
        elements[btn.id] = btn;
        btn.dataset.days = String(days);
        return btn;
      }) : [];
    },
  });
  container.querySelectorAll = selector => selector === '#timeRangeBtns button' ? buttons : [];
  class Chart {
    constructor(canvas, config) { this.canvas = canvas; this.config = config; chartInstances.push(this); }
    destroy() { this.destroyed = true; }
  }
  Chart.defaults = {};
  const sandbox = {
    console, Chart, Date, CLIENT_TTL: { nodeAnalytics: 1000 },
    document: { documentElement: {}, getElementById: id => elements[id] || null },
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    escapeHtml: value => String(value), timeAgo: () => 'now',
    NodeHopAnalytics: { load: (...args) => hopLoads.push(args), destroy() {} },
    registerPage(name, handlers) { assert.strictEqual(name, 'node-analytics'); page = handlers; },
    api(url) { return new Promise((resolve, reject) => requests.push({ url, resolve, reject })); },
  };
  vm.runInNewContext(source, sandbox, { filename: 'node-analytics.js' });
  return {
    page, container, requests, chartInstances, hopLoads,
    init: (key = 'aa') => page.init(container, key.repeat(32) + '/analytics'),
    element: id => elements[id],
    button: days => buttons.find(btn => btn.dataset.days === String(days)),
    async resolve(request, data) { request.resolve(data); await settle(); },
    async reject(request, message) { request.reject(new Error(message)); await settle(); },
  };
}

let passed = 0, failed = 0;
async function test(name, run) {
  const before = unhandled.length;
  try {
    await run();
    await settle();
    assert.deepStrictEqual(unhandled.slice(before), [], 'no background rejection');
    passed++;
    console.log('  ✓ ' + name);
  } catch (error) {
    failed++;
    console.error('  ✗ ' + name + ': ' + error.message.split('\n')[0]);
  }
}

(async () => {
  for (const outcome of ['success', 'failure']) {
    await test('late analytics ' + outcome + ' cannot replace the page after leaving', async () => {
      const h = harness(); h.init(); h.page.destroy(); h.container.innerHTML = 'Nodes list';
      if (outcome === 'success') await h.resolve(h.requests[0], analytics('Old node'));
      else await h.reject(h.requests[0], 'old error');
      assert.strictEqual(h.container.innerHTML, 'Nodes list');
      assert.strictEqual(h.chartInstances.length, 0);
      assert.strictEqual(h.hopLoads.length, 0);
      assert.strictEqual(h.requests.length, 1, 'stale main response must not start battery requests');
    });
    await test('late analytics ' + outcome + ' cannot replace a remounted same node', async () => {
      const h = harness(); h.init(); h.page.destroy(); h.init();
      await h.resolve(h.requests[1], analytics('New render'));
      const current = h.container.innerHTML;
      if (outcome === 'success') await h.resolve(h.requests[0], analytics('Old render'));
      else await h.reject(h.requests[0], 'old error');
      assert.strictEqual(h.container.innerHTML, current);
      assert.strictEqual(h.chartInstances.length, 5);
    });
    await test('late battery ' + outcome + ' cannot alter the newer range', async () => {
      const h = harness(); h.init(); await h.resolve(h.requests[0], analytics());
      const oldBattery = h.requests[1];
      h.button(30).listeners.click();
      await h.resolve(h.requests[2], analytics('New range'));
      await h.resolve(h.requests[3], battery(4000));
      const currentBadge = h.element('batteryStatusBadge').textContent;
      const currentEmpty = h.element('batteryEmpty').textContent;
      const count = h.chartInstances.length;
      if (outcome === 'success') await h.resolve(oldBattery, battery(3200));
      else await h.reject(oldBattery, 'old battery error');
      assert.strictEqual(h.element('batteryStatusBadge').textContent, currentBadge);
      assert.strictEqual(h.element('batteryEmpty').textContent, currentEmpty);
      assert.strictEqual(h.chartInstances.length, count);
    });
    await test('late battery ' + outcome + ' cannot alter another page with matching IDs', async () => {
      const h = harness(); h.init(); await h.resolve(h.requests[0], analytics()); h.page.destroy();
      h.container.innerHTML = '<canvas id="batteryChart"></canvas><div id="batteryStatusBadge"></div><div id="batteryEmpty"></div>';
      if (outcome === 'success') await h.resolve(h.requests[1], battery(3200));
      else await h.reject(h.requests[1], 'old battery error');
      assert.strictEqual(h.element('batteryStatusBadge').textContent, '');
      assert.strictEqual(h.element('batteryEmpty').textContent, '');
      assert.strictEqual(h.chartInstances.length, 5);
    });
  }
  await test('switching node keeps the newer response and chart ownership', async () => {
    const h = harness(); h.init(); h.page.destroy(); h.init('bb');
    await h.resolve(h.requests[1], analytics('Node B'));
    await h.resolve(h.requests[0], analytics('Node A'));
    assert.match(h.container.innerHTML, /Node B/);
    assert.doesNotMatch(h.container.innerHTML, /Node A/);
    assert.strictEqual(h.chartInstances.length, 5);
    assert.strictEqual(h.requests.length, 3);
  });
  await test('disconnected main and battery targets do not render', async () => {
    const h = harness(); h.init(); h.container.isConnected = false;
    await h.resolve(h.requests[0], analytics());
    assert.strictEqual(h.chartInstances.length, 0);
    const live = harness(); live.init(); await live.resolve(live.requests[0], analytics());
    live.container.isConnected = false;
    await live.resolve(live.requests[1], battery());
    assert.strictEqual(live.chartInstances.length, 5);
  });
  await test('old range callbacks cannot start requests or destroy current charts', async () => {
    const h = harness(); h.init(); await h.resolve(h.requests[0], analytics());
    const oldClick = h.button(1).listeners.click;
    h.button(30).listeners.click(); await h.resolve(h.requests[2], analytics('New range'));
    const count = h.requests.length;
    const activeCharts = h.chartInstances.filter(chart => !chart.destroyed);
    oldClick(); await settle();
    assert.strictEqual(h.requests.length, count);
    assert.ok(activeCharts.every(chart => !chart.destroyed));
    h.page.destroy(); oldClick(); await settle();
    assert.strictEqual(h.requests.length, count);
  });
  await test('current charts and main/battery errors still render', async () => {
    const h = harness(); h.init(); await h.resolve(h.requests[0], analytics());
    await h.resolve(h.requests[1], battery());
    assert.strictEqual(h.chartInstances.length, 6);
    assert.strictEqual(h.hopLoads.length, 1);
    assert.match(h.element('batteryStatusBadge').textContent, /3800 mV/);
    h.page.destroy(); assert.ok(h.chartInstances.every(chart => chart.destroyed));
    const mainError = harness(); mainError.init(); await mainError.reject(mainError.requests[0], 'current error');
    assert.match(mainError.container.innerHTML, /Failed to load analytics: current error/);
    const batteryError = harness(); batteryError.init(); await batteryError.resolve(batteryError.requests[0], analytics());
    await batteryError.reject(batteryError.requests[1], 'current battery error');
    assert.match(batteryError.element('batteryEmpty').textContent, /current battery error/);
  });
  console.log(`\nNode Analytics lifecycle: ${passed} passed, ${failed} failed`);
  process.exitCode = failed ? 1 : 0;
})();
