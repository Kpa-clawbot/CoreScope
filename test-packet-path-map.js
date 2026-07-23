/**
 * Tests for public/packet-path-map.js — the on-demand "View path" modal
 * that draws a packet's resolved relay path on a Leaflet map (backed by
 * GET /api/packets/{hash}/path, cmd/server/db.go GetPacketPath).
 *
 * Two layers, matching this repo's established pattern for modal/DOM
 * code (see test-channel-modal-ux.js): string-contract checks over the
 * raw source for structural/safety properties, plus a functional smoke
 * test using a minimal-but-real DOM mock (createElement/appendChild/
 * getElementById/remove all actually work, unlike the channels.js test
 * sandbox's inert stubs) to exercise open()/close() end-to-end on the
 * two code paths that don't need Leaflet: a failed fetch, and a
 * fetch that resolves with nothing plottable.
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

const src = fs.readFileSync('public/packet-path-map.js', 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

console.log('\n=== packet-path-map.js: string-contract checks ===');

test('exports window.PacketPathMap.{open,close}', () => {
  assert.ok(/window\.PacketPathMap\s*=\s*\{\s*open:\s*open,\s*close:\s*close\s*\}/.test(src));
});

test('fetches via the shared api() helper, not a raw fetch (picks up auth/base-URL handling)', () => {
  assert.ok(/api\(\s*'\/packets\/'\s*\+\s*encodeURIComponent\(hash\)\s*\+\s*'\/path'\s*\)/.test(src));
});

test('escapes node/observer names before interpolating into tooltip HTML (operator-controlled data)', () => {
  assert.ok(/escapeHtml\(p\.name\)/.test(src), 'point tooltips must escape the name');
});

test('handles Escape key and click-outside to close, matching other CoreScope modals', () => {
  assert.ok(/e\.key === 'Escape'/.test(src));
  assert.ok(/e\.target === overlay/.test(src));
});

test('degrades gracefully when the Leaflet global is unavailable, rather than throwing', () => {
  assert.ok(/typeof L === 'undefined'/.test(src));
});

test('close() tears down the Leaflet map instance, not just the DOM overlay (avoids a leaked map on repeat opens)', () => {
  assert.ok(/activeMap\.remove\(\)/.test(src));
});

console.log('\n=== packet-path-map.js: functional smoke test (no-Leaflet code paths) ===');

function makeSandbox(apiImpl) {
  // A minimal but REAL DOM: elements track their own children/attributes
  // so createElement -> appendChild -> getElementById -> remove() all
  // actually work, unlike the inert stubs used for pure string-render
  // testing elsewhere. Deliberately small: only what open()/close() touch.
  function makeElement(tag) {
    const el = {
      tagName: tag, children: [], attributes: {}, style: {}, dataset: {},
      _listeners: {},
      get id() { return this.attributes.id || ''; },
      set id(v) { this.attributes.id = v; },
      // Real innerHTML would parse into a live child tree; this mock only
      // needs id-addressable children with a settable textContent (all
      // open()/close() read back), so it scans for id="..." occurrences
      // and registers one lightweight child per id found.
      set innerHTML(html) {
        this._innerHTML = html;
        this.children = [];
        const re = /id="([^"]+)"/g;
        let m;
        while ((m = re.exec(html))) {
          const child = makeElement('div');
          child.id = m[1];
          this.appendChild(child);
        }
      },
      get innerHTML() { return this._innerHTML || ''; },
      set textContent(t) { this._text = t; },
      get textContent() { return this._text || ''; },
      appendChild(child) { this.children.push(child); child._parent = this; return child; },
      remove() { if (this._parent) this._parent.children = this._parent.children.filter(c => c !== this); },
      addEventListener(type, fn) { (this._listeners[type] = this._listeners[type] || []).push(fn); },
      removeEventListener(type, fn) { if (this._listeners[type]) this._listeners[type] = this._listeners[type].filter(f => f !== fn); },
      querySelector() { return null; },
    };
    return el;
  }

  const body = makeElement('body');
  const docListeners = {};
  const doc = {
    createElement: makeElement,
    body,
    documentElement: { style: {} },
    getElementById(id) {
      const search = (el) => {
        if (el.id === id) return el;
        for (const c of el.children) { const found = search(c); if (found) return found; }
        return null;
      };
      return search(body);
    },
    addEventListener(type, fn) { (docListeners[type] = docListeners[type] || []).push(fn); },
    removeEventListener(type, fn) { if (docListeners[type]) docListeners[type] = docListeners[type].filter(f => f !== fn); },
  };

  const ctx = {
    window: {}, document: doc, console, Math, String, JSON, Promise, Error,
    setTimeout, clearTimeout,
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    escapeHtml: (s) => String(s == null ? '' : s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;'),
    api: apiImpl,
    L: undefined, // Leaflet deliberately absent -- these tests only cover the no-plot-data / no-Leaflet paths.
  };
  vm.createContext(ctx);
  vm.runInContext(src, ctx);
  return ctx;
}

(async () => {
  await (async () => {
    try {
      const ctx = makeSandbox(() => Promise.reject(new Error('network down')));
      await ctx.window.PacketPathMap.open('deadbeef');
      const overlay = ctx.document.getElementById('packetPathModal');
      const status = ctx.document.getElementById('packetPathStatus');
      assert.ok(overlay, 'modal overlay should be created');
      assert.ok(status.textContent.includes('Failed to load path'), 'should surface the fetch error, got: ' + status.textContent);
      passed++;
      console.log('  ✅ a failed fetch shows an error status without throwing');
    } catch (e) { failed++; console.log('  ❌ a failed fetch shows an error status without throwing: ' + e.message); }
  })();

  await (async () => {
    try {
      const ctx = makeSandbox(() => Promise.resolve({ hash: 'deadbeef', hops: 0, points: [] }));
      await ctx.window.PacketPathMap.open('deadbeef');
      const status = ctx.document.getElementById('packetPathStatus');
      assert.ok(status.textContent.includes('no resolved relay path'), 'should explain there is nothing to show yet, got: ' + status.textContent);
      passed++;
      console.log('  ✅ an empty path (hops=0, no points) shows a clear "nothing to show" status');
    } catch (e) { failed++; console.log('  ❌ an empty path (hops=0, no points) shows a clear "nothing to show" status: ' + e.message); }
  })();

  await (async () => {
    try {
      const ctx = makeSandbox(() => Promise.resolve({ hash: 'deadbeef', hops: 3, points: [{ publicKey: 'pk1', name: 'RepeaterA', lat: null, lon: null }] }));
      await ctx.window.PacketPathMap.open('deadbeef');
      const status = ctx.document.getElementById('packetPathStatus');
      assert.ok(status.textContent.includes('3 hop'), 'should mention the hop count even when no hop has a known position, got: ' + status.textContent);
      passed++;
      console.log('  ✅ hops with no known position at all still report the hop count, not a silent blank');
    } catch (e) { failed++; console.log('  ❌ hops with no known position at all still report the hop count, not a silent blank: ' + e.message); }
  })();

  await (async () => {
    try {
      const ctx = makeSandbox(() => Promise.reject(new Error('boom')));
      await ctx.window.PacketPathMap.open('deadbeef');
      assert.ok(ctx.document.getElementById('packetPathModal'), 'modal should be open');
      ctx.window.PacketPathMap.close();
      assert.ok(!ctx.document.getElementById('packetPathModal'), 'modal should be removed after close()');
      passed++;
      console.log('  ✅ close() removes the modal overlay from the DOM');
    } catch (e) { failed++; console.log('  ❌ close() removes the modal overlay from the DOM: ' + e.message); }
  })();

  console.log('\n════════════════════════════════════════');
  console.log(`  packet-path-map.js: ${passed} passed, ${failed} failed`);
  console.log('════════════════════════════════════════');
  if (failed > 0) process.exit(1);
})();
