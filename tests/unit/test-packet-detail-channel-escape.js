/**
 * The packet detail pane renders a decoded channel name as text. Approved
 * channel proposals (docs/specs/2026-10-07-channel-proposals-design.md) make
 * decoded.channel a user-chosen name, and the name rules allow "<" and ">",
 * so the message meta line must escape it. Drives the real renderDetail.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

function makeSandbox() {
  const registeredPages = {};
  const ctx = {
    window: {
      addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => {},
      innerWidth: 1200, PacketFilter: null,
    },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '', className: '', style: {},
        appendChild: () => {}, setAttribute: () => {}, addEventListener: () => {},
        querySelectorAll: () => [], querySelector: () => null,
        classList: { add: () => {}, remove: () => {}, contains: () => false } }),
      head: { appendChild: () => {} }, getElementById: () => null,
      addEventListener: () => {}, removeEventListener: () => {},
      querySelectorAll: () => [], querySelector: () => null, body: { appendChild: () => {} },
    },
    console, Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp,
    Error, TypeError, RangeError, parseInt, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent,
    setTimeout: () => {}, clearTimeout: () => {}, setInterval: () => {}, clearInterval: () => {},
    fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    localStorage: (() => { const s = {}; return {
      getItem: k => s[k] || null, setItem: (k, v) => { s[k] = String(v); }, removeItem: k => { delete s[k]; },
    }; })(),
    location: { hash: '' }, history: { replaceState: () => {} },
    CustomEvent: class CustomEvent {}, Map, Set, Promise, URLSearchParams,
    addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => {},
    requestAnimationFrame: (cb) => setTimeout(cb, 0),
    registerPage: (name, handler) => { registeredPages[name] = handler; },
  };
  vm.createContext(ctx);
  return ctx;
}

function loadInCtx(ctx, file) {
  vm.runInContext(fs.readFileSync(file, 'utf8'), ctx, { filename: file });
  for (const k of Object.keys(ctx.window)) { ctx[k] = ctx.window[k]; }
}

function loadPacketsSandbox() {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/payload-labels.js');
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  loadInCtx(ctx, 'public/packet-helpers.js');
  loadInCtx(ctx, 'public/hop-resolver.js');
  vm.runInContext(`
    window.HopDisplay = {
      renderHop: function(h) { return '<span>' + h + '</span>'; },
      _showFromBtn: function() {}
    };
  `, ctx);
  loadInCtx(ctx, 'public/packets.js');
  ctx.fetchAllNodes = () => Promise.resolve({ nodes: [] });
  ctx.api = (path) => Promise.resolve(path === '/observers' ? { observers: [] } : {});
  return ctx;
}

const XSS = '#<img src=x onerror=alert(1)>';

async function detailHtml(decoded) {
  const ctx = loadPacketsSandbox();
  const panel = { innerHTML: '', querySelectorAll: () => [], querySelector: () => null, addEventListener: () => {} };
  await ctx._packetsTestAPI.renderDetail(panel, {
    packet: { id: 1, hash: 'h1', raw_hex: '1540' + 'AA'.repeat(24), route_type: 1, payload_type: 5,
      decoded_json: JSON.stringify(decoded), path_json: '[]' },
    observations: [],
  });
  const m = panel.innerHTML.match(/<div class="detail-message"[\s\S]*?<\/div>\s*<\/div>/);
  assert.ok(m, 'renderDetail did not render the message preview');
  return m[0];
}

(async () => {
  console.log('\n=== packet detail: channel name in the message meta ===');

  await test('a channel name with markup renders inert', async () => {
    const html = await detailHtml({ type: 'CHAN', channel: XSS, sender: 'a', text: 'x' });
    assert.ok(!/<img\b/i.test(html), 'raw <img survived: ' + html);
    assert.ok(html.includes('#&lt;img src=x onerror=alert(1)&gt;'), 'escaped name missing');
  });
  await test('a plain channel name still shows', async () => {
    const html = await detailHtml({ type: 'CHAN', channel: '#test', sender: 'a', text: 'x' });
    assert.ok(html.includes('>#test'), 'channel name missing');
  });

  console.log('');
  if (failed > 0) {
    console.error(`❌ ${failed} test(s) failed, ${passed} passed`);
    process.exit(1);
  }
  console.log(`✅ All ${passed} tests passed`);
})();
