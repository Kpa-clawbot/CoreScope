/* #730: expose the stored foreign-advert flag without claiming a current location.
 * Uses the real map with synthetic API responses, like test-map-estimated-e2e.js.
 * Run against a local fixture server: BASE_URL=http://localhost:13581 node tests/e2e/test-map-foreign-advert-e2e.js
 */
'use strict';
const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const path = require('node:path');
const fs = require('node:fs');
const BASE = process.env.BASE_URL || 'http://localhost:13581';
const keys = ['aa', 'bb', 'cc', 'dd'].map(prefix => prefix + '0'.repeat(62));
const explanation = 'An advert from this node was flagged outside the configured area at ingestion.';
const limitation = 'This historical flag does not indicate its current location.';
let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}

async function markerElement(page, key) {
  const handle = await page.evaluateHandle(key => window.fixtureMarker(key)?.getElement(), key);
  const element = handle.asElement();
  assert.ok(element, 'expected a rendered node marker');
  await element.evaluate((el, key) => { el.dataset.fixtureNode = key; }, key);
  await handle.dispose();
  return page.locator('[data-fixture-node="' + key + '"]');
}

async function checkFlags(page, expected) {
  for (let i = 0; i < 3; i++) {
    const marker = await markerElement(page, keys[i]);
    const actual = await marker.evaluate(el => ({
      warning: !!el.querySelector('.map-foreign-indicator'),
      title: el.title,
      role: el.getAttribute('role'),
      tabindex: el.getAttribute('tabindex'),
    }));
    assert.equal(actual.warning, expected[i], 'visible warning for fixture node ' + i);
    assert.equal(actual.title.includes(explanation), expected[i], 'historical marker title for fixture node ' + i);
    assert.equal(actual.role, 'button', 'marker keeps its keyboard role');
    assert.equal(actual.tabindex, '0', 'marker stays keyboard focusable');
    if (expected[i]) {
      assert.match(actual.title, /historical flag/i);
      assert.match(actual.title, /current location/i);
      assert.ok(actual.title.includes(limitation), 'marker title explicitly denies a current-location inference');
    }
  }
  assert.equal(await page.getByRole('button', { name: /Foreign advert flag/ }).count(), expected.filter(Boolean).length, 'flagged markers have accessible names');
  assert.equal(await page.evaluate(key => !!window.fixtureMarker(key), keys[3]), false, 'a flagged node without GPS must not acquire a marker');
}

async function toggle(page, id, checked) {
  await page.evaluate(({ id, checked }) => {
    const el = document.getElementById(id);
    if (el.checked !== checked) el.click();
  }, { id, checked });
}

async function scenario(browser, width, theme) {
  const context = await browser.newContext({ viewport: { width, height: 900 }, colorScheme: theme });
  const page = await context.newPage();
  page.setDefaultTimeout(10000);
  const errors = [], requests = [];
  page.on('pageerror', error => errors.push(error.message));
  const nodes = keys.map((public_key, i) => ({
    public_key, name: i ? 'Fixture ' + i : 'Flagged <b>fixture</b>', role: 'repeater',
    lat: i === 3 ? 0 : 51 + i * 0.0001, lon: i === 3 ? 0 : 5 + i * 0.0001,
    last_seen: new Date().toISOString(), hash_size: 1,
    scope_config_state: 'full', multi_byte_status: 'confirmed',
    ...(i === 2 ? {} : { foreign: i !== 1 }),
  }));
  let includeObserver = true;
  let socket;
  await page.routeWebSocket('**', ws => { socket = ws; });
  await page.addInitScript(() => {
    localStorage.setItem('meshcore-map-clustering', 'false');
    localStorage.setItem('meshcore-map-estimated', 'false');
    window.addEventListener('theme-refresh', () => { window.fixtureThemeReady = true; });
  });
  // Map tiles are unrelated to the flag and must not require an external API key.
  await page.route('https://*.basemaps.cartocdn.com/**', route => route.fulfill({
    contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="256" height="256"/>',
  }));
  await page.route('**/api/**', route => {
    const url = new URL(route.request().url());
    requests.push(url.pathname);
    const json = body => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    if (url.pathname === '/api/nodes') return json({ nodes, total: nodes.length, has_more: false, counts: { repeaters: nodes.length } });
    // Let a WS advert exercise loadNodes immediately, without the normal list TTL.
    if (url.pathname === '/api/config/cache') return json({ nodeList: 0, observers: 0 });
    if (url.pathname === '/api/stats') return json({ totalPackets: 0, totalNodes: nodes.length, totalObservers: 1 });
    if (url.pathname === '/api/observers') return json({ observers: includeObserver ? [{ id: keys[0], name: 'Fixture observer', lat: 51, lon: 5 }] : [] });
    if (url.pathname === '/api/config/map') return json({ center: [51, 5], zoom: 16 });
    return json({});
  });
  await page.goto(BASE + '/#/map?theme=' + theme, { waitUntil: 'load' });
  await page.waitForSelector('#leaflet-map[data-loaded="true"]');
  // Initial theme application rebuilds the markers after its debounce.
  await page.waitForFunction(() => window.fixtureThemeReady);
  await page.evaluate(() => {
    // Existing map test seam; inspect real Leaflet layers, not copied rendering code.
    window.fixtureMarker = key => {
      let found;
      const visit = layer => {
        if (layer._nodeKey === key) found = layer;
        else if (layer.eachLayer) layer.eachLayer(visit);
      };
      window.__mc_map.eachLayer(visit);
      return found;
    };
  });
  assert.ok(socket, 'the application WebSocket connected');
  const refresh = async () => {
    const previous = requests.filter(p => p === '/api/nodes').length;
    socket.send(JSON.stringify({ type: 'packet', data: { decoded: { header: { payloadTypeName: 'ADVERT' } } } }));
    await page.waitForFunction(expected => JSON.stringify(window.__mc_nodes) === expected, JSON.stringify(nodes));
    assert.equal(requests.filter(p => p === '/api/nodes').length, previous + 1, 'one bulk node fetch per live refresh');
  };

  for (const labels of [true, false]) {
    const label = width + 'px ' + theme + ' ' + (labels ? 'hash labels' : 'dots');
    await toggle(page, 'mcHashLabels', labels);
    await step(label + ': true warns, false/missing and no-GPS do not', () => checkFlags(page, [true, false, false]));
    await step(label + ': keyboard popup explains the historical flag and escapes the node name', async () => {
      const marker = await markerElement(page, keys[0]);
      await marker.hover();
      await marker.focus();
      await marker.press('Enter');
      const popup = page.locator('.map-popup');
      await popup.waitFor();
      await page.waitForFunction(() => getComputedStyle(document.querySelector('.leaflet-popup')).opacity === '1');
      if (process.env.SCREENSHOT_DIR) {
        fs.mkdirSync(process.env.SCREENSHOT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.SCREENSHOT_DIR, 'map-foreign-' + width + '-' + theme + '-' + (labels ? 'labels' : 'dots') + '.png') });
      }
      assert.match(await popup.innerText(), /Foreign advert flag/);
      assert.ok((await popup.innerText()).includes(explanation));
      assert.match(await popup.innerText(), /historical flag.*current location/i);
      assert.ok((await popup.innerText()).includes(limitation), 'popup explicitly denies a current-location inference');
      assert.equal(await popup.locator('h3').innerText(), nodes[0].name);
      assert.equal(await popup.locator('h3 b').count(), 0, 'node name is text, not HTML');
      const obscured = await popup.evaluate(el => {
        const controls = Array.from(document.querySelectorAll('.leaflet-control-zoom, .leaflet-control-layers'));
        return Array.from(el.querySelectorAll('h3, .map-foreign-notice')).some(content => {
          const a = content.getBoundingClientRect();
          return controls.some(control => {
            const b = control.getBoundingClientRect();
            return a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top;
          });
        });
      });
      assert.equal(obscured, false, 'the note must not widen the popup underneath the map controls');
      if (labels) assert.ok((await marker.locator('[role="img"]').getAttribute('aria-label')).includes(explanation), 'label accessible name includes the flag meaning');
      if (labels) assert.ok((await marker.locator('[role="img"]').getAttribute('aria-label')).includes(limitation), 'label accessible name explicitly denies a current-location inference');
      await page.evaluate(() => window.__mc_map.closePopup());
      await popup.waitFor({ state: 'detached' }); // Leaflet fades the old popup out.
      const unflagged = await markerElement(page, keys[1]);
      await unflagged.press('Enter');
      assert.ok(!(await popup.innerText()).includes('Foreign advert flag'), 'unflagged popup has no warning');
      await page.evaluate(() => window.__mc_map.closePopup());
      await popup.waitFor({ state: 'detached' });
    });
    await step(label + ': warning uses the theme token and coexists with observer/scope/multi-byte markers', async () => {
      await toggle(page, 'mcScopeOverlay', true);
      const scopeMarker = await markerElement(page, keys[0]);
      if (labels) {
        assert.equal(await scopeMarker.locator('.scope-full').count(), 1);
        assert.ok((await scopeMarker.locator('.mc-mb-label').getAttribute('title')).includes(explanation), 'scope title also preserves the foreign-advert explanation');
        assert.ok((await scopeMarker.locator('.mc-mb-label').getAttribute('title')).includes(limitation), 'scope title explicitly denies a current-location inference');
      }
      else assert.equal(await scopeMarker.locator('svg > [fill]').first().getAttribute('fill'), 'var(--mc-scope-full)');
      await toggle(page, 'mcMultiByte', true);
      await checkFlags(page, [true, false, false]);
      const marker = await markerElement(page, keys[0]);
      if (labels) assert.equal(await marker.locator('.status-confirmed').count(), 1, 'multi-byte overlay keeps precedence');
      const visual = await marker.evaluate(el => {
        const warning = el.querySelector('.map-foreign-indicator');
        const obs = el.querySelector('[title="Also an observer"]') || el.querySelector('svg > g');
        const probe = document.createElement('span');
        probe.style.color = 'var(--warning)';
        document.body.appendChild(probe);
        const expected = getComputedStyle(probe).color;
        probe.remove();
        if (!warning || !obs) return { missing: true };
        const a = warning.getBoundingClientRect(), b = obs.getBoundingClientRect();
        return { color: getComputedStyle(warning).color, expected, width: a.width, height: a.height,
          overlap: a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top };
      });
      assert.equal(visual.color, visual.expected, 'warning follows --warning in ' + theme);
      assert.ok(visual.width >= 12 && visual.height >= 12, 'warning is visibly sized');
      assert.equal(visual.overlap, false, 'observer star and warning remain distinct');
      await toggle(page, 'mcScopeOverlay', false);
      await toggle(page, 'mcMultiByte', false);
    });
    await step(label + ': existing markers pick up and clear a flag on live refresh', async () => {
      nodes[0].foreign = false;
      nodes[1].foreign = true;
      await refresh();
      await checkFlags(page, [false, true, false]);
      nodes[0].foreign = true;
      nodes[1].foreign = false;
      await refresh();
      await checkFlags(page, [true, false, false]);
    });
    // Restore fixture data even after an assertion failure so later cases stay independent.
    if (!nodes[0].foreign || nodes[1].foreign) {
      nodes[0].foreign = true; nodes[1].foreign = false;
      await refresh();
    }
    await toggle(page, 'mcScopeOverlay', false);
    await toggle(page, 'mcMultiByte', false);
    await step(label + ': clustering retains all nodes and the warning after expansion', async () => {
      await toggle(page, 'mcClusters', true);
      await page.evaluate(() => window.__mc_map.setZoom(12, { animate: false }));
      await page.locator('.mc-cluster').waitFor();
      assert.match(await page.locator('.mc-cluster').getAttribute('aria-label'), /3/);
      await page.evaluate(() => window.__mc_map.setZoom(16, { animate: false }));
      await page.waitForFunction(() => document.querySelectorAll('.meshcore-marker').length === 3);
      await checkFlags(page, [true, false, false]);
      await toggle(page, 'mcClusters', false);
    });
    await toggle(page, 'mcClusters', false);
  }
  await step(width + 'px ' + theme + ': normal role filter hides warnings with their nodes', async () => {
    await toggle(page, 'mcRole_repeater', false);
    assert.equal(await page.locator('.map-foreign-indicator').count(), 0);
    await toggle(page, 'mcRole_repeater', true);
    await checkFlags(page, [true, false, false]);
  });
  if (width === 1280 && theme === 'light') {
    await page.evaluate(() => document.fonts.ready);
    // The first case was separate before the warning added 17px to the label;
    // the second covers a nearby combined node/observer at the review's spacing.
    for (const fixture of [
      { name: 'colocated-two-byte', hashSize: 2, longitudes: [5, 5, 0], observer: false },
      { name: 'nearby-observer', hashSize: 1, longitudes: [5, 5.00112, 5.00224], observer: true },
    ]) {
      includeObserver = fixture.observer;
      for (let i = 0; i < 3; i++) {
        nodes[i].lat = fixture.longitudes[i] ? 51 : 0;
        nodes[i].lon = fixture.longitudes[i];
        nodes[i].hash_size = fixture.hashSize;
      }
      await refresh();
      for (const labels of [true, false]) for (const overlay of ['off', 'scope', 'multi-byte']) {
        const name = fixture.name + '-' + (labels ? 'labels' : 'dots') + '-' + overlay;
        await toggle(page, 'mcHashLabels', labels);
        await toggle(page, 'mcScopeOverlay', overlay !== 'off');
        await toggle(page, 'mcMultiByte', overlay === 'multi-byte');
        const checkWarning = async () => {
          const result = await page.evaluate(({ keys, labels }) => {
            const marker = window.fixtureMarker(keys[0]).getElement();
            const a = marker.querySelector('.map-foreign-indicator').getBoundingClientRect();
            const coveredBy = keys.slice(1, 3).filter(key => {
              const el = window.fixtureMarker(key)?.getElement();
              if (!el) return false;
              const b = (labels ? el.querySelector('.mc-mb-label') : el).getBoundingClientRect();
              return a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top;
            });
            // The warning must remain associated with its own clickable node,
            // including its edges, not merely exist in the DOM underneath it.
            const hits = [0.15, 0.5, 0.85].flatMap(x => [0.15, 0.5, 0.85].map(y => {
              const hit = document.elementFromPoint(a.left + a.width * x, a.top + a.height * y);
              return hit?.closest('.meshcore-marker') === marker;
            }));
            return { coveredBy, hits, warning: { x: a.x, y: a.y, width: a.width, height: a.height } };
          }, { keys, labels });
          assert.deepEqual(result.coveredBy, [], name + ': neighboring marker covers warning: ' + JSON.stringify(result));
          assert.ok(result.hits.every(Boolean), name + ': warning hit-test resolves to another node: ' + JSON.stringify(result));
        };
        await step(name + ': neighboring markers do not cover or intercept the warning', async () => {
          if (process.env.SCREENSHOT_DIR && labels && overlay === 'off') {
            await page.screenshot({ path: path.join(process.env.SCREENSHOT_DIR, name + '.png') });
          }
          await checkWarning();
        });
        if (labels && overlay === 'off') {
          // Watch the existing marker update, without replacing its positioning
          // logic or sleeping through the zoom/resize debounce.
          const watchReposition = () => page.evaluate(key => {
            window.fixtureRepositioned = false;
            const marker = window.fixtureMarker(key);
            const original = marker.setLatLng;
            marker.setLatLng = function () {
              marker.setLatLng = original;
              window.fixtureRepositioned = true;
              return original.apply(this, arguments);
            };
          }, keys[0]);
          for (const zoom of [17, 16]) {
            await watchReposition();
            await page.evaluate(zoom => window.__mc_map.setZoom(zoom, { animate: false }), zoom);
            await page.waitForFunction(() => window.fixtureRepositioned);
            await step(name + ': warning stays clear after zoom ' + zoom, checkWarning);
          }
          await watchReposition();
          await page.setViewportSize({ width: 1200, height: 900 });
          await page.waitForFunction(() => window.fixtureRepositioned);
          await step(name + ': warning stays clear after resize', checkWarning);
          await watchReposition();
          await page.setViewportSize({ width: 1280, height: 900 });
          await page.waitForFunction(() => window.fixtureRepositioned);
        }
      }
    }
  }
  await step(width + 'px ' + theme + ': no per-node requests or page errors', async () => {
    assert.equal(requests.filter(p => /^\/api\/nodes\/[a-f0-9]{64}/.test(p)).length, 0);
    assert.deepEqual(errors, []);
  });
  await context.close();
}

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'] });
  try {
    for (const width of [1280, 375]) for (const theme of ['light', 'dark']) await scenario(browser, width, theme);
  } finally { await browser.close(); }
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exitCode = failed ? 1 : 0;
})().catch(error => { console.error(error); process.exitCode = 1; });
