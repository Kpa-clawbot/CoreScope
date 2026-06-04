/**
 * Behavior test (#1574): operator-configurable `liveMap.maxNodes` cap.
 *
 * Today `public/live.js` hardcodes `/api/nodes?limit=2000` on two adjacent
 * lines (~2515-2516) for the live-map node-load path. The same `2000`
 * magic number also appears at :480 for `/api/packets?limit=2000` in the
 * VCR-rewind code. The fix is to drive both literals from a single
 * `LIVE_MAP_MAX_NODES` constant that is loaded from the server's
 * client-config endpoint (`/api/config/client`) as `liveMapMaxNodes`,
 * with operator override via `config.json` `liveMap.maxNodes` (default
 * 2000, server-side clamp [100, 20000]).
 *
 * Reverting the fix (re-introducing the `?limit=2000` literal) MUST flip
 * this test red.
 */
'use strict';
const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  \u2705 ' + msg); }
  else { failed++; console.error('  \u274c ' + msg); }
}

const liveSrc = fs.readFileSync(path.join(__dirname, 'public', 'live.js'), 'utf8');
const rolesSrc = fs.readFileSync(path.join(__dirname, 'public', 'roles.js'), 'utf8');

// ── 1. The hardcoded /api/nodes?limit=2000 literal must be gone ─────────
const nodesLiteralHits = (liveSrc.match(/\/api\/nodes\?limit=2000/g) || []).length;
assert(nodesLiteralHits === 0,
  'public/live.js no longer contains hardcoded `/api/nodes?limit=2000` literal (found ' +
  nodesLiteralHits + ' occurrence(s))');

// ── 2. live.js consumes the operator-configurable LIVE_MAP_MAX_NODES ─────
assert(/LIVE_MAP_MAX_NODES/.test(liveSrc),
  'public/live.js references LIVE_MAP_MAX_NODES constant');

// ── 3. roles.js plumbs `liveMapMaxNodes` from /api/config/client ────────
assert(/liveMapMaxNodes/.test(rolesSrc),
  'public/roles.js reads `liveMapMaxNodes` from /api/config/client and exposes LIVE_MAP_MAX_NODES');

// ── 4. config.example.json documents the knob ────────────────────────────
const cfgExample = fs.readFileSync(path.join(__dirname, 'config.example.json'), 'utf8');
assert(/"maxNodes"\s*:/.test(cfgExample),
  'config.example.json declares liveMap.maxNodes default');

console.log('\nResults: ' + passed + ' passed, ' + failed + ' failed');
process.exit(failed > 0 ? 1 : 0);
