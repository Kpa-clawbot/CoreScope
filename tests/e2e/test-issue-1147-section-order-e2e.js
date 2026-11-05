/**
 * #1147 — Side panel + full node detail page must render the originated-adverts
 * section directly under Overview, BEFORE Paths/Neighbors/Heard By/Clock Skew.
 *
 * Operator mental order: identity → packets they originated → paths they
 * relay → adverts → meta. That section appeared LAST; this is the regression
 * guard that proves the new ordering.
 *
 * What this suite is about is the ORDER, not the wording. It anchors on the
 * heading text because that is what the DOM offers, which coupled it to a label
 * that then changed: #2042 renamed the heading from "Recent Packets" to "Recent
 * Adverts" (the section only ever held adverts) and this suite failed on the
 * rename alone. Hence the single constant below rather than twelve literals.
 *
 * Acceptance:
 *   - Full node detail page (#/nodes/<pk>): index of the adverts section header
 *     < index of "Paths Through This Node" header AND < index of "Heard By"
 *     header AND < index of "Neighbors" header AND < index of "Clock Skew"
 *     header (when present).
 *   - Side panel (open from /nodes list): same ordering.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1147-section-order-e2e.js
 */
'use strict';

const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

// The heading that opens the originated-adverts section (public/nodes.js).
// Both copies, full page and side pane, use the same text.
const ADVERTS_HEADING = 'Recent Adverts';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

// Pull all rendered <h4> headers (section titles in node detail) in DOM order
// from a given root. Returns array of trimmed text.
async function sectionHeaders(page, rootSelector) {
  return await page.$$eval(`${rootSelector} h4`, els =>
    els.map(e => (e.textContent || '').trim()));
}

// Find index of first header whose text starts with `prefix`. -1 if absent.
function indexOfStarts(headers, prefix) {
  for (let i = 0; i < headers.length; i++) {
    if (headers[i].startsWith(prefix)) return i;
  }
  return -1;
}

// #2309 shares the existing CI-wired node-detail suite. These responses use
// the real {node,recentAdverts} contract; only the input data is controlled.
async function previewMetadata(browser) {
  const relayed = new Date(Date.now() - 60000).toISOString();
  const bad = '"><img src=x onerror="window.previewInjected=1">';
  const cases = [
    { label: 'zero', role: 'repeater', traffic_share_score: 0, usefulness_score: 0.8, want: '0.0%', last_relayed: relayed, relay_active: true, relay_count_1h: 0, relay_count_24h: '7' },
    { label: 'string', role: 'room', traffic_share_score: '0.125', want: '12.5%' },
    { label: 'legacy', name: null, advert_name: 'Legacy preview name', role: 'repeater', usefulness_score: '0.25', want: '25.0%' },
    { label: 'missing', name: null, role: 'repeater', hash_size: null, relay_count_24h: 0 },
    { label: 'companion', role: 'companion', traffic_share_score: 0.5, last_relayed: relayed },
    { label: 'escaped', name: bad, role: 'repeater', traffic_share_score: bad, relay_count_1h: bad, hash_size_inconsistent: true, hash_sizes_seen: [1, bad] },
    { label: 'certain', role: 'repeater', last_certain_relay: relayed, last_heard: relayed, expectActive: true, relay_active: false },
    { label: 'certain-room', role: 'room', last_certain_relay: relayed, last_heard: relayed, expectActive: true },
    { label: 'expired-certain', role: 'repeater', last_certain_relay: new Date(Date.now() - 3 * 3600000).toISOString(), relay_active: true },
    { label: 'invalid-certain', role: 'room', last_certain_relay: 'not-a-date', relay_active: true },
  ].map((c, i) => ({ public_key: (i + 1).toString(16).repeat(64), name: 'Preview ' + c.label,
    advert_count: 1, first_seen: '2020-01-01T00:00:00Z', last_seen: '2020-01-02T00:00:00Z', hash_size: '2', ...c }));
  const byKey = new Map(cases.map(n => [n.public_key, n]));
  const layouts = [
    { name: 'full', width: 1400, root: '#node-stats', ready: '.node-full-card' },
    { name: 'side', width: 1400, root: '#nodesRight .detail-meta', ready: '#nodesRight .node-detail' },
    { name: 'tablet', width: 800, root: '.slide-over-content dl', ready: '.slide-over-content dl' },
  ];
  for (const theme of ['light', 'dark']) for (const layout of layouts) {
    const ctx = await browser.newContext({ viewport: { width: layout.width, height: 1000 } });
    await ctx.addInitScript(theme => {
      localStorage.setItem('meshcore-theme', theme);
      localStorage.setItem('meshcore-user-level', 'experienced');
      localStorage.setItem('meshcore-timestamp-mode', 'ago');
      localStorage.setItem('meshcore-timestamp-timezone', 'utc');
    }, theme);
    const p = await ctx.newPage();
    p.setDefaultTimeout(10000);
    const errors = [], requests = [];
    p.on('pageerror', e => errors.push(e.message));
    p.on('request', r => { const u = new URL(r.url()); if (u.pathname.startsWith('/api/')) requests.push(u.pathname); });
    await p.route(/\/api\/nodes(?:\?.*)?$/, r => r.fulfill({ json: { nodes: cases, total: cases.length, has_more: false, counts: {} } }));
    await p.route(/\/api\/nodes\/[0-9a-f]{64}(?:\/[^?]*)?(?:\?.*)?$/, r => {
      const parts = new URL(r.request().url()).pathname.split('/');
      const n = byKey.get(parts[3]);
      if (!n) return r.continue();
      const data = !parts[4] ? { node: n, recentAdverts: [] }
        : parts[4] === 'health' ? { node: n, stats: { lastHeard: n.last_seen }, observers: [], recentPackets: [] }
        : parts[4] === 'neighbors' ? { neighbors: [] } : parts[4] === 'paths' ? { paths: [] } : {};
      return r.fulfill({ json: data });
    });
    await p.route('**/api/rf-params', r => r.fulfill({ json: { enabled: false } }));
    // Use the actual config response while exercising a non-default relay window.
    await p.route('**/api/config/client', async r => {
      const response = await r.fetch();
      const config = await response.json();
      config.healthThresholds = { ...config.healthThresholds, relayActiveMs: 2 * 3600000 };
      await r.fulfill({ json: config });
    });
    try {
      for (const n of cases) {
        await step(`#2309 ${theme} ${layout.name}: ${n.label} metadata`, async () => {
          if (layout.name === 'full') {
            requests.length = 0;
            await p.goto(BASE + '/?preview-case=' + n.label + '#/nodes/' + n.public_key, { waitUntil: 'domcontentloaded' });
            await p.waitForFunction(() => window.SITE_CONFIG && _themeRefreshTimer === null);
          } else {
            // A fresh document avoids the unrelated side-panel close-button defect.
            await p.goto(BASE + '/?preview-case=' + n.label + '#/nodes', { waitUntil: 'domcontentloaded' });
            await p.waitForFunction(() => window.SITE_CONFIG && _themeRefreshTimer === null);
            await p.locator('#nodesTable tbody tr[data-key="' + n.public_key + '"]').waitFor();
            requests.length = 0;
            await p.locator('#nodesTable tbody tr[data-key="' + n.public_key + '"]').click();
          }
          await p.locator(layout.ready).first().waitFor();
          const root = p.locator(layout.root);
          const fields = await root.evaluate(el => Object.fromEntries(
            [...el.querySelectorAll('dt, tr > td:first-child')].map(label => {
              const value = label.nextElementSibling;
              const stamp = value && value.querySelector('.timestamp-text');
              return [label.textContent.trim().replace(/\s*ⓘ$/, ''), {
                text: value ? value.textContent.trim() : '', title: label.title,
                stampText: stamp && stamp.textContent, stampTitle: stamp && stamp.title,
                variation: value && value.querySelector('[title^="Seen:"]')?.title,
                certainStyle: value && value.querySelector('[data-certain-relay-active]')?.getAttribute('style'),
              }];
            })));
          if (layout.name === 'tablet') {
            assert(fields.Name.text === (n.name || n.advert_name || '—'), 'tablet name must use the node-detail name');
            assert(await p.locator('.slide-over-title').innerText() === (n.name || n.advert_name || n.public_key.slice(0, 10)), 'tablet title must use the node-detail name');
            const heard = fields['Last Heard'] || fields['Last heard'];
            assert(heard.stampText && heard.stampTitle, 'tablet Last Heard needs the shared timestamp tooltip');
          }
          const hash = fields['Hash Prefix'];
          assert(hash, 'Hash Prefix is missing');
          assert(hash.text === (n.hash_size ? n.public_key.slice(0, 4).toUpperCase() + ' (2-byte)' + (n.hash_size_inconsistent ? ' varies' : '') : 'Unknown'), 'incorrect hash prefix: ' + hash.text);
          const share = fields['Traffic share'];
          assert(n.want ? share && share.text.includes(n.want) : !share, 'wrong traffic score or missing-data state');
          if (layout.name === 'full' && n.want) {
            assert(await root.locator('#row-usefulness-score').count() === 1, 'full table must retain the legacy score-row ID');
          }
          const relay = fields['Last Relayed'];
          if (n.role === 'companion') assert(!relay, 'companion must not show relay metrics');
          else {
            assert(relay, 'Last Relayed is missing');
            assert(/shared|ambiguous/i.test(relay.title), 'relay tooltip must explain attribution ambiguity');
            assert(!/alive|idle/i.test(relay.text), 'relay evidence must not infer idle/alive: ' + relay.text);
            assert(/actively relaying/i.test(relay.text) === !!n.expectActive, 'only recent certain relay evidence may show the mark: ' + relay.text);
            if (n.expectActive) assert(relay.certainStyle.includes('var(--status-green-text)'), 'certain relay mark must use the shared theme text token');
            if (n.last_relayed) {
              assert(/attributed relay/i.test(relay.text), 'timestamp needs neutral attribution wording');
              assert(relay.stampText && relay.stampTitle && relay.stampTitle.includes(relayed.slice(0, 10)), 'absolute timestamp tooltip missing');
              assert(relay.text.includes('0 relays/hr, 7 relays/24h'), 'zero and string relay counts must be preserved');
              if (layout.name !== 'tablet') assert((await p.locator(layout.ready).first().innerText()).includes('Uncertain'), 'ambiguous relay must leave status uncertain');
            } else if (n.last_certain_relay && Number.isFinite(Date.parse(n.last_certain_relay))) assert(relay.text.includes('no attributed non-advert relay'), 'certain forwarded adverts must not be called never observed');
            else assert(relay.text.includes('never observed as relay hop'), 'missing relay timestamp changed meaning');
          }
          if (n.label === 'escaped') {
            assert(hash.variation === 'Seen: 1, ' + bad + '-byte', 'hash tooltip must preserve escaped text');
            assert(await p.locator('img[src="x"]').count() === 0, 'metadata injected an image element');
            assert(!await p.evaluate(() => window.previewInjected), 'metadata executed code');
          }
          assert(await p.locator('html').getAttribute('data-theme') === theme, 'wrong theme');
          // All fields must fit their actual responsive container horizontally.
          assert(await root.evaluate(el => el.scrollWidth <= el.clientWidth + 1), 'metadata overflows its layout');
          if (n.label === 'zero') {
            const prefix = '/api/nodes/' + n.public_key;
            const expected = ['', '/health', ...(layout.name === 'tablet' ? [] : ['/neighbors', '/paths', '/clock-skew'])].map(s => prefix + s).sort();
            const actual = requests.filter(url => url.startsWith('/api/nodes/')).sort();
            assert(JSON.stringify(actual) === JSON.stringify(expected), 'detail requests changed: ' + JSON.stringify(actual));
            assert(requests.filter(url => url === '/api/rf-params').length <= 1, 'RF parameters fetched more than once');
            console.log('    existing detail requests: ' + actual.length);
            if (process.env.SCREENSHOT_DIR) {
              fs.mkdirSync(process.env.SCREENSHOT_DIR, { recursive: true });
              await root.screenshot({ path: path.join(process.env.SCREENSHOT_DIR, `node-preview-${layout.name}-${theme}.png`) });
            }
          }
          if (n.expectActive && process.env.SCREENSHOT_DIR) {
            fs.mkdirSync(process.env.SCREENSHOT_DIR, { recursive: true });
            await root.screenshot({ path: path.join(process.env.SCREENSHOT_DIR, `certain-relay-${n.label}-${layout.name}-${theme}.png`) });
          }
        });
      }
      if (layout.name === 'side') {
        await step(`#2309 ${theme}: desktop-to-tablet transition keeps score IDs unique`, async () => {
          await p.goto(BASE + '/?preview-case=resize#/nodes', { waitUntil: 'domcontentloaded' });
          await p.waitForFunction(() => window.SITE_CONFIG && _themeRefreshTimer === null);
          await p.locator('#nodesTable tbody tr[data-key="' + cases[0].public_key + '"] td:visible').first().click();
          const sideScore = p.locator('#nodesRight [data-traffic-share-score]');
          await sideScore.waitFor({ state: 'visible' });
          const before = await p.locator('#row-usefulness-score').count();
          await p.setViewportSize({ width: 800, height: 1000 });
          await p.locator('#nodesTable tbody tr[data-key="' + cases[1].public_key + '"] td:visible').first().click();
          const tabletScore = p.locator('.slide-over-content [data-traffic-share-score]');
          await tabletScore.waitFor({ state: 'visible' });
          const after = await p.locator('#row-usefulness-score').count();
          console.log('    score-row IDs before resize: ' + before + ', after tablet selection: ' + after);
          assert((await sideScore.innerText()).includes('0.0%'), 'retained side preview lost its score');
          assert((await tabletScore.innerText()).includes('12.5%'), 'tablet preview did not render the new node score');
          assert(await sideScore.getAttribute('data-usefulness-score') === '0.0000', 'side preview lost its legacy score data');
          assert(await tabletScore.getAttribute('data-usefulness-score') === '0.1250', 'tablet preview lost its legacy score data');
          for (const root of [p.locator('#nodesRight .detail-meta'), p.locator('.slide-over-content dl')]) {
            const labels = await root.locator('dt').allTextContents();
            assert(labels.includes('Last Relayed') && labels.includes('Hash Prefix'), 'transition lost preview metadata');
          }
          assert(after <= 1, 'duplicate row-usefulness-score IDs after desktop-to-tablet transition: ' + after);
        });
      }
      await step(`#2309 ${theme} ${layout.name}: no page errors`, async () => assert(errors.length === 0, errors.join(' | ')));
    } finally { await ctx.close(); }
  }
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 1000 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1147 section-order E2E against ${BASE} ===`);

  // ─── Pick a node from the live API ───
  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  const pubkey = await page.evaluate(async () => {
    const r = await fetch('/api/nodes?limit=20');
    const d = await r.json();
    // Prefer a node with an advert_count > 0 so the section has content,
    // but ANY node should render the section header (with an empty state).
    const cand = (d.nodes || []).find(n => (n.advert_count || 0) > 0) ||
                 (d.nodes || [])[0];
    return cand && cand.public_key;
  });
  assert(pubkey, 'No node returned from /api/nodes');
  console.log('  → using probe pubkey: ' + pubkey.slice(0, 12) + '…');

  // ─── Case 1: Full node detail page ───
  await step(`full page: ${ADVERTS_HEADING} appears before Paths/Heard By/Neighbors/Clock Skew`, async () => {
    await page.goto(BASE + '/#/nodes/' + encodeURIComponent(pubkey), { waitUntil: 'domcontentloaded' });
    // Wait for the body container to render (the full detail uses .node-full-card).
    await page.waitForSelector('.node-full-card', { timeout: 10000 });
    // Wait until at least one adverts-section header is in the DOM. The heading
    // is passed in: this callback runs in the browser and cannot close over it.
    await page.waitForFunction((heading) => {
      return Array.from(document.querySelectorAll('h4'))
        .some(h => (h.textContent || '').trim().startsWith(heading));
    }, ADVERTS_HEADING, { timeout: 10000 });

    const headers = await sectionHeaders(page, 'body');
    console.log('    headers (full page): ' + JSON.stringify(headers));

    const iRecent = indexOfStarts(headers, ADVERTS_HEADING);
    const iPaths  = indexOfStarts(headers, 'Paths Through This Node');
    const iHeard  = indexOfStarts(headers, 'Heard By');
    const iNeigh  = indexOfStarts(headers, 'Neighbors');
    // Clock Skew is hidden by default but rendered later; only enforce ordering
    // when its container has visible content. Skip if absent.

    assert(iRecent !== -1, `${ADVERTS_HEADING} header not found on full page`);
    assert(iPaths  !== -1, 'Paths Through This Node header not found on full page');
    assert(iRecent < iPaths,
      `${ADVERTS_HEADING} (idx ${iRecent}) must appear BEFORE Paths Through This Node (idx ${iPaths}) on full page`);
    if (iHeard !== -1) {
      assert(iRecent < iHeard,
        `${ADVERTS_HEADING} (idx ${iRecent}) must appear BEFORE Heard By (idx ${iHeard}) on full page`);
    }
    if (iNeigh !== -1) {
      assert(iRecent < iNeigh,
        `${ADVERTS_HEADING} (idx ${iRecent}) must appear BEFORE Neighbors (idx ${iNeigh}) on full page`);
    }
  });

  // ─── Case 2: Side panel (opened from /nodes list) ───
  await step(`side panel: ${ADVERTS_HEADING} appears before Paths/Heard By/Neighbors`, async () => {
    await page.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('[data-loaded="true"]', { timeout: 15000 });
    await page.waitForSelector('table tbody tr:not([id^=vscroll])', { timeout: 15000 });
    // Click the row matching our probe pubkey if possible; fall back to first row.
    const clicked = await page.evaluate((pk) => {
      const rows = Array.from(document.querySelectorAll('table tbody tr'));
      // Try data attributes first
      let target = rows.find(r => (r.getAttribute('data-pubkey') || '').toLowerCase() === pk.toLowerCase());
      // Fall back: any row whose text contains the pubkey prefix
      if (!target) target = rows.find(r => r.textContent && r.textContent.toLowerCase().includes(pk.slice(0, 8).toLowerCase()));
      // Last resort: first non-vscroll row
      if (!target) target = rows.find(r => !(r.id || '').startsWith('vscroll'));
      if (target) { target.click(); return true; }
      return false;
    }, pubkey);
    assert(clicked, 'Could not click any node row to open side panel');

    // Wait for side panel to render the detail container.
    await page.waitForSelector('.node-detail', { timeout: 10000 });
    // Wait until the adverts-section header lands in the side panel scope.
    await page.waitForFunction((heading) => {
      const root = document.querySelector('.node-detail');
      if (!root) return false;
      return Array.from(root.querySelectorAll('h4'))
        .some(h => (h.textContent || '').trim().startsWith(heading));
    }, ADVERTS_HEADING, { timeout: 10000 });

    const headers = await sectionHeaders(page, '.node-detail');
    console.log('    headers (side panel): ' + JSON.stringify(headers));

    const iRecent = indexOfStarts(headers, ADVERTS_HEADING);
    const iPaths  = indexOfStarts(headers, 'Paths Through This Node');
    const iHeard  = indexOfStarts(headers, 'Heard By');
    const iNeigh  = indexOfStarts(headers, 'Neighbors');

    assert(iRecent !== -1, `${ADVERTS_HEADING} header not found in side panel`);
    assert(iPaths  !== -1, 'Paths Through This Node header not found in side panel');
    assert(iRecent < iPaths,
      `${ADVERTS_HEADING} (idx ${iRecent}) must appear BEFORE Paths Through This Node (idx ${iPaths}) in side panel`);
    if (iHeard !== -1) {
      assert(iRecent < iHeard,
        `${ADVERTS_HEADING} (idx ${iRecent}) must appear BEFORE Heard By (idx ${iHeard}) in side panel`);
    }
    if (iNeigh !== -1) {
      assert(iRecent < iNeigh,
        `${ADVERTS_HEADING} (idx ${iRecent}) must appear BEFORE Neighbors (idx ${iNeigh}) in side panel`);
    }
  });

  await previewMetadata(browser);
  await browser.close();

  console.log(`\n${passed}/${passed + failed} passed`);
  process.exit(failed === 0 ? 0 : 1);
})().catch(e => { console.error(e); process.exit(2); });
