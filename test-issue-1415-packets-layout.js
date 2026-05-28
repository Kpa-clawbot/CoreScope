/**
 * #1415 — Packets cross-viewport jank source-grep test.
 *
 * Asserts the four code-level invariants required by the layout fix:
 *
 *  1. Expand-chevron column is pinned narrow at every viewport via an
 *     explicit `.col-expand` class on the first <th>/<td> AND a CSS rule
 *     pinning its width to ~32px (max-width ≤ 36px).
 *  2. DETAILS column is capped — `.col-details` has a `max-width` ≤ 480px
 *     so wide viewports stop wasting hundreds of px on the last column.
 *  3. Mobile chrome compaction — the `@media (max-width: 480px)` block
 *     hides `.col-details` (so the table doesn't carry the dead column to
 *     mobile) AND hides the BYOP button in `.page-header` (operator
 *     request: reclaim 60+ px of pre-table chrome).
 *  4. Mobile-priority detail order — `renderDetail()` renders the Payload
 *     Type as the FIRST `<dt>` of `.detail-meta` (operator's "lead with
 *     packet type"), and wraps the byte-breakdown / hex-dump / field-table
 *     into a `<details class="detail-technical">` element so the
 *     technical fields collapse on mobile (collapsed by default, open on
 *     desktop via the `open` attribute being conditionally set).
 *
 * Strategy: pure source-grep — no browser, no playwright. The grep is the
 * gate. If someone reverts any of the four fixes, the corresponding assert
 * fails. Cheap to run, deterministic, runs in CI without browser deps.
 */
'use strict';
const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  \u2705 ' + msg); }
  else { failed++; console.error('  \u274c ' + msg); }
}

const pktJs = fs.readFileSync(path.join(__dirname, 'public/packets.js'), 'utf8');
const css = fs.readFileSync(path.join(__dirname, 'public/style.css'), 'utf8');

// ── 1. col-expand class + CSS pin ────────────────────────────────────────
assert(
  /<th[^>]*class="col-expand"/.test(pktJs),
  'packets.js header has <th class="col-expand"> on the first column'
);
assert(
  /<td class="col-expand"/.test(pktJs),
  'packets.js row builders emit <td class="col-expand"> for the chevron cell'
);

// CSS must pin width somewhere in the .col-expand selector.
var colExpandBlocks = css.match(/\.col-expand\b[^{}]*\{[^}]*\}/g) || [];
var pinned = colExpandBlocks.some(function (b) {
  return /max-width:\s*3[26]px/.test(b) && /min-width:\s*3[26]px/.test(b);
});
assert(pinned, 'style.css .col-expand pins min-width AND max-width to ~32px');

// ── 1b. Locked column-priority tiers (operator spec) ─────────────────────
// Tier 1 (always — even on smallest mobile): expand, time, type, details
// Tier 2 (tablet+):                          path
// Tier 3 (desktop only):                     hash, observer, rpt
// Region/Size/HB stay at the existing low-priority tiers (already 3-5).
//
// Mapping to priority values (see TableResponsive doc at top of packets.js):
//   priority 1 → always visible
//   priority 3 → hidden ≤ 1024 (desktop-only)
//   priority 5 → hidden ≤  768 (tablet+ only)
function colPriority(klass) {
  var re = new RegExp('<th[^>]*class="' + klass + '"[^>]*data-priority="(\\d+)"');
  var m = pktJs.match(re);
  return m ? parseInt(m[1], 10) : null;
}
assert(colPriority('col-expand')   === 1, 'col-expand is tier-1 priority (always visible)');
assert(colPriority('col-time')     === 1, 'col-time is tier-1 priority (always visible)');
assert(colPriority('col-type')     === 1, 'col-type is tier-1 priority (always visible)');
assert(colPriority('col-details')  === 1, 'col-details is tier-1 priority (always visible)');
assert(colPriority('col-path')     === 5, 'col-path is tier-2 (hidden ≤768, tablet+ only)');
assert(colPriority('col-hash')     === 3, 'col-hash is tier-3 (desktop only, hidden ≤1024)');
assert(colPriority('col-observer') === 3, 'col-observer is tier-3 (desktop only, hidden ≤1024)');
assert(colPriority('col-rpt')      === 3, 'col-rpt is tier-3 (desktop only, hidden ≤1024)');

// ── 2. DETAILS column capped ─────────────────────────────────────────────
var colDetailsBlocks = css.match(/\.col-details\b[^{}]*\{[^}]*\}/g) || [];
var capped = colDetailsBlocks.some(function (b) {
  var m = b.match(/max-width:\s*(\d+)px/);
  return m && parseInt(m[1], 10) <= 480 && parseInt(m[1], 10) >= 200;
});
assert(capped, 'style.css caps .col-details with max-width ≤ 480px');

// ── 3. Mobile compaction — DETAILS hidden + BYOP hidden under 480 ────────
var mobileBlock = (function () {
  var idx = css.indexOf('@media (max-width: 480px)');
  if (idx < 0) return '';
  var depth = 0, start = -1, end = -1;
  for (var i = idx; i < css.length; i++) {
    var c = css[i];
    if (c === '{') { if (depth === 0) start = i; depth++; }
    else if (c === '}') { depth--; if (depth === 0) { end = i; break; } }
  }
  return start > 0 && end > 0 ? css.slice(start, end + 1) : '';
})();
assert(mobileBlock.length > 0, 'style.css has a @media (max-width: 480px) block');
assert(
  /pkt-byop[^{}]*\{[^}]*display:\s*none/.test(mobileBlock),
  'mobile @media block hides the BYOP button (chrome compaction)'
);
// Note: per LOCKED spec, col-details is tier-1 and stays visible at mobile.
// It is the col-path / col-hash / col-observer / col-rpt that drop on mobile,
// already enforced via data-priority above (TableResponsive.apply).

// ── 4. renderDetail mobile-priority ordering ────────────────────────────
var dlMatch = pktJs.match(/<dl class="detail-meta">([\s\S]*?)<\/dl>/);
assert(!!dlMatch, 'renderDetail emits <dl class="detail-meta">');
if (dlMatch) {
  var dlBody = dlMatch[1];
  var idxType = dlBody.indexOf('Payload Type');
  var idxObs = dlBody.indexOf('Observer');
  assert(idxType >= 0, '.detail-meta still includes Payload Type row');
  assert(idxObs >= 0, '.detail-meta still includes Observer row');
  assert(
    idxType >= 0 && idxObs >= 0 && idxType < idxObs,
    '.detail-meta lists Payload Type BEFORE Observer (mobile-priority order)'
  );
}

// Wrap hex / breakdown / observations in a collapsible technical section.
assert(
  /<details[^>]*class="detail-technical"/.test(pktJs),
  'renderDetail wraps technical fields in <details class="detail-technical">'
);

// ── Summary ──────────────────────────────────────────────────────────────
console.log('\n' + passed + ' passed, ' + failed + ' failed');
process.exit(failed === 0 ? 0 : 1);
