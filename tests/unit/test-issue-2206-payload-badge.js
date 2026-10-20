/**
 * #2206 — payload-decoded sender/recipient rows carry the "from payload" chip.
 *
 * A DM's route view draws: sender → [relay hops] → recipient, where the
 * sender and recipient names are decoded from the packet PAYLOAD (1-byte
 * srcHash/destHash) rather than from the outer path bytes. Both builders in
 * map.js flag those entries `_fromPayload: true`, and route-view.js renders a
 * chip for flagged rows. The bug (#2206): the multi-path/sequence builder
 * never set the flag, and the isolate/sequence list renderer never drew the
 * chip — so the endpoints read as ordinary hops and the displayed hop count
 * was inflated.
 *
 * Strategy (mirrors test-issue-1418-edge-weights.js): extract the SHIPPING
 * functions (`payloadChip`, `rowFor`) out of route-view.js with regexes and
 * exercise them in a vm sandbox seeded with their closure variables — if the
 * production file drifts, this test breaks. The map.js sequence builder is
 * pinned by source: every sender/recipient entry it pushes must carry
 * `_fromPayload: true`.
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');

const fs = require('fs');
const path = require('path');
const vm = require('vm');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const routeSrc = fs.readFileSync(path.join(REPO_ROOT, 'public', 'route-view.js'), 'utf8');
const mapSrc = fs.readFileSync(path.join(REPO_ROOT, 'public', 'map.js'), 'utf8');

console.log('\n=== #2206 A: the chip markup lives in exactly one place ===');

const chipMatch = routeSrc.match(/function\s+payloadChip\s*\(\s*p\s*\)\s*\{[\s\S]*?\n {2}\}/);
assert(!!chipMatch, 'payloadChip() helper exists in route-view.js');

const markupSites = (routeSrc.match(/mc-rt-status-payload/g) || []).length;
assert(markupSites === 1,
  'chip markup defined exactly once, not duplicated per renderer (found ' + markupSites + ')');

const helperSites = (routeSrc.match(/var payloadBadge = payloadChip\(p\);/g) || []).length;
assert(helperSites >= 2,
  'both row builders delegate to the shared helper (found ' + helperSites + ' call sites)');

console.log('\n=== #2206 B: payloadChip() behaviour (extracted shipping function) ===');

function runPayloadChip(p) {
  const ctx = { pIn: p };
  vm.createContext(ctx);
  vm.runInContext(chipMatch[0] + '; result = payloadChip(pIn);', ctx);
  return ctx.result;
}

const chipHtml = runPayloadChip({ _fromPayload: true });
assert(typeof chipHtml === 'string' && chipHtml.indexOf('mc-rt-status-chip mc-rt-status-payload') !== -1,
  'flagged row gets the mc-rt-status-payload chip');
assert(chipHtml.indexOf('>from payload</span>') !== -1,
  'chip carries the "from payload" label');
assert(chipHtml.charAt(0) === ' ',
  'chip keeps its leading space so it separates from the node name');
assert(runPayloadChip({}) === '', 'unflagged row gets an empty badge');
assert(runPayloadChip({ _fromPayload: false }) === '', 'explicit false gets an empty badge');

console.log('\n=== #2206 C: rowFor renders the chip only for flagged rows ===');

const rowForMatch = routeSrc.match(/function\s+rowFor\s*\(\s*p\s*,\s*idx\s*\)\s*\{[\s\S]*?\n {8}\}/);
assert(!!rowForMatch, 'rowFor() function body extracted from route-view.js (isolatePath)');

function runRowFor(p, idx, iso, isoDists, isoMaxDist) {
  const ctx = {
    iso: iso, isoDists: isoDists, isoMaxDist: isoMaxDist,
    roleGlyph: function () { return '<glyph>'; },
    escapeHtml: function (s) { return String(s); },
    rampColor: function () { return '#111111'; },
    pIn: p, idxIn: idx,
  };
  vm.createContext(ctx);
  vm.runInContext(chipMatch[0] + '; ' + rowForMatch[0] + '; result = rowFor(pIn, idxIn);', ctx);
  return ctx.result;
}

const sender = { name: 'Sender', pubkey: 'aa00bb11', role: 'companion', resolved: false, isOrigin: true, _fromPayload: true };
const relay = { name: 'Relay', pubkey: 'cc22dd33', role: 'repeater', resolved: true };
const recipient = { name: 'Recipient', pubkey: 'ee44ff55', role: 'companion', resolved: false, gpsless: true, isDest: true, _fromPayload: true };
const iso = [sender, relay, recipient];
const isoDists = [12, 34];
const isoMaxDist = 34;

const senderRow = runRowFor(sender, 0, iso, isoDists, isoMaxDist);
assert(typeof senderRow === 'string' && senderRow.indexOf('<li ') === 0,
  'rowFor returns a list row');
assert(senderRow.indexOf('mc-rt-status-payload') !== -1,
  'sender row (with _fromPayload) renders the chip');
assert(senderRow.indexOf(runPayloadChip(sender)) !== -1,
  'sender row embeds the shared helper\'s exact markup');

const relayRow = runRowFor(relay, 1, iso, isoDists, isoMaxDist);
assert(relayRow.indexOf('mc-rt-status-payload') === -1,
  'relay row WITHOUT _fromPayload does not render the chip');

const recipientRow = runRowFor(recipient, 2, iso, isoDists, isoMaxDist);
assert(recipientRow.indexOf('mc-rt-status-payload') !== -1,
  'recipient row (with _fromPayload) renders the chip');

console.log('\n=== #2206 D: drawPacketRouteMulti flags every sender/recipient entry ===');

const multiMatch = mapSrc.match(/async function drawPacketRouteMulti\s*\([\s\S]*?\n {2}\}/);
assert(!!multiMatch, 'drawPacketRouteMulti() extracted from map.js');
const multiSrc = multiMatch ? multiMatch[0] : '';

const originSites = multiSrc.match(/raw\.unshift\(\{[^}]*\}\)/g) || [];
assert(originSites.length >= 3,
  'sender branches present in the sequence builder (found ' + originSites.length + ')');
originSites.forEach(function (site, i) {
  assert(site.indexOf('isOrigin: true') !== -1 && site.indexOf('_fromPayload: true') !== -1,
    'sender entry #' + (i + 1) + ' carries isOrigin + _fromPayload');
});

const destSites = multiSrc.match(/raw\.push\(\{[^}]*\}\)/g) || [];
assert(destSites.length >= 2,
  'recipient branches present in the sequence builder (found ' + destSites.length + ')');
destSites.forEach(function (site, i) {
  assert(site.indexOf('_fromPayload: true') !== -1,
    'recipient entry #' + (i + 1) + ' carries _fromPayload');
});

console.log('\n=== Summary ===');
console.log('  passed: ' + passed);
console.log('  failed: ' + failed);
if (failed > 0) process.exit(1);
