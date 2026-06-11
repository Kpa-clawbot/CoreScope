#!/usr/bin/env node
/* Issue #1648 — M3: emoji → Phosphor sprite migration (static scan).
 *
 * M3 covers detail panes, status pills, role/payload-type badges:
 *   home.js, channels.js, route-view-utils.js, route-view.js,
 *   app.js, hop-display.js, path-inspector.js
 *
 * Asserts (per file):
 *   1. Zero UI-iconography codepoints (U+1F300–1FAFF, U+2600–27BF, and
 *      Misc-Symbols: ◆●■▲★☆○✓✗⚠✉) outside an allowlist of contexts that are
 *      not UI iconography (CSS comments, JS comments referencing prior
 *      glyphs, console.log/debug strings, and explicitly tagged
 *      // EMOJI-OK lines).
 *   2. At least N <use href="…#ph-… references where N is roughly the
 *      historical swap count.
 *
 * Anti-tautology: this test FAILS pre-implementation by construction.
 *
 * Also asserts status-token CSS pattern: .status-{ok,warn,err,muted} rules
 * exist in style.css and set color via var(--status-*).
 */
'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const ROOT = path.resolve(__dirname, 'public');

const M3_FILES = [
  'home.js',
  'channels.js',
  'route-view-utils.js',
  'route-view.js',
  'app.js',
  'hop-display.js',
  'path-inspector.js',
];

const MIN_USE_REFS = {
  'home.js': 12,
  'channels.js': 10,
  'route-view-utils.js': 6,
  'route-view.js': 3,
  'app.js': 4,
  'hop-display.js': 2,
  'path-inspector.js': 1,
};

const EMOJI = /[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/u;
const MISC_ICON = /[◆●■▲★☆○✓✗⚠✉]/u;

// Allowlist substrings: lines containing any of these are exempt from the
// scan.
const ALLOW_SUBSTRINGS = [
  'EMOJI-OK',
  // home.js: getting-started Q&A items are user-facing copy with emoji
  // baked into the *text* content (Discord link names, step labels).
  // They are not UI icons — they are part of the prose. Allowlist by the
  // EMOJI-OK tag on each retained line.
  // hop-display.js / channels.js / route-view*.js comments referring to
  // prior glyphs:
  '// alongside',
  '// renderer can label',
  '// Unresolved prefix',
  '// clicking',
  '// (the yellow',
  '// PATH_SYMBOLS_LEGEND',
  '// the ✕ disappears',
  '// userAdded so the',
];

function scanFile(rel) {
  const abs = path.join(ROOT, rel);
  const txt = fs.readFileSync(abs, 'utf8');
  const lines = txt.split('\n');
  const hits = [];
  lines.forEach((line, idx) => {
    if (ALLOW_SUBSTRINGS.some(s => line.includes(s))) return;
    const e = EMOJI.test(line);
    const m = MISC_ICON.test(line);
    if (e || m) {
      hits.push({ file: rel, line: idx + 1, kind: e && m ? 'both' : (e ? 'emoji' : 'misc'), text: line.trim().slice(0, 200) });
    }
  });
  return hits;
}

function countUseRefs(rel) {
  const abs = path.join(ROOT, rel);
  const txt = fs.readFileSync(abs, 'utf8');
  return (txt.match(/<use href="\/icons\/phosphor-sprite\.svg#ph-/g) || []).length;
}

function assertSpriteHasM3Icons() {
  const sp = path.join(ROOT, 'icons', 'phosphor-sprite.svg');
  const txt = fs.readFileSync(sp, 'utf8');
  const need = [
    'ph-rocket', 'ph-camera', 'ph-plant', 'ph-bluetooth',
    'ph-paper-plane-tilt', 'ph-shuffle', 'ph-hexagon',
  ];
  const missing = need.filter(id => !txt.includes(`id="${id}"`));
  if (missing.length) throw new Error(`sprite missing M3 symbols: ${missing.join(', ')}`);
}

function assertStatusTokenCss() {
  const css = fs.readFileSync(path.join(ROOT, 'style.css'), 'utf8');
  const needed = ['.status-ok', '.status-warn', '.status-err', '.status-muted'];
  const missing = needed.filter(sel => !new RegExp(sel.replace('.', '\\.') + '\\b').test(css));
  if (missing.length) throw new Error(`style.css missing status-token rules: ${missing.join(', ')}`);
  // Each must thread a --status-* var via color
  const block = css.match(/\.status-ok[\s\S]{0,400}\}/);
  if (!block || !/var\(--status-/.test(block[0])) {
    throw new Error('.status-ok rule must set color via var(--status-*)');
  }
}

function main() {
  let failed = 0;
  console.log('— Issue #1648 M3 — emoji/misc-icon scan');

  try {
    assertSpriteHasM3Icons();
    console.log('  ✓ sprite has required M3 symbols');
  } catch (e) {
    console.error(`  ✗ ${e.message}`);
    failed++;
  }

  try {
    assertStatusTokenCss();
    console.log('  ✓ style.css has .status-{ok,warn,err,muted} threading var(--status-*)');
  } catch (e) {
    console.error(`  ✗ ${e.message}`);
    failed++;
  }

  for (const rel of M3_FILES) {
    const hits = scanFile(rel);
    if (hits.length === 0) {
      console.log(`  ✓ ${rel} clean (no emoji / misc-icon iconography)`);
    } else {
      console.error(`  ✗ ${rel} has ${hits.length} emoji/misc-icon hit(s):`);
      for (const h of hits.slice(0, 30)) console.error(`      ${h.file}:${h.line} [${h.kind}] ${h.text}`);
      if (hits.length > 30) console.error(`      … (+${hits.length - 30} more)`);
      failed++;
    }
    const useRefs = countUseRefs(rel);
    const min = MIN_USE_REFS[rel] || 1;
    if (useRefs < min) {
      console.error(`  ✗ ${rel} has only ${useRefs} <use href="…#ph-…"> refs (expected ≥${min})`);
      failed++;
    } else {
      console.log(`  ✓ ${rel} has ${useRefs} Phosphor <use> refs (≥${min})`);
    }
    assert.strictEqual(hits.length, 0,
      `${rel} must contain zero emoji/misc-icon iconography (got ${hits.length} hit(s))`);
    assert.ok(useRefs >= min,
      `${rel} must have ≥${min} <use href="…#ph-…"> refs (got ${useRefs})`);
  }

  if (failed) {
    console.error(`\nFAIL: ${failed} M3 check(s) failed`);
    process.exit(1);
  }
  console.log('\nPASS: all M3 surfaces icon-free and Phosphor-swapped');
}

main();
