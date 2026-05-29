/**
 * E2E (#1487): BYOP modal must render with a usable layout — the title bar
 * must NOT consume most of the dialog height and the body controls
 * (description, textarea, Decode button) must be visible and not occluded
 * by the sticky header.
 *
 * Reporter: @EldoonNemar — "The dialog text can't be seen due to the title
 * bar being massive."
 *
 * Repro:
 *   1. Open /#/packets on mobile (390x844).
 *   2. Click the 📦 BYOP toolbar button.
 *   3. Observe the modal: the .byop-header swells (~73px tall) and the
 *      next-sibling description paragraph (`.text-muted`) starts INSIDE
 *      the sticky-header band, getting visually clipped/occluded.
 *
 * Root cause: `.byop-header` uses `position: sticky` + a negative
 * `margin: -24px -24px 12px` that assumes desktop `.modal` padding of
 * 24px — but `.modal` switches to 16px padding on mobile. The close
 * button's box (border + padding) further inflates the header. The
 * description paragraph then begins at top≈85 inside a header that
 * spans 24–97, hiding the text.
 *
 * Fix expectation:
 *   - Header height is bounded (<= 56px is a reasonable target).
 *   - The description paragraph's top edge is BELOW the sticky-header
 *     bottom edge — i.e. no visual occlusion.
 *   - The textarea and Decode button are fully within the modal client rect.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1487-byop-modal-layout-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

async function probeModal(page) {
  return page.evaluate(() => {
    const m = document.querySelector('.byop-modal');
    if (!m) return { err: 'no modal' };
    const hdr = m.querySelector('.byop-header');
    const desc = m.querySelector('.text-muted');
    const ta = m.querySelector('.byop-input');
    const btn = m.querySelector('#byopDecode');
    const mr = m.getBoundingClientRect();
    const hr = hdr.getBoundingClientRect();
    const dr = desc.getBoundingClientRect();
    const tr = ta.getBoundingClientRect();
    const br = btn.getBoundingClientRect();
    return {
      modalH: Math.round(mr.height),
      hdrH: Math.round(hr.height),
      hdrBottom: Math.round(hr.bottom),
      descTop: Math.round(dr.top),
      taBottom: Math.round(tr.bottom),
      btnBottom: Math.round(br.bottom),
      modalBottom: Math.round(mr.bottom),
    };
  });
}

async function openBYOP(page) {
  await page.waitForSelector('[data-action="pkt-byop"]', { timeout: 8000 });
  await page.evaluate(() => {
    document.querySelectorAll('.byop-overlay').forEach(o => o.remove());
    document.querySelector('[data-action="pkt-byop"]').click();
  });
  await page.waitForSelector('.byop-modal', { timeout: 5000 });
  await page.waitForTimeout(200);
}

async function runViewport(browser, label, viewport) {
  const ctx = await browser.newContext({ viewport });
  const page = await ctx.newPage();
  page.setDefaultTimeout(10000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n--- viewport ${label} (${viewport.width}x${viewport.height}) ---`);

  await step('navigate to /packets and open BYOP', async () => {
    await page.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
    await openBYOP(page);
  });

  await step('header height is bounded (<= 56px)', async () => {
    const p = await probeModal(page);
    assert(p.hdrH <= 56, `header height ${p.hdrH}px > 56px cap (modal=${p.modalH}px)`);
  });

  await step('description paragraph is NOT occluded by sticky header', async () => {
    const p = await probeModal(page);
    assert(p.descTop >= p.hdrBottom,
      `description top (${p.descTop}) starts INSIDE sticky header band (header bottom=${p.hdrBottom})`);
  });

  await step('textarea and Decode button do not overflow modal client rect', async () => {
    const p = await probeModal(page);
    assert(p.taBottom <= p.modalBottom + 1, `textarea bottom (${p.taBottom}) overflows modal (${p.modalBottom})`);
    assert(p.btnBottom <= p.modalBottom + 1, `Decode button bottom (${p.btnBottom}) overflows modal`);
  });

  await ctx.close();
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log(`\n=== #1487 BYOP modal layout E2E against ${BASE} ===`);

  await runViewport(browser, 'mobile', { width: 390, height: 844 });
  await runViewport(browser, 'desktop', { width: 1280, height: 800 });

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed === 0 ? 0 : 1);
})().catch(e => { console.error(e); process.exit(1); });
