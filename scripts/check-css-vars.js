#!/usr/bin/env node
/*
 * scripts/check-css-vars.js — issue #1128 (audit Section 5 #1)
 *
 * Walks every public/*.css file and asserts that every `var(--name)`
 * reference WITHOUT a fallback resolves to a `--name:` definition in
 * SOME public/*.css file. (References WITH a fallback like
 * `var(--maybe, var(--always))` are tolerated; the fallback chain
 * keeps them safe.)
 *
 * Catches regressions like the original Bug 4 in #1128, where
 * `background: var(--surface)` was shipped with `--surface` undefined,
 * leaving 8 dropdowns/popovers transparent.
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
const files = fs.readdirSync(dir).filter(f => f.endsWith('.css')).map(f => path.join(dir, f));
if (!files.length) {
  console.error('check-css-vars: no .css files found in ' + dir);
  process.exit(2);
}

const defined = new Set();
const uses = []; // { file, line, name }

const defRe = /(?:^|[^a-zA-Z0-9_-])(--[a-zA-Z0-9_-]+)\s*:/g;
// Match var(--name) ONLY when the closing ')' immediately follows the name
// (optional whitespace). Anything else (a comma → fallback) is exempt.
const useRe = /var\(\s*(--[a-zA-Z0-9_-]+)\s*\)/g;

for (const f of files) {
  // Strip /* ... */ comments before scanning so doc-blocks that mention
  // var(--name) as prose don't trigger false positives. Replace each
  // comment span with newlines to keep line numbers stable for any
  // genuine offender that follows.
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

const undef = uses.filter(u => !defined.has(u.name));
if (undef.length) {
  console.error('check-css-vars: ' + undef.length + ' undefined CSS variable reference(s):');
  for (const u of undef) console.error('  ' + u.file + ':' + u.line + '  var(' + u.name + ')');
  console.error('Fix: define the variable in :root, or use var(' + undef[0].name + ', <fallback>).');
  process.exit(1);
}
console.log('check-css-vars: OK — ' + uses.length + ' var() refs, ' + defined.size + ' definitions, 0 undefined.');
