/* Unit test for the PayloadLabels namespace restructure (PR #1804 r1
 * items 8 & 9).
 *
 * Item 8: canonical enum map at window.PayloadLabels.enums[ENUM] and
 *         helpers/derived at window.PayloadLabels.api — direct-prop
 *         collisions (e.g. an enum named BY_ID or ORDER) become
 *         impossible.
 * Item 9: payload-labels.js is loaded synchronously before its
 *         consumers in index.html. Drop the inline fallbacks in
 *         packets.js / packet-filter.js / live.js and add a top-of-file
 *         guard so a missing module is a loud crash, not silent stale
 *         data.
 */
'use strict';
const fs = require('fs');
const vm = require('vm');

const labelsSrc = fs.readFileSync('public/payload-labels.js', 'utf8');
const packetsSrc = fs.readFileSync('public/packets.js', 'utf8');
const filterSrc  = fs.readFileSync('public/packet-filter.js', 'utf8');
const liveSrc    = fs.readFileSync('public/live.js', 'utf8');

const ctx = { window: {}, console };
vm.createContext(ctx);
vm.runInContext(labelsSrc, ctx);
const PL = ctx.window.PayloadLabels;

let pass = 0, fail = 0;
function test(name, fn) {
  try { fn(); pass++; console.log('  \u2713 ' + name); }
  catch (e) { fail++; console.log('  \u2717 ' + name + ' — ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

const EXPECTED_ENUMS = [
  'REQ','RESPONSE','TXT_MSG','ACK','ADVERT','GRP_TXT','GRP_DATA',
  'ANON_REQ','PATH','TRACE','MULTIPART','CONTROL','RAW_CUSTOM'
];

console.log('=== PayloadLabels namespace (item 8) ===');

test('window.PayloadLabels exposes .enums namespace', () => {
  assert(PL && typeof PL.enums === 'object', 'PayloadLabels.enums missing');
  for (const k of EXPECTED_ENUMS) {
    assert(PL.enums[k], 'PayloadLabels.enums.' + k + ' missing');
    assert(PL.enums[k].short, 'PayloadLabels.enums.' + k + '.short missing');
    assert(PL.enums[k].enumId !== undefined, 'PayloadLabels.enums.' + k + '.enumId missing');
  }
});

test('window.PayloadLabels exposes .api namespace with helpers', () => {
  assert(PL.api && typeof PL.api === 'object', 'PayloadLabels.api missing');
  assert(typeof PL.api.shortById === 'function', 'PayloadLabels.api.shortById missing');
  assert(typeof PL.api.longById === 'function', 'PayloadLabels.api.longById missing');
  assert(typeof PL.api.enumNameById === 'function', 'PayloadLabels.api.enumNameById missing');
  assert(PL.api.SHORT_BY_ID, 'PayloadLabels.api.SHORT_BY_ID missing');
  assert(PL.api.FW_PAYLOAD_TYPES, 'PayloadLabels.api.FW_PAYLOAD_TYPES missing');
  assert(PL.api.TYPE_ALIASES, 'PayloadLabels.api.TYPE_ALIASES missing');
  assert(Array.isArray(PL.api.ORDER), 'PayloadLabels.api.ORDER missing');
});

test('legacy direct-prop access still works (back-compat)', () => {
  // Existing callers (live.js, customize*.js, the E2E that pins literals)
  // read pl[ENUM].short directly. Keep that working — restructure adds
  // .enums/.api, doesn't break the direct shape.
  for (const k of EXPECTED_ENUMS) {
    assert(PL[k] && PL[k].short, 'legacy PayloadLabels.' + k + '.short broken');
    assert(PL[k] === PL.enums[k], 'legacy ' + k + ' should alias .enums.' + k);
  }
  // Helpers still reachable at root for back-compat too.
  assert(PL.SHORT_BY_ID, 'legacy PayloadLabels.SHORT_BY_ID missing');
  assert(PL.ORDER, 'legacy PayloadLabels.ORDER missing');
});

console.log('=== inline-fallback maps (item 9) ===');

test('packets.js: DEFAULT_TYPE_NAMES inline fallback is gone', () => {
  assert(!/DEFAULT_TYPE_NAMES\s*=\s*\{/.test(packetsSrc),
    'packets.js still declares DEFAULT_TYPE_NAMES fallback map');
});

test('packets.js: hard guard for missing PayloadLabels', () => {
  // Must throw, not silently fall back.
  assert(/if\s*\(!\s*window\.PayloadLabels\s*\)\s*\{?\s*throw/.test(packetsSrc),
    'packets.js missing `if (!window.PayloadLabels) throw …` guard');
});

test('packet-filter.js: _FALLBACK_FW + _FALLBACK_ALIASES gone', () => {
  assert(!/_FALLBACK_FW\s*=\s*\{/.test(filterSrc),
    'packet-filter.js still declares _FALLBACK_FW');
  assert(!/_FALLBACK_ALIASES\s*=\s*\{/.test(filterSrc),
    'packet-filter.js still declares _FALLBACK_ALIASES');
});

test('packet-filter.js: hard guard for missing PayloadLabels', () => {
  assert(/if\s*\(!\s*window\.PayloadLabels\s*\)\s*\{?\s*throw/.test(filterSrc),
    'packet-filter.js missing throw-guard');
});

test('live.js buildLegendHtml: INLINE_LABELS fallback is gone', () => {
  assert(!/INLINE_LABELS\s*=\s*\{/.test(liveSrc),
    'live.js still declares INLINE_LABELS fallback inside buildLegendHtml');
});

test('live.js: hard guard for missing PayloadLabels at top of file', () => {
  assert(/if\s*\(!\s*window\.PayloadLabels\s*\)\s*\{?\s*throw/.test(liveSrc),
    'live.js missing throw-guard');
});

console.log('=== ' + pass + ' passed, ' + fail + ' failed ===');
process.exit(fail === 0 ? 0 : 1);
