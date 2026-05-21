/**
 * #1034 PR3: Wiring tests — verify public/channels.js calls into
 * window.ChannelQR.generate() from the Generate handler, and that the
 * Scan button is enabled + wired to ChannelQR.scan() that populates
 * the PSK fields.
 *
 * Pure source-string + targeted-snippet assertions (no browser).
 * E2E behavior is covered by test-channel-modal-e2e.js extensions.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0;
let failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const src = fs.readFileSync(
  path.join(__dirname, 'public/channels.js'),
  'utf8'
);

console.log('\n=== #1034 PR3: Generate handler renders QR via ChannelQR.generate ===');

// Locate the chGenerateBtn handler block.
var genIdx = src.indexOf("var genBtn = document.getElementById('chGenerateBtn')");
assert(genIdx > 0, 'found chGenerateBtn handler block');
var genBlock = src.substring(genIdx, genIdx + 1200);

assert(/ChannelQR\s*\.\s*generate\s*\(/.test(genBlock) ||
       /window\.ChannelQR\.generate\s*\(/.test(genBlock),
  'Generate handler calls ChannelQR.generate(...)');

// Old placeholder text must be gone (it forced "QR coming in next update").
assert(!/QR code coming in next update/.test(genBlock),
  'Generate handler no longer prints "QR coming in next update" placeholder');

// The generate call should pass the qr-output element as the render target.
assert(/ChannelQR\.generate\([^)]*qrOut|generate\([^)]*qr-output/.test(genBlock),
  'Generate handler passes #qr-output as the QR render target');

console.log('\n=== #1034 PR3: Scan button enabled + wired ===');

// Scan button must be enabled (no `disabled` attribute) — or the wiring
// must remove it at init.
var scanBtnRender = src.match(/id="scan-qr-btn"[^>]*>/);
assert(scanBtnRender, '#scan-qr-btn render present');
var hasDisabledAttr = scanBtnRender && /\bdisabled\b/.test(scanBtnRender[0]);
var removesDisabled = /scan-qr-btn[\s\S]{0,400}\.removeAttribute\(\s*['"]disabled/.test(src) ||
                      /scanBtn[\s\S]{0,200}\.disabled\s*=\s*false/.test(src);
assert(!hasDisabledAttr || removesDisabled,
  'scan button is enabled (no disabled attr OR runtime removes it)');

// Click handler wired to ChannelQR.scan
assert(/scan-qr-btn[\s\S]{0,800}addEventListener\(\s*['"]click/.test(src) ||
       /scanBtn[\s\S]{0,400}addEventListener\(\s*['"]click/.test(src),
  'scan-qr-btn has a click handler attached');

assert(/ChannelQR\s*\.\s*scan\s*\(/.test(src),
  'click handler calls ChannelQR.scan()');

console.log('\n=== #1034 PR3: Scan result populates PSK fields ===');

// The scan result is {name, secret}. Wiring must populate #chPskKey
// and #chPskName from the parsed result.
var scanWiring = src.match(/ChannelQR\.scan\([\s\S]{0,1500}/);
assert(scanWiring, 'found ChannelQR.scan(...) call site');
if (scanWiring) {
  var sw = scanWiring[0];
  assert(/chPskKey/.test(sw),
    'scan success path writes to #chPskKey');
  assert(/chPskName/.test(sw),
    'scan success path writes to #chPskName');
  assert(/\.secret\b|result\.secret|\.value\s*=\s*[^;]*secret/.test(sw),
    'scan result.secret populates the key field');
}

console.log('\n=== Results ===');
console.log('Passed: ' + passed + ', Failed: ' + failed);
process.exit(failed > 0 ? 1 : 0);
