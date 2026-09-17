/**
 * #1273 — QR overlay container is 2-3× taller than the QR canvas on the
 * full node detail page (`#/nodes/<pubkey>`).
 *
 * On mobile (<=640px) and desktop, `.node-top-row .node-qr-wrap` (the QR
 * overlay box) must NOT have meaningful empty translucent space below the
 * QR canvas. The wrap's bounding-rect height must be ≤ the inner QR
 * canvas/svg height + 32px (covers padding + caption + minor rounding).
 *
 * Asserted on:
 *   - 375x800  (mobile — overlay style applies)
 *   - 1280x800 (desktop guard — existing flex layout must not regress)
 *
 * RED on master (mobile): the absolute-positioned overlay inherits the
 * column flex layout with `justify-content: center` and the caption hidden
 * but still allocates space because the wrap doesn't have a content-fit
 * height.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1273-qr-overlay-height-e2e.js
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

async function pickPubkey(page) {
  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  return await page.evaluate(async () => {
    const r = await fetch('/api/nodes?limit=20');
    const d = await r.json();
    return (d.nodes || [])[0] && (d.nodes || [])[0].public_key;
  });
}

async function measureOverlay(page, pubkey) {
  await page.goto(BASE + '/#/nodes/' + encodeURIComponent(pubkey),
    { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.node-top-row .node-qr-wrap', { timeout: 10000 });
  // Wait until the QR svg is actually painted inside the wrap.
  await page.waitForFunction(() => {
    const wrap = document.querySelector('.node-top-row .node-qr-wrap');
    return wrap && wrap.querySelector('.node-qr svg');
  }, { timeout: 10000 });
  await page.waitForTimeout(150); // allow layout to settle
  return await page.evaluate(() => {
    const wrap = document.querySelector('.node-top-row .node-qr-wrap');
    const svg  = wrap.querySelector('.node-qr svg');
    const wr = wrap.getBoundingClientRect();
    const sr = svg.getBoundingClientRect();
    const cap = wrap.querySelector('.mono');
    const capH = cap && getComputedStyle(cap).display !== 'none'
      ? Math.round(cap.getBoundingClientRect().height) : 0;
    // QR is always square — the visible/intended QR height is the SMALLER
    // of svg width vs svg height. Any "extra" svg height beyond that is
    // wasted intrinsic-sizing space that bloats the wrap.
    const qrVisibleH = Math.min(Math.round(sr.width), Math.round(sr.height));
    return {
      wrapH: Math.round(wr.height),
      wrapW: Math.round(wr.width),
      svgH:  Math.round(sr.height),
      svgW:  Math.round(sr.width),
      qrVisibleH,
      capH,
      position: getComputedStyle(wrap).position,
      top: Math.round(wr.top),
      right: Math.round(window.innerWidth - wr.right),
    };
  });
}

(async () => {
  const launchOpts = { headless: true, args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'] };
  if (process.env.CHROMIUM_PATH) launchOpts.executablePath = process.env.CHROMIUM_PATH;
  const browser = await chromium.launch(launchOpts);

  console.log(`\n=== #1273 QR overlay height E2E against ${BASE} ===`);

  // ── pick a real pubkey from the API ──
  const ctxBoot = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const probe = await ctxBoot.newPage();
  const pubkey = await pickPubkey(probe);
  await ctxBoot.close();
  assert(pubkey, 'No pubkey returned from /api/nodes');
  console.log('  → probe pubkey: ' + pubkey.slice(0, 12) + '…');

  // ── Mobile 375x800 ──
  const m = await browser.newContext({ viewport: { width: 375, height: 800 } });
  const mp = await m.newPage();
  await step('mobile 375x800: .node-qr-wrap height \u2264 visible QR + 32px', async () => {
    const d = await measureOverlay(mp, pubkey);
    console.log('    mobile measurements: ' + JSON.stringify(d));
    assert(d.qrVisibleH > 0, 'QR svg has zero visible square (not rendered)');
    assert(d.position === 'absolute',
      'mobile overlay must remain position:absolute, got ' + d.position);
    assert(d.wrapH <= d.qrVisibleH + d.capH + 32,
      `wrap height ${d.wrapH}px must be \u2264 visible QR ${d.qrVisibleH}px + caption ${d.capH}px + 32px (delta ${d.wrapH - d.qrVisibleH - d.capH}px)`);
  });
  await m.close();

  // ── Desktop 1280x800 (regression guard) ──
  const dctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const dp = await dctx.newPage();
  await step('desktop 1280x800: .node-qr-wrap height \u2264 visible QR + caption + 32px', async () => {
    const d = await measureOverlay(dp, pubkey);
    console.log('    desktop measurements: ' + JSON.stringify(d));
    assert(d.qrVisibleH > 0, 'QR svg has zero visible square (not rendered)');
    assert(d.wrapH <= d.qrVisibleH + d.capH + 32,
      `wrap height ${d.wrapH}px must be \u2264 visible QR ${d.qrVisibleH}px + caption ${d.capH}px + 32px (delta ${d.wrapH - d.qrVisibleH - d.capH}px)`);
  });
  await dctx.close();

  await browser.close();

  console.log('\n' + passed + '/' + (passed + failed) + ' tests passed' +
              (failed ? ', ' + failed + ' failed' : ''));
  process.exit(failed > 0 ? 1 : 0);
}
)().catch(err => { console.error('Fatal:', err); process.exit(1); });
