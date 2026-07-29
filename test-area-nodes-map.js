/**
 * Tests for public/area-nodes-map.js — the small "click an area, see its
 * nodes on a map" modal (Tools > Position-Fix Coverage Gaps), reusing
 * packet-path-map.js's modal/Leaflet conventions but stripped down to
 * plain pins (no path lines, no branch highlighting).
 *
 * Same two-layer pattern as test-packet-path-map.js: string-contract
 * checks over the raw source, plus a functional smoke test using a
 * minimal-but-real DOM mock so open()/close() can be exercised
 * end-to-end (with and without a mocked Leaflet global).
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

const src = fs.readFileSync('public/area-nodes-map.js', 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

console.log('\n=== area-nodes-map.js: string-contract checks ===');

test('exports window.AreaNodesMap.{open,close}', () => {
  assert.ok(/window\.AreaNodesMap\s*=\s*\{\s*open:\s*open,\s*close:\s*close\s*\}/.test(src));
});

test('escapes the area label and node names before interpolating into HTML (operator-controlled data)', () => {
  assert.ok(/escapeHtml\(label\)/.test(src), 'modal title must escape the area label');
  assert.ok(/escapeHtml\(p\.name/.test(src), 'marker tooltip must escape the node name');
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

test('drops points with no known lat/lon rather than plotting NaN markers', () => {
  assert.ok(/typeof p\.lat === 'number' && typeof p\.lon === 'number'/.test(src));
});

console.log('\n=== area-nodes-map.js: functional smoke test ===');

function makeSandbox() {
  // Same minimal-but-real DOM mock as test-packet-path-map.js: elements
  // track their own children/attributes so createElement -> appendChild
  // -> getElementById -> remove() all actually work.
  function makeElement(tag) {
    const el = {
      tagName: tag, children: [], attributes: {}, style: {}, dataset: {},
      _listeners: {},
      get id() { return this.attributes.id || ''; },
      set id(v) { this.attributes.id = v; },
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
    getComputedStyle: () => ({ getPropertyValue: (name) => name }),
    escapeHtml: (s) => String(s == null ? '' : s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;'),
    L: undefined, // absent by default -- individual tests opt in
  };
  vm.createContext(ctx);
  vm.runInContext(src, ctx);
  return ctx;
}

test('opens the modal with the area label as title and a count in the description', () => {
  const ctx = makeSandbox();
  ctx.window.AreaNodesMap.open('Slagelse', [{ publicKey: 'pk1', name: 'Node1', lat: 55.4, lon: 11.4 }]);
  const overlay = ctx.document.getElementById('areaNodesMapModal');
  assert.ok(overlay, 'modal overlay should be created');
  assert.ok(overlay.innerHTML.includes('Slagelse'), 'title should include the area label');
  assert.ok(overlay.innerHTML.includes('1 estimated node'), 'description should count the points, got: ' + overlay.innerHTML);
});

test('pluralizes the node count correctly for 0 and 2+', () => {
  const ctx = makeSandbox();
  ctx.window.AreaNodesMap.open('EmptyArea', []);
  let overlay = ctx.document.getElementById('areaNodesMapModal');
  assert.ok(overlay.innerHTML.includes('0 estimated nodes'), 'expected plural for 0, got: ' + overlay.innerHTML);

  ctx.window.AreaNodesMap.open('TwoArea', [
    { publicKey: 'pk1', name: 'A', lat: 55.0, lon: 10.0 },
    { publicKey: 'pk2', name: 'B', lat: 55.1, lon: 10.1 },
  ]);
  overlay = ctx.document.getElementById('areaNodesMapModal');
  assert.ok(overlay.innerHTML.includes('2 estimated nodes'), 'expected plural for 2, got: ' + overlay.innerHTML);
});

test('does not throw when the Leaflet global is unavailable', () => {
  const ctx = makeSandbox();
  assert.doesNotThrow(() => ctx.window.AreaNodesMap.open('NoLeaflet', [{ publicKey: 'pk1', name: 'A', lat: 55.0, lon: 10.0 }]));
  assert.ok(ctx.document.getElementById('areaNodesMapModal'), 'modal should still open (just without a rendered map)');
});

test('close() removes the modal overlay from the DOM', () => {
  const ctx = makeSandbox();
  ctx.window.AreaNodesMap.open('CloseTest', []);
  assert.ok(ctx.document.getElementById('areaNodesMapModal'), 'modal should be open');
  ctx.window.AreaNodesMap.close();
  assert.ok(!ctx.document.getElementById('areaNodesMapModal'), 'modal should be removed after close()');
});

test('plots one marker per point with lat/lon, skipping points missing either', () => {
  const ctx = makeSandbox();
  let markerCount = 0;
  ctx.L = {
    map: () => ({ setView() { return this; }, fitBounds() {}, invalidateSize() {}, remove() {} }),
    tileLayer: () => ({ addTo() { return this; } }),
    circleMarker: () => { markerCount++; return { addTo() { return this; }, bindTooltip() { return this; }, on() { return this; } }; },
  };
  ctx.window.AreaNodesMap.open('MixedPoints', [
    { publicKey: 'pk1', name: 'HasPosition', lat: 55.0, lon: 10.0 },
    { publicKey: 'pk2', name: 'NoLat', lat: null, lon: 10.0 },
    { publicKey: 'pk3', name: 'NoLon', lat: 55.0, lon: null },
  ]);
  assert.strictEqual(markerCount, 1, 'expected exactly 1 marker (the other two are missing lat/lon), got ' + markerCount);
});

test('clicking a marker navigates to the node detail page and closes the modal', () => {
  const ctx = makeSandbox();
  ctx.window.location = { hash: '' };
  let clickHandler = null;
  ctx.L = {
    map: () => ({ setView() { return this; }, fitBounds() {}, invalidateSize() {}, remove() {} }),
    tileLayer: () => ({ addTo() { return this; } }),
    circleMarker: () => ({
      addTo() { return this; },
      bindTooltip() { return this; },
      on(type, fn) { if (type === 'click') clickHandler = fn; return this; },
    }),
  };
  ctx.window.AreaNodesMap.open('ClickTest', [{ publicKey: 'targetpk', name: 'Target', lat: 55.0, lon: 10.0 }]);
  assert.ok(clickHandler, 'expected a click handler to be registered on the marker');
  clickHandler();
  assert.strictEqual(ctx.window.location.hash, '#/nodes/targetpk', 'expected navigation to the node detail page');
  assert.ok(!ctx.document.getElementById('areaNodesMapModal'), 'expected the modal to close on marker click');
});

test('fitBounds is called with every plotted point when there is at least one', () => {
  const ctx = makeSandbox();
  let boundsSeen = null;
  ctx.L = {
    map: () => ({ setView() { return this; }, fitBounds(b) { boundsSeen = b; }, invalidateSize() {}, remove() {} }),
    tileLayer: () => ({ addTo() { return this; } }),
    circleMarker: () => ({ addTo() { return this; }, bindTooltip() { return this; }, on() { return this; } }),
  };
  ctx.window.AreaNodesMap.open('Bounds', [
    { publicKey: 'pk1', name: 'A', lat: 55.0, lon: 10.0 },
    { publicKey: 'pk2', name: 'B', lat: 56.0, lon: 11.0 },
  ]);
  // JSON comparison, not deepStrictEqual: boundsSeen's arrays were
  // constructed inside the vm context (a separate JS realm) so they
  // fail a strict cross-realm structural comparison against host-realm
  // array literals despite being value-identical.
  assert.strictEqual(JSON.stringify(boundsSeen), JSON.stringify([[55.0, 10.0], [56.0, 11.0]]), 'expected fitBounds called with both points\' coordinates, got: ' + JSON.stringify(boundsSeen));
});

test('does not throw when there are zero plottable points', () => {
  const ctx = makeSandbox();
  let fitBoundsCalled = false;
  ctx.L = {
    map: () => ({ setView() { return this; }, fitBounds() { fitBoundsCalled = true; }, invalidateSize() {}, remove() {} }),
    tileLayer: () => ({ addTo() { return this; } }),
    circleMarker: () => ({ addTo() { return this; }, bindTooltip() { return this; }, on() { return this; } }),
  };
  assert.doesNotThrow(() => ctx.window.AreaNodesMap.open('Empty', []));
  assert.strictEqual(fitBoundsCalled, false, 'fitBounds should not be called with an empty bounds array');
});

console.log('\n════════════════════════════════════════');
console.log(`  area-nodes-map.js: ${passed} passed, ${failed} failed`);
console.log('════════════════════════════════════════');
process.exit(failed === 0 ? 0 : 1);
