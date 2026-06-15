'use strict';
// Unit test for the client-RX coverage frontend gate (SP2).
//
// The coverage toggle (#nqCoverage) and its legend (#nqCovLegend) are built
// inline inside node-reach.js's load() — a large async function that depends on
// api()/Leaflet, so it can't be invoked headless. Instead we extract the exact
// actions-HTML concatenation block from the real source and evaluate it in a vm
// sandbox with both values of window.MC_CLIENT_RX_COVERAGE. This exercises the
// real source markup logic (no hand-copied duplicate) for the gate.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const src = fs.readFileSync(path.join(__dirname, 'public', 'node-reach.js'), 'utf8');

// Slice the actions-HTML expression: from the '<div class="nq-actions ...' line
// through the table that closes the block (ends with '</div></div>';).
const startMarker = "'<div class=\"nq-actions nq-noprint\">'";
const startIdx = src.indexOf(startMarker);
assert.ok(startIdx >= 0, 'could not locate nq-actions block start in node-reach.js');
const endMarker = "'</div></div>';";
const endIdx = src.indexOf(endMarker, startIdx);
assert.ok(endIdx >= 0, 'could not locate nq-actions block end in node-reach.js');
// Drop the trailing ";" so we can wrap the concatenation as a single expression.
let block = src.slice(startIdx, endIdx + endMarker.length).replace(/;\s*$/, '');

// Render the actions HTML for a given flag value via a controlled sandbox.
function renderActions(flag) {
  const sandbox = {
    window: { MC_CLIENT_RX_COVERAGE: flag },
    coverageOn: false,
    statsHtml: '',
  };
  vm.createContext(sandbox);
  return vm.runInContext('(' + block + ')', sandbox);
}

// Flag OFF ⇒ no coverage checkbox, no legend.
const off = renderActions(false);
assert.ok(!off.includes('id="nqCoverage"'),
  'flag false: actions HTML must NOT contain id="nqCoverage"');
assert.ok(!off.includes('id="nqCovLegend"'),
  'flag false: actions HTML must NOT contain id="nqCovLegend"');

// Flag ON ⇒ coverage checkbox and legend present.
const on = renderActions(true);
assert.ok(on.includes('id="nqCoverage"'),
  'flag true: actions HTML MUST contain id="nqCoverage"');
assert.ok(on.includes('id="nqCovLegend"'),
  'flag true: actions HTML MUST contain id="nqCovLegend"');

// Sanity: the non-gated controls render regardless of the flag.
assert.ok(off.includes('id="nqIncoming"') && on.includes('id="nqIncoming"'),
  'incoming filter must render irrespective of the coverage flag');

console.log('coverage gate (node-reach actions HTML) OK');
