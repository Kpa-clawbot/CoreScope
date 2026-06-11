#!/usr/bin/env node
/* Issue #1648 — M4: emoji → Phosphor sprite migration (static scan).
 *
 * M4 covers map & route overlays:
 *   map.js, area-map.html, analytics.js (route-jump residual),
 *   packets.js (route/replay residual), route-view.js,
 *   route-view-utils.js, route-view.css, route-render.js
 *
 * Asserts (per file):
 *   1. Zero UI-iconography codepoints (emoji + the Misc-Symbols set used
 *      historically as icons, including pane-toggle carets ▶/◀, dropdown
 *      caret ▾, ✕ close, ⚑ destination flag) outside an allowlist
 *      (// EMOJI-OK lines, or comments mentioning prior glyphs).
 *   2. At least N <use href="…#ph-… references per file (sanity floor).
 *
 * Anti-tautology: this test FAILS pre-implementation by construction.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const ROOT = path.resolve(__dirname, 'public');

const M4_FILES = [
  'map.js',
  'area-map.html',
  'analytics.js',
  'packets.js',
  'route-view.js',
  'route-view-utils.js',
  'route-view.css',
  'route-render.js',
];

// Floors: existing M2/M3 swaps already in some of these files contribute.
// M4 adds at least a handful per file (carets, replay, close, flag, etc.).
const MIN_USE_REFS = {
  'map.js': 18,
  'area-map.html': 1,
  'analytics.js': 60,
  'packets.js': 8,
  'route-view.js': 5,
  'route-view-utils.js': 6,
  'route-view.css': 0,    // CSS uses content with <svg> not feasible; rely on JS
  // route-render.js uses dynamic href concat (ph-play/ph-flag chosen at runtime);
  // sprite refs are functional but not a literal-substring grep target. Floor 0.
  'route-render.js': 0,
};

const EMOJI = /[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/u;
// Misc-Symbols treated as iconography across all M4 surfaces.
const MISC_ICON_BASE = /[◆●■▲★☆○✓✗⚠✉✕⚑]/u;
// Per-file extra chars: pane/collapse carets ▶◀ and route-CSS chevron ▾.
// (packets.js dropdown chevrons ▾/▴ are M5 table-chrome; not in M4 scope.)
const EXTRA_ICONS = {
  'map.js':            /[▶◀▾]/u,
  'analytics.js':      /[▶◀]/u,
  'packets.js':        /[▶◀]/u,
  'route-view.js':     /[▶◀▾]/u,
  'route-view-utils.js': /[▶◀▾]/u,
  'route-view.css':    /[▶◀▾]/u,
  'route-render.js':   /[▶◀▾]/u,
};

function scanFile(rel) {
  const abs = path.join(ROOT, rel);
  const txt = fs.readFileSync(abs, 'utf8');
  const lines = txt.split('\n');
  const hits = [];
  const extra = EXTRA_ICONS[rel];
  lines.forEach((line, idx) => {
    if (line.includes('EMOJI-OK')) return;
    if (EMOJI.test(line) || MISC_ICON_BASE.test(line) || (extra && extra.test(line))) {
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

function assertSpriteHasM4Icons() {
  const sp = path.join(ROOT, 'icons', 'phosphor-sprite.svg');
  const txt = fs.readFileSync(sp, 'utf8');
  const need = [
    'ph-caret-left', 'ph-caret-right', 'ph-play',
    'ph-flag', 'ph-arrow-u-up-left', 'ph-pause',
  ];
  const missing = need.filter(id => !txt.includes(`id="${id}"`));
  if (missing.length) throw new Error(`sprite missing M4 symbols: ${missing.join(', ')}`);
}

// Route-render.js builds its sprite href with runtime concat. Verify the
// sprite IDs and the sprite path prefix both appear in source.
function assertRouteRenderSpriteRefs() {
  const txt = fs.readFileSync(path.join(ROOT, 'route-render.js'), 'utf8');
  for (const id of ['ph-play', 'ph-flag']) {
    if (!txt.includes("'" + id + "'") && !txt.includes('"' + id + '"')) {
      throw new Error(`route-render.js must reference sprite id ${id}`);
    }
  }
  if (!txt.includes('/icons/phosphor-sprite.svg#')) {
    throw new Error('route-render.js must reference the Phosphor sprite path');
  }
}

function main() {
  let failed = 0;
  console.log('— Issue #1648 M4 — emoji/misc-icon scan');

  try {
    assertSpriteHasM4Icons();
    console.log('  ✓ sprite has required M4 symbols');
  } catch (e) {
    console.error(`  ✗ ${e.message}`);
    failed++;
  }

  try {
    assertRouteRenderSpriteRefs();
    console.log('  ✓ route-render.js threads ph-play / ph-flag sprite refs');
  } catch (e) {
    console.error(`  ✗ ${e.message}`);
    failed++;
  }

  for (const rel of M4_FILES) {
    const hits = scanFile(rel);
    if (hits.length === 0) {
      console.log(`  ✓ ${rel} clean`);
    } else {
      console.error(`  ✗ ${rel} has ${hits.length} emoji/misc-icon hit(s):`);
      for (const h of hits.slice(0, 30)) console.error(`      ${h.file}:${h.line} ${h.text}`);
      if (hits.length > 30) console.error(`      … (+${hits.length - 30} more)`);
      failed++;
    }
    const useRefs = countUseRefs(rel);
    const min = MIN_USE_REFS[rel] || 0;
    if (useRefs < min) {
      console.error(`  ✗ ${rel} has only ${useRefs} <use href="…#ph-…"> refs (expected ≥${min})`);
      failed++;
    } else {
      console.log(`  ✓ ${rel} has ${useRefs} Phosphor <use> refs (≥${min})`);
    }
  }

  if (failed) {
    console.error(`\nFAIL: ${failed} M4 check(s) failed`);
    process.exit(1);
  }
  // Hard asserts for CI
  for (const rel of M4_FILES) {
    const hits = scanFile(rel);
    assert.strictEqual(hits.length, 0,
      `${rel} must contain zero emoji/misc-icon iconography (got ${hits.length} hit(s))`);
    const useRefs = countUseRefs(rel);
    const min = MIN_USE_REFS[rel] || 0;
    assert.ok(useRefs >= min,
      `${rel} must have ≥${min} <use href="…#ph-…"> refs (got ${useRefs})`);
  }
  console.log('\nPASS: all M4 surfaces icon-free and Phosphor-swapped');
}

main();
