#!/usr/bin/env node
/* i18n-report.js — translation coverage for public/locales/*.json.
 *
 * Collects the English strings the frontend passes to tr()/trn()/_t() as
 * literals, plus data-i18n* attributes in public/*.html, and compares them
 * with each dictionary:
 *   missing  — used in code, no entry (shows in English)
 *   unused   — entry not found as a literal in code. Strings built at
 *              runtime (field-table names, config defaults, packet-filter
 *              descriptions) land here too, so treat this list as a hint.
 *
 * Usage:
 *   node scripts/i18n-report.js            summary per language
 *   node scripts/i18n-report.js pt-BR      also list missing/unused keys
 *   node scripts/i18n-report.js --json     machine-readable output
 */
'use strict';
const fs = require('fs');
const path = require('path');

const ROOT = path.join(__dirname, '..');
const PUBLIC = path.join(ROOT, 'public');
const LOCALES = path.join(PUBLIC, 'locales');

// A JS string literal: '...', "..." or `...` without ${}.
const STR = String.raw`('(?:[^'\\\n]|\\.)*'|"(?:[^"\\\n]|\\.)*"|` + '`(?:[^`\\\\$]|\\\\.)*`)';
const CALL = new RegExp(String.raw`\b(?:tr|_t)\(\s*` + STR, 'g');
const PLURAL = new RegExp(String.raw`\btrn\([^,()]+(?:\([^()]*\))?[^,()]*,\s*` + STR + String.raw`\s*,\s*` + STR, 'g');
const ATTR = /data-i18n(?:-title|-aria-label|-placeholder)?="([^"]*)"/g;

function unquote(lit) {
  // eslint-disable-next-line no-new-func
  return Function('"use strict"; return (' + lit + ');')();
}
function decodeEntities(s) {
  return s.replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');
}

function collect() {
  const keys = new Map(); // key -> Set(files)
  const add = (k, file) => {
    if (!keys.has(k)) keys.set(k, new Set());
    keys.get(k).add(file);
  };
  for (const f of fs.readdirSync(PUBLIC)) {
    const full = path.join(PUBLIC, f);
    if (!fs.statSync(full).isFile()) continue;
    const src = fs.readFileSync(full, 'utf8');
    if (f.endsWith('.js') && f !== 'i18n.js') { // i18n.js only holds examples in comments
      for (const m of src.matchAll(CALL)) add(unquote(m[1]), f);
      // A plural entry is keyed by its English plural form.
      for (const m of src.matchAll(PLURAL)) add(unquote(m[2]), f);
    }
    if (f.endsWith('.html')) {
      for (const m of src.matchAll(ATTR)) add(decodeEntities(m[1]), f);
    }
  }
  return keys;
}

function report(code, used) {
  const dict = JSON.parse(fs.readFileSync(path.join(LOCALES, code + '.json'), 'utf8'));
  const has = (k) => Object.prototype.hasOwnProperty.call(dict, k) && dict[k] !== '';
  const missing = [...used.keys()].filter(k => !has(k)).sort();
  const unused = Object.keys(dict).filter(k => !used.has(k)).sort();
  return { code, used: used.size, translated: used.size - missing.length, missing, unused };
}

const args = process.argv.slice(2);
const json = args.includes('--json');
const only = args.find(a => !a.startsWith('--'));
const used = collect();
const codes = fs.readdirSync(LOCALES).filter(f => f.endsWith('.json')).map(f => f.slice(0, -5))
  .filter(c => !only || c === only);
const results = codes.map(c => report(c, used));

if (json) {
  console.log(JSON.stringify(results, null, 2));
} else {
  for (const r of results) {
    const pct = r.used ? Math.round(r.translated / r.used * 100) : 100;
    console.log(`${r.code}: ${r.translated}/${r.used} strings in code translated (${pct}%), ` +
      `${r.missing.length} missing, ${r.unused.length} not found as literals`);
    if (only) {
      if (r.missing.length) console.log('\nMissing:\n' + r.missing.map(k => '  ' + JSON.stringify(k) + '  [' + [...used.get(k)].join(', ') + ']').join('\n'));
      if (r.unused.length) console.log('\nNot found as literals (dynamic or stale):\n' + r.unused.map(k => '  ' + JSON.stringify(k)).join('\n'));
    }
  }
}
