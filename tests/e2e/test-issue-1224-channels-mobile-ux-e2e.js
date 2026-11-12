/**
 * E2E (#1224): Channels page mobile UX overhaul.
 *
 * At 375x800 viewport the channels page must:
 *  - Render a compact header strip above the channel list: page title and
 *    Add chip share the first row, and the header never overlaps the
 *    channel list (#2381: on touch devices the 44/48px touch targets used to
 *    overflow a fixed 56px header onto the first channel rows). The region
 *    pills may sit on a second, sideways-scrolling row.
 *  - Keep that true for both region filter modes, on a plain and a touch
 *    viewport: pills (<=4 regions, the fixture's own) and the dropdown
 *    (>4 regions, two extra served from /api/config/regions). Every region
 *    is picked with a real click: it must be tappable where it sits, the
 *    dropdown menu must not be clipped by the region row, and the page must
 *    re-query /api/channels?region=<code>.
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
async function openChannels(p, reload) {
  if (reload) await p.reload({ waitUntil: 'domcontentloaded' });
  else await p.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
  await p.waitForFunction(() => {
    const l = document.getElementById('chList');
    return l && l.querySelectorAll('.ch-item, .ch-row').length > 0;
  }, { timeout: 15000 });
  await p.waitForSelector('.ch-header-region .region-pill, .ch-header-region .region-dropdown-trigger', { timeout: 10000 });
  await p.waitForTimeout(300);
}

// The element's centre must be on screen and the topmost thing there, so a
// real tap lands on it (not clipped by the scrolling region row, not covered).
async function assertHittable(p, loc, what) {
  const ok = await loc.evaluate((el) => {
    const b = el.getBoundingClientRect();
    const x = b.left + b.width / 2, y = b.top + b.height / 2;
    if (b.width === 0 || b.height === 0 || x < 0 || y < 0 || x > innerWidth || y > innerHeight) return false;
    const hit = document.elementFromPoint(x, y);
    return !!hit && (hit === el || el.contains(hit));
  });
  assert(ok, what + ' is not visible and tappable at its own position (clipped or covered)');
}

async function openDropdown(p) {
  const trigger = p.locator('.ch-header-region .region-dropdown-trigger');
  const menu = p.locator('.ch-header-region .region-dropdown-menu');
  if (!(await menu.isVisible())) await trigger.click();
  assert(await menu.isVisible(), 'region dropdown menu did not open');
  assert((await trigger.getAttribute('aria-expanded')) === 'true', 'dropdown trigger aria-expanded is not true');
  // The menu drops below the header row; it must not be clipped to that row.
  const m = await menu.boundingBox();
  const row = await p.locator('.ch-header-region').boundingBox();
  assert(m && row && m.y >= row.y + row.height - 1, 'dropdown menu does not drop below the region row');
  const visibleH = await menu.evaluate((el) => {
    // Height of the menu actually painted, after every ancestor's clip.
    let b = el.getBoundingClientRect(); let top = b.top, bottom = b.bottom;
    for (let a = el.parentElement; a; a = a.parentElement) {
      const cs = getComputedStyle(a);
      if (cs.overflowY !== 'visible' || cs.overflowX !== 'visible') {
        const ab = a.getBoundingClientRect(); top = Math.max(top, ab.top); bottom = Math.min(bottom, ab.bottom);
      }
    }
    return bottom - top;
  });
  assert(visibleH >= m.height - 1, 'dropdown menu is clipped: ' + Math.round(visibleH) + 'px of ' + Math.round(m.height) + 'px visible');
}

async function resetToAll(p, mode) {
  if (mode === 'pills') {
    await p.locator('.ch-header-region .region-pill[data-region="__all__"]').click();
  } else {
    await openDropdown(p);
    await p.locator('.ch-header-region .region-dropdown-menu input[data-region="__all__"]').click();
  }
  await p.waitForFunction(() => !localStorage.getItem('meshcore-region-filter'), { timeout: 5000 });
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

  await ctx.close();

  // #2381: the region filter in the header, in both of its modes, on a plain
  // mobile viewport and on a touch device ((pointer: coarse) gives the pills
  // and "+ Add" their 44/48px touch-target minimums). Pill mode uses the
  // fixture's own regions (4); dropdown mode (>4) serves two extra regions
  // from /api/config/regions. Every region is selected with a real click, and
  // the page must re-query /api/channels for it with the header still clear
  // of the list.
  const DEVICES = [
    { label: 'mobile 375px', opts: { viewport: { width: 375, height: 800 } } },
    { label: 'touch 375px', opts: { viewport: { width: 375, height: 800 }, hasTouch: true, isMobile: true }, coarse: true },
  ];
  for (const dev of DEVICES) {
    for (const mode of ['pills', 'dropdown']) {
      const c = await browser.newContext(dev.opts);
      const p = await c.newPage();
      let regions = null;
      if (mode === 'dropdown') {
        await p.route('**/api/config/regions', async (route) => {
          const res = await route.fetch();
          regions = Object.assign({}, await res.json(), { ZZA: 'ZZA', ZZB: 'ZZB' });
          await route.fulfill({ response: res, json: regions });
        });
      }
      await openChannels(p);
      const tag = dev.label + ', ' + mode;

      await step(tag + ': header renders the region filter without overlapping the list', async () => {
        if (dev.coarse) {
          assert(await p.evaluate(() => matchMedia('(pointer: coarse)').matches),
            'touch context did not report (pointer: coarse)');
        }
        const sel = mode === 'pills' ? '.ch-header-region .region-pill[data-region]' : '.ch-header-region .region-dropdown-trigger';
        assert(await p.locator(sel).count() > 0, 'region filter not rendered in ' + mode + ' mode (' + sel + ')');
        const r = await p.evaluate(headerGeometry);
        assert(r !== null, 'sidebar header / title / add button / #chList not found');
        checkHeaderGeometry(r);
      });

      const regionCodes = mode === 'pills'
        ? await p.$$eval('.ch-header-region .region-pill[data-region]', (els) =>
            els.map((e) => e.dataset.region).filter((c) => c !== '__all__'))
        : Object.keys(regions || {});
      assert(regionCodes.length > 0, 'no region codes found');
      if (mode === 'pills') assert(regionCodes.length >= 2 && regionCodes.length <= 4, 'pill mode expects 2-4 regions, got ' + regionCodes.length);
      else assert(regionCodes.length > 4, 'dropdown mode expects >4 regions, got ' + regionCodes.length);

      for (const code of regionCodes) {
        await step(tag + ': selecting region ' + code + ' filters the channel list', async () => {
          const req = p.waitForRequest((r) => {
            const u = new URL(r.url());
            return u.pathname.endsWith('/api/channels') && u.searchParams.get('region') === code;
          }, { timeout: 5000 });
          req.catch(() => {}); // awaited below; don't crash the run if a step fails first
          if (mode === 'pills') {
            const pill = p.locator('.ch-header-region .region-pill[data-region="' + code + '"]');
            await pill.scrollIntoViewIfNeeded();
            await assertHittable(p, pill, 'region pill ' + code);
            await pill.click();
            await req;
            const states = await p.$$eval('.ch-header-region .region-pill[data-region]', (els) =>
              els.map((e) => [e.dataset.region, e.getAttribute('aria-checked')]));
            for (const [r, on] of states) {
              assert(on === String(r === code), 'after picking ' + code + ', pill ' + r + ' aria-checked=' + on);
            }
          } else {
            await openDropdown(p);
            const item = p.locator('.ch-header-region .region-dropdown-menu label', { has: p.locator('input[data-region="' + code + '"]') });
            await item.scrollIntoViewIfNeeded();
            await assertHittable(p, item, 'dropdown item ' + code);
            await item.click();
            await req;
            const label = (await p.locator('.ch-header-region .region-dropdown-trigger').innerText()).trim();
            assert(label.indexOf(code) === 0, 'dropdown trigger should read "' + code + '", got "' + label + '"');
            await openDropdown(p);
            const states = await p.$$eval('.ch-header-region .region-dropdown-menu input[data-region]', (els) =>
              els.map((e) => [e.dataset.region, e.checked]));
            for (const [r, on] of states) {
              assert(on === (r === code), 'after picking ' + code + ', checkbox ' + r + ' checked=' + on);
            }
            await p.locator('.ch-header-region .region-dropdown-trigger').click(); // close it again
            assert(!(await p.locator('.ch-header-region .region-dropdown-menu').isVisible()), 'dropdown did not close');
          }
          const r = await p.evaluate(headerGeometry);
          checkHeaderGeometry(r);
          // Back to All with a real click, so the next region starts clean.
          await resetToAll(p, mode);
        });
        // If the step failed part-way, make sure the next one still starts from All.
        if (await p.evaluate(() => !!localStorage.getItem('meshcore-region-filter'))) {
          await p.evaluate(() => localStorage.removeItem('meshcore-region-filter'));
          await openChannels(p, true);
        }
      }
      await c.close();
    }
  }

  // Desktop guard: at 1024x800 the sidebar must remain side-by-side with main
  // (layout flex-direction stays row), not stacked. This protects the desktop
  // experience from a regression introduced by the mobile fix.
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
