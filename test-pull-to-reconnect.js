/* test-pull-to-reconnect.js — behavioral tests for pull-to-reconnect (#1063)
 * Loads app.js in a vm sandbox, stubs WebSocket + DOM, asserts that:
 *  - pullReconnect() exists as a global helper
 *  - calling it closes the existing WS (which triggers the existing
 *    auto-reconnect path)
 *  - setupPullToReconnect() exists and wires touchstart/touchmove/touchend
 *    listeners on the document
 *  - a pull-down gesture at scrollTop=0 over the threshold triggers
 *    pullReconnect
 *  - a touch when scrollTop > 0 does NOT trigger pullReconnect (don't
 *    hijack normal scrolling)
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

console.log('--- test-pull-to-reconnect.js ---');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}\n     ${e.stack.split('\n').slice(1, 3).join('\n     ')}`); }
}

function makeSandbox(opts) {
  opts = opts || {};
  const listeners = {}; // event name -> [fn]
  const elements = {};
  function makeEl(id) {
    const el = {
      id, textContent: '', innerHTML: '', value: '',
      style: {}, dataset: {},
      _classes: new Set(),
      classList: {
        add: function() { for (const a of arguments) el._classes.add(a); },
        remove: function() { for (const a of arguments) el._classes.delete(a); },
        toggle: function(c) { if (el._classes.has(c)) el._classes.delete(c); else el._classes.add(c); },
        contains: function(c) { return el._classes.has(c); },
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

  // Pre-create elements app.js touches at WS time
  makeEl('liveDot');

  // Stub WebSocket — track instances + close calls
  const wsInstances = [];
  function FakeWS(url) {
    this.url = url;
    this.readyState = 1; // OPEN
    this.closed = false;
    this.onopen = null; this.onclose = null; this.onerror = null; this.onmessage = null;
    wsInstances.push(this);
    // simulate immediate open so onopen fires synchronously isn't required;
    // tests will invoke handlers directly when needed.
  }
  FakeWS.prototype.close = function() {
    this.closed = true;
    if (typeof this.onclose === 'function') this.onclose({});
  };
  FakeWS.prototype.send = function() {};

  const body = makeEl('body');

  const ctx = {
    console,
    setTimeout: function(fn, ms) { return 0; }, // suppress reconnect loop
    clearTimeout: function() {},
    setInterval: function() { return 0; },
    clearInterval: function() {},
    Date, Math, JSON, Object, Array, String, Number, Boolean,
    Error, RegExp, Map, Set, Symbol, Promise,
    requestAnimationFrame: function(fn) { return 0; },
    performance: { now: function() { return 0; } },
    location: { protocol: 'http:', host: 'localhost', hash: '' },
    navigator: { userAgent: 'test' },
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
      documentElement: { scrollTop: opts.scrollTop || 0, style: { setProperty: function() {} }, setAttribute: function() {}, getAttribute: function() { return null; } },
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
      ontouchstart: opts.touch === false ? undefined : null,
    },
    CustomEvent: function(type, init) { this.type = type; this.detail = (init || {}).detail; },
  };
  ctx.window.location = ctx.location;
  ctx.window.localStorage = ctx.localStorage;
  ctx.window.document = ctx.document;
  ctx.self = ctx.window;
  ctx.globalThis = ctx;

  vm.createContext(ctx);
  return { ctx, elements, wsInstances, listeners };
}

function loadApp(box) {
  const src = fs.readFileSync('public/app.js', 'utf8');
  vm.runInContext(src, box.ctx);
}

console.log('\n=== pullReconnect helper exists ===');
test('pullReconnect is exposed on window', () => {
  const box = makeSandbox();
  loadApp(box);
  assert.strictEqual(typeof box.ctx.window.pullReconnect, 'function',
    'window.pullReconnect must be a function');
});

console.log('\n=== setupPullToReconnect exists ===');
test('setupPullToReconnect is exposed on window', () => {
  const box = makeSandbox();
  loadApp(box);
  assert.strictEqual(typeof box.ctx.window.setupPullToReconnect, 'function',
    'window.setupPullToReconnect must be a function');
});

console.log('\n=== pullReconnect closes existing WS ===');
test('calling pullReconnect() closes the current WebSocket', () => {
  const box = makeSandbox();
  loadApp(box);
  // app.js does NOT call connectWS until DOMContentLoaded. Force one:
  box.ctx.window.connectWS && box.ctx.window.connectWS();
  // If app.js doesn't expose connectWS, fall back to invoking pullReconnect
  // and checking that something tries to open a new socket.
  const beforeCount = box.wsInstances.length;
  box.ctx.window.pullReconnect();
  // Either: existing WS got closed, OR a new WS was opened (reconnect)
  const closed = box.wsInstances.some(function(w) { return w.closed; });
  const opened = box.wsInstances.length > beforeCount;
  assert.ok(closed || opened,
    'pullReconnect must close the WS or open a new one (got closed=' + closed + ', opened=' + opened + ')');
});

console.log('\n=== setupPullToReconnect wires document touch listeners ===');
test('setupPullToReconnect attaches touchstart listener', () => {
  const box = makeSandbox();
  loadApp(box);
  box.ctx.window.setupPullToReconnect();
  assert.ok((box.listeners['touchstart'] || []).length > 0,
    'touchstart listener must be attached to document');
  assert.ok((box.listeners['touchmove'] || []).length > 0,
    'touchmove listener must be attached to document');
  assert.ok((box.listeners['touchend'] || []).length > 0,
    'touchend listener must be attached to document');
});

console.log('\n=== Pull gesture at scrollTop=0 triggers reconnect ===');
test('pull-down past threshold at scrollTop=0 triggers pullReconnect', () => {
  const box = makeSandbox({ scrollTop: 0 });
  loadApp(box);
  box.ctx.window.connectWS && box.ctx.window.connectWS();
  box.ctx.window.setupPullToReconnect();

  let triggered = false;
  const orig = box.ctx.window.pullReconnect;
  box.ctx.window.pullReconnect = function() { triggered = true; return orig.apply(this, arguments); };

  function fire(name, y) {
    (box.listeners[name] || []).forEach(function(fn) {
      fn({ touches: [{ clientY: y }], changedTouches: [{ clientY: y }], preventDefault: function() {}, type: name });
    });
  }
  fire('touchstart', 10);
  fire('touchmove', 100);
  fire('touchmove', 200);
  fire('touchend', 200);

  assert.ok(triggered, 'pullReconnect must be called after pull > threshold at scrollTop=0');
});

console.log('\n=== Pull gesture when scrolled DOWN does NOT trigger ===');
test('pull when scrollTop > 0 does NOT trigger pullReconnect', () => {
  const box = makeSandbox({ scrollTop: 500 });
  loadApp(box);
  box.ctx.window.connectWS && box.ctx.window.connectWS();
  box.ctx.window.setupPullToReconnect();

  let triggered = false;
  box.ctx.window.pullReconnect = function() { triggered = true; };

  function fire(name, y) {
    (box.listeners[name] || []).forEach(function(fn) {
      fn({ touches: [{ clientY: y }], changedTouches: [{ clientY: y }], preventDefault: function() {}, type: name });
    });
  }
  fire('touchstart', 10);
  fire('touchmove', 200);
  fire('touchend', 200);

  assert.strictEqual(triggered, false,
    'pullReconnect must NOT fire when page is scrolled (scrollTop > 0)');
});

console.log('\n=== Results: ' + passed + ' passed, ' + failed + ' failed ===\n');
process.exit(failed > 0 ? 1 : 0);
