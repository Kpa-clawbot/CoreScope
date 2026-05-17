#!/usr/bin/env node
/*
 * scripts/check-css-vars.js — issue #1128 (audit Section 5 #1)
 *
 * Walks every public/*.css file (definitions + uses) AND every
 * public/**\/*.{js,html} file (uses only) and asserts that every
 * `var(--name)` reference WITHOUT a fallback resolves to a `--name:`
 * definition in SOME public/*.css file.
 *
 * Why JS/HTML are scanned: the original Bug 4 in #1128 came from
 * filter-ux.js shipping `style="background: var(--surface)"` while
 * --surface was undefined. CSS-only scanning misses inline styles
 * embedded in JS template literals and HTML attributes.
 *
 * References WITH a fallback like `var(--maybe, var(--always))` are
 * tolerated; the fallback chain keeps them safe. Definitions still
 * only come from CSS files (JS/HTML cannot define custom properties
 * without runtime parsing we do not attempt).
 *
 * Exit code: 0 = clean, 1 = one or more undefined vars (with locations).
 *
 * Usage:
 *   node scripts/check-css-vars.js          # default (lints public/)
 *   node scripts/check-css-vars.js --dir x  # lint a different directory
 */
'use strict';
const fs = require('fs');
const path = require('path');

let dir = 'public';
for (let i = 2; i < process.argv.length; i++) {
  if (process.argv[i] === '--dir' && process.argv[i + 1]) { dir = process.argv[++i]; }
}

if (!fs.existsSync(dir)) {
  console.error('check-css-vars: directory not found: ' + dir);
  process.exit(2);
}

// Recursively walk dir, returning files matching one of the given extensions.
// Skips node_modules and any vendor/ directory by name to keep the lint fast
// and focused on first-party code.
const SKIP_DIRS = new Set(['node_modules', 'vendor', '.git']);
function walk(root, exts) {
  const out = [];
  const stack = [root];
  while (stack.length) {
    const cur = stack.pop();
    let entries;
    try { entries = fs.readdirSync(cur, { withFileTypes: true }); }
    catch (e) { continue; }
    for (const ent of entries) {
      const full = path.join(cur, ent.name);
      if (ent.isDirectory()) {
        if (SKIP_DIRS.has(ent.name)) continue;
        stack.push(full);
      } else if (ent.isFile()) {
        const ext = path.extname(ent.name).toLowerCase();
        if (exts.includes(ext)) out.push(full);
      }
    }
  }
  return out;
}

const cssFiles = walk(dir, ['.css']);
const codeFiles = walk(dir, ['.js', '.html', '.htm']);

if (!cssFiles.length) {
  console.error('check-css-vars: no .css files found in ' + dir);
  process.exit(2);
}

const defined = new Set();
const uses = []; // { file, line, name }

const defRe = /(?:^|[^a-zA-Z0-9_-])(--[a-zA-Z0-9_-]+)\s*:/g;
// Match var(--name) ONLY when the closing ')' immediately follows the name
// (optional whitespace). Anything else (a comma → fallback) is exempt.
const useRe = /var\(\s*(--[a-zA-Z0-9_-]+)\s*\)/g;

function scanCss(f) {
  // Strip /* ... */ comments before scanning so doc-blocks that mention
  // var(--name) as prose don't trigger false positives. Replace each
  // comment span with whitespace to keep line numbers stable.
  const raw = fs.readFileSync(f, 'utf8');
  const stripped = raw.replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '));
  const lines = stripped.split('\n');
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    let m;
    defRe.lastIndex = 0;
    while ((m = defRe.exec(line)) !== null) defined.add(m[1]);
    useRe.lastIndex = 0;
    while ((m = useRe.exec(line)) !== null) uses.push({ file: f, line: i + 1, name: m[1] });
  }
}

function scanCode(f) {
  // For JS / HTML we collect USES only. We also strip /* */ and //
  // line comments (JS) and <!-- --> (HTML) so doc prose mentioning
  // var(--name) doesn't false-positive. Definitions in JS/HTML are
  // not supported (would require runtime parsing).
  let raw = fs.readFileSync(f, 'utf8');
  raw = raw.replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '));
  raw = raw.replace(/<!--[\s\S]*?-->/g, (m) => m.replace(/[^\n]/g, ' '));
  // Strip // line comments (best-effort; harmless if it nicks a URL since
  // we only care about var(--…) tokens that follow on the same line).
  raw = raw.replace(/(^|[^:])\/\/[^\n]*/g, (m, p1) => p1 + ' '.repeat(m.length - p1.length));
  const lines = raw.split('\n');
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    let m;
    useRe.lastIndex = 0;
    while ((m = useRe.exec(line)) !== null) uses.push({ file: f, line: i + 1, name: m[1] });
  }
}

for (const f of cssFiles) scanCss(f);
for (const f of codeFiles) scanCode(f);

const undef = uses.filter(u => !defined.has(u.name));
if (undef.length) {
  console.error('check-css-vars: ' + undef.length + ' undefined CSS variable reference(s):');
  for (const u of undef) console.error('  ' + u.file + ':' + u.line + '  var(' + u.name + ')');
  console.error('Fix: define the variable in :root, or use var(' + undef[0].name + ', <fallback>).');
  process.exit(1);
}
console.log('check-css-vars: OK — ' + uses.length + ' var() refs across ' +
  (cssFiles.length + codeFiles.length) + ' files (' + cssFiles.length + ' css, ' +
  codeFiles.length + ' js/html), ' + defined.size + ' definitions, 0 undefined.');
