/**
 * E2E (#1173): Replace #liveDot WebSocket indicator with packet-driven
 * brand-logo node-pulse animation.
 *
 * Red-then-green pattern (per AGENTS.md TDD rule). This file is committed
 * BEFORE the implementation; CI must FAIL on assertion (not import error).
 *
 * The implementation must expose a deterministic test hook on
 *   window.__corescopeLogo
 * with the following surface (pure CSS animations, no per-frame mutation):
 *   .pulse(msg)        — simulate one WS message arrival (rate-gated)
 *   .setConnected(b)   — simulate connect/disconnect class toggle
 *   .lastDirection     — 'a' or 'b' — direction of most recent ping
 *   .stats             — { triggered, dropped }
 * Implementations may also wire real WS handlers; this hook is the test seam.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-logo-pulse-1173-e2e.js
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

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log(`\n=== #1173 logo-pulse E2E against ${BASE} ===`);

  // ---- Default viewport (full brand-logo SVG visible) ----
  {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));
    await page.goto(BASE + '/#/home', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.brand-logo', { timeout: 8000 });
    // Wait for app boot — hook should be installed during connectWS().
    await page.waitForFunction(() => !!(window.__corescopeLogo && typeof window.__corescopeLogo.pulse === 'function'), null, { timeout: 8000 }).catch(()=>{});

    // (a) #liveDot must NOT exist anywhere in the document.
    await step('#liveDot is removed from the DOM', async () => {
      const found = await page.evaluate(() => !!document.getElementById('liveDot'));
      assert(!found, '#liveDot still present in DOM');
    });

    // (b) Both .brand-logo and .brand-mark-only carry the new pulse classes
    //     on their two inner circles.
    await step('both logo SVGs have .logo-node-a and .logo-node-b circles', async () => {
      const info = await page.evaluate(() => {
        function probe(parentSel) {
          const p = document.querySelector(parentSel);
          if (!p) return { exists: false };
          const a = p.querySelector('circle.logo-node-a');
          const b = p.querySelector('circle.logo-node-b');
          return { exists: true, hasA: !!a, hasB: !!b,
            aCx: a && a.getAttribute('cx'), bCx: b && b.getAttribute('cx') };
        }
        return { full: probe('.brand-logo'), mark: probe('.brand-mark-only') };
      });
      assert(info.full.exists && info.full.hasA && info.full.hasB,
        '.brand-logo missing pulse classes: ' + JSON.stringify(info.full));
      assert(info.mark.exists && info.mark.hasA && info.mark.hasB,
        '.brand-mark-only missing pulse classes: ' + JSON.stringify(info.mark));
      assert(info.full.aCx === '540' && info.full.bCx === '660',
        'pulse classes attached to wrong circles (expected cx=540/660): ' + JSON.stringify(info.full));
    });

    // (c) Test hook installed and pulse() toggles a class on the source circle.
    await step('window.__corescopeLogo.pulse() toggles .logo-pulse-active on source circle', async () => {
      const r = await page.evaluate(async () => {
        if (!window.__corescopeLogo || typeof window.__corescopeLogo.pulse !== 'function') {
          return { hookMissing: true };
        }
        const a = document.querySelector('.brand-logo circle.logo-node-a');
        const b = document.querySelector('.brand-logo circle.logo-node-b');
        const before = { a: a.classList.contains('logo-pulse-active'),
                         b: b.classList.contains('logo-pulse-active') };
        window.__corescopeLogo.pulse({ synthetic: true });
        // Class must be present synchronously OR within one rAF (≤16ms).
        await new Promise(r => requestAnimationFrame(() => r()));
        const after = { a: a.classList.contains('logo-pulse-active'),
                        b: b.classList.contains('logo-pulse-active'),
                        dir: window.__corescopeLogo.lastDirection };
        return { hookMissing: false, before, after };
      });
      assert(!r.hookMissing, 'window.__corescopeLogo.pulse hook is missing');
      // Either A or B must be active (the source of the first ping).
      assert(r.after.a || r.after.b, 'no circle got .logo-pulse-active after first pulse: ' + JSON.stringify(r));
    });

    // (d) Direction alternates: 4 messages → toggles fire on alternating circles.
    await step('direction alternates A→B / B→A across 4 pings', async () => {
      const dirs = await page.evaluate(async () => {
        // Wait long enough between pings to clear the rate gate.
        const out = [];
        for (let i = 0; i < 4; i++) {
          window.__corescopeLogo.pulse({ synthetic: true });
          out.push(window.__corescopeLogo.lastDirection);
          await new Promise(r => setTimeout(r, 80));
        }
        return out;
      });
      // Expect a strict A,B,A,B (or B,A,B,A) alternation.
      assert(dirs.length === 4, 'expected 4 direction samples, got ' + dirs.length);
      assert(dirs[0] && dirs[1] && dirs[0] !== dirs[1], 'first two pings did not alternate: ' + dirs);
      assert(dirs[2] === dirs[0] && dirs[3] === dirs[1],
        'pings 3/4 did not alternate (expected ' + dirs[0] + ',' + dirs[1] + ',' + dirs[0] + ',' + dirs[1] + ', got ' + dirs.join(',') + ')');
    });

    // (e) Rate cap: 100 synthetic pulses within ~100ms → ≤16 toggles fire.
    await step('rate-cap: 100 pulses in ~100ms drop most (≤16 trigger)', async () => {
      const r = await page.evaluate(async () => {
        const before = Object.assign({}, window.__corescopeLogo.stats);
        const t0 = performance.now();
        for (let i = 0; i < 100; i++) window.__corescopeLogo.pulse({ synthetic: true });
        const t1 = performance.now();
        const after = Object.assign({}, window.__corescopeLogo.stats);
        return { before, after, elapsed: t1 - t0 };
      });
      const triggered = (r.after.triggered || 0) - (r.before.triggered || 0);
      // Permit a small slack — 100 calls in <100ms should produce 1 ping
      // (the rest hit the 66ms gate). Allow up to 16 to avoid flakes if the
      // burst spans a window boundary.
      assert(triggered >= 1 && triggered <= 16,
        'rate-gate fired ' + triggered + ' times (expected 1..16) — stats=' + JSON.stringify(r));
    });

    // (g) Disconnect simulation: setConnected(false) → .logo-disconnected class.
    await step('setConnected(false) puts .logo-disconnected on .brand-logo', async () => {
      const has = await page.evaluate(() => {
        window.__corescopeLogo.setConnected(false);
        const full = document.querySelector('.brand-logo').classList.contains('logo-disconnected');
        const mark = document.querySelector('.brand-mark-only').classList.contains('logo-disconnected');
        // restore for next steps
        window.__corescopeLogo.setConnected(true);
        return { full, mark };
      });
      assert(has.full && has.mark, 'logo-disconnected not applied to both SVG instances: ' + JSON.stringify(has));
    });

    // (h) Theme: pulse circles get fill from --logo-accent / --logo-accent-hi.
    await step('pulse circle fills resolve to --logo-accent/--logo-accent-hi tokens', async () => {
      const r = await page.evaluate(() => {
        const root = document.documentElement;
        const cs = getComputedStyle(root);
        const accent = cs.getPropertyValue('--logo-accent').trim();
        const accentHi = cs.getPropertyValue('--logo-accent-hi').trim();
        const a = document.querySelector('.brand-logo circle.logo-node-a');
        const b = document.querySelector('.brand-logo circle.logo-node-b');
        return {
          accent, accentHi,
          aFill: getComputedStyle(a).fill,
          bFill: getComputedStyle(b).fill,
        };
      });
      assert(r.accent && r.accentHi, '--logo-accent / --logo-accent-hi not defined');
      // Computed fill resolves to rgb(...) — just sanity-check it is non-empty
      // and not the default black/transparent.
      assert(r.aFill && r.aFill !== 'rgb(0, 0, 0)' && r.aFill !== 'rgba(0, 0, 0, 0)', 'node-a fill not themed: ' + r.aFill);
      assert(r.bFill && r.bFill !== 'rgb(0, 0, 0)' && r.bFill !== 'rgba(0, 0, 0, 0)', 'node-b fill not themed: ' + r.bFill);
    });

    await ctx.close();
  }

  // (f) prefers-reduced-motion: blip class differs from chained pulse class.
  {
    const ctx = await browser.newContext({
      viewport: { width: 1280, height: 800 },
      reducedMotion: 'reduce',
    });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    await page.goto(BASE + '/#/home', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.brand-logo', { timeout: 8000 });
    await page.waitForFunction(() => !!(window.__corescopeLogo && typeof window.__corescopeLogo.pulse === 'function'), null, { timeout: 8000 }).catch(()=>{});

    await step('prefers-reduced-motion: blip class is .logo-pulse-blip (not .logo-pulse-active)', async () => {
      const r = await page.evaluate(async () => {
        if (!window.__corescopeLogo) return { hookMissing: true };
        window.__corescopeLogo.pulse({ synthetic: true });
        await new Promise(r => requestAnimationFrame(() => r()));
        const a = document.querySelector('.brand-logo circle.logo-node-a');
        const b = document.querySelector('.brand-logo circle.logo-node-b');
        return {
          hookMissing: false,
          activeA: a.classList.contains('logo-pulse-active'),
          activeB: b.classList.contains('logo-pulse-active'),
          blipA: a.classList.contains('logo-pulse-blip'),
          blipB: b.classList.contains('logo-pulse-blip'),
        };
      });
      assert(!r.hookMissing, 'window.__corescopeLogo hook missing in reduced-motion ctx');
      assert(r.blipA || r.blipB, 'reduced-motion did not toggle .logo-pulse-blip: ' + JSON.stringify(r));
      assert(!(r.activeA || r.activeB), 'reduced-motion incorrectly toggled chained .logo-pulse-active: ' + JSON.stringify(r));
    });

    await ctx.close();
  }

  // ---- Hidden-tab gate (#1177 carmack must-fix #1) ----
  // When document.hidden=true, pulse() must return false BEFORE updating
  // lastPingTs and BEFORE scheduling any rAF/setTimeout chain. No circle
  // class toggles must occur.
  {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    await page.goto(BASE + '/#/home', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.brand-logo', { timeout: 8000 });
    await page.waitForFunction(() => !!(window.__corescopeLogo && typeof window.__corescopeLogo.pulse === 'function'), null, { timeout: 8000 }).catch(()=>{});

    await step('hidden tab: pulse() returns false and toggles no classes', async () => {
      const r = await page.evaluate(async () => {
        Object.defineProperty(document, 'hidden', { value: true, configurable: true });
        Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true });
        const before = Object.assign({}, window.__corescopeLogo.stats);
        const ret = window.__corescopeLogo.pulse({ synthetic: true });
        await new Promise(r => requestAnimationFrame(() => r()));
        const a = document.querySelector('.brand-logo circle.logo-node-a');
        const b = document.querySelector('.brand-logo circle.logo-node-b');
        const after = Object.assign({}, window.__corescopeLogo.stats);
        return {
          ret, before, after,
          activeA: a.classList.contains('logo-pulse-active'),
          activeB: b.classList.contains('logo-pulse-active'),
          blipA: a.classList.contains('logo-pulse-blip'),
          blipB: b.classList.contains('logo-pulse-blip'),
        };
      });
      assert(r.ret === false, 'pulse() should return false when document.hidden=true (got ' + r.ret + ')');
      assert(!r.activeA && !r.activeB, 'logo-pulse-active should not toggle in hidden tab');
      assert(!r.blipA && !r.blipB, 'logo-pulse-blip should not toggle in hidden tab');
      assert((r.after.triggered || 0) === (r.before.triggered || 0),
        'stats.triggered must not increment in hidden tab');
    });

    await ctx.close();
  }

  // ---- matchMedia caching (#1177 carmack must-fix #2) ----
  // The reduced-motion query must be cached at module load. 100 pulses
  // must NOT result in 100 window.matchMedia() calls.
  {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    // Wrap window.matchMedia BEFORE any app script runs.
    await page.addInitScript(() => {
      const orig = window.matchMedia;
      window.__matchMediaCalls = 0;
      window.matchMedia = function (q) {
        try { window.__matchMediaCalls = (window.__matchMediaCalls | 0) + 1; } catch (_) {}
        return orig.call(window, q);
      };
    });
    await page.goto(BASE + '/#/home', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.brand-logo', { timeout: 8000 });
    await page.waitForFunction(() => !!(window.__corescopeLogo && typeof window.__corescopeLogo.pulse === 'function'), null, { timeout: 8000 }).catch(()=>{});

    await step('matchMedia: cached singleton — 100 pulses do not call window.matchMedia per pulse', async () => {
      const r = await page.evaluate(async () => {
        const callsBefore = window.__matchMediaCalls | 0;
        for (let i = 0; i < 100; i++) window.__corescopeLogo.pulse({ synthetic: true });
        await new Promise(r => setTimeout(r, 50));
        const callsAfter = window.__matchMediaCalls | 0;
        return { callsBefore, callsAfter, delta: callsAfter - callsBefore };
      });
      // 100 pulses → matchMedia should NOT be invoked per pulse. Allow 0 (cached).
      assert(r.delta === 0,
        'matchMedia called ' + r.delta + ' times during 100 pulses (expected 0 — should be cached at module load)');
    });

    await ctx.close();
  }

  await browser.close();

  console.log(`\n=== #1173 logo-pulse E2E: ${passed} passed, ${failed} failed ===`);
  process.exit(failed ? 1 : 0);
})();
