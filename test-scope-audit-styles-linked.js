/* Every class the Scope Audit page paints with must have a rule that can
 * actually apply to the element the page renders, in a stylesheet index.html
 * links.
 *
 * The page shipped referencing .ns-decl / .ns-empty / .ns-table, whose rules
 * live in node-scopes.css — a file that was never added, and never linked. The
 * page still rendered: correct data, correct DOM, no console error, and every
 * badge as unstyled text. Nothing in the suite noticed, because nothing
 * compared the classes the page writes against the CSS the page loads.
 *
 * "Can actually apply" is the part a substring check gets wrong. `.ns-truncated`
 * was mentioned in exactly one rule, `.ns-declared-meta .ns-truncated`, and this
 * page renders that span with no such ancestor — styled on paper, unstyled on
 * screen, which is the same bug one level down.
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
// page's own namespaces are checked; shared vocabulary (btn, active,
// text-muted) belongs to style.css and is not this page's to own.
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

function classRe(className) {
  return new RegExp('\\.' + className.replace(/-/g, '\\-') + '(?![a-zA-Z0-9_-])');
}

// selectorsFor returns every selector that targets className, comments
// stripped: a class named only inside a comment has no rule.
function selectorsFor(css, className) {
  const clean = css.replace(/\/\*[\s\S]*?\*\//g, '');
  const re = classRe(className);
  const out = [];
  for (const chunk of clean.split('}')) {
    const brace = chunk.indexOf('{');
    if (brace === -1) continue;
    for (const sel of chunk.slice(0, brace).split(',')) {
      const t = sel.trim();
      if (t && !t.startsWith('@') && re.test(t)) out.push(t);
    }
  }
  return out;
}

// requiredAncestors lists the classes a selector needs on an ANCESTOR before
// its own compound can match. ".a .b" requires a; ".a.b" requires nothing,
// since both sit on one element.
function requiredAncestors(selector, className) {
  const parts = selector.split(/\s*[>+~]\s*|\s+/).filter(Boolean);
  const own = parts.findIndex(p => classRe(className).test(p));
  if (own <= 0) return [];
  const need = [];
  for (const p of parts.slice(0, own)) {
    for (const m of p.match(/\.[a-zA-Z][a-zA-Z0-9_-]*/g) || []) need.push(m.slice(1));
  }
  return need;
}

// A class passes when some rule targets it with no ancestor requirement, or
// when every ancestor that rule requires is a class this page also emits.
function ruleApplies(css, className, emitted) {
  const sels = selectorsFor(css, className);
  if (!sels.length) return false;
  return sels.some(sel => requiredAncestors(sel, className).every(a => emitted.has(a)));
}

console.log('\n=== scope-audit: every class it paints with is in a linked stylesheet ===');

const html = fs.readFileSync('public/index.html', 'utf8');
const sheets = linkedStylesheets(html);
const css = sheets.map(p => (fs.existsSync(p) ? fs.readFileSync(p, 'utf8') : '')).join('\n');
const js = fs.readFileSync('public/scope-audit.js', 'utf8');

// Every class the page emits, in any namespace — the set an ancestor
// requirement is checked against.
const emitted = new Set(classesUsedBy(js, ['']));
// Checked classes are this page's own namespaces, minus tokens that cannot be
// a class name: a dynamically built prefix ('ns-decl-' + state) or a template
// fragment. Failing on those would say nothing about the page.
const used = classesUsedBy(js, ['ns-', 'sa-']).filter(c => /^[a-z][a-z0-9-]*[a-z0-9]$/.test(c));

test('index.html links the stylesheets it names', () => {
  const missing = sheets.filter(p => !fs.existsSync(p));
  assert.deepStrictEqual(missing, [], 'linked but absent from the tree: ' + missing.join(', '));
});

test('scope-audit.js uses a non-trivial number of its own classes', () => {
  assert.ok(used.length >= 10, 'expected the page to use its own vocabulary, found: ' + used.join(', '));
});

test('every ns-* / sa-* class it writes has a rule that can apply', () => {
  const unstyled = used.filter(c => !ruleApplies(css, c, emitted));
  assert.deepStrictEqual(unstyled, [], 'classes with no applicable rule in any linked stylesheet: ' + unstyled.join(', '));
});

// The checker itself, so a broken checker cannot pass the tree.
test('the checker rejects a rule gated on an ancestor the page never renders', () => {
  const emit = new Set(['sa-page', 'ns-truncated']);
  assert.strictEqual(ruleApplies('.ns-truncated { color: red }', 'ns-truncated', emit), true);
  assert.strictEqual(ruleApplies('.sa-page .ns-truncated { color: red }', 'ns-truncated', emit), true);
  assert.strictEqual(ruleApplies('.never-rendered .ns-truncated { color: red }', 'ns-truncated', emit), false);
  assert.strictEqual(ruleApplies('/* .ns-truncated lives elsewhere */', 'ns-truncated', emit), false);
  assert.strictEqual(ruleApplies('.ns-truncated-other { color: red }', 'ns-truncated', emit), false);
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
