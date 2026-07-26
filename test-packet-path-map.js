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
  assert.ok(/escapeHtml\(pt\.name\)/.test(src), 'point tooltips must escape the name');
});

test('tooltips use a wrapping CSS class -- Leaflet\'s default nowrap tooltip becomes unreadable once role/approx/distance/timing info are all combined', () => {
  const bindCalls = (src.match(/\.bindTooltip\(/g) || []).length;
  const classNameUses = (src.match(/className:\s*'packet-path-tooltip'/g) || []).length;
  assert.ok(bindCalls > 0, 'expected at least one bindTooltip call');
  assert.strictEqual(classNameUses, bindCalls, `expected every bindTooltip call (${bindCalls}) to pass the wrapping class, found ${classNameUses}`);
});

test('draws every branch, not just the deepest one', () => {
  assert.ok(/branches\.map/.test(src), 'should iterate all branches from the response');
});

test('draws the deepest branch on top of the others (primary drawn last)', () => {
  assert.ok(/a\.primary \? 1 : 0/.test(src) || /primary.*sort/.test(src), 'should reorder so the primary branch paints last');
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
    // Returns the variable name itself (not a real color) so tests can
    // assert two markers use DIFFERENT css vars without caring what the
    // actual theme color is.
    getComputedStyle: () => ({ getPropertyValue: (name) => name }),
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
      const ctx = makeSandbox(() => Promise.resolve({ hash: 'deadbeef', branches: [] }));
      await ctx.window.PacketPathMap.open('deadbeef');
      const status = ctx.document.getElementById('packetPathStatus');
      assert.ok(status.textContent.includes('no observations'), 'should explain there is nothing to show yet, got: ' + status.textContent);
      passed++;
      console.log('  ✅ no branches at all shows a clear "nothing to show" status');
    } catch (e) { failed++; console.log('  ❌ no branches at all shows a clear "nothing to show" status: ' + e.message); }
  })();

  await (async () => {
    try {
      const ctx = makeSandbox(() => Promise.resolve({
        hash: 'deadbeef',
        branches: [{ hops: 3, points: [{ publicKey: 'pk1', name: 'RepeaterA', lat: null, lon: null }], observer: null }],
      }));
      await ctx.window.PacketPathMap.open('deadbeef');
      const status = ctx.document.getElementById('packetPathStatus');
      assert.ok(status.textContent.includes('3 hop'), 'should mention the hop count even when no branch has a known position, got: ' + status.textContent);
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

  await (async () => {
    try {
      // Two branches: a 2-hop chain (deepest, drawn primary) and a
      // 0-hop direct observer with no resolvable relay names at all.
      // Both should still get plotted -- this is the whole point of the
      // "show every branch" rework (a station that heard the packet
      // directly is real reach data even without a relay chain).
      const ctx = makeSandbox(() => Promise.resolve({
        hash: 'deadbeef',
        branches: [
          {
            hops: 2,
            points: [
              { publicKey: 'pk1', name: 'RepeaterA', lat: 56.0, lon: 10.0 },
              { publicKey: 'pk2', name: 'RepeaterB', lat: 56.1, lon: 10.1 },
            ],
            observer: { name: 'FarObserver', lat: 56.2, lon: 10.2 },
          },
          { hops: 0, points: [], observer: { name: 'NearObserver', lat: 55.9, lon: 9.9 } },
        ],
      }));

      let markerCount = 0, polylineCount = 0;
      ctx.L = {
        map: () => ({
          setView() { return this; },
          fitBounds() {},
          invalidateSize() {},
          remove() {},
        }),
        tileLayer: () => ({ addTo() { return this; } }),
        circleMarker: () => { markerCount++; return { addTo() { return this; }, bindTooltip() { return this; }, on() { return this; } }; },
        polyline: () => { polylineCount++; return { addTo() { return this; } }; },
      };

      await ctx.window.PacketPathMap.open('deadbeef');
      const status = ctx.document.getElementById('packetPathStatus');
      assert.strictEqual(markerCount, 4, 'expected 4 markers: 2 hops + observer for branch 1, 1 observer-only point for branch 2, got ' + markerCount);
      assert.strictEqual(polylineCount, 1, 'expected exactly 1 polyline (only the 3-point branch has >1 point to connect), got ' + polylineCount);
      assert.ok(status.textContent.includes('2 of 2 stations shown'), 'status should report both branches plotted, got: ' + status.textContent);
      assert.ok(status.textContent.includes('deepest reached 2 hop'), 'status should report the deepest branch hop count, got: ' + status.textContent);
      passed++;
      console.log('  ✅ multiple branches (including a 0-hop direct observer) are all plotted, not just the deepest');
    } catch (e) { failed++; console.log('  ❌ multiple branches (including a 0-hop direct observer) are all plotted, not just the deepest: ' + e.message); }
  })();

  await (async () => {
    try {
      // `first` is the earliest-arriving observation (usually 0 hops,
      // close to the sender) -- distinct from the deepest branch. It
      // should get its own extra landmark marker on top of the branch
      // dots, and be called out in the status line.
      const ctx = makeSandbox(() => Promise.resolve({
        hash: 'deadbeef',
        branches: [
          { hops: 5, points: [], observer: { name: 'FarObserver', lat: 56.2, lon: 10.2 } },
        ],
        first: { hops: 0, points: [], observer: { name: 'NearObserver', lat: 55.9, lon: 9.9 } },
      }));

      let markerCount = 0;
      ctx.L = {
        map: () => ({
          setView() { return this; },
          fitBounds() {},
          invalidateSize() {},
          remove() {},
        }),
        tileLayer: () => ({ addTo() { return this; } }),
        circleMarker: () => { markerCount++; return { addTo() { return this; }, bindTooltip() { return this; }, on() { return this; } }; },
        polyline: () => ({ addTo() { return this; } }),
      };

      await ctx.window.PacketPathMap.open('deadbeef');
      const status = ctx.document.getElementById('packetPathStatus');
      assert.strictEqual(markerCount, 2, 'expected 2 markers: the branch observer dot plus the extra first-observer landmark ring, got ' + markerCount);
      assert.ok(status.textContent.includes('entered near NearObserver'), 'status should call out the first observer, got: ' + status.textContent);
      passed++;
      console.log('  ✅ the earliest-arriving observation gets its own landmark marker and status callout');
    } catch (e) { failed++; console.log('  ❌ the earliest-arriving observation gets its own landmark marker and status callout: ' + e.message); }
  })();

  await (async () => {
    try {
      // A hop point and an observer with `approx: true` (server borrowed
      // the position from their strongest neighbor -- see GetPacketPath's
      // nearestPositionedNeighbor). Must render hollow/dashed, not as a
      // solid dot indistinguishable from a real fix, and get called out
      // in the status line.
      const ctx = makeSandbox(() => Promise.resolve({
        hash: 'deadbeef',
        branches: [
          {
            hops: 1,
            points: [{ publicKey: 'pk1', name: 'GhostRepeater', lat: 56.0, lon: 10.0, approx: true }],
            observer: { name: 'GhostObserver', lat: 56.1, lon: 10.1, approx: true },
          },
        ],
      }));

      let approxMarkerCalls = 0, solidMarkerCalls = 0;
      ctx.L = {
        map: () => ({
          setView() { return this; },
          fitBounds() {},
          invalidateSize() {},
          remove() {},
        }),
        tileLayer: () => ({ addTo() { return this; } }),
        circleMarker: (latlng, opts) => {
          if (opts && opts.dashArray) approxMarkerCalls++;
          else solidMarkerCalls++;
          return { addTo() { return this; }, bindTooltip() { return this; }, on() { return this; } };
        },
        polyline: () => ({ addTo() { return this; } }),
      };

      await ctx.window.PacketPathMap.open('deadbeef');
      const status = ctx.document.getElementById('packetPathStatus');
      assert.strictEqual(approxMarkerCalls, 2, 'expected both the hop point and the observer to render as hollow/dashed (approx), got ' + approxMarkerCalls);
      assert.strictEqual(solidMarkerCalls, 0, 'expected no solid markers -- both points in this branch are approximate, got ' + solidMarkerCalls);
      assert.ok(status.textContent.includes('2 approximate'), 'status should call out the approximate count, got: ' + status.textContent);
      passed++;
      console.log('  ✅ approximate (neighbor-borrowed) positions render hollow/dashed and are called out in status');
    } catch (e) { failed++; console.log('  ❌ approximate (neighbor-borrowed) positions render hollow/dashed and are called out in status: ' + e.message); }
  })();

  await (async () => {
    try {
      // branch.secondsAfterFirst (0 for the earliest arrival, positive
      // for later ones) should show up in the observer's tooltip label.
      const ctx = makeSandbox(() => Promise.resolve({
        hash: 'deadbeef',
        branches: [
          { hops: 2, points: [], observer: { name: 'LateObserver', lat: 56.0, lon: 10.0 }, secondsAfterFirst: 4.7 },
        ],
        first: { hops: 0, points: [], observer: { name: 'LateObserver', lat: 56.0, lon: 10.0 }, secondsAfterFirst: 0 },
      }));

      let tooltips = [];
      ctx.L = {
        map: () => ({ setView() { return this; }, fitBounds() {}, invalidateSize() {}, remove() {} }),
        tileLayer: () => ({ addTo() { return this; } }),
        circleMarker: () => ({ addTo() { return this; }, bindTooltip(t) { tooltips.push(t); return this; }, on() { return this; } }),
        polyline: () => ({ addTo() { return this; } }),
      };

      await ctx.window.PacketPathMap.open('deadbeef');
      assert.ok(tooltips.some((t) => t.includes('+4.7s')), 'expected a tooltip with the +4.7s elapsed time, got: ' + JSON.stringify(tooltips));
      passed++;
      console.log('  ✅ secondsAfterFirst renders as an elapsed-time label in the tooltip');
    } catch (e) { failed++; console.log('  ❌ secondsAfterFirst renders as an elapsed-time label in the tooltip: ' + e.message); }
  })();

  await (async () => {
    try {
      // branch.distanceFromFirstKm (> 0) should show up in the observer's
      // tooltip label; exactly 0 (First itself) should not add a
      // redundant "0.0 km away".
      const ctx = makeSandbox(() => Promise.resolve({
        hash: 'deadbeef',
        branches: [
          { hops: 2, points: [], observer: { name: 'FarObserver', lat: 56.0, lon: 10.0 }, distanceFromFirstKm: 42.3 },
        ],
        first: { hops: 0, points: [], observer: { name: 'FarObserver', lat: 56.0, lon: 10.0 }, distanceFromFirstKm: 0 },
      }));

      let tooltips = [];
      ctx.L = {
        map: () => ({ setView() { return this; }, fitBounds() {}, invalidateSize() {}, remove() {} }),
        tileLayer: () => ({ addTo() { return this; } }),
        circleMarker: () => ({ addTo() { return this; }, bindTooltip(t) { tooltips.push(t); return this; }, on() { return this; } }),
        polyline: () => ({ addTo() { return this; } }),
      };

      await ctx.window.PacketPathMap.open('deadbeef');
      assert.ok(tooltips.some((t) => t.includes('42.3 km away')), 'expected a tooltip with the 42.3 km distance, got: ' + JSON.stringify(tooltips));
      assert.ok(!tooltips.some((t) => t.includes('0.0 km away')), 'did not expect a "0.0 km away" label, got: ' + JSON.stringify(tooltips));
      passed++;
      console.log('  ✅ distanceFromFirstKm renders as a "N km away" label in the tooltip');
    } catch (e) { failed++; console.log('  ❌ distanceFromFirstKm renders as a "N km away" label in the tooltip: ' + e.message); }
  })();

  await (async () => {
    try {
      // A single-neighbor approx point should render with a bigger,
      // fainter ring than a 4-neighbor approx point -- more agreeing
      // neighbors means more confidence, so a tighter, more solid marker.
      const ctx = makeSandbox(() => Promise.resolve({
        hash: 'deadbeef',
        branches: [
          {
            hops: 2,
            points: [
              { publicKey: 'pk1', name: 'LowConfidence', lat: 56.0, lon: 10.0, approx: true, approxNeighborCount: 1 },
              { publicKey: 'pk2', name: 'HighConfidence', lat: 56.1, lon: 10.1, approx: true, approxNeighborCount: 4, approxSpreadKm: 5 },
            ],
            observer: null,
          },
        ],
      }));

      const markerOptsByName = {};
      const tooltipByCall = [];
      ctx.L = {
        map: () => ({ setView() { return this; }, fitBounds() {}, invalidateSize() {}, remove() {} }),
        tileLayer: () => ({ addTo() { return this; } }),
        circleMarker: (latlng, opts) => {
          tooltipByCall.push(opts);
          return { addTo() { return this; }, bindTooltip(t) { markerOptsByName[t] = opts; return this; }, on() { return this; } };
        },
        polyline: () => ({ addTo() { return this; } }),
      };

      await ctx.window.PacketPathMap.open('deadbeef');
      const lowKey = Object.keys(markerOptsByName).find((k) => k.includes('LowConfidence'));
      const highKey = Object.keys(markerOptsByName).find((k) => k.includes('HighConfidence'));
      assert.ok(lowKey, 'expected a tooltip for LowConfidence');
      assert.ok(highKey, 'expected a tooltip for HighConfidence');
      assert.ok(markerOptsByName[lowKey].radius > markerOptsByName[highKey].radius,
        'expected the 1-neighbor marker to be larger than the 4-neighbor marker, got radii ' + markerOptsByName[lowKey].radius + ' vs ' + markerOptsByName[highKey].radius);
      assert.ok(markerOptsByName[lowKey].fillOpacity < markerOptsByName[highKey].fillOpacity,
        'expected the 1-neighbor marker to be fainter than the 4-neighbor marker');
      assert.ok(lowKey.includes('from 1 neighbor'), 'expected the tooltip to mention the neighbor count, got: ' + lowKey);
      assert.ok(highKey.includes('from 4 neighbors'), 'expected the tooltip to mention the neighbor count, got: ' + highKey);
      passed++;
      console.log('  ✅ approximate markers scale size/opacity by neighbor confidence');
    } catch (e) { failed++; console.log('  ❌ approximate markers scale size/opacity by neighbor confidence: ' + e.message); }
  })();

  await (async () => {
    try {
      // A hop point and an observer with a known `role` should get a
      // role-specific icon prefix in their tooltip.
      const ctx = makeSandbox(() => Promise.resolve({
        hash: 'deadbeef',
        branches: [
          {
            hops: 1,
            points: [{ publicKey: 'pk1', name: 'RepeaterA', lat: 56.0, lon: 10.0, role: 'repeater' }],
            observer: { name: 'RoomObserver', lat: 56.1, lon: 10.1, role: 'room' },
          },
        ],
      }));

      const tooltips = [];
      ctx.L = {
        map: () => ({ setView() { return this; }, fitBounds() {}, invalidateSize() {}, remove() {} }),
        tileLayer: () => ({ addTo() { return this; } }),
        circleMarker: () => ({ addTo() { return this; }, bindTooltip(t) { tooltips.push(t); return this; }, on() { return this; } }),
        polyline: () => ({ addTo() { return this; } }),
      };

      await ctx.window.PacketPathMap.open('deadbeef');
      assert.ok(tooltips.some((t) => t.includes('📡') && t.includes('RepeaterA')), 'expected a repeater icon on RepeaterA, got: ' + JSON.stringify(tooltips));
      assert.ok(tooltips.some((t) => t.includes('🏠') && t.includes('RoomObserver')), 'expected a room icon on RoomObserver, got: ' + JSON.stringify(tooltips));
      passed++;
      console.log('  ✅ nodes with a known role get a role icon in their tooltip');
    } catch (e) { failed++; console.log('  ❌ nodes with a known role get a role icon in their tooltip: ' + e.message); }
  })();

  await (async () => {
    try {
      // A marker with a publicKey should register a click handler that
      // navigates to #/nodes/{pubkey} (closing the modal first); one
      // without a publicKey should register no click handler at all.
      const ctx = makeSandbox(() => Promise.resolve({
        hash: 'deadbeef',
        branches: [
          {
            hops: 1,
            points: [{ publicKey: 'pk-with-key', name: 'HasKey', lat: 56.0, lon: 10.0 }],
            observer: { name: 'NoKeyObserver', lat: 56.1, lon: 10.1 }, // no publicKey
          },
        ],
      }));
      ctx.window.location = { hash: '' };

      const clickHandlersByTooltip = {};
      let lastTooltip = null;
      ctx.L = {
        map: () => ({ setView() { return this; }, fitBounds() {}, invalidateSize() {}, remove() {} }),
        tileLayer: () => ({ addTo() { return this; } }),
        circleMarker: () => ({
          addTo() { return this; },
          bindTooltip(t) { lastTooltip = t; return this; },
          on(evt, fn) { if (evt === 'click') clickHandlersByTooltip[lastTooltip] = fn; return this; },
        }),
        polyline: () => ({ addTo() { return this; } }),
      };

      await ctx.window.PacketPathMap.open('deadbeef');
      const hasKeyTooltip = Object.keys(clickHandlersByTooltip).find((t) => t.includes('HasKey'));
      assert.ok(hasKeyTooltip, 'expected a click handler registered for the HasKey marker, got: ' + JSON.stringify(Object.keys(clickHandlersByTooltip)));
      assert.ok(hasKeyTooltip.includes('click for node detail'), 'expected the tooltip to hint it is clickable, got: ' + hasKeyTooltip);
      assert.ok(!Object.keys(clickHandlersByTooltip).some((t) => t.includes('NoKeyObserver')), 'expected NO click handler for the keyless observer');

      clickHandlersByTooltip[hasKeyTooltip]();
      assert.strictEqual(ctx.window.location.hash, '#/nodes/pk-with-key', 'expected clicking the marker to navigate to the node detail hash route, got: ' + ctx.window.location.hash);
      assert.ok(!ctx.document.getElementById('packetPathModal'), 'expected the modal to close after navigating away');
      passed++;
      console.log('  ✅ markers with a publicKey are clickable and navigate to node detail, closing the modal');
    } catch (e) { failed++; console.log('  ❌ markers with a publicKey are clickable and navigate to node detail, closing the modal: ' + e.message); }
  })();

  await (async () => {
    try {
      // touchedAreas is the server-resolved, uncapped list of every
      // configured area any point/observer on the path fell in -- View
      // Path has room to show all of them (unlike the pong reply's
      // capped "+N more" version).
      const ctx = makeSandbox(() => Promise.resolve({
        hash: 'deadbeef',
        branches: [{ hops: 0, points: [], observer: { name: 'Obs', lat: 56.0, lon: 10.0 } }],
        touchedAreas: ['Aarhus by', 'Djursland', 'Odense by'],
      }));
      ctx.L = {
        map: () => ({ setView() { return this; }, fitBounds() {}, invalidateSize() {}, remove() {} }),
        tileLayer: () => ({ addTo() { return this; } }),
        circleMarker: () => ({ addTo() { return this; }, bindTooltip() { return this; }, on() { return this; } }),
        polyline: () => ({ addTo() { return this; } }),
      };

      await ctx.window.PacketPathMap.open('deadbeef');
      const status = ctx.document.getElementById('packetPathStatus');
      assert.ok(status.textContent.includes('touched Aarhus by, Djursland, Odense by'), 'status should list every touched area uncapped, got: ' + status.textContent);
      passed++;
      console.log('  ✅ touchedAreas renders as an uncapped, comma-joined list in the status line');
    } catch (e) { failed++; console.log('  ❌ touchedAreas renders as an uncapped, comma-joined list in the status line: ' + e.message); }
  })();

  await (async () => {
    try {
      // No touchedAreas field at all (no areas configured server-side, or
      // none resolved) -- must not add a stray "touched" fragment or throw.
      const ctx = makeSandbox(() => Promise.resolve({
        hash: 'deadbeef',
        branches: [{ hops: 0, points: [], observer: { name: 'Obs', lat: 56.0, lon: 10.0 } }],
      }));
      ctx.L = {
        map: () => ({ setView() { return this; }, fitBounds() {}, invalidateSize() {}, remove() {} }),
        tileLayer: () => ({ addTo() { return this; } }),
        circleMarker: () => ({ addTo() { return this; }, bindTooltip() { return this; }, on() { return this; } }),
        polyline: () => ({ addTo() { return this; } }),
      };

      await ctx.window.PacketPathMap.open('deadbeef');
      const status = ctx.document.getElementById('packetPathStatus');
      assert.ok(!status.textContent.includes('touched'), 'status should have no "touched" fragment when touchedAreas is absent, got: ' + status.textContent);
      passed++;
      console.log('  ✅ omits the "touched" fragment when touchedAreas is absent');
    } catch (e) { failed++; console.log('  ❌ omits the "touched" fragment when touchedAreas is absent: ' + e.message); }
  })();

  console.log('\n════════════════════════════════════════');
  console.log(`  packet-path-map.js: ${passed} passed, ${failed} failed`);
  console.log('════════════════════════════════════════');
  if (failed > 0) process.exit(1);
})();
