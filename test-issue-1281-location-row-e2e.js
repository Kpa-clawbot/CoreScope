/**
 * #1281 — Packet detail Location row + 📍map link contrast.
 *
 * Bug:
 *   A) <dt>Location</dt><dd>—</dd> renders unconditionally on every packet,
 *      wasting a row on ~90% of packet types (only ADVERT carries unencrypted
 *      transmitter GPS).
 *   B) The trailing `📍map` link has no class/color → inherits UA-default <a>
 *      blue → unreadable in dark mode.
 *
 * Asserts:
 *   1. Some non-ADVERT packet detail does NOT contain <dt>Location</dt>.
 *   2. Some ADVERT packet detail DOES contain <dt>Location</dt> with coords.
 *   3. The 📍map link uses class="loc-map-link" with color = --accent
 *      (NOT the default UA blue rgb(0,0,238)).
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1281-location-row-e2e.js
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

function normRgb(s) {
  const m = s && s.match(/rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)/);
  if (!m) return null;
  return `rgb(${m[1]}, ${m[2]}, ${m[3]})`;
}

async function gotoPackets(page) {
  await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
  await page.evaluate(() => {
    localStorage.removeItem('meshcore-groupbyhash');
    localStorage.setItem('meshcore-time-window', '525600');
  });
  await page.reload({ waitUntil: 'load' });
  await page.waitForSelector('table tbody tr[data-hash]', { timeout: 15000 });
}

// Click rows until detail pane's Payload Type matches `wantType` (e.g. "Advert"
// or any non-"Advert"). Returns true on hit, false if exhausted.
async function findPacketDetailByType(page, predicate, maxRows = 40) {
  await page.waitForTimeout(400);
  const rows = await page.$$('table tbody tr[data-hash][data-action]');
  for (let i = 0; i < Math.min(rows.length, maxRows); i++) {
    await rows[i].click({ timeout: 3000 }).catch(() => null);
    await page.waitForTimeout(350);
    const meta = await page.evaluate(() => {
      const dts = document.querySelectorAll('dl.detail-meta dt');
      let typeName = null;
      let hasLocation = false;
      let locationText = '';
      for (const dt of dts) {
        const label = dt.textContent.trim();
        const dd = dt.nextElementSibling;
        if (label === 'Payload Type') typeName = dd ? dd.textContent.trim() : null;
        if (label === 'Location') { hasLocation = true; locationText = dd ? dd.textContent.trim() : ''; }
      }
      return { typeName, hasLocation, locationText };
    });
    if (predicate(meta)) return meta;
  }
  return null;
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || '/usr/bin/chromium',
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1281 Location row + map link contrast E2E against ${BASE} ===`);

  await step('Non-ADVERT packet detail does NOT render <dt>Location</dt>', async () => {
    await gotoPackets(page);
    // Filter to a non-ADVERT type to make the search efficient.
    const meta = await findPacketDetailByType(
      page,
      (m) => m.typeName && m.typeName !== 'Advert',
      40
    );
    assert(meta, 'No non-ADVERT packet found in first 40 rows');
    assert(!meta.hasLocation,
      `Expected NO <dt>Location</dt> for type "${meta.typeName}", but found one with text "${meta.locationText}"`);
  });

  await step('ADVERT packet detail STILL renders <dt>Location</dt> with GPS coords', async () => {
    await gotoPackets(page);
    // Filter UI to ADVERTs to guarantee we find one.
    const fInput = await page.$('#packetFilterInput');
    if (fInput) {
      await fInput.fill('type == ADVERT');
      await page.keyboard.press('Enter');
      await page.waitForTimeout(600);
    }
    const meta = await findPacketDetailByType(
      page,
      (m) => m.typeName === 'Advert' && m.hasLocation,
      40
    );
    assert(meta, 'No ADVERT packet with Location row found in first 40 ADVERT rows');
    assert(/-?\d+\.\d+\s*,\s*-?\d+\.\d+/.test(meta.locationText),
      `ADVERT Location should contain GPS coords, got: "${meta.locationText}"`);
  });

  await step('📍map link uses class="loc-map-link" with color = var(--accent)', async () => {
    // Reuse the ADVERT detail pane left open from the previous step.
    const result = await page.evaluate(() => {
      const link = document.querySelector('dl.detail-meta a.loc-map-link');
      if (!link) return { missing: true };
      const cs = getComputedStyle(link);
      const accentRaw = getComputedStyle(document.documentElement).getPropertyValue('--accent').trim();
      // Resolve --accent value to its computed rgb() via a probe element.
      const probe = document.createElement('span');
      probe.style.color = `var(--accent)`;
      document.body.appendChild(probe);
      const accentRgb = getComputedStyle(probe).color;
      probe.remove();
      return {
        linkColor: cs.color,
        accentRgb,
        accentRaw,
        href: link.getAttribute('href'),
        text: link.textContent.trim(),
      };
    });
    assert(!result.missing,
      '<a class="loc-map-link"> not found in detail pane — implementation must apply the class');
    const link = normRgb(result.linkColor);
    const accent = normRgb(result.accentRgb);
    console.log(`    link.color=${result.linkColor}  --accent→${result.accentRgb} (raw "${result.accentRaw}")`);
    assert(link === accent,
      `📍map link color ${result.linkColor} must equal --accent (${result.accentRgb}); ` +
      `default UA blue (rgb(0, 0, 238)) is not acceptable`);
    assert(link !== 'rgb(0, 0, 238)',
      'Link color is UA-default blue — class is missing or CSS rule does not match');
  });

  await browser.close();

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(1); });
