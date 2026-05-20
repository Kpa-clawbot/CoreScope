/**
 * Composed-cell test (#1189 R2 item 3): the grouped row's Observer cell must
 * NOT render two adjacent unlabeled "+N +M" tokens.
 *
 * Before R2: `Name SJC SFO +1 +5` — `+1` was distinct-IATA overflow,
 * `+5` was observer-count overflow. On mobile the two unlabeled tokens
 * wrap/clip and operators can't tell what either number means. The
 * eyeball-count badge `👁 N` in the col-rpt cell already conveys multi-
 * reception, so we drop the observer-count `+N` from this composed cell
 * and keep `+N` semantics SINGLE — distinct-IATA overflow only.
 *
 * Strategy: extract just the col-observer template-literal expression
 * from buildGroupRowHtml in public/packets.js, evaluate it in a Node
 * sandbox with mocked helpers, and assert the output contains exactly
 * ONE `+N` token (or zero if all IATAs fit). Mutation test: revert the
 * fix → this test must turn red.
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  \u2705 ' + msg); }
  else { failed++; console.error('  \u274c ' + msg); }
}

const src = fs.readFileSync(path.join(__dirname, 'public/packets.js'), 'utf8');

// ── Extract the composed col-observer cell expression. ──────────────────
// The cell is built inline in buildGroupRowHtml's template literal:
//   <td class="col-observer" ...>${ ... composed expression ... }</td>
// We capture the expression between the `>` and the closing `</td>` of the
// col-observer cell when the row is a GROUP HEADER (isSingle false branch).
// The expression matches the pattern:
//   isSingle ? <single-branch> : <multi-branch>
// We want to verify the multi-branch.
const cellRe = /class="col-observer"[^<]*?>\$\{(isSingle\s*\?[\s\S]*?)\}<\/td>/m;
const m = src.match(cellRe);
assert(m != null,
  'extracted composed col-observer cell expression from buildGroupRowHtml');

if (m) {
  // The matched expression is `isSingle ? <single> : <multi>`. Force the
  // multi branch by binding isSingle=false in the sandbox and evaluating.
  const expr = m[1];

  // Sandbox mocks. `groupedObserverIataBadgesHtml` returns a deterministic
  // multi-IATA overflow string so the composed cell would historically have
  // shown `<helper output> +<observer_count - 1>` adjacent to it.
  const ctx = {
    p: {
      observer_count: 6,                 // would have rendered ' +5'
      observer_id: 'obsA',
      _children: [],                     // collapsed view — no children
      distinct_iatas: ['SJC', 'SFO', 'OAK'],
    },
    headerObserverId: 'obsA',
    isSingle: false,
    truncate(s, n) { return String(s || '').slice(0, n); },
    obsNameOnly(_id) { return 'NameA'; },
    obsIataBadge(_p) { return ''; },
    groupedObserverIataBadgesHtml(_p) {
      return '<span class="badge-iata">SJC</span><span class="badge-iata">SFO</span> +1';
    },
    escapeHtml(s) {
      return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
        .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
    },
  };
  vm.createContext(ctx);
  // Wrap in template literal so all ${} substitutions resolve.
  ctx.__result__ = vm.runInContext('`${' + expr + '}`', ctx);
  const cell = ctx.__result__;

  // Cell must include the IATA badges from the helper.
  assert(cell.includes('SJC') && cell.includes('SFO'),
    'composed cell contains the distinct-IATA badges, got: ' + cell);

  // CORE assertion: exactly ONE `+N` token in the cell. Before the fix the
  // cell would carry `+1 +5` (distinct-IATA overflow + observer-count
  // overflow); after the fix only `+1` remains.
  const plusTokens = cell.match(/\+\d+/g) || [];
  assert(plusTokens.length === 1,
    'composed cell carries exactly ONE `+N` token (distinct-IATA only); got ' +
    plusTokens.length + ' tokens: ' + JSON.stringify(plusTokens) +
    '\n  cell=' + cell);

  // The `+5` (observer_count - 1 = 5) MUST NOT appear — that would mean
  // the observer-count overflow was re-introduced.
  assert(!cell.includes('+5'),
    'composed cell must NOT contain observer-count overflow `+5`; got: ' + cell);

  // And the cell must not contain ` +` adjacent twice (the failure mode is
  // `+N +M` with whitespace between two `+` tokens).
  assert(!/\+\d+\s+\+\d+/.test(cell),
    'composed cell must NOT contain adjacent `+N +M` tokens; got: ' + cell);

  // Edge case: even when the helper returns no overflow at all, the cell
  // must still NOT append an observer-count `+N`.
  const ctx2 = Object.assign({}, ctx);
  ctx2.groupedObserverIataBadgesHtml = function () {
    return '<span class="badge-iata">SJC</span>';
  };
  vm.createContext(ctx2);
  ctx2.__result__ = vm.runInContext('`${' + expr + '}`', ctx2);
  const cell2 = ctx2.__result__;
  const plusTokens2 = cell2.match(/\+\d+/g) || [];
  assert(plusTokens2.length === 0,
    'composed cell with single-IATA helper output has ZERO `+N` tokens; got: ' +
    JSON.stringify(plusTokens2) + '\n  cell=' + cell2);
}

console.log(`\n=== Results: ${passed} passed, ${failed} failed ===`);
process.exit(failed > 0 ? 1 : 0);
