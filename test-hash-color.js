/* test-hash-color.js — Unit tests for hash-color.js (vm.createContext sandbox)
 * Tests: purity, theme split, saturation variability, lightness variability,
 *        outline darker than fill, sentinel, perceptual distance
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');

const src = fs.readFileSync(path.join(__dirname, 'public', 'hash-color.js'), 'utf8');

function createSandbox() {
  const sandbox = { window: {}, module: {} };
  vm.createContext(sandbox);
  vm.runInContext(src, sandbox);
  return sandbox.window.HashColor || sandbox.module.exports;
}

const HashColor = createSandbox();
let passed = 0;
let failed = 0;

function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

function parseHsl(str) {
  const m = str.match(/hsl\((\d+),\s*(\d+)%,\s*(\d+)%\)/);
  if (!m) return null;
  return { h: parseInt(m[1]), s: parseInt(m[2]), l: parseInt(m[3]) };
}

// --- Purity: same input → same output ---
console.log('Purity:');
const r1 = HashColor.hashToHsl('a1b2c3d4', 'light');
const r2 = HashColor.hashToHsl('a1b2c3d4', 'light');
assert(r1 === r2, 'Same hash+theme → identical output');
const r3 = HashColor.hashToHsl('a1b2c3d4', 'light');
assert(r1 === r3, 'Third call still identical (no internal state)');

// --- Theme split: light vs dark produce different L ---
console.log('Theme split:');
const light = HashColor.hashToHsl('ff00aa80', 'light');
const dark = HashColor.hashToHsl('ff00aa80', 'dark');
assert(light !== dark, 'Light and dark produce different colors for same hash');
const lightP = parseHsl(light);
const darkP = parseHsl(dark);
assert(lightP.l >= 50 && lightP.l <= 65, 'Light theme L in [50,65] (got ' + lightP.l + ')');
assert(darkP.l >= 55 && darkP.l <= 72, 'Dark theme L in [55,72] (got ' + darkP.l + ')');

// --- Saturation varies with byte 2 ---
console.log('Saturation variability (byte 2):');
const lowSat = HashColor.hashToHsl('000000ff', 'light');  // byte2=0x00
const highSat = HashColor.hashToHsl('0000ffff', 'light'); // byte2=0xff
const lowSatP = parseHsl(lowSat);
const highSatP = parseHsl(highSat);
assert(lowSatP.s === 55, 'byte2=0x00 → S=55% (got ' + lowSatP.s + ')');
assert(highSatP.s === 95, 'byte2=0xff → S=95% (got ' + highSatP.s + ')');
// Mid value
const midSat = HashColor.hashToHsl('00008000', 'light'); // byte2=0x80
const midSatP = parseHsl(midSat);
assert(midSatP.s > 55 && midSatP.s < 95, 'byte2=0x80 → S between 55 and 95 (got ' + midSatP.s + ')');

// --- Lightness varies with byte 3 ---
console.log('Lightness variability (byte 3):');
const lowL = HashColor.hashToHsl('00000000', 'light');  // byte3=0x00
const highL = HashColor.hashToHsl('000000ff', 'light'); // byte3=0xff
const lowLP = parseHsl(lowL);
const highLP = parseHsl(highL);
assert(lowLP.l === 50, 'byte3=0x00 light → L=50 (got ' + lowLP.l + ')');
assert(highLP.l === 65, 'byte3=0xff light → L=65 (got ' + highLP.l + ')');
const lowLD = HashColor.hashToHsl('00000000', 'dark');
const highLD = HashColor.hashToHsl('000000ff', 'dark');
assert(parseHsl(lowLD).l === 55, 'byte3=0x00 dark → L=55 (got ' + parseHsl(lowLD).l + ')');
assert(parseHsl(highLD).l === 72, 'byte3=0xff dark → L=72 (got ' + parseHsl(highLD).l + ')');

// --- Outline is darker than fill ---
console.log('Outline darker than fill:');
['a1b2c3d4', 'ff00aa80', '12345678', 'deadbeef'].forEach(h => {
  ['light', 'dark'].forEach(theme => {
    const fill = parseHsl(HashColor.hashToHsl(h, theme));
    const outline = parseHsl(HashColor.hashToOutline(h, theme));
    assert(outline.l < fill.l, 'Outline L(' + outline.l + ') < Fill L(' + fill.l + ') for ' + h + '/' + theme);
  });
});

// --- Outline same hue as fill ---
console.log('Outline same hue as fill:');
['a1b2c3d4', 'deadbeef'].forEach(h => {
  const fill = parseHsl(HashColor.hashToHsl(h, 'light'));
  const outline = parseHsl(HashColor.hashToOutline(h, 'light'));
  assert(fill.h === outline.h, 'Hue matches: fill=' + fill.h + ' outline=' + outline.h + ' for ' + h);
});

// --- Sentinel: null/empty/short hash ---
console.log('Sentinel:');
assert(HashColor.hashToHsl(null, 'light') === 'hsl(0, 0%, 50%)', 'null → sentinel');
assert(HashColor.hashToHsl('', 'light') === 'hsl(0, 0%, 50%)', 'empty string → sentinel');
assert(HashColor.hashToHsl('ab', 'dark') === 'hsl(0, 0%, 50%)', 'too short (2 chars) → sentinel');
assert(HashColor.hashToHsl('abcdef', 'dark') === 'hsl(0, 0%, 50%)', '6 chars (need 8) → sentinel');
assert(HashColor.hashToHsl(undefined, 'dark') === 'hsl(0, 0%, 50%)', 'undefined → sentinel');
assert(HashColor.hashToOutline(null, 'light') === 'hsl(0, 0%, 30%)', 'null outline → sentinel');

// --- Variability: different hashes → different colors (anti-tautology) ---
console.log('Variability (anti-tautology):');
const colors = new Set();
['00008080', '80008080', 'ff008080', '00ff8080', 'ffff8080'].forEach(h => {
  colors.add(HashColor.hashToHsl(h, 'light'));
});
assert(colors.size >= 4, 'At least 4 distinct colors from 5 different hashes (got ' + colors.size + ')');

// Adjacent hashes differ
const c1 = HashColor.hashToHsl('01008080', 'light');
const c2 = HashColor.hashToHsl('02008080', 'light');
assert(c1 !== c2, 'Adjacent hashes produce different colors');

// --- Perceptual distance: sample 50 hashes, compute pairwise HSL distance ---
console.log('Perceptual distance (50 sample hashes):');
function hslDistance(a, b) {
  // Simple cylindrical distance: weight hue wrap, sat, lightness
  var dh = Math.min(Math.abs(a.h - b.h), 360 - Math.abs(a.h - b.h)) / 180; // 0-1
  var ds = Math.abs(a.s - b.s) / 100; // 0-1
  var dl = Math.abs(a.l - b.l) / 100; // 0-1
  return Math.sqrt(dh*dh + ds*ds + dl*dl);
}

const deterministicHashes = [];
for (var i = 0; i < 50; i++) {
  var hex = ('0000000' + (i * 5347 + 12345).toString(16)).slice(-8);
  deterministicHashes.push(hex);
}

const parsedColors = deterministicHashes.map(h => parseHsl(HashColor.hashToHsl(h, 'light')));
var distances = [];
for (var i = 0; i < parsedColors.length; i++) {
  for (var j = i + 1; j < parsedColors.length; j++) {
    distances.push(hslDistance(parsedColors[i], parsedColors[j]));
  }
}
var avgDist = distances.reduce((a, b) => a + b, 0) / distances.length;
var minDist = Math.min(...distances);
console.log('    Avg pairwise HSL distance: ' + avgDist.toFixed(4));
console.log('    Min pairwise HSL distance: ' + minDist.toFixed(4));
assert(avgDist > 0.15, 'Average pairwise distance > 0.15 (got ' + avgDist.toFixed(4) + ')');
assert(minDist > 0.01, 'Min pairwise distance > 0.01 (got ' + minDist.toFixed(4) + ')');

// --- Summary ---
console.log('\n' + passed + ' passed, ' + failed + ' failed');
if (failed > 0) process.exit(1);
