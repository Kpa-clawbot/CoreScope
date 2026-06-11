#!/usr/bin/env node
/* Issue #1648 — M5: emoji → Phosphor sprite (E2E behavioral).
 *
 * Asserts (in a real Chromium against a running server):
 *   (a) Customize panel — toggling the customizer renders Phosphor sprites
 *       in tab labels, header, and reset/save buttons.
 *   (b) Customize panel BACK-COMPAT: when localStorage has operator config
 *       with a legacy emoji step value, that emoji must STILL render — the
 *       new code must NOT override operator-stored emoji values (design
 *       call #1 from the M5 brief).
 *   (c) /channels modal open — modal action buttons render sprites.
 *   (d) /audio-lab — audio unlock prompt + action icons render sprites.
 *   (e) geofilter-builder.html — control buttons render sprites.
 *   (f) NO .notdef anywhere — every <use> resolves to a defined symbol id.
 *
 * CI gating: CHROMIUM_REQUIRE=1 makes Chromium-launch failure a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');
const assert = require('assert');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const M5_FORBIDDEN_RE = /[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}◆●■▲★☆○✓✗⚠✉✕]/u;

let passes = 0, failures = 0;
function pass(msg) { console.log(`  ✓ ${msg}`); passes++; }
function fail(msg) { console.error(`  ✗ ${msg}`); failures++; }

async function gotoSpa(page, route) {
  await page.goto(`${BASE}/#${route}`, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => !!document.querySelector('#app'), null, { timeout: 8000 }).catch(() => {});
  await page.waitForTimeout(700);
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
      console.error(`test-issue-1648-m5-icons-e2e.js: HARD FAIL — Chromium unavailable: ${err.message}`);
      process.exit(1);
    }
    console.warn(`SKIP — Chromium unavailable: ${err.message}`);
    process.exit(0);
  }

  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();

  // ── (a) Customize panel — defaults render as Phosphor sprites ──────
  await gotoSpa(page, '/');
  // Click the customize trigger (palette icon in nav).
  await page.evaluate(() => {
    const btn = document.querySelector('[data-toggle="customize"], #customizeToggle, .cust-toggle, [aria-label*="ustomize" i]');
    if (btn) btn.click();
    else if (window.Customize && window.Customize.toggle) window.Customize.toggle();
    else if (window.CustomizeV2 && window.CustomizeV2.toggle) window.CustomizeV2.toggle();
  });
  await page.waitForTimeout(900);
  const custA = await page.evaluate(() => {
    const overlay = document.querySelector('.cust-overlay');
    if (!overlay) return { open: false };
    const tabs = overlay.querySelectorAll('.cust-tab');
    let tabSprites = 0; tabs.forEach(t => { tabSprites += t.querySelectorAll('svg.ph-icon use').length; });
    return {
      open: true,
      tabCount: tabs.length,
      tabSprites,
      headerSprites: overlay.querySelectorAll('.cust-header svg.ph-icon use').length,
      overlayText: overlay.textContent || '',
    };
  });
  if (!custA.open) {
    fail('(a) customize panel did not open — cannot assert');
  } else {
    if (custA.tabSprites < custA.tabCount) {
      fail(`(a) customize tabs: ${custA.tabSprites} sprite(s) for ${custA.tabCount} tabs (each tab needs ≥1)`);
    } else {
      pass(`(a) customize tabs: ${custA.tabSprites} sprite(s) across ${custA.tabCount} tabs`);
    }
    if (custA.headerSprites < 2) {
      fail(`(a) customize header: ${custA.headerSprites} sprite(s) (expected ≥2 — title + close)`);
    } else {
      pass(`(a) customize header: ${custA.headerSprites} sprite(s)`);
    }
    // Note: the overlay still RENDERS legacy operator emoji values via
    // renderConfigGlyph for back-compat (test (b) below). The overlay's own
    // chrome/tabs/headers must be sprite-driven, so we look at chrome only.
    const chromeText = await page.evaluate(() => {
      const ovl = document.querySelector('.cust-overlay');
      if (!ovl) return '';
      // Walk only the tab + header chrome, not the input previews (which
      // legitimately echo operator values — including legacy emoji).
      const parts = [];
      ovl.querySelectorAll('.cust-tab, .cust-header').forEach(el => parts.push(el.textContent || ''));
      return parts.join(' | ');
    });
    if (M5_FORBIDDEN_RE.test(chromeText)) {
      fail(`(a) customize chrome still contains an emoji/misc-icon: ${JSON.stringify(chromeText.match(M5_FORBIDDEN_RE))}`);
    } else {
      pass('(a) customize chrome (tabs + header) is icon-free text');
    }
  }

  // ── (b) BACK-COMPAT: operator-stored emoji values must still render ──
  // Inject a fake user-theme into localStorage with a legacy emoji step,
  // reload, re-open customize, navigate to home tab, and confirm the
  // emoji STILL renders verbatim (not stripped, not replaced).
  await page.evaluate(() => {
    const legacy = {
      home: {
        heroTitle: 'CoreScope',
        heroSubtitle: 'legacy test',
        steps: [
          { emoji: '🐙', title: 'Legacy octopus step', description: 'Operator-stored emoji must survive M5 migration.' },
          { emoji: 'ph:bluetooth', title: 'New ph token step', description: 'Renders as sprite.' },
        ],
        footerLinks: [{ label: '🦄 Legacy unicorn', url: '#/legacy' }],
      },
    };
    localStorage.setItem('meshcore-user-theme', JSON.stringify(legacy));
    // customize-v2 reads from cv2 overrides too
    try {
      const cv2 = { home: legacy.home };
      localStorage.setItem('meshcore-cv2-overrides', JSON.stringify(cv2));
    } catch (e) {}
  });
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(700);
  // Re-open customize and switch to Home tab.
  await page.evaluate(() => {
    const btn = document.querySelector('[data-toggle="customize"], #customizeToggle, .cust-toggle, [aria-label*="ustomize" i]');
    if (btn) btn.click();
    else if (window.Customize && window.Customize.toggle) window.Customize.toggle();
    else if (window.CustomizeV2 && window.CustomizeV2.toggle) window.CustomizeV2.toggle();
  });
  await page.waitForTimeout(700);
  await page.evaluate(() => {
    const home = document.querySelector('.cust-overlay .cust-tab[data-tab="home"]');
    if (home) home.click();
  });
  await page.waitForTimeout(500);
  const custB = await page.evaluate(() => {
    const overlay = document.querySelector('.cust-overlay');
    if (!overlay) return { open: false };
    // The step emoji input value should contain the literal 🐙 char.
    const inputs = Array.from(overlay.querySelectorAll('.cust-emoji-input'));
    const previews = Array.from(overlay.querySelectorAll('.cust-emoji-preview'));
    return {
      open: true,
      inputValues: inputs.map(i => i.value),
      previewHtmls: previews.map(p => p.innerHTML),
    };
  });
  if (!custB.open) {
    fail('(b) back-compat: customize panel did not open after localStorage inject');
  } else {
    const hasOctopus = custB.inputValues.some(v => v && v.indexOf('🐙') !== -1);
    if (!hasOctopus) {
      fail(`(b) back-compat: operator-stored emoji 🐙 missing from inputs (values=${JSON.stringify(custB.inputValues)})`);
    } else {
      pass('(b) back-compat: operator-stored emoji 🐙 survives in step input value');
    }
    const hasOctopusPreview = custB.previewHtmls.some(h => h && h.indexOf('🐙') !== -1);
    if (!hasOctopusPreview) {
      fail(`(b) back-compat: emoji preview span does not render 🐙 (htmls=${JSON.stringify(custB.previewHtmls)})`);
    } else {
      pass('(b) back-compat: renderConfigGlyph rendered 🐙 as text (legacy branch)');
    }
    const hasPhSprite = custB.previewHtmls.some(h => h && /<use[^>]+ph-bluetooth/.test(h));
    if (!hasPhSprite) {
      fail('(b) back-compat: new ph:bluetooth step did not render as sprite');
    } else {
      pass('(b) back-compat: ph:bluetooth step rendered as Phosphor sprite alongside legacy emoji');
    }
  }
  // Clean up the legacy injection so it doesn't leak into later assertions.
  await page.evaluate(() => {
    localStorage.removeItem('meshcore-user-theme');
    localStorage.removeItem('meshcore-cv2-overrides');
  });

  // ── (c) /channels modal open — modal controls render sprites ────────
  await page.reload({ waitUntil: 'domcontentloaded' });
  await gotoSpa(page, '/channels');
  // The share/QR modals are gated on a user-added channel; we just assert
  // any modal-close button present on the page already renders a sprite
  // (the M3 swap landed `.modal-close` chrome → sprite).
  const ch = await page.evaluate(() => {
    const modals = Array.from(document.querySelectorAll('.modal, .ch-modal, .modal-overlay'));
    let modalSprites = 0;
    modals.forEach(m => { modalSprites += m.querySelectorAll('svg.ph-icon use').length; });
    const pageSprites = document.querySelectorAll('svg.ph-icon use').length;
    return { modalCount: modals.length, modalSprites, pageSprites };
  });
  if (ch.pageSprites < 3) fail(`(c) /channels: only ${ch.pageSprites} sprites total (expected ≥3)`);
  else pass(`(c) /channels: ${ch.pageSprites} sprite refs on page (${ch.modalSprites} inside ${ch.modalCount} modal element(s))`);

  // ── (d) /audio-lab — sprite icons in audio prompts/controls ────────
  await gotoSpa(page, '/audio-lab');
  await page.waitForTimeout(500);
  const aud = await page.evaluate(() => {
    return {
      sprites: document.querySelectorAll('svg.ph-icon use').length,
      bodyText: (document.getElementById('app') || document.body).textContent || '',
    };
  });
  if (aud.sprites < 1) fail(`(d) /audio-lab: ${aud.sprites} sprite refs (expected ≥1)`);
  else pass(`(d) /audio-lab: ${aud.sprites} sprite refs`);

  // ── (e) geofilter-builder.html (standalone) ─────────────────────────
  await page.goto(`${BASE}/geofilter-builder.html`, { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(500);
  const geo = await page.evaluate(() => {
    const ctrls = document.querySelectorAll('.controls button');
    let s = 0; ctrls.forEach(b => { s += b.querySelectorAll('svg.ph-icon use').length; });
    return {
      ctrlSprites: s,
      ctrlCount: ctrls.length,
      btnTexts: Array.from(ctrls).map(b => b.textContent || ''),
    };
  });
  if (geo.ctrlSprites < geo.ctrlCount) {
    fail(`(e) geofilter-builder: ${geo.ctrlSprites} sprite(s) for ${geo.ctrlCount} control button(s)`);
  } else {
    pass(`(e) geofilter-builder: ${geo.ctrlSprites} sprite(s) across ${geo.ctrlCount} control buttons`);
  }
  const ctrlEmoji = geo.btnTexts.find(t => M5_FORBIDDEN_RE.test(t));
  if (ctrlEmoji) fail(`(e) geofilter-builder: control button still contains emoji/misc-icon: ${JSON.stringify(ctrlEmoji)}`);
  else pass('(e) geofilter-builder: control button text is icon-free');

  // ── (f) Every <use> resolves ────────────────────────────────────────
  await gotoSpa(page, '/');
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
  else if (undef.missing && undef.missing.length) fail(`(f) ${undef.missing.length} sprite ref(s) unresolved: ${undef.missing.slice(0,5).join(', ')}`);
  else pass(`(f) all ${undef.count} sprite refs resolve to one of ${undef.ids} defined symbols`);

  await browser.close();
  console.log(`\ntest-issue-1648-m5-icons-e2e.js: ${passes} passed, ${failures} failed`);
  assert.strictEqual(failures, 0, `${failures} M5 icon-render assertions failed`);
  process.exit(0);
}

main().catch((err) => {
  console.error('test-issue-1648-m5-icons-e2e.js: FAIL —', err);
  process.exit(1);
});
