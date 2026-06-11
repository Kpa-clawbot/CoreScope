#!/usr/bin/env node
/* Issue #1648 — M1: emoji → Phosphor sprite migration (unit/static check).
 *
 * Asserts that the M1 surfaces (top-nav, bottom-nav, nav-drawer,
 * mobile-page-actions, observers Compare entries) contain ZERO emoji or
 * misc-symbol codepoints used as iconography:
 *   - U+1F300–U+1FAFF  (Symbols & Pictographs / Supplemental, Symbols & Pictographs)
 *   - U+2600–U+27BF    (Misc Symbols, Dingbats)
 *   - Misc-iconography set: ◆●■▲★☆○✓✗⚠✉
 *
 * Allowlist: comments and string-literal text NOT used as UI iconography
 * are still stripped out where they exist as plain code-comments — the
 * grep operates on the WHOLE file but only the M1-listed lines are required
 * to be clean. We assert per-symbol on lines tagged in the migration plan.
 *
 * Anti-tautology: this test FAILS today (pre-fix) by construction —
 * each named line currently contains the offending codepoint.
 */
'use strict';

const fs = require('fs');
const path = require('path');

const ROOT = path.resolve(__dirname, 'public');

// File-by-file expected codepoint-free regions.
// We scan the WHOLE file for nav/UI emoji rather than specific lines so
// the assertion stays robust to line-number drift.
const FILES = [
  'index.html',
  'bottom-nav.js',
  'nav-drawer.js',
  'mobile-page-actions.js',
  'observers.js',
  'observer-detail.js',
];

// Codepoint ranges (emoji proper).
const EMOJI = /[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/u;
// Misc-symbols used as iconography (per #1648 surprise #5).
// EXCLUDES standard quotes/dashes; INCLUDES box-drawing role shapes + check/cross/warn.
const MISC_ICON = /[◆●■▲★☆○✓✗⚠✉]/u;

// Per-file: lines exempt from the misc-icon scan because they are plain
// code-comments / sync-warning text rather than UI iconography. Keep tiny.
const COMMENT_EXEMPT_SUBSTRINGS = [
  'MANUAL SYNC REQUIRED',
  'Keep in sync with',
  'naive-clock',
];

function scanFile(rel) {
  const abs = path.join(ROOT, rel);
  const txt = fs.readFileSync(abs, 'utf8');
  const lines = txt.split('\n');
  const hits = [];
  lines.forEach((line, idx) => {
    if (EMOJI.test(line)) {
      hits.push({ file: rel, line: idx + 1, kind: 'emoji', text: line.trim().slice(0, 200) });
    }
    if (MISC_ICON.test(line)) {
      const isExemptComment = COMMENT_EXEMPT_SUBSTRINGS.some(s => line.includes(s));
      if (!isExemptComment) {
        hits.push({ file: rel, line: idx + 1, kind: 'misc', text: line.trim().slice(0, 200) });
      }
    }
  });
  return hits;
}

function assertSpriteWired() {
  // index.html must reference the Phosphor sprite (either inline <symbol id="ph-…"
  // or via <use href="…phosphor-sprite.svg#ph-…">).
  const idx = fs.readFileSync(path.join(ROOT, 'index.html'), 'utf8');
  const ok = /phosphor-sprite\.svg#ph-/.test(idx) || /<symbol id="ph-/.test(idx);
  if (!ok) {
    throw new Error('index.html does not reference the Phosphor sprite (expected phosphor-sprite.svg#ph-… or inline <symbol id="ph-…">)');
  }
}

function assertSpriteFilePresent() {
  const sp = path.join(ROOT, 'icons', 'phosphor-sprite.svg');
  if (!fs.existsSync(sp)) throw new Error(`Phosphor sprite missing at public/icons/phosphor-sprite.svg`);
  const txt = fs.readFileSync(sp, 'utf8');
  // Must contain at least the M1 icons.
  const required = [
    'ph-broadcast', 'ph-lightning', 'ph-music-note', 'ph-magnifying-glass',
    'ph-palette', 'ph-sun', 'ph-moon', 'ph-list', 'ph-house', 'ph-package',
    'ph-map-trifold', 'ph-chat-circle', 'ph-monitor', 'ph-wrench', 'ph-eye',
    'ph-chart-bar', 'ph-scales', 'ph-arrow-clockwise', 'ph-circle-fill',
    'ph-triangle', 'ph-x', 'ph-warning',
  ];
  const missing = required.filter(id => !txt.includes(`id="${id}"`));
  if (missing.length) throw new Error(`sprite missing symbols: ${missing.join(', ')}`);
}

function main() {
  let failed = 0;
  console.log('— Issue #1648 M1 — emoji/misc-icon scan');

  try {
    assertSpriteFilePresent();
    console.log('  ✓ sprite file present + has required symbols');
  } catch (e) {
    console.error(`  ✗ ${e.message}`);
    failed++;
  }

  try {
    assertSpriteWired();
    console.log('  ✓ index.html references Phosphor sprite');
  } catch (e) {
    console.error(`  ✗ ${e.message}`);
    failed++;
  }

  for (const rel of FILES) {
    const hits = scanFile(rel);
    if (hits.length === 0) {
      console.log(`  ✓ ${rel} clean (no emoji / misc-icon codepoints)`);
    } else {
      console.error(`  ✗ ${rel} has ${hits.length} emoji/misc-icon hit(s):`);
      for (const h of hits) console.error(`      ${h.file}:${h.line} [${h.kind}] ${h.text}`);
      failed++;
    }
  }

  if (failed) {
    console.error(`\nFAIL: ${failed} file(s) still contain emoji/misc-icon iconography`);
    process.exit(1);
  }
  console.log('\nPASS: all M1 surfaces icon-free');
}

main();
