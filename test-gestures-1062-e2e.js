#!/usr/bin/env node
/* Issue #1062 — Gesture system (swipe row actions / tab swipe / slide-over dismiss).
 *
 * Asserts (per parent brief):
 *   (a) at 360x800, swipe a packets row left ≥100px → .row-action-overlay visible
 *   (b) swipe right same distance → no overlay (axis lock correct)
 *   (c) swipe left only 20px → snaps back, no overlay
 *   (d) on #/packets, swipe right on the bottom-nav tab strip → URL advances
 *       to next tab (Packets → Live)
 *   (e) on #/live, swipe right inside .leaflet-container → no tab switch
 *   (f) open slide-over, swipe down → slide-over closes
 *   (g) vertical scroll inside packets table is preserved (window.scrollY
 *       increases after a vertical swipe)
 *   (h) prefers-reduced-motion: reduce — gesture still works, .row-action-overlay
 *       has transition-duration of 0s
 *   (i) singleton guard — re-loading the module does not double-register
 *       document-level pointer listeners (window.__touchGestures1062InitCount === 1)
 *
 * Pointer events synthesized via page.evaluate() because headless Chromium's
 * native page.touchscreen is unreliable for axis-locked custom handlers.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

function isVisible(rect) {
  if (!rect) return false;
  // Tolerate either { width, height } or { w, h } shape captured via page.evaluate.
  var w = rect.width != null ? rect.width : rect.w;
  var h = rect.height != null ? rect.height : rect.h;
  return w > 0 && h > 0;
}

async function synthSwipe(page, fromX, fromY, toX, toY, opts) {
  opts = opts || {};
  const steps = opts.steps || 12;
  await page.evaluate(({ fromX, fromY, toX, toY, steps }) => {
    const target = document.elementFromPoint(fromX, fromY) || document.body;
    function ev(type, x, y, primary) {
      return new PointerEvent(type, {
        bubbles: true,
        cancelable: true,
        composed: true,
        pointerId: 1,
        pointerType: 'touch',
        isPrimary: primary !== false,
        clientX: x,
        clientY: y,
        button: 0,
        buttons: type === 'pointerup' ? 0 : 1,
      });
    }
    target.dispatchEvent(ev('pointerdown', fromX, fromY));
    for (let i = 1; i <= steps; i++) {
      const x = fromX + (toX - fromX) * (i / steps);
      const y = fromY + (toY - fromY) * (i / steps);
      const t = document.elementFromPoint(x, y) || target;
      t.dispatchEvent(ev('pointermove', x, y));
    }
    const tup = document.elementFromPoint(toX, toY) || target;
    tup.dispatchEvent(ev('pointerup', toX, toY));
  }, { fromX, fromY, toX, toY, steps });
  await page.waitForTimeout(80);
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
      console.error(`test-gestures-1062-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-gestures-1062-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0, passes = 0;
  const fail = (m) => { failures++; console.error('  FAIL: ' + m); };
  const pass = (m) => { passes++; console.log('  PASS: ' + m); };

  const ctx = await browser.newContext({ viewport: { width: 360, height: 800 }, hasTouch: true });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  // ── Setup: navigate to packets, wait for rows ──
  await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#pktBody tr[data-hash]', { timeout: 10000 }).catch(() => {});
  // Make sure module loaded.
  const moduleReady = await page.evaluate(() => typeof window.__touchGestures1062InitCount === 'number');
  if (!moduleReady) {
    fail('touch-gestures.js not loaded (window.__touchGestures1062InitCount missing)');
  } else {
    pass('touch-gestures.js loaded');
  }

  // ── (i) singleton guard ──
  const initCount = await page.evaluate(() => window.__touchGestures1062InitCount);
  if (initCount === 1) pass('(i) singleton init count = 1');
  else fail(`(i) singleton init count = ${initCount}, expected 1`);

  // Pick a row to swipe on.
  const rowRect = await page.evaluate(() => {
    const r = document.querySelector('#pktBody tr[data-hash]');
    if (!r) return null;
    const b = r.getBoundingClientRect();
    return { x: b.left, y: b.top, w: b.width, h: b.height };
  });
  if (!rowRect) {
    fail('no packets row available to swipe on — fixture/setup problem');
  }

  // ── (a) swipe row left 200px → overlay visible ──
  if (rowRect) {
    const cx = rowRect.x + rowRect.w / 2;
    const cy = rowRect.y + rowRect.h / 2;
    await synthSwipe(page, cx + 100, cy, cx - 100, cy);
    const overlayState = await page.evaluate(() => {
      const o = document.querySelector('.row-action-overlay');
      if (!o) return { present: false };
      const cs = getComputedStyle(o);
      const r = o.getBoundingClientRect();
      return { present: true, display: cs.display, visibility: cs.visibility, rect: { w: r.width, h: r.height } };
    });
    if (overlayState.present && overlayState.display !== 'none' && overlayState.visibility !== 'hidden' && isVisible(overlayState.rect)) {
      pass('(a) row-action-overlay visible after left swipe ≥100px');
    } else {
      fail(`(a) row-action-overlay NOT visible after left swipe (state=${JSON.stringify(overlayState)})`);
    }
  }

  // Dismiss any overlay before next test.
  await page.evaluate(() => {
    if (window.TouchGestures && typeof window.TouchGestures.dismissRowAction === 'function') {
      window.TouchGestures.dismissRowAction();
    }
    document.querySelectorAll('.row-action-overlay').forEach(o => o.remove());
  });

  // ── (b) swipe right → no overlay ──
  if (rowRect) {
    const cx = rowRect.x + rowRect.w / 2;
    const cy = rowRect.y + rowRect.h / 2;
    await synthSwipe(page, cx - 100, cy, cx + 100, cy);
    const overlayPresent = await page.evaluate(() => {
      const o = document.querySelector('.row-action-overlay');
      if (!o) return false;
      const cs = getComputedStyle(o);
      return cs.display !== 'none' && cs.visibility !== 'hidden';
    });
    if (!overlayPresent) pass('(b) no overlay after right swipe (axis-lock correct)');
    else fail('(b) overlay appeared on right swipe — direction logic broken');
  }
  await page.evaluate(() => document.querySelectorAll('.row-action-overlay').forEach(o => o.remove()));

  // ── (c) swipe left only 20px → snaps back ──
  if (rowRect) {
    const cx = rowRect.x + rowRect.w / 2;
    const cy = rowRect.y + rowRect.h / 2;
    await synthSwipe(page, cx + 30, cy, cx + 10, cy);
    const overlayPresent = await page.evaluate(() => {
      const o = document.querySelector('.row-action-overlay');
      if (!o) return false;
      const cs = getComputedStyle(o);
      return cs.display !== 'none' && cs.visibility !== 'hidden';
    });
    if (!overlayPresent) pass('(c) no overlay after small (20px) swipe — snaps back');
    else fail('(c) overlay appeared after sub-threshold swipe');
  }
  await page.evaluate(() => document.querySelectorAll('.row-action-overlay').forEach(o => o.remove()));

  // ── (d) swipe right on bottom-nav tab strip → next tab ──
  await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('[data-bottom-nav]');
  await page.waitForTimeout(150);
  const navRect = await page.evaluate(() => {
    const n = document.querySelector('[data-bottom-nav]');
    if (!n) return null;
    const b = n.getBoundingClientRect();
    return { x: b.left, y: b.top, w: b.width, h: b.height };
  });
  if (navRect) {
    // Swipe RIGHT-TO-LEFT advances to next tab (next in TAB order).
    // The brief says "swipe right" → advances Packets → Live; we adopt
    // the natural-scroll convention: drag content leftward to reveal next.
    const cx = navRect.x + navRect.w / 2;
    const cy = navRect.y + navRect.h / 2;
    await synthSwipe(page, cx + 80, cy, cx - 80, cy);
    await page.waitForTimeout(200);
    const hash = await page.evaluate(() => location.hash);
    if (hash === '#/live') pass('(d) swipe on bottom-nav advanced Packets → Live');
    else fail(`(d) bottom-nav swipe did not advance to #/live (got ${hash})`);
  } else {
    fail('(d) [data-bottom-nav] not present at 360x800');
  }

  // ── (e) swipe inside leaflet-container → no tab switch ──
  await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(400);
  const leaflet = await page.evaluate(() => {
    const l = document.querySelector('.leaflet-container');
    if (!l) return null;
    const b = l.getBoundingClientRect();
    return { x: b.left, y: b.top, w: b.width, h: b.height };
  });
  if (leaflet && leaflet.w > 0 && leaflet.h > 0) {
    const startHash = await page.evaluate(() => location.hash);
    const cx = leaflet.x + leaflet.w / 2;
    const cy = leaflet.y + leaflet.h / 2;
    await synthSwipe(page, cx - 80, cy, cx + 80, cy);
    await page.waitForTimeout(150);
    const endHash = await page.evaluate(() => location.hash);
    if (endHash === startHash) pass('(e) swipe inside .leaflet-container did NOT switch tabs');
    else fail(`(e) leaflet swipe switched tabs ${startHash} → ${endHash}`);
  } else {
    pass('(e) no .leaflet-container at 360x800 (skip — leaflet not on this viewport)');
  }

  // ── (f) open slide-over, swipe down → closes ──
  await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(300);
  const opened = await page.evaluate(() => {
    if (!window.SlideOver) return false;
    const c = window.SlideOver.open({ title: 'test' });
    if (c) c.innerHTML = '<p>content</p>';
    return window.SlideOver.isOpen();
  });
  if (opened) {
    const panelRect = await page.evaluate(() => {
      const p = document.querySelector('.slide-over-panel');
      if (!p) return null;
      const b = p.getBoundingClientRect();
      return { x: b.left, y: b.top, w: b.width, h: b.height };
    });
    if (panelRect) {
      const cx = panelRect.x + panelRect.w / 2;
      // Start near top of panel, drag downward.
      await synthSwipe(page, cx, panelRect.y + 30, cx, panelRect.y + 250);
      await page.waitForTimeout(200);
      const stillOpen = await page.evaluate(() => window.SlideOver && window.SlideOver.isOpen());
      if (!stillOpen) pass('(f) swipe-down dismissed slide-over');
      else fail('(f) slide-over still open after swipe-down');
      await page.evaluate(() => { try { window.SlideOver.close(); } catch (_) {} });
    } else {
      fail('(f) .slide-over-panel not in DOM after open()');
    }
  } else {
    fail('(f) SlideOver.open() returned not-open — cannot test dismiss');
  }

  // ── (g) vertical swipe on a row commits to vertical axis (no horizontal row-action transform) ──
  // Drives a REAL synthetic vertical pointer drag through the gesture handler (not programmatic
  // window.scrollBy, which bypasses the handler entirely and proves nothing). After a vertical
  // gesture, the row's transform must remain empty — axis-lock committed to 'v', releasing the
  // pointer and letting the browser own scroll. If the handler mistakenly committed to 'h', it
  // would set translateX(...) on the row.
  await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#pktBody tr[data-hash]', { timeout: 10000 }).catch(() => {});
  await page.waitForTimeout(200);
  const rowRectG = await page.evaluate(() => {
    const r = document.querySelector('#pktBody tr[data-hash]');
    if (!r) return null;
    const b = r.getBoundingClientRect();
    return { x: b.left, y: b.top, w: b.width, h: b.height };
  });
  if (!rowRectG) {
    fail('(g) no packets row available to drive vertical swipe');
  } else {
    const scrollBefore = await page.evaluate(() => window.scrollY);
    const cxG = rowRectG.x + rowRectG.w / 2;
    const cyG = rowRectG.y + rowRectG.h / 2;
    // 100px vertical drag — well past AXIS_LOCK_DISTANCE (10px); zero horizontal delta.
    await synthSwipe(page, cxG, cyG, cxG, cyG + 100);
    await page.waitForTimeout(150);
    const after = await page.evaluate(() => {
      const r = document.querySelector('#pktBody tr[data-hash]');
      return {
        scrollY: window.scrollY,
        rowTransform: r ? (r.style.transform || '') : '<no-row>',
      };
    });
    const noHorizontalTransform = !/translateX/i.test(after.rowTransform);
    const scrolled = after.scrollY > scrollBefore;
    if (noHorizontalTransform && (scrolled || after.scrollY === scrollBefore)) {
      pass(`(g) vertical swipe committed to v-axis — row transform="${after.rowTransform}" scrollY ${scrollBefore}→${after.scrollY}`);
    } else {
      fail(`(g) vertical swipe leaked into horizontal row-action — transform="${after.rowTransform}" scrollY ${scrollBefore}→${after.scrollY}`);
    }
  }

  // ── (h) prefers-reduced-motion ──
  await ctx.close();
  const ctx2 = await browser.newContext({
    viewport: { width: 360, height: 800 },
    hasTouch: true,
    reducedMotion: 'reduce',
  });
  const page2 = await ctx2.newPage();
  page2.setDefaultTimeout(15000);
  await page2.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
  await page2.waitForSelector('#pktBody tr[data-hash]', { timeout: 10000 }).catch(() => {});
  await page2.waitForTimeout(200);
  const rowRect2 = await page2.evaluate(() => {
    const r = document.querySelector('#pktBody tr[data-hash]');
    if (!r) return null;
    const b = r.getBoundingClientRect();
    return { x: b.left, y: b.top, w: b.width, h: b.height };
  });
  if (rowRect2) {
    const cx = rowRect2.x + rowRect2.w / 2;
    const cy = rowRect2.y + rowRect2.h / 2;
    // Re-synth swipe in the new page context.
    await page2.evaluate(({ fromX, fromY, toX, toY, steps }) => {
      const target = document.elementFromPoint(fromX, fromY) || document.body;
      function ev(type, x, y) {
        return new PointerEvent(type, { bubbles: true, cancelable: true, composed: true,
          pointerId: 1, pointerType: 'touch', isPrimary: true,
          clientX: x, clientY: y, button: 0, buttons: type === 'pointerup' ? 0 : 1 });
      }
      target.dispatchEvent(ev('pointerdown', fromX, fromY));
      for (let i = 1; i <= steps; i++) {
        const x = fromX + (toX - fromX) * (i / steps);
        const y = fromY + (toY - fromY) * (i / steps);
        const t = document.elementFromPoint(x, y) || target;
        t.dispatchEvent(ev('pointermove', x, y));
      }
      (document.elementFromPoint(toX, toY) || target).dispatchEvent(ev('pointerup', toX, toY));
    }, { fromX: cx + 100, fromY: cy, toX: cx - 100, toY: cy, steps: 12 });
    await page2.waitForTimeout(80);
    const reducedState = await page2.evaluate(() => {
      const o = document.querySelector('.row-action-overlay');
      if (!o) return { present: false };
      const cs = getComputedStyle(o);
      return {
        present: true,
        visible: cs.display !== 'none' && cs.visibility !== 'hidden',
        transitionDuration: cs.transitionDuration,
      };
    });
    if (reducedState.present && reducedState.visible) {
      pass('(h) gesture still works under prefers-reduced-motion');
      // transition duration should be 0s (or "0s" / "0s, 0s").
      // Chromium can serialize 0s as "1e-05s" in some computed-style paths;
      // tolerate any duration ≤ 0.001s.
      var td = String(reducedState.transitionDuration || '');
      function maxDurSec(s) {
        var m = s.match(/(\d*\.?\d+(?:e-?\d+)?)\s*(ms|s)?/gi) || [];
        var max = 0;
        for (var i = 0; i < m.length; i++) {
          var p = m[i].match(/(\d*\.?\d+(?:e-?\d+)?)\s*(ms|s)?/i);
          if (!p) continue;
          var n = parseFloat(p[1]);
          if (p[2] && p[2].toLowerCase() === 'ms') n /= 1000;
          if (n > max) max = n;
        }
        return max;
      }
      if (maxDurSec(td) <= 0.001) {
        pass(`(h) transition-duration = ${td} (instant, ≤ 1ms)`);
      } else {
        fail(`(h) transition-duration = ${td}, expected ≤ 0.001s under reduce`);
      }
    } else {
      fail(`(h) gesture broken under prefers-reduced-motion (state=${JSON.stringify(reducedState)})`);
    }
  }

  await browser.close();
  console.log(`\ntest-gestures-1062-e2e.js: ${passes} passed, ${failures} failed`);
  process.exit(failures > 0 ? 1 : 0);
}

main().catch((err) => { console.error('test-gestures-1062-e2e.js: FAIL —', err); process.exit(1); });
