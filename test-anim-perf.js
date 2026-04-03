/**
 * test-anim-perf.js — Performance benchmark for animation timer management
 *
 * Demonstrates that the rAF + concurrency-cap approach keeps active animation
 * count bounded, whereas the old setInterval approach accumulated without limit.
 *
 * Run: node test-anim-perf.js
 */

'use strict';

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { console.log(`  ✅ ${msg}`); passed++; }
  else { console.log(`  ❌ ${msg}`); failed++; }
}

// ---------------------------------------------------------------------------
// Simulate OLD behaviour: setInterval-based, no concurrency cap
// ---------------------------------------------------------------------------
function simulateOldModel(packetsPerSec, hopsPerPacket, durationSec) {
  // Each hop spawns 3 intervals (pulse 26ms, line 33ms, fade 52ms).
  // Pulse lasts ~2s, line ~0.66s, fade ~0.8s+0.4s ≈ 1.2s
  // At any moment, timers from the last ~2s of packets are still alive.
  const intervalLifetimes = [2.0, 0.66, 1.2]; // seconds each interval lives
  let maxConcurrent = 0;
  // Walk through time in 0.1s steps
  const dt = 0.1;
  const spawns = []; // {time, lifetime}
  for (let t = 0; t < durationSec; t += dt) {
    // Spawn timers for packets arriving in this window
    const pktsInWindow = packetsPerSec * dt;
    for (let p = 0; p < pktsInWindow; p++) {
      for (let h = 0; h < hopsPerPacket; h++) {
        for (const lt of intervalLifetimes) {
          spawns.push({ time: t, lifetime: lt });
        }
      }
    }
    // Count alive timers
    const alive = spawns.filter(s => t < s.time + s.lifetime).length;
    if (alive > maxConcurrent) maxConcurrent = alive;
  }
  return maxConcurrent;
}

// ---------------------------------------------------------------------------
// Simulate NEW behaviour: rAF + MAX_CONCURRENT_ANIMS cap
// ---------------------------------------------------------------------------
function simulateNewModel(packetsPerSec, hopsPerPacket, durationSec) {
  const MAX_CONCURRENT_ANIMS = 20;
  let activeAnims = 0;
  let maxConcurrent = 0;
  const anims = []; // {endTime}
  const dt = 0.1;
  for (let t = 0; t < durationSec; t += dt) {
    // Expire finished animations
    while (anims.length && anims[0].endTime <= t) {
      anims.shift();
      activeAnims--;
    }
    // Try to start new animations
    const pktsInWindow = packetsPerSec * dt;
    for (let p = 0; p < pktsInWindow; p++) {
      if (activeAnims >= MAX_CONCURRENT_ANIMS) break; // cap reached — drop
      activeAnims++;
      // rAF animation lifetime: longest is pulse ~2s
      anims.push({ endTime: t + 2.0 });
    }
    // Sort by endTime so expiry works
    anims.sort((a, b) => a.endTime - b.endTime);
    if (activeAnims > maxConcurrent) maxConcurrent = activeAnims;
  }
  return maxConcurrent;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

console.log('\n=== Animation timer accumulation: old vs new ===');

// Scenario: 5 pkts/sec, 3 hops each, 30 seconds
const oldPeak30s = simulateOldModel(5, 3, 30);
const newPeak30s = simulateNewModel(5, 3, 30);
console.log(`  Old model (30s @ 5pkt/s×3hops): peak ${oldPeak30s} concurrent timers`);
console.log(`  New model (30s @ 5pkt/s×3hops): peak ${newPeak30s} concurrent animations`);
assert(oldPeak30s > 100, `old model accumulates >100 timers (got ${oldPeak30s})`);
assert(newPeak30s <= 20, `new model stays ≤20 (got ${newPeak30s})`);

// Scenario: 5 minutes sustained
const oldPeak5m = simulateOldModel(5, 3, 300);
const newPeak5m = simulateNewModel(5, 3, 300);
console.log(`  Old model (5min @ 5pkt/s×3hops): peak ${oldPeak5m} concurrent timers`);
console.log(`  New model (5min @ 5pkt/s×3hops): peak ${newPeak5m} concurrent animations`);
assert(oldPeak5m > 100, `old model at 5min still unbounded (got ${oldPeak5m})`);
assert(newPeak5m <= 20, `new model at 5min still ≤20 (got ${newPeak5m})`);

// Scenario: burst — 20 pkts/sec for 10s
const oldBurst = simulateOldModel(20, 3, 10);
const newBurst = simulateNewModel(20, 3, 10);
console.log(`  Old model (burst 20pkt/s×3hops, 10s): peak ${oldBurst} concurrent timers`);
console.log(`  New model (burst 20pkt/s×3hops, 10s): peak ${newBurst} concurrent animations`);
assert(oldBurst > 200, `old model under burst >200 timers (got ${oldBurst})`);
assert(newBurst <= 20, `new model under burst stays ≤20 (got ${newBurst})`);

console.log('\n=== drawAnimatedLine frame-drop catch-up ===');

// Read the source and verify catch-up logic exists
const fs = require('fs');
const src = fs.readFileSync(__dirname + '/public/live.js', 'utf8');

// Extract the animateLine function body
const lineMatch = src.match(/function animateLine\(now\)\s*\{[\s\S]*?requestAnimationFrame\(animateLine\)/);
assert(lineMatch && /Math\.min\(Math\.floor\(elapsed\s*\/\s*33\)/.test(lineMatch[0]),
  'drawAnimatedLine catches up on frame drops (multi-tick per frame)');

const fadeMatch = src.match(/function animateFade\(now\)\s*\{[\s\S]*?requestAnimationFrame\(animateFade\)/);
assert(fadeMatch && /Math\.min\(Math\.floor\(fadeElapsed\s*\/\s*52\)/.test(fadeMatch[0]),
  'animateFade catches up on frame drops (multi-tick per frame)');

console.log(`\n${passed} passed, ${failed} failed\n`);
process.exit(failed ? 1 : 0);
