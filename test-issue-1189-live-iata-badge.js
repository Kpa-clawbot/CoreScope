/**
 * Behavior test (#1189 R2): the /live feed item DOM must surface the observer
 * IATA pill. Earlier R1 fix only updated the /packets table; mesh operators
 * live on /live and were missing the SAME-region vs CROSS-region affordance.
 *
 * Strategy mirrors test-observer-iata-1188.js — extract the helper from
 * public/live.js, evaluate it in a Node sandbox, and assert the rendered
 * HTML contains the expected `.badge-iata` markup. Mutating live.js to
 * drop the badge (revert to ${obsBadge} without ${iataBadge}) MUST flip
 * this test red.
 *
 * Additionally grep the three live-feed render sites to confirm they all
 * emit ${iataBadge} — protects against partial-fix regressions.
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

const src = fs.readFileSync(path.join(__dirname, 'public/live.js'), 'utf8');

// ── Extract obsIataBadgeHtml(pkt) ─────────────────────────────────────────
function extractFn(name) {
  const re = new RegExp(
    'function\\s+' + name + '\\s*\\([^)]*\\)\\s*\\{[\\s\\S]*?\\n\\s{2}\\}',
    'm'
  );
  const m = src.match(re);
  return m ? m[0] : null;
}

const badgeSrc = extractFn('obsIataBadgeHtml');
assert(badgeSrc != null,
  'live.js defines a top-level function obsIataBadgeHtml (the live-feed IATA helper)');

const ctx = {
  observerIataMap: {},
  escapeHtml(s) {
    return String(s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  },
  obsIataBadgeHtml: function () { return ''; },
};
vm.createContext(ctx);
if (badgeSrc) {
  vm.runInContext(badgeSrc + '\nobsIataBadgeHtml = obsIataBadgeHtml;', ctx);
}
const obsIataBadgeHtml = ctx.obsIataBadgeHtml;

// pkt.observer_iata wins ─────────────────────────────────────────────────
{
  const html = obsIataBadgeHtml({ observer_iata: 'SJC' });
  assert(html.includes('class="badge-iata"'),
    'observer_iata=SJC renders .badge-iata span, got: ' + html);
  assert(html.includes('>SJC<'),
    'observer_iata=SJC renders the IATA text, got: ' + html);
}

// Fallback to observerIataMap ────────────────────────────────────────────
{
  ctx.observerIataMap = { 'obs-1': 'SFO' };
  const html = obsIataBadgeHtml({ observer_id: 'obs-1' });
  assert(html.includes('>SFO<'),
    'falls back to observerIataMap when observer_iata absent, got: ' + html);
}

// No IATA → empty ────────────────────────────────────────────────────────
{
  ctx.observerIataMap = {};
  const html = obsIataBadgeHtml({ observer_id: 'unknown' });
  assert(html === '', 'no IATA → empty string, got: ' + html);
}

// null safety ─────────────────────────────────────────────────────────────
{
  assert(obsIataBadgeHtml(null) === '', 'null packet → empty string');
}

// Hostile input is escaped ────────────────────────────────────────────────
{
  const html = obsIataBadgeHtml({ observer_iata: '<x>' });
  assert(!html.includes('<x>') && html.includes('&lt;x&gt;'),
    'escapes hostile IATA, got: ' + html);
}

// ── Wiring guard: every feed-item render site must emit ${iataBadge} ─────
// rebuildFeedList (~2200), addFeedItemDOM (~3270), addFeedItem (~3360).
// If a future patch adds a 4th render path without the badge, this fails.
const iataBadgeUsages = (src.match(/\$\{iataBadge\}/g) || []).length;
assert(iataBadgeUsages >= 3,
  'live.js feed render sites all include ${iataBadge} (>=3 usages); got: ' + iataBadgeUsages);

// And each `obsBadge` declaration must be paired with an `iataBadge` declaration
// immediately after — guards against a render site that computes obsBadge but
// forgets iataBadge.
const obsBadgeDecls = src.match(/const obsBadge\s*=/g) || [];
const iataBadgeDecls = src.match(/const iataBadge\s*=\s*obsIataBadgeHtml\(/g) || [];
assert(obsBadgeDecls.length === iataBadgeDecls.length,
  `obsBadge decls (${obsBadgeDecls.length}) match iataBadge decls (${iataBadgeDecls.length})`);

console.log(`\n=== Results: ${passed} passed, ${failed} failed ===`);
process.exit(failed > 0 ? 1 : 0);
