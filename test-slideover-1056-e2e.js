/**
 * E2E (#1056 AC #4): Row-detail slide-over panel at narrow widths.
 *
 * At viewports <=1023, clicking a row in the Packets, Nodes, or Observers
 * tables must open the row's detail in a slide-over panel
 * (`.slide-over-panel`) with a backdrop (`.slide-over-backdrop`), instead of
 * pushing layout to a separate page. The panel must close via the X button,
 * a backdrop click, and the Escape key.
 *
 * Wide viewports (>=1280) MUST NOT trigger the slide-over — the existing
 * right-side detail panel behavior is preserved.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-slideover-1056-e2e.js
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

const PAGES = [
  { hash: '#/packets',   tableSel: '#pktTable',    rowSel: '#pktTable tbody tr[data-id], #pktTable tbody tr',   name: 'packets'   },
  { hash: '#/nodes',     tableSel: '#nodesTable',  rowSel: '#nodesTable tbody tr[data-value]',                  name: 'nodes'     },
  { hash: '#/observers', tableSel: '#obsTable',    rowSel: '#obsTable tbody tr[data-action="navigate"]',        name: 'observers' },
];

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log(`\n=== #1056 AC#4 slide-over E2E against ${BASE} ===`);

  // ---- Narrow viewport: slide-over MUST appear ----
  for (const p of PAGES) {
    const ctx = await browser.newContext({ viewport: { width: 800, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));

    const tag = `${p.name}@800`;

    await step(`${tag}: page renders + first row exists`, async () => {
      await page.goto(BASE + '/' + p.hash, { waitUntil: 'domcontentloaded' });
      await page.waitForSelector(p.tableSel, { timeout: 8000 });
      // wait for at least one tbody row
      await page.waitForFunction((sel) => {
        const t = document.querySelector(sel);
        return t && t.querySelectorAll('tbody tr').length > 0;
      }, p.tableSel, { timeout: 8000 });
    });

    await step(`${tag}: clicking row opens slide-over with backdrop`, async () => {
      // Click the first body row — prefer one with a data-action attribute
      // (packets) or any row otherwise.
      const diag = await page.evaluate((sel) => {
        const t = document.querySelector(sel);
        if (!t) return { ok: false, why: 'no table' };
        const rows = t.querySelectorAll('tbody tr');
        // The packets table uses virtual scroll, so the FIRST DOM-order <tr>
        // is a spacer with no data-* attrs and no click handler. Skip those:
        // pick the first row that actually carries a delegated action.
        const candidates = Array.from(rows);
        const row = candidates.find(r => r.hasAttribute('data-action'))
                || candidates.find(r => r.hasAttribute('data-value'))
                || candidates.find(r => r.children.length > 0);
        if (!row) return { ok: false, why: 'no row', rowCount: rows.length };
        // Click a real cell (avoid empty/loading rows)
        const td = row.querySelector('td:not(:empty)') || row;
        // Dispatch a real bubbling click event so delegated tbody handlers fire.
        const ev = new MouseEvent('click', { bubbles: true, cancelable: true, view: window });
        td.dispatchEvent(ev);
        return {
          ok: true,
          rowCount: rows.length,
          rowAction: row.getAttribute('data-action') || null,
          rowValue: row.getAttribute('data-value') || null,
          hasSlideOver: typeof window.SlideOver !== 'undefined',
          shouldUse: !!(window.SlideOver && window.SlideOver.shouldUse && window.SlideOver.shouldUse()),
          innerW: window.innerWidth,
        };
      }, p.tableSel);
      if (!diag.ok) throw new Error('click setup failed: ' + JSON.stringify(diag));
      // Wait up to 15s for the slide-over to appear (packets does async fetches).
      try {
        await page.waitForFunction(() => {
          const panel = document.querySelector('.slide-over-panel');
          return panel && !panel.hidden;
        }, null, { timeout: 15000 });
      } catch (_) { /* fall through to assertion below for clearer message */ }
      const info = await page.evaluate(() => {
        function isShown(el) {
          if (!el) return false;
          if (el.hidden) return false;
          const r = el.getBoundingClientRect();
          return r.width > 0 && r.height > 0;
        }
        const panel = document.querySelector('.slide-over-panel');
        const back  = document.querySelector('.slide-over-backdrop');
        const closeBtn = panel && panel.querySelector('.slide-over-close');
        return {
          panelPresent: !!panel,
          panelVisible: isShown(panel),
          backdropPresent: !!back,
          backdropVisible: isShown(back),
          hasCloseBtn: !!closeBtn,
        };
      });
      assert(info.panelPresent, 'slide-over panel not in DOM (diag: ' + JSON.stringify(diag) + ')');
      assert(info.panelVisible, 'slide-over panel not visible');
      assert(info.backdropPresent, 'slide-over backdrop not in DOM');
      assert(info.backdropVisible, 'slide-over backdrop not visible');
      assert(info.hasCloseBtn, 'slide-over panel missing .slide-over-close X button');
    });

    await step(`${tag}: panel anchored to right edge + a11y attrs + body scroll lock`, async () => {
      // The slideInRight keyframe applies a transient translateX, which shifts
      // getBoundingClientRect for ~200ms. Wait for it to settle before
      // asserting, OR rely on computed style (right:0) which reflects layout.
      await page.waitForTimeout(300);
      const a = await page.evaluate(() => {
        const panel = document.querySelector('.slide-over-panel');
        const back  = document.querySelector('.slide-over-backdrop');
        const x = panel && panel.querySelector('.slide-over-close');
        const cs = panel && getComputedStyle(panel);
        const xr = x && x.getBoundingClientRect();
        return {
          // Anchored to right edge — computed CSS right MUST be 0px.
          cssRight: cs && cs.right,
          cssTop: cs && cs.top,
          cssPosition: cs && cs.position,
          role: panel && panel.getAttribute('role'),
          ariaModal: panel && panel.getAttribute('aria-modal'),
          // #1168 review must-fix #4: panel must use aria-labelledby pointing
          // at the actual <h3 id="slideOverTitle"> so screen readers announce
          // the meaningful title, not a generic static "Detail" string.
          ariaLabelledBy: panel && panel.getAttribute('aria-labelledby'),
          ariaLabel: panel && panel.getAttribute('aria-label'),
          titleId: panel && panel.querySelector('.slide-over-title')
            ? panel.querySelector('.slide-over-title').id : null,
          backdropAriaHidden: back && back.getAttribute('aria-hidden'),
          xAriaLabel: x && x.getAttribute('aria-label'),
          xWidth: xr ? xr.width : 0,
          xHeight: xr ? xr.height : 0,
          bodyOverflow: document.body.style.overflow,
        };
      });
      assert(a.cssPosition === 'fixed', 'slide-over panel not position:fixed (got ' + a.cssPosition + ')');
      assert(a.cssRight === '0px', 'slide-over panel not anchored to right:0 (got ' + a.cssRight + ')');
      assert(a.cssTop === '0px', 'slide-over panel does not start at top:0 (got ' + a.cssTop + ')');
      assert(a.role === 'dialog', 'slide-over role!=dialog (got ' + a.role + ')');
      assert(a.ariaModal === 'true', 'slide-over aria-modal!=true (got ' + a.ariaModal + ')');
      // #1168 must-fix #4: aria-labelledby (pointing to the title h3) wins
      // over a static aria-label so SRs announce the actual packet/node name.
      assert(a.ariaLabelledBy === 'slideOverTitle',
        'slide-over panel must use aria-labelledby="slideOverTitle" (got ' + a.ariaLabelledBy + ')');
      assert(a.titleId === 'slideOverTitle',
        'slide-over title must keep id="slideOverTitle" (got ' + a.titleId + ')');
      assert(!a.ariaLabel,
        'slide-over panel must NOT carry a static aria-label that shadows the title (got ' + a.ariaLabel + ')');
      assert(a.backdropAriaHidden === 'true', 'backdrop aria-hidden!=true (got ' + a.backdropAriaHidden + ')');
      assert(a.xAriaLabel && a.xAriaLabel.length > 0, 'X button missing aria-label');
      assert(a.xWidth >= 44 && a.xHeight >= 44, 'X tap target <44px (' + a.xWidth + 'x' + a.xHeight + ')');
      assert(a.bodyOverflow === 'hidden', 'body scroll not locked while open (overflow=' + a.bodyOverflow + ')');
    });

    await step(`${tag}: Escape closes slide-over`, async () => {
      await page.keyboard.press('Escape');
      await page.waitForTimeout(200);
      const info = await page.evaluate(() => {
        function isShown(el) {
          if (!el) return false;
          if (el.hidden) return false;
          const r = el.getBoundingClientRect();
          return r.width > 0 && r.height > 0;
        }
        const panel = document.querySelector('.slide-over-panel');
        const back  = document.querySelector('.slide-over-backdrop');
        return { panelGone: !isShown(panel), backGone: !isShown(back), bodyOverflow: document.body.style.overflow };
      });
      assert(info.panelGone, 'slide-over panel still visible after Escape');
      assert(info.backGone, 'slide-over backdrop still visible after Escape');
      assert(info.bodyOverflow !== 'hidden', 'body scroll lock not released after Escape (overflow=' + info.bodyOverflow + ')');
    });

    await step(`${tag}: backdrop click closes slide-over`, async () => {
      await page.evaluate((sel) => {
        const t = document.querySelector(sel);
        if (!t) return;
        const rows = Array.from(t.querySelectorAll('tbody tr'));
        const row = rows.find(r => r.hasAttribute('data-action'))
                || rows.find(r => r.hasAttribute('data-value'))
                || rows.find(r => r.children.length > 0);
        if (!row) return;
        const td = row.querySelector('td:not(:empty)') || row;
        td.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, view: window }));
      }, p.tableSel);
      try {
        await page.waitForFunction(() => {
          const panel = document.querySelector('.slide-over-panel');
          return panel && !panel.hidden;
        }, null, { timeout: 5000 });
      } catch (_) {}
      // Click the backdrop directly.
      await page.evaluate(() => {
        const b = document.querySelector('.slide-over-backdrop');
        if (b) b.click();
      });
      await page.waitForTimeout(200);
      const gone = await page.evaluate(() => {
        const panel = document.querySelector('.slide-over-panel');
        if (!panel || panel.hidden) return true;
        const r = panel.getBoundingClientRect();
        return r.width === 0 || r.height === 0;
      });
      assert(gone, 'slide-over still visible after backdrop click');
    });

    await step(`${tag}: X button closes slide-over`, async () => {
      await page.evaluate((sel) => {
        const t = document.querySelector(sel);
        if (!t) return;
        const rows = Array.from(t.querySelectorAll('tbody tr'));
        const row = rows.find(r => r.hasAttribute('data-action'))
                || rows.find(r => r.hasAttribute('data-value'))
                || rows.find(r => r.children.length > 0);
        if (!row) return;
        const td = row.querySelector('td:not(:empty)') || row;
        td.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, view: window }));
      }, p.tableSel);
      try {
        await page.waitForFunction(() => {
          const panel = document.querySelector('.slide-over-panel');
          return panel && !panel.hidden;
        }, null, { timeout: 5000 });
      } catch (_) {}
      await page.evaluate(() => {
        const x = document.querySelector('.slide-over-panel .slide-over-close');
        if (x) x.click();
      });
      await page.waitForTimeout(200);
      const gone = await page.evaluate(() => {
        const panel = document.querySelector('.slide-over-panel');
        if (!panel || panel.hidden) return true;
        const r = panel.getBoundingClientRect();
        return r.width === 0 || r.height === 0;
      });
      assert(gone, 'slide-over still visible after X click');
    });

    await ctx.close();
  }

  // ---- Wide viewport: slide-over MUST NOT appear (regression guard) ----
  {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);

    await step('wide@1440 packets: row click does NOT open slide-over', async () => {
      await page.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
      await page.waitForSelector('#pktTable', { timeout: 8000 });
      await page.waitForFunction(() => document.querySelectorAll('#pktTable tbody tr').length > 0, null, { timeout: 8000 });
      await page.evaluate(() => {
        const r = document.querySelector('#pktTable tbody tr');
        if (r) r.click();
      });
      await page.waitForTimeout(300);
      const slideOverShown = await page.evaluate(() => {
        const p = document.querySelector('.slide-over-panel');
        if (!p || p.hidden) return false;
        const r = p.getBoundingClientRect();
        return r.width > 0 && r.height > 0;
      });
      assert(!slideOverShown, 'slide-over should NOT appear at 1440px width');
    });

    await ctx.close();
  }

  await browser.close();

  console.log(`\n=== #1056 AC#4 slide-over E2E: ${passed} passed, ${failed} failed ===`);
  process.exit(failed ? 1 : 0);
})();
