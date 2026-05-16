/**
 * E2E (#1206): VCR controls panel must not occlude the bottom of the
 * Live Map packet list (live feed).
 *
 * Acceptance: the bottom of the last visible packet-list row must be at or
 * above the top of the VCR bar — i.e. lastRow.bottom ≤ vcr.top — when both
 * the feed and VCR are visible.
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1206-vcr-overlap-e2e.js
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

async function gotoLive(page) {
  await page.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#liveMap');
  await page.waitForSelector('#vcrBar');
  await page.waitForSelector('#liveFeed');
  // Let the feed populate from the fixture
  await page.waitForFunction(() => {
    var f = document.querySelector('#liveFeed .panel-content');
    return f && f.children.length > 0;
  }, { timeout: 8000 }).catch(() => {});
  await page.waitForTimeout(300);
}

async function measure(page) {
  return page.evaluate(() => {
    var feed = document.getElementById('liveFeed');
    var vcr = document.getElementById('vcrBar');
    var legend = document.getElementById('liveLegend');
    var content = feed ? feed.querySelector('.panel-content') : null;
    if (!feed || !vcr || !content) return null;
    var rows = content.children;
    var lastRow = rows.length ? rows[rows.length - 1] : null;
    var feedRect = feed.getBoundingClientRect();
    var vcrRect = vcr.getBoundingClientRect();
    var legendRect = legend ? legend.getBoundingClientRect() : null;
    return {
      feedBottom: feedRect.bottom,
      vcrTop: vcrRect.top,
      vcrHeight: vcrRect.height,
      lastRowBottom: lastRow ? lastRow.getBoundingClientRect().bottom : null,
      rowCount: rows.length,
      feedVisible: feedRect.width > 0 && feedRect.height > 0,
      vcrVisible: vcrRect.width > 0 && vcrRect.height > 0,
      legendBottom: legendRect ? legendRect.bottom : null,
      legendVisible: legendRect ? (legendRect.width > 0 && legendRect.height > 0) : false,
    };
  });
}

// Force the VCR bar to a tall height to simulate real-world conditions
// (mobile two-row layout, safe-area-inset). Bottom-pinned overlays MUST
// track --vcr-bar-height (set by ResizeObserver on .vcr-bar) — anything
// using a hard-coded offset will overlap once the bar grows.
async function inflateVCR(page, heightPx) {
  await page.evaluate((h) => {
    var bar = document.getElementById('vcrBar');
    if (bar) {
      bar.style.minHeight = h + 'px';
      bar.style.height = h + 'px';
    }
  }, heightPx);
  // Allow ResizeObserver + frame
  await page.waitForTimeout(120);
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log('\n=== #1206 VCR overlap E2E against ' + BASE + ' ===');

  // Desktop viewport — feed is bl-pinned by default, VCR is full-width bottom.
  {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));
    await step('[1280x800] navigate to /live, feed + VCR present', async () => {
      await gotoLive(page);
      const m = await measure(page);
      assert(m, 'feed/vcr/content not found');
      assert(m.feedVisible, 'live feed must be visible');
      assert(m.vcrVisible, 'vcr bar must be visible');
      assert(m.vcrHeight >= 20, 'vcr should have a real height, got ' + m.vcrHeight);
    });

    await step('[1280x800] live feed container bottom <= VCR top (no overlap)', async () => {
      const m = await measure(page);
      assert(m.feedBottom <= m.vcrTop + 0.5,
        'feed bottom (' + m.feedBottom + ') must be <= vcr top (' + m.vcrTop + ')');
    });

    await step('[1280x800] last packet row bottom <= VCR top (no row occluded)', async () => {
      const m = await measure(page);
      if (m.lastRowBottom == null) {
        // No rows in fixture — fall back to container check, already covered.
        return;
      }
      assert(m.lastRowBottom <= m.vcrTop + 0.5,
        'last packet row bottom (' + m.lastRowBottom + ') must be <= vcr top (' + m.vcrTop + ')');
    });

    await step('[1280x800] legend bottom <= VCR top (no overlap, default height)', async () => {
      const m = await measure(page);
      if (!m.legendVisible) return; // legend hidden = no overlap to test
      assert(m.legendBottom <= m.vcrTop + 0.5,
        'legend bottom (' + m.legendBottom + ') must be <= vcr top (' + m.vcrTop + ')');
    });

    await step('[1280x800] inflate VCR to 120px — feed AND legend still clear bar', async () => {
      await inflateVCR(page, 120);
      const m = await measure(page);
      assert(m.vcrHeight >= 100, 'inflate failed, vcr height = ' + m.vcrHeight);
      assert(m.feedBottom <= m.vcrTop + 0.5,
        'feed bottom (' + m.feedBottom + ') must be <= vcr top (' + m.vcrTop + ') after inflate');
      if (m.legendVisible) {
        assert(m.legendBottom <= m.vcrTop + 0.5,
          'legend bottom (' + m.legendBottom + ') must be <= vcr top (' + m.vcrTop + ') after inflate — ' +
          'legend is not tracking --vcr-bar-height');
      }
    });

    await ctx.close();
  }

  // Mobile-medium viewport (768) — feed still rendered; same invariant must hold.
  {
    const ctx = await browser.newContext({ viewport: { width: 720, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));
    await step('[720x800] navigate to /live', async () => { await gotoLive(page); });

    await step('[720x800] feed bottom <= VCR top', async () => {
      const m = await measure(page);
      if (!m || !m.feedVisible) return; // feed hidden at this width = no overlap to test
      assert(m.feedBottom <= m.vcrTop + 0.5,
        'feed bottom (' + m.feedBottom + ') must be <= vcr top (' + m.vcrTop + ')');
    });

    await ctx.close();
  }

  await browser.close();
  console.log('\n#1206 VCR overlap: ' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
