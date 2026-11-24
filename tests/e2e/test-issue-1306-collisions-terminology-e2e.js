#!/usr/bin/env node
/**
 * #1306 — Prefix Tool & Collisions tabs use the word "collisions" with
 * different meanings; Prefix Tool doesn't list WHICH prefixes/nodes
 * collide.
 *
 * Asserts (would fail on master):
 *  1. Prefix Tool Network Overview cards use disambiguating wording
 *     ("address conflicts" or "would-collide") rather than bare
 *     "collisions" — and include a cross-reference link to
 *     `#/analytics?tab=collisions`.
 *  2. When a tier has colliding slices (1-byte in the e2e fixture has
 *     20), an expandable toggle is rendered; clicking it reveals a
 *     table with at least 2 node links (`#/nodes/<pubkey>`).
 *  3. The Collisions (Hash Issues) tab body contains the reverse
 *     cross-reference link back to `#/analytics?tab=prefix-tool`
 *     framed around "actually observed" / packet traffic wording.
 *  4. #1914: shared Hash Issues links restore the selected byte size and
 *     its server data; selection survives navigation and refreshes.
 *  5. #1710: Prefix Tool results describe only loaded repeaters, including
 *     when a real key is excluded by the selected region.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1306-collisions-terminology-e2e.js
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

async function openPrefixTool(page, query = '') {
  await page.goto(BASE + '/#/analytics?tab=prefix-tool' + query, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => window.__collisionsThemeReady);
  await page.waitForSelector('#ptOverview', { timeout: 15000 });
  // expand the overview body (collapsed by default)
  await page.evaluate(() => {
    const body = document.getElementById('ptOverviewBody');
    if (body) body.style.display = '';
  });
}

async function prefixRepeaters(page, query = '') {
  const response = await page.request.get(BASE + '/api/nodes?limit=500&offset=0&sortBy=lastSeen' + query);
  assert(response.ok(), 'Local node API must respond successfully');
  const data = await response.json();
  assert(data.has_more === false, 'Prefix regression fixture must fit in one node page');
  return data.nodes.filter(n => n.role === 'repeater' && /^[0-9a-f]{64}$/i.test(n.public_key));
}

async function assertPrefixMatches(page, input, nodes) {
  const key = input.toUpperCase();
  const fullKey = key.length === 64;
  const lengths = fullKey ? [2, 4, 6] : [key.length];
  const cards = page.locator('#ptCheckerResults > div:not([role="alert"])');
  assert(await cards.count() === lengths.length, 'Checker must retain the expected prefix tiers');
  for (const [i, length] of lengths.entries()) {
    const expected = nodes.filter(n => n.public_key.toUpperCase().startsWith(key.slice(0, length)) &&
      (!fullKey || n.public_key.toUpperCase() !== key));
    const links = await cards.nth(i).locator('a.analytics-link').evaluateAll(els => els.map(el => el.getAttribute('href')).sort());
    assert(JSON.stringify(links) === JSON.stringify(expected.map(n => '#/nodes/' + n.public_key).sort()),
      'Matches must use loaded repeaters and exclude only the full key itself');
    const badge = (await cards.nth(i).locator(':scope > div:first-child > span').last().textContent()).trim();
    assert(badge === (expected.length ? expected.length + ' collision' + (expected.length === 1 ? '' : 's') : 'Unique'),
      'Displayed match count must agree with the loaded repeater matches');
  }
}

async function openCollisions(page, query = '') {
  await page.goto('about:blank');
  await page.goto(BASE + '/#/analytics?tab=collisions' + query, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#hashMatrixSection', { timeout: 15000 });
  await page.waitForFunction(() => document.querySelector('#hashMatrix .analytics-stat-value'));
}

function hashParams(page) {
  return new URLSearchParams(new URL(page.url()).hash.split('?')[1] || '');
}

async function refreshCollisions(page, action) {
  const previous = await page.locator('#hashMatrix').elementHandle();
  await action();
  await page.waitForFunction(el => !el.isConnected, previous);
  await previous.dispose();
}

async function assertHashView(page, bytes, collisionData) {
  await page.waitForFunction(() => window.__collisionsThemeReady);
  await page.waitForFunction(() => document.querySelector('#hashMatrix .analytics-stat-value'));
  const selected = page.locator('#hashByteSelector .hash-byte-btn.active');
  assert(await selected.count() === 1, 'Exactly one byte-size button should be selected');
  const actual = await selected.getAttribute('data-bytes');
  assert(actual === String(bytes), `Expected ${bytes}-byte selection, got ${actual}`);
  const riskTitle = await page.locator('#collisionRiskTitle').textContent();
  assert(riskTitle.includes(bytes + '-Byte Collision Risk'), 'Risk title does not match selected byte size');
  const usingCard = page.locator('#hashMatrix .analytics-stat-card').nth(1);
  assert((await usingCard.textContent()).includes('Using ' + bytes + '-byte ID'), 'Matrix uses the wrong byte-size data');
  const expected = collisionData.by_size[String(bytes)];
  assert(!!expected, 'Fixture must include ' + bytes + '-byte collision data');
  const usingCount = Number((await usingCard.locator('.analytics-stat-value').textContent()).replace(/,/g, ''));
  assert(usingCount === (expected.stats.using_this_size || 0), 'Matrix count differs from the selected server data');
  assert(await page.locator('#hashMatrix .hash-matrix-table').count() === (bytes === 3 ? 0 : 1), 'Expected a grid only for 1-byte and 2-byte views');
  const prefixes = await page.locator('#collisionList tbody tr > td:first-child').allTextContents();
  assert(JSON.stringify(prefixes) === JSON.stringify((expected.collisions || []).map(c => c.prefix)), 'Collision risk rows differ from the selected server data');
  if (!prefixes.length) {
    assert((await page.locator('#collisionList').textContent()).includes('No ' + bytes + '-byte'), 'Empty collision result should name the selected byte size');
  }
}

(async () => {
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
      console.error('test-issue-1306-collisions-terminology-e2e.js: FAIL — Chromium required but unavailable: ' + err.message);
      process.exit(1);
    }
    console.log('test-issue-1306-collisions-terminology-e2e.js: SKIP (Chromium unavailable: ' + err.message.split('\n')[0] + ')');
    process.exit(0);
  }

  const ctx = await browser.newContext({ viewport: { width: 1400, height: 1200 } });
  // Initial theme loading rebuilds analytics once; wait for it before
  // interacting with an expanded panel or asserting the restored view.
  await ctx.addInitScript(() => {
    window.addEventListener('theme-refresh', () => { window.__collisionsThemeReady = true; }, { once: true });
  });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log('\n=== #1306 collisions-terminology E2E against ' + BASE + ' ===');

  await step('Prefix Tool overview uses disambiguated wording + cross-ref link', async () => {
    await openPrefixTool(page);
    const overviewText = (await page.locator('#ptOverview').textContent()) || '';
    const lower = overviewText.toLowerCase();
    // Must contain disambiguating phrasing
    const hasDisambig = lower.includes('address conflict') || lower.includes('would-collide') || lower.includes('would collide');
    assert(hasDisambig, 'Prefix Tool overview should use "address conflict" or "would-collide" terminology, got: ' + overviewText.slice(0, 200));
    // Must include a cross-reference link to the Collisions/Hash Issues tab
    const xrefHref = await page.locator('#ptOverview a[href="#/analytics?tab=collisions"]').count();
    assert(xrefHref >= 1, 'Prefix Tool overview missing cross-reference link to #/analytics?tab=collisions');
  });

  await step('Tier with colliding slices renders expandable list of WHICH nodes collide', async () => {
    await openPrefixTool(page);
    // Find the toggle for any tier (1-byte fixture has 20 theoretical collisions)
    const toggle = page.locator('[data-pt-collide-toggle]').first();
    const count = await toggle.count();
    assert(count >= 1, 'No expandable "which collides" toggle found in Network Overview');
    await toggle.click();
    // After click, a panel with node links should appear
    const panel = page.locator('[data-pt-collide-panel]').first();
    await panel.waitFor({ state: 'visible', timeout: 4000 });
    const nodeLinks = await panel.locator('a[href^="#/nodes/"]').count();
    assert(nodeLinks >= 2, 'Expanded collision panel should list >=2 node links, got ' + nodeLinks);
  });

  // #1710: two controlled repeaters share AB; only the inspected key uses AB12.
  {
    const key = 'AB12' + '0'.repeat(60), otherKey = 'AB34' + '0'.repeat(60);
    const nodes = [key, otherKey].map(public_key => ({ public_key, role: 'repeater' }));
    const region = 'QZZ', regionsPattern = '**/api/config/regions', pattern = '**/api/nodes?*';
    let empty = false, emptyRequests = 0;
    try {
      await page.route(regionsPattern, route => route.fulfill({ status: 200, json: { [region]: 'Fixture region' } }));
      await page.route(pattern, route => {
        if (new URL(route.request().url()).searchParams.get('region') !== region) return route.continue();
        if (empty) emptyRequests++;
        return route.fulfill({ status: 200, json: { nodes: empty ? [] : nodes, total: empty ? 0 : 2, counts: {}, has_more: false } });
      });
      for (const width of [1400, 390]) {
        empty = false;
        await page.setViewportSize({ width, height: width === 390 ? 844 : 1200 });
        await page.goto('about:blank');
        await page.goto(BASE + '/#/analytics?tab=prefix-tool&region=' + encodeURIComponent(region), { waitUntil: 'domcontentloaded' });
        await page.waitForFunction(() => window.__collisionsThemeReady);
        await page.waitForSelector('#ptPrefixInput');
        const input = page.locator('#ptPrefixInput'), check = page.locator('#ptCheckBtn');
        const messages = page.locator('#ptCheckerResults > div > div.text-muted');
        await step(`#1710: ${width}px known key keeps its match and describes self-only tiers`, async () => {
          await input.fill(key); await check.click();
          const links = page.locator('#ptCheckerResults a.analytics-link');
          assert(await links.count() === 1 && await links.getAttribute('href') === '#/nodes/' + otherKey,
            'Full-key positive control must link only to the other AB repeater');
          const text = await messages.allTextContents();
          assert(text.length === 2 && text.every(t => t === 'The entered key is the only match among loaded repeaters.'),
            'Self-only tiers must identify the actual match; got: ' + text.join(' | '));
        });
        for (const [label, value] of [['absent full key', 'CD' + '0'.repeat(62)], ['partial input', 'CD000000']]) {
          await step(`#1710: ${width}px ${label} uses plain no-match wording`, async () => {
            await input.fill(value); await check.click();
            const text = await messages.allTextContents();
            assert(text.length === 3 && text.every(t => t === 'No matches among loaded repeaters.'),
              'Unmatched input must not imply an excluded key; got: ' + text.join(' | '));
          });
        }
        await step(`#1710: ${width}px manual no-match prefix scopes its empty result`, async () => {
          await input.fill('CD'); await check.click();
          const text = await messages.textContent();
          assert(text === 'No matches among loaded repeaters.', 'Manual empty result must name its population; got: ' + text);
        });
        await step(`#1710: ${width}px empty selected region makes no network-wide claim`, async () => {
          empty = true; emptyRequests = 0;
          await page.reload({ waitUntil: 'domcontentloaded' });
          await page.waitForFunction(() => window.__collisionsThemeReady);
          await page.waitForSelector('#analyticsContent > .analytics-card > p');
          assert(emptyRequests > 0 && await page.evaluate(() => RegionFilter.getRegionParam()) === region,
            'Empty result must use the actually selected region');
          const text = (await page.locator('#analyticsContent').textContent()).trim();
          assert(text === 'No repeaters loaded for the current view.', 'Empty view must name its loaded population; got: ' + text);
        });
      }
    } finally {
      await page.unroute(pattern);
      await page.unroute(regionsPattern);
      await page.evaluate(() => localStorage.removeItem('meshcore-region-filter'));
      await page.setViewportSize({ width: 1400, height: 1200 });
    }
  }

  await step('Collisions tab includes reverse cross-reference to Prefix Tool', async () => {
    await openCollisions(page);
    // Look for a link back to the prefix-tool tab with "actually observed" framing nearby
    const bodyText = (await page.locator('#hashIssuesToc, .analytics-card').first().locator('xpath=ancestor-or-self::*').last().textContent()) || '';
    const fullText = await page.evaluate(() => document.body.innerText);
    const lower = fullText.toLowerCase();
    const hasObservedFraming = lower.includes('actually observed') || lower.includes('observed in actual packet') || lower.includes('observed in packet traffic');
    assert(hasObservedFraming, 'Collisions tab missing "actually observed" framing line');
    const xref = await page.locator('a[href="#/analytics?tab=prefix-tool"]').count();
    assert(xref >= 1, 'Collisions tab missing cross-reference link to #/analytics?tab=prefix-tool');
  });

  const collisionResponse = await page.request.get(BASE + '/api/analytics/hash-collisions');
  assert(collisionResponse.ok(), 'Local collision API must respond successfully');
  const collisionData = await collisionResponse.json();

  for (const section of ['hashMatrixSection', 'collisionRiskSection']) {
    for (const bytes of [1, 2, 3]) {
      await step(`#1914: ${bytes}-byte ${section} deep link restores selection and data`, async () => {
        await openCollisions(page, '&section=' + section + '&bytes=' + bytes);
        await assertHashView(page, bytes, collisionData);
        assert(hashParams(page).get('section') === section, 'Deep link must retain the target section');
      });
    }
  }

  await step('#1914: byte clicks update a reloadable URL without losing other parameters', async () => {
    await openCollisions(page, '&section=collisionRiskSection&window=7d&filter=kept');
    for (const bytes of [2, 3, 1]) {
      await page.locator(`.hash-byte-btn[data-bytes="${bytes}"]`).click();
      const params = hashParams(page);
      assert(params.get('bytes') === String(bytes), `Byte click should write bytes=${bytes}, got ${params.get('bytes')}`);
      assert(params.get('section') === 'collisionRiskSection' && params.get('window') === '7d' && params.get('filter') === 'kept', 'Byte click must preserve other parameters');
      await page.reload({ waitUntil: 'domcontentloaded' });
      await assertHashView(page, bytes, collisionData);
    }
  });

  await step('#1914: section and top links preserve byte size and other parameters', async () => {
    await openCollisions(page, '&bytes=3&window=7d&filter=kept');
    for (const section of ['hashMatrixSection', 'collisionRiskSection', 'inconsistentHashSection']) {
      const link = page.locator(`#hashIssuesToc a[href*="section=${section}"]`);
      const params = new URLSearchParams((await link.getAttribute('href')).split('?')[1]);
      assert(params.get('bytes') === '3' && params.get('window') === '7d' && params.get('filter') === 'kept', 'Section href must carry the selected view for copying or opening');
      await link.click();
      await assertHashView(page, 3, collisionData);
      assert(hashParams(page).get('section') === section, 'Section click must update its URL target');
    }
    await page.locator('#collisionRiskSection a', { hasText: 'top' }).click();
    await assertHashView(page, 3, collisionData);
    assert(!hashParams(page).has('section') && hashParams(page).get('bytes') === '3', 'Top link should clear only the section');
  });

  await step('#1914: time-window, tab and theme refreshes retain the chosen byte size', async () => {
    await openCollisions(page);
    await page.locator('.hash-byte-btn[data-bytes="2"]').click();
    await refreshCollisions(page, () => page.locator('#analyticsTimeWindow').selectOption('24h'));
    await page.waitForFunction(() => new URLSearchParams(location.hash.split('?')[1]).get('window') === '24h');
    await assertHashView(page, 2, collisionData);
    await page.locator('#analyticsTabs [data-tab="hashsizes"]').click();
    await page.locator('#analyticsTabs [data-tab="collisions"]').click();
    await assertHashView(page, 2, collisionData);
    await page.evaluate(() => window.dispatchEvent(new Event('theme-refresh')));
    await assertHashView(page, 2, collisionData);
    assert(hashParams(page).get('bytes') === '2' && hashParams(page).get('window') === '24h', 'Refreshes must preserve the view URL');
  });

  for (const value of [null, '', '0', '4', '-1', '2.5', '2bytes', '02', 'NaN']) {
    await step(`#1914: ${value === null ? 'missing' : JSON.stringify(value)} byte size safely defaults to 1`, async () => {
      await openCollisions(page, value === null ? '' : '&bytes=' + encodeURIComponent(value));
      await assertHashView(page, 1, collisionData);
    });
  }

  for (const filter of ['region', 'area']) {
    await openCollisions(page, '&bytes=3');
    const container = page.locator(filter === 'region' ? '#analyticsRegionFilter' : '#analyticsAreaFilter');
    const option = container.locator(`[data-${filter}]:not([data-${filter}="__all__"])`).first();
    const config = await (await page.request.get(BASE + '/api/config/' + filter + 's')).json();
    if (filter === 'region' ? Object.keys(config).length < 2 : config.length === 0) {
      console.log(`  #1914 ${filter} refresh: SKIP (local fixture has no selectable ${filter} filter)`);
      continue;
    }
    await step(`#1914: ${filter} filter refresh retains byte size and selects its server data`, async () => {
      await option.waitFor({ state: 'attached' });
      const selected = await option.getAttribute('data-' + filter);
      const dropdown = container.locator('.region-dropdown-trigger');
      if (await dropdown.count()) await dropdown.click();
      await refreshCollisions(page, () => option.click());
      const response = await page.request.get(BASE + '/api/analytics/hash-collisions?' + filter + '=' + encodeURIComponent(selected));
      assert(response.ok(), 'Filtered collision API must respond successfully');
      await assertHashView(page, 3, await response.json());
      assert(hashParams(page).get('bytes') === '3', 'Filter refresh must preserve the byte-size URL');
      if (await dropdown.count()) await dropdown.click();
      await refreshCollisions(page, () => container.locator(`[data-${filter}="__all__"]`).click());
      await assertHashView(page, 3, collisionData);
    });
  }

  await step('#1914: nonempty risk rows follow deep-linked and clicked byte sizes', async () => {
    const prefixesBySize = { 1: ['A1'], 2: ['B2C3', 'B2D4'], 3: ['D4E5F6'] };
    const routePattern = '**/api/analytics/hash-collisions*';
    await page.route(routePattern, async route => {
      const response = await route.fetch();
      const json = await response.json();
      // Keep the real matrix data, but distinguish each risk list even
      // when the local fixture has no observed collisions.
      for (const [bytes, prefixes] of Object.entries(prefixesBySize)) {
        json.by_size[bytes].collisions = prefixes.map(prefix => ({
          prefix, appearances: 1, with_coords: 0, classification: 'unknown', nodes: []
        }));
      }
      await route.fulfill({ response, json });
    });
    try {
      await openCollisions(page, '&section=collisionRiskSection&bytes=2');
      await page.waitForFunction(() => window.__collisionsThemeReady);
      for (const bytes of [2, 3, 1]) {
        if (bytes !== 2) await page.locator(`.hash-byte-btn[data-bytes="${bytes}"]`).click();
        const prefixes = await page.locator('#collisionList tbody tr > td:first-child').allTextContents();
        assert(JSON.stringify(prefixes) === JSON.stringify(prefixesBySize[bytes]),
          `Expected ${bytes}-byte risk prefixes ${prefixesBySize[bytes]}, got ${prefixes}`);
      }
    } finally {
      await page.unroute(routePattern);
    }
  });

  // Use real fixture data: the missing key must exist outside the selected
  // region, not be an invented identity or an intercepted API response.
  const repeaters = await prefixRepeaters(page);
  const regions = await (await page.request.get(BASE + '/api/config/regions')).json();
  let population;
  for (const region of Object.keys(regions)) {
    const nodes = await prefixRepeaters(page, '&region=' + encodeURIComponent(region));
    const keys = new Set(nodes.map(n => n.public_key.toUpperCase()));
    const prefixes = new Set(nodes.map(n => n.public_key.slice(0, 2).toUpperCase()));
    const excluded = repeaters.find(n => !keys.has(n.public_key.toUpperCase()) &&
      prefixes.has(n.public_key.slice(0, 2).toUpperCase()));
    if (nodes.length && excluded) { population = { region, nodes, excluded }; break; }
  }
  assert(population, 'Fixture needs an excluded real repeater sharing a one-byte prefix with a loaded repeater');
  const missingKey = population.excluded.public_key.toLowerCase();
  const presentKey = population.nodes[0].public_key.toLowerCase();
  const regionQuery = '&region=' + encodeURIComponent(population.region);

  for (const width of [1400, 390]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1200 });
    await page.goto('about:blank');
    await openPrefixTool(page, regionQuery + '&prefix=' + missingKey);
    await page.locator('#ptOverviewToggle').click();

    await step(`#1710: ${width}px checker explains the loaded population before results`, async () => {
      const help = page.locator('#ptChecker > p');
      assert(await help.isVisible(), 'Population context must be visible without expanding Network Overview');
      const text = await help.textContent();
      assert(/loaded repeaters/i.test(text) && /region/i.test(text) && /area/i.test(text) && /node loading limit/i.test(text),
        'Checker help must scope results to loaded repeaters, active region/area filters and the node loading limit; got: ' + text);
    });

    await step(`#1710: ${width}px excluded real key is absent only from loaded repeaters`, async () => {
      assert(await page.locator('#ptPrefixInput').inputValue() === missingKey, 'Deep link must prefill the full key');
      assert(hashParams(page).get('prefix') === missingKey, 'Deep link must retain the full key');
      const derived = await page.locator('#ptCheckerResults > p code').allTextContents();
      assert(JSON.stringify(derived) === JSON.stringify([2, 4, 6].map(n => missingKey.slice(0, n).toUpperCase())),
        'Full key must retain all three derived prefixes');
      const oneByteMatches = await page.locator('#ptCheckerResults > div:not([role="alert"])').first().locator('a.analytics-link').count();
      assert(oneByteMatches > 0, 'Excluded full key must show a loaded one-byte match; got: ' + oneByteMatches);
      console.log(`    ${width}px excluded full key: ${oneByteMatches} loaded one-byte match(es)`);
      await assertPrefixMatches(page, missingKey, population.nodes);
      const message = await page.locator('#ptCheckerResults > p').textContent();
      assert(/not present among the loaded repeaters/i.test(message) && !/not yet in the network/i.test(message),
        'Excluded key must not imply absence from the network; got: ' + message);
    });

    await step(`#1710: ${width}px present key and manual prefix retain their matching behavior`, async () => {
      await page.goto('about:blank');
      await openPrefixTool(page, regionQuery + '&prefix=' + presentKey);
      assert(await page.locator('#ptCheckerResults > p em').count() === 0, 'Present key must not receive an absence message');
      await assertPrefixMatches(page, presentKey, population.nodes);
      const prefix = presentKey.slice(0, 4);
      await page.locator('#ptPrefixInput').fill(prefix);
      await page.locator('#ptCheckBtn').click();
      assert(await page.locator('#ptCheckerResults > p').count() === 0, 'Manual prefix must not receive a full-key absence message');
      await assertPrefixMatches(page, prefix, population.nodes);
      await page.locator('#ptPrefixInput').fill(presentKey);
      await page.locator('#ptPrefixInput').press('Enter');
      await assertPrefixMatches(page, presentKey, population.nodes);
    });
  }

  await browser.close();

  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  if (failed > 0) {
    console.error('test-issue-1306-collisions-terminology-e2e.js: FAIL');
    process.exit(1);
  }
  console.log('test-issue-1306-collisions-terminology-e2e.js: PASS');
})();
