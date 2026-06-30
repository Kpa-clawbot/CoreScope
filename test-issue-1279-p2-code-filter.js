/* Unit tests for issue #1279 P2 #3 — code1/code2 filter grammar */
'use strict';
const vm = require('vm');
const fs = require('fs');

const labelsCode = fs.readFileSync('public/payload-labels.js', 'utf8');
const code = fs.readFileSync('public/packet-filter.js', 'utf8');
const ctx = { window: {}, console };
vm.createContext(ctx);
// PR #1804 r1 item 9: packet-filter.js hard-fails if window.PayloadLabels
// is missing. Pre-load the canonical module so the test exercises the
// production code path.
vm.runInContext(labelsCode, ctx);
vm.runInContext(code, ctx);
const PF = ctx.window.PacketFilter;

let pass = 0, fail = 0;
function test(name, fn) {
  try { fn(); pass++; console.log('  ✓ ' + name); }
  catch (e) { console.log('  ✗ ' + name + ' — ' + e.message); fail++; }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

const withCodes = {
  route_type: 0, payload_type: 2, raw_hex: '00aabbccdd',
  decoded_json: JSON.stringify({ transportCodes: { code1: 'AABB', code2: 'CCDD' } })
};
const noCodes = {
  route_type: 1, payload_type: 2, raw_hex: '04',
  decoded_json: JSON.stringify({})
};

test('code1 == AABB matches transport packet', () => {
  assert(PF.compile('code1 == AABB').filter(withCodes));
});
test('code1 == aabb (case-insensitive)', () => {
  assert(PF.compile('code1 == aabb').filter(withCodes));
});
test('code1 == AABB does NOT match packet without transportCodes', () => {
  assert(!PF.compile('code1 == AABB').filter(noCodes));
});
test('code2 == CCDD matches', () => {
  assert(PF.compile('code2 == CCDD').filter(withCodes));
});
test('code1 != AABB false when code1 is AABB', () => {
  assert(!PF.compile('code1 != AABB').filter(withCodes));
});
test('FIELDS metadata includes code1 and code2', () => {
  var names = PF.FIELDS.map(function(f){return f.name;});
  assert(names.indexOf('code1') !== -1, 'code1 missing from FIELDS');
  assert(names.indexOf('code2') !== -1, 'code2 missing from FIELDS');
});

console.log('\n' + pass + ' passed, ' + fail + ' failed');
process.exit(fail === 0 ? 0 : 1);
