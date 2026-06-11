#!/usr/bin/env node
/* Issue #1648 — M5: emoji → Phosphor sprite migration (static scan).
 *
 * M5 covers settings, customize, modals, misc surfaces:
 *   customize.js, customize-v2.js (config DEFAULTS + UI controls — must
 *   keep emoji branch in render path for back-compat with operator
 *   stored values),
 *   geofilter-builder.html, audio.js, filter-ux.js, style.css ::before
 *   glyph, cmd/server/routes.go (lockstep with customize-v2.js defaults).
 *
 * Asserts (per file):
 *   1. Zero UI-iconography codepoints outside an allowlist
 *      (// EMOJI-OK / EMOJI-OK-LEGACY-RENDER / EMOJI-OK-COMMENT lines).
 *      For customize.js / customize-v2.js, the LEGACY-RENDER allowlist
 *      flags the back-compat branch of renderConfigGlyph() that MUST
 *      still accept operator-stored emoji values at runtime (design
 *      call #1 from #1648 M5).
 *   2. The in-source DEFAULTS for customize.js / customize-v2.js use
 *      `ph:<name>` tokens (asserted by grep for known keys).
 *   3. ≥N <use href="...#ph-..."> refs per file (sanity floor).
 *
 * Anti-tautology: this test FAILS pre-implementation by construction —
 * every M5 file has at least one icon emoji or misc-symbol today.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const ROOT = path.resolve(__dirname);
const PUB = path.join(ROOT, 'public');

const M5_FILES = [
  'public/customize.js',
  'public/customize-v2.js',
  'public/geofilter-builder.html',
  'public/audio.js',
  'public/filter-ux.js',
  'public/style.css',
  'cmd/server/routes.go',
];

const MIN_USE_REFS = {
  'public/customize.js': 4,
  'public/customize-v2.js': 4,
  'public/geofilter-builder.html': 3,
  'public/audio.js': 1,
  'public/filter-ux.js': 2,   // M2 already added some
  'public/style.css': 0,      // CSS uses background-image SVG data-URI; not a literal <use>
  'cmd/server/routes.go': 0,
};

const EMOJI = /[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/u;
const MISC_ICON = /[◆●■▲★☆○✓✗⚠✉✕]/u;

const ALLOW_TOKENS = ['EMOJI-OK', 'EMOJI-OK-LEGACY-RENDER', 'EMOJI-OK-COMMENT'];

function scanFile(rel) {
  const abs = path.join(ROOT, rel);
  const txt = fs.readFileSync(abs, 'utf8');
  const lines = txt.split('\n');
  const hits = [];
  lines.forEach((line, idx) => {
    if (ALLOW_TOKENS.some(t => line.includes(t))) return;
    if (EMOJI.test(line) || MISC_ICON.test(line)) {
      hits.push({ file: rel, line: idx + 1, text: line.trim().slice(0, 200) });
    }
  });
  return hits;
}

function countUseRefs(rel) {
  const abs = path.join(ROOT, rel);
  const txt = fs.readFileSync(abs, 'utf8');
  return (txt.match(/<use href="\/icons\/phosphor-sprite\.svg#ph-/g) || []).length;
}

function assertSpriteHasM5Icons() {
  const sp = path.join(PUB, 'icons', 'phosphor-sprite.svg');
  const txt = fs.readFileSync(sp, 'utf8');
  const need = [
    'ph-tag', 'ph-trash', 'ph-floppy-disk', 'ph-folder-open',
    'ph-download-simple', 'ph-x', 'ph-check', 'ph-warning',
    'ph-speaker-high', 'ph-star', 'ph-bluetooth',
  ];
  const missing = need.filter(id => !txt.includes(`id="${id}"`));
  if (missing.length) throw new Error(`sprite missing M5 symbols: ${missing.join(', ')}`);
}

function assertCustomizeDefaultsUsePhTokens() {
  // Both files must store sprite tokens (ph:<name>) in DEFAULTS, not emoji.
  // The string `ph:` should appear in the NODE_EMOJI / TYPE_EMOJI / steps /
  // footerLinks DEFAULTS region of both files. We assert by literal-count.
  for (const rel of ['public/customize.js', 'public/customize-v2.js']) {
    const txt = fs.readFileSync(path.join(ROOT, rel), 'utf8');
    const phTokens = (txt.match(/['"]ph:[a-z-]+['"]/g) || []).length;
    assert.ok(phTokens >= 10,
      `${rel} must have ≥10 'ph:<name>' DEFAULT tokens (got ${phTokens})`);
    assert.ok(/renderConfigGlyph\s*\(/.test(txt),
      `${rel} must define a renderConfigGlyph() helper (back-compat for legacy emoji values)`);
  }
}

function assertRoutesGoUsesPhTokens() {
  const txt = fs.readFileSync(path.join(ROOT, 'cmd/server/routes.go'), 'utf8');
  // Onboarding home defaults must echo Phosphor tokens, not raw emoji.
  const phTokens = (txt.match(/"ph:[a-z-]+"/g) || []).length;
  assert.ok(phTokens >= 6,
    `cmd/server/routes.go must echo ≥6 'ph:<name>' tokens in home defaults (got ${phTokens})`);
}

function main() {
  let failed = 0;
  console.log('— Issue #1648 M5 — emoji/misc-icon scan');

  try { assertSpriteHasM5Icons(); console.log('  ✓ sprite has required M5 symbols'); }
  catch (e) { console.error(`  ✗ ${e.message}`); failed++; }

  try { assertCustomizeDefaultsUsePhTokens(); console.log('  ✓ customize.js + customize-v2.js DEFAULTS use ph: tokens'); }
  catch (e) { console.error(`  ✗ ${e.message}`); failed++; }

  try { assertRoutesGoUsesPhTokens(); console.log('  ✓ cmd/server/routes.go home defaults use ph: tokens'); }
  catch (e) { console.error(`  ✗ ${e.message}`); failed++; }

  for (const rel of M5_FILES) {
    const hits = scanFile(rel);
    if (hits.length === 0) {
      console.log(`  ✓ ${rel} clean`);
    } else {
      console.error(`  ✗ ${rel} has ${hits.length} emoji/misc-icon hit(s):`);
      for (const h of hits.slice(0, 30)) console.error(`      ${h.file}:${h.line} ${h.text}`);
      if (hits.length > 30) console.error(`      … (+${hits.length - 30} more)`);
      failed++;
    }
    const min = MIN_USE_REFS[rel] || 0;
    if (min > 0) {
      const useRefs = countUseRefs(rel);
      if (useRefs < min) {
        console.error(`  ✗ ${rel} has only ${useRefs} <use href="…#ph-…"> refs (expected ≥${min})`);
        failed++;
      } else {
        console.log(`  ✓ ${rel} has ${useRefs} Phosphor <use> refs (≥${min})`);
      }
    }
  }

  if (failed) {
    console.error(`\nFAIL: ${failed} M5 check(s) failed`);
    process.exit(1);
  }

  // Hard asserts for CI
  for (const rel of M5_FILES) {
    const hits = scanFile(rel);
    assert.strictEqual(hits.length, 0,
      `${rel} must contain zero emoji/misc-icon iconography (got ${hits.length} hit(s))`);
    const min = MIN_USE_REFS[rel] || 0;
    if (min > 0) {
      const useRefs = countUseRefs(rel);
      assert.ok(useRefs >= min,
        `${rel} must have ≥${min} <use href="…#ph-…"> refs (got ${useRefs})`);
    }
  }
  console.log('\nPASS: all M5 surfaces icon-free and Phosphor-swapped');
}

main();
