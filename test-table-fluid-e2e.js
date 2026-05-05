/**
 * E2E (#1056): Fluid table columns + "+N hidden" pill.
 *
 * Boots Chromium against a local corescope-server and verifies that the
 * primary tables (Packets, Nodes, Observers) collapse priority-tagged
 * columns at narrow viewports, render a "+N hidden" pill in the header
 * showing the count, and that clicking the pill reveals the hidden columns.
 *
 * Tested viewports: 768, 1080, 1440 (parent task: 768/1080/1440/1920).
 *
 * Usage: BASE_URL=http://localhost:13581 node test-table-fluid-e2e.js
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
  { hash: '#/packets',   tableSel: '#pktTable',    name: 'packets'   },
  { hash: '#/nodes',     tableSel: '#nodesTable',  name: 'nodes'     },
  { hash: '#/observers', tableSel: '#obsTable',    name: 'observers' },
];

const VIEWPORTS = [
  { w: 768,  h: 900, expectHidden: true  },
  { w: 1080, h: 900, expectHidden: true  },
  { w: 1440, h: 900, expectHidden: false }, // wide enough — no hide expected (or 0)
];

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log(`\n=== #1056 fluid tables E2E against ${BASE} ===`);

  for (const vp of VIEWPORTS) {
    const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));

    for (const p of PAGES) {
      const tag = `${p.name}@${vp.w}`;

      await step(`${tag}: page renders`, async () => {
        await page.goto(BASE + '/' + p.hash, { waitUntil: 'domcontentloaded' });
        await page.waitForSelector(p.tableSel, { timeout: 8000 });
        // give responsive logic a tick
        await page.waitForTimeout(300);
      });

      await step(`${tag}: no horizontal table scroll`, async () => {
        const overflow = await page.evaluate((sel) => {
          const t = document.querySelector(sel);
          if (!t) return { ok: false, reason: 'no table' };
          // Either the table itself or a wrapper must not horizontally overflow
          // its container at this viewport.
          const wrap = t.closest('.table-fluid-wrap, .obs-table-scroll, .table-scroll-wrap') || t.parentElement;
          return {
            tableW: t.scrollWidth,
            wrapW: wrap.clientWidth,
            ok: t.scrollWidth <= wrap.clientWidth + 2,
          };
        }, p.tableSel);
        assert(overflow.ok, `table overflows: tableW=${overflow.tableW} wrapW=${overflow.wrapW}`);
      });

      await step(`${tag}: +N hidden pill state matches hidden columns`, async () => {
        const info = await page.evaluate((sel) => {
          const t = document.querySelector(sel);
          if (!t) return { ok: false, reason: 'no table' };
          const heads = Array.from(t.querySelectorAll('thead th'));
          const hiddenHeads = heads.filter(h => h.classList.contains('col-hidden'));
          const pill = t.querySelector('.col-hidden-pill');
          return {
            hiddenCount: hiddenHeads.length,
            hasPill: !!pill,
            pillText: pill ? pill.textContent.trim() : '',
            pillVisible: pill ? pill.offsetParent !== null : false,
          };
        }, p.tableSel);

        if (vp.expectHidden) {
          assert(info.hiddenCount >= 1, `expected ≥1 hidden col at ${vp.w}px, got ${info.hiddenCount}`);
          assert(info.hasPill && info.pillVisible, `expected visible +N pill at ${vp.w}px`);
          assert(/\+\d+/.test(info.pillText), `pill text "${info.pillText}" missing +N marker`);
          const m = info.pillText.match(/\+(\d+)/);
          const n = m ? parseInt(m[1], 10) : -1;
          assert(n === info.hiddenCount, `pill says +${n} but ${info.hiddenCount} columns are hidden`);
        } else {
          // wide: no hidden cols, pill should be absent or hidden
          if (info.hasPill) assert(!info.pillVisible || /\+0/.test(info.pillText), `expected no/zero pill at ${vp.w}px, got "${info.pillText}"`);
        }
      });

      if (vp.expectHidden) {
        await step(`${tag}: clicking pill reveals hidden columns`, async () => {
          // Click pill
          const pillSel = `${p.tableSel} .col-hidden-pill`;
          await page.click(pillSel);
          await page.waitForTimeout(150);
          const after = await page.evaluate((sel) => {
            const t = document.querySelector(sel);
            const heads = Array.from(t.querySelectorAll('thead th'));
            return heads.filter(h => h.classList.contains('col-hidden')).length;
          }, p.tableSel);
          assert(after === 0, `expected 0 hidden cols after pill click, got ${after}`);
        });
      }
    }

    await ctx.close();
  }

  await browser.close();

  console.log(`\n=== #1056 fluid tables E2E: ${passed} passed, ${failed} failed ===`);
  process.exit(failed ? 1 : 0);
})();
