/**
 * Playwright E2E — packets table column packing and the Full Names toggle.
 *
 * The packets table used to store every column width as a percentage of the
 * table, measured once on load. Percentages scale with the screen, so on a
 * wide monitor "17s ago" sat in a 180px column; and the measurement read the
 * virtual-scroll spacer row (one colspan cell as wide as the table) as
 * column 0, inflating the expand column too. fitColumnsToContent (app.js)
 * now sizes short columns to their content in px and gives the rest to Path
 * and Details.
 *
 * Usage: BASE_URL=http://localhost:13581 node tests/e2e/test-packets-compact-columns-e2e.js
 */
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';
const results = [];

async function test(name, fn) {
  try {
    await fn();
    results.push({ name, pass: true });
    console.log(`  ✅ ${name}`);
  } catch (err) {
    results.push({ name, pass: false, error: err.message });
    console.log(`  ❌ ${name}: ${err.message}`);
  }
}

function assert(condition, msg) {
  if (!condition) throw new Error(msg || 'Assertion failed');
}

// Upper bounds for the short columns at any desktop width. Content needs
// ~30-70px; the old percentage layout gave them 100-180px at 1920px.
const MAX_WIDTH = {
  'col-expand': 40,
  'col-time': 100,
  'col-size': 70,
  'col-hashsize': 50,
  'col-scope': 80,
  'col-rpt': 70,
};

async function gotoPackets(page, prefs) {
  // The window goes in the URL: mobile clamps a stored window above 180min
  // back to 15min, and the fixture is freshened at the start of the CI job.
  await page.goto(BASE + '/#/packets?timeWindow=10080', { waitUntil: 'domcontentloaded' });
  await page.evaluate((extra) => {
    localStorage.removeItem('packets-visible-cols');
    localStorage.removeItem('packets-known-cols');
    localStorage.removeItem('meshcore-pkt-col-px');
    localStorage.removeItem('meshcore-full-names');
    for (const [k, v] of Object.entries(extra || {})) localStorage.setItem(k, v);
  }, prefs);
  await page.reload({ waitUntil: 'networkidle' });
  await page.waitForSelector('#pktTable tbody tr:not([id^=vscroll]) td.col-type',
    { state: 'attached', timeout: 30000 });
  await settle(page);
}

// Column layout runs synchronously after a render, and container resizes are
// coalesced to the next animation frame: two frames cover both.
function settle(page) {
  return page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
}

// The "+N" pills are recomputed once hop names settle (up to ~1s after a
// render). Wait for that pass to finish rather than for a fixed delay.
function pillsReady(page) {
  return page.waitForFunction(() => {
    const tbody = document.getElementById('pktBody');
    return tbody && !tbody._rePathOverflowObserver &&
      document.querySelector('#pktTable .path-overflow-pill');
  }, null, { timeout: 10000 });
}

async function setColumnVisible(page, key, visible) {
  // A checkbox click bubbles to the document handler that closes the menu,
  // so each toggle needs its own open.
  await page.click('#colToggleBtn');
  const box = await page.waitForSelector(`#colToggleMenu input[data-col="${key}"]`);
  if ((await box.isChecked()) !== visible) await box.click();
  await page.waitForFunction(([k, v]) =>
    document.getElementById('pktTable').classList.contains('hide-col-' + k) === !v, [key, visible]);
  await settle(page);
}

function layout(page) {
  return page.evaluate(() => {
    const t = document.getElementById('pktTable');
    const wrap = t.parentElement;
    const widths = {};
    t.querySelectorAll('thead th').forEach(th => {
      if (th.offsetParent === null) return;
      const key = Array.from(th.classList).find(c => c.startsWith('col-'));
      widths[key] = th.offsetWidth;
    });
    const row = Array.from(t.querySelectorAll('tbody tr'))
      .find(r => r.children.length === t.querySelectorAll('thead th').length);
    const cells = row ? Array.from(row.children).filter(td => td.offsetParent !== null) : [];
    const lastCell = cells[cells.length - 1];
    const wr = wrap.getBoundingClientRect();
    return {
      widths,
      wrapRight: wr.left + wrap.clientLeft + wrap.clientWidth,
      tableRight: t.getBoundingClientRect().right,
      lastCellRight: lastCell ? lastCell.getBoundingClientRect().right : null,
      hScroll: wrap.scrollWidth - wrap.clientWidth,
    };
  });
}

(async () => {
  console.log(`\nPackets compact columns — ${BASE}\n`);
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage']
  });
  const page = await browser.newPage({ viewport: { width: 1920, height: 1000 } });

  await gotoPackets(page);

  await test('short columns stay short on a wide screen', async () => {
    const { widths } = await layout(page);
    for (const [col, max] of Object.entries(MAX_WIDTH)) {
      if (widths[col] == null) continue; // hidden by a preference
      assert(widths[col] <= max, `${col} is ${widths[col]}px, expected <= ${max}px (all: ${JSON.stringify(widths)})`);
    }
  });

  await test('Path and Details take the freed width', async () => {
    const { widths } = await layout(page);
    assert(widths['col-path'] >= 400, `col-path only ${widths['col-path']}px`);
    assert(widths['col-details'] >= 400, `col-details only ${widths['col-details']}px`);
  });

  await test('no blank strip after the last column, no horizontal scroll', async () => {
    const l = await layout(page);
    assert(l.lastCellRight != null, 'no data row to measure');
    assert(Math.abs(l.tableRight - l.lastCellRight) <= 2,
      `last cell ends at ${l.lastCellRight}, table at ${l.tableRight}: a hidden column's slot is taking width`);
    assert(l.hScroll <= 0, `table overflows its wrapper by ${l.hScroll}px`);
    assert(Math.abs(l.wrapRight - l.tableRight) <= 2,
      `table ends at ${l.tableRight}, its wrapper at ${l.wrapRight}`);
  });

  await test('hiding both Path and Details keeps the table at the wrapper width', async () => {
    // Review on #2090: with no flex column the table shrank to the sum of
    // the short columns (~400px in a 1900px wrapper). The gap was between
    // the table and its wrapper, which the check above did not measure.
    let l;
    try {
      await setColumnVisible(page, 'path', false);
      await setColumnVisible(page, 'details', false);
      l = await layout(page);
    } finally {
      // Restore before asserting, so a failure here does not leak two
      // hidden columns into every later case.
      await setColumnVisible(page, 'path', true);
      await setColumnVisible(page, 'details', true);
    }
    assert(l.widths['col-path'] == null && l.widths['col-details'] == null, 'Path/Details still visible');
    assert(Math.abs(l.wrapRight - l.tableRight) <= 2,
      `table ends at ${l.tableRight}, its wrapper at ${l.wrapRight}: ${Math.round(l.wrapRight - l.tableRight)}px empty`);
    assert(l.hScroll <= 0, `table overflows its wrapper by ${l.hScroll}px`);
    const back = await layout(page);
    assert(back.widths['col-details'] >= 400, `Details did not take the width back: ${back.widths['col-details']}px`);
  });

  await test('opening the detail panel re-fits the table without scrolling', async () => {
    await page.click('#pktTable tbody tr:not([id^=vscroll]) td.col-time');
    await page.waitForSelector('#pktRight:not(.empty)', { timeout: 10000 })
      .catch(() => { throw new Error('clicking a row did not open the detail panel'); });
    await settle(page);
    const l = await layout(page);
    assert(l.hScroll <= 0, `table overflows its wrapper by ${l.hScroll}px with the panel open`);
    await page.keyboard.press('Escape');
    await page.waitForSelector('#pktRight.empty', { state: 'attached', timeout: 10000 })
      .catch(() => { throw new Error('Escape did not close the detail panel'); });
    await settle(page);
    const after = await layout(page);
    assert(after.widths['col-details'] >= 400, `Details did not grow back after closing the panel: ${after.widths['col-details']}px`);
  });

  await test('stale percentage widths from the old layout are dropped', async () => {
    const stale = JSON.stringify([12.6, 0.4, 8.7, 9.1, 5, 3.4, 12.6, 6.2, 12.6, 12.6, 4, 12.8]);
    await gotoPackets(page, { 'meshcore-pkt-col-widths': stale });
    const gone = await page.evaluate(() => localStorage.getItem('meshcore-pkt-col-widths') === null);
    assert(gone, 'meshcore-pkt-col-widths still present');
    const { widths } = await layout(page);
    assert(widths['col-time'] <= MAX_WIDTH['col-time'], `col-time ${widths['col-time']}px after a stale save`);
  });

  await test('Full Names shows observer names untruncated and lifts the chip cap', async () => {
    await gotoPackets(page);
    const before = await page.evaluate(() => ({
      cls: document.getElementById('pktTable').classList.contains('pkt-full-names'),
      chipMax: (() => { const c = document.querySelector('#pktTable .path-hops .hop-named'); return c ? getComputedStyle(c).maxWidth : null; })(),
    }));
    assert(!before.cls, 'pkt-full-names set before toggling');
    if (before.chipMax) assert(before.chipMax === '120px', `default chip cap is ${before.chipMax}`);

    await page.click('#fullNamesToggle');
    await page.waitForFunction(() => document.getElementById('pktTable').classList.contains('pkt-full-names'));
    await pillsReady(page);
    await settle(page);
    const after = await page.evaluate(() => {
      const t = document.getElementById('pktTable');
      const obs = Array.from(t.querySelectorAll('td.col-observer')).map(td => td.textContent);
      const chip = t.querySelector('.path-hops .hop-named');
      return {
        cls: t.classList.contains('pkt-full-names'),
        active: document.getElementById('fullNamesToggle').classList.contains('active'),
        ellipsised: obs.filter(s => s.includes('…')).length,
        chipMax: chip ? getComputedStyle(chip).maxWidth : null,
        hScroll: t.parentElement.scrollWidth - t.parentElement.clientWidth,
        // The virtual scroller positions rows at a fixed height: a path that
        // wrapped onto a second line would break it.
        tallPaths: Array.from(t.querySelectorAll('.path-hops')).filter(h => h.offsetHeight > 24).length,
        // Hops past the edge are reachable through the +N pill, which must
        // sit inside the clipped path rather than past its edge.
        offscreenPills: Array.from(t.querySelectorAll('.path-overflow-pill')).filter(p => {
          const host = p.closest('.path-hops').getBoundingClientRect();
          const r = p.getBoundingClientRect();
          return r.right > host.right + 1 || r.left < host.left;
        }).length,
      };
    });
    assert(after.cls && after.active, 'toggle did not switch the table to full names');
    assert(after.ellipsised === 0, `${after.ellipsised} observer cells still end in "…"`);
    if (after.chipMax) assert(after.chipMax !== '120px', 'path chips still capped at 120px');
    assert(after.hScroll <= 0, `table overflows by ${after.hScroll}px in full-name mode`);
    assert(after.tallPaths === 0, `${after.tallPaths} paths wrapped onto a second line`);
    assert(after.offscreenPills === 0, `${after.offscreenPills} "+N" overflow pills are outside the visible path`);
  });

  await test('Full Names persists across reloads', async () => {
    await page.reload({ waitUntil: 'networkidle' });
    await page.waitForSelector('#pktTable tbody tr:not([id^=vscroll]) td.col-type',
      { state: 'attached', timeout: 30000 });
    const on = await page.evaluate(() =>
      document.getElementById('pktTable').classList.contains('pkt-full-names') &&
      document.getElementById('fullNamesToggle').classList.contains('active'));
    assert(on, 'full-name mode lost on reload');
    await page.click('#fullNamesToggle');
  });

  await test('hovering the +N pill lists the full path vertically; click pins it', async () => {
    await gotoPackets(page, { 'meshcore-full-names': 'true' });
    // At 1920px with full names the fixture's multi-hop paths always overflow.
    await pillsReady(page).catch(() => null);
    const pillSel = await page.evaluate(() => {
      const pills = Array.from(document.querySelectorAll('#pktTable .path-overflow-pill'));
      if (!pills.length) return null;
      pills[0].closest('tr').setAttribute('data-e2e-pill-row', '1');
      return '#pktTable tr[data-e2e-pill-row] .path-overflow-pill';
    });
    // At 1920px with full names the fixture's multi-hop paths always overflow;
    // no pill means the pill logic broke, not that there is nothing to test.
    assert(pillSel, 'no "+N" overflow pill rendered under Full Names');
    const hopsInRow = await page.$eval(pillSel, p =>
      p.closest('.path-hops').querySelectorAll('.arrow').length + 1);

    await page.hover(pillSel);
    await page.waitForSelector('#pathPopover', { timeout: 3000 });
    // The pill sits over the last chip: a translucent hover background let
    // the chip show through it.
    const hoverBg = await page.$eval(pillSel, p => getComputedStyle(p).backgroundColor);
    const alpha = /rgba\([^)]*,\s*([\d.]+)\)/.exec(hoverBg);
    assert(hoverBg !== 'transparent' && (!alpha || parseFloat(alpha[1]) === 1),
      `hovered pill background is not opaque: ${hoverBg}`);
    const pop = await page.evaluate(() => {
      const el = document.getElementById('pathPopover');
      const lis = Array.from(el.querySelectorAll('li'));
      return {
        items: lis.length,
        vertical: lis.every((li, i) => i === 0 || li.getBoundingClientRect().top > lis[i - 1].getBoundingClientRect().top),
        truncated: Array.from(el.querySelectorAll('.hop-named')).filter(c => c.scrollWidth > c.clientWidth + 1).length,
        title: el.querySelector('.path-popover-title').textContent,
      };
    });
    assert(pop.items === hopsInRow, `popover lists ${pop.items} hops, row has ${hopsInRow}`);
    assert(pop.vertical, 'hops are not stacked one per line');
    assert(pop.truncated === 0, `${pop.truncated} names truncated in the popover`);
    assert(pop.title.includes(hopsInRow + ' hop'), `title "${pop.title}" should count hops, not arrows`);

    // Moving away closes an unpinned popover (after a 150ms grace period).
    await page.mouse.move(5, 5);
    await page.waitForSelector('#pathPopover', { state: 'detached', timeout: 3000 })
      .catch(() => { throw new Error('popover still open after the pointer left'); });

    // Clicking pins it, and must not select the row underneath.
    const panelEmptyBefore = await page.$eval('#pktRight', el => el.classList.contains('empty'));
    await page.click(pillSel);
    await page.waitForSelector('#pathPopover', { timeout: 3000 });
    await page.mouse.move(5, 5);
    // Deliberately fixed: proving it stays open means outlasting the 150ms
    // close grace an unpinned popover would get.
    await page.waitForTimeout(400);
    assert(await page.$('#pathPopover'), 'pinned popover closed when the pointer left');
    const panelEmptyAfter = await page.$eval('#pktRight', el => el.classList.contains('empty'));
    assert(panelEmptyBefore === panelEmptyAfter, 'clicking the pill also selected the row');
    await page.keyboard.press('Escape');
    await page.waitForSelector('#pathPopover', { state: 'detached', timeout: 3000 })
      .catch(() => { throw new Error('Escape did not close the pinned popover'); });

    // Keyboard: focusing a pill shows the popover too. Escape above also
    // closes the detail panel, which re-renders the rows: query afresh.
    await pillsReady(page);
    await page.focus('#pktTable .path-overflow-pill');
    await page.waitForSelector('#pathPopover', { timeout: 3000 });
    await page.keyboard.press('Escape');

    // The popover lives on <body>: leaving the page must take it along.
    await page.click('#pktTable .path-overflow-pill');
    await page.waitForSelector('#pathPopover', { timeout: 3000 });
    await page.evaluate(() => { location.hash = '#/home'; });
    await page.waitForSelector('#pathPopover', { state: 'detached', timeout: 5000 })
      .catch(() => { throw new Error('pinned path popover survived navigating away from Packets'); });
    await page.evaluate(() => { localStorage.removeItem('meshcore-full-names'); });
  });

  await test('Full Names is carried in the URL', async () => {
    await gotoPackets(page);
    // Don't assume the starting state: the previous test turned Full Names on,
    // and the in-page setting (not just localStorage) can reach this page's
    // URL before gotoPackets reloads it. Check both directions instead.
    const state = () => page.evaluate(() => ({
      on: document.getElementById('pktTable').classList.contains('pkt-full-names'),
      inUrl: /[?&]fullNames=1\b/.test(location.hash),
      hash: location.hash,
    }));
    for (let n = 0; n < 2; n++) {
      const was = (await state()).on;
      await page.click('#fullNamesToggle');
      await page.waitForFunction(w => document.getElementById('pktTable').classList.contains('pkt-full-names') !== w, was);
      const s = await state();
      assert(s.on === s.inUrl, `Full Names is ${s.on ? 'on' : 'off'} but the URL says otherwise: ${s.hash}`);
    }
    // A shared link switches the mode on for a visitor whose own pref is off.
    await page.evaluate(() => { localStorage.setItem('meshcore-full-names', 'false'); });
    await page.goto(BASE + '/#/packets?timeWindow=10080&fullNames=1', { waitUntil: 'domcontentloaded' });
    await page.reload({ waitUntil: 'networkidle' });
    await page.waitForSelector('#pktTable tbody tr:not([id^=vscroll]) td.col-type', { state: 'attached', timeout: 30000 });
    const on = await page.$eval('#pktTable', t => t.classList.contains('pkt-full-names'));
    assert(on, 'fullNames=1 in the URL did not turn Full Names on');
    // ...for that page only: the visitor's own saved preference is untouched.
    const saved = await page.evaluate(() => localStorage.getItem('meshcore-full-names'));
    assert(saved === 'false', `opening a fullNames=1 link overwrote the saved preference with ${saved}`);
    await page.goto(BASE + '/#/packets?timeWindow=10080', { waitUntil: 'domcontentloaded' });
    await page.reload({ waitUntil: 'networkidle' });
    await page.waitForSelector('#pktTable tbody tr:not([id^=vscroll]) td.col-type', { state: 'attached', timeout: 30000 });
    const offAgain = await page.$eval('#pktTable', t => !t.classList.contains('pkt-full-names'));
    assert(offAgain, 'without the parameter the page did not return to the saved preference (off)');
    await page.evaluate(() => { localStorage.removeItem('meshcore-full-names'); });
  });

  await test('Observer column shrinks back once observer names arrive late', async () => {
    // Rows can render before /api/observers resolves (#1692), showing raw
    // 64-char pubkeys — ~600px under Full Names. grow() alone would keep the
    // column that wide after the names replace them.
    await page.route('**/api/observers*', async route => {
      await new Promise(r => setTimeout(r, 3000));
      await route.continue();
    });
    try {
      await gotoPackets(page, { 'meshcore-full-names': 'true' });
      await page.waitForFunction(() =>
        !Array.from(document.querySelectorAll('#pktTable td.col-observer'))
          .some(td => /[0-9A-F]{40}/.test(td.textContent)), null, { timeout: 15000 });
      await settle(page);
      const { th, widest } = await page.evaluate(() => {
        const cells = Array.from(document.querySelectorAll('#pktTable td.col-observer'))
          .filter(td => td.offsetParent !== null);
        return {
          th: document.querySelector('#pktTable th.col-observer').offsetWidth,
          widest: Math.max(...cells.map(td => cellContentWidth(td))),
        };
      });
      assert(th <= widest + 8, `Observer column ${th}px for ${widest}px of content: stuck at the pubkey width`);
    } finally {
      await page.unroute('**/api/observers*');
    }
  });

  await test('dragging a handle resizes the column; double-click fits it back', async () => {
    await gotoPackets(page);
    const flexHandles = await page.$$eval('#pktTable th.col-path .col-resize-handle, #pktTable th.col-details .col-resize-handle', h => h.length);
    assert(flexHandles === 0, 'Path/Details must not have drag handles: they share the remaining width');
    const before = await page.$eval('#pktTable th.col-hash', th => th.offsetWidth);
    // Grab the handle 2px inside the header: its outer half overhangs the
    // next header, which paints over it.
    const box = await page.$eval('#pktTable th.col-hash', th => {
      const r = th.getBoundingClientRect(); return { x: r.right - 2, y: r.top + r.height / 2 };
    });
    await page.mouse.move(box.x, box.y);
    await page.mouse.down();
    await page.mouse.move(box.x + 30, box.y, { steps: 3 });
    await page.mouse.move(box.x + 60, box.y, { steps: 3 });
    await page.mouse.up();
    await settle(page);
    const dragged = await page.evaluate(() => ({
      w: document.querySelector('#pktTable th.col-hash').offsetWidth,
      saved: JSON.parse(localStorage.getItem('meshcore-pkt-col-px') || '{}')['col-hash'],
    }));
    // The edge follows the pointer: no lag from fixed layout spreading the delta.
    assert(Math.abs(dragged.w - (before + 60)) <= 5, `dragged +60px from ${before}px, column is ${dragged.w}px`);
    assert(Math.abs(dragged.saved - dragged.w) <= 1, `saved ${dragged.saved}px, column ${dragged.w}px`);
    const edge = await page.$eval('#pktTable th.col-hash', th => {
      const r = th.getBoundingClientRect(); return { x: r.right - 2, y: r.top + r.height / 2 };
    });
    await page.mouse.dblclick(edge.x, edge.y);
    await settle(page);
    const reset = await page.evaluate(() => ({
      w: document.querySelector('#pktTable th.col-hash').offsetWidth,
      saved: JSON.parse(localStorage.getItem('meshcore-pkt-col-px') || '{}')['col-hash'],
    }));
    assert(Math.abs(reset.w - before) <= 1, `double-click left the column at ${reset.w}px, expected ${before}px`);
    assert(reset.saved === undefined, 'double-click did not forget the saved width');
  });

  await test('expanding a group leaves the column widths alone', async () => {
    // CI seeds one grouped transmission (fae0c9e6d357a814, 3 observations).
    await page.goto(BASE + '/#/packets?hash=fae0c9e6d357a814&timeWindow=0', { waitUntil: 'networkidle' });
    // Fail, don't skip: without the seeded row this case would silently stop
    // asserting (review on #2090).
    await page.waitForSelector('#pktTable tr.group-header', { timeout: 10000 })
      .catch(() => { throw new Error('seeded grouped row fae0c9e6d357a814 not found: check the CI fixture seeding step'); });
    await settle(page);
    const widths = () => page.$$eval('#pktTable thead th', ths => Object.fromEntries(ths
      .filter(th => th.offsetParent !== null && !th.matches('.col-path, .col-details'))
      .map(th => [th.className.split(' ')[0], th.offsetWidth])));
    const before = await widths();
    await page.click('#pktTable tr.group-header td.col-expand');
    await page.waitForSelector('#pktTable tr.group-child', { timeout: 10000 });
    await settle(page);
    const after = await widths();
    // Child rows name other observers and may legitimately need a few px
    // more; the bug was every column jumping ~14px from the row indent.
    const grew = Object.keys(before).filter(k => k !== 'col-time' && after[k] - before[k] > 8);
    assert(grew.length === 0, `columns widened on expand: ${grew.map(k => `${k} ${before[k]}→${after[k]}`).join(', ')}`);
    await page.keyboard.press('Escape');
  });

  await test('1100px with the detail panel open: the table still fits', async () => {
    // All columns are visible above 1024px, and the panel leaves ~650px.
    await page.setViewportSize({ width: 1100, height: 900 });
    await gotoPackets(page);
    await page.click('#pktTable tbody tr:not([id^=vscroll]) td.col-time');
    await page.waitForSelector('#pktRight:not(.empty)', { timeout: 10000 });
    await settle(page);
    const { hScroll } = await layout(page);
    assert(hScroll <= 0, `table overflows its wrapper by ${hScroll}px`);
    await page.keyboard.press('Escape');
  });

  await test('revealing and re-hiding responsive columns re-fits the table', async () => {
    // At <=1024px TableResponsive hides Hash, Observer and Rpt behind "+N hidden".
    await page.setViewportSize({ width: 1000, height: 900 });
    await gotoPackets(page);
    const pill = await page.$('#pktTable .col-hidden-pill:not(.col-rehide-pill)');
    assert(pill, 'no "+N hidden" pill at 1000px');
    const check = async (label) => {
      const l = await layout(page);
      assert(l.hScroll <= 0, `${label}: table overflows by ${l.hScroll}px`);
      assert(Math.abs(l.tableRight - l.lastCellRight) <= 2,
        `${label}: last cell ends at ${l.lastCellRight}, table at ${l.tableRight} (blank strip)`);
    };
    await pill.click();
    await page.waitForFunction(() => document.querySelector('#pktTable th.col-observer').offsetParent !== null, null, { timeout: 5000 })
      .catch(() => { throw new Error('Observer not revealed'); });
    await settle(page);
    await check('after reveal');
    await page.click('#pktTable .col-rehide-pill');
    await page.waitForFunction(() => document.querySelector('#pktTable th.col-observer').offsetParent === null, null, { timeout: 5000 });
    await settle(page);
    await check('after re-hide');
  });

  await test('mobile (375px): table fits without horizontal scroll', async () => {
    await page.setViewportSize({ width: 375, height: 800 });
    await gotoPackets(page);
    const { hScroll } = await layout(page);
    assert(hScroll <= 0, `table overflows by ${hScroll}px at 375px`);
  });

  await browser.close();

  const passed = results.filter(r => r.pass).length;
  const failed = results.length - passed;
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})().catch(err => {
  console.error(err);
  process.exit(1);
});
