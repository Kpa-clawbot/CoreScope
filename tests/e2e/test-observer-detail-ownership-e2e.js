/* #2316: real SPA navigation, deferred HTTP responses, and real Chart instances. */
'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:3000';
assert.ok(['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname), 'Use a local fixture server');
const IDS = { A: 'AA'.repeat(32), B: 'BB'.repeat(32) };
const ARTIFACTS = process.env.ARTIFACT_DIR;
if (ARTIFACTS) fs.mkdirSync(ARTIFACTS, { recursive: true });
const cases = [];
function test(name, run) { cases.push({ name, run }); }

async function setup(browser, viewport, theme) {
  const context = await browser.newContext({ viewport, colorScheme: theme });
  const page = await context.newPage();
  page.setDefaultTimeout(10000);
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const pending = new Map();
  const ready = new Map();
  const visits = { A: 0, B: 0 };
  const network = [];
  function observer(letter, visit = 0) {
    return { id: IDS[letter], name: 'Observer ' + letter + (visit ? ' visit ' + visit : ''),
      last_seen: letter === 'B' ? new Date().toISOString() : null, packet_count: 5 };
  }
  await page.route('**/api/observers**', async route => {
    const url = new URL(route.request().url());
    const key = url.pathname + url.search;
    network.push(key);
    if (url.pathname === '/api/observers') {
      return route.fulfill({ json: { observers: ['A', 'B'].map(letter => observer(letter)), server_time: new Date().toISOString() } });
    }
    if (url.pathname === '/api/observers/clock-skew') {
      return route.fulfill({ json: [
        { observerID: IDS.A, offsetSec: 1, samples: 11 },
        { observerID: IDS.B, offsetSec: 2, samples: 22 },
        { observerID: IDS.A, offsetSec: 3, samples: 33 },
      ] });
    }
    for (const letter of ['A', 'B']) {
      if (url.pathname === '/api/observers/' + IDS[letter]) {
        return route.fulfill({ json: observer(letter, ++visits[letter]) });
      }
      if (url.pathname === '/api/observers/' + IDS[letter] + '/analytics') {
        const key = letter + ':' + url.searchParams.get('days');
        pending.set(key, route);
        if (ready.has(key)) ready.get(key)();
        return;
      }
    }
    return route.continue();
  });
  await page.goto(BASE + '/?theme=' + theme + '#/observers', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#obsTable tbody tr[data-value]');
  // Observe the real api() promises (including its in-flight deduplication),
  // so assertions wait for loadDetail's continuations, never a guessed sleep.
  await page.evaluate(() => {
    const original = api;
    window.__observerRequests = [];
    api = function (...args) {
      const promise = original(...args);
      window.__observerRequests.push({ path: args[0], promise });
      return promise;
    };
  });
  async function open(letter) {
    if (await page.locator('#obsTitle').count()) {
      await page.locator('a[title="Back to Observers"]').click();
    }
    await page.locator('#obsTable tbody tr[data-value="#/observers/' + IDS[letter] + '"]').click();
    if (viewport.width < 768) {
      await page.locator('.slide-over-panel a').filter({ hasText: 'Open full detail' }).click();
      await page.keyboard.press('Escape');
      await page.locator('.slide-over-panel[data-state="open"]').waitFor({ state: 'hidden' });
    }
    await page.waitForSelector('#obsDaysSelect');
    await page.waitForFunction(id => window.__observerRequests.some(r => r.path.startsWith('/observers/' + id + '/analytics')), IDS[letter]);
    await page.evaluate(id => Promise.allSettled(window.__observerRequests.filter(r => r.path === '/observers/' + id).map(r => r.promise)), IDS[letter]);
  }
  async function release(letter, days, status = 200) {
    const key = letter + ':' + days;
    // Wait until this exact route is intercepted, without a timing assumption.
    if (!pending.has(key)) {
      let timeout;
      try {
        await new Promise((resolve, reject) => {
          ready.set(key, resolve);
          timeout = setTimeout(() => reject(new Error('Missing held analytics request: ' + key)), 10000);
        });
      } finally { clearTimeout(timeout); ready.delete(key); }
    }
    const route = pending.get(key);
    assert.ok(route, 'analytics request is held: ' + key);
    pending.delete(key);
    const label = letter + ' ' + days + ' days';
    await route.fulfill({ status, json: status === 200 ? {
      timeline: [{ label, count: days }], packetTypes: { 4: days },
      nodesTimeline: [{ label, count: 1 }], snrDistribution: [{ range: '0 to 5', count: days }], recentPackets: [],
    } : { error: 'fixture failure' } });
    await page.evaluate(async suffix => {
      await Promise.allSettled(window.__observerRequests.filter(r => r.path.endsWith(suffix) || !r.path.includes('/analytics?')).map(r => r.promise));
      await new Promise(requestAnimationFrame);
    }, '/observers/' + IDS[letter] + '/analytics?days=' + days);
  }
  async function snapshot() {
    return page.evaluate(() => ({
      title: document.getElementById('obsTitle')?.textContent,
      content: document.getElementById('obsDetailContent')?.innerHTML,
      charts: Object.values(Chart.instances).map(chart => ({ id: chart.id, labels: chart.data.labels, connected: chart.canvas.isConnected })),
    }));
  }
  async function current(letter, days, visit = visits[letter]) {
    const state = await snapshot();
    assert.strictEqual(state.title, 'Observer ' + letter + ' visit ' + visit);
    assert.strictEqual(state.charts.length, 4, 'only four current charts, no detached stale charts');
    assert.ok(state.charts.every(chart => chart.connected));
    assert.strictEqual(state.charts[0].labels[0], letter + ' ' + days + ' days');
    assert.match(state.content, letter === 'B' ? /Online/ : /Offline/);
    assert.match(state.content, letter === 'B' ? /22 samples/ : /33 samples/);
    assert.strictEqual(await page.locator('#obsDaysSelect').inputValue(), String(days));
  }
  return { page, context, errors, open, release, snapshot, current, network };
}

test('late-success', async h => {
  await h.open('A');
  await h.open('B');
  await h.release('B', 7);
  await h.current('B', 7);
  const before = await h.snapshot();
  await h.release('A', 7);
  assert.deepStrictEqual(await h.snapshot(), before, 'late A must not replace B content, status, calibration or charts');
});

test('late-error', async h => {
  await h.open('A');
  await h.open('B');
  await h.release('B', 7);
  const before = await h.snapshot();
  await h.release('A', 7, 500);
  assert.deepStrictEqual(await h.snapshot(), before, 'late A failure must not replace B with an error');
});

test('same-observer-revisit', async h => {
  await h.open('A');
  await h.open('B');
  await h.release('B', 7);
  await h.open('A');
  await h.release('A', 7); // Both A visits share this real api() in-flight promise.
  await h.current('A', 7, 2);
  assert.strictEqual(h.network.filter(url => url.endsWith('/' + IDS.A + '/analytics?days=7')).length, 1,
    'same-observer revisit retains existing in-flight request deduplication');
});

for (const status of [200, 500]) test('rapid-days-' + status, async h => {
  await h.open('A');
  await h.page.selectOption('#obsDaysSelect', '1');
  await h.page.selectOption('#obsDaysSelect', '3');
  await h.release('A', 3);
  await h.current('A', 3);
  const before = await h.snapshot();
  await h.release('A', 1, status);
  assert.deepStrictEqual(await h.snapshot(), before, 'older window response must not replace latest results');
  await h.release('A', 7);
  assert.deepStrictEqual(await h.snapshot(), before, 'older window success must not replace latest results');
  assert.strictEqual(h.network.filter(url => url.includes('/analytics?')).length, 3, 'exactly one analytics request per selection');
});

test('current-error', async h => {
  await h.open('A');
  await h.open('B');
  await h.release('B', 7, 500);
  const before = await h.snapshot();
  assert.match(before.content, /Error: API 500:/, 'current error stays visible');
  assert.strictEqual(before.charts.length, 0);
  await h.release('A', 7);
  assert.deepStrictEqual(await h.snapshot(), before, 'stale success must not clear current error');
});

test('visit-default-days', async h => {
  await h.open('A');
  await h.release('A', 7);
  await h.page.selectOption('#obsDaysSelect', '30');
  await h.release('A', 30);
  await h.current('A', 30);
  await h.open('B');
  async function assertDefault() {
    assert.strictEqual(await h.page.locator('#obsDaysSelect').inputValue(), '7');
    const requested = await h.page.evaluate(() => window.__observerRequests.filter(r => r.path.includes('/analytics?')).at(-1).path);
    assert.ok(requested.endsWith('?days=7'), 'new visit must request displayed 7 days; got ' + requested);
  }
  await assertDefault();
  await h.release('B', 7);
  await h.current('B', 7);
  await h.page.selectOption('#obsDaysSelect', '1');
  await h.release('B', 1);
  await h.current('B', 1);
  await h.open('A');
  await assertDefault();
  await h.release('A', 7);
  await h.current('A', 7);
  assert.strictEqual(h.network.filter(url => url.includes('/analytics?')).length, 5, 'no corrective refetch on either new visit');
});

test('dispose-and-controls', async h => {
  await h.open('A');
  await h.release('A', 7);
  await h.current('A', 7);
  await h.page.selectOption('#obsCompareWithPicker', IDS.B);
  assert.strictEqual(await h.page.locator('[data-action="compare-with-go"]').isEnabled(), true);
  await h.page.selectOption('#obsDaysSelect', '30');
  await h.page.locator('[data-action="compare-with-go"]').click();
  await h.page.waitForFunction(ids => location.hash.startsWith('#/compare?a=' + ids.A + '&b=' + ids.B), IDS);
  await h.page.waitForSelector('#compareContent .compare-results');
  assert.strictEqual((await h.snapshot()).charts.length, 0, 'leaving detail destroys its charts');
  const before = await h.page.locator('#app').innerHTML();
  await h.release('A', 30);
  assert.strictEqual(await h.page.locator('#app').innerHTML(), before, 'disposed request cannot change comparison page');
  assert.strictEqual((await h.snapshot()).charts.length, 0);
});

test('charts-fit-and-resize', async h => {
  await h.open('A');
  await h.release('A', 7);
  await h.current('A', 7);
  const originalViewport = h.page.viewportSize();
  await h.page.evaluate(() => {
    window.__observerChartLayout = () => {
      const box = element => {
        const rect = element.getBoundingClientRect();
        return { left: rect.left, right: rect.right, top: rect.top, width: rect.width, height: rect.height };
      };
      const grid = document.querySelector('.obs-charts');
      return {
        documentWidth: document.documentElement.scrollWidth,
        grid: box(grid),
        cards: [...grid.children].map(box),
        charts: [...grid.querySelectorAll('canvas')].map(canvas => {
          const chart = Chart.getChart(canvas);
          const parent = canvas.parentElement;
          const style = getComputedStyle(parent);
          return { id: canvas.id, ...box(canvas), connected: canvas.isConnected,
            responsive: chart.options.responsive, chartWidth: chart.width,
            parent: box(parent), contentWidth: parent.getBoundingClientRect().width
              - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight)
              - parseFloat(style.borderLeftWidth) - parseFloat(style.borderRightWidth) };
        }),
      };
    };
  });
  async function checkLayout(width, columns) {
    await h.page.setViewportSize({ width, height: originalViewport.height });
    // Chart.js may need several ResizeObserver deliveries to finish a resize.
    // Wait for real canvas dimensions, without changing the charts ourselves.
    await h.page.waitForFunction(() => {
      const layout = window.__observerChartLayout();
      return layout.charts.length === 4 && layout.charts.every(chart =>
        Math.abs(chart.width - chart.contentWidth) <= 1 && Math.abs(chart.chartWidth - chart.width) <= 1);
    });
    const layout = await h.page.evaluate(() => window.__observerChartLayout());
    assert.ok(layout.documentWidth <= width, 'observer charts must not widen the document at ' + width
      + 'px: got ' + layout.documentWidth + 'px');
    assert.strictEqual(layout.cards.length, 4);
    assert.strictEqual(layout.charts.length, 4);
    assert.strictEqual(layout.cards.filter(card => Math.abs(card.top - layout.cards[0].top) < 1).length,
      columns, 'fluid column count at ' + width + 'px');
    for (const card of layout.cards) {
      assert.ok(card.width > 0 && card.left >= layout.grid.left - 1 && card.right <= layout.grid.right + 1,
        'chart card must fit its grid at ' + width + 'px');
    }
    for (const chart of layout.charts) {
      assert.ok(chart.connected && chart.responsive && chart.width > 0 && chart.height > 0,
        chart.id + ' must remain a live responsive chart');
      assert.ok(chart.left >= chart.parent.left - 1 && chart.right <= chart.parent.right + 1,
        chart.id + ' canvas must fit its parent at ' + width + 'px');
      assert.ok(Math.abs(chart.width - chart.contentWidth) <= 1 && Math.abs(chart.chartWidth - chart.width) <= 1,
        chart.id + ' must resize to its available content width at ' + width + 'px');
    }
  }
  const initialRequests = h.network.filter(url => url.includes('/analytics?')).length;
  for (const [width, columns] of [[320, 1], [375, 1], [390, 1], [847, 1], [848, 2], [1400, 3], [390, 1]]) {
    await checkLayout(width, columns);
  }
  assert.strictEqual(h.network.filter(url => url.includes('/analytics?')).length, initialRequests,
    'viewport resizing must not fetch analytics again');
  await h.page.selectOption('#obsDaysSelect', '30');
  await h.release('A', 30);
  await h.current('A', 30);
  await checkLayout(390, 1);
  assert.strictEqual(h.network.filter(url => url.includes('/analytics?')).length, initialRequests + 1,
    'time-range reload keeps one analytics request per selection');
  await h.page.locator('a[title="Back to Observers"]').click();
  await h.page.waitForSelector('#obsTable tbody tr[data-value]');
  assert.strictEqual((await h.snapshot()).charts.length, 0, 'leaving the resized detail destroys its charts');
});

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-dev-shm-usage'] });
  let passed = 0, failed = 0;
  try {
    for (const [device, viewport] of Object.entries({ desktop: { width: 1400, height: 900 }, mobile: { width: 390, height: 844 } })) {
      for (const theme of ['light', 'dark']) {
        for (const { name, run } of cases) {
          const label = device + '-' + theme + '-' + name;
          const h = await setup(browser, viewport, theme);
          try {
            await run(h);
            assert.deepStrictEqual(h.errors, [], 'no uncaught browser errors');
            assert.strictEqual(await h.page.getAttribute('html', 'data-theme'), theme);
            if (ARTIFACTS && name === 'rapid-days-200') await h.page.screenshot({ path: path.join(ARTIFACTS, label + '.png'), fullPage: true });
            passed++;
            console.log('PASS ' + label);
          } catch (error) {
            failed++;
            console.error('FAIL ' + label + ': ' + error.message.split('\n')[0]);
            if (ARTIFACTS) await h.page.screenshot({ path: path.join(ARTIFACTS, label + '-failed.png'), fullPage: true });
          } finally { await h.context.close(); }
        }
      }
    }
  } finally { await browser.close(); }
  console.log(`Observer ownership E2E: ${passed} passed, ${failed} failed`);
  process.exitCode = failed ? 1 : 0;
})().catch(error => { console.error(error); process.exitCode = 1; });
