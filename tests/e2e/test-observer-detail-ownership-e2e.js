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
function test(name, run, options = {}) { cases.push({ name, run, options }); }

async function setup(browser, viewport, theme, options) {
  const context = await browser.newContext({ viewport, colorScheme: theme });
  const page = await context.newPage();
  page.setDefaultTimeout(10000);
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const pending = new Map();
  const ready = new Map();
  const visits = { A: 0, B: 0 };
  const network = [];
  let holdClock = false;
  function observer(letter, visit = 0) {
    return { id: IDS[letter], name: 'Observer ' + letter + (visit ? ' visit ' + visit : ''),
      last_seen: letter === 'B' ? new Date().toISOString() : null, packet_count: 5,
      model: 'Test radio', radio: '910.0,250,10,5' };
  }
  await page.route('**/api/observers**', async route => {
    const url = new URL(route.request().url());
    const key = url.pathname + url.search;
    network.push(key);
    if (url.pathname === '/api/observers') {
      return route.fulfill({ json: { observers: ['A', 'B'].map(letter => observer(letter)), server_time: new Date().toISOString() } });
    }
    if (url.pathname === '/api/observers/clock-skew') {
      if (holdClock) {
        pending.set('clock', route);
        if (ready.has('clock')) ready.get('clock')();
        return;
      }
      return route.fulfill({ json: [
        { observerID: IDS.A, offsetSec: 1, samples: 11 },
        { observerID: IDS.B, offsetSec: 2, samples: 22 },
        { observerID: IDS.A, offsetSec: 3, samples: 33 },
      ] });
    }
    for (const letter of ['A', 'B']) {
      if (url.pathname === '/api/observers/' + IDS[letter]) {
        if (options.identityFailure) return route.fulfill({ status: 404, json: { error: 'fixture identity failure' } });
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
  // The list also requests calibration. Hold only the detail request, after
  // clearing that completed list cache entry (preserve real in-flight dedup).
  holdClock = !!options.holdClock;
  if (holdClock) await page.evaluate(() => invalidateApiCache('/observers/clock-skew'));
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
    if (holdClock && await page.locator('#obsTitle').count()) {
      // Navigate through the real router without making the list wait for
      // this deliberately held, shared calibration request.
      await page.evaluate(id => { location.hash = '#/observers/' + id; }, IDS[letter]);
    } else {
      if (await page.locator('#obsTitle').count()) {
        await page.locator('a[title="Back to Observers"]').click();
      }
      await page.locator('#obsTable tbody tr[data-value="#/observers/' + IDS[letter] + '"]').click();
      if (viewport.width < 768) {
        await page.locator('.slide-over-panel a').filter({ hasText: 'Open full detail' }).click();
        await page.keyboard.press('Escape');
        await page.locator('.slide-over-panel[data-state="open"]').waitFor({ state: 'hidden' });
      }
    }
    await page.waitForSelector('#obsDaysSelect');
    await page.waitForFunction(id => window.__observerRequests.some(r => r.path.startsWith('/observers/' + id + '/analytics')), IDS[letter]);
    await page.evaluate(id => Promise.allSettled(window.__observerRequests.filter(r => r.path === '/observers/' + id).map(r => r.promise)), IDS[letter]);
  }
  async function held(key) {
    // Wait until this exact route is intercepted, without a timing assumption.
    if (!pending.has(key)) {
      let timeout;
      try {
        await new Promise((resolve, reject) => {
          ready.set(key, resolve);
          timeout = setTimeout(() => reject(new Error('Missing held request: ' + key)), 10000);
        });
      } finally { clearTimeout(timeout); ready.delete(key); }
    }
    const route = pending.get(key);
    assert.ok(route, 'request is held: ' + key);
    pending.delete(key);
    return route;
  }
  async function settled(path) {
    await page.evaluate(async path => {
      await Promise.allSettled(window.__observerRequests.filter(r => r.path === path).map(r => r.promise));
      await new Promise(requestAnimationFrame);
    }, path);
  }
  async function releaseClock(status = 200) {
    const route = await held('clock');
    await route.fulfill({ status, json: status === 200 ? [
      { observerID: IDS.A, offsetSec: 3, samples: 33 },
      { observerID: IDS.B, offsetSec: 2, samples: 22 },
    ] : { error: 'fixture calibration failure' } });
    await settled('/observers/clock-skew');
  }
  async function release(letter, days, status = 200) {
    const route = await held(letter + ':' + days);
    const label = letter + ' ' + days + ' days';
    await route.fulfill({ status, json: status === 200 ? {
      timeline: [{ label, count: days }], packetTypes: { 4: days },
      nodesTimeline: [{ label, count: 1 }], snrDistribution: [{ range: '0 to 5', count: days }], recentPackets: [],
    } : { error: 'fixture failure' } });
    await settled('/observers/' + IDS[letter] + '/analytics?days=' + days);
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
  return { page, context, errors, open, release, releaseClock, snapshot, current, network };
}

for (const first of ['analytics', 'clock']) test('progressive-' + first + '-first', async h => {
  await h.open('B');
  assert.strictEqual(await h.page.locator('#obsTitle').textContent(), 'Observer B visit 1',
    'identity must be visible with both secondary responses held');
  const info = h.page.locator('.obs-info-grid');
  assert.match(await info.innerText(), /Online/);
  assert.match(await info.innerText(), /Test radio/);
  assert.match(await info.innerText(), /910.0 MHz/);
  assert.strictEqual((await h.snapshot()).charts.length, 0);
  assert.strictEqual(await h.page.locator('#obsAnalytics').getAttribute('aria-busy'), 'true');
  await h.page.evaluate(() => { window.__observerMetadata = document.querySelector('.obs-info-grid'); });
  if (ARTIFACTS) await h.page.screenshot({ path: path.join(ARTIFACTS, h.label + '-pending.png'), fullPage: true });
  if (first === 'analytics') {
    await h.release('B', 7);
    assert.strictEqual((await h.snapshot()).charts.length, 4, 'analytics does not wait for calibration');
    await h.releaseClock();
  } else {
    await h.releaseClock();
    assert.match((await h.snapshot()).content, /22 samples/, 'calibration does not wait for analytics');
    assert.strictEqual((await h.snapshot()).charts.length, 0);
    await h.release('B', 7);
  }
  await h.current('B', 7);
  assert.strictEqual(await h.page.locator('#obsAnalytics').getAttribute('aria-busy'), 'false');
  assert.strictEqual(await h.page.evaluate(() => window.__observerMetadata === document.querySelector('.obs-info-grid')), true,
    'secondary replies preserve the actual metadata DOM node');
  assert.strictEqual(h.network.filter(url => url.includes('/analytics?')).length, 1);
  assert.strictEqual(h.network.filter(url => url === '/api/observers/clock-skew').length, 2,
    'one list calibration request plus one detail request');
  if (ARTIFACTS) await h.page.screenshot({ path: path.join(ARTIFACTS, h.label + '-complete.png'), fullPage: true });
}, { holdClock: true });

test('analytics-error-keeps-identity', async h => {
  await h.open('B');
  await h.release('B', 7, 500);
  assert.strictEqual(await h.page.locator('#obsTitle').textContent(), 'Observer B visit 1');
  assert.match(await h.page.locator('.obs-info-grid').innerText(), /Online/);
  assert.match(await h.page.locator('#obsAnalytics').innerText(), /Error: API 500:/);
  await h.releaseClock();
  assert.match((await h.snapshot()).content, /22 samples/);
  assert.strictEqual((await h.snapshot()).charts.length, 0);
}, { holdClock: true });

test('calibration-error-keeps-identity', async h => {
  await h.open('B');
  await h.releaseClock(500);
  assert.match(await h.page.locator('.obs-info-grid').innerText(), /Test radio/);
  await h.release('B', 7);
  assert.strictEqual((await h.snapshot()).charts.length, 4);
  assert.strictEqual(await h.page.locator('.skew-detail-section').count(), 0);
}, { holdClock: true });

test('identity-error-with-late-secondary-results', async h => {
  await h.open('B');
  const before = await h.snapshot();
  assert.match(before.content, /Error: API 404:/);
  await h.release('B', 7);
  await h.releaseClock();
  assert.deepStrictEqual(await h.snapshot(), before, 'secondary success cannot hide a failed identity');
}, { holdClock: true, identityFailure: true });

test('early-identity-route-with-held-calibration', async h => {
  await h.open('A');
  assert.strictEqual(await h.page.locator('#obsTitle').textContent(), 'Observer A visit 1');
  await h.open('B');
  await h.release('B', 7);
  await h.releaseClock();
  await h.current('B', 7);
  const before = await h.snapshot();
  await h.release('A', 7);
  assert.deepStrictEqual(await h.snapshot(), before, 'old observer cannot mutate independently completed sections');
}, { holdClock: true });

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

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-dev-shm-usage'] });
  let passed = 0, failed = 0;
  try {
    for (const [device, viewport] of Object.entries({ desktop: { width: 1400, height: 900 }, mobile: { width: 390, height: 844 } })) {
      for (const theme of ['light', 'dark']) {
        for (const { name, run, options } of cases) {
          const label = device + '-' + theme + '-' + name;
          const h = await setup(browser, viewport, theme, options);
          h.label = label;
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
