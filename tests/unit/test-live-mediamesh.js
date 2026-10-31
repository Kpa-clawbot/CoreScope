/* Unit tests for MediaMesh (radio2) paths on Live: an observation with
 * `mediamesh: true` is drawn as hollow rings; TRACE / ghost hops keep their
 * dashes and normal packets their line. */
'use strict';
const fs = require('fs');
const assert = require('assert');
const vm = require('vm');

const src = fs.readFileSync('public/live.js', 'utf8');
const css = fs.readFileSync('public/style.css', 'utf8');
const liveCss = fs.readFileSync('public/live.css', 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// Evaluate the pure helpers (constants through pathHopStyle) on their own.
const start = src.indexOf('const MEDIAMESH_RING_SPACING_PX');
const end = src.indexOf('function mediaMeshPathColor');
assert.ok(start > 0 && end > start, 'MediaMesh helper block not found in live.js');
const ctx = {};
vm.createContext(ctx);
vm.runInContext(src.slice(start, end).replace(/^\s*let mediaMesh\w+ = null;.*$/gm, '') +
  '\nthis.api = { mediaMeshRingCap, mediaMeshRingFractions, pathHopStyle, MEDIAMESH_RING_SPACING_PX };', ctx);
const { mediaMeshRingCap, mediaMeshRingFractions, pathHopStyle, MEDIAMESH_RING_SPACING_PX } = ctx.api;

console.log('\n=== Ring layout ===');

test('one ring every 12 px, evenly spread inside the hop', () => {
  assert.strictEqual(MEDIAMESH_RING_SPACING_PX, 12);
  const fr = mediaMeshRingFractions(120, 40);
  assert.strictEqual(fr.length, 10);
  assert.ok(fr[0] > 0 && fr[fr.length - 1] < 1, 'rings stay inside the hop');
  for (let i = 1; i < fr.length; i++) assert.ok(Math.abs((fr[i] - fr[i - 1]) - 0.1) < 1e-9);
});

test('a short hop still gets one ring; zero length gets none', () => {
  assert.strictEqual(mediaMeshRingFractions(5, 40).length, 1);
  assert.strictEqual(mediaMeshRingFractions(0, 40).length, 0);
  assert.strictEqual(mediaMeshRingFractions(NaN, 40).length, 0);
});

test('at most 40 rings per hop: the spacing widens instead', () => {
  assert.strictEqual(mediaMeshRingFractions(5000, 40).length, 40);
  assert.strictEqual(mediaMeshRingFractions(5000).length, 40);
});

test('a path stays within 200 rings', () => {
  for (const hops of [1, 2, 4, 5, 6, 9, 17, 64, 300]) {
    const cap = mediaMeshRingCap(hops);
    assert.ok(cap >= 1 && cap <= 40, `cap ${cap} for ${hops} hops`);
    if (hops <= 200) assert.ok(cap * hops <= 200, `${hops} hops × ${cap} > 200`);
  }
  assert.strictEqual(mediaMeshRingCap(1), 40);
  assert.strictEqual(mediaMeshRingCap(10), 20);
});

console.log('\n=== Which style a hop gets ===');

test('MediaMesh hops are rings; TRACE / ghost hops stay dashed; others a line', () => {
  assert.strictEqual(pathHopStyle(false, true), 'rings');
  assert.strictEqual(pathHopStyle(true, true), 'dashed');
  assert.strictEqual(pathHopStyle(true, false), 'dashed');
  assert.strictEqual(pathHopStyle(false, false), 'line');
});

console.log('\n=== Wiring ===');

test('replayed packets carry the flag (dbPacketToLive)', () => {
  const body = src.slice(src.indexOf('function dbPacketToLive('), src.indexOf('function dbPacketToLive(') + 1600);
  assert.ok(body.includes('mediamesh: pkt.mediamesh === true'));
});

test('each animated path carries its own flag to animatePath', () => {
  assert.ok(src.includes('mediamesh: qpkt.mediamesh === true'));
  const calls = src.match(/animatePath\([^;]*mediamesh: allPaths\[ai\]\.mediamesh/g) || [];
  assert.strictEqual(calls.length, 2, 'both animatePath calls (TRACE split and normal) pass the flag');
});

test('the canvas engine draws rings, not the core line, for MediaMesh', () => {
  const body = src.slice(src.indexOf('function renderAnimations('), src.indexOf('function renderFades('));
  assert.ok(body.includes('if (anim.isMediaMesh)'));
  assert.ok(body.includes('mediaMeshRingFractions('));
  assert.ok(body.includes('MEDIAMESH_RING_RADIUS'));
});

test('fading rings are circle markers on one shared canvas, not SVG', () => {
  assert.ok(src.includes("mediaMeshRenderer = L.canvas({ pane: 'animationsPane' })"));
  const b = src.slice(src.indexOf('function buildMediaMeshRings('), src.indexOf('function respaceMediaMeshRings('));
  assert.ok(b.includes('renderer: mediaMeshRenderer'));
  assert.ok(b.includes('fill: false'));
  assert.ok(b.includes('interactive: false'));
});

test('rings are re-spaced on zoom and the hook is removed on destroy', () => {
  assert.ok(src.includes("map.on('zoomend', respaceMediaMeshRings)"));
  const d = src.slice(src.indexOf('function destroy()'), src.indexOf('function destroy()') + 2500);
  assert.ok(d.includes("off('zoomend', respaceMediaMeshRings)"));
});

test('legend and path popup name MediaMesh', () => {
  assert.ok(src.includes('Hollow rings — MediaMesh (radio2)'));
  assert.ok(src.includes('buildClickablePathPopupHtml(typeName, color, hopNames, tsMs, hash, mediaMesh)'));
});

test('popup shows the MediaMesh line only for MediaMesh paths', () => {
  const sandbox = { window: {}, document: {}, Date };
  vm.createContext(sandbox);
  const fnSrc = src.slice(src.indexOf('function buildClickablePathPopupHtml('), src.indexOf('function pruneClickablePaths('));
  vm.runInContext('function escapeHtml(s){return String(s);}\n' + fnSrc + '\nthis.f = buildClickablePathPopupHtml;', sandbox);
  assert.ok(sandbox.f('ADVERT', 'red', ['a', 'b'], Date.now(), 'h', true).includes('MediaMesh (radio2)'));
  assert.ok(!sandbox.f('ADVERT', 'red', ['a', 'b'], Date.now(), 'h', false).includes('MediaMesh'));
});

console.log('\n=== Theme ===');

test('colours are CSS variables, light and dark, and customizable', () => {
  assert.ok((css.match(/--mediamesh-path-color:/g) || []).length >= 3, 'path colour in :root and both dark blocks');
  assert.ok((css.match(/--mediamesh-node-color:/g) || []).length >= 3, 'node colour in :root and both dark blocks');
  assert.ok(liveCss.includes('var(--mediamesh-path-color)'));
  for (const f of ['public/customize.js', 'public/customize-v2.js']) {
    const c = fs.readFileSync(f, 'utf8');
    assert.ok(c.includes("mediaMeshPath: '--mediamesh-path-color'"), f);
    assert.ok(c.includes("mediaMeshNode: '--mediamesh-node-color'"), f);
  }
  assert.ok(src.includes("getPropertyValue('--mediamesh-path-color')"));
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed) process.exit(1);
