/**
 * E2E (#1224): Channels page mobile UX overhaul.
 *
 * At 375x800 viewport the channels page must:
 *  - Render a compact header strip above the channel list: page title and
 *    Add chip share the first row, and the header never overlaps the
 *    channel list (#2381: on touch devices the 44/48px touch targets used to
 *    overflow a fixed 56px header onto the first channel rows). The region
 *    pills may sit on a second, sideways-scrolling row.
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

// Runs in the page. Returns the header, title, Add chip and channel-list boxes.
function headerGeometry() {
  const sidebar = document.querySelector('.ch-sidebar');
  const header = sidebar && sidebar.querySelector('.ch-sidebar-header');
  const title = header && header.querySelector('.ch-sidebar-title');
  const add = document.getElementById('chAddChannelBtn');
  const list = document.getElementById('chList');
  if (!header || !title || !add || !list) return null;
  const box = (el) => { const b = el.getBoundingClientRect(); return { top: b.top, bottom: b.bottom, h: b.height }; };
  // The header's own box can stay small while its children overflow it
  // (#2381 had max-height: 56px with wrapped 44px pills painting below),
  // so measure the lowest visible descendant too.
  let contentBottom = header.getBoundingClientRect().bottom;
  header.querySelectorAll('*').forEach((el) => {
    const b = el.getBoundingClientRect();
    if (b.height > 0 && b.width > 0) contentBottom = Math.max(contentBottom, b.bottom);
  });
  return { header: box(header), title: box(title), add: box(add), list: box(list), contentBottom };
}

// #2381: the header may grow to a second row for the region pills, but it
// must stay compact and must never paint over the channel list.
function checkHeaderGeometry(r) {
  assert(r.contentBottom <= r.list.top + 1,
    'sidebar header content (bottom ' + Math.round(r.contentBottom) + 'px) overlaps #chList (top ' +
    Math.round(r.list.top) + 'px)');
  const titleMid = r.title.top + r.title.h / 2;
  assert(titleMid >= r.add.top && titleMid <= r.add.bottom,
    '"+ Add" chip must share the first header row with the title');
  assert(r.header.h <= 120, 'sidebar header must stay compact on mobile, got ' + Math.round(r.header.h) + 'px');
}
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
    // #1367: mobile now renders flat .ch-row entries; older .ch-item
    // markup still ships on desktop. Accept either so this regression
    // test keeps gating the header/empty-state/name-width invariants
    // (which apply to both layouts) without pinning the row markup.
    return l && l.querySelectorAll('.ch-item, .ch-row').length > 0;
  }, { timeout: 15000 });
  await page.waitForTimeout(300);

  await step('header: title + Add on one row, no overlap with channel list', async () => {
    const r = await page.evaluate(headerGeometry);
    assert(r !== null, 'sidebar header / title / add button / #chList not found');
    checkHeaderGeometry(r);
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
      // #1367: chat-app mobile row uses .ch-row + .ch-row-name. Fall back to
      // the legacy .ch-item .ch-item-name so this test still works on the
      // desktop layout / any regression that re-renders the old markup.
      const name = document.querySelector('#chList .ch-row .ch-row-name')
        || document.querySelector('#chList .ch-item .ch-item-name');
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

  // #2381: same checks on a touch device, where (pointer: coarse) gives the
  // region pills and "+ Add" their 44/48px touch-target minimums.
  await ctx.close();
  const ctxT = await browser.newContext({ viewport: { width: 375, height: 800 }, hasTouch: true, isMobile: true });
  const pT = await ctxT.newPage();
  await pT.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
  await pT.waitForFunction(() => {
    const l = document.getElementById('chList');
    return l && l.querySelectorAll('.ch-item, .ch-row').length > 0;
  }, { timeout: 15000 });
  await pT.waitForTimeout(300);

  await step('touch (375px, pointer: coarse): header does not overlap channel list', async () => {
    const coarse = await pT.evaluate(() => matchMedia('(pointer: coarse)').matches);
    assert(coarse, 'touch context did not report (pointer: coarse)');
    const r = await pT.evaluate(headerGeometry);
    assert(r !== null, 'sidebar header / title / add button / #chList not found');
    checkHeaderGeometry(r);
  });

  // Desktop guard: at 1024x800 the sidebar must remain side-by-side with main
  // (layout flex-direction stays row), not stacked. This protects the desktop
  // experience from a regression introduced by the mobile fix.
  await ctxT.close();
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
