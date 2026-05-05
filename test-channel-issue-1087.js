/**
 * #1087 — Channel modal QR/share regression tests.
 *
 * Pure source-string + targeted DOM-string assertions covering all 4 bugs:
 *
 *   1. QR generator must use the vendored Kazuhiko Arase `qrcode()` API
 *      (lowercase). Old code checked `root.QRCode` which never existed,
 *      causing "[QR library not loaded]" on every Generate click.
 *   2. The Share button must use the user's display label (not the
 *      internal `psk:<hex8>` lookup key) when building the QR/URL.
 *   3. PSK channel persistence: the Add/Generate handlers must route
 *      writes through a single dedicated helper (`persistAddedChannel`)
 *      so storage happens synchronously inside the submit path — not as
 *      a side effect of subsequent UI events. The helper must also
 *      verify localStorage actually contains the key after the write.
 *   4. The Share affordance must open a DEDICATED modal element
 *      (`chShareModal`) — not reuse the Add Channel modal
 *      (`chAddChannelModal`).
 *
 * Companion E2E coverage: test-channel-issue-1087-e2e.js
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

const chSrc   = fs.readFileSync(path.join(__dirname, 'public/channels.js'),       'utf8');
const qrSrc   = fs.readFileSync(path.join(__dirname, 'public/channel-qr.js'),     'utf8');
const decSrc  = fs.readFileSync(path.join(__dirname, 'public/channel-decrypt.js'), 'utf8');
const idxSrc  = fs.readFileSync(path.join(__dirname, 'public/index.html'),         'utf8');

console.log('\n=== #1087 Bug 1: QR vendor library is wired correctly ===');

// The vendored library is Kazuhiko Arase's qrcode-generator (lowercase
// `qrcode` global). The generate() helper must call into that API —
// either via `root.qrcode(...)` / `window.qrcode(...)` / a direct
// `qrcode(` call producing an image with `createImgTag` /
// `createSvgTag` / `createDataURL`.
assert(/\bqrcode\s*\(\s*\d/.test(qrSrc) ||
       /createImgTag|createSvgTag|createDataURL/.test(qrSrc),
  'channel-qr.js generate() uses the vendored qrcode-generator API');

// The "[QR library not loaded]" fallback string must NOT be the only
// detection branch for the generator — the new code must support the
// lowercase qrcode global. We accept either (a) the old check is gone
// or (b) the new check is added alongside.
assert(/typeof\s+(root|window)\.qrcode\s*===\s*['"]function['"]/.test(qrSrc) ||
       /typeof\s+qrcode\s*===\s*['"]function['"]/.test(qrSrc),
  'channel-qr.js detects the lowercase `qrcode` global (not just `QRCode`)');

console.log('\n=== #1087 Bug 2: Share QR uses the user display label ===');

// The share-channel click handler must resolve a display label
// (via ChannelDecrypt.getLabel / .getLabels / userLabel lookup) and
// pass that human-readable name to ChannelQR.generate — NOT the raw
// `psk:<hex8>` key prefix.
var shareIdx = chSrc.indexOf("data-share-channel");
assert(shareIdx > 0, 'found share button DOM marker');

// Find a window of source covering the share button click handler.
var shareHandlerIdx = chSrc.indexOf("e.target.closest('[data-share-channel]')");
assert(shareHandlerIdx > 0, 'found share-channel click handler block');
var shareBlock = chSrc.substring(shareHandlerIdx, shareHandlerIdx + 2500);

assert(/getLabel\s*\(|getLabels\s*\(|userLabel|labels\s*\[/.test(shareBlock),
  'share handler resolves the user display label before rendering QR');

// Belt-and-suspenders: the call to ChannelQR.generate() inside the
// share handler must NOT pass a value derived only from
// `shareHash.substring(5)` (which yields `psk:<hex8>`). Require an
// explicit label fallback chain.
assert(/ChannelQR\.generate\s*\(\s*[a-zA-Z_]*[Ll]abel/.test(shareBlock) ||
       /ChannelQR\.generate\s*\(\s*displayLabel|displayName/.test(shareBlock),
  'share handler passes a label-derived display name to ChannelQR.generate');

console.log('\n=== #1087 Bug 3: PSK channel persistence via dedicated helper ===');

// A single canonical helper must own the persistence path. Both the
// Generate and the PSK-Add submit handlers must route through it so
// storage cannot be skipped or deferred to a later UI event.
assert(/function\s+persistAddedChannel\s*\(/.test(chSrc),
  'channels.js defines a persistAddedChannel(...) helper');

// Helper must call ChannelDecrypt.storeKey AND verify the write
// landed in localStorage by re-reading it.
var helperIdx = chSrc.indexOf('function persistAddedChannel');
assert(helperIdx > 0, 'helper definition located');
var helperBlock = helperIdx > 0 ? chSrc.substring(helperIdx, helperIdx + 1500) : '';
assert(/storeKey\s*\(/.test(helperBlock),
  'persistAddedChannel calls ChannelDecrypt.storeKey()');
assert(/getStoredKeys\s*\(|getKeys\s*\(|localStorage\.getItem/.test(helperBlock),
  'persistAddedChannel verifies the write by re-reading storage');

// Both submit paths must invoke the helper.
assert(/chGenerateBtn[\s\S]{0,2000}persistAddedChannel\s*\(/.test(chSrc),
  'Generate (#chGenerateBtn) handler invokes persistAddedChannel');
assert(/chPskAddBtn[\s\S]{0,2500}persistAddedChannel\s*\(|addUserChannel[\s\S]{0,2500}persistAddedChannel\s*\(/.test(chSrc),
  'PSK Add path invokes persistAddedChannel');

console.log('\n=== #1087 Bug 4: Dedicated Share modal (separate from Add) ===');

// A NEW DOM element distinct from #chAddChannelModal must exist for
// sharing. Title, hex key field, URL field, privacy warning.
assert(/id="chShareModal"/.test(chSrc),
  'dedicated #chShareModal element exists in channels.js markup');

// Modal must NOT just be an alias for the Add modal — its internals
// must include share-specific affordances.
var shareModalIdx = chSrc.indexOf('id="chShareModal"');
assert(shareModalIdx > 0, 'share modal markup located');
var shareModalBlock = shareModalIdx > 0 ? chSrc.substring(shareModalIdx, shareModalIdx + 3000) : '';
assert(/id="chShareModalTitle"|class="ch-share-modal-title"|>Share[^<]*</.test(shareModalBlock),
  'share modal has its own title element ("Share: <Channel Name>")');
assert(/id="chShareKey"|data-share-field="key"/.test(shareModalBlock),
  'share modal exposes the hex key field with a copy affordance');
// #1101: meshcore:// URL field intentionally REMOVED — QR already
// encodes the URL, separate field/button was redundant.
assert(/trusted|privacy|do not share|only share/i.test(shareModalBlock),
  'share modal includes a privacy warning');

// Share click handler must open #chShareModal — not openAddModal().
var shareClickIdx = chSrc.indexOf("e.target.closest('[data-share-channel]')");
var shareClickBlock = shareClickIdx > 0 ? chSrc.substring(shareClickIdx, shareClickIdx + 2500) : '';
assert(/openShareModal\s*\(|chShareModal/.test(shareClickBlock),
  'share button click handler opens #chShareModal (not the Add modal)');
assert(!/openAddModal\s*\(\s*\)/.test(shareClickBlock),
  'share button click handler does NOT call openAddModal()');

console.log('\n=== Results ===');
console.log('Passed: ' + passed + ', Failed: ' + failed);
process.exit(failed > 0 ? 1 : 0);
