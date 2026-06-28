/**
 * #1770 — Packets view jitters on mobile because variable row heights
 * break the virtual-scroller's constant-row-height assumption.
 *
 * S quick-fix path: under the mobile breakpoint, clamp .col-details
 * (the packet "Details" cell in the packets table) to a single line via
 *   white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
 * The base rule at line ~1097 currently sets `white-space: normal` which
 * allows wrapping → variable row heights → virtual-scroll jitter.
 *
 * This is a CSS-grep test: it parses public/style.css, locates the
 * `@media (max-width: 640px)` block used by the packets view on mobile,
 * and asserts that block declares a `.col-details` clamp rule with
 * `white-space: nowrap`. Cheap to run, no browser required.
 *
 * Pattern borrowed from test-issue-1364-pill-no-clamp.js.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const cssSrc = fs.readFileSync(path.join(__dirname, 'public', 'style.css'), 'utf8');

// Pull the existing mobile breakpoint block. The packets-table mobile
// overrides (.data-table font-size, .data-table td max-width, etc.) live
// inside `@media (max-width: 640px)` at ~line 2361.
// Pull every `@media (max-width: 640px)` block. There are several in
// style.css (the packets-view block is at ~L2362 but many feature
// sections add their own); the clamp can live in any of them.
function extractAllMediaBlocks(src, header) {
  const out = [];
  let from = 0;
  while (true) {
    const idx = src.indexOf(header, from);
    if (idx === -1) break;
    let depth = 0, started = false, end = -1;
    for (let i = idx; i < src.length; i++) {
      const c = src[i];
      if (c === '{') { depth++; started = true; }
      else if (c === '}') {
        depth--;
        if (started && depth === 0) { end = i + 1; break; }
      }
    }
    if (end === -1) break;
    out.push(src.slice(idx, end));
    from = end;
  }
  return out;
}

const mobileBlocks = extractAllMediaBlocks(cssSrc, '@media (max-width: 640px)');

console.log('\n=== #1770: .col-details clamps to one line under mobile breakpoint ===');

assert(mobileBlocks.length > 0, '`@media (max-width: 640px)` block(s) found in style.css');

if (mobileBlocks.length > 0) {
  // Find the .col-details rule body inside any mobile block. Accepts any
  // selector that includes `.col-details` (e.g. `.col-details { ... }` or
  // `.data-table td.col-details { ... }` or — post-#1805 — an inner
  // `.col-details > .col-details-clip` wrapper). The clamp invariant is
  // what matters; the selector scope was tightened in #1805 to avoid
  // blowing past `max-width` and breaking row-action gestures (#1062).
  const ruleRe = /([^{}]*\.col-details\b[^{}]*)\{([^{}]*)\}/g;
  let clampBody = null;
  for (const block of mobileBlocks) {
    let m;
    ruleRe.lastIndex = 0;
    while ((m = ruleRe.exec(block)) !== null) {
      if (/white-space\s*:\s*nowrap/.test(m[2])) { clampBody = m[2]; break; }
    }
    if (clampBody) break;
  }

  assert(
    !!clampBody,
    '.col-details mobile rule declares `white-space: nowrap` (single-line clamp)'
  );

  if (clampBody) {
    assert(
      /overflow\s*:\s*hidden/.test(clampBody),
      '.col-details mobile rule declares `overflow: hidden`'
    );
    assert(
      /text-overflow\s*:\s*ellipsis/.test(clampBody),
      '.col-details mobile rule declares `text-overflow: ellipsis`'
    );
  }
}

console.log('\n=== Summary ===');
console.log('  Passed: ' + passed);
console.log('  Failed: ' + failed);
console.log('\n#1770 ' + (failed === 0 ? 'PASS' : 'FAIL'));
process.exit(failed === 0 ? 0 : 1);
