/**
 * #1101 — Strip Share modal: remove redundant URL copy + duplicated key field.
 *
 * Acceptance criteria:
 *   - Share modal contains only: QR (just the QR image, nothing else
 *     in that box), Hex Key field with single Copy button BELOW the QR,
 *     privacy warning, Close ✕ button.
 *   - No "Copy URL" affordance ANYWHERE in the modal.
 *   - No duplicated meshcore:// URL field below the QR.
 *   - The QR box (#chShareQr) must contain ONLY the QR image — no URL
 *     text, no Copy Key button overlapping it.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const channelsSrc = fs.readFileSync(path.join(__dirname, 'public', 'channels.js'), 'utf8');
const qrSrc = fs.readFileSync(path.join(__dirname, 'public', 'channel-qr.js'), 'utf8');

console.log('\n=== #1101: Share modal markup ===');

// Locate the share modal markup block.
const shareModalIdx = channelsSrc.indexOf('id="chShareModal"');
assert(shareModalIdx > 0, 'share modal markup located');
// Tighten block isolation: scan forward for the share modal's own
// closing tag (the outer overlay div is indented 6 spaces, so its
// matching close is the first "\n      </div>" we hit after the
// opener). Falls back to the old ch-main heuristic if that pattern
// disappears for any reason.
let shareEnd = channelsSrc.indexOf('\n      </div>', shareModalIdx);
if (shareEnd < 0) {
  shareEnd = channelsSrc.indexOf('<div class="ch-main"', shareModalIdx);
}
const shareModalBlock = channelsSrc.substring(shareModalIdx, shareEnd);
assert(shareModalBlock.length > 0 && shareModalBlock.length < 4000,
  'share modal block isolated');

// Hex key field MUST still be present (single source of truth).
assert(/id="chShareKey"/.test(shareModalBlock),
  'share modal still exposes the hex key field with a Copy button');

// meshcore:// URL field MUST be removed.
assert(!/id="chShareUrl"/.test(shareModalBlock),
  'share modal does NOT render a #chShareUrl input field');
assert(!/data-share-field="url"/.test(shareModalBlock),
  'share modal does NOT render any [data-share-field="url"] element');
assert(!/data-share-copy="url"/.test(shareModalBlock),
  'share modal does NOT render any [data-share-copy="url"] button');
assert(!/meshcore:\/\/ URL/.test(shareModalBlock),
  'share modal does NOT show a "meshcore:// URL" label');

// Privacy warning + close button still required.
assert(/ch-modal-warn/.test(shareModalBlock),
  'share modal still includes the privacy warning');
assert(/id="chShareModalClose"/.test(shareModalBlock),
  'share modal still has the ✕ close button');

console.log('\n=== #1101: openShareModal() body ===');

// openShareModal must no longer reference chShareUrl or build URL into a field.
const openIdx = channelsSrc.indexOf('function openShareModal(');
assert(openIdx > 0, 'openShareModal located');
const openEnd = channelsSrc.indexOf('function ', openIdx + 30);
const openBlock = channelsSrc.substring(openIdx, openEnd);
assert(!/getElementById\('chShareUrl'\)/.test(openBlock),
  'openShareModal does NOT look up #chShareUrl');
assert(!/urlField\.value\s*=/.test(openBlock),
  'openShareModal does NOT assign to urlField.value');

console.log('\n=== #1101: ChannelQR.generate() supports qrOnly ===');

// ChannelQR.generate must accept an opts.qrOnly flag so the Share
// modal's QR box can render JUST the QR image — no URL line, no
// inline Copy Key button. (The Share modal has its own dedicated
// hex key field + Copy button BELOW the QR.)
assert(/function generate\([^)]*opts[^)]*\)/.test(qrSrc),
  'ChannelQR.generate accepts an opts argument');
assert(/qrOnly/.test(qrSrc),
  'ChannelQR.generate honours opts.qrOnly');

// Share modal call site must pass qrOnly:true.
assert(/ChannelQR\.generate\([^)]*qrOnly[^)]*\)/.test(channelsSrc) ||
       /ChannelQR\.generate\([\s\S]{0,200}qrOnly\s*:\s*true/.test(channelsSrc),
  'openShareModal passes { qrOnly: true } to ChannelQR.generate');

console.log('\n=== Results: ' + passed + ' passed, ' + failed + ' failed ===');
process.exit(failed > 0 ? 1 : 0);
