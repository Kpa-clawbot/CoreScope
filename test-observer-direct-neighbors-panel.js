/* #1865 follow-up (cwichura, PR #1867): "Direct Neighbors" panel on the
 * observer detail page. Production code must expose:
 *   window.renderDirectNeighbors(neighborsData) -> string
 * Must render a plain, neutral note (no warning icon) when there is no
 * neighbor data -- absence is normal (opt-in firmware, non-PSRAM hardware),
 * never a fault.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
const pending = [];
function test(name, fn) {
  try {
    const result = fn();
    if (result && typeof result.then === 'function') {
      // Async test (the sparkline loader tests) -- defer accounting until
      // it settles; the final summary waits on `pending` before printing.
      pending.push(result.then(
        () => { passed++; console.log(`  ✅ ${name}`); },
        (e) => { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
      ));
    } else {
      passed++; console.log(`  ✅ ${name}`);
    }
  } catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

function makeCtx() {
  const ctx = {
    window: { addEventListener: () => {}, dispatchEvent: () => {} },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '' }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    console, Date, Math, Array, Object, String, Number, Boolean, JSON,
    setInterval: () => 0, clearInterval: () => {},
    setTimeout: (fn) => { try { fn(); } catch {} return 0; },
    encodeURIComponent, decodeURIComponent,
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
  };
  ctx.registerPage = () => {};
  ctx.timeAgo = (iso) => 'TIME_AGO(' + iso + ')';
  ctx.escapeHtml = (s) => String(s == null ? '' : s).replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  })[c]);
  ctx.Chart = function () { return { destroy() {} }; };
  vm.createContext(ctx);
  return ctx;
}

// Richer sandbox for the async sparkline loader: needs a mockable api()
// and a document.getElementById that returns a settable-outerHTML stub.
function makeLoaderCtx(apiImpl, elementsById) {
  const ctx = {
    window: { addEventListener: () => {}, dispatchEvent: () => {} },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '' }),
      head: { appendChild: () => {} },
      getElementById: (id) => elementsById[id] || null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    console, Date, Math, Array, Object, String, Number, Boolean, JSON, Promise,
    setInterval: () => 0, clearInterval: () => {},
    setTimeout: (fn) => { try { fn(); } catch {} return 0; },
    encodeURIComponent, decodeURIComponent,
    api: apiImpl,
  };
  ctx.registerPage = () => {};
  ctx.timeAgo = (iso) => 'TIME_AGO(' + iso + ')';
  ctx.escapeHtml = (s) => String(s == null ? '' : s).replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  })[c]);
  ctx.Chart = function () { return { destroy() {} }; };
  vm.createContext(ctx);
  return ctx;
}
function mockElement() {
  return { _outerHTML: '', set outerHTML(v) { this._outerHTML = v; }, get outerHTML() { return this._outerHTML; } };
}

console.log('\n=== #1865 follow-up — Observer detail "Direct Neighbors" panel ===');

const ctx = makeCtx();
vm.runInContext(fs.readFileSync('public/observer-detail.js', 'utf8'), ctx);

test('window.renderDirectNeighbors exists', () => {
  assert.strictEqual(typeof ctx.window.renderDirectNeighbors, 'function');
});

test('empty/never-reported renders a neutral note, not a warning', () => {
  const html = ctx.window.renderDirectNeighbors({ neighbors: [], reportedAt: '' });
  assert.ok(/text-muted/.test(html));
  assert.ok(!/ph-warning/.test(html));
  assert.ok(/opt-in/.test(html));
});

test('missing neighbors field (defensive) also renders the empty note', () => {
  const html = ctx.window.renderDirectNeighbors({});
  assert.ok(/No direct-neighbor data/.test(html));
});

test('a known node renders a link + role icon + escaped name', () => {
  const html = ctx.window.renderDirectNeighbors({
    neighbors: [{ pubkey: 'abc123', name: '<b>Evil</b>', role: 'repeater', scopes: '#dk', status: 'responded' }],
    reportedAt: '2026-07-26T10:00:00Z',
  });
  assert.ok(html.includes('href="#/nodes/abc123"'), 'expected a link to the node detail page');
  assert.ok(html.includes('ph-broadcast'), 'expected the repeater role icon');
  assert.ok(!html.includes('<b>Evil</b>'), 'name must be HTML-escaped');
  assert.ok(html.includes('&lt;b&gt;'), 'expected escaped name markup');
  assert.ok(html.includes('#dk'), 'expected the scope badge');
  assert.ok(html.includes('TIME_AGO(2026-07-26T10:00:00Z)'), 'expected the "as of" timestamp');
});

test('an unresolved pubkey (no name) renders truncated, unlinked', () => {
  const html = ctx.window.renderDirectNeighbors({
    neighbors: [{ pubkey: 'deadbeefdeadbeefdeadbeef', name: null, role: null, scopes: null, status: 'timeout' }],
    reportedAt: '',
  });
  assert.ok(!/href="#\/nodes\//.test(html), 'must not link an unresolved pubkey');
  assert.ok(html.includes('deadbeefdead'), 'expected a truncated pubkey');
  assert.ok(/no reply/.test(html), 'expected the timeout "no reply" label');
});

test('seenViaPackets=true renders "confirmed", not an error/warning treatment', () => {
  const html = ctx.window.renderDirectNeighbors({
    neighbors: [{ pubkey: 'abc123', name: 'Repeater A', role: 'repeater', scopes: '#dk', status: 'responded', seenViaPackets: true }],
    reportedAt: '',
  });
  assert.ok(/confirmed/.test(html));
  assert.ok(!/ph-warning/.test(html));
});

test('seenViaPackets=false surfaces the coverage-gap diagnostic, styled neutrally', () => {
  const html = ctx.window.renderDirectNeighbors({
    neighbors: [{ pubkey: 'abc123', name: 'Repeater A', role: 'repeater', scopes: '#dk', status: 'responded', seenViaPackets: false }],
    reportedAt: '',
  });
  assert.ok(/not seen yet/.test(html));
  assert.ok(/coverage gap/.test(html), 'expected the explanatory tooltip');
  assert.ok(!/ph-warning/.test(html), 'must not use the warning icon treatment');
});

test('renders a placeholder span per row for the async SNR sparkline loader', () => {
  const html = ctx.window.renderDirectNeighbors({
    neighbors: [{ pubkey: 'abc123', name: 'Repeater A', role: 'repeater', scopes: '#dk', status: 'responded' }],
    reportedAt: '',
  });
  assert.ok(html.includes('id="nb-spark-abc123"'), 'expected a placeholder span keyed by pubkey');
});

console.log('\n=== #1865 follow-up — SNR sparkline (pure helper) ===');

test('window.neighborSnrSparkline exists', () => {
  assert.strictEqual(typeof ctx.window.neighborSnrSparkline, 'function');
});

test('empty data returns empty string', () => {
  assert.strictEqual(ctx.window.neighborSnrSparkline([], 80, 20), '');
});

test('renders an SVG polyline for 2+ points', () => {
  const html = ctx.window.neighborSnrSparkline([1, 5, 3], 80, 20);
  assert.ok(html.includes('<svg'));
  assert.ok(html.includes('<polyline'));
});

test('labels the min/max dB values directly on the sparkline', () => {
  // dborup: "jeg vil bare gerne se mere" -- min/max readable without hover.
  const html = ctx.window.neighborSnrSparkline([1, 5, 3], 80, 20);
  assert.ok(html.includes('<text'), 'expected min/max <text> labels');
  assert.ok(html.includes('>5.0<'), 'expected the max value labeled');
  assert.ok(html.includes('>1.0<'), 'expected the min value labeled');
});

console.log('\n=== #1865 follow-up — SNR sparkline async loader ===');

test('loads and injects a sparkline + latest value on success', async () => {
  const el = mockElement();
  const apiCtx = makeLoaderCtx(
    () => Promise.resolve({ metrics: [{ timestamp: 't1', snr: 5 }, { timestamp: 't2', snr: 8 }] }),
    { 'nb-spark-abc123': el }
  );
  vm.runInContext(fs.readFileSync('public/observer-detail.js', 'utf8'), apiCtx);
  await apiCtx.window.loadNeighborSnrSparklines('obs1', { neighbors: [{ pubkey: 'abc123' }] });
  assert.ok(el.outerHTML.includes('<svg'), 'expected a sparkline to be injected');
  assert.ok(el.outerHTML.includes('8.0 dB'), 'expected the latest SNR value shown');
});

test('shows a single value (no sparkline) when only one data point exists', async () => {
  const el = mockElement();
  const apiCtx = makeLoaderCtx(
    () => Promise.resolve({ metrics: [{ timestamp: 't1', snr: 4.5 }] }),
    { 'nb-spark-abc123': el }
  );
  vm.runInContext(fs.readFileSync('public/observer-detail.js', 'utf8'), apiCtx);
  await apiCtx.window.loadNeighborSnrSparklines('obs1', { neighbors: [{ pubkey: 'abc123' }] });
  assert.ok(!el.outerHTML.includes('<svg'), 'a single point should not render a sparkline');
  assert.ok(el.outerHTML.includes('4.5 dB'));
});

test('shows a neutral "no data" state, not an error, when metrics is empty', async () => {
  const el = mockElement();
  const apiCtx = makeLoaderCtx(
    () => Promise.resolve({ metrics: [] }),
    { 'nb-spark-abc123': el }
  );
  vm.runInContext(fs.readFileSync('public/observer-detail.js', 'utf8'), apiCtx);
  await apiCtx.window.loadNeighborSnrSparklines('obs1', { neighbors: [{ pubkey: 'abc123' }] });
  assert.ok(/no data/.test(el.outerHTML));
});

test('a failed fetch degrades to a neutral dash, not a thrown error', async () => {
  const el = mockElement();
  const apiCtx = makeLoaderCtx(
    () => Promise.reject(new Error('network error')),
    { 'nb-spark-abc123': el }
  );
  vm.runInContext(fs.readFileSync('public/observer-detail.js', 'utf8'), apiCtx);
  await apiCtx.window.loadNeighborSnrSparklines('obs1', { neighbors: [{ pubkey: 'abc123' }] });
  assert.ok(el.outerHTML.length > 0, 'expected a fallback rendered, not left blank/thrown');
});

test('a sparkline with data is marked clickable (data-nb-pubkey), for the expand-to-modal feature', async () => {
  const el = mockElement();
  const apiCtx = makeLoaderCtx(
    () => Promise.resolve({ metrics: [{ timestamp: 't1', snr: 5 }, { timestamp: 't2', snr: 8 }] }),
    { 'nb-spark-abc123': el }
  );
  vm.runInContext(fs.readFileSync('public/observer-detail.js', 'utf8'), apiCtx);
  await apiCtx.window.loadNeighborSnrSparklines('obs1', { neighbors: [{ pubkey: 'abc123' }] });
  assert.ok(el.outerHTML.includes('data-nb-pubkey="abc123"'));
});

test('the "no data" state is not marked clickable (nothing to expand)', async () => {
  const el = mockElement();
  const apiCtx = makeLoaderCtx(
    () => Promise.resolve({ metrics: [] }),
    { 'nb-spark-abc123': el }
  );
  vm.runInContext(fs.readFileSync('public/observer-detail.js', 'utf8'), apiCtx);
  await apiCtx.window.loadNeighborSnrSparklines('obs1', { neighbors: [{ pubkey: 'abc123' }] });
  assert.ok(!el.outerHTML.includes('data-nb-pubkey'));
});

console.log('\n=== #1865 follow-up — SNR history expand-to-modal ===');

// Minimal fake DOM sufficient for openNeighborSnrModal: createElement /
// body.appendChild / querySelector / addEventListener, nothing more.
function fakeModalDom() {
  function makeEl() {
    const listeners = {};
    const el = {
      _html: '',
      set innerHTML(v) { this._html = v; },
      get innerHTML() { return this._html; },
      setAttribute() {},
      className: '',
      addEventListener(type, fn) { (listeners[type] = listeners[type] || []).push(fn); },
      removeEventListener() {},
      _fire(type, evt) { (listeners[type] || []).forEach((fn) => fn(evt || {})); },
      querySelector() { return makeEl(); },
      remove() {},
    };
    return el;
  }
  const body = { appendChild() {} };
  return { createElement: () => makeEl(), body, addEventListener() {}, removeEventListener() {} };
}

test('clicking a cached sparkline opens a modal with a dual-axis SNR/heard-secs-ago chart', async () => {
  let lastChartConfig = null;
  const apiCtx = makeLoaderCtx(
    () => Promise.resolve({ metrics: [
      { timestamp: '2026-07-26T10:00:00Z', snr: 5, heardSecsAgo: 60 },
      { timestamp: '2026-07-26T11:00:00Z', snr: 8, heardSecsAgo: 90 },
    ] }),
    { 'nb-spark-abc123': mockElement() }
  );
  apiCtx.document = fakeModalDom();
  apiCtx.Chart = function(canvas, config) { lastChartConfig = config; return { destroy() {} }; };
  vm.createContext(apiCtx);
  vm.runInContext(fs.readFileSync('public/observer-detail.js', 'utf8'), apiCtx);

  // Populate the metrics cache the way loadNeighborSnrSparklines would --
  // getElementById isn't wired to the fake modal DOM here, so call the
  // loader against the richer document directly to exercise the real path.
  apiCtx.document.getElementById = () => mockElement();
  await apiCtx.window.loadNeighborSnrSparklines('obs1', { neighbors: [{ pubkey: 'abc123', name: 'Repeater A' }] });

  apiCtx.window.openNeighborSnrModal('abc123');
  assert.ok(lastChartConfig, 'expected Chart to be constructed');
  assert.strictEqual(lastChartConfig.data.datasets.length, 2, 'expected an SNR dataset and a heard_secs_ago dataset');
  assert.strictEqual(lastChartConfig.data.datasets[0].yAxisID, 'y');
  assert.strictEqual(lastChartConfig.data.datasets[1].yAxisID, 'y1');
  assert.ok(lastChartConfig.options.scales.y1.title.text.toLowerCase().includes('heard'), 'expected the second axis labeled for heard_secs_ago');
  // Regression: canvas can't resolve CSS var() as a strokeStyle, so a raw
  // 'var(--x)' string silently renders as Chart.js's default black --
  // both lines looked identical/black in production before this was caught.
  // Line colors must be resolved literal values (this page's CHART_COLORS
  // palette), and the two datasets must be visually distinct.
  const c0 = lastChartConfig.data.datasets[0].borderColor;
  const c1 = lastChartConfig.data.datasets[1].borderColor;
  assert.ok(!/^var\(/.test(c0), `dataset[0].borderColor must not be a CSS var() reference, got ${c0}`);
  assert.ok(!/^var\(/.test(c1), `dataset[1].borderColor must not be a CSS var() reference, got ${c1}`);
  assert.ok(/^#[0-9a-f]{6}$/i.test(c0), `dataset[0].borderColor must be a resolved hex color, got ${c0}`);
  assert.ok(/^#[0-9a-f]{6}$/i.test(c1), `dataset[1].borderColor must be a resolved hex color, got ${c1}`);
  assert.notStrictEqual(c0, c1, 'SNR and Heard(s ago) lines must be visually distinct colors');
});

test('openNeighborSnrModal is a no-op for a pubkey with no cached data', () => {
  let chartCalled = false;
  const apiCtx = makeLoaderCtx(() => Promise.resolve({ metrics: [] }), {});
  apiCtx.document = fakeModalDom();
  apiCtx.Chart = function() { chartCalled = true; return { destroy() {} }; };
  vm.createContext(apiCtx);
  vm.runInContext(fs.readFileSync('public/observer-detail.js', 'utf8'), apiCtx);
  apiCtx.window.openNeighborSnrModal('never-fetched-pubkey');
  assert.ok(!chartCalled, 'must not construct a chart with no data to show');
});

Promise.all(pending).then(() => {
  console.log(`\n${passed} passed, ${failed} failed\n`);
  if (failed > 0) process.exit(1);
});
