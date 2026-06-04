/**
 * E2E for #1567 — Move-panel corner-cycle button silently no-ops after the
 * panel has been dragged.
 *
 * Root cause (see triage on #1567): two coexisting positioning systems
 * mutate disjoint state. `drag-manager.js` sets `data-dragged="true"` and
 * inline `position:fixed; top/left/right:auto; bottom:auto; transform:none`
 * and persists to `localStorage['panel-drag-<id>']`. `live.js` →
 * `applyPanelPosition()` only flips the `data-position` attribute (which
 * selects a `.live-overlay[data-position="…"]` CSS rule with `top/left/right/
 * bottom`). The inline styles win cascade-wise, so the panel does not move.
 *
 * Fix path under test: `onCornerClick` must clear drag state (attribute,
 * inline coords, localStorage) BEFORE calling `applyPanelPosition`.
 *
 * Assertions:
 *   (a) After programmatic drag, the panel sits at the dragged coords
 *       (sanity; if false the harness is broken).
 *   (b) After clicking the panel-corner-btn, `data-dragged` is gone,
 *       inline `top/left/right/bottom/transform/position` are cleared,
 *       `localStorage['panel-drag-<id>']` is gone, and the panel's
 *       bounding rect matches the CSS corner-anchor for the new
 *       `data-position` value (NOT the dragged coords).
 *
 * Red-on-master: assertion (b) fails on master — panel stays at the
 * dragged coords after the click because inline styles are not cleared.
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1567-corner-clears-drag-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  \u2713 ' + name); }
  catch (e) { failed++; console.error('  \u2717 ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  // Desktop viewport — drag is gated off on coarse pointer / narrow widths.
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(10000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1567 corner-button clears drag state E2E against ${BASE} ===`);

  await step('navigate to /live with a clean slate', async () => {
    await page.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => {
      ['liveFeed', 'liveLegend', 'liveNodeDetail'].forEach((id) => {
        try { localStorage.removeItem('panel-drag-' + id); } catch (_) {}
        try { localStorage.removeItem('panel-corner-' + id); } catch (_) {}
      });
    });
    await page.reload({ waitUntil: 'load' });
    await page.waitForSelector('#liveFeed .panel-corner-btn', { timeout: 8000 });
    await page.waitForTimeout(200);
  });

  // (a) Programmatically push #liveFeed into "dragged" state via the same
  //     DOM shape DragManager produces. Using direct attribute/style writes
  //     keeps the test deterministic across viewports / pointer types and
  //     avoids racing the real drag handlers.
  await step('inject drag state on #liveFeed (mirrors DragManager output)', async () => {
    const got = await page.evaluate(() => {
      const el = document.getElementById('liveFeed');
      if (!el) return null;
      el.removeAttribute('data-position');
      el.dataset.dragged = 'true';
      el.style.position = 'fixed';
      el.style.top = '300px';
      el.style.left = '500px';
      el.style.right = 'auto';
      el.style.bottom = 'auto';
      el.style.transform = 'none';
      localStorage.setItem('panel-drag-liveFeed', JSON.stringify({
        xPct: 500 / window.innerWidth, yPct: 300 / window.innerHeight,
      }));
      const r = el.getBoundingClientRect();
      return { top: r.top, left: r.left, dragged: el.dataset.dragged };
    });
    assert(got, '#liveFeed missing');
    assert(got.dragged === 'true', 'data-dragged should be "true"');
    // Tolerate a few px (panel chrome/scrollbar). Bug repro requires the
    // panel to actually be at the dragged coords before the click.
    assert(Math.abs(got.top - 300) < 8, 'pre-click panel top ~300, got ' + got.top);
    assert(Math.abs(got.left - 500) < 8, 'pre-click panel left ~500, got ' + got.left);
  });

  // (b) Click the corner button and assert drag state is FULLY cleared
  //     and the panel actually sits at the CSS corner anchor for the new
  //     data-position.
  await step('clicking .panel-corner-btn clears drag state and snaps to the corner anchor', async () => {
    await page.click('#liveFeed .panel-corner-btn');
    await page.waitForTimeout(150);

    const after = await page.evaluate(() => {
      const el = document.getElementById('liveFeed');
      const pos = el.getAttribute('data-position');
      const rect = el.getBoundingClientRect();
      // Compute the CSS-rule anchor for this corner. Mirrors live.css
      // .live-overlay[data-position="…"] rules (see public/live.css ~1200).
      const VCR = parseInt(getComputedStyle(document.documentElement)
        .getPropertyValue('--vcr-bar-height')) || 58;
      const anchors = {
        tl: { top: 64, left: 12 },
        tr: { top: 64, right: 12 },
        bl: { bottom: VCR + 10, left: 12 },
        br: { bottom: VCR + 10, right: 12 },
      };
      return {
        dragged: el.dataset.dragged,
        inlineTop: el.style.top,
        inlineLeft: el.style.left,
        inlineRight: el.style.right,
        inlineBottom: el.style.bottom,
        inlineTransform: el.style.transform,
        inlinePosition: el.style.position,
        ls: localStorage.getItem('panel-drag-liveFeed'),
        pos: pos,
        rect: { top: rect.top, left: rect.left, right: rect.right, bottom: rect.bottom },
        vw: window.innerWidth, vh: window.innerHeight,
        anchor: anchors[pos],
      };
    });

    assert(after.pos && /^(tl|tr|bl|br)$/.test(after.pos),
      'data-position must be a corner code, got: ' + after.pos);
    assert(!after.dragged,
      'data-dragged must be cleared after corner click, got: ' + after.dragged);
    assert(!after.ls,
      'localStorage panel-drag-liveFeed must be removed, got: ' + after.ls);
    ['inlineTop', 'inlineLeft', 'inlineRight', 'inlineBottom', 'inlineTransform', 'inlinePosition'].forEach((k) => {
      assert(after[k] === '',
        'inline style ' + k + ' must be cleared after corner click, got: ' + JSON.stringify(after[k]));
    });

    // Now the panel must actually be at the corner anchor — not at the
    // dragged coords. Tolerate a few px (border/scrollbar).
    var TOL = 12;
    if ('top' in after.anchor) {
      assert(Math.abs(after.rect.top - after.anchor.top) < TOL,
        'panel top must match CSS anchor (' + after.anchor.top + ') for ' + after.pos +
        ', got rect.top=' + after.rect.top);
    }
    if ('left' in after.anchor) {
      assert(Math.abs(after.rect.left - after.anchor.left) < TOL,
        'panel left must match CSS anchor (' + after.anchor.left + ') for ' + after.pos +
        ', got rect.left=' + after.rect.left);
    }
    if ('right' in after.anchor) {
      var rightGap = after.vw - after.rect.right;
      assert(Math.abs(rightGap - after.anchor.right) < TOL,
        'panel right-gap must match CSS anchor (' + after.anchor.right + ') for ' + after.pos +
        ', got vw-rect.right=' + rightGap);
    }
    if ('bottom' in after.anchor) {
      var bottomGap = after.vh - after.rect.bottom;
      assert(Math.abs(bottomGap - after.anchor.bottom) < TOL,
        'panel bottom-gap must match CSS anchor (' + after.anchor.bottom + ') for ' + after.pos +
        ', got vh-rect.bottom=' + bottomGap);
    }
    // Sanity: the panel must have actually moved away from the dragged coords.
    assert(Math.abs(after.rect.top - 300) > TOL || Math.abs(after.rect.left - 500) > TOL,
      'panel must move away from dragged coords (300,500); rect=' + JSON.stringify(after.rect));
  });

  await ctx.close();
  await browser.close();
  console.log(`\n=== Results: ${passed} passed, ${failed} failed ===`);
  process.exit(failed > 0 ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
