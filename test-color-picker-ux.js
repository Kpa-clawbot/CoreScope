/**
 * Tests for channel color picker UX fixes (#681)
 *
 * Verifies:
 * 1. Live feed color dots are >= 16px (not tiny 12px)
 * 2. No contextmenu handler on live feed that hijacks right-click
 * 3. Channels page color dots with assigned color show clear affordance
 * 4. Popover positioning respects viewport bounds with margin
 */

'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');

let passed = 0;
let failed = 0;

function assert(condition, msg) {
  if (condition) {
    passed++;
    console.log(`  ✓ ${msg}`);
  } else {
    failed++;
    console.error(`  ✗ FAIL: ${msg}`);
  }
}

// --- Test 1: Live feed dot size ---
console.log('\n=== Live feed color dot size (#681) ===');

const liveSource = fs.readFileSync(path.join(__dirname, 'public/live.js'), 'utf8');

// The feed-color-dot inline style should use width >= 16px
const dotMatch = liveSource.match(/feed-color-dot.*?width:(\d+)px/);
assert(dotMatch !== null, 'feed-color-dot has width in inline style');
if (dotMatch) {
  const dotWidth = parseInt(dotMatch[1], 10);
  assert(dotWidth >= 16, `feed-color-dot width is ${dotWidth}px (should be >= 16px)`);
}

// Height should match
const dotHeightMatch = liveSource.match(/feed-color-dot.*?height:(\d+)px/);
if (dotHeightMatch) {
  const dotHeight = parseInt(dotHeightMatch[1], 10);
  assert(dotHeight >= 16, `feed-color-dot height is ${dotHeight}px (should be >= 16px)`);
}

// --- Test 2: No contextmenu hijack on live feed ---
console.log('\n=== No right-click hijack on live feed (#681) ===');

const pickerSource = fs.readFileSync(path.join(__dirname, 'public/channel-color-picker.js'), 'utf8');

// The picker should NOT install a contextmenu listener on the live feed
// Look for the installLiveFeedHandlers function and check it doesn't add contextmenu
const liveFeedHandlerMatch = pickerSource.match(/function installLiveFeedHandlers\(\)[\s\S]*?^  \}/m);
if (liveFeedHandlerMatch) {
  const handlerBody = liveFeedHandlerMatch[0];
  assert(!handlerBody.includes("'contextmenu'") && !handlerBody.includes('"contextmenu"'),
    'installLiveFeedHandlers does NOT add contextmenu listener');
} else {
  // Alternative: check the entire picker source for liveFeed + contextmenu combo
  // The feed variable + contextmenu listener pattern
  const hasLiveFeedContextMenu = /feed\.addEventListener\(['"]contextmenu['"]/.test(pickerSource);
  assert(!hasLiveFeedContextMenu, 'No contextmenu listener on liveFeed element');
}

// --- Test 3: Channels page clear affordance ---
console.log('\n=== Channels page clear affordance (#681) ===');

const channelsSource = fs.readFileSync(path.join(__dirname, 'public/channels.js'), 'utf8');

// Channels page should render a clear button/icon next to colored dots
// without requiring the picker to be opened
const hasClearAffordance = channelsSource.includes('ch-color-clear') ||
  channelsSource.includes('color-clear');
assert(hasClearAffordance, 'Channels page has inline clear affordance for colored dots');

// --- Test 4: Popover positioning margin ---
console.log('\n=== Popover positioning margin (#681) ===');

// The popover positioning should use a margin of at least 12px from edges
// (not just 8px which causes overlap with panel borders)
const posMatch = pickerSource.match(/vw - pw - (\d+)/);
assert(posMatch !== null, 'Popover has horizontal edge margin');
if (posMatch) {
  const margin = parseInt(posMatch[1], 10);
  assert(margin >= 12, `Popover edge margin is ${margin}px (should be >= 12px)`);
}

const posMatchV = pickerSource.match(/vh - ph - (\d+)/);
if (posMatchV) {
  const marginV = parseInt(posMatchV[1], 10);
  assert(marginV >= 12, `Popover vertical margin is ${marginV}px (should be >= 12px)`);
}

// --- Summary ---
console.log(`\n${passed + failed} tests: ${passed} passed, ${failed} failed`);
process.exit(failed > 0 ? 1 : 0);
