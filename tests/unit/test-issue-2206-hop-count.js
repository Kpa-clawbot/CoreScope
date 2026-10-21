/**
 * #2206 follow-up — the "N hops" figures count only true hops.
 *
 * The route meta (desktop header + mobile summary) printed `positions.length`
 * as "N hops". For a DM the positions include the payload-decoded sender and
 * recipient rows — endpoints, not hops — so a 6-relay DM read "8 hops". The
 * fix counts rows without `_fromPayload` (both builders set the flag), so
 * the same chain reads "6 hops".
 *
 * Strategy (mirrors test-issue-1418-edge-weights.js): extract the SHIPPING
 * `countHops()` out of route-view.js and exercise it in a vm sandbox; pin
 * both meta strings to the helper so a regression back to `total` fails.
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

const src = fs.readFileSync(path.join(REPO_ROOT, 'public', 'route-view.js'), 'utf8');

console.log('\n=== #2206 hop count A: the count helper exists ===');

const fnMatch = src.match(/function\s+countHops\s*\(\s*positions\s*\)\s*\{[\s\S]*?\n {2}\}/);
assert(!!fnMatch, 'countHops() helper extracted from route-view.js');

console.log('\n=== #2206 hop count B: countHops() behaviour (extracted shipping function) ===');

function runCountHops(positions) {
  const ctx = { positions: positions };
  vm.createContext(ctx);
  vm.runInContext(fnMatch[0] + '; result = countHops(positions);', ctx);
  return ctx.result;
}

const sender = { name: 'Sender', _fromPayload: true };
const recipient = { name: 'Recipient', _fromPayload: true };
const relay = function (n) { return { name: 'R' + n }; };

// The issue's chain: sender + 6 relays + recipient = 8 rows that used to
// read "8 hops".
const sixRelayDm = [sender, relay(1), relay(2), relay(3), relay(4), relay(5), relay(6), recipient];
assert(runCountHops(sixRelayDm) === 6,
  'a 6-relay DM counts 6 hops, not 8 rows (got ' + runCountHops(sixRelayDm) + ')');
assert(runCountHops([sender, relay(1), recipient]) === 1,
  'a 1-relay DM counts 1 hop');
assert(runCountHops([relay(1), relay(2), relay(3)]) === 3,
  'a plain relay chain is unchanged');
assert(runCountHops([{ isOrigin: true }, { isDest: true }]) === 2,
  'non-payload origin/dest rows still count (normal routes unchanged)');
assert(runCountHops([]) === 0, 'empty route counts 0');
assert(runCountHops([sender, recipient]) === 0, 'payload-only route counts 0');

console.log('\n=== #2206 hop count C: both meta renderers use the helper count ===');

const derived = (src.match(/var hopCount = countHops\(positions\);/g) || []).length;
assert(derived === 1, 'hopCount derived once in buildSidebar (found ' + derived + ')');

const fixed = (src.match(/hopCount \+ ' hops · ' \+ totalKm \+ ' km'/g) || []).length;
assert(fixed === 2, 'desktop header + mobile summary both render hopCount (found ' + fixed + ')');

const stale = (src.match(/total \+ ' hops · '/g) || []).length;
assert(stale === 0, 'no "N hops" string still built from total (found ' + stale + ')');

assert(/rows\[total - 1\]/.test(src),
  'the row-count `total` still slices the pinned origin/dest rows');

console.log('\n=== Summary ===');
console.log('  passed: ' + passed);
console.log('  failed: ' + failed);
if (failed > 0) process.exit(1);
