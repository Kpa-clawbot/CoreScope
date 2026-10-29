/**
 * E2E: the packet detail pane infers an observation's hops by position, and
 * the map route view draws the same hops.
 *
 * Fixture: packet 57224a4a01c9c682, observation 147 — a 22-hop flood whose
 * path carries the 1-byte prefix "10" three times and "D6" twice, so a
 * per-prefix resolution would give each repeat the same answer.
 *
 * Usage: BASE_URL=http://localhost:13581 node tests/e2e/test-hop-path-inference-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const HASH = '57224a4a01c9c682';
const OBS_ID = '147';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(10000);
  const pageErrors = [];
  page.on('pageerror', (e) => { pageErrors.push(e.message); console.error('[pageerror]', e.message); });

  console.log(`\n=== Hop path inference E2E against ${BASE} ===`);

  let detailHops = [];
  await step('the detail pane renders the observation\'s 22 hops', async () => {
    await page.goto(`${BASE}/#/packets/${HASH}?obs=${OBS_ID}`, { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => {
      const dt = [...document.querySelectorAll('.detail-meta dt')].find(d => d.textContent === 'Path');
      return dt && dt.nextElementSibling.querySelectorAll('.hop').length > 0;
    }, null, { timeout: 15000 });
    detailHops = await page.evaluate(() => {
      const dt = [...document.querySelectorAll('.detail-meta dt')].find(d => d.textContent === 'Path');
      return [...dt.nextElementSibling.querySelectorAll('.hop')].map(h => ({
        text: h.textContent.trim(), title: h.title, cls: h.className,
      }));
    });
    assert(detailHops.length === 22, 'expected 22 hops, got ' + detailHops.length);
  });

  await step('every hop is an inferred entry: a likelihood or an unlisted reason', async () => {
    const bad = detailHops.filter(h => !/% likely|no listed node|no known node|no GPS/.test(h.title));
    assert(!bad.length, 'hops without an inferred title: ' + JSON.stringify(bad.slice(0, 3)));
  });

  await step('the repeated "10" prefix is resolved per position', async () => {
    const tens = detailHops.filter(h => /^10 — /.test(h.title));
    assert(tens.length === 3, 'expected three "10" hops, got ' + tens.length);
    // Not every repeat collapses onto one answer: a node forwards a packet
    // once, so at most one of them can be any given node.
    const named = tens.filter(h => !/hop-unlisted/.test(h.cls)).map(h => h.text);
    assert(new Set(named).size === named.length, 'a node appears twice: ' + named.join(', '));
  });

  await step('a candidate badge opens a popover with likelihoods', async () => {
    const btn = page.locator('.detail-meta .hop-conflict-btn').first();
    await btn.click();
    await page.waitForSelector('.hop-conflict-popover', { timeout: 5000 });
    const header = await page.locator('.hop-conflict-popover .hop-conflict-header').textContent();
    assert(/\(\d+%\)/.test(header), 'popover header has no likelihood: ' + header);
  });

  await step('the map route view draws the same hops, unlisted ones as placeholders', async () => {
    await page.goto(`${BASE}/#/map?packet=${HASH}&obs=${OBS_ID}`, { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => document.querySelectorAll('.mc-rt-row').length >= 22, null, { timeout: 15000 });
    const rows = await page.evaluate(() => [...document.querySelectorAll('.mc-rt-row')].map(r => r.innerText));
    for (let i = 0; i < detailHops.length; i++) {
      const hop = detailHops[i];
      const row = rows[i] || '';
      if (/hop-unlisted/.test(hop.cls) && !/no GPS/.test(hop.title)) {
        assert(/unlisted/.test(row), `hop ${i} (${hop.text}) is unlisted in the detail but not on the map: ${row}`);
      } else {
        const name = hop.text.replace(/ \(no GPS\)$/, '');
        assert(row.includes(name), `hop ${i}: detail "${name}" vs map "${row.replace(/\s+/g, ' ')}"`);
      }
    }
  });

  await step('no page errors', async () => {
    assert(!pageErrors.length, pageErrors.join(' | '));
  });

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
