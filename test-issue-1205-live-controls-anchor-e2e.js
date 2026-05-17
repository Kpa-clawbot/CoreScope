/**
 * E2E for #1205 — Live settings toggle row must be anchored INSIDE the
 * MESH LIVE panel (`#liveHeader`), not floating free as a sibling
 * `.live-overlay` child of `.live-page` / body / `#liveLegend`.
 *
 * Background: PR #1180 detached `#liveControls` into a separate
 * `position:fixed` overlay. The detached row visually orphaned on many
 * viewports (issue #1205). The correct fix restores the pre-regression
 * structural pattern where the toggles live inside the MESH LIVE panel.
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1205-live-controls-anchor-e2e.js
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

async function gotoLive(page) {
  await page.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#liveHeader', { timeout: 8000 });
  await page.waitForSelector('#liveControls', { timeout: 8000 });
  await page.waitForTimeout(400);
}

async function assertAnchoredToMeshLive(page, label) {
  await step(label + ' #liveControls DOM-parent chain goes through #liveHeader (MESH LIVE panel)', async () => {
    const info = await page.evaluate(() => {
      const ctrl = document.getElementById('liveControls');
      const header = document.getElementById('liveHeader');
      const legend = document.getElementById('liveLegend');
      if (!ctrl || !header) return { ok: false, reason: 'missing elements' };
      // Walk ancestors and record id/class to make failures actionable.
      const chain = [];
      let n = ctrl.parentElement;
      while (n && n !== document.body) {
        chain.push((n.id ? '#' + n.id : '') + '.' + (n.className || '').toString().split(' ').join('.'));
        n = n.parentElement;
      }
      return {
        ok: header.contains(ctrl) && (!legend || !legend.contains(ctrl)),
        parentId: ctrl.parentElement ? ctrl.parentElement.id : '',
        parentClass: ctrl.parentElement ? ctrl.parentElement.className : '',
        chain,
        inHeader: header.contains(ctrl),
        inLegend: !!legend && legend.contains(ctrl),
      };
    });
    assert(info.ok,
      `#liveControls must live inside #liveHeader and NOT inside #liveLegend. ` +
      `parent=${info.parentId || '(no id)'} class="${info.parentClass}" ` +
      `inHeader=${info.inHeader} inLegend=${info.inLegend} ` +
      `chain=${JSON.stringify(info.chain)}`);
  });

  await step(label + ' #liveControls parent is not <body> and not .live-page directly', async () => {
    const r = await page.evaluate(() => {
      const ctrl = document.getElementById('liveControls');
      const p = ctrl.parentElement;
      return {
        tag: p ? p.tagName : '',
        id: p ? p.id : '',
        isLivePage: p ? p.classList.contains('live-page') : false,
      };
    });
    assert(r.tag !== 'BODY', '#liveControls parent must not be <body>');
    assert(!r.isLivePage, '#liveControls parent must not be .live-page directly (was the regression)');
  });

  await step(label + ' toggle inputs reachable (Heat/Ghosts/Audio render)', async () => {
    const have = await page.evaluate(() => ({
      heat: !!document.getElementById('liveHeatToggle'),
      ghost: !!document.getElementById('liveGhostToggle'),
      audio: !!document.getElementById('liveAudioToggle'),
    }));
    assert(have.heat && have.ghost && have.audio,
      `toggles missing from DOM: ${JSON.stringify(have)}`);
  });
}

async function assertReachable(page, label) {
  // Mobile: make sure toggles are visible / scrollable within viewport (not off-screen).
  await step(label + ' #liveControls rect within viewport bounds (reachable)', async () => {
    // Expand the header if it is collapse-hidden so the controls can lay out.
    await page.evaluate(() => {
      const hdr = document.getElementById('liveHeader');
      const body = document.getElementById('liveHeaderBody');
      if (hdr && hdr.classList.contains('is-collapsed')) {
        hdr.classList.remove('is-collapsed');
        hdr.classList.add('is-expanded');
        if (body) body.removeAttribute('hidden');
      }
      const cBtn = document.getElementById('liveControlsToggle');
      const ctrl = document.getElementById('liveControls');
      if (ctrl && ctrl.classList.contains('is-collapsed')) {
        ctrl.classList.remove('is-collapsed');
        ctrl.classList.add('is-expanded');
        const cb = document.getElementById('liveControlsBody');
        if (cb) cb.removeAttribute('hidden');
      }
    });
    await page.waitForTimeout(100);
    const r = await page.evaluate(() => {
      const ctrl = document.getElementById('liveControls');
      const rect = ctrl.getBoundingClientRect();
      return { left: rect.left, top: rect.top, right: rect.right, bottom: rect.bottom,
               vw: window.innerWidth, vh: window.innerHeight };
    });
    // Allow some scroll tolerance; key requirement: not completely off-screen and width fits viewport.
    assert(r.right <= r.vw + 1, `controls right=${r.right} exceeds viewport vw=${r.vw}`);
    assert(r.left >= -1, `controls left=${r.left} off-screen left`);
  });
}

async function assertMatrixThemeTransparent(page, label) {
  await step(label + ' matrix theme: .live-controls background stays transparent (no nested chrome box)', async () => {
    const r = await page.evaluate(() => {
      // Apply matrix theme the way the runtime toggles it.
      document.documentElement.classList.add('matrix-theme');
      document.body.classList.add('matrix-theme');
      const ctrl = document.getElementById('liveControls');
      const cs = getComputedStyle(ctrl);
      return {
        bg: cs.backgroundColor,
        borderTopWidth: cs.borderTopWidth,
        borderRightWidth: cs.borderRightWidth,
        borderBottomWidth: cs.borderBottomWidth,
        borderLeftWidth: cs.borderLeftWidth,
      };
    });
    // Accept any rgba with alpha 0 OR the literal 'transparent' / rgba(0,0,0,0).
    const transparent = /rgba?\([^)]*,\s*0\s*\)$/i.test(r.bg) || r.bg === 'transparent' || r.bg === 'rgba(0, 0, 0, 0)';
    assert(transparent, `matrix .live-controls background must be transparent; got "${r.bg}"`);
    const noBorder = ['borderTopWidth','borderRightWidth','borderBottomWidth','borderLeftWidth']
      .every((k) => r[k] === '0px');
    assert(noBorder, `matrix .live-controls borders must be 0; got ${JSON.stringify(r)}`);
  });
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log(`\n=== #1205 live controls anchored to MESH LIVE panel — E2E against ${BASE} ===`);

  for (const vp of [
    { w: 1440, h: 900, tag: '[1440x900 desktop]' },
    { w: 640,  h: 900, tag: '[640x900 tablet]' },
    { w: 320,  h: 800, tag: '[320x800 narrow phone]' },
  ]) {
    const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));
    await step(vp.tag + ' navigate to /live', async () => { await gotoLive(page); });
    await assertAnchoredToMeshLive(page, vp.tag);
    await assertReachable(page, vp.tag);
    await assertMatrixThemeTransparent(page, vp.tag);
    await ctx.close();
  }

  await browser.close();
  console.log(`\n=== ${passed} passed, ${failed} failed ===`);
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(1); });
