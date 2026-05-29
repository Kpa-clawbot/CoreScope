/* Unit tests for live.js animation system — verifies rAF migration and concurrency cap */
'use strict';
const fs = require('fs');
const assert = require('assert');

const src = fs.readFileSync('public/live.js', 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

console.log('\n=== Canvas Animation Engine Architecture ===');

test('Canvas array, flag, and loop are implemented', () => {
  assert.ok(src.includes('activeAnimations'), 'Canvas engine missing: activeAnimations array not found');
  assert.ok(src.includes('preferCanvas: true'), 'Performance regression: preferCanvas flag missing from map init');
  assert.ok(src.includes("map.createPane('animationsPane')"), 'Z-index regression: animationsPane not created');
  assert.ok(src.includes('requestAnimationFrame(renderAnimations)'), 'Animation loop missing: renderAnimations not scheduled');
});

test('Leaflet integration and resizing handles DPR correctly', () => {
  assert.ok(src.includes('map.latLngToLayerPoint'), 'Map de-sync regression: Canvas not using LayerPoints for CSS transform sync');
  assert.ok(src.includes('animCtx.setTransform(dpr, 0, 0, dpr, 0, 0)'), 'HiDPI regression: resizeAnimCanvas does not reset transform matrix');
  assert.ok(src.includes('matchMedia'), 'Monitor drag regression: DPR listener missing');
});

console.log('\n=== Battery / GPU Optimization (State Machine) ===');

test('Engine gracefully sleeps and wakes', () => {
  assert.ok(src.includes('isAnimating = false'), 'Battery drain regression: Engine does not explicitly sleep on empty queue');
  assert.ok(src.includes('window._wakeCanvasEngine'), 'Wake controller missing: _wakeCanvasEngine not exposed');
  assert.ok(src.includes('anim.lastTick = null'), 'Time-jump regression: Engine does not nullify tick on pause');
});

console.log('\n=== Concurrency cap ===');

test('MAX_CONCURRENT_ANIMS is defined', () => {
  assert.ok(src.includes('MAX_CONCURRENT_ANIMS'), 'MAX_CONCURRENT_ANIMS constant not found');
});

test('MAX_CONCURRENT_ANIMS is set to 20', () => {
  const match = src.match(/MAX_CONCURRENT_ANIMS\s*=\s*(\d+)/);
  assert.ok(match, 'Could not parse MAX_CONCURRENT_ANIMS value');
  assert.strictEqual(parseInt(match[1]), 20);
});

test('animatePath checks MAX_CONCURRENT_ANIMS before proceeding', () => {
  const animStart = src.indexOf('function animatePath(');
  // Check that within the first 200 chars of the function, we check the cap
  const snippet = src.substring(animStart, animStart + 300);
  assert.ok(snippet.includes('activeAnims >= MAX_CONCURRENT_ANIMS'), 'animatePath should check activeAnims against cap');
});

console.log('\n=== Memory Leaks & Teardown ===');

test('Destroy function cleanly halts canvas engine', () => {
  const destroyStart = src.indexOf('function destroy()');
  const destroyBody = src.substring(destroyStart, destroyStart + 800);
  assert.ok(destroyBody.includes('activeAnimations.length = 0'), 'Memory leak: destroy() does not clear activeAnimations array');
  assert.ok(destroyBody.includes('isAnimating = false'), 'Memory leak: destroy() does not halt isAnimating flag');
});

test('Entry points guard against destroyed map', () => {
  const lineStart = src.indexOf('function drawAnimatedLine(');
  const lineBody = src.substring(lineStart, lineStart + 150);
  assert.ok(lineBody.includes('!map || !animCtx'), 'Race condition: drawAnimatedLine missing map/ctx null guard');
});

console.log('\n=== Safety: no stale setInterval in animation functions ===');

test('no setInterval remains in animation hot path', () => {
  // The only acceptable setIntervals are the UI ones (timeline, clock, prune, rate counter)
  // Count total setInterval occurrences
  const matches = src.match(/setInterval\(/g) || [];
  // Count known OK ones: _timelineRefreshInterval, _lcdClockInterval, _pruneInterval, _rateCounterInterval
  const okPatterns = ['_timelineRefreshInterval', '_lcdClockInterval', '_pruneInterval', '_rateCounterInterval'];
  let okCount = 0;
  for (const p of okPatterns) {
    if (src.includes(p + ' = setInterval') || src.includes(p + '= setInterval')) okCount++;
  }
  // Allow some non-animation setIntervals (the 4 UI ones above)
  assert.ok(matches.length <= okCount + 1, 
    `Found ${matches.length} setInterval calls, expected at most ${okCount + 1} (non-animation). Some animation setIntervals may remain.`);
});

console.log(`\n${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exit(1);

/* === Null-guard coverage for rAF callbacks === */
const src2 = fs.readFileSync('public/live.js', 'utf8');
let p2 = 0, f2 = 0;
function test2(name, fn) {
  try { fn(); p2++; console.log(`  ✅ ${name}`); }
  catch (e) { f2++; console.log(`  ❌ ${name}: ${e.message}`); }
}

console.log('\n=== Null guards on rAF animation callbacks ===');

test2('animatePath tick() has null guard', () => {
  // tick is inside animatePath, after "function tick(now)"
  const tickStart = src2.indexOf('function tick(now)');
  const tickBody = src2.substring(tickStart, tickStart + 200);
  assert.ok(tickBody.includes('!animLayer || !pathsLayer'), 'tick() missing animLayer/pathsLayer null guard');
});

test2('animatePath fadeOut() has null guard', () => {
  const fadeOutStart = src2.indexOf('function fadeOut(now)');
  const fadeOutBody = src2.substring(fadeOutStart, fadeOutStart + 200);
  assert.ok(fadeOutBody.includes('!animLayer || !pathsLayer'), 'fadeOut() missing animLayer/pathsLayer null guard');
});

test2('drawAnimatedLine animateLine() has null guard', () => {
  const lineStart = src2.indexOf('function animateLine(now)');
  const lineBody = src2.substring(lineStart, lineStart + 200);
  assert.ok(lineBody.includes('!animLayer || !pathsLayer'), 'animateLine() missing animLayer/pathsLayer null guard');
});

test2('drawAnimatedLine animateFade() has null guard', () => {
  const fadeStart = src2.indexOf('function animateFade(now)');
  const fadeBody = src2.substring(fadeStart, fadeStart + 200);
  assert.ok(fadeBody.includes('!pathsLayer'), 'animateFade() missing pathsLayer null guard');
});

test2('pulseNode animatePulse() has null guard', () => {
  const pulseStart = src2.indexOf('function animatePulse(now)');
  const pulseBody = src2.substring(pulseStart, pulseStart + 200);
  assert.ok(pulseBody.includes('!animLayer'), 'animatePulse() missing animLayer null guard');
});

test2('ghostPulse has null guard', () => {
  const ghostStart = src2.indexOf('function ghostPulse(now)');
  const ghostBody = src2.substring(ghostStart, ghostStart + 200);
  assert.ok(ghostBody.includes('!animLayer'), 'ghostPulse() missing animLayer null guard');
});

console.log(`\n${p2} passed, ${f2} failed\n`);
if (f2 > 0) process.exit(1);
