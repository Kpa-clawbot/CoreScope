/* Every class the Scope Audit page paints with must have a rule in a
 * stylesheet index.html actually links.
 *
 * The page shipped referencing .ns-decl / .ns-empty / .ns-table, whose rules
 * live in node-scopes.css — a file that was never added, and never linked. The
 * page still rendered: correct data, correct DOM, no console error, and every
 * badge as unstyled text. Nothing in the suite noticed, because nothing
 * compared the classes the page writes against the CSS the page loads.
 *
 * Deliberately not a whitelist of known-good class names: that would need
 * updating every time the page grows a class, which is exactly the moment this
 * check has to fire.
 */
'use strict';
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// linkedStylesheets returns the local stylesheet paths index.html loads, in
// link order. Vendor CSS is included: it is loaded too, and excluding it would
// be a guess about where a rule is allowed to live.
function linkedStylesheets(html) {
  const out = [];
  const re = /<link[^>]+rel=["']stylesheet["'][^>]*>/g;
  let m;
  while ((m = re.exec(html))) {
    const href = /href=["']([^"']+)["']/.exec(m[0]);
    if (!href) continue;
    const path = href[1].split('?')[0];
    if (/^https?:/.test(path)) continue; // remote sheets are not ours to check
    out.push('public/' + path);
  }
  return out;
}

// classesUsedBy pulls class names out of the class="..." literals a page
// script writes, plus the ' class-name' fragments it concatenates. Only the
// page's own namespaces are checked; shared vocabulary (btn, active, text-
// muted) belongs to style.css and is not this page's to own.
function classesUsedBy(js, prefixes) {
  const found = new Set();
  const re = /class="([^"]*)"|'\s*([a-z][a-z0-9-]*)'/g;
  let m;
  while ((m = re.exec(js))) {
    const chunk = m[1] || m[2] || '';
    for (const raw of chunk.split(/[\s'"+]+/)) {
      const name = raw.trim();
      if (!name) continue;
      if (prefixes.some(p => name.startsWith(p))) found.add(name);
    }
  }
  return [...found].sort();
}

function ruleExists(css, className) {
  // A rule for .foo, allowing .foo.bar, .foo:hover, .a .foo, .foo, .b { ... }
  return new RegExp('\\.' + className.replace(/-/g, '\\-') + '(?![a-zA-Z0-9_-])').test(
    css.replace(/\/\*[\s\S]*?\*\//g, '') // comments never count as a rule
  );
}

console.log('\n=== scope-audit: every class it paints with is in a linked stylesheet ===');

const html = fs.readFileSync('public/index.html', 'utf8');
const sheets = linkedStylesheets(html);
const css = sheets.map(p => (fs.existsSync(p) ? fs.readFileSync(p, 'utf8') : '')).join('\n');
const js = fs.readFileSync('public/scope-audit.js', 'utf8');
const used = classesUsedBy(js, ['ns-', 'sa-']);

test('index.html links the stylesheets it names', () => {
  const missing = sheets.filter(p => !fs.existsSync(p));
  assert.deepStrictEqual(missing, [], 'linked but absent from the tree: ' + missing.join(', '));
});

test('scope-audit.js uses a non-trivial number of its own classes', () => {
  assert.ok(used.length >= 10, 'expected the page to use its own vocabulary, found: ' + used.join(', '));
});

test('every ns-* / sa-* class it writes has a rule in a linked stylesheet', () => {
  const unstyled = used.filter(c => !ruleExists(css, c));
  assert.deepStrictEqual(unstyled, [], 'classes with no rule in any linked stylesheet: ' + unstyled.join(', '));
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
