'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
// Unit test for map-estimated.js: estimated positions must never read as a
// node's own position. Loads the browser IIFE in a vm sandbox and drives
// addTo with a recording Leaflet stub.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const code = fs.readFileSync(path.join(REPO_ROOT, 'public', 'map-estimated.js'), 'utf8');
// The module uses app.js's escapeHtml and the helpers in reported-nodes.js:
// load the real ones.
const escSrc = fs.readFileSync(path.join(REPO_ROOT, 'public', 'app.js'), 'utf8').match(/function escapeHtml\(s\) \{[\s\S]*?\n\}/);
assert.ok(escSrc, 'escapeHtml not found in app.js');
const sandbox = { window: {}, escapeHtml: vm.runInNewContext('(' + escSrc[0].replace(/^function escapeHtml/, 'function') + ')') };
vm.createContext(sandbox);
vm.runInContext(fs.readFileSync(path.join(REPO_ROOT, 'public', 'reported-nodes.js'), 'utf8'), sandbox);
vm.runInContext(code, sandbox);
const M = sandbox.window.MapEstimated;
const R = sandbox.window.ReportedNodes;

const placed = {
  pubkey: 'c0cdee59e0db981b00000000000000000000000000000000000000000000000a',
  name: 'Self <Named>', name_reported_at: '2026-10-16T10:00:00.000Z', reporters: 1,
  receptions: 15, spots: 4, first_heard: '2026-09-18T06:36:06Z', last_heard: '2026-10-16T06:50:48Z',
  estimate: { lat: 50.99712, lon: 5.37811, radius_m: 2480 },
};
const unplaced = Object.assign({}, placed, { pubkey: 'b1b1' + '0'.repeat(60), estimate: undefined, spots: 1 });
const method = { spot_m: 100, min_spots: 3, min_spread_m: 200, min_radius_m: 500 };

// Only nodes with an estimate are drawn.
assert.deepStrictEqual(M.withEstimate([placed, unplaced, null]).map(n => n.pubkey), [placed.pubkey]);
assert.strictEqual(M.withEstimate([{ estimate: { lat: NaN, lon: 5 } }]).length, 0, 'a NaN estimate is not drawn');

assert.strictEqual(R.formatRadius(500), '500 m');
assert.strictEqual(R.formatRadius(2480), '2.5 km');

// The popup says what this is, escapes the name, and links to the coverage map there.
const html = M.popupHtml(placed, method);
assert.ok(html.includes('Estimated position'), 'badge');
assert.ok(html.includes('No advert position'), 'states there is no advert position');
assert.ok(html.includes('2.5 km'), 'uncertainty radius');
assert.ok(html.includes('15 times at 4 spots (estimate needs 3)'), 'reception facts');
assert.ok(html.includes('Self &lt;Named&gt;') && !html.includes('<Named>'), 'name escaped');
assert.ok(html.includes('#/rx-coverage?days=30&lat=50.99712&lon=5.37811&zoom=14'), 'coverage link');
assert.ok(html.includes('1 companion<'), 'singular companion');

// The icon is the hollow dashed ring with the ≈ glyph, not a role marker.
assert.ok(M.iconHtml().includes('mc-est-ring') && M.iconHtml().includes('≈'));

// addTo: one uncertainty circle (non-interactive, dashed class) and one marker per estimate.
const added = [];
const layer = {};
const L = {
  circle(ll, opts) { const o = { kind: 'circle', ll, opts, addTo() { added.push(o); return o; } }; return o; },
  divIcon(opts) { return opts; },
  marker(ll, opts) { const o = { kind: 'marker', ll, opts, popup: null, bindPopup(h) { o.popup = h; return o; }, addTo() { added.push(o); return o; } }; return o; },
};
assert.strictEqual(M.addTo(layer, L, [placed, unplaced], method), 1);
assert.strictEqual(added.length, 2);
const [circle, marker] = added;
assert.strictEqual(circle.kind, 'circle', 'circle drawn first so the marker stays on top');
assert.strictEqual(circle.opts.radius, 2480);
assert.strictEqual(circle.opts.className, 'mc-est-area');
assert.strictEqual(circle.opts.interactive, false);
assert.strictEqual(marker.opts.icon.className, 'mc-est-marker');
assert.ok(marker.opts.title.includes('(estimated position)'), 'hover title says estimated');
assert.ok(marker.popup.includes('Estimated position'));

console.log('map-estimated OK');
