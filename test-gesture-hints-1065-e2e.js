#!/usr/bin/env node
/* Issue #1065 — Gesture discoverability hints (first-visit).
 *
 * Asserts (per parent brief):
 *   (a) on first visit at 360x800 + /#/packets, hint balloon visible after page settle,
 *       with role=status / aria-live=polite region containing swipe-row hint text
 *   (b) tap "Got it" → balloon disappears, localStorage `meshcore-gesture-hints-row-swipe`=`seen`
 *   (c) reload → hint NOT shown (flag persists)
 *   (d) clear flag via Settings UI ("Reset gesture hints") → reload → hint shown again
 *   (e) at 1024x800, edge-swipe hint visible
 *   (f) prefers-reduced-motion: reduce — animation-name 'none' (just opacity fade)
 *   (g) hint does NOT steal focus (document.activeElement === document.body after settle)
 *   (h) singleton: 5 SPA round-trips don't re-show dismissed hints
 *
 * Hint timing: brief expects 800ms post-page-settle delay; we wait 1500ms after navigate.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const HINT_SETTLE_MS = 1500;

const KEYS = {
  rowSwipe: 'meshcore-gesture-hints-row-swipe',
  tabSwipe: 'meshcore-gesture-hints-tab-swipe',
  edgeDrawer: 'meshcore-gesture-hints-edge-drawer',
  pullRefresh: 'meshcore-gesture-hints-pull-refresh',
};

async function clearAllHintFlags(page) {
  await page.evaluate((keys) => {
    Object.values(keys).forEach((k) => localStorage.removeItem(k));
  }, KEYS);
}

async function hintVisible(page, hintId) {
  return page.evaluate((id) => {
    const el = document.querySelector('[data-gesture-hint="' + id + '"]');
    if (!el) return { present: false };
    const cs = getComputedStyle(el);
    const r = el.getBoundingClientRect();
    return {
      present: true,
      visible: cs.display !== 'none' && cs.visibility !== 'hidden' && parseFloat(cs.opacity || '1') > 0.01 && r.width > 0 && r.height > 0,
      role: el.getAttribute('role'),
      ariaLive: el.getAttribute('aria-live'),
      text: el.textContent || '',
      animationName: cs.animationName,
      pointerEvents: cs.pointerEvents,
    };
  }, hintId);
}

async function main() {
  const requireChromium = process.env.CHROMIUM_REQUIRE === '1';
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      executablePath: process.env.CHROMIUM_PATH || undefined,
      args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    });
  } catch (err) {
    if (requireChromium) {
      console.error(`test-gesture-hints-1065-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-gesture-hints-1065-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0, passes = 0;
  const fail = (m) => { failures++; console.error('  FAIL: ' + m); };
  const pass = (m) => { passes++; console.log('  PASS: ' + m); };

  // ── (a) first visit on /#/packets at 360x800 → row-swipe hint visible ──
  const ctx = await browser.newContext({ viewport: { width: 360, height: 800 }, hasTouch: true });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  // Clear localStorage before first navigate.
  await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
  await clearAllHintFlags(page);
  // Reload to simulate first-visit cleanly.
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(HINT_SETTLE_MS);

  const moduleReady = await page.evaluate(() => typeof window.__gestureHints1065Init === 'number');
  if (moduleReady) pass('gesture-hints.js loaded (window.__gestureHints1065Init present)');
  else fail('gesture-hints.js NOT loaded (window.__gestureHints1065Init missing)');

  const rowHint = await hintVisible(page, 'row-swipe');
  if (rowHint.present && rowHint.visible) {
    pass('(a) row-swipe hint visible on first visit at /#/packets 360x800');
  } else {
    fail(`(a) row-swipe hint NOT visible — state=${JSON.stringify(rowHint)}`);
  }
  if (rowHint.role === 'status' && rowHint.ariaLive === 'polite') {
    pass('(a) hint has role=status and aria-live=polite');
  } else {
    fail(`(a) hint missing aria — role=${rowHint.role} aria-live=${rowHint.ariaLive}`);
  }
  if (rowHint.pointerEvents === 'none') {
    pass('(a) hint pointer-events: none — does not capture pointer');
  } else {
    fail(`(a) hint pointer-events=${rowHint.pointerEvents}, expected none`);
  }

  // ── (g) does not steal focus ──
  const activeTag = await page.evaluate(() => document.activeElement && document.activeElement.tagName);
  if (activeTag === 'BODY' || activeTag === null || activeTag === 'HTML') {
    pass(`(g) focus not stolen (activeElement=${activeTag})`);
  } else {
    // Allow if active element is not inside the hint.
    const inHint = await page.evaluate(() => {
      const a = document.activeElement;
      if (!a) return false;
      return !!a.closest('[data-gesture-hint]');
    });
    if (!inHint) pass(`(g) focus not in hint (activeElement=${activeTag})`);
    else fail(`(g) hint stole focus to element inside hint (${activeTag})`);
  }

  // ── (b) tap "Got it" → balloon gone, localStorage flag set ──
  const dismissed = await page.evaluate(() => {
    const el = document.querySelector('[data-gesture-hint="row-swipe"]');
    if (!el) return { ok: false, reason: 'no hint' };
    const btn = el.querySelector('[data-gesture-hint-dismiss]');
    if (!btn) return { ok: false, reason: 'no button' };
    btn.click();
    return { ok: true };
  });
  if (!dismissed.ok) fail('(b) cannot dismiss: ' + dismissed.reason);
  await page.waitForTimeout(400);
  const afterDismiss = await page.evaluate((k) => ({
    stillThere: !!document.querySelector('[data-gesture-hint="row-swipe"]'),
    flag: localStorage.getItem(k),
  }), KEYS.rowSwipe);
  if (!afterDismiss.stillThere && afterDismiss.flag === 'seen') {
    pass('(b) "Got it" removed hint and set localStorage flag = "seen"');
  } else {
    fail(`(b) dismiss failed — stillThere=${afterDismiss.stillThere} flag=${afterDismiss.flag}`);
  }

  // ── (c) reload → hint NOT shown ──
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(HINT_SETTLE_MS);
  const afterReload = await hintVisible(page, 'row-swipe');
  if (!afterReload.present || !afterReload.visible) {
    pass('(c) hint NOT shown after reload (flag persisted)');
  } else {
    fail('(c) hint reappeared after reload — flag did not persist');
  }

  // ── (d) clear flag via Settings UI → reload → hint visible again ──
  // Brief asks for a "Reset gesture hints" button. Click it programmatically
  // via the UI element if present; otherwise fall back to direct localStorage clear
  // and FAIL the assertion (the brief requires a UI surface).
  const resetWorked = await page.evaluate(() => {
    // Open customize panel.
    var btn = document.getElementById('customizeToggle');
    if (btn) btn.click();
    // The reset button may live anywhere in the panel; look for it by data-attr.
    var resetBtn = document.querySelector('[data-cv2-reset-hints], [data-reset-gesture-hints]');
    if (!resetBtn) return { ok: false, reason: 'reset button not found' };
    resetBtn.click();
    return { ok: true };
  });
  if (!resetWorked.ok) {
    fail('(d) Settings UI "Reset gesture hints" button not found — ' + resetWorked.reason);
    // Force-clear so subsequent assertions can run.
    await clearAllHintFlags(page);
  } else {
    pass('(d.1) "Reset gesture hints" button clicked');
  }
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(HINT_SETTLE_MS);
  const afterReset = await hintVisible(page, 'row-swipe');
  if (afterReset.present && afterReset.visible) {
    pass('(d.2) hint shown again after settings reset');
  } else {
    fail(`(d.2) hint NOT shown after reset — state=${JSON.stringify(afterReset)}`);
  }

  // ── (h) singleton: 5 SPA round-trips don't re-show dismissed hints ──
  // Dismiss again first.
  await page.evaluate(() => {
    const el = document.querySelector('[data-gesture-hint="row-swipe"]');
    if (el) {
      const b = el.querySelector('[data-gesture-hint-dismiss]');
      if (b) b.click();
    }
  });
  await page.waitForTimeout(300);
  let reShowCount = 0;
  for (let i = 0; i < 5; i++) {
    await page.evaluate(() => { location.hash = '#/nodes'; });
    await page.waitForTimeout(300);
    await page.evaluate(() => { location.hash = '#/packets'; });
    await page.waitForTimeout(800);
    const v = await hintVisible(page, 'row-swipe');
    if (v.present && v.visible) reShowCount++;
  }
  if (reShowCount === 0) pass('(h) 5 SPA round-trips: hint did NOT re-show after dismiss');
  else fail(`(h) hint re-showed ${reShowCount}/5 SPA round-trips after dismiss`);

  await ctx.close();

  // ── (e) at 1024x800, edge-swipe hint visible on first visit ──
  const ctx2 = await browser.newContext({ viewport: { width: 1024, height: 800 } });
  const page2 = await ctx2.newPage();
  await page2.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
  await page2.evaluate((keys) => Object.values(keys).forEach((k) => localStorage.removeItem(k)), KEYS);
  await page2.reload({ waitUntil: 'domcontentloaded' });
  await page2.waitForTimeout(HINT_SETTLE_MS);
  const edgeHint = await hintVisible(page2, 'edge-drawer');
  if (edgeHint.present && edgeHint.visible) {
    pass('(e) edge-drawer hint visible at 1024x800');
  } else {
    fail(`(e) edge-drawer hint NOT visible at 1024x800 — state=${JSON.stringify(edgeHint)}`);
  }
  await ctx2.close();

  // ── (f) prefers-reduced-motion: animation-name = 'none' ──
  const ctx3 = await browser.newContext({ viewport: { width: 360, height: 800 }, hasTouch: true, reducedMotion: 'reduce' });
  const page3 = await ctx3.newPage();
  await page3.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
  await page3.evaluate((keys) => Object.values(keys).forEach((k) => localStorage.removeItem(k)), KEYS);
  await page3.reload({ waitUntil: 'domcontentloaded' });
  await page3.waitForTimeout(HINT_SETTLE_MS);
  const reducedHint = await hintVisible(page3, 'row-swipe');
  if (reducedHint.present && reducedHint.visible) {
    if (reducedHint.animationName === 'none' || reducedHint.animationName === '' || /none/i.test(String(reducedHint.animationName))) {
      pass(`(f) prefers-reduced-motion: animation-name=${reducedHint.animationName} (no slide animation)`);
    } else {
      fail(`(f) reduced-motion: animation-name=${reducedHint.animationName}, expected 'none'`);
    }
  } else {
    fail(`(f) hint not visible under reduced-motion — state=${JSON.stringify(reducedHint)}`);
  }
  await ctx3.close();

  await browser.close();
  console.log(`\ntest-gesture-hints-1065-e2e.js: ${passes} passed, ${failures} failed`);
  process.exit(failures > 0 ? 1 : 0);
}

main().catch((err) => { console.error('test-gesture-hints-1065-e2e.js: FAIL —', err); process.exit(1); });
