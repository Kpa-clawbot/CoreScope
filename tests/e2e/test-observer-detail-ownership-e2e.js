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

// #2342: comparison results can precede the router's heading focus frame.
for (const lateFocus of [false, true]) test('dispose-and-controls' + (lateFocus ? '-late-focus' : ''), async h => {
  await h.open('A');
  await h.release('A', 7);
  await h.current('A', 7);
  await h.page.selectOption('#obsCompareWithPicker', IDS.B);
  assert.strictEqual(await h.page.locator('[data-action="compare-with-go"]').isEnabled(), true);
  await h.page.selectOption('#obsDaysSelect', '30');
  if (lateFocus) await h.page.evaluate(() => {
    const init = pages.compare.init;
    const requestFrame = window.requestAnimationFrame;
    pages.compare.init = function (...args) {
      pages.compare.init = init;
      init(...args);
      // navigate() queues heading focus immediately after page init. Hold
      // that real callback until the baseline capture has been requested.
      window.requestAnimationFrame = callback => {
        window.requestAnimationFrame = requestFrame;
        window.__releaseComparisonFocus = () => requestFrame(callback);
        return 0;
      };
    };
  });
  await h.page.locator('[data-action="compare-with-go"]').click();
  await h.page.waitForFunction(ids => location.hash.startsWith('#/compare?a=' + ids.A + '&b=' + ids.B), IDS);
  await h.page.waitForSelector('#compareContent .compare-results');
  assert.strictEqual((await h.snapshot()).charts.length, 0, 'leaving detail destroys its charts');
  const baseline = (async () => {
    // Results do not imply route focus has run; the heading may be hidden on mobile.
    await h.page.locator('.compare-page h2[tabindex="-1"]').waitFor({ state: 'attached' });
    return h.page.evaluate(() => document.getElementById('app').innerHTML);
  })();
  if (lateFocus) {
    assert.strictEqual(await h.page.locator('.compare-page h2').getAttribute('tabindex'), null,
      'comparison results can render before route focus');
    await h.page.evaluate(() => window.__releaseComparisonFocus());
  }
  const before = await baseline;
  await h.release('A', 30);
  assert.strictEqual(await h.page.locator('#app').innerHTML(), before, 'disposed request cannot change comparison page');
  assert.strictEqual((await h.snapshot()).charts.length, 0);
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
