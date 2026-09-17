/* test-pull-to-reconnect-1091.js — gesture-tuning tests for issue #1091
 *
 * Pull-to-reconnect must require a DELIBERATE pull (~140px) at scrollTop=0.
 * Short pulls and incidental scrolls must NOT trigger the reconnect.
 *
 * Failing-first assertions (red commit before fix):
 *  - 100px pull at scrollTop=0 must NOT trigger (old code triggers at 80px)
 *  - Lifting finger before threshold must NOT trigger
 *  - scrollTop changing from 0 mid-gesture must NOT trigger
 *
 * Passing assertions (sanity):
 *  - 50px pull (well below threshold): no trigger
 *  - 160px pull (above 140px threshold): triggers
 *  - Pull from non-zero scrollTop: no trigger
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

console.log('--- test-pull-to-reconnect-1091.js ---');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}\n     ${e.stack.split('\n').slice(1, 3).join('\n     ')}`); }
}

function makeSandbox(opts) {
  opts = opts || {};
  const listeners = {};
  const elements = {};
  function makeEl(id) {
    const el = {
      id, textContent: '', innerHTML: '', value: '',
      style: {}, dataset: {},
      _classes: new Set(),
      classList: {
        add: function() {}, remove: function() {}, toggle: function() {}, contains: function() { return false; },
      },
      addEventListener: function(ev, fn) { (el['_on_' + ev] = el['_on_' + ev] || []).push(fn); },
      removeEventListener: function() {},
      setAttribute: function() {}, getAttribute: function() { return null; },
      appendChild: function(child) { (el._children = el._children || []).push(child); return child; },
      remove: function() {},
      querySelector: function() { return null; },
      querySelectorAll: function() { return []; },
    };
    elements[id] = el;
    return el;
  }
  makeEl('liveDot');

  const wsInstances = [];
  function FakeWS(url) {
    this.url = url;
    this.readyState = 1;
    this.closed = false;
    this.onopen = null; this.onclose = null; this.onerror = null; this.onmessage = null;
    wsInstances.push(this);
  }
  FakeWS.prototype.close = function() {
    this.closed = true;
    if (typeof this.onclose === 'function') this.onclose({});
  };
  FakeWS.prototype.send = function() {};

  const body = makeEl('body');
  // Mutable scrollTop so tests can change mid-gesture
  const docEl = {
    scrollTop: opts.scrollTop || 0,
    style: { setProperty: function() {} },
    setAttribute: function() {}, getAttribute: function() { return null; },
  };

  const ctx = {
    console,
    setTimeout: function() { return 0; },
    clearTimeout: function() {},
    setInterval: function() { return 0; },
    clearInterval: function() {},
    Date, Math, JSON, Object, Array, String, Number, Boolean,
    Error, RegExp, Map, Set, Symbol, Promise,
    requestAnimationFrame: function() { return 0; },
    performance: { now: function() { return 0; } },
    location: { protocol: 'http:', host: 'localhost', hash: '' },
    navigator: { userAgent: 'test', maxTouchPoints: 5 },
    WebSocket: FakeWS,
    fetch: function() { return Promise.resolve({ ok: true, json: function() { return Promise.resolve({}); } }); },
    localStorage: {
      _data: {},
      getItem: function(k) { return this._data[k] || null; },
      setItem: function(k, v) { this._data[k] = String(v); },
      removeItem: function(k) { delete this._data[k]; },
    },
    document: {
      readyState: 'complete',
      documentElement: docEl,
      body: body,
      head: { appendChild: function() {} },
      createElement: function(tag) { return makeEl(tag); },
      getElementById: function(id) { return elements[id] || null; },
      querySelector: function() { return null; },
      querySelectorAll: function() { return []; },
      addEventListener: function(ev, fn) { (listeners[ev] = listeners[ev] || []).push(fn); },
      removeEventListener: function() {},
      dispatchEvent: function(e) { (listeners[e.type] || []).forEach(function(fn) { fn(e); }); return true; },
    },
    window: {
      addEventListener: function() {}, removeEventListener: function() {}, dispatchEvent: function() {},
      matchMedia: function() { return { matches: false, addEventListener: function() {} }; },
      ontouchstart: null,
    },
    CustomEvent: function(type, init) { this.type = type; this.detail = (init || {}).detail; },
  };
  ctx.window.location = ctx.location;
  ctx.window.localStorage = ctx.localStorage;
  ctx.window.document = ctx.document;
  ctx.window.navigator = ctx.navigator;
  ctx.self = ctx.window;
  ctx.globalThis = ctx;

  vm.createContext(ctx);
  return { ctx, elements, wsInstances, listeners, docEl };
}

function loadApp(box) {
  const src = fs.readFileSync('public/app.js', 'utf8');
  vm.runInContext(src, box.ctx);
}

function fire(box, name, y, opts) {
  opts = opts || {};
  (box.listeners[name] || []).forEach(function(fn) {
    fn({
      touches: name === 'touchend' ? [] : [{ clientY: y }],
      changedTouches: [{ clientY: y }],
      preventDefault: opts.preventDefault || function() {},
      cancelable: true,
      type: name,
    });
  });
}

function setupAndStub(box) {
  box.ctx.window.connectWS && box.ctx.window.connectWS();
  box.ctx.window.setupPullToReconnect();
  let triggered = false;
  box.ctx.window.pullReconnect = function() { triggered = true; };
  return { isTriggered: function() { return triggered; } };
}

console.log('\n=== Issue #1091: gesture must require deliberate pull ===');

test('50px pull at scrollTop=0: NO reconnect (well below threshold)', () => {
  const box = makeSandbox({ scrollTop: 0 });
  loadApp(box);
  const t = setupAndStub(box);
  fire(box, 'touchstart', 100);
  fire(box, 'touchmove', 130);
  fire(box, 'touchmove', 150);
  fire(box, 'touchend', 150);
  assert.strictEqual(t.isTriggered(), false, '50px pull must NOT trigger reconnect');
});

test('100px pull at scrollTop=0: NO reconnect (above old 80px, below new 140px) — REGRESSION GUARD', () => {
  const box = makeSandbox({ scrollTop: 0 });
  loadApp(box);
  const t = setupAndStub(box);
  fire(box, 'touchstart', 100);
  fire(box, 'touchmove', 150);
  fire(box, 'touchmove', 200);
  fire(box, 'touchend', 200);
  assert.strictEqual(t.isTriggered(), false,
    '100px pull must NOT trigger reconnect (this is the bug from #1091 — old threshold 80px triggered here)');
});

test('160px pull at scrollTop=0: triggers reconnect (above 140px threshold)', () => {
  const box = makeSandbox({ scrollTop: 0 });
  loadApp(box);
  const t = setupAndStub(box);
  fire(box, 'touchstart', 100);
  fire(box, 'touchmove', 200);
  fire(box, 'touchmove', 270);
  fire(box, 'touchend', 270);
  assert.strictEqual(t.isTriggered(), true, '160px pull MUST trigger reconnect');
});

test('Pull from non-zero scrollTop: NO reconnect', () => {
  const box = makeSandbox({ scrollTop: 500 });
  loadApp(box);
  const t = setupAndStub(box);
  fire(box, 'touchstart', 100);
  fire(box, 'touchmove', 200);
  fire(box, 'touchmove', 300);
  fire(box, 'touchend', 300);
  assert.strictEqual(t.isTriggered(), false, 'pull from scrolled page must NOT trigger');
});

test('Lift before threshold (only 90px) does not trigger reconnect', () => {
  const box = makeSandbox({ scrollTop: 0 });
  loadApp(box);
  const t = setupAndStub(box);
  fire(box, 'touchstart', 100);
  fire(box, 'touchmove', 190); // 90px — below 140
  fire(box, 'touchend', 190);
  assert.strictEqual(t.isTriggered(), false, 'lifting before 140px must NOT trigger');
});

test('scrollTop changes from 0 mid-gesture: NO reconnect', () => {
  const box = makeSandbox({ scrollTop: 0 });
  loadApp(box);
  const t = setupAndStub(box);
  fire(box, 'touchstart', 100);
  fire(box, 'touchmove', 150);
  // Page scrolled mid-gesture (e.g., user scrolled up while holding)
  box.docEl.scrollTop = 50;
  fire(box, 'touchmove', 280);
  fire(box, 'touchend', 280);
  assert.strictEqual(t.isTriggered(), false,
    'gesture must cancel when scrollTop leaves 0 mid-pull');
});

test('preventDefault is NOT called below threshold (lets natural scroll work)', () => {
  const box = makeSandbox({ scrollTop: 0 });
  loadApp(box);
  setupAndStub(box);
  let prevented = 0;
  function pd() { prevented++; }
  function fireWith(name, y) {
    (box.listeners[name] || []).forEach(function(fn) {
      fn({
        touches: name === 'touchend' ? [] : [{ clientY: y }],
        changedTouches: [{ clientY: y }],
        preventDefault: pd, cancelable: true, type: name,
      });
    });
  }
  fireWith('touchstart', 100);
  fireWith('touchmove', 130); // 30px — well below 140
  fireWith('touchmove', 180); // 80px — still below 140
  fireWith('touchend', 180);
  assert.strictEqual(prevented, 0,
    'preventDefault must NOT fire while gesture is below the commit threshold');
});

test('Pull past 140px then retract dy<=0 below threshold then touchend: NO reconnect (MINOR-1 regression)', () => {
  const box = makeSandbox({ scrollTop: 0 });
  loadApp(box);
  const t = setupAndStub(box);
  fire(box, 'touchstart', 100);
  fire(box, 'touchmove', 200); // 100px - below threshold
  fire(box, 'touchmove', 260); // 160px - past threshold, pulling=true
  fire(box, 'touchmove', 90);  // dy = -10, retract past threshold back upward
  fire(box, 'touchend', 90);
  assert.strictEqual(t.isTriggered(), false,
    'retracting past-threshold pull (dy<=0) must reset pulling/dist so touchend does NOT fire reconnect');
});

console.log('\n=== Results: ' + passed + ' passed, ' + failed + ' failed ===\n');
process.exit(failed > 0 ? 1 : 0);
