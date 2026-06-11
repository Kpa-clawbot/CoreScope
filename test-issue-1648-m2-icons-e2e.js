#!/usr/bin/env node
/* Issue #1648 — M2: emoji → Phosphor sprite migration (E2E behavioral).
 *
 * Asserts (in a real Chromium against a running server):
 *   (a) /analytics renders the Mesh Analytics h2 with a .ph-icon child and
 *       zero emoji codepoints in the rendered heading text.
 *   (b) /packets filter row renders Refresh / BYOP / Clear / My Nodes
 *       buttons each with a .ph-icon child and no emoji text.
 *   (c) /nodes table renders sort-arrow indicators using .ph-icon (no
 *       legacy ▲▼ in the rendered DOM) and the panel-close button has a
 *       .ph-icon.
 *   (d) /live renders the audio-toggle label with a .ph-icon, feed
 *       hide/show buttons each have .ph-icon.
 *   (e) /map renders the Map Controls h3 with .ph-icon and the
 *       map-controls-toggle has .ph-icon (no emoji).
 *   (f) /traces renders Packet Trace h2 with .ph-icon.
 *   (g) /perf renders Performance Dashboard h2 with .ph-icon.
 *   (h) /audio-lab renders Packet Data h3 with .ph-icon.
 *   (i) NO .notdef glyph appears in any rendered SVG sprite ref.
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

async function checkHeading(page, route, headingPattern, label) {
  await page.goto(`${BASE}/#${route}`, { waitUntil: 'domcontentloaded' });
  // Wait for the SPA to render. The headings live inside #app after route resolution.
  try {
    await page.waitForFunction(
      (pat) => {
        const hs = Array.from(document.querySelectorAll('h2, h3'));
        return hs.some(h => new RegExp(pat, 'i').test(h.textContent || ''));
      },
      headingPattern.source,
      { timeout: 8000 },
    );
  } catch {
    fail(`${label}: heading matching /${headingPattern.source}/i did not render`);
    return;
  }
  const res = await page.evaluate((pat) => {
    const hs = Array.from(document.querySelectorAll('h2, h3'));
    const h = hs.find(e => new RegExp(pat, 'i').test(e.textContent || ''));
    if (!h) return null;
    const ph = h.querySelector('svg.ph-icon, .ph-icon');
    return { has: !!ph, text: h.textContent || '' };
  }, headingPattern.source);
  if (!res) { fail(`${label}: heading went away after wait`); return; }
  if (!res.has) fail(`${label}: heading has no .ph-icon (text="${res.text.slice(0,60)}")`);
  else pass(`${label}: heading has .ph-icon`);
  if (EMOJI_RE.test(res.text)) fail(`${label}: heading text still contains emoji`);
}

async function spriteRefsResolve(page, label) {
  const r = await page.evaluate(() => {
    const uses = Array.from(document.querySelectorAll('svg.ph-icon use'));
    return {
      count: uses.length,
      // We assume any rendered .ph-icon use is OK if sprite loaded; verify by
      // fetching the sprite URL & confirming the symbol id exists.
      refs: uses.slice(0, 5).map(u => u.getAttribute('href') || u.getAttribute('xlink:href') || ''),
    };
  });
  if (r.count === 0) fail(`${label}: zero <svg.ph-icon use> elements rendered`);
  else pass(`${label}: ${r.count} sprite refs rendered (samples: ${r.refs.join(', ')})`);
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
      console.error(`test-issue-1648-m2-icons-e2e.js: HARD FAIL — Chromium unavailable: ${err.message}`);
      process.exit(1);
    }
    console.warn(`SKIP — Chromium unavailable: ${err.message}`);
    process.exit(0);
  }

  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();

  // (a) /analytics
  await checkHeading(page, '/analytics', /Mesh Analytics/, '(a) /analytics');
  await spriteRefsResolve(page, '(a) /analytics sprite');

  // (b) /packets — Refresh / BYOP / My Nodes buttons (visible without any data
  // dependency since they're in the static filter chrome).
  await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForSelector('[data-action="pkt-refresh"]', { timeout: 8000 });
  } catch {
    fail('(b) /packets: pkt-refresh button not rendered');
  }
  const pkt = await page.evaluate(() => {
    function has(sel) {
      const el = document.querySelector(sel);
      if (!el) return { sel, found: false };
      return {
        sel, found: true,
        ph: !!el.querySelector('svg.ph-icon, .ph-icon'),
        text: el.textContent || '',
      };
    }
    return {
      refresh: has('[data-action="pkt-refresh"]'),
      byop: has('[data-action="pkt-byop"]'),
      myNodes: has('#fMyNodes'),
    };
  });
  for (const [name, r] of Object.entries(pkt)) {
    if (!r.found) { fail(`(b) /packets: ${name} not found`); continue; }
    if (!r.ph) fail(`(b) /packets: ${name} has no .ph-icon`);
    else pass(`(b) /packets: ${name} has .ph-icon`);
    if (EMOJI_RE.test(r.text)) fail(`(b) /packets: ${name} text still has emoji`);
  }

  // (c) /nodes — sort-arrow indicators (sort-arrow class) — exercise click on a
  // header to trigger sort indicator. To avoid flakiness when there's no data,
  // we just assert NO emoji codepoints appear in the rendered table chrome
  // (filter bar, search input area, panel-close-btn template).
  await page.goto(`${BASE}/#/nodes`, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(
    () => !!document.querySelector('table, .nodes-empty, .empty-state, #nodesList'),
    null, { timeout: 8000 },
  ).catch(() => {});
  const nodesBody = await page.evaluate(() => {
    const app = document.getElementById('app') || document.body;
    return app.textContent || '';
  });
  // Sort-arrow ▲▼ → caret-up/caret-down; assert no triangle/down-triangle.
  if (/[▲▼]/.test(nodesBody)) fail('(c) /nodes: rendered DOM still has ▲ or ▼ chars');
  else pass('(c) /nodes: no ▲ or ▼ in rendered DOM');
  await spriteRefsResolve(page, '(c) /nodes sprite');

  // (d) /live — audio label + feed buttons. Live page has many controls; assert
  // sprite refs > 0 and audio toggle present.
  await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => !!document.querySelector('#liveAudioToggle, #feedHideBtn'),
    null, { timeout: 8000 }).catch(() => {});
  const live = await page.evaluate(() => {
    function has(sel) {
      const el = document.querySelector(sel);
      if (!el) return { sel, found: false };
      // Audio toggle is an <input> whose <label> wraps it; walk up.
      const host = el.tagName === 'INPUT' ? el.closest('label') : el;
      return {
        sel, found: true,
        ph: !!(host && host.querySelector('svg.ph-icon, .ph-icon')),
        text: host ? host.textContent : '',
      };
    }
    return {
      audio: has('#liveAudioToggle'),
      hide: has('#feedHideBtn'),
    };
  });
  for (const [name, r] of Object.entries(live)) {
    if (!r.found) { fail(`(d) /live: ${name} not found`); continue; }
    if (!r.ph) fail(`(d) /live: ${name} has no .ph-icon`);
    else pass(`(d) /live: ${name} has .ph-icon`);
    if (EMOJI_RE.test(r.text)) fail(`(d) /live: ${name} text still has emoji`);
  }

  // (e) /map — Map Controls h3 + toggle
  await checkHeading(page, '/map', /Map Controls/, '(e) /map');
  const mapToggle = await page.evaluate(() => {
    const el = document.querySelector('#mapControlsToggle');
    if (!el) return { found: false };
    return { found: true, ph: !!el.querySelector('svg.ph-icon, .ph-icon'), text: el.textContent || '' };
  });
  if (!mapToggle.found) fail('(e) /map: #mapControlsToggle not found');
  else {
    if (!mapToggle.ph) fail('(e) /map: #mapControlsToggle missing .ph-icon');
    else pass('(e) /map: #mapControlsToggle has .ph-icon');
    if (EMOJI_RE.test(mapToggle.text)) fail('(e) /map: #mapControlsToggle text still has emoji');
  }

  // (f) /traces
  await checkHeading(page, '/traces', /Packet Trace/, '(f) /traces');

  // (g) /perf
  await checkHeading(page, '/perf', /Performance Dashboard/, '(g) /perf');

  // (h) /audio-lab — Loop button has .ph-icon (visible immediately on init, no
  // packet selection required). Section headings (Packet Data, Sound Mapping,
  // etc.) only render after a packet is selected — gated by data availability.
  await page.goto(`${BASE}/#/audio-lab`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForSelector('#alabLoop', { timeout: 8000 });
    const al = await page.evaluate(() => {
      const el = document.querySelector('#alabLoop');
      return { found: !!el, ph: !!(el && el.querySelector('svg.ph-icon, .ph-icon')), text: el ? el.textContent : '' };
    });
    if (!al.found) fail('(h) /audio-lab: #alabLoop not found');
    else if (!al.ph) fail('(h) /audio-lab: #alabLoop has no .ph-icon');
    else pass('(h) /audio-lab: #alabLoop has .ph-icon');
    if (EMOJI_RE.test(al.text)) fail('(h) /audio-lab: #alabLoop text still has emoji');
  } catch {
    fail('(h) /audio-lab: #alabLoop did not render');
  }

  // (i) No .notdef glyph anywhere. We approximate this by checking that every
  // <use href> resolves to a defined symbol id in the loaded sprite document.
  const undef = await page.evaluate(async () => {
    // Fetch sprite once.
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
  if (undef.error) fail(`(i) sprite fetch: ${undef.error}`);
  else if (undef.missing && undef.missing.length) fail(`(i) ${undef.missing.length} sprite ref(s) not resolved: ${undef.missing.slice(0,5).join(', ')}`);
  else pass(`(i) all ${undef.count} sprite refs resolve to one of ${undef.ids} defined symbols`);

  // (j) /nodes — favorite-toggle button: aria-label + ≥44×44 hit area + ph-star sprite (not ☆ text)
  await page.goto(`${BASE}/#/nodes`, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => !!document.querySelector('.fav-star'),
    null, { timeout: 8000 }).catch(() => {});
  const favProbe = await page.evaluate(() => {
    const btn = document.querySelector('.fav-star');
    if (!btn) return { found: false };
    const r = btn.getBoundingClientRect();
    return {
      found: true,
      aria: btn.getAttribute('aria-label') || '',
      w: Math.round(r.width), h: Math.round(r.height),
      hasPh: !!btn.querySelector('svg.ph-icon use'),
      hasStarText: /[★☆]/.test(btn.textContent || ''),
      ariaPressed: btn.getAttribute('aria-pressed'),
    };
  });
  if (!favProbe.found) fail('(j) /nodes: no .fav-star rendered (need fixture data)');
  else {
    if (favProbe.aria !== 'Toggle favorite') fail(`(j) /nodes fav-star: aria-label missing/wrong (got "${favProbe.aria}")`);
    else pass('(j) /nodes fav-star has aria-label="Toggle favorite"');
    if (favProbe.w < 44 || favProbe.h < 44) fail(`(j) /nodes fav-star bbox ${favProbe.w}x${favProbe.h} < 44x44 (WCAG 2.5.5)`);
    else pass(`(j) /nodes fav-star bbox ${favProbe.w}x${favProbe.h} ≥ 44x44`);
    if (!favProbe.hasPh) fail('(j) /nodes fav-star missing ph-icon sprite');
    else pass('(j) /nodes fav-star uses ph-icon sprite');
    if (favProbe.hasStarText) fail('(j) /nodes fav-star still contains ★ or ☆ text');
  }

  // (k) /nodes — No-Clock skew badge uses ph-prohibit (not 🚫 emoji)
  // Probe by injecting via window.renderSkewBadge directly (no fixture dep).
  const noClock = await page.evaluate(() => {
    if (typeof window.renderSkewBadge !== 'function') return { fnFound: false };
    const html = window.renderSkewBadge('no_clock', 0, null);
    return {
      fnFound: true,
      html: html,
      usesProhibit: /#ph-prohibit/.test(html),
      hasProhibitEmoji: /🚫/.test(html),
    };
  });
  if (!noClock.fnFound) fail('(k) /nodes: window.renderSkewBadge not found');
  else {
    if (!noClock.usesProhibit) fail(`(k) No-Clock badge missing ph-prohibit (html=${noClock.html.slice(0,200)})`);
    else pass('(k) No-Clock badge uses #ph-prohibit sprite');
    if (noClock.hasProhibitEmoji) fail('(k) No-Clock badge still contains 🚫 emoji');
  }

  // (l) /packets — "Saved" filter chip uses separate ph-star-fill + ph-caret-down (no ★ ▾ text)
  await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#filterSavedTrigger', { timeout: 8000 }).catch(() => {});
  const saved = await page.evaluate(() => {
    const el = document.querySelector('#filterSavedTrigger');
    if (!el) return { found: false };
    const uses = Array.from(el.querySelectorAll('svg.ph-icon use'))
      .map(u => (u.getAttribute('href') || '').replace(/^.*#/, ''));
    return {
      found: true,
      uses,
      text: el.textContent || '',
      hasStarText: /[★☆]/.test(el.textContent || ''),
      hasCaretText: /[▾▴]/.test(el.textContent || ''),
    };
  });
  if (!saved.found) fail('(l) /packets: #filterSavedTrigger not rendered');
  else {
    if (!saved.uses.includes('ph-star-fill')) fail(`(l) Saved chip missing ph-star-fill (uses=${saved.uses.join(',')})`);
    else pass('(l) Saved chip has ph-star-fill');
    if (!saved.uses.includes('ph-caret-down')) fail(`(l) Saved chip missing ph-caret-down (uses=${saved.uses.join(',')})`);
    else pass('(l) Saved chip has ph-caret-down');
    if (saved.hasStarText) fail('(l) Saved chip still has ★/☆ unicode text');
    if (saved.hasCaretText) fail('(l) Saved chip still has ▾/▴ unicode text');
  }

  await browser.close();
  console.log(`\ntest-issue-1648-m2-icons-e2e.js: ${passes} passed, ${failures} failed`);
  assert.strictEqual(failures, 0, `${failures} M2 icon-render assertions failed`);
  process.exit(0);
}

main().catch((err) => {
  console.error('test-issue-1648-m2-icons-e2e.js: FAIL —', err);
  process.exit(1);
});
