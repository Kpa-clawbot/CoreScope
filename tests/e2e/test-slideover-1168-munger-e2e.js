/**
 * E2E (#1168 Munger review): SlideOver hardening.
 *
 * Three regressions surfaced by Munger persona on PR #1168:
 *
 *   (1) rAF-clobber: close()'s deferred focus-restore captured `target`
 *       in closure scope. If close() ran, then a NEW open() ran on the
 *       SAME microtask before rAF fired, the old rAF would `target.focus()`
 *       AFTER the new panel opened, stealing focus back to row A's row.
 *       Fix: openSeq counter + rAF guard — only restore if no newer open
 *       has happened since close-time.
 *
 *   (2) hashchange: `location.hash` changing (e.g. user navigates
 *       /#/packets → /#/nodes via the URL bar or a hash anchor) did NOT
 *       close the open slide-over. Panel + backdrop + scroll-lock
 *       leaked across pages. Fix: window.addEventListener('hashchange',
 *       () => isOpen() && close()).
 *
 *   (3) Scroll-lock corruption: capturing literal body.style.overflow at
 *       open-time and restoring on close means two cooperating modal
 *       surfaces (e.g. SlideOver + ChannelColorPicker) running concurrently
 *       can leave overflow in a wrong state (last-writer-wins). Fix:
 *       reference-counted `body.scroll-locked` class — multiple lockers
 *       each add/remove independently; class only removed when count==0.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-slideover-1168-munger-e2e.js
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

async function holdPacketDetails(page, hashes) {
  // Hold decoded responses at the same boundary the row handler awaits.
  await page.evaluate(hashes => {
    const read = window.api;
    window.heldRows = new Map();
    window.api = async function (path, options) {
      const data = await read(path, options);
      const hash = data?.packet?.hash;
      if (hashes.includes(hash) && path === '/packets/' + hash) {
        await new Promise(resolve => { window.heldRows.set(hash, resolve); });
      }
      return data;
    };
  }, hashes);
  return {
    held: hash => page.waitForFunction(hash => window.heldRows.has(hash), hash),
    release: hash => page.evaluate(async hash => {
      window.heldRows.get(hash)();
      await new Promise(requestAnimationFrame);
    }, hash),
  };
}

const desktopHashes = ['aaaaaaaaaaaa0001', 'bbbbbbbbbbbb0002'];
async function openDesktopRows(page, count) {
  const now = new Date().toISOString();
  const packets = desktopHashes.map((hash, i) => ({
    hash, id: i + 1, count, observation_count: count, latest: now,
    path_json: count === 1 ? '["FA"]' : '[]',
  }));
  await page.route(url => url.pathname === '/api/packets', route =>
    route.fulfill({ json: { packets, total: packets.length } }));
  for (const packet of packets) {
    const observations = Array.from({ length: count }, (_, i) => ({
      id: 10 * packet.id + i, hash: packet.hash, path_json: packet.path_json, timestamp: now,
    }));
    await page.route('**/api/packets/' + packet.hash, route =>
      route.fulfill({ json: { packet, observations } }));
  }
  await page.goto(BASE + '/#/packets?timeWindow=0', { waitUntil: 'domcontentloaded' });
  await page.locator('#pktBody tr[data-hash]').nth(1).waitFor();
}

async function assertDesktopPacket(page, hash) {
  const panel = page.locator('#pktRight:not(.empty)');
  try { await panel.locator('[data-packet-hash="' + hash + '"]').waitFor(); }
  catch { throw new Error('expected rendered packet ' + hash + ', panel shows ' + await panel.innerText()); }
  const route = new URL(page.url()).hash;
  const title = await page.title();
  assert(route === '#/packets/' + hash + '?timeWindow=0', 'wrong packet address: ' + route);
  assert(title.includes(hash.slice(0, 12)), 'wrong packet title: ' + title);
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log(`\n=== #1168 Munger SlideOver hardening E2E against ${BASE} ===`);

  // ============================================================
  // Item 1: rAF-clobber — close A then immediately open B; the
  // pending rAF from close(A) must NOT focus row A's row after
  // panel B is open. Panel B (or its first focusable) wins.
  // ============================================================
  {
    const ctx = await browser.newContext({ viewport: { width: 800, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));
    await page.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#nodesTable tbody tr[data-value]', { timeout: 8000 });

    await step('rAF-clobber: close-then-open does NOT steal focus back to row A', async () => {
      const result = await page.evaluate(async () => {
        if (!window.SlideOver) return { ok: false, why: 'no SlideOver' };
        // Synthesize a "row A" that close() would otherwise focus on rAF.
        const fakeRowA = document.createElement('button');
        fakeRowA.id = '__munger_rowA';
        fakeRowA.textContent = 'rowA';
        document.body.appendChild(fakeRowA);
        fakeRowA.focus();

        // Open A — capturing fakeRowA as prevFocus.
        const aContent = window.SlideOver.open({ title: 'A' });
        aContent.innerHTML = '<button id="__a_btn">a</button>';

        // Close A. close() schedules an rAF that would focus fakeRowA
        // when it fires. Without the openSeq guard this rAF will
        // run AFTER the next open() and steal focus.
        window.SlideOver.close();

        // IMMEDIATELY open B before the rAF fires.
        const bContent = window.SlideOver.open({ title: 'B' });
        bContent.innerHTML = '<button id="__b_btn">b</button>';
        // Move focus into B explicitly (caller convention — first focusable).
        document.querySelector('#__b_btn').focus();

        // Wait two animation frames so the stale rAF would have fired.
        await new Promise((res) => requestAnimationFrame(() => requestAnimationFrame(res)));

        const active = document.activeElement;
        const result = {
          ok: true,
          panelOpen: window.SlideOver.isOpen(),
          activeId: active && active.id,
          activeTag: active && active.tagName,
          // Critical: focus is NOT on the closed-A's originating fake row.
          stoleBackToA: active === fakeRowA,
        };
        // Cleanup
        window.SlideOver.close();
        fakeRowA.remove();
        return result;
      });
      assert(result.ok, 'precondition: ' + JSON.stringify(result));
      assert(result.panelOpen, 'panel B should remain open after stale rAF window');
      assert(!result.stoleBackToA,
        'Stale rAF from close(A) clobbered focus back to row A after open(B). ' +
        'activeId=' + result.activeId + ', tag=' + result.activeTag);
    });

    await ctx.close();
  }

  // ============================================================
  // Item 2: hashchange cleanup. Open a real packet preview, navigate
  // away, and retain the destination as well as releasing the overlay.
  // ============================================================
  {
    const ctx = await browser.newContext({ viewport: { width: 800, height: 900 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));
    await page.goto(BASE + '/#/home', { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => window.SITE_CONFIG && window.SITE_CONFIG.timestamps && _themeRefreshTimer === null);
    const homeTitle = await page.title();
    // #1923 follow-up: pin an explicit window so this step tests the munger
    // slide-over rather than the clock, the same way #1924 pinned
    // test-slideover-1056-e2e.js. The packets page defaults to
    // `since = now - 15 min` client-side, and the freshened fixture's newest
    // rows age out of that window ~15 minutes after freshen-fixture.sh runs.
    // Tablet views accept at most 180 minutes; All time or larger values
    // fall back to the default 15-minute window.
    await page.goto(BASE + '/#/packets?timeWindow=180', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#pktTable tbody tr[data-action]', { timeout: 8000 });
    const packetListTitle = await page.title();

    await step('hashchange: packet cleanup preserves the destination and title', async () => {
      // A generic SlideOver has no packet onClose callback and misses #2380.
      await page.locator('#pktTable tbody tr[data-action]').first().locator('td').first().click();
      await page.waitForSelector('.slide-over-panel');
      const before = await page.evaluate(() => ({
        open: window.SlideOver.isOpen(),
        bodyHasLockClass: document.body.classList.contains('scroll-locked'),
        bodyOverflowComputed: getComputedStyle(document.body).overflow,
      }));
      assert(before.open, 'precondition: panel should be open');

      // Trigger a hashchange.
      await page.evaluate(() => { location.hash = '#/home'; });
      await page.waitForSelector('.slide-over-panel', { state: 'detached' });

      const after = await page.evaluate(() => {
        const p = document.querySelector('.slide-over-panel');
        const b = document.querySelector('.slide-over-backdrop');
        function shown(el) {
          if (!el) return false;
          if (el.hidden) return false;
          const r = el.getBoundingClientRect();
          return r.width > 0 && r.height > 0;
        }
        return {
          hash: location.hash,
          title: document.title,
          isOpen: window.SlideOver.isOpen(),
          panelShown: shown(p),
          backdropShown: shown(b),
          bodyHasLockClass: document.body.classList.contains('scroll-locked'),
          bodyOverflowComputed: getComputedStyle(document.body).overflow,
        };
      });
      assert(!after.isOpen, 'SlideOver.isOpen() must be false after hashchange: ' + JSON.stringify(after));
      assert(!after.panelShown, 'panel must be hidden after hashchange: ' + JSON.stringify(after));
      assert(!after.backdropShown, 'backdrop must be hidden after hashchange: ' + JSON.stringify(after));
      assert(!after.bodyHasLockClass, 'body must NOT carry scroll-locked class after hashchange: ' + JSON.stringify(after));
      assert(after.bodyOverflowComputed !== 'hidden', 'body scroll lock must be released after hashchange: ' + JSON.stringify(after));
      assert(after.hash === '#/home' && after.title === homeTitle,
        'packet cleanup must preserve destination #/home and title ' + JSON.stringify(homeTitle) + ': ' + JSON.stringify(after));
    });

    for (const method of ['X', 'Escape', 'backdrop', 'resize']) {
      await step('packet dismissal: ' + method + ' returns to the list and restores focus', async () => {
        await page.goto(BASE + '/#/packets?timeWindow=180', { waitUntil: 'domcontentloaded' });
        const row = page.locator('#pktTable tbody tr[data-action]').first();
        await row.waitFor();
        const rowValue = await row.getAttribute('data-value');
        const listTitle = await page.title();
        await row.locator('td').first().click();
        await page.waitForSelector('.slide-over-panel');
        if (method === 'X') await page.locator('.slide-over-close').click();
        else if (method === 'Escape') await page.keyboard.press('Escape');
        else if (method === 'backdrop') await page.locator('.slide-over-backdrop').click({ position: { x: 10, y: 20 } });
        else await page.setViewportSize({ width: 1440, height: 900 });
        await page.waitForSelector('.slide-over-panel', { state: 'detached' });
        const after = await page.evaluate(async () => {
          await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
          return {
            hash: location.hash,
            title: document.title,
            backdrop: !!document.querySelector('.slide-over-backdrop'),
            locked: document.body.classList.contains('scroll-locked'),
            overflow: getComputedStyle(document.body).overflow,
            focusedValue: document.activeElement && document.activeElement.getAttribute('data-value'),
          };
        });
        assert(after.hash === '#/packets' && after.title === listTitle,
          'manual dismissal must return to the packet list and title: ' + JSON.stringify(after));
        assert(!after.backdrop && !after.locked && after.overflow !== 'hidden',
          'manual dismissal must release backdrop and scroll lock: ' + JSON.stringify(after));
        assert(after.focusedValue === rowValue,
          'manual dismissal must restore the originating row: ' + JSON.stringify(after));
      });
    }

    await page.setViewportSize({ width: 800, height: 900 });
    const { packets } = await (await page.request.get(BASE + '/api/packets?limit=20')).json();
    const packet = packets.find(p => p.hash && p.hash !== p.hash.toUpperCase());
    assert(packet, 'fixture needs a packet whose hash has a letter');
    const other = packets.find(p => p.hash && p.hash !== packet.hash);
    assert(other, 'fixture needs a second distinct packet');

    // Keep the real detail lookup for every alias. Single-observation fixtures
    // need a second row to exercise the observation link in the preview.
    await page.route(new RegExp('/api/packets/(' + packet.id + '|' + packet.hash + ')$', 'i'), async route => {
      const response = await route.fetch();
      const detail = await response.json();
      if (detail.observations.length === 1) {
        const first = detail.observations[0];
        detail.observations.push({ ...first, id: first.id + 1 });
        detail.observation_count = 2;
      }
      await route.fulfill({ response, json: detail });
    });

    const aliases = [['numeric ID', packet.id], ['uppercase hash', packet.hash.toUpperCase()]];
    for (const [kind, key] of [...aliases, ['lowercase hash', packet.hash]]) {
      await step(kind + ' preview returns to the list on Escape after selecting an observation', async () => {
        await page.goto(BASE + '/#/packets/' + key);
        const row = page.locator('.slide-over-panel .detail-obs-row').nth(1);
        const obsId = await row.getAttribute('data-obs-id');
        const historyLength = await page.evaluate(() => history.length);
        await row.click();
        const selected = new URL(page.url()).hash;
        const [path, query] = selected.split('?');
        assert(path === '#/packets/' + packet.hash && new URLSearchParams(query).get('obs') === obsId,
          'selected observation must have its canonical packet URL: ' + selected);
        await page.keyboard.press('Escape');
        await page.waitForSelector('.slide-over-panel', { state: 'detached' });
        const after = await page.evaluate(() => ({ hash: location.hash, title: document.title, length: history.length }));
        assert(after.hash === '#/packets' && after.title === packetListTitle && after.length === historyLength,
          'Escape must restore the packet list and title without adding history: ' + JSON.stringify(after));
      });
    }

    const detailFor = hash => page.locator('.slide-over-panel .copy-link-btn[data-packet-hash="' + hash + '"]');
    await step('Back from an open packet preview preserves the other packet detail', async () => {
      await page.goto(BASE + '/#/packets/' + other.hash);
      await detailFor(other.hash).waitFor();
      const previous = new URL(page.url()).hash;
      const title = await page.title();
      assert(previous.split('?')[0] === '#/packets/' + other.hash, 'expected the other packet URL: ' + previous);
      await page.goto(BASE + '/#/packets/' + packet.hash);
      await detailFor(packet.hash).waitFor();
      await page.goBack();
      await detailFor(other.hash).waitFor();
      const after = new URL(page.url()).hash;
      assert(after === previous && await page.title() === title,
        'Back must retain the previous packet URL and title: ' + after);
    });

    for (const [kind, key] of [...aliases, ['different packet', other.hash]]) {
      await step('Back from ' + kind + ' restores the bookmarked observation', async () => {
        await page.goto(BASE + '/#/home');
        await page.locator('.slide-over-panel').waitFor({ state: 'detached' });
        await page.goto(BASE + '/#/packets/' + packet.hash);
        const second = page.locator('.slide-over-panel .detail-obs-row').nth(1);
        const obs = await second.getAttribute('data-obs-id');
        await second.click();
        await page.locator('.slide-over-panel .observation-current[data-obs-id="' + obs + '"]').waitFor();
        const previous = new URL(page.url()).hash;
        const title = await page.title();

        // Aliases render the same packet. Wait for each old detail element
        // to detach so the checks read the replacement preview.
        const original = await detailFor(packet.hash).elementHandle();
        await page.goto(BASE + '/#/packets/' + key);
        await original.waitForElementState('hidden');
        const away = await detailFor(key === other.hash ? other.hash : packet.hash).elementHandle();
        const historyLength = await page.evaluate(() => history.length);
        await page.goBack();
        await away.waitForElementState('hidden');
        await page.evaluate(() => {
          window.__copiedPacketLink = null;
          navigator.clipboard.writeText = async text => { window.__copiedPacketLink = new URL(text).hash; };
        });
        await detailFor(packet.hash).click();
        const after = await page.evaluate(() => ({
          hash: location.hash, title: document.title, length: history.length,
          obs: document.querySelector('.slide-over-panel .observation-current')?.dataset.obsId,
          copied: window.__copiedPacketLink,
        }));
        assert(after.hash === previous && after.title === title && after.length === historyLength &&
          after.obs === obs && after.copied === '#/packets/' + packet.hash + '?obs=' + obs,
          'Back must restore the URL, title, selected observation and copy link: ' + JSON.stringify(after));
      });
    }

    await ctx.close();
  }

  // ============================================================
  // Item 3: Scroll-lock ref-count via class. Two simulated
  // lockers can independently add/remove without corruption.
  // The fix uses `body.scroll-locked` class (CSS supplies
  // overflow:hidden) — class-add is idempotent; removal only
  // happens when the last locker releases. We assert this with
  // a direct simulation independent of which surface owns it.
  // ============================================================
  {
    const ctx = await browser.newContext({ viewport: { width: 800, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));
    await page.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#nodesTable tbody tr[data-value]', { timeout: 8000 });

    await step('scroll-lock: ref-counted class survives interleaved lockers without corruption', async () => {
      const result = await page.evaluate(() => {
        if (!window.__scrollLock) {
          return { ok: false, why: 'window.__scrollLock helper not exposed (item 3 not implemented)' };
        }
        // Simulate locker A acquiring → CSS class present + overflow hidden.
        const tokenA = window.__scrollLock.acquire();
        const afterA = {
          cls: document.body.classList.contains('scroll-locked'),
          ovf: getComputedStyle(document.body).overflow,
        };
        // Simulate locker B acquiring → still locked.
        const tokenB = window.__scrollLock.acquire();
        const afterB = {
          cls: document.body.classList.contains('scroll-locked'),
          ovf: getComputedStyle(document.body).overflow,
        };
        // Locker A releases first → still locked because B holds it.
        window.__scrollLock.release(tokenA);
        const afterAreleased = {
          cls: document.body.classList.contains('scroll-locked'),
          ovf: getComputedStyle(document.body).overflow,
        };
        // Locker B releases → unlocked.
        window.__scrollLock.release(tokenB);
        const afterBreleased = {
          cls: document.body.classList.contains('scroll-locked'),
          ovf: getComputedStyle(document.body).overflow,
        };
        return { ok: true, afterA, afterB, afterAreleased, afterBreleased };
      });
      assert(result.ok, 'precondition: ' + JSON.stringify(result));
      assert(result.afterA.cls, 'after A acquire: scroll-locked class missing');
      assert(result.afterA.ovf === 'hidden', 'after A acquire: overflow not hidden (got ' + result.afterA.ovf + ')');
      assert(result.afterB.cls, 'after B acquire: scroll-locked class missing');
      assert(result.afterAreleased.cls, 'after A release: scroll-locked must STAY (B still holds): ' + JSON.stringify(result.afterAreleased));
      assert(result.afterAreleased.ovf === 'hidden', 'after A release: overflow must stay hidden (B still holds)');
      assert(!result.afterBreleased.cls, 'after B release: scroll-locked class must be gone: ' + JSON.stringify(result.afterBreleased));
      assert(result.afterBreleased.ovf !== 'hidden', 'after B release: overflow must NOT be hidden (got ' + result.afterBreleased.ovf + ')');
    });

    await ctx.close();
  }

  for (const destination of ['detail link', 'second row', 'nodes page']) {
    await step('delayed packet row preserves the ' + destination + ' selection', async () => {
      const page = await browser.newPage({ viewport: { width: 800, height: 900 } });
      page.setDefaultTimeout(8000);
      try {
        const list = '#/packets?timeWindow=180';
        await page.goto(BASE + '/' + list, { waitUntil: 'domcontentloaded' });
        const rows = page.locator('#pktBody tr[data-hash]');
        await rows.nth(1).waitFor();
        const first = page.locator('#pktBody tr[data-action="select-hash"]').first();
        // An all-grouped fixture exposes the same row action in the flat list.
        if (!await first.count()) await page.locator('#fGroup').click();
        const a = await first.getAttribute('data-hash');
        const other = page.locator('#pktBody tr[data-hash]:not([data-hash="' + a + '"])').first();
        const b = await other.getAttribute('data-hash');

        const secondClick = destination === 'second row';
        const { held, release } = await holdPacketDetails(page, secondClick ? [a, b] : [a]);
        await first.locator('.col-time').click();
        await held(a);

        const leftPackets = destination === 'nodes page';
        const target = leftPackets ? '#/nodes' : '#/packets/' + b + '?timeWindow=180';
        if (secondClick) {
          await other.locator('.col-time').click();
          await held(b);
          await release(a);
          assert(!new URL(page.url()).hash.includes(a), 'A took over while the newer row was loading');
          await release(b);
        } else await page.evaluate(route => { location.hash = route; }, target);
        const panel = '.slide-over-panel[data-state="open"]';
        if (leftPackets) await page.locator('#nodesTable').waitFor();
        else await page.locator(panel + ' [data-packet-hash="' + b + '"]').waitFor();
        const title = await page.title();
        assert(title.includes(leftPackets ? 'Nodes' : b.slice(0, 12)), 'wrong destination title: ' + title);
        assert(new URL(page.url()).hash === target, 'wrong destination URL before releasing A');

        if (!secondClick) await release(a);
        const after = await page.evaluate(panel => ({
          route: location.hash, title: document.title,
          shown: document.querySelector(panel + ' [data-packet-hash]')?.dataset.packetHash || null,
          open: !!document.querySelector(panel),
        }), panel);
        assert(after.route === target && after.title === title && after.open === !leftPackets
          && after.shown === (leftPackets ? null : b),
          'expected ' + target + ' / ' + title + ' after releasing A, got ' + JSON.stringify(after));
      } finally { await page.close(); }
    });
  }

  const [a, b] = desktopHashes;
  for (const order of [[a, b], [b, a]]) {
    await step('both groups expand with B selected when ' + (order[0] === a ? 'A' : 'B') + ' responds first', async () => {
      const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
      page.setDefaultTimeout(8000);
      try {
        await openDesktopRows(page, 2);
        const { held, release } = await holdPacketDetails(page, desktopHashes);
        for (const hash of desktopHashes) {
          await page.locator('#pktBody tr[data-hash="' + hash + '"] .col-expand').click();
          await held(hash);
        }
        for (const hash of order) await release(hash);
        await assertDesktopPacket(page, b);
        for (const [i, hash] of desktopHashes.entries()) {
          const ids = await page.locator('#pktBody tr.group-child[data-parent-hash="' + hash + '"]')
            .evaluateAll(rows => rows.map(row => Number(row.dataset.id)).sort((x, y) => x - y));
          const first = 10 * (i + 1);
          assert(ids.join(',') === [first, first + 1].join(','),
            'expected both observations for ' + hash + ', got [' + ids + ']');
        }
      } finally { await page.close(); }
    });
  }

  await step('a failed second row leaves the first packet able to finish rendering', async () => {
    const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
    page.setDefaultTimeout(8000);
    let releaseHops;
    const hops = new Promise(resolve => { releaseHops = resolve; });
    try {
      // A's path needs the resolver. Hold its real lookup through the failed click.
      await page.route('**/api/iata-coords', async route => { await hops; await route.continue(); });
      await openDesktopRows(page, 1);
      await page.route('**/api/packets/' + b, route => route.fulfill({ status: 404, json: {} }));
      await page.locator('#pktBody tr[data-hash="' + a + '"] .col-time').click();
      await page.locator('#pktRight').getByText('Loading…', { exact: true }).waitFor();
      const failure = page.waitForResponse(res => res.url().endsWith('/api/packets/' + b) && res.status() === 404);
      await page.locator('#pktBody tr[data-hash="' + b + '"] .col-time').click();
      await (await failure).finished();
      releaseHops();
      await assertDesktopPacket(page, a);
    } finally { releaseHops(); await page.close(); }
  });

  await browser.close();

  console.log(`\n=== #1168 Munger SlideOver hardening: ${passed} passed, ${failed} failed ===`);
  process.exit(failed ? 1 : 0);
})();
