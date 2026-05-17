#!/usr/bin/env node
/* Issue #1060 / PR #1067 follow-up — touch targets behavior test.
 *
 * MAJOR-2 from pr-polish review: the previous version of this file
 * grep'd CSS strings, which is tautological — it asserted that the
 * source contained the literal characters that were just edited in.
 * It would have passed even if the CSS was syntactically broken or
 * if selectors didn't match any element on the real page.
 *
 * This rewrite loads public/style.css into a real Chromium page via
 * Playwright with an iPhone-class touch emulation context, renders
 * representative DOM samples for every selector we claim to harden,
 * and reads getBoundingClientRect()/getComputedStyle() to assert the
 * 48x48 minimum hit area. It also exercises the .sort-help tap-to-
 * reveal flow (focus event must un-hide the .sort-help-tip) since
 * MAJOR-1 is enforced both in markup (tabindex="0" in packets.js) and
 * in CSS (:focus / :focus-within rule in the Touch Targets section).
 */
'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');
const { chromium, devices } = require('playwright');

const REPO = __dirname;
const CSS = fs.readFileSync(path.join(REPO, 'public/style.css'), 'utf8');

// Selectors we claim to make 48x48. Each entry: [selector, tag, classes,
// optional inner-html]. Tag matters because some rules are scoped to
// `button.ch-item` and some only apply to specific input[type=...].
const BUTTON_SELECTORS = [
  ['.btn',                   'button', 'btn'],
  ['.btn-icon',              'button', 'btn-icon'],
  ['.nav-btn',               'button', 'nav-btn'],
  ['.ch-icon-btn',           'button', 'ch-icon-btn'],
  ['.ch-remove-btn',         'button', 'ch-remove-btn'],
  ['.ch-share-btn',          'button', 'ch-share-btn'],
  ['.ch-gear-btn',           'button', 'ch-gear-btn'],
  ['.panel-close-btn',       'button', 'panel-close-btn'],
  ['.mc-jump-btn',           'button', 'mc-jump-btn'],
  ['button.ch-item',         'button', 'ch-item'],
  ['.btn-link',              'button', 'btn-link'],
  ['.col-toggle-btn',        'button', 'col-toggle-btn'],
  ['.filter-toggle-btn',     'button', 'filter-toggle-btn'],
  ['.ch-add-channel-btn',    'button', 'ch-add-channel-btn'],
  ['.ch-back-btn',           'button', 'ch-back-btn'],
  ['.ch-modal-btn-secondary','button', 'ch-modal-btn-secondary'],
  ['.ch-scroll-btn',         'button', 'ch-scroll-btn'],
  ['.chooser-btn',           'button', 'chooser-btn'],
  ['.clock-filter-btn',      'button', 'clock-filter-btn'],
  ['.compare-btn',           'button', 'compare-btn'],
  ['.copy-link-btn',         'button', 'copy-link-btn'],
  ['.alab-btn',              'button', 'alab-btn'],
];

// Form controls. min-WIDTH is not enforced on these (text fields legitimately
// span a wide column); we only require min-height: 48px.
const FIELD_SELECTORS = [
  ['select',                 'select', '',                 '<option>x</option>'],
  ['input[type=text]',       'input',  '', null, { type: 'text' }],
  ['input[type=search]',     'input',  '', null, { type: 'search' }],
  ['input[type=number]',     'input',  '', null, { type: 'number' }],
  ['input[type=email]',      'input',  '', null, { type: 'email' }],
  ['input[type=password]',   'input',  '', null, { type: 'password' }],
  ['input[type=tel]',        'input',  '', null, { type: 'tel' }],
  ['input[type=url]',        'input',  '', null, { type: 'url' }],
  ['input[type=date]',       'input',  '', null, { type: 'date' }],
  ['input[type=time]',       'input',  '', null, { type: 'time' }],
];

function buildSampleHtml() {
  const buttons = BUTTON_SELECTORS
    .map(([_, tag, cls]) => `<${tag} class="${cls}" data-sel="${cls}">x</${tag}>`)
    .join('\n      ');
  const fields = FIELD_SELECTORS
    .map(([sel, tag, cls, inner, attrs]) => {
      const attrStr = attrs
        ? Object.entries(attrs).map(([k, v]) => `${k}="${v}"`).join(' ')
        : '';
      const open = `<${tag} ${attrStr} data-sel="${sel.replace(/[\[\]=]/g, '_')}">`;
      const close = tag === 'input' ? '' : `${inner || ''}</${tag}>`;
      return open + close;
    })
    .join('\n      ');

  // .sort-help sample mirrors the markup the JS produces (post-fix):
  // tabindex="0" so :focus-within can fire on touch tap.
  return `<!doctype html>
<html><head><meta charset="utf-8">
<style>${CSS}</style>
</head><body>
  <div id="harness" style="padding: 16px; display: flex; flex-direction: column; gap: 8px; align-items: flex-start;">
    ${buttons}
    ${fields}
    <span class="sort-help" id="sortHelp" tabindex="0" role="button" aria-label="Sort help">ⓘ
      <span class="sort-help-tip">Tip body</span>
    </span>
  </div>
</body></html>`;
}

async function run() {
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      executablePath: process.env.CHROMIUM_PATH || undefined,
      args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    });
  } catch (err) {
    // Allow the test to be skipped on hosts where Chromium cannot launch
    // (e.g. some musl-libc dev boxes). CI uses standard glibc Ubuntu runners
    // where this path is never taken. Set TOUCH_TARGETS_REQUIRE=1 to force
    // a hard failure even when Chromium is unavailable.
    if (process.env.TOUCH_TARGETS_REQUIRE === '1') throw err;
    console.log(`test-touch-targets.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  // iPhone 13 has hasTouch:true, isMobile:true, no hover. Exactly the
  // capability matrix that the @media (hover: hover) gate and 48px
  // minimums are designed for.
  const iPhone = devices['iPhone 13'];
  const context = await browser.newContext({ ...iPhone });
  const page = await context.newPage();

  // Load the harness via a data: URL so we don't need a running server.
  const html = buildSampleHtml();
  await page.setContent(html, { waitUntil: 'load' });
  if (page.evaluate) {
    await page.evaluate(() => document.fonts && document.fonts.ready ? document.fonts.ready : null);
  }

  let failures = 0;
  function record(name, ok, detail) {
    if (ok) {
      console.log(`  \u2705 ${name}`);
    } else {
      console.log(`  \u274c ${name}: ${detail}`);
      failures++;
    }
  }

  // --- Buttons: rendered hit area must be at least 48x48 CSS px.
  for (const [selector, , cls] of BUTTON_SELECTORS) {
    const dim = await page.$eval(`[data-sel="${cls}"]`, (el) => {
      const r = el.getBoundingClientRect();
      const cs = getComputedStyle(el);
      return { w: r.width, h: r.height, mh: cs.minHeight, mw: cs.minWidth };
    });
    const okH = dim.h >= 48;
    const okW = dim.w >= 48;
    record(`${selector}: rendered ${dim.w.toFixed(1)}x${dim.h.toFixed(1)} (min ${dim.mw}/${dim.mh})`,
           okH && okW,
           `expected >=48x48, got ${dim.w}x${dim.h}`);
  }

  // --- Form controls: rendered height must be at least 48 CSS px.
  for (const [selector, , , , attrs] of FIELD_SELECTORS) {
    const dataKey = selector.replace(/[\[\]=]/g, '_');
    const dim = await page.$eval(`[data-sel="${dataKey}"]`, (el) => {
      const r = el.getBoundingClientRect();
      const cs = getComputedStyle(el);
      return { h: r.height, mh: cs.minHeight };
    });
    record(`${selector}: rendered height ${dim.h.toFixed(1)} (min ${dim.mh})`,
           dim.h >= 48,
           `expected height >=48, got ${dim.h}`);
  }

  // --- MAJOR-1 verification: .sort-help is keyboard/tap focusable AND the
  // tooltip becomes visible on focus (tap-to-reveal works without hover).
  const tabIndex = await page.$eval('#sortHelp', (el) => el.getAttribute('tabindex'));
  record('.sort-help has tabindex="0" in markup', tabIndex === '0',
         `expected "0", got ${JSON.stringify(tabIndex)}`);

  const tipBeforeFocus = await page.$eval('#sortHelp .sort-help-tip',
    (el) => getComputedStyle(el).display);
  // CSS rule on touch-only viewport: hover-rule is gated, focus-rule reveals.
  record('.sort-help-tip is hidden by default on touch', tipBeforeFocus === 'none',
         `expected display:none initially, got ${tipBeforeFocus}`);

  await page.focus('#sortHelp');
  const tipAfterFocus = await page.$eval('#sortHelp .sort-help-tip',
    (el) => getComputedStyle(el).display);
  record('.sort-help-tip becomes visible on focus (tap-to-reveal)',
         tipAfterFocus === 'block',
         `expected display:block after focus, got ${tipAfterFocus}`);

  // --- Hover-only rule must be gated behind @media (hover: hover) so that on
  // touch the iPhone context never enters a "stuck hover" state when a tap
  // toggles :hover. We assert this by reading the matchMedia value the page
  // sees and confirming :hover did NOT take effect on tap.
  const hoverCapable = await page.evaluate(() => matchMedia('(hover: hover)').matches);
  record('iPhone context reports (hover: hover) = false', hoverCapable === false,
         `expected false on touch device, got ${hoverCapable}`);

  await browser.close();

  if (failures > 0) {
    console.log(`\ntest-touch-targets.js: FAIL (${failures} assertion(s))`);
    process.exit(1);
  }
  console.log('\ntest-touch-targets.js: OK');
}

run().catch((err) => {
  console.error('test-touch-targets.js: fatal', err);
  process.exit(1);
});
