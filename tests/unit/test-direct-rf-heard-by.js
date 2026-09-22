/* The node detail "Heard By" card reports direct radio reception only.
 *
 * Before this split the card credited every observer that saw traffic the
 * node was involved in — including traffic merely relayed through it — and
 * printed an SNR/RSSI next to each. Those numbers belong to whichever node
 * last transmitted the copy the observer received. A 433 MHz repeater was
 * listed as heard by twelve 868 MHz observers this way.
 *
 * The card template itself is exercised here, not a copy of it: the block is
 * sliced out of public/nodes.js and evaluated as the template literal it is.
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

console.log('\n=== node detail: Heard By is direct-RF only ===');

const src = fs.readFileSync(REPO_ROOT + '/public/nodes.js', 'utf8');

// --- slice the full-detail card out of the renderer ---------------------------
const START = '${observers.length || relayObserverCount ? `<div class="node-full-card" id="node-observers">';
const END = '<div class="node-full-card" id="node-neighbors">';
const startIdx = src.indexOf(START);
assert.ok(startIdx >= 0, 'could not find the Heard By card in public/nodes.js');
const endIdx = src.indexOf(END, startIdx);
assert.ok(endIdx > startIdx, 'could not find the end of the Heard By card');
const block = src.slice(startIdx, endIdx).replace(/\s+$/, '');

// The block is one `${cond ? `...` : ''}` substitution, so wrapping it in
// backticks turns it back into the markup the page renders.
const renderCard = new Function(
  'observers', 'relayObserverCount', 'escapeHtml',
  'return `' + block + '`;'
);
const esc = s => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

const directRow = {
  observer_id: 'obs-direct', observer_name: 'NearbyObserver',
  packetCount: 42, avgSnr: 11.25, avgRssi: -63.4, can_relay: true,
};

test('heading says "direct" and counts only the direct observers', () => {
  const html = renderCard([directRow], 7, esc);
  assert.ok(/Heard By &mdash; direct \(1 observer\)/.test(html),
    'expected a singular direct heading, got: ' + html.slice(0, 300));
  assert.ok(!/Heard By \(/.test(html), 'the old undifferentiated heading is still rendered');
});

test('relayed observers are reported as a count, without signal numbers', () => {
  const html = renderCard([directRow], 12, esc);
  assert.ok(/Seen via relay by 12 observers/.test(html),
    'expected the relay count line, got: ' + html);
  // The relay line must not carry an SNR/RSSI: no signal was measured for
  // this node on those receptions.
  const relayLine = html.slice(html.indexOf('Seen via relay'));
  assert.ok(!/dBm?/.test(relayLine), 'relay line must not print signal values');
});

test('a node nobody hears directly still renders the card, with an empty state', () => {
  const html = renderCard([], 35, esc);
  assert.ok(/Heard By &mdash; direct \(0 observers\)/.test(html), 'expected a zero direct heading');
  assert.ok(/No observation proves a direct reception here/.test(html),
    'expected the empty state line');
  // The empty state must not assert that nobody is in range. It cannot know
  // that: direct-routed traffic carries no sender (firmware Mesh.cpp calls
  // removeSelfFromPath before retransmitting) and ambiguous relay hops are
  // left unattributed, so a node can be heard by several observers and still
  // have no attributable direct reception. Measured on a production instance
  // when this card shipped: 16 of 40 sampled repeaters showed the empty state
  // while the same card counted relay observers two lines below.
  assert.ok(!/No observer is within radio range/i.test(html),
    'the empty state must not assert that nobody is in range');
  assert.ok(/not the same as being out of range/i.test(html),
    'the empty state must say what it cannot conclude, not only what it found');
  assert.ok(/Seen via relay by 35 observers/.test(html), 'expected the relay count');
  assert.ok(!/observer-sort-table/.test(html),
    'an empty direct list must not render a table header with no rows');
});

test('a node with neither direct nor relayed observers renders nothing', () => {
  assert.strictEqual(renderCard([], 0, esc).trim(), '');
});

test('singular and plural agree for one relayed observer', () => {
  const html = renderCard([], 1, esc);
  assert.ok(/Seen via relay by 1 observer\./.test(html), 'expected singular, got: ' + html);
});

test('direct rows keep their signal columns', () => {
  const html = renderCard([directRow], 0, esc);
  assert.ok(/11\.3 dB/.test(html), 'expected the rounded avg SNR');
  assert.ok(/-63 dBm/.test(html), 'expected the rounded avg RSSI');
  assert.ok(/NearbyObserver/.test(html), 'expected the observer name');
});

test('the listener/repeater badge still renders on direct rows', () => {
  const listener = Object.assign({}, directRow, { can_relay: false });
  assert.ok(/badge-listener/.test(renderCard([listener], 0, esc)), 'expected the listener badge');
  assert.ok(/badge-repeater/.test(renderCard([directRow], 0, esc)), 'expected the repeater badge');
  const unknown = Object.assign({}, directRow, { can_relay: null });
  const html = renderCard([unknown], 0, esc);
  assert.ok(!/badge-listener|badge-repeater/.test(html),
    'an observer with no repeat field must get no badge');
});

// --- the side pane must not drift from the full page -------------------------
test('the side pane reads relayObserverCount too', () => {
  const occurrences = (src.match(/const relayObserverCount = Number\(h\.relayObserverCount\) \|\| 0;/g) || []).length;
  assert.strictEqual(occurrences, 2,
    'both the full detail page and the side pane must read relayObserverCount');
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
