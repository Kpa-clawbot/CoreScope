/**
 * E2E (#1224): Channels page mobile UX overhaul.
 *
 * At 375x800 viewport the channels page must:
 *  - Render a header strip above the channel list ≤60px tall (page title +
 *    Add chip + region filter chip + analytics overflow) in ONE row.
 *  - Render "+ Add Channel" as a compact chip — NOT a full-width hero (the
 *    add control must be narrower than 65% of the sidebar width).
 *  - Render channel rows where the channel name has computed-width > 150px
 *    (the row must not be clipped by oversized inline action buttons).
 *  - Render the "Select a channel" empty state container occupying < 40% of
 *    the viewport height (no desktop-thinking empty state on mobile).
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1224-channels-mobile-ux-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  \u2713 ' + name); }
  catch (e) { failed++; console.error('  \u2717 ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

async function run() {
  const launchOpts = { args: ['--no-sandbox'] };
  if (process.env.CHROMIUM_PATH) launchOpts.executablePath = process.env.CHROMIUM_PATH;
  const browser = await chromium.launch(launchOpts);
  const ctx = await browser.newContext({ viewport: { width: 375, height: 800 } });
  const page = await ctx.newPage();

  await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#chList', { timeout: 10000 });
  await page.waitForFunction(() => {
    const l = document.getElementById('chList');
    return l && l.querySelectorAll('.ch-item').length > 0;
  }, { timeout: 15000 });
  await page.waitForTimeout(300);

  await step('header strip above channel list is \u226460px tall on mobile', async () => {
    const headerH = await page.evaluate(() => {
      const sidebar = document.querySelector('.ch-sidebar');
      const header = sidebar && sidebar.querySelector('.ch-sidebar-header');
      if (!header) return null;
      return Math.round(header.getBoundingClientRect().height);
    });
    assert(headerH !== null, 'sidebar header not found');
    assert(headerH <= 60, 'sidebar header must be \u226460px on mobile, got ' + headerH + 'px');
  });

  await step('"+ Add Channel" is a compact chip, not full-width hero', async () => {
    const ratio = await page.evaluate(() => {
      const sidebar = document.querySelector('.ch-sidebar');
      const btn = document.getElementById('chAddChannelBtn');
      if (!sidebar || !btn) return null;
      const sw = sidebar.getBoundingClientRect().width;
      const bw = btn.getBoundingClientRect().width;
      return bw / sw;
    });
    assert(ratio !== null, 'add channel button not found');
    assert(ratio < 0.65, 'add button width must be <65% of sidebar, got ratio=' + ratio.toFixed(2));
  });

  await step('first channel row name has computed-width >150px', async () => {
    const nameW = await page.evaluate(() => {
      const name = document.querySelector('#chList .ch-item .ch-item-name');
      if (!name) return null;
      return Math.round(name.getBoundingClientRect().width);
    });
    assert(nameW !== null, 'first channel name element not found');
    assert(nameW > 150, 'channel name width must be >150px on mobile, got ' + nameW + 'px');
  });

  await step('empty-state container is < 40% of viewport height', async () => {
    const data = await page.evaluate(() => {
      const empty = document.querySelector('.ch-empty');
      if (!empty) return null;
      return {
        h: Math.round(empty.getBoundingClientRect().height),
        vh: window.innerHeight,
      };
    });
    assert(data !== null, 'empty state element not found');
    const pct = data.h / data.vh;
    assert(pct < 0.40,
      'empty-state height ' + data.h + 'px is ' + Math.round(pct * 100) +
      '% of viewport (' + data.vh + 'px) \u2014 must be <40%');
  });

  // Desktop guard: at 1024x800 the sidebar must remain side-by-side with main
  // (layout flex-direction stays row), not stacked. This protects the desktop
  // experience from a regression introduced by the mobile fix.
  await ctx.close();
  const ctx2 = await browser.newContext({ viewport: { width: 1024, height: 800 } });
  const p2 = await ctx2.newPage();
  await p2.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
  await p2.waitForSelector('.ch-layout', { timeout: 10000 });
  await p2.waitForTimeout(200);

  await step('desktop (1024px): ch-layout stays row (side-by-side)', async () => {
    const dir = await p2.evaluate(() => {
      const l = document.querySelector('.ch-layout');
      return l ? getComputedStyle(l).flexDirection : null;
    });
    assert(dir === 'row', 'desktop ch-layout flex-direction must be "row", got ' + dir);
  });

  await browser.close();

  console.log('\n' + passed + '/' + (passed + failed) + ' tests passed' +
              (failed ? ', ' + failed + ' failed' : ''));
  process.exit(failed > 0 ? 1 : 0);
}

run().catch(err => { console.error('Fatal:', err); process.exit(1); });
