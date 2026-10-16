'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
// Unit test for reported-nodes.js: repeaters known only from their reported
// name must be findable, and every position must read as an estimate.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const code = fs.readFileSync(path.join(REPO_ROOT, 'public', 'reported-nodes.js'), 'utf8');
// The module escapes with app.js's global escapeHtml: load the real one.
const escSrc = fs.readFileSync(path.join(REPO_ROOT, 'public', 'app.js'), 'utf8').match(/function escapeHtml\(s\) \{[\s\S]*?\n\}/);
assert.ok(escSrc, 'escapeHtml not found in app.js');
let fetched = 0;
const sandbox = {
  window: {},
  escapeHtml: vm.runInNewContext('(' + escSrc[0].replace(/^function escapeHtml/, 'function') + ')'),
  fetch: () => { fetched++; return Promise.resolve({ ok: true, json: () => Promise.resolve(fixture) }); },
  Date,
};
vm.createContext(sandbox);
vm.runInContext(code, sandbox);
const R = sandbox.window.ReportedNodes;

const placed = {
  pubkey: 'c0cdee59e0db981b' + '0'.repeat(48), name: 'Gent <Zuid>', name_reported_at: '2026-10-16T10:00:00.000Z',
  reporters: 2, receptions: 15, spots: 4, first_heard: '2026-09-18T06:36:06Z', last_heard: '2026-10-16T06:50:48Z',
  estimate: { lat: 50.99712, lon: 5.37811, radius_m: 2480 },
};
const unplaced = { pubkey: 'b1b122' + '0'.repeat(58), name: 'Lonely', name_reported_at: '2026-10-16T09:00:00.000Z', reporters: 1, receptions: 2, spots: 1, first_heard: '2026-10-06T10:00:00Z', last_heard: '2026-10-06T10:01:00Z' };
const method = { spot_m: 100, min_spots: 3, min_spread_m: 200, min_radius_m: 500 };
const fixture = { nodes: [placed, unplaced], method };

// matches: name (any case) or pubkey prefix.
assert.ok(R.matches(placed, 'gent'));
assert.ok(R.matches(placed, 'c0cdee'));
assert.ok(!R.matches(placed, 'ee59'), 'pubkey matches only as a prefix');
assert.ok(R.matches(placed, ''), 'empty query matches all');

// find: full key or a prefix of 8+, unique only.
assert.strictEqual(R.find(fixture.nodes, placed.pubkey), placed);
assert.strictEqual(R.find(fixture.nodes, 'c0cdee59'), placed);
assert.strictEqual(R.find(fixture.nodes, 'c0cd'), null, 'short prefix refused');
assert.strictEqual(R.find([placed, Object.assign({}, placed, { pubkey: 'c0cdee59ff' + '0'.repeat(54) })], 'c0cdee59'), null, 'ambiguous prefix refused');

// positionText: an estimate is always marked as one; no estimate says why.
assert.strictEqual(R.positionText(placed, method), '≈ estimated, ± 2.5 km');
assert.strictEqual(R.positionText(unplaced, method), 'Unknown, heard at 1 spot (estimate needs 3)');

// sectionHtml: filtered by query, escaped, links to the node view.
const sec = R.sectionHtml(fixture.nodes, method, '');
assert.ok(sec.includes('Self-reported repeaters'));
assert.ok(sec.includes('Gent &lt;Zuid&gt;') && !sec.includes('<Zuid>'), 'name escaped');
assert.ok(sec.includes('href="#/nodes/' + placed.pubkey + '"'));
assert.ok(sec.includes('never sent an advert'));
assert.strictEqual(R.sectionHtml(fixture.nodes, method, 'nomatch'), '', 'no section when nothing matches');
assert.ok(!R.sectionHtml(fixture.nodes, method, 'lonely').includes('Gent'), 'query filters rows');

// detailHtml: badge, no-advert statement, estimate or the reason it is missing.
const d1 = R.detailHtml(placed, method);
assert.ok(d1.includes('Self-reported repeater') && d1.includes('No advert from this repeater'));
assert.ok(d1.includes('≈ 50.99712, 5.37811 (estimated, within about 2.5 km)'));
assert.ok(d1.includes('#/rx-coverage?days=30&lat=50.99712&lon=5.37811&zoom=14'));
assert.ok(d1.includes('2 companions'));
const d2 = R.detailHtml(unplaced, method);
assert.ok(d2.includes('Unknown. Heard at 1 spot; an estimate needs 3 spots at least 200 m apart.'));
assert.ok(!d2.includes('≈'), 'no estimate glyph without an estimate');

// searchItemsHtml: tagged as self-reported, limited.
const items = R.searchItemsHtml(fixture.nodes, 'e', 5);
assert.ok(items.includes('self-reported, ≈ position') && items.includes('data-href="#/nodes/' + placed.pubkey + '"'));
assert.strictEqual((R.searchItemsHtml(fixture.nodes, '', 1).match(/search-result-item/g) || []).length, 1, 'limit applies');

// load: one request per TTL window.
(async () => {
  const a = await R.load();
  const b = await R.load();
  assert.strictEqual(a.nodes.length, 2);
  assert.strictEqual(b, a);
  assert.strictEqual(fetched, 1, 'cached within the TTL');
  console.log('reported-nodes OK');
})().catch((e) => { console.error(e); process.exit(1); });
