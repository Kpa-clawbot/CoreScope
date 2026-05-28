/**
 * #1450 — Custom navbar logo aspect ratio preservation.
 *
 * The default inline-SVG wordmark has a fixed 125x36 box matching its
 * viewBox; pinning it via `.brand-logo { width: 125px }` was correct for
 * the SVG. When the operator sets `branding.logoUrl`, customize-v2 swaps
 * the inline <svg> for an <img class="brand-logo"> — that <img> then
 * inherits the same `width: 125px` (plus hardcoded width/height attrs),
 * stretching every non-3.08:1 image into a pill shape.
 *
 * Fix: split the CSS rule so `svg.brand-logo` keeps the 125px pin, but
 * `img.brand-logo` uses `width: auto` (with a `max-width` cap) so the
 * natural aspect ratio of operator-provided images is preserved. Drop
 * the hardcoded width/height attrs from the IMG element created in
 * customize-v2 _setBrandLogoUrl.
 *
 * Pure-Node, no browser. Parses style.css + greps customize-v2.js.
 */
'use strict';

const fs = require('fs');
const path = require('path');

const ROOT = __dirname;
const CSS = fs.readFileSync(path.join(ROOT, 'public/style.css'), 'utf8');
const CUSTOMIZE = fs.readFileSync(path.join(ROOT, 'public/customize-v2.js'), 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

// Extract bodies of ALL CSS rules whose selector matches `sel` (literal)
// at brace-depth 0 (i.e. NOT inside an @media or other at-rule). Returns
// an array of declaration-body strings.
function findTopLevelRules(css, sel) {
  const out = [];
  let depth = 0;
  let i = 0;
  while (i < css.length) {
    const ch = css[i];
    if (ch === '{') { depth++; i++; continue; }
    if (ch === '}') { depth--; i++; continue; }
    if (depth === 0 && ch === sel[0]) {
      // Try to match selector at this position
      if (css.startsWith(sel, i)) {
        // Make sure prev char is whitespace/newline/, /} to avoid mid-token matches
        const prev = i > 0 ? css[i - 1] : '\n';
        if (/[\s},]/.test(prev)) {
          // Find next `{`
          const open = css.indexOf('{', i + sel.length);
          if (open > 0) {
            const close = css.indexOf('}', open);
            if (close > open) {
              out.push(css.slice(open + 1, close));
              i = close + 1;
              continue;
            }
          }
        }
      }
    }
    i++;
  }
  return out;
}

function findTopLevelRule(css, sel) {
  const all = findTopLevelRules(css, sel);
  return all.length ? all[0] : null;
}

console.log('── #1450 logo aspect ratio ──');

test('img.brand-logo CSS rule exists and uses width:auto (not pinned)', () => {
  const body = findTopLevelRule(CSS, 'img.brand-logo');
  assert(body, 'expected a top-level `img.brand-logo` CSS rule');
  assert(/width\s*:\s*auto/i.test(body),
    'expected `width: auto` in img.brand-logo (got: ' + body.trim().replace(/\s+/g, ' ') + ')');
  assert(/max-width\s*:\s*\d+px/i.test(body),
    'expected a `max-width: <N>px` cap on img.brand-logo to prevent very-wide logos blowing nav layout');
  assert(/height\s*:\s*36px/i.test(body),
    'expected `height: 36px` on img.brand-logo (matches default SVG height)');
});

test('svg.brand-logo CSS rule still pins width:125px (no default regression)', () => {
  const body = findTopLevelRule(CSS, 'svg.brand-logo');
  assert(body, 'expected a top-level `svg.brand-logo` CSS rule keeping the default-wordmark pin');
  assert(/width\s*:\s*125px/i.test(body),
    'svg.brand-logo MUST keep width: 125px to preserve the default wordmark layout');
  assert(/height\s*:\s*36px/i.test(body),
    'svg.brand-logo MUST keep height: 36px');
});

test('mobile media-query splits the .brand-logo rule into svg/img variants', () => {
  // Find the @media block(s) and look for split rules.
  // We just require BOTH `svg.brand-logo` and `img.brand-logo` to appear
  // somewhere inside an @media block, AND the bare `.brand-logo { height
  // ... width: <px> }` form to NOT exist in tablet/mobile breakpoints
  // (since that pinned width is what we're getting away from for IMG).
  const mediaIdx = CSS.indexOf('@media');
  assert(mediaIdx > 0, 'expected @media blocks in style.css');
  const tail = CSS.slice(mediaIdx);
  assert(/svg\.brand-logo\s*\{[^}]*width\s*:\s*\d+px/i.test(tail),
    'expected `svg.brand-logo { ... width:<N>px }` inside a media query (tablet pin)');
  assert(/img\.brand-logo\s*\{[^}]*width\s*:\s*auto/i.test(tail),
    'expected `img.brand-logo { ... width:auto }` inside a media query (tablet IMG variant)');
});

test('customize-v2 _setBrandLogoUrl does NOT hardcode width/height attrs on the IMG', () => {
  // The fix removes these two setAttribute calls so CSS img.brand-logo
  // governs sizing without overriding aspect.
  assert(!/img\.setAttribute\(\s*['"]width['"]\s*,\s*['"]125['"]\s*\)/.test(CUSTOMIZE),
    'customize-v2.js still sets img width="125" — that overrides the CSS width:auto and squishes custom logos');
  assert(!/img\.setAttribute\(\s*['"]height['"]\s*,\s*['"]36['"]\s*\)/.test(CUSTOMIZE),
    'customize-v2.js still sets img height="36" — CSS height:36px is sufficient and the attr blocks aspect math when only one dim is constrained');
});

console.log('\n' + passed + ' passed, ' + failed + ' failed');
process.exit(failed === 0 ? 0 : 1);
