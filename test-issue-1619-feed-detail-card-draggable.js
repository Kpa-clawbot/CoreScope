/**
 * #1619 — Live view: feed-detail-card popup (with ↻ Replay) is undraggable
 * and frequently sits behind the legend (z=1000), leaving the Replay button
 * unreachable.
 *
 * Source-invariant assertions on public/live.css and public/live.js:
 *   A. .feed-detail-card z-index is bumped to 1050 (above legend z=1000,
 *      below mobile bottom-nav z=1100).
 *   B. The card markup created in live.js includes a `panel-header` div
 *      (the drag handle expected by DragManager).
 *   C. The bootstrap exposes the DragManager instance (window._liveDragMgr
 *      or equivalent) so the popup-creation site can register the card.
 *   D. The popup-creation site calls dragMgr.register(card) — wired through
 *      the exposed instance.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const liveCss = fs.readFileSync(path.join(__dirname, 'public', 'live.css'), 'utf8');
const liveJs = fs.readFileSync(path.join(__dirname, 'public', 'live.js'), 'utf8');

console.log('\n=== #1619 A: .feed-detail-card z-index above legend ===');

// Capture the .feed-detail-card { ... } block (the FIRST/base rule, not a
// nested @media override). Match the rule selector at start-of-line.
const fdcRuleMatch = liveCss.match(/^\.feed-detail-card\s*\{([\s\S]*?)\}/m);
assert(!!fdcRuleMatch, '.feed-detail-card base rule found in live.css');
if (fdcRuleMatch) {
  const body = fdcRuleMatch[1];
  const zMatch = body.match(/z-index\s*:\s*(\d+)/);
  assert(!!zMatch, '.feed-detail-card declares a z-index');
  if (zMatch) {
    const z = parseInt(zMatch[1], 10);
    assert(z === 1050,
      '.feed-detail-card z-index === 1050 (above legend 1000, below bottom-nav 1100) — got ' + z);
  }
}

console.log('\n=== #1619 B: card markup includes .panel-header drag handle ===');

// Locate the feed-detail-card construction block and verify it contains a
// panel-header div (DragManager.register requires panel.querySelector('.panel-header')).
const cardBlockMatch = liveJs.match(
  /card\.className\s*=\s*['"]feed-detail-card['"][\s\S]{0,2000}?card\.innerHTML\s*=\s*`([\s\S]*?)`/
);
assert(!!cardBlockMatch, 'feed-detail-card construction site found in live.js');
if (cardBlockMatch) {
  const html = cardBlockMatch[1];
  assert(/class\s*=\s*["']panel-header["']/.test(html) ||
         /class\s*=\s*["'][^"']*\bpanel-header\b[^"']*["']/.test(html),
    'feed-detail-card innerHTML contains a .panel-header element (drag handle)');
}

console.log('\n=== #1619 C: DragManager instance exposed for popup-site use ===');

// The popup is created in a different scope than the bootstrap dragMgr.
// Expose it on window (or equivalent global registrar) so the popup site
// can call .register(card). Accept any of: window._liveDragMgr,
// window.liveDragMgr, or a registrar function exposed on window.
const exposeMatch = liveJs.match(
  /window\.(_liveDragMgr|liveDragMgr|liveRegisterDraggable)\s*=/
);
assert(!!exposeMatch,
  'DragManager instance / registrar exposed on window (e.g. window._liveDragMgr = dragMgr)');

console.log('\n=== #1619 D: popup-creation site registers the card with DragManager ===');

// Look for a call to register/registrar that takes `card` near the
// feed-detail-card construction block.
const popupTail = cardBlockMatch
  ? liveJs.slice(liveJs.indexOf(cardBlockMatch[0]), liveJs.indexOf(cardBlockMatch[0]) + 4000)
  : '';
const registerCall =
  /(_liveDragMgr|liveDragMgr)\s*(?:&&\s*\1\s*)?\.register\s*\(\s*card\s*\)/.test(popupTail) ||
  /liveRegisterDraggable\s*\(\s*card\s*\)/.test(popupTail);
assert(registerCall,
  'popup-creation site calls <exposed-dragMgr>.register(card) (or registrar) after appending');

console.log('\n=== Summary ===');
console.log('  Passed: ' + passed);
console.log('  Failed: ' + failed);
process.exit(failed === 0 ? 0 : 1);
