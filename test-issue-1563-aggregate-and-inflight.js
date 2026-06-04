/**
 * #1563 — Round-1 review must-fix tests:
 *
 *  A. ObserversSummary aggregate counts MUST come from the SAME
 *     classifier used to render per-row dots (window.observerHealthStatus),
 *     not a parallel hardcoded threshold ladder. Regression pin for #1562:
 *     if anyone re-introduces hardcoded thresholds in the summary, this
 *     test breaks because the per-row tally and the aggregate counts
 *     disagree.
 *
 *  B. loadObservers() must guard against in-flight races: if a slow call
 *     resolves AFTER a newer call, the newer call's data wins (and the
 *     "Last updated" pill reflects the latest fetch, not the stale one).
 */
'use strict';

const fs = require('fs');
const path = require('path');
const vm = require('vm');
const assert = require('assert');

let passed = 0, failed = 0;
function t(name, fn) {
  try {
    const r = fn();
    if (r && typeof r.then === 'function') {
      return r.then(
        () => { passed++; console.log('  ✓ ' + name); },
        (e) => { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
      );
    }
    passed++; console.log('  ✓ ' + name);
  } catch (e) {
    failed++; console.error('  ✗ ' + name + ': ' + e.message);
  }
}

function makeSandbox() {
  const ctx = {
    window: { addEventListener: () => {}, dispatchEvent: () => {} },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '' }),
      head: { appendChild: () => {} },
      getElementById: () => ({ innerHTML: '' }),
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

console.log('\n=== #1563 A. Aggregate uses same classifier as per-row dots ===');

(function runAggregateTests() {
  const ctx = makeSandbox();
  load(ctx, 'public/roles.js');
  load(ctx, 'public/app.js');
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
  const healthStatus = ctx.window.observerHealthStatus;

  t('window.observerHealthStatus is exposed by observers.js', () => {
    assert.strictEqual(typeof healthStatus, 'function',
      'observers.js must expose window.observerHealthStatus so ObserversSummary can call it');
  });

  t('REGRESSION PIN: aggregate counts equal per-row tally for 10 mixed observers', () => {
    const now = Date.now();
    const obs = [
      { id: '1',  last_seen: new Date(now - 5 * 1000).toISOString() },        // green
      { id: '2',  last_seen: new Date(now - 60 * 1000).toISOString() },       // green
      { id: '3',  last_seen: new Date(now - 4 * 60 * 1000).toISOString() },   // green
      { id: '4',  last_seen: new Date(now - 9 * 60 * 1000).toISOString() },   // green
      { id: '5',  last_seen: new Date(now - 15 * 60 * 1000).toISOString() },  // yellow
      { id: '6',  last_seen: new Date(now - 30 * 60 * 1000).toISOString() },  // yellow
      { id: '7',  last_seen: new Date(now - 59 * 60 * 1000).toISOString() },  // yellow
      { id: '8',  last_seen: new Date(now - 2 * 3600 * 1000).toISOString() }, // red
      { id: '9',  last_seen: new Date(now - 24 * 3600 * 1000).toISOString() },// red
      { id: '10', last_seen: null },                                           // red
    ];
    // Per-row tally (same classifier used by the table renderer)
    let rowGreen = 0, rowYellow = 0, rowRed = 0;
    for (const o of obs) {
      const h = healthStatus(o.last_seen);
      if (h.cls === 'health-green') rowGreen++;
      else if (h.cls === 'health-yellow') rowYellow++;
      else rowRed++;
    }
    const agg = Summary.computeCounts(obs);
    assert.strictEqual(agg.online,  rowGreen,  'online ' + agg.online  + ' != per-row green ' + rowGreen);
    assert.strictEqual(agg.stale,   rowYellow, 'stale '  + agg.stale   + ' != per-row yellow ' + rowYellow);
    assert.strictEqual(agg.offline, rowRed,    'offline '+ agg.offline + ' != per-row red '   + rowRed);
    assert.strictEqual(agg.total,   obs.length);
  });

  t('source: observers.js no longer contains the old standalone `classify()` ladder', () => {
    // Defense in depth — make sure the parallel ladder isn't re-introduced.
    const src = fs.readFileSync(path.join(__dirname, 'public', 'observers.js'), 'utf8');
    // Old code had `function classify(lastSeen)` returning string 'online'/'stale'/'offline'
    // The new defaultClassify() returns { cls: 'health-*' } objects.
    assert.ok(!/function\s+classify\s*\(\s*lastSeen\s*\)\s*\{[\s\S]*?return\s+'online'/.test(src),
      'observers.js still has the legacy `classify()` returning string buckets — must be removed');
  });
})();

console.log('\n=== #1563 B. loadObservers in-flight guard ===');

(async function runInflightTests() {
  const ctx = makeSandbox();
  load(ctx, 'public/roles.js');
  load(ctx, 'public/app.js');
  ctx.RegionFilter = { init: () => {}, onChange: () => () => {}, offChange: () => {}, getSelected: () => null };
  ctx.registerPage = (name, page) => { ctx.__page = page; };
  ctx.debouncedOnWS = () => () => {};
  ctx.offWS = () => {};
  ctx.CLIENT_TTL = { observers: 120000 };
  ctx.makeColumnsResizable = () => {};
  ctx.TableResponsive = { register: () => {} };
  ctx.SlideOver = null;
  ctx.observerSkewSeverity = () => 'ok';
  ctx.renderSkewBadge = () => '';

  // Controllable api: returns observers["fast"] or observers["slow"]
  // with deferred resolution per URL. We capture the resolvers so we
  // can control ordering.
  const deferred = {};
  let callIndex = 0;
  ctx.api = function (url) {
    callIndex++;
    if (url === '/observers') {
      let payload;
      if (callIndex === 1) payload = 'slow';
      else if (callIndex === 3) payload = 'fast'; // 3rd api call = 2nd observers fetch
      else payload = 'other';
      return new Promise((resolve) => {
        deferred[payload] = () => resolve({ observers: [{ id: payload, last_seen: new Date().toISOString() }] });
      });
    }
    // /observers/clock-skew etc.
    return Promise.resolve([]);
  };

  load(ctx, 'public/observers.js');

  // We can't easily call the IIFE-private loadObservers directly. Instead,
  // invoke it via the registered page's init() — which calls loadObservers()
  // on mount and again via the refresh button. Since init() is heavy, we
  // assert behavior through observable side effects: window.ObserversSummary
  // helper + the visible #obsContent innerHTML. But cleaner: drive the
  // IIFE through the exposed test seam — we expose loadObservers via a
  // window seam in observers.js. If it doesn't exist, skip the runtime
  // test but the source-grep test below still asserts the guard exists.
  await t('source: loadObservers tracks a monotonic request id', () => {
    const src = fs.readFileSync(path.join(__dirname, 'public', 'observers.js'), 'utf8');
    assert.ok(/_loadObserversReqId|loadObserversReqId/.test(src),
      'observers.js must track a monotonic request id on loadObservers');
    assert.ok(/myId\s*!==\s*_loadObserversReqId/.test(src) || /myId\s*!==\s*loadObserversReqId/.test(src),
      'observers.js must compare per-call id against the latest before applying data');
  });

  await t('source: stale resolutions return early before assigning observers/_fetchedAt', () => {
    const src = fs.readFileSync(path.join(__dirname, 'public', 'observers.js'), 'utf8');
    // The guard must appear BEFORE `observers = data.observers` to actually drop stale data.
    const guardIdx = src.search(/if\s*\(\s*myId\s*!==\s*_loadObserversReqId\s*\)\s*return/);
    const assignIdx = src.search(/observers\s*=\s*data\.observers/);
    assert.ok(guardIdx > -1 && assignIdx > -1, 'both guard and assignment must exist');
    assert.ok(guardIdx < assignIdx, 'guard must come BEFORE observers assignment, else stale data still lands');
  });

  // Runtime race test: fire two calls back-to-back, resolve slow LAST,
  // assert _fetchedAt + observers reflect the SECOND (fast) call's data,
  // not the late-resolving first call.
  await t('runtime: 2nd loadObservers wins even when 1st resolves later', async () => {
    // We need a handle on loadObservers. Re-instantiate a fresh sandbox with
    // a tiny seam injected via post-load eval.
    const ctx2 = makeSandbox();
    load(ctx2, 'public/roles.js');
    load(ctx2, 'public/app.js');
    ctx2.RegionFilter = { init: () => {}, onChange: () => () => {}, offChange: () => {}, getSelected: () => null };
    ctx2.registerPage = () => {};
    ctx2.debouncedOnWS = () => () => {};
    ctx2.offWS = () => {};
    ctx2.CLIENT_TTL = { observers: 120000 };
    ctx2.makeColumnsResizable = () => {};
    ctx2.TableResponsive = { register: () => {} };
    ctx2.SlideOver = null;
    ctx2.observerSkewSeverity = () => 'ok';
    ctx2.renderSkewBadge = () => '';

    const handles = { slow: null, fast: null };
    let n = 0;
    ctx2.api = function (url) {
      if (url === '/observers') {
        n++;
        return new Promise((resolve) => {
          if (n === 1) handles.slow = () => resolve({ observers: [{ id: 'SLOW', last_seen: new Date().toISOString() }] });
          else if (n === 2) handles.fast = () => resolve({ observers: [{ id: 'FAST', last_seen: new Date().toISOString() }] });
          else resolve({ observers: [] });
        });
      }
      return Promise.resolve([]);
    };

    // Inject a seam: re-exec observers.js with a trailing line that
    // exposes loadObservers + the `observers` local + `_fetchedAt` for
    // inspection. We patch the source on the fly.
    const src = fs.readFileSync(path.join(__dirname, 'public', 'observers.js'), 'utf8');
    // Append exposure inside the IIFE by replacing the closing `})();` of
    // the second IIFE with seam code then re-closing.
    const seam =
      "\n  window.__test_loadObservers = loadObservers;\n" +
      "  window.__test_getState = function () { return { observers: observers, fetchedAt: _fetchedAt }; };\n" +
      "})();\n";
    const lastClose = src.lastIndexOf('})();');
    const patched = src.slice(0, lastClose) + seam;
    vm.runInContext(patched, ctx2);

    const load1 = ctx2.window.__test_loadObservers();
    const load2 = ctx2.window.__test_loadObservers();

    // Resolve in reverse order: fast (2nd call) first, then slow (1st call).
    handles.fast();
    await load2;
    const afterFast = ctx2.window.__test_getState();
    assert.strictEqual(afterFast.observers[0].id, 'FAST',
      'after 2nd call resolves, observers should be FAST, got ' + JSON.stringify(afterFast.observers));
    const fetchedAtAfterFast = afterFast.fetchedAt;

    handles.slow();
    await load1;
    const afterSlow = ctx2.window.__test_getState();
    assert.strictEqual(afterSlow.observers[0].id, 'FAST',
      'stale SLOW resolve must NOT clobber FAST data, got ' + JSON.stringify(afterSlow.observers));
    assert.strictEqual(afterSlow.fetchedAt, fetchedAtAfterFast,
      '_fetchedAt must NOT be updated by stale SLOW resolve (would mislead the "Last updated" pill)');
  });
})().then(() => {
  console.log('\n' + '='.repeat(40));
  console.log('  #1563 round-1: ' + passed + ' passed, ' + failed + ' failed');
  console.log('='.repeat(40));
  if (failed > 0) process.exit(1);
});
