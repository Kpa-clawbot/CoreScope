/**
 * #1846 — Observers page pinned to 1200px on wide screens; columns crushed
 * while responsive auto-hide has already fired for narrow widths.
 *
 * Root cause: public/style.css `.observers-page { max-width: 1200px; }`
 * caps a column-dense table at 1200px, leaving unused space on wide
 * monitors while narrow viewports still trigger data-priority column
 * auto-hide.
 *
 * Fix: drop the narrow cap on `.observers-page`. Either remove `max-width`
 * entirely (preferred — let the browser use available width) or raise it
 * to a value >= 1600px (matches `.analytics-page` convention).
 *
 * This is a regression guard: a future author must not re-introduce a
 * narrow (<1600px) `max-width` on `.observers-page` without deleting or
 * updating this test.
 */
'use strict';

const fs = require('fs');
const path = require('path');

const css = fs.readFileSync(path.join(__dirname, 'public', 'style.css'), 'utf8');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ok  ', msg); }
  else      { failed++; console.log('  FAIL', msg); }
}

console.log('# #1846 observers-page width regression guard');

// Match the .observers-page block (single-line rule).
// e.g. ".observers-page { padding: 20px; max-width: 1200px; margin: 0 auto; }"
const re = /\.observers-page\s*\{([^}]*)\}/g;
const blocks = [];
let m;
while ((m = re.exec(css)) !== null) blocks.push(m[1]);

assert(blocks.length > 0, `.observers-page rule exists in public/style.css (found ${blocks.length})`);

for (const body of blocks) {
  const mw = body.match(/max-width\s*:\s*([^;]+);?/i);
  if (!mw) {
    assert(true, `.observers-page has no narrow max-width cap`);
    continue;
  }
  const val = mw[1].trim();
  // Allowlist: none / unset / initial / a length >= 1600px.
  if (/^(none|unset|initial|revert)$/i.test(val)) {
    assert(true, `.observers-page max-width is ${val} (uncapped)`);
    continue;
  }
  const pxMatch = val.match(/^(\d+(?:\.\d+)?)px$/i);
  if (pxMatch) {
    const px = parseFloat(pxMatch[1]);
    assert(px >= 1600, `.observers-page max-width ${val} must be >= 1600px (matches .analytics-page convention) — see #1846`);
    continue;
  }
  // Non-px unit (e.g. %, vw) — accept, hard to reason about.
  assert(true, `.observers-page max-width is non-px (${val}) — accepted`);
}

console.log(`\n# ${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
