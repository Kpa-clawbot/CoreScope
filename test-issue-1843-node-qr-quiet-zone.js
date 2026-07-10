/**
 * Regression test for issue #1843 — node-details QR codes do not scan.
 *
 * Root cause: `public/nodes.js` calls `qr.createSvgTag(3, 0)` at the two
 * node-details render sites. margin=0 removes the QR quiet zone (spec
 * requires >= 4 modules of light border) so every scanner rejects the code.
 *
 * This test asserts (a) both nodes.js call sites use margin >= 4 and
 * (b) the produced SVG source keeps all dark modules at least
 * `margin * cellSize` pixels away from the SVG edge.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const vm = require('vm');

const CELL_SIZE = 3;
const MIN_MARGIN = 4; // QR spec minimum quiet zone in modules

let passed = 0;
let failed = 0;
function ok(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

// --- (a) Source-grep: both createSvgTag call sites in nodes.js use margin >= 4 ---
const nodesSrc = fs.readFileSync(path.join(__dirname, 'public/nodes.js'), 'utf8');
const callRe = /createSvgTag\(\s*(\d+)\s*,\s*(\d+)\s*\)/g;
const calls = [];
let m;
while ((m = callRe.exec(nodesSrc)) !== null) {
  calls.push({ cellSize: Number(m[1]), margin: Number(m[2]), index: m.index });
}
ok(calls.length === 2, `nodes.js has exactly 2 createSvgTag(cell,margin) call sites (found ${calls.length})`);
calls.forEach((c, i) => {
  ok(c.margin >= MIN_MARGIN,
    `nodes.js call site #${i + 1} at offset ${c.index}: margin=${c.margin} >= ${MIN_MARGIN} (QR quiet-zone spec)`);
});

// --- (b) Render an SVG with the qrcode.js vendor lib as nodes.js does, ---
// --- and grep the path `M` coordinates. Every dark module must sit at   ---
// --- margin*cellSize or further from the SVG edge.                      ---
const sandbox = { window: {}, self: {}, console };
sandbox.window = sandbox;
sandbox.self = sandbox;
vm.createContext(sandbox);
vm.runInContext(
  fs.readFileSync(path.join(__dirname, 'public/vendor/qrcode.js'), 'utf8'),
  sandbox,
);
const qrcode = sandbox.window.qrcode || sandbox.qrcode;
ok(typeof qrcode === 'function', 'vendor qrcode.js exposes qrcode()');

const usedMargin = calls[0] ? calls[0].margin : 0;
const qr = qrcode(0, 'M');
qr.addData('meshcore://contact/add?name=Test&public_key=' + 'ab'.repeat(32) + '&type=2');
qr.make();
const svg = qr.createSvgTag(CELL_SIZE, usedMargin);

// viewBox size must equal modules*cellSize + 2*margin*cellSize
const modules = qr.getModuleCount();
const expectedSize = modules * CELL_SIZE + 2 * usedMargin * CELL_SIZE;
const vbMatch = svg.match(/viewBox="0 0 (\d+) (\d+)"/);
ok(vbMatch && Number(vbMatch[1]) === expectedSize && Number(vbMatch[2]) === expectedSize,
  `SVG viewBox is ${expectedSize}x${expectedSize} (modules=${modules}, cell=${CELL_SIZE}, margin=${usedMargin})`);

const minQuiet = MIN_MARGIN * CELL_SIZE;
const pathMatch = svg.match(/<path d="([^"]+)"/);
ok(!!pathMatch, 'SVG contains a <path d="..."> with dark-module rects');
const d = pathMatch ? pathMatch[1] : '';
const coordRe = /M(\d+),(\d+)/g;
let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
let coord;
while ((coord = coordRe.exec(d)) !== null) {
  const x = Number(coord[1]);
  const y = Number(coord[2]);
  if (x < minX) minX = x;
  if (y < minY) minY = y;
  if (x + CELL_SIZE > maxX) maxX = x + CELL_SIZE;
  if (y + CELL_SIZE > maxY) maxY = y + CELL_SIZE;
}
ok(minX >= minQuiet && minY >= minQuiet,
  `left/top quiet zone: min dark-module coord (${minX},${minY}) >= ${minQuiet}px`);
ok(expectedSize - maxX >= minQuiet && expectedSize - maxY >= minQuiet,
  `right/bottom quiet zone: (${expectedSize - maxX},${expectedSize - maxY})px free border >= ${minQuiet}px`);

console.log('');
console.log(`  ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
