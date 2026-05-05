/**
 * Regression: channel sidebar layout for user-added (PSK) channels was
 * broken by #1024 (✕ remove + 🔑 badge) interacting with the outer
 * `.ch-item` <button> wrapper.
 *
 * Root cause: HTML5 disallows nesting <button> inside <button>. The parser
 * implicitly closes the outer `.ch-item` button as soon as it hits the
 * inner `<button class="ch-remove-btn">`. This re-parents the remove
 * button + everything after it (the `.ch-item-preview` "X: msg" line)
 * outside the channel entry, producing the visible bug:
 *
 *   [icon] Levski 🔑                  <-- outer button closes early here
 *   ✕                                 <-- orphaned, "floats"
 *   KpaPocket: Тест                   <-- preview text orphaned
 *   [icon] #bookclub ...
 *
 * This test asserts the rendered template does NOT contain a nested
 * `<button>` inside the `.ch-item` button. Plus the "No key" toggle gets
 * clearer copy and stays grouped with the channel controls.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const chSrc = fs.readFileSync(path.join(__dirname, 'public/channels.js'), 'utf8');
const cssSrc = fs.readFileSync(path.join(__dirname, 'public/style.css'), 'utf8');

console.log('\n=== Sidebar layout: no nested <button> inside .ch-item ===');

// The bug: a literal `<button class="ch-remove-btn"` inside the
// `.ch-item` template. After fix, the remove affordance must be a
// non-<button> element (e.g. <span role="button">) so HTML parsing
// keeps it inside the channel entry.
assert(!/<button[^>]*class="ch-remove-btn"/.test(chSrc),
  'remove (✕) affordance must NOT be a <button> element (would close outer .ch-item button)');

// Remove control must still be discoverable (data attribute keeps the
// existing click handler in `addEventListener('click', ...)`).
assert(/data-remove-channel="/.test(chSrc),
  'remove affordance still carries data-remove-channel for click delegation');

console.log('\n=== Sidebar layout: ✕ visible on user-added rows (not opacity:0) ===');
// Bug compounded: even if the button rendered correctly, opacity:0
// hide-until-hover made it impossible to discover on touch devices.
// The user-added (PSK) row should expose ✕ at full visibility.
const removeRule = cssSrc.match(/\.ch-remove-btn\s*\{[^}]*\}/);
assert(removeRule, 'found .ch-remove-btn CSS rule');
if (removeRule) {
  assert(!/opacity:\s*0\s*[;}]/.test(removeRule[0]),
    '.ch-remove-btn must not be opacity:0 by default (was invisible on touch)');
}

console.log('\n=== Encrypted section: header exists and is collapsible (#1037 redesign) ===');
// #1037 replaced the binary "No key" visibility toggle with a sectioned
// sidebar — encrypted (no-key) channels live in their own collapsible
// section grouped with the rest. The old toggle is intentionally gone.
assert(/ch-section-encrypted/.test(chSrc),
  'sidebar renders a dedicated Encrypted section');
assert(/id="chEncryptedToggle"/.test(chSrc),
  'Encrypted section header is a toggle (button#chEncryptedToggle)');
assert(/aria-expanded=/.test(chSrc) && /aria-controls="chEncryptedBody"/.test(chSrc),
  'toggle exposes ARIA collapsible state (aria-expanded + aria-controls)');
assert(/Encrypted \(\$\{encrypted\.length\}\)/.test(chSrc),
  'Encrypted header shows live count');

console.log('\n=== Results ===');
console.log('Passed: ' + passed + ', Failed: ' + failed);
process.exit(failed > 0 ? 1 : 0);
