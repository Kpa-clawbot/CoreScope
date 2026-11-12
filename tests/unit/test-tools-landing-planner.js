/* Tools landing page: planner card on warm and cold loads (review #2391 point 6). */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');
process.chdir(path.resolve(__dirname, '..', '..'));

let passed = 0, failed = 0;
const pending = [];
function test(name, fn) {
  pending.push(Promise.resolve().then(fn).then(
    () => { passed++; console.log('  ✅ ' + name); },
    (e) => { failed++; console.log('  ❌ ' + name + ': ' + e.message); }));
}

function makeCtx() {
  const ctx = {
    window: { addEventListener() {}, dispatchEvent() {} },
    document: {
      readyState: 'complete', title: '',
      createElement: () => ({ id: '', textContent: '', innerHTML: '' }),
      head: { appendChild() {} }, getElementById: () => null, addEventListener() {},
      querySelectorAll: () => [], querySelector: () => null,
    },
    console, Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp, Error, TypeError,
    parseInt, parseFloat, isNaN, isFinite, encodeURIComponent, decodeURIComponent,
    setTimeout() {}, clearTimeout() {}, setInterval() {}, clearInterval() {},
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    location: { hash: '' }, CustomEvent: class {}, Map, Promise, URLSearchParams,
    addEventListener() {}, dispatchEvent() {}, requestAnimationFrame: (cb) => cb(),
  };
  vm.createContext(ctx);
  for (const f of ['public/payload-labels.js', 'public/roles.js', 'public/app.js']) {
    vm.runInContext(fs.readFileSync(f, 'utf8'), ctx);
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  }
  return ctx;
}

// Minimal container: holds the HTML string, supports the few DOM calls the page makes.
function makeContainer() {
  const c = {
    innerHTML: '',
    querySelector(sel) {
      if (sel !== '.tools-menu' || c.innerHTML.indexOf('tools-menu') < 0) return null;
      return {
        firstElementChild: {
          insertAdjacentHTML(_w, html) { c.innerHTML = c.innerHTML.replace('</a>', '</a>' + html); },
        },
        querySelector: (s) => (s === 'a[href="#/tools/planner"]' && c.innerHTML.indexOf('href="#/tools/planner"') >= 0 ? {} : null),
        insertAdjacentHTML() {},
      };
    },
  };
  // Make firstElementChild.insertAdjacentHTML reachable from menu.firstElementChild.
  return c;
}
const count = (c) => (c.innerHTML.match(/href="#\/tools\/planner"/g) || []).length;
const tick = () => new Promise((r) => setImmediate(r));
function page(ctx) { return vm.runInContext("pages['tools-landing']", ctx); }

console.log('\n=== tools landing: planner card ===');

test('config already loaded: card present, second after Path Inspector', () => {
  const ctx = makeCtx(); ctx.window.MC_PLANNER_CFG = { enabled: true };
  const c = makeContainer(); page(ctx).init(c);
  assert.strictEqual(count(c), 1);
  assert.ok(c.innerHTML.indexOf('Path Inspector') < c.innerHTML.indexOf('Repeater Planner'));
  assert.ok(c.innerHTML.indexOf('Repeater Planner') < c.innerHTML.indexOf('Trace Viewer'));
});

test('cold load: absent, then present once MeshConfigReady resolves', async () => {
  const ctx = makeCtx(); let resolve;
  ctx.window.MeshConfigReady = new Promise((r) => { resolve = r; });
  const c = makeContainer(); page(ctx).init(c);
  assert.strictEqual(count(c), 0);
  ctx.window.MC_PLANNER_CFG = { enabled: true }; resolve(); await tick();
  assert.strictEqual(count(c), 1);
  assert.ok(c.innerHTML.indexOf('Path Inspector') < c.innerHTML.indexOf('Repeater Planner'));
  assert.ok(c.innerHTML.indexOf('Repeater Planner') < c.innerHTML.indexOf('Trace Viewer'));
});

test('cold load resolving with planner null: no card', async () => {
  const ctx = makeCtx(); let resolve;
  ctx.window.MeshConfigReady = new Promise((r) => { resolve = r; });
  const c = makeContainer(); page(ctx).init(c);
  ctx.window.MC_PLANNER_CFG = null; resolve(); await tick();
  assert.strictEqual(count(c), 0);
});

test('navigated away before resolve: nothing inserted, no error', async () => {
  const ctx = makeCtx(); let resolve;
  ctx.window.MeshConfigReady = new Promise((r) => { resolve = r; });
  const c = makeContainer(); const p = page(ctx); p.init(c); p.destroy();
  ctx.window.MC_PLANNER_CFG = { enabled: true }; resolve(); await tick();
  assert.strictEqual(count(c), 0);
});

test('re-init before resolve does not duplicate the card', async () => {
  const ctx = makeCtx(); let resolve;
  ctx.window.MeshConfigReady = new Promise((r) => { resolve = r; });
  const c = makeContainer(); const p = page(ctx); p.init(c); p.init(c);
  ctx.window.MC_PLANNER_CFG = { enabled: true }; resolve(); await tick();
  assert.strictEqual(count(c), 1);
});

Promise.all(pending).then(() => {
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed ? 1 : 0);
});
