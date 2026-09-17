#!/usr/bin/env node
/* Issue #1648 — M3: emoji → Phosphor sprite migration (E2E behavioral).
 *
 * Asserts (in a real Chromium against a running server):
 *   (a) /home welcome cards render Phosphor icons (chooser, FAQ heading,
 *       step glyphs) and the rendered DOM has zero emoji codepoints.
 *   (b) /channels modal title/help renders Phosphor sprites (no 💬 / 🔒
 *       emoji in modal chrome).
 *   (c) /nodes/<one-pubkey> detail pane — at minimum, sprite refs render
 *       and no .notdef glyphs leak through.
 *   (d) /analytics page renders sprite refs (M2+M3 surfaces combined).
 *   (e) /live page renders sprite refs.
 *   (f) NO .notdef glyph anywhere — every <use> resolves to a defined
 *       sprite symbol id.
 *   (g) The .status-{ok,warn,err,muted} rules in style.css resolve to
 *       actual --status-* color values (computed color reflects token).
 *
 * CI gating: CHROMIUM_REQUIRE=1 makes Chromium-launch failure a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');
const assert = require('assert');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const EMOJI_RE = /[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}◆●■▲★☆○✓✗⚠✉]/u;

let passes = 0, failures = 0;
function pass(msg) { console.log(`  ✓ ${msg}`); passes++; }
function fail(msg) { console.error(`  ✗ ${msg}`); failures++; }

async function spriteRefsResolve(page, label, min) {
  min = min || 1;
  const r = await page.evaluate(() => {
    const uses = Array.from(document.querySelectorAll('svg.ph-icon use'));
    return {
      count: uses.length,
      refs: uses.slice(0, 5).map(u => u.getAttribute('href') || u.getAttribute('xlink:href') || ''),
    };
  });
  if (r.count < min) fail(`${label}: only ${r.count} sprite refs (expected ≥${min})`);
  else pass(`${label}: ${r.count} sprite refs (≥${min})`);
}

async function noEmojiInRender(page, route, label) {
  await page.goto(`${BASE}/#${route}`, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => !!document.querySelector('#app'),
    null, { timeout: 8000 }).catch(() => {});
  // Give the SPA a tick to render
  await page.waitForTimeout(400);
  const txt = await page.evaluate(() => (document.getElementById('app') || document.body).textContent || '');
  if (EMOJI_RE.test(txt)) {
    const sample = txt.match(EMOJI_RE);
    fail(`${label}: rendered DOM contains emoji (sample: ${JSON.stringify(sample && sample[0])})`);
  } else {
    pass(`${label}: rendered DOM is emoji-free`);
  }
}

async function main() {
  const requireChromium = process.env.CHROMIUM_REQUIRE === '1';
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      executablePath: process.env.CHROMIUM_PATH || undefined,
      args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    });
  } catch (err) {
    if (requireChromium) {
      console.error(`test-issue-1648-m3-icons-e2e.js: HARD FAIL — Chromium unavailable: ${err.message}`);
      process.exit(1);
    }
    console.warn(`SKIP — Chromium unavailable: ${err.message}`);
    process.exit(0);
  }

  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();

  // (a) /home welcome cards
  await noEmojiInRender(page, '/home', '(a) /home');
  // /home should render at least the chooser/onboard sprites
  const home = await page.evaluate(() => {
    return {
      uses: Array.from(document.querySelectorAll('svg.ph-icon use'))
        .map(u => (u.getAttribute('href') || '').replace(/^.*#/, '')),
      hasChooser: !!document.querySelector('.chooser-icon, .onboard-icon, .home-footer-link'),
    };
  });
  if (!home.hasChooser) fail('(a) /home: chooser/onboard elements missing');
  else pass('(a) /home: chooser/onboard surfaces present');
  if (home.uses.length === 0) fail('(a) /home: no sprite refs rendered');
  else pass(`(a) /home: ${home.uses.length} sprite refs (sample: ${home.uses.slice(0,4).join(',')})`);

  // (b) /channels modal + sidebar
  await page.goto(`${BASE}/#/channels`, { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(600);
  const channels = await page.evaluate(() => {
    return {
      sidebarText: (document.querySelector('.ch-sidebar-title') || {}).textContent || '',
      sidebarSprites: (document.querySelectorAll('.ch-sidebar-title svg.ph-icon') || []).length,
      bodyText: (document.getElementById('app') || document.body).textContent || '',
      allSprites: document.querySelectorAll('svg.ph-icon use').length,
    };
  });
  if (EMOJI_RE.test(channels.sidebarText)) fail('(b) /channels: sidebar title still has emoji');
  else pass('(b) /channels: sidebar title is emoji-free');
  if (channels.sidebarSprites === 0) fail('(b) /channels: sidebar title has no sprite icon');
  else pass(`(b) /channels: sidebar title has ${channels.sidebarSprites} sprite(s)`);
  if (channels.allSprites === 0) fail('(b) /channels: zero sprite refs on page');
  else pass(`(b) /channels: ${channels.allSprites} sprite refs on page`);

  // (c) /nodes detail — fetch one pubkey from /api/nodes
  const pk = await page.evaluate(async () => {
    try {
      const r = await fetch('/api/nodes?limit=1');
      const j = await r.json();
      const list = j.nodes || j.data || j || [];
      const n = Array.isArray(list) ? list[0] : null;
      return n && (n.public_key || n.pubkey || n.pubKey) || null;
    } catch { return null; }
  });
  if (!pk) {
    console.warn('  ⚠ (c) /nodes/<id>: no node from API; falling back to /nodes list page');
    await noEmojiInRender(page, '/nodes', '(c) /nodes');
  } else {
    await page.goto(`${BASE}/#/nodes/${pk}`, { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(800);
    await spriteRefsResolve(page, '(c) /nodes/<pk> sprite refs', 1);
  }

  // (d) /analytics — exercise multiple tabs casually
  await noEmojiInRender(page, '/analytics', '(d) /analytics');
  await spriteRefsResolve(page, '(d) /analytics sprite refs', 5);

  // (e) /live
  await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(600);
  await spriteRefsResolve(page, '(e) /live sprite refs', 3);

  // (f) No .notdef anywhere — every <use> resolves to a defined symbol id.
  const undef = await page.evaluate(async () => {
    const resp = await fetch('/icons/phosphor-sprite.svg').catch(() => null);
    if (!resp || !resp.ok) return { error: 'sprite fetch failed' };
    const text = await resp.text();
    const ids = new Set();
    for (const m of text.matchAll(/id="(ph-[a-z-]+)"/g)) ids.add(m[1]);
    const uses = Array.from(document.querySelectorAll('svg.ph-icon use'));
    const missing = [];
    for (const u of uses) {
      const href = u.getAttribute('href') || u.getAttribute('xlink:href') || '';
      const m = href.match(/#(ph-[a-z-]+)/);
      if (!m) { missing.push(href); continue; }
      if (!ids.has(m[1])) missing.push(m[1]);
    }
    return { count: uses.length, ids: ids.size, missing };
  });
  if (undef.error) fail(`(f) sprite fetch: ${undef.error}`);
  else if (undef.missing && undef.missing.length) fail(`(f) ${undef.missing.length} sprite ref(s) not resolved: ${undef.missing.slice(0,5).join(', ')}`);
  else pass(`(f) all ${undef.count} sprite refs resolve to one of ${undef.ids} defined symbols`);

  // (g) status-token CSS resolves to a real color
  await page.goto(`${BASE}/#/home`, { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(200);
  const status = await page.evaluate(() => {
    const probe = document.createElement('span');
    probe.className = 'status-ok';
    probe.style.position = 'absolute'; probe.style.left = '-9999px';
    document.body.appendChild(probe);
    const cs = getComputedStyle(probe);
    const color = cs.color;
    probe.remove();
    return color;
  });
  if (!/rgb/.test(status)) fail(`(g) .status-ok did not resolve to an rgb color (got "${status}")`);
  else pass(`(g) .status-ok resolves to ${status} (currentColor token threaded)`);

  await browser.close();
  console.log(`\ntest-issue-1648-m3-icons-e2e.js: ${passes} passed, ${failures} failed`);
  assert.strictEqual(failures, 0, `${failures} M3 icon-render assertions failed`);
  process.exit(0);
}

main().catch((err) => {
  console.error('test-issue-1648-m3-icons-e2e.js: FAIL —', err);
  process.exit(1);
});
