/**
 * #2218: a Node Analytics response that lands after the visit ended (or after
 * a newer time range was picked) must not touch the page. Loads the real
 * public/node-analytics.js in a vm sandbox with a held api().
 *
 * Usage: node tests/unit/test-issue-2218-node-analytics-stale-render.js
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const SRC = fs.readFileSync(path.join(__dirname, '..', '..', 'public/node-analytics.js'), 'utf8');
let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}
const flush = () => new Promise((r) => setTimeout(r, 10));
const PK = 'ab'.repeat(32);

function analyticsData(days) {
  return {
    node: { public_key: PK, name: 'Relay One', role: 'repeater' },
    computedStats: { totalTransmissions: days * 100, availabilityPct: 50, signalGrade: 'A', avgPacketsPerDay: 10,
      uniqueObservers: 2, relayPct: 5, longestSilenceMs: 0 },
    timeRange: null, peerInteractions: [], activityTimeline: [], snrTrend: [], packetTypeBreakdown: [],
    observerCoverage: [], hopDistribution: [], uptimeHeatmap: [],
  };
}

function load() {
  const els = {};
  const calls = [];
  const nav = { gen: 0 };
  const hop = { loads: 0 };
  const mkEl = () => ({ style: {}, textContent: '', innerHTML: '',
    insertAdjacentText(position, text) { this.textContent += text; },
  });
  const ctx = {
    console, Math, JSON, Date, Number, String, Array, Object, Promise, isNaN, setTimeout,
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    Chart: Object.assign(function Chart() { this.destroy = () => {}; }, { defaults: {} }),
    CLIENT_TTL: {}, escapeHtml: (s) => String(s), timeAgo: () => '',
    viewGeneration: () => nav.gen,
    api: (url) => new Promise((resolve, reject) => calls.push({ url, resolve, reject })),
    NodeHopAnalytics: { load() { hop.loads++; }, destroy() {} },
  };
  let page = null;
  ctx.registerPage = (name, p) => { if (name === 'node-analytics') page = p; };
  ctx.window = ctx;
  ctx.document = { documentElement: {}, getElementById: (id) => els[id] || (els[id] = mkEl()) };
  // The battery card of whatever page is on screen.
  ['batteryStatusBadge', 'batteryEmpty', 'batteryChart'].forEach((id) => { els[id] = mkEl(); });
  vm.createContext(ctx);
  vm.runInContext(SRC, ctx);
  // The shared #app container; time-range buttons are wired after a render.
  const buttons = {};
  const app = {
    innerHTML: '',
    querySelectorAll: (sel) => sel !== '#timeRangeBtns button' ? [] : ['1', '7', '30', '365'].map((d) => ({
      dataset: { days: d }, addEventListener(t, fn) { buttons[d] = fn; },
    })),
  };
  const call = (prefix) => calls.filter((c) => c.url.startsWith(prefix));
  return { page, app, els, nav, hop, buttons, call };
}

// The real router in public/app.js, so the generation the pages read is the
// one navigate() bumps.
function loadRouter() {
  const listeners = {};
  const app = { innerHTML: '', classList: { toggle() {}, add() {}, remove() {} }, offsetWidth: 0,
    querySelector: () => null, setAttribute() {}, focus() {} };
  const ctx = {
    console, Math, JSON, Date, Number, String, Array, Object, RegExp, Error, Map, Set, Promise, URLSearchParams,
    parseInt, parseFloat, isNaN, isFinite, encodeURIComponent, decodeURIComponent,
    setTimeout: () => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {},
    requestAnimationFrame: () => 0, performance: { now: () => 0 },
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    location: { hash: '' }, history: { replaceState() {} }, CustomEvent: class {},
    document: {
      title: 'CoreScope',
      readyState: 'complete', body: { classList: { toggle() {}, add() {}, remove() {} } },
      createElement: () => ({}), head: { appendChild() {} },
      getElementById: (id) => (id === 'app' ? app : null),
      addEventListener() {}, querySelectorAll: () => [], querySelector: () => null,
    },
  };
  ctx.window = ctx;
  ctx.addEventListener = (t, fn) => { listeners[t] = fn; };
  ctx.dispatchEvent = () => {};
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(path.join(__dirname, '..', '..', 'public/roles.js'), 'utf8'), ctx);
  vm.runInContext(fs.readFileSync(path.join(__dirname, '..', '..', 'public/app.js'), 'utf8'), ctx);
  const go = (hash) => { ctx.location.hash = hash; listeners.hashchange(); };
  return { ctx, go };
}

(async () => {
  console.log('Router: current view in the browser title (#2259)');
  {
    const r = loadRouter();
    for (const page of ['home', 'packets', 'packet-detail', 'nodes', 'node-analytics', 'node-reach', 'observer-detail', 'analytics', 'account']) {
      r.ctx.registerPage(page, { init() {}, destroy() {} });
    }
    for (const [hash, title] of [
      ['#/home', 'CoreScope'],
      ['#/packets', 'Packets | CoreScope'],
      ['#/nodes', 'Nodes | CoreScope'],
      ['#/nodes/' + PK, 'Node abababababab | CoreScope'],
      ['#/nodes/' + PK + '/reach', 'Node abababababab | Reach | CoreScope'],
      ['#/nodes/' + PK + '/analytics', 'Node abababababab | Analytics | CoreScope'],
      ['#/packet/123', 'Packet 123 | CoreScope'],
      ['#/packets/abcdef?obs=2', 'Packet abcdef | CoreScope'],
      ['#/observers/observer-one', 'Observer observer-one | CoreScope'],
      ['#/account/login', 'Account | CoreScope'],
      ['#/account/activate?token=sample-value', 'Account | CoreScope'],
      ['#/account/reset/sample-value', 'Account | CoreScope'],
      ['#/home', 'CoreScope'],
    ]) {
      r.go(hash);
      assert(r.ctx.document.title === title, hash + ': ' + title);
    }
    assert(typeof r.ctx.setSiteTitle === 'function' && typeof r.ctx.setRouteTitle === 'function',
      'branding and detail updates have shared title helpers');
    if (r.ctx.setSiteTitle && r.ctx.setRouteTitle) {
      r.go('#/nodes');
      r.ctx.setSiteTitle('Example Mesh');
      assert(r.ctx.document.title === 'Nodes | Example Mesh', 'late branding preserves the current page');
      r.ctx.setRouteTitle('packets', 'late-packet');
      assert(r.ctx.document.title === 'Nodes | Example Mesh', 'a detail callback from another page cannot replace the title');
      r.ctx.setSiteTitle('  ');
      assert(r.ctx.document.title === 'Nodes | CoreScope', 'blank branding falls back without accumulating suffixes');
    }
  }
  console.log('Router: viewGeneration()');
  {
    const r = loadRouter();
    const seen = [];
    r.ctx.registerPage('nodes', { init() { seen.push(['nodes', r.ctx.viewGeneration()]); }, destroy() {} });
    r.ctx.registerPage('node-analytics', {
      init() { seen.push(['init', r.ctx.viewGeneration()]); }, destroy() {},
    });
    r.go('#/nodes/' + PK + '/analytics');
    r.go('#/nodes/' + 'cd'.repeat(32) + '/analytics');
    r.go('#/nodes');
    const gens = seen.map((e) => e[1]);
    assert(gens.length === 3 && gens[0] < gens[1] && gens[1] < gens[2], 'every init sees a new generation, another node included (' + gens + ')');
  }

  console.log('Node Analytics: response after navigating away');
  {
    const t = load();
    t.page.init(t.app, PK + '/analytics');
    t.page.destroy();
    t.nav.gen++;
    t.app.innerHTML = 'NODES LIST';
    t.call('/nodes/')[0].resolve(analyticsData(7));
    await flush();
    assert(t.app.innerHTML === 'NODES LIST', 'a late success leaves the next page alone');
    assert(t.hop.loads === 0, 'and starts no nested hop-count load');
  }
  {
    const t = load();
    t.page.init(t.app, PK + '/analytics');
    t.page.destroy();
    t.nav.gen++;
    t.app.innerHTML = 'NODES LIST';
    t.call('/nodes/')[0].reject(new Error('timeout'));
    await flush();
    assert(t.app.innerHTML === 'NODES LIST', 'a late error leaves the next page alone');
  }

  console.log('Node Analytics: another node, then back');
  {
    const t = load();
    t.page.init(t.app, PK + '/analytics');
    const first = t.call('/nodes/')[0];
    t.page.destroy(); t.nav.gen++;
    t.page.init(t.app, 'cd'.repeat(32) + '/analytics');
    t.page.destroy(); t.nav.gen++;
    t.page.init(t.app, PK + '/analytics');
    first.resolve(Object.assign(analyticsData(7), { node: { public_key: PK, name: 'STALE VISIT' } }));
    await flush();
    assert(!t.app.innerHTML.includes('STALE VISIT'), 'the first visit does not render into a later visit of the same node');
    t.call('/nodes/')[2].resolve(analyticsData(7));
    await flush();
    assert(t.app.innerHTML.includes('Relay One') && t.app.innerHTML.includes('700 packets in 7d'), 'the current visit renders its response');
  }

  console.log('Node Analytics: an older time range landing last');
  {
    const t = load();
    t.page.init(t.app, PK + '/analytics');
    t.call('/nodes/')[0].resolve(analyticsData(7));
    await flush();
    t.buttons['1']();
    t.buttons['30']();
    const [r1, r30] = t.call('/nodes/' + PK + '/analytics').slice(1);
    r30.resolve(analyticsData(30));
    await flush();
    r1.resolve(analyticsData(1));
    await flush();
    assert(t.app.innerHTML.includes('3000 packets in 30d'), 'the newest range stays on screen');
  }

  console.log('Node Analytics: nested battery work');
  {
    const t = load();
    t.page.init(t.app, PK + '/analytics');
    t.call('/nodes/')[0].resolve(analyticsData(7));
    await flush();
    const battery = t.call('/nodes/' + PK + '/battery')[0];
    assert(!!battery, 'the current render starts the battery load');
    t.page.destroy(); t.nav.gen++;
    battery.resolve({ status: 'ok', latest_mv: 4100, samples: [] });
    await flush();
    assert(t.els.batteryStatusBadge.textContent === '' && t.els.batteryEmpty.style.display === undefined,
      'a late battery answer does not touch the next page');
  }
  {
    const t = load();
    t.page.init(t.app, PK + '/analytics');
    t.call('/nodes/')[0].resolve(analyticsData(7));
    await flush();
    t.page.destroy(); t.nav.gen++;
    t.call('/nodes/' + PK + '/battery')[0].reject(new Error('gone'));
    await flush();
    assert(t.els.batteryEmpty.textContent === '', 'a late battery error does not touch the next page');
  }
  {
    const t = load();
    t.page.init(t.app, PK + '/analytics');
    t.call('/nodes/')[0].resolve(analyticsData(7));
    await flush();
    t.call('/nodes/' + PK + '/battery')[0].resolve({ status: 'ok', latest_mv: 4100, samples: [] });
    await flush();
    assert(t.els.batteryStatusBadge.textContent.includes('4100 mV'), 'the current battery answer still renders');
  }

  console.log('Node Analytics: the current error still renders');
  {
    const t = load();
    t.page.init(t.app, PK + '/analytics');
    t.call('/nodes/')[0].reject(new Error('server down'));
    await flush();
    assert(t.app.innerHTML.includes('Failed to load analytics: server down'), 'the current rejection renders');
  }

  console.log('Node Analytics: battery badge separates icons from data (#2232)');
  for (const [status, mv, label, color] of [
    ['ok', 4100, 'OK', 'var(--success)'],
    ['low', 3200, 'Low', 'var(--warning)'],
    ['critical', 2900, 'Critical', 'var(--danger)'],
    ['critical', 0, 'Critical', 'var(--danger)'],
    ['unknown', null, 'No data', 'var(--text-muted)'],
    ['constructor', null, 'No data', 'var(--text-muted)'],
    ['ok', '<img src=x onerror=alert(1)>', 'OK', 'var(--success)'],
  ]) {
    const t = load();
    t.page.init(t.app, PK + '/analytics');
    t.call('/nodes/')[0].resolve(analyticsData(7));
    await flush();
    t.call('/nodes/' + PK + '/battery')[0].resolve({ status, latest_mv: mv, samples: [] });
    await flush();
    const badge = t.els.batteryStatusBadge;
    assert(badge.innerHTML.includes(label), status + ': trusted badge label is rendered');
    assert(!badge.textContent.includes('<svg'), status + ': SVG source is not displayed as text');
    assert(!badge.innerHTML.includes('<img'), status + ': voltage never enters HTML');
    assert(badge.textContent === (mv == null ? '' : ' · ' + mv + ' mV'), status + ': voltage is appended as text, including zero');
    assert(badge.style.color === color, status + ': badge uses its theme color');
  }

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})();
