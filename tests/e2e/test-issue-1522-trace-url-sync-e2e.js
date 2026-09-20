#!/usr/bin/env node
/* Issue #1522 — the trace hash belongs in the URL.
 *
 * Two directions, and the bug was that neither held:
 *   1. Running a trace must write the hash into the URL, so the result can
 *      be linked and reloaded.
 *   2. Opening that URL must pre-fill the input and run the trace, without
 *      a replaceState that rewrites the address the visitor just opened.
 *
 * test-e2e-playwright.js covers that the trace page loads and that a search
 * returns results. It does not look at the URL, which is the whole of #1522.
 *
 * Ported from the @playwright/test version of this file, which was written
 * against a runner this project does not install and so had never run
 * (#2037). Same two assertions, plain-node Chromium like every other suite
 * here. CHROMIUM_REQUIRE=1 makes Chromium-launch failure a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');

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

  const ctx = await browser.newContext();
  const page = await ctx.newPage();

  // (1) Clicking Trace writes the hash into the URL.
  await page.goto(`${BASE}/#/tools/trace/`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForSelector('#traceHashInput', { timeout: 8000 });
    await page.fill('#traceHashInput', 'deadbeef');
    await page.click('#traceBtn');
    await page.waitForFunction(() => /#\/tools\/trace\/deadbeef$/.test(location.hash ? location.href : ''), null, { timeout: 8000 });
    pass('(1) running a trace writes the hash into the URL');
  } catch {
    fail(`(1) the URL did not become #/tools/trace/deadbeef; it is ${JSON.stringify(page.url())}`);
  }

  // (2) A deep link pre-fills the input and leaves the URL alone.
  await page.goto(`${BASE}/#/tools/trace/cafebabe`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForSelector('#traceHashInput', { timeout: 8000 });
    await page.waitForFunction(() => {
      const el = document.getElementById('traceHashInput');
      return !!el && el.value === 'cafebabe';
    }, null, { timeout: 8000 });
    pass('(2) a deep link pre-fills the trace input');
  } catch {
    const v = await page.evaluate(() => {
      const el = document.getElementById('traceHashInput');
      return el ? el.value : '(input missing)';
    });
    fail(`(2) the input holds ${JSON.stringify(v)}, expected "cafebabe"`);
  }

  if (/#\/tools\/trace\/cafebabe$/.test(page.url())) {
    pass('(3) the deep-linked URL is left unchanged (no replaceState pollution)');
  } else {
    fail(`(3) the URL was rewritten to ${JSON.stringify(page.url())}`);
  }

  await browser.close();
  console.log(`\ntest-issue-1522-trace-url-sync-e2e: ${passes} passed, ${failures} failed`);
  process.exit(failures ? 1 : 0);
}

main().catch((e) => { console.error(e); process.exit(1); });
