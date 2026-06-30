/**
 * E2E for #1799 — canonical payload label vocabulary across surfaces.
 *
 * After PR #1804 round-1 review:
 *   - Item 12: literal pinned expected labels (not derived from the same
 *     map being tested) + inline-fallback drift gate.
 *   - Item 13: explicit TYPE_ALIASES coverage through PacketFilter.
 *   - Item 14: every key in the canonical map is exercised against pinned
 *     literals — not just the original 3 enums.
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

// PINNED expected literals — these MUST match public/payload-labels.js by
// hand. The whole point of pinning (round-1 review item 12) is that if
// either side drifts, the test fails — NOT because both sides derive from
// the same object.
const EXPECTED_SHORT = {
  REQ:        'Request',
  RESPONSE:   'Response',
  TXT_MSG:    'Direct Msg',
  ACK:        'ACK',
  ADVERT:     'Advert',
  GRP_TXT:    'Channel Msg',
  GRP_DATA:   'Group Data',
  ANON_REQ:   'Anon Req',
  PATH:       'Path',
  TRACE:      'Trace',
  MULTIPART:  'Multipart',
  CONTROL:    'Control',
  RAW_CUSTOM: 'Raw Custom'
};
// PR #1804 r1 item 2 (tufte2): every `long` must DESCRIBE the packet's
// behaviour, not echo the short label. Pinned literals — drift here
// fails the E2E.
const EXPECTED_LONG = {
  REQ:        'Encrypted data request to a remote node',
  RESPONSE:   'Encrypted data response from a remote node',
  TXT_MSG:    'Encrypted point-to-point text message',
  ACK:        'Acknowledgment of a prior message or request',
  ADVERT:     'Node identity and capability advertisement',
  GRP_TXT:    'Channel-scoped group text message',
  GRP_DATA:   'Channel-scoped group datagram (non-text payload)',
  ANON_REQ:   'Anonymous encrypted request via ephemeral key',
  PATH:       'Network path discovery and return-path advertisement',
  TRACE:      'Per-hop route trace with SNR samples',
  MULTIPART:  'Fragmented payload reassembled across multiple packets',
  CONTROL:    'Mesh control-plane signalling (e.g. zero-hop direct)',
  RAW_CUSTOM: 'Application-defined raw payload, no firmware envelope'
};
const EXPECTED_ID = {
  REQ: 0, RESPONSE: 1, TXT_MSG: 2, ACK: 3, ADVERT: 4, GRP_TXT: 5,
  GRP_DATA: 6, ANON_REQ: 7, PATH: 8, TRACE: 9, MULTIPART: 10,
  CONTROL: 11, RAW_CUSTOM: 15
};
const ALL_ENUMS = Object.keys(EXPECTED_SHORT);

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

// Pull "short" label from each legend row. Build the color→enum reverse
// map from TYPE_COLORS so we can identify rows by enum without trusting
// the rendered text.
async function legendShortLabels(page) {
  return page.evaluate(() => {
    const out = {};
    const el = document.getElementById('liveLegend');
    if (!el) return out;
    const lis = el.querySelectorAll('.legend-list li');
    const TYPE_COLORS = window.TYPE_COLORS || {};
    const colorToEnum = {};
    for (const k of Object.keys(TYPE_COLORS)) colorToEnum[String(TYPE_COLORS[k]).toLowerCase()] = k;
    for (const li of lis) {
      // PR #1804 r1 item 4 (tufte4+adv5): rows now carry data-enum, so we
      // identify by enum directly instead of reverse-mapping via the
      // shared #6b7280 color (which forced an insertion-order workaround
      // in roles.js). Fall back to the color path only if data-enum is
      // missing, for robustness while the change rolls out.
      const enumAttr = li.getAttribute('data-enum');
      const dot = li.querySelector('.live-dot');
      let enumName = enumAttr || '';
      if (!enumName) {
        if (!dot) continue;
        const styleAttr = dot.getAttribute('style') || '';
        const mhex = styleAttr.match(/#([0-9a-f]{3,8})/i);
        const color = mhex ? ('#' + mhex[1].toLowerCase()) : '';
        enumName = colorToEnum[color];
        if (!enumName) continue;
      }
      const txt = (li.textContent || '').trim();
      // PR #1804 r1 item 1 (tufte1+adv1): all rows render with the same
      // em-dash separator now (no slash special-case for ACK), so a
      // single split rule applies.
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
    // Pinned-literal check, NOT a self-derived comparison (round-1 item 12).
    for (const name of ALL_ENUMS) {
      assert(pl[name], `PayloadLabels.${name} missing`);
      assert(pl[name].short === EXPECTED_SHORT[name],
        `PayloadLabels.${name}.short: expected "${EXPECTED_SHORT[name]}", got "${pl[name].short}"`);
      assert(pl[name].enumId === EXPECTED_ID[name],
        `PayloadLabels.${name}.enumId: expected ${EXPECTED_ID[name]}, got ${pl[name].enumId}`);
      assert(pl[name].enumName === name,
        `PayloadLabels.${name}.enumName: expected "${name}", got "${pl[name].enumName}"`);
      // PR #1804 r1 item 2: long must be the behavioural description, not
      // a tautological echo of short. Pinned literals.
      assert(pl[name].long === EXPECTED_LONG[name],
        `PayloadLabels.${name}.long: expected "${EXPECTED_LONG[name]}", got "${pl[name].long}"`);
    }
  });

  await step('every legend row matches the pinned canonical short label', async () => {
    // Round-1 item 14: cover ALL 13 enums, not just 3.
    for (const name of ALL_ENUMS) {
      const got = legend[name];
      assert(got === EXPECTED_SHORT[name],
        `legend[${name}]: expected "${EXPECTED_SHORT[name]}", got "${got}" (full legend: ${JSON.stringify(legend)})`);
    }
  });

  await step('every legend row carries data-enum=<ENUM_NAME> (PR #1804 r1 item 4)', async () => {
    // tufte4+adv5: rows must be identifiable by enum, not by reverse-
    // mapping the shared #6b7280 color.
    const rows = await page.evaluate(() => {
      const el = document.getElementById('liveLegend');
      if (!el) return [];
      return Array.from(el.querySelectorAll('.legend-list li')).map(li => ({
        en: li.getAttribute('data-enum'),
        text: (li.textContent || '').trim()
      }));
    });
    const enumsSeen = new Set();
    for (const r of rows) {
      // Only legend rows with a live-dot are payload-type rows. Other
      // <li>s (e.g. role legend, ring legend) may not carry data-enum.
      if (!r.en) continue;
      enumsSeen.add(r.en);
    }
    for (const name of ALL_ENUMS) {
      assert(enumsSeen.has(name),
        `data-enum="${name}" missing on legend row (rows=${JSON.stringify(rows)})`);
    }
  });

  await step('all legend rows render with the uniform em-dash separator (PR #1804 r1 item 1)', async () => {
    // tufte1+adv1: ACK row used to render with a slash + 'Other —'
    // wrapper. Now every row is `SHORT — LONG`.
    const rows = await page.evaluate(() => {
      const el = document.getElementById('liveLegend');
      if (!el) return [];
      return Array.from(el.querySelectorAll('.legend-list li[data-enum]'))
        .map(li => (li.textContent || '').trim());
    });
    for (const t of rows) {
      assert(t.indexOf('\u2014') !== -1, `legend row missing em-dash: "${t}"`);
      assert(t.indexOf(' / ') === -1, `legend row uses slash separator: "${t}"`);
    }
  });

  await step('canonical map exposed at window.PayloadLabels.enums (PR #1804 r1 item 8)', async () => {
    const ok = await page.evaluate((enums) => {
      const PL = window.PayloadLabels;
      if (!PL || !PL.enums || !PL.api) return { ok: false, why: 'missing PL/enums/api' };
      for (const k of enums) {
        if (!PL.enums[k]) return { ok: false, why: 'PL.enums.' + k + ' missing' };
        if (!PL.api.SHORT_BY_ID) return { ok: false, why: 'PL.api.SHORT_BY_ID missing' };
      }
      return { ok: true };
    }, ALL_ENUMS);
    assert(ok.ok, 'namespace check: ' + (ok.why || 'unknown'));
  });

  await step('navigate to /packets and open type filter', async () => { await gotoPackets(page); });
  const packetsLabels = await packetsTypeLabels(page);

  await step('canonical map also exposed on /packets', async () => {
    const pl = await page.evaluate(() => window.PayloadLabels || null);
    assert(pl && typeof pl === 'object', 'window.PayloadLabels missing on /packets');
  });

  await step('every packets type-filter row matches the pinned canonical short label', async () => {
    // Round-1 item 14: cover ALL enums on the packets page too.
    for (const name of ALL_ENUMS) {
      const id = String(EXPECTED_ID[name]);
      const got = packetsLabels[id];
      assert(got === EXPECTED_SHORT[name],
        `packets type-menu[id=${id} (${name})]: expected "${EXPECTED_SHORT[name]}", got "${got}"`);
    }
  });

  await step('PacketFilter recognises every enum name (round-trip)', async () => {
    const pf = await page.evaluate((enums) => {
      const pf = window.PacketFilter; if (!pf) return null;
      const out = {};
      for (const e of enums) {
        const c = pf.compile('type == ' + e.name);
        out[e.name] = !c.error && c.filter({ payload_type: e.id }) === true;
      }
      return out;
    }, ALL_ENUMS.map(n => ({ name: n, id: EXPECTED_ID[n] })));
    assert(pf, 'window.PacketFilter missing');
    for (const name of ALL_ENUMS) {
      assert(pf[name], `packet-filter does not recognise enum name "${name}"`);
    }
  });

  await step('PacketFilter resolves TYPE_ALIASES through PacketFilter.compile (round-1 item 13)', async () => {
    // Map of alias → expected enumId. Each must resolve via the filter
    // language (quoted alias values use the same alias table). Covers the
    // path that round-1 item 13 flagged as untested.
    const ALIAS_CASES = [
      { alias: 'channel msg', id: 5 },
      { alias: 'dm',          id: 2 },
      { alias: 'direct msg',  id: 2 },
      { alias: 'group data',  id: 6 },
      { alias: 'raw custom',  id: 15 },
      { alias: 'anon req',    id: 7 },
      { alias: 'request',     id: 0 }
    ];
    const got = await page.evaluate((cases) => {
      const pf = window.PacketFilter; if (!pf) return null;
      return cases.map(c => {
        const compiled = pf.compile('type == "' + c.alias + '"');
        return {
          alias: c.alias,
          id: c.id,
          ok: !compiled.error && compiled.filter({ payload_type: c.id }) === true,
          err: compiled.error || null
        };
      });
    }, ALIAS_CASES);
    assert(got, 'window.PacketFilter missing');
    for (const r of got) {
      assert(r.ok, `alias "${r.alias}" → payload_type=${r.id} failed (err=${r.err})`);
    }
  });

  await ctx.close();
  await browser.close();
  console.log(`\n=== ${passed} passed, ${failed} failed ===`);
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(1); });
