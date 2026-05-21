/**
 * Tests for #1034 — Channel UX redesign PR1: Modal + sectioned sidebar.
 *
 * Pattern follows test-channel-psk-ux.js: string-contract assertions over
 * public/channels.js + DOM render harness via vm sandbox.
 *
 *   - [+ Add Channel] button in sidebar (replaces inline form)
 *   - Modal overlay with three labeled sections:
 *       Generate PSK Channel | Add Private Channel (PSK) | Monitor Hashtag Channel
 *   - QR placeholders (#qr-output, #scan-qr-btn[disabled])
 *   - Privacy footer text
 *   - Sectioned sidebar render: My Channels / Network / Encrypted (N)
 *   - "No key" checkbox is gone
 *   - Three modal action handlers wired
 *
 * Runs in Node.js — no browser.
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

const chSrc = fs.readFileSync(path.join(__dirname, 'public/channels.js'), 'utf8');
const cssSrc = fs.readFileSync(path.join(__dirname, 'public/style.css'), 'utf8');

console.log('\n=== #1034 PR1: [+ Add Channel] sidebar button ===');
assert(/id="chAddChannelBtn"/.test(chSrc),
  'sidebar exposes #chAddChannelBtn (replaces inline form)');
assert(/\+ Add Channel/.test(chSrc) || /Add Channel/.test(chSrc),
  '[+ Add Channel] button label present');
// Old "No key" toggle must be GONE.
assert(!/No key/.test(chSrc),
  'old "No key" checkbox removed from sidebar');
assert(!/id="chShowEncrypted"/.test(chSrc),
  'old #chShowEncrypted toggle removed');

console.log('\n=== #1034 PR1: Modal markup ===');
assert(/id="chAddChannelModal"/.test(chSrc),
  'modal element #chAddChannelModal exists');
assert(/modal-overlay|ch-modal-overlay/.test(chSrc),
  'modal uses overlay pattern (matches existing modal-overlay class)');
assert(/data-action="ch-modal-close"/.test(chSrc) || /id="chModalClose"/.test(chSrc),
  'modal has close affordance (data-action ch-modal-close or #chModalClose)');

console.log('\n=== #1034 PR1: Three sections by label ===');
assert(/Generate PSK Channel/.test(chSrc),
  'section 1 label: "Generate PSK Channel"');
assert(/Add Private Channel \(PSK\)/.test(chSrc),
  'section 2 label: "Add Private Channel (PSK)"');
assert(/Monitor Hashtag Channel/.test(chSrc),
  'section 3 label: "Monitor Hashtag Channel"');

console.log('\n=== #1034 PR1: Section 1 — Generate PSK ===');
assert(/id="chGenerateName"/.test(chSrc),
  'generate section has #chGenerateName input');
assert(/id="chGenerateBtn"/.test(chSrc),
  'generate section has #chGenerateBtn');
assert(/Generate &amp; Show QR|Generate & Show QR/.test(chSrc),
  '[Generate & Show QR] button label present');
assert(/id="qr-output"/.test(chSrc),
  '#qr-output placeholder div present (QR code render is PR #2)');

console.log('\n=== #1034 PR1: Section 2 — Add PSK ===');
assert(/id="chPskKey"/.test(chSrc),
  'PSK section has #chPskKey input (32-hex)');
assert(/id="chPskName"/.test(chSrc),
  'PSK section has optional #chPskName input');
assert(/id="chPskAddBtn"/.test(chSrc),
  'PSK section has #chPskAddBtn');
assert(/id="scan-qr-btn"/.test(chSrc),
  '#scan-qr-btn present (wired in PR3 — see test-channel-qr-wiring.js)');
assert(/\[0-9a-fA-F\]\{32\}|isHexKey/.test(chSrc),
  'PSK section validates 32-hex format');

console.log('\n=== #1034 PR1: Section 3 — Monitor Hashtag ===');
assert(/id="chHashtagName"/.test(chSrc),
  'hashtag section has #chHashtagName input');
assert(/id="chHashtagBtn"/.test(chSrc),
  'hashtag section has #chHashtagBtn');
assert(/Case-sensitive|case-sensitive/.test(chSrc),
  'hashtag section shows case-sensitivity warning');

console.log('\n=== #1034 PR1: Privacy footer ===');
assert(/Keys stay in your browser/.test(chSrc),
  'privacy footer "Keys stay in your browser" present');
assert(/passive observer/.test(chSrc),
  'privacy footer mentions "passive observer"');

console.log('\n=== #1034 PR1: Sectioned sidebar ===');
assert(/ch-section-mychannels|My Channels/.test(chSrc),
  'sidebar renders "My Channels" section');
assert(/ch-section-network|>Network</.test(chSrc),
  'sidebar renders "Network" section');
assert(/ch-section-encrypted|Encrypted \(/.test(chSrc),
  'sidebar renders "Encrypted (N)" section');
assert(/data-encrypted-collapsed|chEncryptedCollapsed|encrypted-collapsed/.test(chSrc),
  'Encrypted section is collapsible (collapsed by default)');

console.log('\n=== #1034 PR1: Modal action wiring ===');
assert(/chGenerateBtn[\s\S]{0,400}addEventListener|onGenerate|generatePsk/.test(chSrc),
  '#chGenerateBtn has a click handler wired');
assert(/chPskAddBtn[\s\S]{0,400}addEventListener|onPskAdd/.test(chSrc),
  '#chPskAddBtn has a click handler wired');
assert(/chHashtagBtn[\s\S]{0,400}addEventListener|onHashtag/.test(chSrc),
  '#chHashtagBtn has a click handler wired');
// Generate uses crypto.getRandomValues(16)
assert(/getRandomValues\(\s*new Uint8Array\(\s*16\s*\)|getRandomValues\([^)]*16/.test(chSrc),
  'generate handler uses crypto.getRandomValues(16) for the key');

console.log('\n=== #1034 PR1: CSS for modal ===');
assert(/ch-modal|ch-add-modal|chAddChannelModal/.test(cssSrc) || /\.modal-overlay/.test(cssSrc),
  'modal CSS present (ch-modal-* or reuses .modal-overlay)');

console.log('\n=== Results ===');
console.log('Passed: ' + passed + ', Failed: ' + failed);
process.exit(failed > 0 ? 1 : 0);
