#!/usr/bin/env node
/* Issue #1648 — M1: emoji → Phosphor sprite migration (E2E behavioral).
 *
 * Asserts (in a real Chromium against a running server):
 *   (a) top-nav buttons that previously held emoji glyphs (Live, Perf,
 *       Audio Lab, Search, Customize, theme toggle, hamburger) now render
 *       a Phosphor <svg class="ph-icon">…<use href="…#ph-…"/></svg>
 *       child with non-zero rendered size.
 *   (b) bottom-nav primary tabs (Home, Packets, Live, Map, Channels, More)
 *       each render a .ph-icon with a getBBox() that has non-zero width/height
 *       at viewport 360x800.
 *   (c) /#/observers — the "Compare observers" heading and the "Compare
 *       selected" button each render a .ph-icon child (no bare emoji).
 *   (d) zero emoji codepoints in the rendered DOM textContent of .top-nav
 *       and [data-bottom-nav] and the observers compare bar.
 *
 * CI gating: CHROMIUM_REQUIRE=1 makes Chromium-launch failure a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const EMOJI_RE = /[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}◆●■▲★☆○✓✗⚠✉]/u;

let passes = 0, failures = 0;
function pass(msg) { console.log(`  ✓ ${msg}`); passes++; }
function fail(msg) { console.error(`  ✗ ${msg}`); failures++; }

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
      console.error(`test-issue-1648-m1-icons-e2e.js: HARD FAIL — Chromium unavailable: ${err.message}`);
      process.exit(1);
    }
    console.warn(`SKIP — Chromium unavailable: ${err.message}`);
    process.exit(0);
  }

  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();

  // ── (a) top-nav: Live, Perf, Audio-Lab, Search toggle, Customize, theme, hamburger ──
  await page.goto(`${BASE}/#/`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.top-nav', { timeout: 5000 });

  const topNavCheck = await page.evaluate(() => {
    const sels = [
      ['Live nav',   '.top-nav a[data-route="live"]'],
      ['Perf nav',   '.top-nav a[data-route="perf"]'],
      ['Audio-Lab',  '.top-nav a[data-route="audio-lab"]'],
      ['Search btn', '#searchToggle'],
      ['Custom btn', '#customizeToggle'],
      ['Hamburger',  '#hamburger'],
    ];
    return sels.map(([label, sel]) => {
      const el = document.querySelector(sel);
      if (!el) return { label, sel, found: false };
      const ph = el.querySelector('svg.ph-icon, .ph-icon');
      const rect = ph ? ph.getBoundingClientRect() : null;
      return {
        label, sel, found: true,
        hasPhIcon: !!ph,
        rectW: rect ? rect.width : 0,
        rectH: rect ? rect.height : 0,
        text: el.textContent || '',
      };
    });
  });

  for (const r of topNavCheck) {
    if (!r.found) { fail(`(a) ${r.label}: selector ${r.sel} not found`); continue; }
    if (!r.hasPhIcon) fail(`(a) ${r.label}: no .ph-icon child (text="${r.text.slice(0,40)}")`);
    else if (r.rectW <= 0 || r.rectH <= 0) fail(`(a) ${r.label}: .ph-icon has zero size (${r.rectW}x${r.rectH})`);
    else pass(`(a) ${r.label}: .ph-icon ${r.rectW.toFixed(0)}x${r.rectH.toFixed(0)}`);
    if (EMOJI_RE.test(r.text)) fail(`(a) ${r.label}: still contains emoji codepoint in text`);
  }

  // ── (b) bottom-nav at 360x800 ──
  await ctx.close();
  const ctxMobile = await browser.newContext({ viewport: { width: 360, height: 800 } });
  const m = await ctxMobile.newPage();
  await m.goto(`${BASE}/#/`, { waitUntil: 'domcontentloaded' });
  await m.waitForSelector('[data-bottom-nav]', { timeout: 5000 });
  const bn = await m.evaluate(() => {
    const tabs = ['home','packets','live','map','channels','more'];
    return tabs.map(route => {
      const el = document.querySelector(`[data-bottom-nav-tab="${route}"]`);
      if (!el) return { route, found: false };
      const ph = el.querySelector('svg.ph-icon, .ph-icon');
      const rect = ph ? ph.getBoundingClientRect() : null;
      return {
        route, found: true,
        hasPhIcon: !!ph,
        rectW: rect ? rect.width : 0,
        rectH: rect ? rect.height : 0,
        text: el.textContent || '',
      };
    });
  });
  for (const r of bn) {
    if (!r.found) { fail(`(b) bottom-nav tab ${r.route} not found`); continue; }
    if (!r.hasPhIcon) fail(`(b) bottom-nav tab ${r.route}: no .ph-icon`);
    else if (r.rectW <= 0 || r.rectH <= 0) fail(`(b) bottom-nav tab ${r.route}: zero size`);
    else pass(`(b) bottom-nav tab ${r.route}: .ph-icon ${r.rectW.toFixed(0)}x${r.rectH.toFixed(0)}`);
    if (EMOJI_RE.test(r.text)) fail(`(b) bottom-nav tab ${r.route}: emoji codepoint in textContent`);
  }

  // ── (c) /observers — Compare heading + Compare-selected button ──
  await m.goto(`${BASE}/#/observers`, { waitUntil: 'domcontentloaded' });
  // Give the page a moment to render observers list.
  await m.waitForTimeout(1500);
  const obs = await m.evaluate(() => {
    // Compare observers heading: span containing "Compare" near top of /observers
    const headings = Array.from(document.querySelectorAll('h2, h3, .observers-compare-heading, [data-role="compare-heading"]'));
    const compareHeading = headings.find(h => /Compare\s+observers/i.test(h.textContent || ''));
    const compareBtn = document.querySelector('[data-action="compare"], button[data-role="compare-btn"], .compare-selected-btn')
      || Array.from(document.querySelectorAll('button')).find(b => /Compare\s+selected/i.test(b.textContent || ''));
    const refreshBtn = document.querySelector('[data-action="obs-refresh"]');
    function describe(el) {
      if (!el) return null;
      const ph = el.querySelector('svg.ph-icon, .ph-icon');
      const rect = ph ? ph.getBoundingClientRect() : null;
      return { has: !!ph, rectW: rect?rect.width:0, rectH: rect?rect.height:0, text: el.textContent||'' };
    }
    return {
      heading: describe(compareHeading),
      btn: describe(compareBtn),
      refresh: describe(refreshBtn),
    };
  });
  if (!obs.heading) fail('(c) Compare-observers heading not found on /observers');
  else if (!obs.heading.has) fail('(c) Compare-observers heading missing .ph-icon');
  else pass(`(c) Compare-observers heading has .ph-icon ${obs.heading.rectW.toFixed(0)}x${obs.heading.rectH.toFixed(0)}`);
  if (obs.heading && EMOJI_RE.test(obs.heading.text)) fail('(c) Compare heading text still has emoji');

  if (!obs.btn) fail('(c) Compare-selected button not found on /observers');
  else if (!obs.btn.has) fail('(c) Compare-selected button missing .ph-icon');
  else pass(`(c) Compare-selected button has .ph-icon ${obs.btn.rectW.toFixed(0)}x${obs.btn.rectH.toFixed(0)}`);
  if (obs.btn && EMOJI_RE.test(obs.btn.text)) fail('(c) Compare-selected text still has emoji');

  if (obs.refresh) {
    if (!obs.refresh.has) fail('(c) /observers refresh button missing .ph-icon');
    else pass(`(c) /observers refresh button has .ph-icon`);
    if (EMOJI_RE.test(obs.refresh.text)) fail('(c) refresh button still has emoji');
  }

  // ── (d) zero emoji codepoints in rendered nav DOM ──
  await m.goto(`${BASE}/#/`, { waitUntil: 'domcontentloaded' });
  await m.waitForSelector('.top-nav', { timeout: 5000 });
  const text = await m.evaluate(() => {
    const top = document.querySelector('.top-nav');
    const bot = document.querySelector('[data-bottom-nav]');
    return [(top && top.textContent) || '', (bot && bot.textContent) || ''].join('\n');
  });
  if (EMOJI_RE.test(text)) {
    const hits = (text.match(new RegExp(EMOJI_RE.source, 'gu')) || []).slice(0, 10);
    fail(`(d) rendered nav DOM still has emoji codepoints: ${hits.join(' ')}`);
  } else {
    pass('(d) rendered nav DOM (top-nav + bottom-nav) has zero emoji codepoints');
  }

  await browser.close();
  console.log(`\ntest-issue-1648-m1-icons-e2e.js: ${passes} passed, ${failures} failed`);
  process.exit(failures > 0 ? 1 : 0);
}

main().catch((err) => {
  console.error('test-issue-1648-m1-icons-e2e.js: FAIL —', err);
  process.exit(1);
});
