/**
 * #1454 — customizer toggle for "show encrypted channels".
 *
 * The read gate at public/channels.js ~L1564 has long been:
 *   var showEnc = localStorage.getItem('channels-show-encrypted') === 'true';
 * but there has been NO UI affordance to flip it. This PR adds a checkbox
 * to the customizer Display panel that toggles the localStorage value and
 * fires a custom event the channels page listens for to re-fetch live —
 * no full page reload.
 *
 * Source-grep invariants (cheap, deterministic). Reverting the production
 * code must break these.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const customizeSrc = fs.readFileSync(
  path.join(__dirname, 'public', 'customize-v2.js'), 'utf8');
const channelsSrc = fs.readFileSync(
  path.join(__dirname, 'public', 'channels.js'), 'utf8');

console.log('\n=== #1454 A: read gate in channels.js unchanged ===');
// The localStorage read-gate stays as the source-of-truth contract.
assert(
  /localStorage\.getItem\(\s*['"]channels-show-encrypted['"]\s*\)\s*===\s*['"]true['"]/
    .test(channelsSrc),
  'channels.js still reads localStorage["channels-show-encrypted"] === "true"');
assert(
  /includeEncrypted=true/.test(channelsSrc),
  'channels.js still appends includeEncrypted=true to the channels fetch');

console.log('\n=== #1454 B: customizer registers a write path ===');
assert(
  customizeSrc.indexOf("channels-show-encrypted") !== -1,
  'customize-v2.js mentions the channels-show-encrypted localStorage key');
assert(
  /localStorage\.setItem\(\s*['"]channels-show-encrypted['"]\s*,\s*['"]true['"]\s*\)/
    .test(customizeSrc),
  'customize-v2.js sets the key to "true" when toggle goes ON');
assert(
  /localStorage\.removeItem\(\s*['"]channels-show-encrypted['"]\s*\)/
    .test(customizeSrc),
  'customize-v2.js removes the key when toggle goes OFF (clean default)');
assert(
  /CustomEvent\(\s*['"]mc-channels-show-encrypted-changed['"]/
    .test(customizeSrc),
  'customize-v2.js dispatches mc-channels-show-encrypted-changed on toggle');
assert(
  /data-cv2-channels-show-encrypted/.test(customizeSrc),
  'customize-v2.js renders an input with data-cv2-channels-show-encrypted hook');
assert(
  /type="checkbox"[^>]*data-cv2-channels-show-encrypted/.test(customizeSrc) ||
  /data-cv2-channels-show-encrypted[^>]*type="checkbox"/.test(customizeSrc),
  'the toggle is a checkbox input');

console.log('\n=== #1454 C: channels.js listens for the event ===');
assert(
  /addEventListener\(\s*['"]mc-channels-show-encrypted-changed['"]/
    .test(channelsSrc),
  'channels.js has a listener for mc-channels-show-encrypted-changed');
assert(
  /mc-channels-show-encrypted-changed[\s\S]{0,200}loadChannels\s*\(/
    .test(channelsSrc),
  'the listener calls loadChannels() to re-fetch without page reload');

console.log('\n=== #1454 D: default-off invariant preserved ===');
// Setting must only ever store the string "true". OFF deletes the key so
// the read-gate cleanly returns false. No "false" string ever written.
assert(
  !/localStorage\.setItem\(\s*['"]channels-show-encrypted['"]\s*,\s*['"]false['"]\s*\)/
    .test(customizeSrc),
  'customize-v2.js never writes the string "false" (delete-on-off)');

console.log('\n#1454 results: ' + passed + ' passed, ' + failed + ' failed');
process.exit(failed === 0 ? 0 : 1);
