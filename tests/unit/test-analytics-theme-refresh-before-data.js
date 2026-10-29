/**
 * Analytics: a theme-refresh that fires before the analytics answers land.
 *
 * Every page load fires a theme-refresh about 300ms after the theme resolves.
 * On a slow server the answers land later, and re-rendering the Overview from
 * the empty data object threw (reading 'totalTransmissions'). The handler
 * must leave the tab alone until loadAnalytics() renders it.
 *
 * Usage: node tests/unit/test-analytics-theme-refresh-before-data.js
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

function el() {
  return {
    style: {}, dataset: {}, value: '', innerHTML: '',
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
    addEventListener() {}, removeEventListener() {},
    querySelector: () => null, querySelectorAll: () => [],
  };
}
const els = {};
let contentLookups = 0;
const listeners = {};
const ctx = {
  console, Math, JSON, Date, Number, String, Array, Object, Set, Map, RegExp, URLSearchParams, Promise,
  setTimeout, clearTimeout, requestAnimationFrame: () => 0,
  getComputedStyle: () => ({ getPropertyValue: () => '' }),
  localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  location: { hash: '#/analytics' },
  addEventListener: (type, fn) => { listeners[type] = fn; },
  removeEventListener() {},
  initTabBar() {}, CLIENT_TTL: {}, viewGeneration: () => 0,
  // The answers never land: the refresh below fires while they are pending.
  api: () => new Promise(() => {}),
  RegionFilter: { init() {}, onChange() {}, regionQueryString: () => '' },
  AreaFilter: { init() {}, onChange() {}, areaQueryString: () => '' },
};
let page = null;
ctx.registerPage = (name, p) => { if (name === 'analytics') page = p; };
ctx.window = ctx;
ctx.document = {
  documentElement: {}, createElement: el,
  addEventListener() {}, removeEventListener() {}, querySelector: () => null, querySelectorAll: () => [],
  getElementById: (id) => {
    if (id === 'analyticsContent') contentLookups++;
    return els[id] || (els[id] = el());
  },
};
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(path.join(__dirname, '..', '..', 'public/analytics.js'), 'utf8'), ctx);

const rejections = [];
process.on('unhandledRejection', (e) => rejections.push(e));

(async () => {
  await page.init(el());
  const refresh = listeners['theme-refresh'];
  assert(typeof refresh === 'function', 'init registers a theme-refresh handler');
  const before = contentLookups;
  let threw = null;
  try { refresh(); } catch (e) { threw = e; }
  await new Promise((r) => setTimeout(r, 20));
  assert(!threw && rejections.length === 0, 'a refresh before the data lands does not throw (' + (threw || rejections[0] || 'ok') + ')');
  assert(contentLookups === before, 'and does not re-render the tab');
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})();
