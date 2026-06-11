#!/usr/bin/env node
/* Issue #1648 — M2: emoji → Phosphor sprite migration (static scan).
 *
 * M2 covers page headers + table chrome surfaces:
 *   analytics.js, nodes.js, packets.js, live.js, map.js,
 *   node-analytics.js, traces.js, perf.js, audio-lab.js
 *
 * Asserts (per file):
 *   1. Zero UI-iconography codepoints (U+1F300–1FAFF, U+2600–27BF, and
 *      Misc-Symbols: ◆●■▲★☆○✓✗⚠✉) outside an allowlist of contexts that are
 *      not UI iconography (CSS comments, JS comments referencing prior
 *      glyphs, console.log/debug strings, and explicitly tagged
 *      // EMOJI-OK lines).
 *   2. At least N <use href="…#ph-… references where N is roughly the
 *      historical swap count (i.e., the swaps actually landed).
 *
 * Anti-tautology: this test FAILS today (pre-fix) by construction — each
 * M2 file currently contains many of the offending codepoints.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const ROOT = path.resolve(__dirname, 'public');

const M2_FILES = [
  'analytics.js',
  'nodes.js',
  'packets.js',
  'live.js',
  'map.js',
  'node-analytics.js',
  'traces.js',
  'perf.js',
  'audio-lab.js',
];

// Minimum number of Phosphor <use href> swaps we expect per file after M2.
// Floor values, intentionally conservative so we don't over-fit.
const MIN_USE_REFS = {
  'analytics.js': 60,
  'nodes.js': 15,
  'packets.js': 18,
  'live.js': 15,
  'map.js': 10,
  'node-analytics.js': 4,
  'traces.js': 1,
  'perf.js': 9,
  'audio-lab.js': 5,
};

// Codepoint ranges (emoji proper).
const EMOJI = /[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/u;
// Misc-symbols used as iconography per #1648.
const MISC_ICON = /[◆●■▲★☆○✓✗⚠✉]/u;

// Allowlist substrings: lines containing any of these are exempt from the
// scan (they reference prior emoji as data, not UI). Keep this tight.
const ALLOW_SUBSTRINGS = [
  'EMOJI-OK',          // explicit per-line opt-out tag
  '// alongside the',  // live.js code comment referencing the old 👁 badge
  '// renderer can label', // map.js comment
  '// Unresolved prefix', // map.js comment
  '// clicking #liveFullscreenToggle', // live.js comment
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

function assertSpriteHasM2Icons() {
  const sp = path.join(ROOT, 'icons', 'phosphor-sprite.svg');
  const txt = fs.readFileSync(sp, 'utf8');
  const need = [
    // M2-added icons that were not in M1
    'ph-bomb', 'ph-buildings', 'ph-caret-down', 'ph-caret-up',
    'ph-cell-signal-high', 'ph-chats', 'ph-check-circle', 'ph-clipboard-text',
    'ph-clock', 'ph-crosshair', 'ph-envelope', 'ph-flame', 'ph-gear',
    'ph-globe', 'ph-graph', 'ph-handshake', 'ph-info', 'ph-key', 'ph-link',
    'ph-lock-open', 'ph-map-pin', 'ph-path', 'ph-piano-keys', 'ph-prohibit',
    'ph-question', 'ph-radio', 'ph-repeat', 'ph-ruler', 'ph-share-network',
    'ph-shuffle', 'ph-signpost', 'ph-speaker-high', 'ph-target',
    'ph-thermometer', 'ph-trend-up', 'ph-trophy', 'ph-x-circle',
    'ph-battery-high', 'ph-battery-low', 'ph-arrows-out', 'ph-pulse',
    'ph-chart-line', 'ph-list-numbers', 'ph-dice-five', 'ph-book-open',
    'ph-microphone', 'ph-house-line', 'ph-push-pin',
  ];
  const missing = need.filter(id => !txt.includes(`id="${id}"`));
  if (missing.length) throw new Error(`sprite missing M2 symbols: ${missing.join(', ')}`);
}

function main() {
  let failed = 0;
  console.log('— Issue #1648 M2 — emoji/misc-icon scan');

  try {
    assertSpriteHasM2Icons();
    console.log('  ✓ sprite has required M2 symbols');
  } catch (e) {
    console.error(`  ✗ ${e.message}`);
    failed++;
  }

  for (const rel of M2_FILES) {
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
    // Hard assertion (separate from logging) — file must be clean AND have enough swaps.
    assert.strictEqual(hits.length, 0,
      `${rel} must contain zero emoji/misc-icon iconography (got ${hits.length} hit(s))`);
    assert.ok(useRefs >= min,
      `${rel} must have ≥${min} <use href="…#ph-…"> refs (got ${useRefs})`);
  }

  if (failed) {
    console.error(`\nFAIL: ${failed} M2 check(s) failed`);
    process.exit(1);
  }
  console.log('\nPASS: all M2 surfaces icon-free and Phosphor-swapped');
}

main();
