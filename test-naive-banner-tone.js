/* Test: naive-clock banner should be a small neutral notice, not a scary alert card */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// Load observer-detail.js in a VM sandbox
const src = fs.readFileSync(__dirname + '/public/observer-detail.js', 'utf8');
const window = {};
const ctx = vm.createContext({ window, document: { getElementById: () => null, querySelector: () => null, querySelectorAll: () => [], createElement: () => ({ style: {}, classList: { add(){}, remove(){} }, appendChild(){}, setAttribute(){} }), createDocumentFragment: () => ({ appendChild(){} }) }, console, setTimeout, setInterval, clearInterval, fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) }), location: { hash: '' }, history: { replaceState() {} }, Chart: function() { return { destroy(){}, update(){} }; }, registerPage() {}, L: { map(){ return { setView(){ return this; }, on(){ return this; }, remove(){} }; }, tileLayer(){ return { addTo(){} }; } } });
vm.runInContext(src, ctx);

const render = ctx.window.ObserverDetailNaiveBanner.render;

const obs = { clock_naive: true, clock_skew_seconds: 3700, clock_skew_count_24h: 5, clock_last_naive_at: '2025-01-01T00:00:00Z' };
const html = render(obs);

console.log('── naive-banner tone tests ──');

test('no warning emoji', () => {
  assert.ok(!html.includes('\u26A0'), 'should not contain ⚠️');
});

test('no role=alert', () => {
  assert.ok(!html.includes('role="alert"'), 'should use role="note" not "alert"');
});

test('no shame word: muddies', () => {
  assert.ok(!html.toLowerCase().includes('muddies'), 'should not say "muddies"');
});

test('no shame word: clamped in visible copy', () => {
  // "clamped" should not appear in visible text (ok in comments)
  assert.ok(!html.toLowerCase().includes('clamped'), 'should not say "clamped"');
});

test('no yellow background', () => {
  assert.ok(!html.includes('255,193,7'), 'should not have yellow bg');
  assert.ok(!html.includes('#ffc107'), 'should not have yellow border');
});

test('has role=note', () => {
  assert.ok(html.includes('role="note"'), 'should have role="note"');
});

test('has details expander', () => {
  assert.ok(html.includes('<details'), 'should have a <details> expander');
});

test('single compact container (no big heading)', () => {
  assert.ok(!html.includes('font-weight:600'), 'no bold heading');
});

if (failed > 0) { console.log(`\n${failed} FAILED`); process.exit(1); }
console.log(`\nAll ${passed} passed`);
