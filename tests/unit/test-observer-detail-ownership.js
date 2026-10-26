/* #2316: exercise the real page lifecycle with explicitly ordered API completions. */
'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

function harness() {
  const elements = new Map();
  const requests = [];
  const charts = [];
  let page;
  function element(id) {
    const el = { textContent: '', firstChild: { textContent: '' }, listeners: {},
      addEventListener(name, fn) { this.listeners[name] = fn; }, setAttribute() {} };
    let html = '';
    Object.defineProperty(el, 'innerHTML', {
      get() { return html; },
      set(value) {
        html = value;
        el.firstChild = { textContent: '' };
        for (const match of value.matchAll(/id="([^"]+)"/g)) elements.set(match[1], element(match[1]));
      },
    });
    return el;
  }
  const app = element('app');
  class Chart {
    static defaults = {};
    constructor(canvas, config) { this.config = config; this.destroyed = false; charts.push(this); }
    destroy() { this.destroyed = true; }
  }
  const context = vm.createContext({
    window: {}, document: { documentElement: {}, getElementById: id => elements.get(id), querySelector: () => null },
    Chart, getComputedStyle: () => ({ getPropertyValue: () => '' }),
    HEALTH_THRESHOLDS: {}, escapeHtml: s => String(s), timeAgo: () => 'now',
    formatSkew: s => s + ' seconds', renderSkewBadge: () => '', observerSkewSeverity: () => 'ok',
    registerPage: (name, mod) => { page = mod; },
    api(url, options) {
      if (url === '/observers') return Promise.resolve({ observers: [] });
      const request = { url, options };
      requests.push(request);
      return new Promise((resolve, reject) => Object.assign(request, { resolve, reject }));
    },
  });
  vm.runInContext(fs.readFileSync(path.resolve(__dirname, '../../public/observer-detail.js'), 'utf8'), context);
  function visit(id) {
    page.destroy();
    page.init(app, id);
    return requests.slice(-3);
  }
  function days(value) {
    elements.get('obsDaysSelect').listeners.change({ target: { value: String(value) } });
    return requests.slice(-3);
  }
  function snapshot() {
    return { title: elements.get('obsTitle').textContent,
      html: elements.get('obsDetailContent').innerHTML,
      error: elements.get('obsDetailContent').firstChild.textContent,
      charts: charts.filter(c => !c.destroyed) };
  }
  return { page, app, elements, requests, charts, visit, days, snapshot };
}

function complete(requests, id, label = id) {
  requests[0].resolve({ id, name: label, last_seen: null });
  requests[1].resolve({ timeline: [{ label, count: 1 }], packetTypes: { 4: 1 },
    nodesTimeline: [{ label, count: 1 }], snrDistribution: [{ range: '0 to 5', count: 1 }], recentPackets: [] });
  requests[2].resolve([{ observerID: 'other', offsetSec: 99, samples: 99 },
    { observerID: id, offsetSec: 1, samples: 1 }, { observerID: id, offsetSec: 2, samples: 2 }]);
}
// Yield one event-loop turn after resolving promises, rather than guessing a delay.
const settled = () => new Promise(resolve => setImmediate(resolve));
let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log('PASS ' + name); }
  catch (error) { failed++; console.error('FAIL ' + name + ': ' + error.message.split('\n')[0]); }
}

(async () => {
  await test('current success renders four charts and the last matching calibration without extra calls', async () => {
    const h = harness();
    complete(h.visit('A /?'), 'A /?', 'Current observer');
    await settled();
    assert.strictEqual(h.snapshot().title, 'Current observer');
    assert.strictEqual(h.snapshot().charts.length, 4);
    assert.match(h.snapshot().html, /2 seconds/);
    assert.doesNotMatch(h.snapshot().html, /99 seconds/);
    assert.deepStrictEqual(h.requests.map(r => r.url), [
      '/observers/A%20%2F%3F', '/observers/A%20%2F%3F/analytics?days=7', '/observers/clock-skew']);
    assert.strictEqual(h.requests[2].options.ttl, 30000);
  });

  for (const failure of [false, true]) {
    await test('A to B ignores late A ' + (failure ? 'failure' : 'success') + ' and preserves B charts', async () => {
      const h = harness();
      const old = h.visit('A');
      complete(h.visit('B'), 'B');
      await settled();
      const before = h.snapshot();
      if (failure) old[1].reject(new Error('old failure'));
      complete(old, 'A');
      await settled();
      assert.deepStrictEqual(h.snapshot(), before, 'late A must not change B content or chart instances');
    });
  }

  await test('A to B to A gives the second A visit ownership', async () => {
    const h = harness();
    const old = h.visit('A');
    complete(h.visit('B'), 'B');
    await settled();
    complete(h.visit('A'), 'A', 'A new visit');
    await settled();
    const before = h.snapshot();
    complete(old, 'A', 'A old visit');
    await settled();
    assert.deepStrictEqual(h.snapshot(), before, 'matching observer IDs do not make an old visit current');
  });

  for (const failure of [false, true]) {
    await test('rapid day changes ignore older ' + (failure ? 'errors' : 'results'), async () => {
      const h = harness();
      const week = h.visit('A');
      const day = h.days(1);
      const threeDays = h.days(3);
      complete(threeDays, 'A', 'three days');
      await settled();
      const before = h.snapshot();
      if (failure) day[0].reject(new Error('old day failed'));
      complete(day, 'A', 'one day');
      complete(week, 'A', 'seven days');
      await settled();
      assert.deepStrictEqual(h.snapshot(), before, 'only latest selected days may update the view');
      assert.deepStrictEqual(h.requests.filter(r => r.url.includes('/analytics')).map(r => r.url), [
        '/observers/A/analytics?days=7', '/observers/A/analytics?days=1', '/observers/A/analytics?days=3']);
    });
  }

  await test('stale success cannot clear the current request error', async () => {
    const h = harness();
    const old = h.visit('A');
    const current = h.visit('B');
    current[0].reject(new Error('<current error>'));
    complete(current, 'B');
    await settled();
    const before = h.snapshot();
    assert.strictEqual(before.error, 'Error: <current error>');
    assert.doesNotMatch(before.html, /<current error>/);
    complete(old, 'A');
    await settled();
    assert.deepStrictEqual(h.snapshot(), before, 'current error must remain visible');
  });

  for (const failure of [false, true]) {
    await test('destroy invalidates pending ' + (failure ? 'failure' : 'success') + ' even before DOM removal', async () => {
      const h = harness();
      complete(h.visit('A'), 'A');
      await settled();
      const pending = h.days(30);
      h.page.destroy();
      const before = h.snapshot();
      assert.ok(h.charts.every(c => c.destroyed), 'existing charts are cleaned up');
      if (failure) pending[0].reject(new Error('disposed failure'));
      complete(pending, 'A', 'disposed');
      await settled();
      assert.deepStrictEqual(h.snapshot(), before, 'destroyed page must receive no further writes');
    });
  }

  await test('optional clock calibration failure still renders current detail', async () => {
    const h = harness();
    const pending = h.visit('A');
    pending[2].reject(new Error('calibration unavailable'));
    complete(pending, 'A');
    await settled();
    assert.strictEqual(h.snapshot().title, 'A');
    assert.strictEqual(h.snapshot().charts.length, 4);
    assert.doesNotMatch(h.snapshot().html, /Clock Offset/);
  });

  await test('#2320 each visit requests the displayed seven-day default', async () => {
    const h = harness();
    complete(h.visit('A'), 'A');
    await settled();
    complete(h.days(30), 'A');
    await settled();
    const next = h.visit('B');
    assert.match(h.app.innerHTML, /value="7" selected/);
    assert.strictEqual(next[1].url, '/observers/B/analytics?days=7', 'new observer request must match displayed days');
    complete(next, 'B');
    await settled();
    complete(h.days(1), 'B');
    await settled();
    const revisit = h.visit('A');
    assert.strictEqual(revisit[1].url, '/observers/A/analytics?days=7', 'revisited observer request must match displayed days');
    complete(revisit, 'A');
    await settled();
    assert.strictEqual(h.requests.length, 15, 'three detail requests per load, without a corrective refetch');
  });
  console.log(`Observer detail ownership: ${passed} passed, ${failed} failed`);
  process.exitCode = failed ? 1 : 0;
})();
