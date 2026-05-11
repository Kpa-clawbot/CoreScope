/**
 * Issue #1057 — Channels sidebar + message area fluidity (static assertions).
 *
 * Verifies that public/style.css makes the channels sidebar fluid rather
 * than locked at fixed pixel widths, that the message area uses remaining
 * space, and that narrow stacking is driven by a container query (not a
 * hardcoded fixed-px breakpoint baked into the channels layout).
 *
 * Companion E2E (real viewport assertions): test-channel-fluid-e2e.js.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const css = fs.readFileSync(path.join(__dirname, 'public/style.css'), 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// Helper: extract the first matching rule body for a selector (no nesting parser, simple regex).
function ruleBody(selector) {
  // Match selector that's NOT preceded by a non-space CSS-name char (avoids matching .ch-sidebar-foo when looking for .ch-sidebar).
  const re = new RegExp(
    '(?:^|[^a-zA-Z0-9_-])' + selector.replace(/[-/\\^$*+?.()|[\]{}]/g, '\\$&') + '\\s*\\{([^}]*)\\}'
  );
  const m = css.match(re);
  return m ? m[1] : null;
}

console.log('\n=== #1057 Channels sidebar fluid width ===');

test('.ch-sidebar default rule uses clamp() for width (not a fixed px)', () => {
  const body = ruleBody('.ch-sidebar');
  assert.ok(body, '.ch-sidebar rule not found');
  assert.ok(/width\s*:\s*clamp\s*\(/.test(body),
    `.ch-sidebar should use width: clamp(...); got: ${body.trim()}`);
});

test('.ch-sidebar declares a sane min-width (>=200px)', () => {
  const body = ruleBody('.ch-sidebar');
  assert.ok(body, '.ch-sidebar rule not found');
  // min-width may be a literal px or part of clamp's first arg.
  const minMatch = body.match(/min-width\s*:\s*(\d+)px/);
  assert.ok(minMatch, '.ch-sidebar should declare min-width');
  const px = parseInt(minMatch[1], 10);
  assert.ok(px >= 200 && px <= 280, `min-width should be 200..280px (got ${px}px)`);
});

console.log('\n=== #1057 Message area fills remaining width ===');

test('.ch-main keeps flex:1 (uses remaining width on wide screens)', () => {
  const body = ruleBody('.ch-main');
  assert.ok(body, '.ch-main rule not found');
  assert.ok(/flex\s*:\s*1\b/.test(body),
    '.ch-main should use flex: 1 to fill remaining width');
});

console.log('\n=== #1057 Narrow stacking via container query (not hardcoded px) ===');

test('style.css declares a container query for the channels layout', () => {
  // Either container-type or container shorthand on .ch-layout.
  const layout = ruleBody('.ch-layout');
  assert.ok(layout, '.ch-layout rule not found');
  assert.ok(/container(-type|-name|\s*:)/.test(layout),
    '.ch-layout should declare container-type/container for container queries');
});

test('style.css contains an @container rule that targets the channels sidebar', () => {
  // Look for "@container ... .ch-sidebar" anywhere.
  const re = /@container[^{]*\{[\s\S]*?\.ch-(sidebar|layout|main)[^{]*\{/;
  assert.ok(re.test(css),
    'expected an @container rule scoping .ch-sidebar/.ch-layout/.ch-main');
});

test('removed legacy fixed 220px override at @media (max-width: 900px) for .ch-sidebar', () => {
  // The old block: @media (max-width: 900px){ ... .ch-sidebar { width: 220px; min-width: 220px; } ... }
  // After fluid migration this hardcoded sub-rule should be gone (the clamp+container query handle it).
  const m = css.match(/@media[^{]*max-width:\s*900px[^{]*\{[\s\S]*?\n\}/);
  if (m) {
    assert.ok(!/\.ch-sidebar\s*\{[^}]*width\s*:\s*220px/.test(m[0]),
      'legacy hardcoded .ch-sidebar width:220px override should be removed');
  }
});

console.log('\n=== Summary ===');
console.log(`${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
