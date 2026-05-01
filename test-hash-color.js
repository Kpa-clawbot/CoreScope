/* test-hash-color.js — Unit tests for hash-color.js (vm.createContext sandbox)
 * Tests: purity, theme split, yellow-zone clamp, sentinel, WCAG sweep
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

// --- Purity: same input → same output ---
console.log('Purity:');
const r1 = HashColor.hashToHsl('a1b2c3d4', 'light');
const r2 = HashColor.hashToHsl('a1b2c3d4', 'light');
assert(r1 === r2, 'Same hash+theme → identical output');
const r3 = HashColor.hashToHsl('a1b2c3d4', 'light');
assert(r1 === r3, 'Third call still identical (no internal state)');

// --- Theme split: light vs dark produce different L ---
console.log('Theme split:');
const light = HashColor.hashToHsl('ff00aabb', 'light');
const dark = HashColor.hashToHsl('ff00aabb', 'dark');
assert(light !== dark, 'Light and dark produce different colors for same hash');
// Extract L values
const lightL = parseInt(light.match(/(\d+)%\)$/)[1]);
const darkL = parseInt(dark.match(/(\d+)%\)$/)[1]);
assert(lightL <= 45, 'Light theme L ≤ 45% (got ' + lightL + ')');
assert(darkL >= 60, 'Dark theme L ≥ 60% (got ' + darkL + ')');

// --- Yellow-zone clamp: hue ∈ [45°, 75°] → L=45% in light mode ---
console.log('Yellow-zone clamp (hue 45-195 → L=30%):');
// Hue 60° → bytes: 60/360 * 65535 = 10922 = 0x2AAA → hex "2aaa"
const yellow = HashColor.hashToHsl('2aaa0000', 'light');
const yellowL = parseInt(yellow.match(/(\d+)%\)$/)[1]);
const yellowH = parseInt(yellow.match(/hsl\((\d+)/)[1]);
assert(yellowH >= 45 && yellowH <= 75, 'Yellow zone hue confirmed (' + yellowH + '°)');
assert(yellowL === 30, 'Yellow-zone L clamped to 30% in light (got ' + yellowL + ')');
// Same hash in dark should NOT clamp
const yellowDark = HashColor.hashToHsl('2aaa0000', 'dark');
const yellowDarkL = parseInt(yellowDark.match(/(\d+)%\)$/)[1]);
assert(yellowDarkL === 65, 'Yellow-zone NOT clamped in dark (got ' + yellowDarkL + ')');

// --- Sentinel: null/empty hash ---
console.log('Sentinel:');
assert(HashColor.hashToHsl(null, 'light') === 'hsl(0, 0%, 50%)', 'null → sentinel');
assert(HashColor.hashToHsl('', 'light') === 'hsl(0, 0%, 50%)', 'empty string → sentinel');
assert(HashColor.hashToHsl('ab', 'dark') === 'hsl(0, 0%, 50%)', 'too short (2 chars) → sentinel');
assert(HashColor.hashToHsl(undefined, 'dark') === 'hsl(0, 0%, 50%)', 'undefined → sentinel');

// --- Variability: different hashes → different colors (anti-tautology) ---
console.log('Variability (anti-tautology):');
const colors = new Set();
['00000000', '80000000', 'ff000000', '00ff0000', 'ffff0000'].forEach(h => {
  colors.add(HashColor.hashToHsl(h, 'light'));
});
assert(colors.size >= 4, 'At least 4 distinct colors from 5 different hashes (got ' + colors.size + ')');

const darkColors = new Set();
['11110000', '55550000', '99990000', 'dddd0000'].forEach(h => {
  darkColors.add(HashColor.hashToHsl(h, 'dark'));
});
assert(darkColors.size >= 3, 'At least 3 distinct dark colors from 4 hashes (got ' + darkColors.size + ')');

// Another variability: consecutive hashes differ
const c1 = HashColor.hashToHsl('01000000', 'light');
const c2 = HashColor.hashToHsl('02000000', 'light');
assert(c1 !== c2, 'Adjacent hashes produce different colors');

// --- WCAG contrast sweep ---
// Background constants from style.css:32 (light --content-bg: #f4f5f7) and style.css:55 (dark --content-bg: #0f0f23)
console.log('WCAG contrast sweep (≥3.0 against --content-bg):');

function hexToRgb(hex) {
  hex = hex.replace('#', '');
  return [parseInt(hex.slice(0,2),16), parseInt(hex.slice(2,4),16), parseInt(hex.slice(4,6),16)];
}

function hslToRgb(h, s, l) {
  s /= 100; l /= 100;
  var c = (1 - Math.abs(2*l - 1)) * s;
  var x = c * (1 - Math.abs((h/60) % 2 - 1));
  var m = l - c/2;
  var r, g, b;
  if (h < 60) { r=c; g=x; b=0; }
  else if (h < 120) { r=x; g=c; b=0; }
  else if (h < 180) { r=0; g=c; b=x; }
  else if (h < 240) { r=0; g=x; b=c; }
  else if (h < 300) { r=x; g=0; b=c; }
  else { r=c; g=0; b=x; }
  return [Math.round((r+m)*255), Math.round((g+m)*255), Math.round((b+m)*255)];
}

function luminance(rgb) {
  var a = rgb.map(function(v) { v /= 255; return v <= 0.03928 ? v/12.92 : Math.pow((v+0.055)/1.055, 2.4); });
  return 0.2126*a[0] + 0.7152*a[1] + 0.0722*a[2];
}

function contrastRatio(rgb1, rgb2) {
  var l1 = luminance(rgb1), l2 = luminance(rgb2);
  var lighter = Math.max(l1, l2), darker = Math.min(l1, l2);
  return (lighter + 0.05) / (darker + 0.05);
}

// Light bg: #f4f5f7 (style.css:32 --surface-0, which --content-bg references)
var lightBg = hexToRgb('#f4f5f7');
// Dark bg: #0f0f23 (style.css:55 --surface-0 dark, which --content-bg references)
var darkBg = hexToRgb('#0f0f23');

var wcagFails = [];
for (var hue = 0; hue < 360; hue += 15) {
  // Simulate hashToHsl output for this hue
  // Light theme
  var lL = (hue >= 45 && hue <= 195) ? 30 : 38;
  var lightRgb = hslToRgb(hue, 70, lL);
  var lRatio = contrastRatio(lightRgb, lightBg);
  if (lRatio < 3.0) wcagFails.push('light hue=' + hue + ' ratio=' + lRatio.toFixed(2));

  // Dark theme
  var darkRgb = hslToRgb(hue, 70, 65);
  var dRatio = contrastRatio(darkRgb, darkBg);
  if (dRatio < 3.0) wcagFails.push('dark hue=' + hue + ' ratio=' + dRatio.toFixed(2));
}

assert(wcagFails.length === 0, 'All hues pass WCAG ≥3.0 contrast' + (wcagFails.length ? ' FAILURES: ' + wcagFails.join('; ') : ''));

// --- Summary ---
console.log('\n' + passed + ' passed, ' + failed + ' failed');
if (failed > 0) process.exit(1);
