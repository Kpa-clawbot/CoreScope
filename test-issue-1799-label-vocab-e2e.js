/**
 * E2E for #1799 — canonical payload label vocabulary across surfaces.
 *
 * Asserts that the human-readable label for at least three firmware payload
 * types (TXT_MSG, GRP_TXT, GRP_DATA) is identical across:
 *   - the Live page legend (public/live.js)
 *   - the Packets page type filter (public/packets.js)
 *   - the canonical map (public/payload-labels.js, exposed as
 *     window.PayloadLabels)
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1799-label-vocab-e2e.js
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

// (enum name, numeric id) pairs we cross-check.
const ENUMS = [
  { name: 'TXT_MSG',  id: 2 },
  { name: 'GRP_TXT',  id: 5 },
  { name: 'GRP_DATA', id: 6 },
];

async function gotoLive(page) {
  await page.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#liveLegend', { timeout: 10000, state: 'attached' });
  await page.evaluate(() => {
    try { localStorage.removeItem('live-legend-hidden'); } catch (_) {}
    const el = document.getElementById('liveLegend');
    if (el) el.classList.remove('hidden');
  });
  await page.waitForTimeout(300);
}

// Pull "short" label from a legend row like "Group Data — Group datagram"
// by taking everything before the em-dash separator (with surrounding spaces).
async function legendShortLabels(page) {
  return page.evaluate(() => {
    const out = {};
    const el = document.getElementById('liveLegend');
    if (!el) return out;
    const lis = el.querySelectorAll('.legend-list li');
    const TYPE_COLORS = window.TYPE_COLORS || {};
    // Build reverse map color -> enum name
    const colorToEnum = {};
    for (const k of Object.keys(TYPE_COLORS)) colorToEnum[String(TYPE_COLORS[k]).toLowerCase()] = k;
    for (const li of lis) {
      const dot = li.querySelector('.live-dot');
      if (!dot) continue;
      const bg = (dot.style.background || dot.style.backgroundColor || '').toLowerCase();
      // background may be parsed as "rgb(...)" — extract hex from the inline style attribute instead.
      const styleAttr = dot.getAttribute('style') || '';
      const mhex = styleAttr.match(/#([0-9a-f]{3,8})/i);
      let color = mhex ? ('#' + mhex[1].toLowerCase()) : bg;
      const enumName = colorToEnum[color];
      if (!enumName) continue;
      const txt = (li.textContent || '').trim();
      // Strip leading whitespace and split on em-dash.
      const parts = txt.split(/\s+\u2014\s+/);
      out[enumName] = parts[0].trim();
    }
    return out;
  });
}

async function gotoPackets(page) {
  await page.evaluate(() => {
    try {
      localStorage.removeItem('meshcore-groupbyhash');
      localStorage.setItem('meshcore-time-window', '525600');
    } catch (_) {}
  });
  await page.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
  await page.reload({ waitUntil: 'load' });
  await page.waitForSelector('#typeTrigger', { timeout: 15000 });
  // Open the type menu so its items are rendered.
  await page.click('#typeTrigger');
  await page.waitForSelector('#typeMenu .multi-select-item', { timeout: 5000 });
}

async function packetsTypeLabels(page) {
  return page.evaluate(() => {
    const out = {};
    const items = document.querySelectorAll('#typeMenu .multi-select-item');
    for (const lab of items) {
      const cb = lab.querySelector('input[type=checkbox]');
      if (!cb) continue;
      const id = cb.getAttribute('data-type-id');
      if (id === '__all__') continue;
      out[id] = (lab.textContent || '').trim();
    }
    return out;
  });
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log(`\n=== #1799 canonical payload label vocabulary — E2E against ${BASE} ===`);

  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(10000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  await step('navigate to /live and read legend short labels', async () => { await gotoLive(page); });
  const legend = await legendShortLabels(page);

  await step('canonical map exposed as window.PayloadLabels on /live', async () => {
    const pl = await page.evaluate(() => window.PayloadLabels || null);
    assert(pl && typeof pl === 'object', 'window.PayloadLabels missing');
    for (const e of ENUMS) {
      assert(pl[e.name] && typeof pl[e.name].short === 'string',
        `PayloadLabels.${e.name}.short missing`);
      assert(typeof pl[e.name].long === 'string',
        `PayloadLabels.${e.name}.long missing`);
      assert(pl[e.name].enumId === e.id,
        `PayloadLabels.${e.name}.enumId expected ${e.id}, got ${pl[e.name].enumId}`);
    }
  });

  await step('navigate to /packets and open type filter', async () => { await gotoPackets(page); });
  const packetsLabels = await packetsTypeLabels(page);

  await step('canonical map also exposed on /packets', async () => {
    const pl = await page.evaluate(() => window.PayloadLabels || null);
    assert(pl && typeof pl === 'object', 'window.PayloadLabels missing on /packets');
  });

  const canonical = await page.evaluate(() => window.PayloadLabels || {});

  for (const e of ENUMS) {
    await step(`label equality for ${e.name}: legend == packets-filter == canonical.short`, async () => {
      const canon = canonical[e.name] && canonical[e.name].short;
      const fromLegend = legend[e.name];
      const fromPackets = packetsLabels[String(e.id)];
      assert(canon, `canonical short missing for ${e.name}`);
      assert(fromLegend, `legend label missing for ${e.name} (got: ${JSON.stringify(legend)})`);
      assert(fromPackets, `packets type-menu label missing for id=${e.id} (got: ${JSON.stringify(packetsLabels)})`);
      assert(fromLegend === canon,
        `legend label "${fromLegend}" != canonical "${canon}" for ${e.name}`);
      assert(fromPackets === canon,
        `packets label "${fromPackets}" != canonical "${canon}" for ${e.name}`);
    });
  }

  await step('packet-filter FW_PAYLOAD_TYPES still maps numeric ids to enum names', async () => {
    const pf = await page.evaluate(() => {
      // packet-filter doesn't expose FW_PAYLOAD_TYPES directly; compile a
      // round-trip to verify enum name is recognised.
      const pf = window.PacketFilter; if (!pf) return null;
      const out = {};
      for (const [name, id] of [['TXT_MSG',2],['GRP_TXT',5],['GRP_DATA',6]]) {
        const c = pf.compile('type == ' + name);
        out[name] = !c.error && c.filter({ payload_type: id }) === true;
      }
      return out;
    });
    assert(pf, 'window.PacketFilter missing');
    for (const e of ENUMS) {
      assert(pf[e.name], `packet-filter does not recognise ${e.name}`);
    }
  });

  await ctx.close();
  await browser.close();
  console.log(`\n=== ${passed} passed, ${failed} failed ===`);
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(1); });
