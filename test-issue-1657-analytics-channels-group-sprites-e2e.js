#!/usr/bin/env node
/* Issue #1657 — Analytics → Channels: group-header rows must render real
 * <svg> Phosphor sprites (My Channels / Network / Encrypted), NOT the
 * HTML-escaped literal "<svg class=\"ph-icon\"…>" source text.
 *
 * Bug: public/analytics.js passed the hardcoded group-section label (which
 * contains the sprite markup) through esc(), HTML-encoding the angle
 * brackets so the browser displayed the source instead of rendering it.
 *
 * This test asserts (in real Chromium against a running server):
 *   (1) The Channel Activity table has at least one group-header row.
 *   (2) Each rendered group-header row contains a real <svg.ph-icon>
 *       element (NOT escaped text).
 *   (3) For each expected group (key/radio/lock), if the label text is
 *       present, an actual <use href="…#ph-{key|radio|lock}"> resolves
 *       inside the same row.
 *   (4) The Channel Activity table's innerText contains zero literal
 *       "<svg" substrings (case-insensitive) — i.e. no escape leak.
 *
 * CHROMIUM_REQUIRE=1 makes Chromium-launch failure a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');
const assert = require('assert');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passes = 0, failures = 0;
function pass(msg) { console.log(`  ✓ ${msg}`); passes++; }
function fail(msg) { console.error(`  ✗ ${msg}`); failures++; }

async function main() {
  const requireChromium = process.env.CHROMIUM_REQUIRE === '1';
  let browser;
  try {
    browser = await chromium.launch({ headless: true });
  } catch (err) {
    if (requireChromium) {
      console.error(`HARD FAIL — Chromium unavailable: ${err.message}`);
      process.exit(1);
    }
    console.warn(`SKIP — Chromium unavailable: ${err.message}`);
    process.exit(0);
  }

  // Mobile viewport (375 wide) matches the staging screenshot that surfaced the bug.
  const ctx = await browser.newContext({ viewport: { width: 375, height: 800 } });
  const page = await ctx.newPage();

  await page.goto(`${BASE}/#/analytics?tab=channels`, { waitUntil: 'domcontentloaded' });

  // Wait for the channels table tbody to populate.
  try {
    await page.waitForFunction(() => {
      const tb = document.getElementById('channelsTbody');
      return tb && tb.querySelectorAll('tr').length > 0;
    }, null, { timeout: 12000 });
  } catch {
    fail('channelsTbody never populated within 12s');
    await browser.close();
    console.log(`\ntest-issue-1657: ${passes} passed, ${failures} failed`);
    process.exit(failures ? 1 : 0);
  }

  // Allow grouped render (decorate + buildHashKeyMap promise) to settle.
  await page.waitForTimeout(800);

  const result = await page.evaluate(() => {
    const tb = document.getElementById('channelsTbody');
    if (!tb) return { error: 'tbody missing' };
    const headerRows = Array.from(tb.querySelectorAll('tr.ch-section-row'));
    const perHeader = headerRows.map((tr) => {
      const cell = tr.querySelector('td.ch-section-header') || tr.querySelector('td');
      const text = (cell && cell.textContent) || '';
      const svgs = cell ? cell.querySelectorAll('svg.ph-icon').length : 0;
      const uses = cell
        ? Array.from(cell.querySelectorAll('svg.ph-icon use'))
            .map((u) => (u.getAttribute('href') || u.getAttribute('xlink:href') || ''))
        : [];
      return { text: text.trim(), svgs, uses };
    });
    const tableEl = document.getElementById('channelsTable') || tb.parentElement;
    const innerHTML = tableEl ? tableEl.innerHTML : '';
    // innerText reflects what the user sees; literal "<svg" in innerText
    // means the markup got HTML-escaped before being inserted.
    const innerText = tableEl ? (tableEl.innerText || tableEl.textContent || '') : '';
    return { headerRows: perHeader, hasLiteralSvgInText: /<svg/i.test(innerText), htmlLen: innerHTML.length };
  });

  if (result.error) {
    fail(result.error);
    await browser.close();
    process.exit(1);
  }

  // (1) at least one header row
  if (result.headerRows.length === 0) {
    fail('(1) no group-header rows rendered (expected at least one of My Channels / Network / Encrypted)');
  } else {
    pass(`(1) ${result.headerRows.length} group-header row(s) present`);
  }

  // (2) every header row has a real SVG element
  let allHaveSvg = true;
  for (const h of result.headerRows) {
    if (h.svgs < 1) {
      fail(`(2) group-header row "${h.text.slice(0, 40)}" has 0 svg.ph-icon children (escape leak)`);
      allHaveSvg = false;
    }
  }
  if (allHaveSvg && result.headerRows.length) pass(`(2) all ${result.headerRows.length} group-header rows contain a real <svg.ph-icon>`);

  // (3) per expected group, verify the sprite ref
  const expected = [
    { match: /My Channels/i, ref: /#ph-key$/, name: 'My Channels → #ph-key' },
    { match: /Network/i,     ref: /#ph-radio$/, name: 'Network → #ph-radio' },
    { match: /Encrypted/i,   ref: /#ph-lock$/, name: 'Encrypted → #ph-lock' },
  ];
  for (const e of expected) {
    const row = result.headerRows.find((h) => e.match.test(h.text));
    if (!row) {
      // Not all groups are guaranteed to have rows (depends on fixture data).
      // Skip silently — group only renders if it has channels.
      continue;
    }
    const ok = row.uses.some((u) => e.ref.test(u));
    if (!ok) fail(`(3) ${e.name}: expected ${e.ref} but uses=${JSON.stringify(row.uses)}`);
    else pass(`(3) ${e.name} rendered as real sprite`);
  }

  // (4) no literal "<svg" in the table's user-visible text
  if (result.hasLiteralSvgInText) {
    fail('(4) Channel Activity table innerText contains literal "<svg" — sprite markup was HTML-escaped');
  } else {
    pass('(4) Channel Activity table innerText is free of literal "<svg" leaks');
  }

  await browser.close();
  console.log(`\ntest-issue-1657: ${passes} passed, ${failures} failed`);
  assert.strictEqual(failures, 0, `${failures} assertion(s) failed`);
  process.exit(0);
}

main().catch((err) => {
  console.error('test-issue-1657: FAIL —', err);
  process.exit(1);
});
