/**
 * E2E (#1367): Channels page chat-app redesign — restore prod's row layout,
 * drop the analytics chip, and add a per-channel detail view.
 *
 * Design source: issue #1367 body + 4 design-lock comments
 * (Operator + Tufte): full-width chat-app rows with avatar / name /
 * preview / relative-time; no inline action chips on rows; tap a row
 * to slide into a full-screen messages view; back chevron + title.
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1367-channels-chat-app-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  \u2713 ' + name); }
  catch (e) { failed++; console.error('  \u2717 ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

async function run() {
  const launchOpts = { args: ['--no-sandbox'] };
  if (process.env.CHROMIUM_PATH) launchOpts.executablePath = process.env.CHROMIUM_PATH;
  const browser = await chromium.launch(launchOpts);

  // ----- Mobile (375x800) -----
  const ctx = await browser.newContext({ viewport: { width: 375, height: 800 } });
  const page = await ctx.newPage();

  await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#chList', { timeout: 10000 });
  // New rows use .ch-row; wait for at least one to render.
  await page.waitForFunction(() => {
    const l = document.getElementById('chList');
    return l && l.querySelectorAll('.ch-row').length > 0;
  }, { timeout: 15000 });
  await page.waitForTimeout(200);

  await step('channel rows use .ch-row, are ~80px tall, full-width', async () => {
    const data = await page.evaluate(() => {
      const rows = document.querySelectorAll('#chList .ch-row');
      if (!rows.length) return null;
      const r = rows[0];
      const rect = r.getBoundingClientRect();
      const parentW = r.parentElement.getBoundingClientRect().width;
      return { h: Math.round(rect.height), w: Math.round(rect.width), parentW: Math.round(parentW), count: rows.length };
    });
    assert(data, 'no .ch-row elements found');
    assert(data.h >= 72 && data.h <= 88, '.ch-row height must be 72-88px, got ' + data.h);
    // Full-width within its list container (allow 4px slop for borders/padding).
    assert(data.w >= data.parentW - 8, '.ch-row width ' + data.w + ' must fill parent ' + data.parentW);
  });

  await step('each row has .ch-avatar with hash-derived bg + 2-3 char text', async () => {
    const info = await page.evaluate(() => {
      const row = document.querySelector('#chList .ch-row');
      const av = row && row.querySelector('.ch-avatar');
      if (!av) return null;
      const bg = getComputedStyle(av).backgroundColor;
      return { text: (av.textContent || '').trim(), bg: bg };
    });
    assert(info, 'first row has no .ch-avatar');
    assert(info.text.length >= 1 && info.text.length <= 3, 'avatar text length must be 1-3, got "' + info.text + '"');
    // Background should be a real color, not transparent / none.
    assert(info.bg && info.bg !== 'rgba(0, 0, 0, 0)' && info.bg !== 'transparent',
      'avatar bg must be a real color, got ' + info.bg);
  });

  await step('row body has bold name, preview text, right-aligned timestamp', async () => {
    const data = await page.evaluate(() => {
      const row = document.querySelector('#chList .ch-row');
      const name = row && row.querySelector('.ch-row-name');
      const prev = row && row.querySelector('.ch-row-preview');
      const time = row && row.querySelector('.ch-row-time');
      if (!name || !prev || !time) return { missing: { name: !name, prev: !prev, time: !time } };
      const rowRect = row.getBoundingClientRect();
      const timeRect = time.getBoundingClientRect();
      const nameRect = name.getBoundingClientRect();
      return {
        nameWeight: getComputedStyle(name).fontWeight,
        timeRight: rowRect.right - timeRect.right,
        // Timestamp must sit to the right of the name's right edge.
        timeAfterName: timeRect.left >= nameRect.right - 4,
      };
    });
    assert(!data.missing, 'missing sub-elements: ' + JSON.stringify(data.missing || {}));
    const w = parseInt(data.nameWeight, 10) || 0;
    assert(w >= 600 || data.nameWeight === 'bold', 'channel name must be bold, got ' + data.nameWeight);
    assert(data.timeRight <= 20, 'timestamp must be right-aligned, got ' + data.timeRight + 'px from row right');
    assert(data.timeAfterName, 'timestamp must be to the right of the name');
  });

  await step('rows have NO inline share/remove action chips', async () => {
    const offenders = await page.evaluate(() => {
      const rows = document.querySelectorAll('#chList .ch-row');
      let bad = [];
      for (const r of rows) {
        if (r.querySelector('.ch-row-actions, .ch-share, .ch-remove, .ch-share-btn, .ch-remove-btn, [data-share-channel], [data-remove-channel]')) {
          bad.push(r.getAttribute('data-hash') || '?');
        }
      }
      return bad;
    });
    assert(offenders.length === 0,
      'inline action chips found on ' + offenders.length + ' rows: ' + offenders.slice(0, 3).join(','));
  });

  await step('header has NO analytics / chart-emoji chip', async () => {
    const hits = await page.evaluate(() => {
      const sidebar = document.querySelector('.ch-sidebar');
      const header = sidebar && sidebar.querySelector('.ch-sidebar-header');
      if (!header) return { noHeader: true };
      const hasLink = !!header.querySelector('.ch-analytics-link, a[href*="analytics"]');
      const hasEmoji = (header.textContent || '').indexOf('\uD83D\uDCCA') !== -1;
      return { hasLink, hasEmoji };
    });
    assert(!hits.noHeader, 'channels sidebar header not found');
    assert(!hits.hasLink, 'analytics link must be removed from header');
    assert(!hits.hasEmoji, '📊 emoji must be removed from header');
  });

  await step('tap a row → URL hash changes to channel detail route', async () => {
    const targetHash = await page.evaluate(() => {
      const r = document.querySelector('#chList .ch-row[data-hash]');
      return r ? r.getAttribute('data-hash') : null;
    });
    assert(targetHash, 'no .ch-row[data-hash] to click');
    await page.click('#chList .ch-row[data-hash="' + targetHash.replace(/"/g, '\\"') + '"]');
    await page.waitForFunction((h) => location.hash.indexOf(encodeURIComponent(h)) !== -1
      || location.hash.indexOf(h) !== -1, targetHash, { timeout: 5000 });
    const hash = await page.evaluate(() => location.hash);
    assert(hash.indexOf('/channels/') !== -1, 'URL hash should include /channels/<hash>, got ' + hash);
  });

  // ----- Detail view (mobile, after tap) -----
  await step('detail view header: back affordance + "<name> — <count> messages"', async () => {
    // The header already updates on selection; assert the back chevron and the title format.
    await page.waitForFunction(() => {
      const t = document.querySelector('#chHeader .ch-header-text');
      return t && /—\s*\d+\s*messages/i.test(t.textContent || '');
    }, { timeout: 8000 });
    const data = await page.evaluate(() => {
      const header = document.getElementById('chHeader');
      const back = header && header.querySelector('.ch-back, [data-action="ch-back"], [aria-label*="Back"]');
      const title = header && header.querySelector('.ch-header-text');
      return {
        hasBack: !!back,
        title: title ? (title.textContent || '').trim() : '',
      };
    });
    assert(data.hasBack, 'detail header must include a back button (.ch-back / data-action=ch-back)');
    assert(/—\s*\d+\s*messages/i.test(data.title), 'header title must be "<name> — <count> messages", got: ' + data.title);
  });

  await step('detail view renders at least one .ch-message (avatar + bubble + footer)', async () => {
    // Wait up to 8s for messages to load (some channels may be empty — pick the busiest).
    const ok = await page.evaluate(async () => {
      function sleep(ms){return new Promise(r=>setTimeout(r,ms));}
      for (let i = 0; i < 40; i++) {
        const m = document.querySelector('.ch-message');
        if (m) {
          const av = m.querySelector('.ch-avatar');
          const body = m.querySelector('.ch-message-bubble, .ch-msg-bubble');
          const foot = m.querySelector('.ch-message-meta, .ch-msg-meta');
          if (av && body && foot) return true;
        }
        await sleep(200);
      }
      return false;
    });
    assert(ok, '.ch-message with avatar+bubble+footer not rendered in detail view');
  });

  await ctx.close();

  // ----- Desktop (1024x800) -----
  const ctx2 = await browser.newContext({ viewport: { width: 1024, height: 800 } });
  const p2 = await ctx2.newPage();
  await p2.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
  await p2.waitForSelector('.ch-layout', { timeout: 10000 });
  await p2.waitForTimeout(200);

  await step('desktop (1024px): two-pane layout preserved', async () => {
    const dir = await p2.evaluate(() => {
      const l = document.querySelector('.ch-layout');
      return l ? getComputedStyle(l).flexDirection : null;
    });
    assert(dir === 'row', 'desktop ch-layout flex-direction must remain "row", got ' + dir);
  });

  await browser.close();
  console.log('\n' + passed + '/' + (passed + failed) + ' tests passed' + (failed ? ', ' + failed + ' failed' : ''));
  process.exit(failed > 0 ? 1 : 0);
}

run().catch(err => { console.error('Fatal:', err); process.exit(1); });
