/**
 * Issue #1456 — UI rename "Usefulness" → "Traffic share" + tooltips
 * for both Traffic share and Bridge score.
 *
 * Backend keeps `usefulness_score` for API compat; a new
 * `traffic_share_score` field carries the same value and is what new
 * UI consumers should read (UI falls back to usefulness_score for
 * graceful degradation). The Go-side surface check for the new field
 * lives in cmd/server/traffic_share_score_test.go.
 *
 * This file pins the frontend pieces by file-grep on nodes.js.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const src = fs.readFileSync(path.join(__dirname, 'public', 'nodes.js'), 'utf8');

console.log('\n=== #1456: nodes.js renders "Traffic share" label ===');

// The node-stats-table row that surfaces the score. Locate the row by
// its stable id `row-usefulness-score` (kept for backwards-compat /
// DOM hooks).
const rowIdx = src.indexOf('id="row-usefulness-score"');
assert(rowIdx > 0, 'usefulness-score row block present in nodes.js');
const rowBlock = src.slice(Math.max(0, rowIdx - 400), rowIdx + 800);

assert(rowBlock.includes('Traffic share'),
  'row block contains visible label "Traffic share"');
assert(!/>\s*Usefulness\s*</.test(rowBlock),
  'row block no longer renders ">Usefulness<" as visible cell text');

console.log('\n=== #1456: tooltip text present for both scores ===');

assert(/betweenness centrality/i.test(src),
  'nodes.js contains tooltip fragment "betweenness centrality" (Bridge tooltip)');
assert(/non-advert (mesh )?traffic/i.test(src),
  'nodes.js contains tooltip fragment "non-advert traffic" (Traffic share tooltip)');
assert(/chokepoint|irreplaceable/i.test(src),
  'nodes.js Bridge tooltip mentions chokepoint/irreplaceable framing');

console.log('\n=== #1456: percent formatting near score render ===');

const usefulRender = src.slice(rowIdx - 600, rowIdx + 600);
assert(/\*\s*100\)?\s*\.toFixed\(1\)/.test(usefulRender) ||
       /toFixed\(1\)\s*\+\s*['"]%['"]/.test(usefulRender) ||
       /\$\{pct\}%/.test(usefulRender),
  'traffic-share render emits percent-formatted value (×100 + toFixed(1))');

console.log('\n=== #1456: UI prefers traffic_share_score, falls back to usefulness_score ===');

// New frontend reads the new field with graceful fallback so stale
// servers (still only emitting usefulness_score) keep working.
assert(/traffic_share_score/.test(src),
  'nodes.js references the new traffic_share_score field');
const fallbackRe = /n\.traffic_share_score\s*[!=]=\s*null\s*\?\s*n\.traffic_share_score\s*:\s*n\.usefulness_score/;
assert(fallbackRe.test(src) ||
       /traffic_share_score\s*\?\?\s*n?\.?usefulness_score/.test(src) ||
       /traffic_share_score[\s\S]{0,80}usefulness_score/.test(src),
  'nodes.js falls back to usefulness_score when traffic_share_score is absent');

console.log('\n────────────────────────────────────────');
console.log(`  ${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
