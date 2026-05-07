/**
 * E2E (#1128 final): Multi-viewport layout collision + z-scale enforcement.
 *
 * Sister of test-issue-1128-packets-layout-e2e.js. That file asserts
 * individual component properties; this one closes the original
 * acceptance criterion: SCREENSHOTS at multiple viewports + bounding-rect
 * collision detection on visible interactive elements.
 *
 * What this test asserts (on top of the sibling):
 *
 *   A. At each of three viewports (1280×900, 1080×800, 768×1024), no
 *      two `.filter-group` siblings vertically overlap. (Bug 3
 *      regression guard — row-gap must be large enough for 34px
 *      controls when the bar wraps.)
 *
 *   B. (Removed during self-review — was conceptually flawed; see the
 *      block comment inside the per-viewport loop. Z-scale band check
 *      C and the check-css-vars lint together gate the actual Bug 4
 *      regression — dropdowns rendering transparent or behind rows.)
 *
 *   C. Z-scale enforcement (audit Section 2): every dropdown selector
 *      (`.col-toggle-menu`, `.multi-select-menu`,
 *      `.region-dropdown-menu`, `.fux-saved-menu`,
 *      `.fux-ac-dropdown`) must compute a z-index inside the
 *      `--z-dropdown` band [100,199] — NOT 50, 90, or 9200.
 *
 *   D. Path chip line-height lock: `.path-hops .hop, .path-hops
 *      .hop-named, .path-hops .arrow` must all compute line-height
 *      ≤ 18px so chips never spill the 22px host (Bug 1 belt-
 *      and-suspenders, audit Section 3).
 *
 *   E. `.col-path` must use `height: 28px`, not `max-height` (table
 *      cells ignore max-height per audit).
 *
 *   F. Screenshots saved under e2e-screenshots/ for the PR record.
 *
 * Usage: BASE_URL=http://localhost:13581 node \
 *        test-issue-1128-multi-viewport-e2e.js
 */
'use strict';
const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const SHOT_DIR = 'e2e-screenshots';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

const VIEWPORTS = [
  { w: 1280, h: 900,  name: 'desktop-1280' },
  { w: 1080, h: 800,  name: 'laptop-1080' },
  { w: 768,  h: 1024, name: 'tablet-768' },
];

function vOverlap(a, b) {
  // Reject sub-pixel rounding noise.
  if (a.bottom <= b.top + 1) return false;
  if (b.bottom <= a.top + 1) return false;
  return true;
}

async function gotoPackets(page) {
  await page.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#packetFilterInput', { timeout: 8000 });
  await page.waitForFunction(() => !!document.querySelector('#filterUxBar'), { timeout: 8000 });
  await page.evaluate(() => {
    const sel = document.getElementById('fTimeWindow');
    if (sel) { sel.value = '0'; sel.dispatchEvent(new Event('change', { bubbles: true })); }
  });
  await page.waitForFunction(
    () => Array.from(document.querySelectorAll('#pktBody tr'))
            .filter(r => r.id !== 'vscroll-top' && r.id !== 'vscroll-bottom').length > 0,
    { timeout: 8000 });
  await page.waitForTimeout(400); // hop-resolver
}

(async () => {
  if (!fs.existsSync(SHOT_DIR)) fs.mkdirSync(SHOT_DIR, { recursive: true });

  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log(`\n=== #1128 multi-viewport E2E against ${BASE} ===`);

  for (const vp of VIEWPORTS) {
    const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));

    await step(`[${vp.name}] navigate to /packets`, async () => {
      await gotoPackets(page);
    });

    await step(`[${vp.name}] screenshot toolbar`, async () => {
      await page.screenshot({
        path: path.join(SHOT_DIR, `issue-1128-${vp.name}.png`),
        fullPage: false,
      });
    });

    await step(`[${vp.name}] no two .filter-group siblings vertically overlap`, async () => {
      const result = await page.evaluate(() => {
        const groups = Array.from(document.querySelectorAll('.filter-bar > .filter-group'))
          .filter(el => {
            const cs = getComputedStyle(el);
            return cs.display !== 'none' && cs.visibility !== 'hidden';
          });
        const rects = groups.map(g => {
          const r = g.getBoundingClientRect();
          return { top: r.top, bottom: r.bottom, left: r.left, right: r.right, w: r.width, h: r.height };
        });
        // Pairs that share x-overlap (same row attempt) but vertical overlap = bug.
        const offenders = [];
        for (let i = 0; i < rects.length; i++) {
          for (let j = i + 1; j < rects.length; j++) {
            const a = rects[i], b = rects[j];
            const xOverlap = !(a.right <= b.left + 1 || b.right <= a.left + 1);
            const yOverlap = !(a.bottom <= b.top + 1 || b.bottom <= a.top + 1);
            if (xOverlap && yOverlap) offenders.push({ i, j, a, b });
          }
        }
        return { count: groups.length, offenders };
      });
      assert(result.count > 0, 'no .filter-group elements found');
      assert(result.offenders.length === 0,
        `${result.offenders.length} .filter-group sibling pairs overlap: ` +
        JSON.stringify(result.offenders[0]));
    });

    // #1128 Bug 5 — toolbar reorder gate: toggles MUST appear before dropdowns
    // in document order. packets.js was reordered so the most-frequently-used
    // toggles (Group/My Nodes/time) sit next to the search input. A revert
    // would reintroduce the original eye-trail problem; this assertion fails
    // if the order is swapped back.
    await step(`[${vp.name}] Bug 5: filter-group-toggles precedes filter-group-dropdowns`, async () => {
      const order = await page.evaluate(() => {
        const groups = Array.from(document.querySelectorAll('.filter-bar .filter-group'));
        const togIdx = groups.findIndex(g => g.classList.contains('filter-group-toggles'));
        const dropIdx = groups.findIndex(g => g.classList.contains('filter-group-dropdowns'));
        return { togIdx, dropIdx, total: groups.length };
      });
      assert(order.togIdx >= 0, 'no .filter-group-toggles found in toolbar');
      assert(order.dropIdx >= 0, 'no .filter-group-dropdowns found in toolbar');
      assert(order.togIdx < order.dropIdx,
        `toggles (idx ${order.togIdx}) must precede dropdowns (idx ${order.dropIdx})`);
    });

    // NOTE on removed sub-tests (#1128 self-review): earlier drafts had
    // "[vp] Saved menu does not overlap toolbar groups below it" and
    // "[vp] Types multi-select dropdown does not overlap toolbar groups
    // below it". Those sub-tests had a fundamentally wrong premise — a
    // position:absolute dropdown opened from a wrapped toolbar row will
    // ALWAYS overlap toolbar rows below it; that's by design. What
    // matters for #1128 Bug 4 is that the dropdown (a) paints on top
    // (z-index) and (b) is opaque (no transparent --surface). Both are
    // already gated independently:
    //   - z-index band: "Z-scale: dropdown selectors compute z-index in
    //     [100,199] band" (below)
    //   - opacity / undefined vars: scripts/check-css-vars.js (CI lint,
    //     wired in deploy.yml)
    // Reintroducing a "no rect overlap" assertion would require pinning
    // the toolbar to a single non-wrapping row, which contradicts the
    // responsive design the rest of this file exercises.
    void vp;

    await ctx.close();
  }

  // Single z-scale + line-height + col-path checks are viewport-agnostic.
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  await gotoPackets(page);

  await step('Z-scale: dropdown selectors compute z-index in [100,199] band', async () => {
    const checks = await page.evaluate(() => {
      // We can't always force every menu to render, so we test the
      // computed z-index by inserting throwaway DOM nodes that match the
      // selector and reading getComputedStyle. The browser applies the
      // CSS rule by selector, regardless of whether the menu is the
      // "real" one — what matters is the rule's declared z-index.
      const selectors = [
        '.col-toggle-menu',
        '.multi-select-menu',
        '.region-dropdown-menu',
        '.fux-saved-menu',
        '.fux-ac-dropdown',
      ];
      const results = [];
      for (const sel of selectors) {
        const el = document.createElement('div');
        // Strip leading dot, attach class.
        el.className = sel.replace(/^\./, '');
        // Force visible so getComputedStyle returns the matched rule.
        el.style.position = 'absolute';
        el.style.top = '-9999px';
        el.style.display = 'block';
        document.body.appendChild(el);
        const z = parseInt(getComputedStyle(el).zIndex, 10);
        results.push({ sel, z: isNaN(z) ? null : z });
        el.remove();
      }
      return results;
    });
    const offenders = checks.filter(c => c.z === null || c.z < 100 || c.z > 199);
    assert(offenders.length === 0,
      'dropdown z-index outside [100,199] band: ' + JSON.stringify(offenders));
  });

  await step('Bug 1 polish: .path-hops chip children compute line-height ≤ 18px', async () => {
    const offenders = await page.evaluate(() => {
      const probe = (cls) => {
        const host = document.createElement('div');
        host.className = 'path-hops';
        const child = document.createElement('span');
        child.className = cls;
        child.textContent = 'x';
        host.appendChild(child);
        document.body.appendChild(host);
        const lh = parseFloat(getComputedStyle(child).lineHeight);
        host.remove();
        return { cls, lh };
      };
      const probed = ['hop', 'hop-named', 'arrow'].map(probe);
      return probed.filter(p => !(p.lh <= 18.5));
    });
    assert(offenders.length === 0,
      '.path-hops chip line-height > 18px: ' + JSON.stringify(offenders));
  });

  await step('Bug 1 polish: .col-path uses fixed height (not max-height) ≤ 28px', async () => {
    const result = await page.evaluate(() => {
      // Build a fake table cell to read the computed rule.
      const tbl = document.createElement('table');
      tbl.className = 'data-table';
      const tr = document.createElement('tr');
      const td = document.createElement('td');
      td.className = 'col-path';
      tr.appendChild(td);
      const tbody = document.createElement('tbody');
      tbody.appendChild(tr);
      tbl.appendChild(tbody);
      tbl.style.position = 'absolute';
      tbl.style.top = '-9999px';
      document.body.appendChild(tbl);
      const cs = getComputedStyle(td);
      const height = parseFloat(cs.height);
      const maxHeight = cs.maxHeight;
      tbl.remove();
      // Walk the loaded stylesheets to find the rule(s) that target
      // .data-table td.col-path and report which physical property
      // (height vs max-height) is declared. The audit fix REQUIRES
      // `height: 28px`; `max-height: 28px` is the original Bug 1
      // regression and must fail this test.
      const declared = { height: null, maxHeight: null, ruleSrc: null };
      for (const sheet of document.styleSheets) {
        let rules;
        try { rules = sheet.cssRules; } catch (e) { continue; }
        if (!rules) continue;
        for (const rule of rules) {
          if (!rule.selectorText) continue;
          // Match selectors that include both .data-table and .col-path
          // (allow combinator variations like ".data-table td.col-path").
          if (/\.data-table[\s\S]*\.col-path/.test(rule.selectorText)) {
            const h = rule.style && rule.style.getPropertyValue('height');
            const mh = rule.style && rule.style.getPropertyValue('max-height');
            if (h) declared.height = h.trim();
            if (mh) declared.maxHeight = mh.trim();
            if (h || mh) declared.ruleSrc = rule.selectorText;
          }
        }
      }
      return { height, maxHeight, declared };
    });
    // Computed height must be exactly 28px.
    assert(Math.abs(result.height - 28) < 0.5,
      '.col-path computed height not 28px: ' + JSON.stringify(result));
    // Audit fix gate: rule must declare `height: 28px`. If the regression
    // is reverted to `max-height: 28px`, declared.height is null and this
    // assertion fails.
    assert(result.declared.height && /^28px$/.test(result.declared.height),
      '.col-path rule must declare `height: 28px` (audit fix), got: ' +
      JSON.stringify(result.declared));
    assert(!result.declared.maxHeight,
      '.col-path rule must NOT declare max-height (original Bug 1 regression): ' +
      JSON.stringify(result.declared));
  });

  await ctx.close();
  await browser.close();

  console.log(`\n=== Results: passed ${passed} failed ${failed} ===`);
  process.exit(failed > 0 ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
