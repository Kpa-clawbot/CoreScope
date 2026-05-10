#!/usr/bin/env node
/**
 * Issue #1064 — Edge-swipe nav drawer (parent epic #1052).
 *
 * Asserts:
 *   (a) at 1024x800: touch pointer-down at x=30, drag to x=220 → drawer opens
 *       (24px iOS back-swipe reservation + 24-44px drawer trigger zone),
 *       drawer.getBoundingClientRect().left === 0
 *   (b) drawer items present (long-tail routes from PR #1174)
 *   (c) tap a drawer item → URL hash changes, drawer closes
 *   (d) Esc closes drawer
 *   (e) backdrop click closes drawer
 *   (f) at 360x800: edge-swipe does NOT open drawer (Option A —
 *       drawer disabled at narrow widths because bottom-nav has More tab)
 *   (g) singleton: navigate away+back 5 times, pointermove bind count ≤ 1
 *   (h) focus trap: open drawer, Tab from last focusable wraps to first
 *
 * Stable selectors (consumed by the test):
 *   - <aside data-nav-drawer> ............. drawer panel
 *   - [data-nav-drawer-backdrop] .......... backdrop
 *   - [data-nav-drawer-item="<route>"] .... drawer route links
 *   - window.__navDrawer .................. { open(), close(), isOpen() }
 *   - window.__navDrawerPointerBindCount .. integer, MUST be ≤ 1 across SPA navs
 *
 * CI gating: CHROMIUM_REQUIRE=1 ⇒ missing/broken Chromium is a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const EXPECTED_LONG_TAIL = ['nodes', 'tools', 'observers', 'analytics', 'perf', 'audio-lab'];

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

// Synthesize a pointer drag at the document level using PointerEvent.
// Headless Chromium supports PointerEvent natively; dispatching them
// directly on document avoids touch-emulation flakiness.
async function edgeSwipe(page, x0, y0, x1, y1, steps) {
  await page.evaluate(({ x0, y0, x1, y1, steps }) => {
    const target = document.elementFromPoint(x0, y0) || document.body;
    function pe(type, x, y) {
      return new PointerEvent(type, {
        bubbles: true,
        cancelable: true,
        composed: true,
        pointerId: 1,
        pointerType: 'touch',
        isPrimary: true,
        clientX: x, clientY: y,
        screenX: x, screenY: y,
        button: 0, buttons: type === 'pointerup' ? 0 : 1,
      });
    }
    target.dispatchEvent(pe('pointerdown', x0, y0));
    const N = Math.max(2, steps | 0);
    for (let i = 1; i <= N; i++) {
      const t = i / N;
      const x = Math.round(x0 + (x1 - x0) * t);
      const y = Math.round(y0 + (y1 - y0) * t);
      document.dispatchEvent(pe('pointermove', x, y));
    }
    document.dispatchEvent(pe('pointerup', x1, y1));
  }, { x0, y0, x1, y1, steps });
}

(async () => {
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
      console.error(`test-nav-drawer-1064-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-nav-drawer-1064-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  console.log(`\n=== #1064 edge-swipe nav drawer E2E against ${BASE} ===`);

  // ── Wide viewport: drawer enabled ──
  const wideCtx = await browser.newContext({ viewport: { width: 1024, height: 800 } });
  const wide = await wideCtx.newPage();
  wide.setDefaultTimeout(10000);
  wide.on('pageerror', (e) => console.error('[pageerror]', e.message));

  await wide.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
  await wide.waitForSelector('main#app', { timeout: 8000 });
  await wide.waitForTimeout(300);

  await step('(a) edge-swipe at x=30→220 opens drawer flush at left:0 (24-44px trigger zone)', async () => {
    // Start at x=30: clears the 24px iOS back-swipe reservation zone
    // and falls inside the 24-44px drawer trigger window.
    await edgeSwipe(wide, 30, 400, 220, 400, 12);
    await wide.waitForTimeout(250);
    const rect = await wide.evaluate(() => {
      const d = document.querySelector('[data-nav-drawer]');
      if (!d) return null;
      const r = d.getBoundingClientRect();
      return { left: r.left, width: r.width, isOpen: !!(window.__navDrawer && window.__navDrawer.isOpen && window.__navDrawer.isOpen()) };
    });
    assert(rect, '[data-nav-drawer] not in DOM');
    assert(rect.isOpen, 'drawer.isOpen() returned false after edge-swipe');
    assert(rect.left === 0, `drawer.getBoundingClientRect().left expected 0, got ${rect.left}`);
    assert(rect.width > 0, 'drawer width is 0');
  });

  await step('(b) drawer contains long-tail routes from PR #1174', async () => {
    const items = await wide.evaluate((expected) => {
      return expected.map((r) => ({
        route: r,
        present: !!document.querySelector(`[data-nav-drawer-item="${r}"]`),
      }));
    }, EXPECTED_LONG_TAIL);
    const missing = items.filter((it) => !it.present).map((it) => it.route);
    assert(missing.length === 0, `missing drawer items: ${missing.join(', ')}`);
  });

  await step('(c) tapping a drawer item navigates and closes drawer', async () => {
    // Open drawer (in case prior step left it closed) then click an item.
    await wide.evaluate(() => window.__navDrawer && window.__navDrawer.open && window.__navDrawer.open());
    await wide.waitForTimeout(150);
    await wide.click('[data-nav-drawer-item="tools"]');
    await wide.waitForTimeout(200);
    const result = await wide.evaluate(() => ({
      hash: location.hash,
      isOpen: !!(window.__navDrawer && window.__navDrawer.isOpen && window.__navDrawer.isOpen()),
    }));
    assert(result.hash.indexOf('#/tools') === 0, `expected hash #/tools, got ${result.hash}`);
    assert(!result.isOpen, 'drawer should have closed after item tap');
  });

  await step('(d) Esc closes drawer', async () => {
    await wide.evaluate(() => window.__navDrawer && window.__navDrawer.open && window.__navDrawer.open());
    await wide.waitForTimeout(150);
    await wide.keyboard.press('Escape');
    await wide.waitForTimeout(150);
    const open = await wide.evaluate(() => !!(window.__navDrawer && window.__navDrawer.isOpen && window.__navDrawer.isOpen()));
    assert(!open, 'drawer still open after Esc');
  });

  await step('(d2) close() restores focus to previously-focused element (#1168 regression class)', async () => {
    // Park focus on a sentinel button outside the drawer; open drawer; close it; assert focus came back.
    await wide.evaluate(() => {
      var btn = document.getElementById('__nav_drawer_focus_sentinel');
      if (!btn) {
        btn = document.createElement('button');
        btn.id = '__nav_drawer_focus_sentinel';
        btn.textContent = 'sentinel';
        btn.style.position = 'fixed';
        btn.style.top = '-9999px';
        document.body.appendChild(btn);
      }
      btn.focus();
    });
    const beforeOk = await wide.evaluate(() => document.activeElement && document.activeElement.id === '__nav_drawer_focus_sentinel');
    assert(beforeOk, 'failed to focus sentinel button before opening drawer');
    await wide.evaluate(() => window.__navDrawer.open());
    await wide.waitForTimeout(120);
    // Confirm focus actually moved into drawer (precondition for the restore check).
    const inside = await wide.evaluate(() => {
      var d = document.querySelector('[data-nav-drawer]');
      return !!(d && d.contains(document.activeElement));
    });
    assert(inside, 'open() did not move focus into drawer');
    await wide.evaluate(() => window.__navDrawer.close());
    await wide.waitForTimeout(120);
    const restored = await wide.evaluate(() => document.activeElement && document.activeElement.id === '__nav_drawer_focus_sentinel');
    assert(restored, 'close() did not restore focus to the previously-focused element');
  });

  await step('(e) backdrop click closes drawer', async () => {
    await wide.evaluate(() => window.__navDrawer && window.__navDrawer.open && window.__navDrawer.open());
    await wide.waitForTimeout(150);
    const has = await wide.$('[data-nav-drawer-backdrop]');
    assert(has, '[data-nav-drawer-backdrop] missing');
    // Click far right of viewport: backdrop covers the whole window
    // (position:fixed; inset:0), but the drawer (z-index 1260) sits on
    // top of the backdrop (z-index 1250) over the left ~320px. Clicking
    // near the left edge would hit the drawer instead of the backdrop
    // (Playwright actionability check would time out). Compute a point
    // clearly outside the drawer's bounds at the current viewport so this
    // is robust to viewport changes.
    const vp = wide.viewportSize();
    const clickX = Math.max(400, vp.width - 50);
    await wide.click('[data-nav-drawer-backdrop]', { position: { x: clickX, y: Math.floor(vp.height / 2) } });
    await wide.waitForTimeout(150);
    const open = await wide.evaluate(() => !!(window.__navDrawer && window.__navDrawer.isOpen && window.__navDrawer.isOpen()));
    assert(!open, 'drawer still open after backdrop click');
  });

  await step('(g) singleton: 5 SPA round-trips keep pointermove bind count ≤ 1', async () => {
    for (let i = 0; i < 5; i++) {
      await wide.evaluate(() => { location.hash = '#/packets'; });
      await wide.waitForTimeout(80);
      await wide.evaluate(() => { location.hash = '#/map'; });
      await wide.waitForTimeout(80);
    }
    const count = await wide.evaluate(() => window.__navDrawerPointerBindCount);
    assert(typeof count === 'number',
      'window.__navDrawerPointerBindCount missing — debug seam not exposed by nav-drawer.js');
    assert(count <= 1,
      `nav-drawer pointermove handler bind count = ${count}, expected ≤ 1 (singleton)`);
  });

  await step('(h) focus trap: Tab from last focusable wraps to first', async () => {
    await wide.evaluate(() => window.__navDrawer && window.__navDrawer.open && window.__navDrawer.open());
    await wide.waitForTimeout(150);
    // Focus the LAST focusable inside the drawer, then press Tab.
    const wrapped = await wide.evaluate(async () => {
      const drawer = document.querySelector('[data-nav-drawer]');
      if (!drawer) return { error: 'no drawer' };
      const focusables = drawer.querySelectorAll(
        'a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"]), input, select, textarea'
      );
      if (focusables.length < 2) return { error: 'fewer than 2 focusables in drawer' };
      const first = focusables[0];
      const last = focusables[focusables.length - 1];
      last.focus();
      return { firstId: first.getAttribute('data-nav-drawer-item') || first.id || first.textContent.trim(),
               lastFocused: document.activeElement === last };
    });
    assert(!wrapped.error, wrapped.error);
    assert(wrapped.lastFocused, 'failed to focus last drawer focusable');
    await wide.keyboard.press('Tab');
    const wrappedToFirst = await wide.evaluate(() => {
      const drawer = document.querySelector('[data-nav-drawer]');
      const focusables = drawer.querySelectorAll(
        'a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"]), input, select, textarea'
      );
      return document.activeElement === focusables[0];
    });
    assert(wrappedToFirst, 'Tab from last focusable did NOT wrap to first (focus trap broken)');
  });

  await step('(i) mouse-down at left edge does NOT open drawer (pointerType=mouse must be ignored)', async () => {
    // Ensure drawer is closed before the assertion.
    await wide.evaluate(() => window.__navDrawer && window.__navDrawer.close && window.__navDrawer.close());
    await wide.waitForTimeout(120);
    // Use Playwright's real mouse API — emits PointerEvent with pointerType="mouse".
    await wide.mouse.move(30, 400); // x=30 is inside the touch trigger zone (24-44) so the only thing rejecting this drag is the pointerType filter
    await wide.mouse.down();
    await wide.mouse.move(240, 400, { steps: 12 });
    await wide.mouse.up();
    await wide.waitForTimeout(200);
    const open = await wide.evaluate(() => !!(window.__navDrawer && window.__navDrawer.isOpen && window.__navDrawer.isOpen()));
    assert(!open, 'drawer opened on mouse drag from left edge — pointerdown must reject pointerType=mouse');
  });

  await step('(j) touch swipe from x=10 (inside iOS back-swipe reservation) does NOT open drawer', async () => {
    await wide.evaluate(() => window.__navDrawer && window.__navDrawer.close && window.__navDrawer.close());
    await wide.waitForTimeout(120);
    // x=10 is inside the 24px reservation zone for iOS back-swipe — drawer must NOT open.
    await edgeSwipe(wide, 10, 400, 220, 400, 12);
    await wide.waitForTimeout(200);
    const open = await wide.evaluate(() => !!(window.__navDrawer && window.__navDrawer.isOpen && window.__navDrawer.isOpen()));
    assert(!open, 'drawer opened on touch swipe from x=10 — first 24px must be reserved for iOS back-swipe');
  });

  await wideCtx.close();

  // ── Narrow viewport (Option A): drawer disabled ──
  const narrowCtx = await browser.newContext({ viewport: { width: 360, height: 800 } });
  const narrow = await narrowCtx.newPage();
  narrow.setDefaultTimeout(10000);
  narrow.on('pageerror', (e) => console.error('[pageerror]', e.message));

  await narrow.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
  await narrow.waitForSelector('main#app', { timeout: 8000 });
  await narrow.waitForTimeout(300);

  await step('(f) narrow viewport: edge-swipe does NOT open drawer (Option A)', async () => {
    await edgeSwipe(narrow, 30, 400, 220, 400, 12);
    await narrow.waitForTimeout(250);
    const open = await narrow.evaluate(() => {
      if (!window.__navDrawer || !window.__navDrawer.isOpen) return false;
      return window.__navDrawer.isOpen();
    });
    assert(!open, 'drawer opened at narrow viewport (≤768px) — Option A says it must stay closed');
  });

  await narrowCtx.close();
  await browser.close();
  console.log(`\n=== Results: passed ${passed} failed ${failed} ===`);
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
