/**
 * Follow-up UX fixes to #1037 channel modal/sidebar redesign:
 *
 *   1. ✕ remove button must hit a 44×44px touch target (WCAG 2.5.5).
 *   2. Channel rows must NOT display "0 messages" — when no messages
 *      have been decrypted yet, omit the count entirely.
 *   3. Modal footer wording: keys removed via ✕ button, not by
 *      clearing browser data.
 *   4. Each user-added (PSK) row must expose a Share affordance that
 *      re-opens the QR/key for that channel without re-generating it.
 *   5. "(your key)" preview suffix on user-added rows is noise; drop it.
 *      Likewise no key hex in the default row rendering.
 */
'use strict';

const fs = require('fs');
const path = require('path');

const chSrc = fs.readFileSync(path.join(__dirname, 'public/channels.js'), 'utf8');
const cssSrc = fs.readFileSync(path.join(__dirname, 'public/style.css'), 'utf8');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

console.log('\n=== Fix 1: ✕ touch target ≥ 44×44px ===');
const removeRule = (cssSrc.match(/\.ch-remove-btn\s*\{[^}]*\}/) || [''])[0];
assert(/min-width:\s*44px/.test(removeRule),
  '.ch-remove-btn declares min-width: 44px');
assert(/min-height:\s*44px/.test(removeRule),
  '.ch-remove-btn declares min-height: 44px');

console.log('\n=== Fix 2: no "0 messages" in default row ===');
// renderChannelRow must not emit a literal "0 messages" preview when
// messageCount is missing/zero. Look for the offending fallback pattern.
assert(!/\$\{ch\.messageCount\s*\|\|\s*0\}\s*messages/.test(chSrc),
  'preview no longer falls back to "${ch.messageCount || 0} messages"');
assert(!/\$\{ch\.messageCount\s*\|\|\s*0\}\s*packets/.test(chSrc),
  'encrypted preview no longer falls back to "${ch.messageCount || 0} packets"');

console.log('\n=== Fix 3: privacy footer wording ===');
assert(!/Clear browser data to remove stored keys/.test(chSrc),
  'old "Clear browser data to remove stored keys" copy is gone');
assert(/Use\s+✕\s+to remove individual channels/.test(chSrc),
  'new copy points at the ✕ button for individual key removal');

console.log('\n=== Fix 4: Share/reshare affordance on user-added rows ===');
assert(/data-share-channel="/.test(chSrc),
  'user-added rows carry a data-share-channel hook');
assert(/class="ch-share-btn"/.test(chSrc),
  'share affordance uses .ch-share-btn class');
// Click handler must wire the share button to ChannelQR.generate (or a
// QR-display fallback). The handler lives in the chListEl click delegation.
assert(/data-share-channel/.test(chSrc) && /ChannelQR/.test(chSrc),
  'share handler references ChannelQR for QR rendering');
// Modal must have a target container for the reshare QR output.
assert(/id="chShareOutput"/.test(chSrc) || /id="chReshareOutput"/.test(chSrc),
  'modal has a reshare QR output container');

console.log('\n=== Fix 5: "(your key)" suffix removed from preview ===');
assert(!/\(your key\)/.test(chSrc),
  'user-added preview no longer says "(your key)"');

console.log('\n=== Fix 6: browser-local warning is obvious ===');
// A visible callout in the modal — separate from the small privacy footer.
assert(/class="ch-modal-callout"/.test(chSrc),
  'modal has a dedicated .ch-modal-callout for the locality warning');
assert(/THIS browser only/.test(chSrc),
  'callout uses emphatic copy: "Channels are saved to THIS browser only"');
assert(/won't appear on other devices or browsers|won.t appear on other devices/.test(chSrc),
  'callout warns that channels won\u2019t appear on other devices/browsers');

// Sidebar "My Channels" section header gets a locality marker.
assert(/My Channels[^<]*\(this browser\)|🖥️[^<]*My Channels|My Channels[^<]*🖥️/.test(chSrc),
  'My Channels section header reinforces locality (🖥️ or "(this browser)")');

// Remove confirm prompt explicitly mentions "this browser".
assert(/permanently remove the key from this browser/.test(chSrc),
  'remove confirm says key is permanently removed from this browser');

console.log('\n=== Results ===');
console.log('Passed: ' + passed + ', Failed: ' + failed);
process.exit(failed > 0 ? 1 : 0);
