/**
 * #1562 — Observers page header: "Last updated: X ago" label and
 * compute aggregate counts from a pure, testable helper so operators
 * can see when the cached payload is stale.
 *
 * Pattern: pure helper on window.ObserversSummary (so it's easy to test
 * without a DOM) + render-string assertions for the header HTML.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const vm = require('vm');
const assert = require('assert');

let passed = 0, failed = 0;
function t(name, fn) {
  try { fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}

function makeSandbox() {
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
    console,
    Date, Math, Array, Object, Number, String, Boolean, RegExp, JSON,
    Promise, Map, Set, Symbol, Error,
    setTimeout, clearTimeout, setInterval, clearInterval,
    performance: { now: () => Date.now() },
    fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) }),
    localStorage: { getItem: () => null, setItem: () => {}, removeItem: () => {} },
    location: { hash: '#/observers', search: '' },
    history: { pushState: () => {} },
    navigator: { userAgent: 'node' },
    requestAnimationFrame: (cb) => setTimeout(cb, 0),
    URL,
    URLSearchParams,
  };
  ctx.window.location = ctx.location;
  ctx.window.localStorage = ctx.localStorage;
  vm.createContext(ctx);
  return ctx;
}
function load(ctx, file) {
  vm.runInContext(fs.readFileSync(path.join(__dirname, file), 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
}

const ctx = makeSandbox();
load(ctx, 'public/roles.js');
load(ctx, 'public/app.js');
// Stub IIFE-required globals so observers.js loads cleanly in sandbox
ctx.RegionFilter = { init: () => {}, onChange: () => () => {}, offChange: () => {}, getSelected: () => null };
ctx.registerPage = () => {};
ctx.debouncedOnWS = () => () => {};
ctx.offWS = () => {};
ctx.CLIENT_TTL = { observers: 120000 };
ctx.api = () => Promise.resolve({ observers: [] });
ctx.makeColumnsResizable = () => {};
ctx.TableResponsive = { register: () => {} };
ctx.SlideOver = null;
ctx.observerSkewSeverity = () => 'ok';
ctx.renderSkewBadge = () => '';
load(ctx, 'public/observers.js');

const Summary = ctx.window.ObserversSummary;

console.log('\n=== #1562 ObserversSummary helper ===');

t('window.ObserversSummary is exposed', () => {
  assert.ok(Summary, 'expected window.ObserversSummary to be defined');
  assert.strictEqual(typeof Summary.computeCounts, 'function');
  assert.strictEqual(typeof Summary.renderHeader, 'function');
});

t('computeCounts classifies 1 online (15s) / 1 stale (90min) / 1 offline (25h)', () => {
  const now = Date.now();
  const obs = [
    { id: 'a', last_seen: new Date(now - 15 * 1000).toISOString() },
    { id: 'b', last_seen: new Date(now - 90 * 60 * 1000).toISOString() },
    { id: 'c', last_seen: new Date(now - 25 * 60 * 60 * 1000).toISOString() },
  ];
  const r = Summary.computeCounts(obs);
  assert.strictEqual(r.online, 1, 'online=' + r.online);
  assert.strictEqual(r.stale, 1, 'stale=' + r.stale);
  assert.strictEqual(r.offline, 1, 'offline=' + r.offline);
  assert.strictEqual(r.total, 3, 'total=' + r.total);
});

t('computeCounts handles empty + null last_seen as offline', () => {
  const r = Summary.computeCounts([{ id: 'x', last_seen: null }]);
  assert.strictEqual(r.offline, 1);
  assert.strictEqual(r.online, 0);
  assert.strictEqual(r.stale, 0);
});

t('renderHeader includes "Last updated" + relative-time text', () => {
  const fetchedAt = Date.now() - 10 * 1000;
  const html = Summary.renderHeader({ online: 1, stale: 0, offline: 0, total: 1 }, fetchedAt);
  assert.ok(/Last updated/i.test(html), 'should mention "Last updated": ' + html);
  assert.ok(/ago/.test(html), 'should include relative "ago": ' + html);
});

t('renderHeader includes count labels (Online / Stale / Offline)', () => {
  const html = Summary.renderHeader({ online: 5, stale: 2, offline: 3, total: 10 }, Date.now());
  assert.ok(/5\s*Online/.test(html), 'online count: ' + html);
  assert.ok(/2\s*Stale/.test(html), 'stale count: ' + html);
  assert.ok(/3\s*Offline/.test(html), 'offline count: ' + html);
});

t('renderHeader marks obs-updated-stale class when fetchedAt > 60s old', () => {
  const fetchedAt = Date.now() - 90 * 1000;
  const html = Summary.renderHeader({ online: 0, stale: 0, offline: 0, total: 0 }, fetchedAt);
  assert.ok(/obs-updated-stale/.test(html), 'expected obs-updated-stale class: ' + html);
});

t('renderHeader omits stale-warning class when fetchedAt < 60s old', () => {
  const fetchedAt = Date.now() - 10 * 1000;
  const html = Summary.renderHeader({ online: 0, stale: 0, offline: 0, total: 0 }, fetchedAt);
  assert.ok(!/obs-updated-stale/.test(html), 'should NOT mark stale: ' + html);
});

t('renderHeader still renders cleanly when fetchedAt is null/0 (graceful degrade)', () => {
  const html = Summary.renderHeader({ online: 0, stale: 0, offline: 0, total: 0 }, null);
  assert.ok(typeof html === 'string' && html.length > 0, 'returns a non-empty string');
});

console.log('\n=== #1562 DOM-grep checks ===');

const observersSrc = fs.readFileSync(path.join(__dirname, 'public', 'observers.js'), 'utf8');

t('observers.js exposes ObserversSummary global', () => {
  assert.ok(/window\.ObserversSummary\s*=/.test(observersSrc),
    'expected `window.ObserversSummary =` assignment in observers.js');
});

t('observers.js tracks a fetchedAt timestamp', () => {
  assert.ok(/_fetchedAt|fetchedAt/.test(observersSrc),
    'expected fetchedAt tracking');
});

t('observers.js calls Summary.renderHeader (or equivalent) — not the old inline block', () => {
  assert.ok(/ObserversSummary\.renderHeader|Summary\.renderHeader/.test(observersSrc),
    'render() should delegate header HTML to ObserversSummary.renderHeader');
});

t('observers.js bypasses cache on manual refresh (bust: true)', () => {
  assert.ok(/bust\s*:\s*true/.test(observersSrc),
    'expected api(..., { bust: true }) on the manual refresh path');
});

const styleSrc = fs.readFileSync(path.join(__dirname, 'public', 'style.css'), 'utf8');
t('style.css defines .obs-updated-stale visual rule', () => {
  assert.ok(/\.obs-updated-stale\b/.test(styleSrc),
    'expected .obs-updated-stale class in style.css');
});

console.log('\n' + '='.repeat(40));
console.log('  #1562 ObserversSummary: ' + passed + ' passed, ' + failed + ' failed');
console.log('='.repeat(40));
if (failed > 0) process.exit(1);
