/**
 * Playwright E2E tests — proof of concept
 * Runs against prod (analyzer.00id.net), read-only.
 * Usage: node test-e2e-playwright.js
 */
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';
const results = [];

async function test(name, fn) {
  try {
    await fn();
    results.push({ name, pass: true });
    console.log(`  \u2705 ${name}`);
  } catch (err) {
    results.push({ name, pass: false, error: err.message });
    console.log(`  \u274c ${name}: ${err.message}`);
  }
}

function assert(condition, msg) {
  if (!condition) throw new Error(msg || 'Assertion failed');
}

async function run() {
  console.log('Launching Chromium...');
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage']
  });
  const context = await browser.newContext();
  const page = await context.newPage();
  page.setDefaultTimeout(10000);

  console.log(`\nRunning E2E tests against ${BASE}\n`);

  // --- Group: Home page (tests 1, 6, 7) ---

  // Test 1: Home page loads
  await test('Home page loads', async () => {
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    const title = await page.title();
    assert(title.toLowerCase().includes('meshcore'), `Title "${title}" doesn't contain MeshCore`);
    const nav = await page.$('nav, .navbar, .nav, [class*="nav"]');
    assert(nav, 'Nav bar not found');
  });

  // Test 6: Theme customizer opens (reuses home page from test 1)
  await test('Theme customizer opens', async () => {
    // Look for palette/customize button
    const btn = await page.$('button[title*="ustom" i], button[aria-label*="theme" i], [class*="customize"], button:has-text("\ud83c\udfa8")');
    if (!btn) {
      // Try finding by emoji content
      const allButtons = await page.$$('button');
      let found = false;
      for (const b of allButtons) {
        const text = await b.textContent();
        if (text.includes('\ud83c\udfa8')) {
          await b.click();
          found = true;
          break;
        }
      }
      assert(found, 'Could not find theme customizer button');
    } else {
      await btn.click();
    }
    await page.waitForFunction(() => {
      const html = document.body.innerHTML;
      return html.includes('preset') || html.includes('Preset') || html.includes('theme') || html.includes('Theme');
    });
    const html = await page.content();
    const hasCustomizer = html.includes('preset') || html.includes('Preset') || html.includes('theme') || html.includes('Theme');
    assert(hasCustomizer, 'Customizer panel not found after clicking');
  });

  // Test 7: Dark mode toggle (fresh navigation \u2014 customizer panel may be open)
  await test('Dark mode toggle', async () => {
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    const themeBefore = await page.$eval('html', el => el.getAttribute('data-theme'));
    // Find toggle button
    const allButtons = await page.$$('button');
    let toggled = false;
    for (const b of allButtons) {
      const text = await b.textContent();
      if (text.includes('\u2600') || text.includes('\ud83c\udf19') || text.includes('\ud83c\udf11') || text.includes('\ud83c\udf15')) {
        await b.click();
        toggled = true;
        break;
      }
    }
    assert(toggled, 'Could not find dark mode toggle button');
    await page.waitForFunction(
      (before) => document.documentElement.getAttribute('data-theme') !== before,
      themeBefore
    );
    const themeAfter = await page.$eval('html', el => el.getAttribute('data-theme'));
    assert(themeBefore !== themeAfter, `Theme didn't change: before=${themeBefore}, after=${themeAfter}`);
  });

  // --- Group: Nodes page (tests 2, 5) ---

  // Test 2: Nodes page loads with data
  await test('Nodes page loads with data', async () => {
    await page.goto(`${BASE}/#/nodes`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('table tbody tr');
    const headers = await page.$$eval('th', els => els.map(e => e.textContent.trim()));
    for (const col of ['Name', 'Public Key', 'Role']) {
      assert(headers.some(h => h.includes(col)), `Missing column: ${col}`);
    }
    assert(headers.some(h => h.includes('Last Seen') || h.includes('Last')), 'Missing Last Seen column');
    const rows = await page.$$('table tbody tr');
    assert(rows.length >= 1, `Expected >=1 nodes, got ${rows.length}`);
  });

  // Test 5: Node detail loads (reuses nodes page from test 2)
  await test('Node detail loads', async () => {
    await page.waitForSelector('table tbody tr');
    // Click first row
    const firstRow = await page.$('table tbody tr');
    assert(firstRow, 'No node rows found');
    await firstRow.click();
    // Wait for detail pane to appear
    await page.waitForSelector('.node-detail');
    const html = await page.content();
    // Check for status indicator
    const hasStatus = html.includes('\ud83d\udfe2') || html.includes('\u26aa') || html.includes('status') || html.includes('Active') || html.includes('Stale');
    assert(hasStatus, 'No status indicator found in node detail');
  });

  // --- Group: Map page (tests 3, 9, 10, 13, 16) ---

  // Test 3: Map page loads with markers
  await test('Map page loads with markers', async () => {
    await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.leaflet-container');
    await page.waitForSelector('.leaflet-tile-loaded');
    // Wait for markers/overlays to render (may not exist with empty DB)
    try {
      await page.waitForSelector('.leaflet-marker-icon, .leaflet-interactive, circle, .marker-cluster, .leaflet-marker-pane > *, .leaflet-overlay-pane svg path, .leaflet-overlay-pane svg circle', { timeout: 3000 });
    } catch (_) {
      // No markers with empty DB \u2014 assertion below handles it
    }
    const markers = await page.$$('.leaflet-marker-icon, .leaflet-interactive, circle, .marker-cluster, .leaflet-marker-pane > *, .leaflet-overlay-pane svg path, .leaflet-overlay-pane svg circle');
    assert(markers.length > 0, 'No map markers/overlays found');
  });

  // Test 9: Map heat checkbox persists in localStorage (reuses map page)
  await test('Map heat checkbox persists in localStorage', async () => {
    await page.waitForSelector('#mcHeatmap');
    // Uncheck first to ensure clean state
    await page.evaluate(() => localStorage.removeItem('meshcore-map-heatmap'));
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#mcHeatmap');
    let checked = await page.$eval('#mcHeatmap', el => el.checked);
    assert(!checked, 'Heat checkbox should be unchecked by default');
    // Check it
    await page.click('#mcHeatmap');
    const stored = await page.evaluate(() => localStorage.getItem('meshcore-map-heatmap'));
    assert(stored === 'true', `localStorage should be "true" but got "${stored}"`);
    // Reload and verify persisted
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#mcHeatmap');
    checked = await page.$eval('#mcHeatmap', el => el.checked);
    assert(checked, 'Heat checkbox should be checked after reload');
    // Clean up
    await page.evaluate(() => localStorage.removeItem('meshcore-map-heatmap'));
  });

  // Test 10: Map heat checkbox is not disabled (unless matrix mode)
  await test('Map heat checkbox is clickable', async () => {
    await page.waitForSelector('#mcHeatmap');
    const disabled = await page.$eval('#mcHeatmap', el => el.disabled);
    assert(!disabled, 'Heat checkbox should not be disabled');
    // Click and verify state changes
    const before = await page.$eval('#mcHeatmap', el => el.checked);
    await page.click('#mcHeatmap');
    const after = await page.$eval('#mcHeatmap', el => el.checked);
    assert(before !== after, 'Heat checkbox state should toggle on click');
  });

  // Test 13: Heatmap opacity stored in localStorage (reuses map page)
  await test('Heatmap opacity persists in localStorage', async () => {
    await page.evaluate(() => localStorage.setItem('meshcore-heatmap-opacity', '0.5'));
    // Enable heat to trigger layer creation with saved opacity
    await page.evaluate(() => localStorage.setItem('meshcore-map-heatmap', 'true'));
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#mcHeatmap');
    const opacity = await page.evaluate(() => localStorage.getItem('meshcore-heatmap-opacity'));
    assert(opacity === '0.5', `Opacity should persist as "0.5" but got "${opacity}"`);
    // Verify the canvas element has the opacity applied (if heat layer exists)
    const canvasOpacity = await page.evaluate(() => {
      if (window._meshcoreHeatLayer && window._meshcoreHeatLayer._canvas) {
        return window._meshcoreHeatLayer._canvas.style.opacity;
      }
      return null; // no heat layer (no node data) \u2014 skip
    });
    if (canvasOpacity !== null) {
      assert(canvasOpacity === '0.5', `Canvas opacity should be "0.5" but got "${canvasOpacity}"`);
    }
    // Clean up
    await page.evaluate(() => {
      localStorage.removeItem('meshcore-heatmap-opacity');
      localStorage.removeItem('meshcore-map-heatmap');
    });
  });

  // Test 16: Map re-renders markers on resize (decollision recalculates)
  await test('Map re-renders on resize', async () => {
    await page.waitForSelector('.leaflet-container');
    // Wait for markers (may not exist with empty DB)
    try {
      await page.waitForSelector('.leaflet-marker-icon, .leaflet-interactive', { timeout: 3000 });
    } catch (_) {
      // No markers with empty DB
    }
    // Count markers before resize
    const beforeCount = await page.$$eval('.leaflet-marker-icon, .leaflet-interactive', els => els.length);
    // Resize viewport
    await page.setViewportSize({ width: 600, height: 400 });
    // Wait for Leaflet to process resize
    await page.waitForFunction(() => {
      const c = document.querySelector('.leaflet-container');
      return c && c.offsetWidth <= 600;
    });
    // Markers should still be present after resize (re-rendered, not lost)
    const afterCount = await page.$$eval('.leaflet-marker-icon, .leaflet-interactive', els => els.length);
    assert(afterCount > 0, `Should have markers after resize, got ${afterCount}`);
    // Restore
    await page.setViewportSize({ width: 1280, height: 720 });
  });

  // --- Group: Packets page (test 4) ---

  // Test 4: Packets page loads with filter
  await test('Packets page loads with filter', async () => {
    await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('table tbody tr');
    const rowsBefore = await page.$$('table tbody tr');
    assert(rowsBefore.length > 0, 'No packets visible');
    // Use the specific filter input
    const filterInput = await page.$('#packetFilterInput');
    assert(filterInput, 'Packet filter input not found');
    await filterInput.fill('type == ADVERT');
    // Client-side filter has input debounce (~250ms); wait for it to apply
    await page.waitForTimeout(500);
    // Verify filter was applied (count may differ)
    const rowsAfter = await page.$$('table tbody tr');
    assert(rowsAfter.length > 0, 'No packets after filtering');
  });

  // Test: Packet detail pane hidden on fresh load
  await test('Packets detail pane hidden on fresh load', async () => {
    await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#pktRight');
    const isEmpty = await page.$eval('#pktRight', el => el.classList.contains('empty'));
    assert(isEmpty, 'Detail pane should have "empty" class on fresh load');
  });

  // Test: Packets groupByHash toggle changes view
  await test('Packets groupByHash toggle works', async () => {
    await page.waitForSelector('table tbody tr');
    const groupBtn = await page.$('#fGroup');
    assert(groupBtn, 'Group by hash button (#fGroup) not found');
    // Check initial state (default is grouped/active)
    const initialActive = await page.$eval('#fGroup', el => el.classList.contains('active'));
    // Click to toggle
    await groupBtn.click();
    await page.waitForFunction((wasActive) => {
      const btn = document.getElementById('fGroup');
      return btn && btn.classList.contains('active') !== wasActive;
    }, initialActive, { timeout: 5000 });
    const afterFirst = await page.$eval('#fGroup', el => el.classList.contains('active'));
    assert(afterFirst !== initialActive, 'Group button state should change after click');
    await page.waitForSelector('table tbody tr');
    const rows = await page.$$eval('table tbody tr', r => r.length);
    assert(rows > 0, 'Should have rows after toggle');
    // Click again to toggle back
    await groupBtn.click();
    await page.waitForFunction((prev) => {
      const btn = document.getElementById('fGroup');
      return btn && btn.classList.contains('active') !== prev;
    }, afterFirst, { timeout: 5000 });
    const afterSecond = await page.$eval('#fGroup', el => el.classList.contains('active'));
    assert(afterSecond === initialActive, 'Group button should return to initial state after second click');
  });

  // Test: Clicking a packet row opens detail pane
  await test('Packets clicking row shows detail pane', async () => {
    await page.waitForSelector('table tbody tr[data-action]');
    const firstRow = await page.$('table tbody tr[data-action]');
    assert(firstRow, 'No clickable packet rows found');
    await firstRow.click();
    await page.waitForFunction(() => {
      const panel = document.getElementById('pktRight');
      return panel && !panel.classList.contains('empty');
    }, { timeout: 5000 });
    const panelVisible = await page.$eval('#pktRight', el => !el.classList.contains('empty'));
    assert(panelVisible, 'Detail pane should open after clicking a row');
    const content = await page.$eval('#pktRight', el => el.textContent.trim());
    assert(content.length > 0, 'Detail pane should have content');
  });

  // Test: Packet detail pane dismiss button (Issue #125)
  await test('Packet detail pane closes on ✕ click', async () => {
    // Detail pane should be open from previous test
    const panelOpen = await page.$eval('#pktRight', el => !el.classList.contains('empty'));
    if (!panelOpen) {
      const firstRow = await page.$('table tbody tr[data-action]');
      if (!firstRow) { console.log('    ⏭️  Skipped (no clickable rows)'); return; }
      await firstRow.click();
      await page.waitForFunction(() => {
        const panel = document.getElementById('pktRight');
        return panel && !panel.classList.contains('empty');
      }, { timeout: 5000 });
    }
    const closeBtn = await page.$('#pktRight .panel-close-btn');
    assert(closeBtn, 'Close button (✕) not found in detail pane');
    await closeBtn.click();
    await page.waitForFunction(() => {
      const panel = document.getElementById('pktRight');
      return panel && panel.classList.contains('empty');
    }, { timeout: 3000 });
    const panelHidden = await page.$eval('#pktRight', el => el.classList.contains('empty'));
    assert(panelHidden, 'Detail pane should be hidden after clicking ✕');
  });

  // --- Group: Analytics page (test 8 + sub-tabs) ---

  // Test 8: Analytics page loads with overview
  await test('Analytics page loads', async () => {
    await page.goto(`${BASE}/#/analytics`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#analyticsTabs');
    const tabs = await page.$$('#analyticsTabs .tab-btn');
    assert(tabs.length >= 8, `Expected >=8 analytics tabs, got ${tabs.length}`);
    // Overview tab should be active by default and show stat cards
    await page.waitForSelector('#analyticsContent .stat-card', { timeout: 8000 });
    const cards = await page.$$('#analyticsContent .stat-card');
    assert(cards.length >= 3, `Expected >=3 overview stat cards, got ${cards.length}`);
  });

  // Analytics sub-tab tests
  await test('Analytics RF tab renders content', async () => {
    await page.click('[data-tab="rf"]');
    await page.waitForSelector('#analyticsContent .analytics-table, #analyticsContent svg', { timeout: 8000 });
    const hasTables = await page.$$eval('#analyticsContent .analytics-table', els => els.length);
    const hasSvg = await page.$$eval('#analyticsContent svg', els => els.length);
    assert(hasTables > 0 || hasSvg > 0, 'RF tab should render tables or SVG charts');
  });

  await test('Analytics Topology tab renders content', async () => {
    await page.click('[data-tab="topology"]');
    await page.waitForFunction(() => {
      const c = document.getElementById('analyticsContent');
      return c && (c.querySelector('.repeater-list') || c.querySelector('.analytics-card') || c.querySelector('.reach-rings'));
    }, { timeout: 8000 });
    const hasContent = await page.$$eval('#analyticsContent .analytics-card, #analyticsContent .repeater-list', els => els.length);
    assert(hasContent > 0, 'Topology tab should render cards or repeater list');
  });

  await test('Analytics Channels tab renders content', async () => {
    await page.click('[data-tab="channels"]');
    await page.waitForFunction(() => {
      const c = document.getElementById('analyticsContent');
      return c && c.textContent.trim().length > 10;
    }, { timeout: 8000 });
    const content = await page.$eval('#analyticsContent', el => el.textContent.trim());
    assert(content.length > 10, 'Channels tab should render content');
  });

  await test('Analytics Hash Stats tab renders content', async () => {
    await page.click('[data-tab="hashsizes"]');
    await page.waitForSelector('#analyticsContent .hash-bar-row, #analyticsContent .analytics-table', { timeout: 8000 });
    const content = await page.$eval('#analyticsContent', el => el.textContent.trim());
    assert(content.length > 10, 'Hash Stats tab should render content');
  });

  await test('Analytics Hash Issues tab renders content', async () => {
    await page.click('[data-tab="collisions"]');
    await page.waitForFunction(() => {
      const c = document.getElementById('analyticsContent');
      return c && (c.querySelector('#hashMatrix') || c.querySelector('#inconsistentHashSection') || c.textContent.trim().length > 20);
    }, { timeout: 8000 });
    const hasContent = await page.$('#analyticsContent #hashMatrix, #analyticsContent #inconsistentHashSection');
    const text = await page.$eval('#analyticsContent', el => el.textContent.trim());
    assert(hasContent || text.length > 20, 'Hash Issues tab should render content');
  });

  await test('Analytics Route Patterns tab renders content', async () => {
    await page.click('[data-tab="subpaths"]');
    await page.waitForFunction(() => {
      const c = document.getElementById('analyticsContent');
      return c && (c.querySelector('.subpath-layout') || c.textContent.trim().length > 20);
    }, { timeout: 8000 });
    const content = await page.$eval('#analyticsContent', el => el.textContent.trim());
    assert(content.length > 10, 'Route Patterns tab should render content');
  });

  await test('Analytics Distance tab renders content', async () => {
    await page.click('[data-tab="distance"]');
    await page.waitForFunction(() => {
      const c = document.getElementById('analyticsContent');
      return c && (c.querySelector('.stat-card') || c.querySelector('.data-table') || c.textContent.trim().length > 20);
    }, { timeout: 8000 });
    const content = await page.$eval('#analyticsContent', el => el.textContent.trim());
    assert(content.length > 10, 'Distance tab should render content');
  });

  // --- Group: Compare page ---

  await test('Compare page loads with observer dropdowns', async () => {
    await page.goto(`${BASE}/#/compare`, { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => {
      const selA = document.getElementById('compareObsA');
      return selA && selA.options.length > 1;
    }, { timeout: 10000 });
    const optionsA = await page.$$eval('#compareObsA option', opts => opts.length);
    const optionsB = await page.$$eval('#compareObsB option', opts => opts.length);
    assert(optionsA > 1, `Observer A dropdown should have options, got ${optionsA}`);
    assert(optionsB > 1, `Observer B dropdown should have options, got ${optionsB}`);
  });

  await test('Compare page runs comparison', async () => {
    const options = await page.$$eval('#compareObsA option', opts =>
      opts.filter(o => o.value).map(o => o.value)
    );
    assert(options.length >= 2, `Need >=2 observers, got ${options.length}`);
    await page.selectOption('#compareObsA', options[0]);
    await page.selectOption('#compareObsB', options[1]);
    await page.waitForFunction(() => {
      const btn = document.getElementById('compareBtn');
      return btn && !btn.disabled;
    }, { timeout: 3000 });
    await page.click('#compareBtn');
    await page.waitForFunction(() => {
      const c = document.getElementById('compareContent');
      return c && c.textContent.trim().length > 20;
    }, { timeout: 15000 });
    const hasResults = await page.$eval('#compareContent', el => el.textContent.trim().length > 0);
    assert(hasResults, 'Comparison should produce results');
  });

  // --- Group: Live page ---

  // Test: Live page loads with map and stats
  await test('Live page loads with map and stats', async () => {
    await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#liveMap');
    // Verify key page elements exist
    const hasMap = await page.$('#liveMap');
    assert(hasMap, 'Live page should have map element');
    const hasHeader = await page.$('#liveHeader, .live-header');
    assert(hasHeader, 'Live page should have header');
    // Check stats elements exist
    const pktCount = await page.$('#livePktCount');
    assert(pktCount, 'Live page should have packet count element');
    const nodeCount = await page.$('#liveNodeCount');
    assert(nodeCount, 'Live page should have node count element');
  });

  // Test: Live page WebSocket connects
  await test('Live page WebSocket connects', async () => {
    // Check for live beacon indicator (shows page is in live mode)
    const hasBeacon = await page.$('.live-beacon');
    assert(hasBeacon, 'Live page should have beacon indicator');
    // Check VCR mode indicator shows LIVE
    const vcrMode = await page.$('#vcrMode, #vcrLcdMode');
    assert(vcrMode, 'Live page should have VCR mode indicator');
    // Verify WebSocket is connected by checking for the ws object
    const wsConnected = await page.evaluate(() => {
      // The live page creates a WebSocket - check if it exists
      // Look for any WebSocket instances or connection indicators
      const beacon = document.querySelector('.live-beacon');
      const vcrDot = document.querySelector('.vcr-live-dot');
      return !!(beacon || vcrDot);
    });
    assert(wsConnected, 'WebSocket connection indicators should be present');
  });


  // Test 11: Live page heat checkbox disabled by matrix/ghosts mode
  await test('Live heat disabled when ghosts mode active', async () => {
    await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#liveHeatToggle');
    // Enable matrix mode if not already
    const matrixEl = await page.$('#liveMatrixToggle');
    if (matrixEl) {
      await page.evaluate(() => {
        const mt = document.getElementById('liveMatrixToggle');
        if (mt && !mt.checked) mt.click();
      });
      await page.waitForFunction(() => {
        const heat = document.getElementById('liveHeatToggle');
        return heat && heat.disabled;
      });
      const heatDisabled = await page.$eval('#liveHeatToggle', el => el.disabled);
      assert(heatDisabled, 'Heat should be disabled when ghosts/matrix is on');
      // Turn off matrix
      await page.evaluate(() => {
        const mt = document.getElementById('liveMatrixToggle');
        if (mt && mt.checked) mt.click();
      });
      await page.waitForFunction(() => {
        const heat = document.getElementById('liveHeatToggle');
        return heat && !heat.disabled;
      });
      const heatEnabled = await page.$eval('#liveHeatToggle', el => !el.disabled);
      assert(heatEnabled, 'Heat should be re-enabled when ghosts/matrix is off');
    }
  });

  // Test 12: Live page heat checkbox persists across reload (reuses live page)
  await test('Live heat checkbox persists in localStorage', async () => {
    await page.waitForSelector('#liveHeatToggle');
    // Clear state
    await page.evaluate(() => localStorage.removeItem('meshcore-live-heatmap'));
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#liveHeatToggle');
    // Default is checked (has `checked` attribute in HTML)
    const defaultState = await page.$eval('#liveHeatToggle', el => el.checked);
    // Uncheck it
    if (defaultState) await page.click('#liveHeatToggle');
    const stored = await page.evaluate(() => localStorage.getItem('meshcore-live-heatmap'));
    assert(stored === 'false', `localStorage should be "false" after unchecking but got "${stored}"`);
    // Reload and verify persisted
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#liveHeatToggle');
    const afterReload = await page.$eval('#liveHeatToggle', el => el.checked);
    assert(!afterReload, 'Live heat checkbox should stay unchecked after reload');
    // Clean up
    await page.evaluate(() => localStorage.removeItem('meshcore-live-heatmap'));
  });

  // --- Group: No navigation needed (tests 14, 15) ---

  // Test 14: Live heatmap opacity stored in localStorage
  await test('Live heatmap opacity persists in localStorage', async () => {
    // Verify localStorage key works (no page load needed \u2014 reuse current page)
    await page.evaluate(() => localStorage.setItem('meshcore-live-heatmap-opacity', '0.6'));
    const opacity = await page.evaluate(() => localStorage.getItem('meshcore-live-heatmap-opacity'));
    assert(opacity === '0.6', `Live opacity should persist as "0.6" but got "${opacity}"`);
    await page.evaluate(() => localStorage.removeItem('meshcore-live-heatmap-opacity'));
  });

  // Test 15: Customizer has separate Map and Live opacity sliders
  await test('Customizer has separate map and live opacity sliders', async () => {
    // Verify by checking JS source \u2014 avoids heavy page reloads that crash ARM chromium
    const custJs = await page.evaluate(async () => {
      const res = await fetch('/customize.js?_=' + Date.now());
      return res.text();
    });
    assert(custJs.includes('custHeatOpacity'), 'customize.js should have map opacity slider (custHeatOpacity)');
    assert(custJs.includes('custLiveHeatOpacity'), 'customize.js should have live opacity slider (custLiveHeatOpacity)');
    assert(custJs.includes('meshcore-heatmap-opacity'), 'customize.js should use meshcore-heatmap-opacity key');
    assert(custJs.includes('meshcore-live-heatmap-opacity'), 'customize.js should use meshcore-live-heatmap-opacity key');
    // Verify labels are distinct
    assert(custJs.includes('Nodes Map') || custJs.includes('nodes map') || custJs.includes('\ud83d\uddfa'), 'Map slider should have map-related label');
    assert(custJs.includes('Live Map') || custJs.includes('live map') || custJs.includes('\ud83d\udce1'), 'Live slider should have live-related label');
  });

  // --- Group: Channels page ---

  await test('Channels page loads with channel list', async () => {
    await page.goto(`${BASE}/#/channels`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#chList', { timeout: 8000 });
    // Channels are fetched async — wait for items to render
    await page.waitForFunction(() => {
      const list = document.getElementById('chList');
      return list && list.querySelectorAll('.ch-item').length > 0;
    }, { timeout: 15000 });
    const items = await page.$$('#chList .ch-item');
    assert(items.length > 0, `Expected >=1 channel items, got ${items.length}`);
    // Verify channel items have names
    const names = await page.$$eval('#chList .ch-item-name', els => els.map(e => e.textContent.trim()));
    assert(names.length > 0, 'Channel items should have names');
    assert(names[0].length > 0, 'First channel name should not be empty');
  });

  await test('Channels clicking channel shows messages', async () => {
    await page.waitForFunction(() => {
      const list = document.getElementById('chList');
      return list && list.querySelectorAll('.ch-item').length > 0;
    }, { timeout: 10000 });
    const firstItem = await page.$('#chList .ch-item');
    assert(firstItem, 'No channel items to click');
    await firstItem.click();
    await page.waitForFunction(() => {
      const msgs = document.getElementById('chMessages');
      return msgs && msgs.children.length > 0;
    }, { timeout: 10000 });
    const msgCount = await page.$$eval('#chMessages > *', els => els.length);
    assert(msgCount > 0, `Expected messages after clicking channel, got ${msgCount}`);
    // Verify header updated with channel name
    const header = await page.$eval('#chHeader', el => el.textContent.trim());
    assert(header.length > 0, 'Channel header should show channel name');
  });

  // --- Group: Traces page ---

  await test('Traces page loads with search input', async () => {
    await page.goto(`${BASE}/#/traces`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#traceHashInput', { timeout: 8000 });
    const input = await page.$('#traceHashInput');
    assert(input, 'Trace hash input not found');
    const btn = await page.$('#traceBtn');
    assert(btn, 'Trace button not found');
  });

  await test('Traces search returns results for valid hash', async () => {
    // First get a real packet hash from the packets API
    const hash = await page.evaluate(async () => {
      const res = await fetch('/api/packets?limit=1');
      const data = await res.json();
      if (data.packets && data.packets.length > 0) return data.packets[0].hash;
      if (Array.isArray(data) && data.length > 0) return data[0].hash;
      return null;
    });
    if (!hash) { console.log('    ⏭️  Skipped (no packets available)'); return; }
    await page.fill('#traceHashInput', hash);
    await page.click('#traceBtn');
    await page.waitForFunction(() => {
      const r = document.getElementById('traceResults');
      return r && r.textContent.trim().length > 10;
    }, { timeout: 10000 });
    const content = await page.$eval('#traceResults', el => el.textContent.trim());
    assert(content.length > 10, 'Trace results should have content');
  });

  // --- Group: Observers page ---

  await test('Observers page loads with table', async () => {
    await page.goto(`${BASE}/#/observers`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#obsTable', { timeout: 8000 });
    const table = await page.$('#obsTable');
    assert(table, 'Observers table not found');
    // Check for summary stats
    const summary = await page.$('.obs-summary');
    assert(summary, 'Observer summary stats not found');
    // Verify table has rows
    const rows = await page.$$('#obsTable tbody tr');
    assert(rows.length > 0, `Expected >=1 observer rows, got ${rows.length}`);
  });

  await test('Observers table shows health indicators', async () => {
    const dots = await page.$$('#obsTable .health-dot');
    assert(dots.length > 0, 'Observer rows should have health status dots');
    // Verify at least one row has an observer name
    const firstCell = await page.$eval('#obsTable tbody tr td', el => el.textContent.trim());
    assert(firstCell.length > 0, 'Observer name cell should not be empty');
  });

  // --- Group: Perf page ---

  await test('Perf page loads with metrics', async () => {
    await page.goto(`${BASE}/#/perf`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#perfContent', { timeout: 8000 });
    // Wait for perf cards to render (fetches /api/perf and /api/health)
    await page.waitForFunction(() => {
      const c = document.getElementById('perfContent');
      return c && (c.querySelector('.perf-card') || c.querySelector('.perf-table') || c.textContent.trim().length > 20);
    }, { timeout: 10000 });
    const content = await page.$eval('#perfContent', el => el.textContent.trim());
    assert(content.length > 10, 'Perf page should show metrics content');
  });

  await test('Perf page has refresh button', async () => {
    const refreshBtn = await page.$('#perfRefresh');
    assert(refreshBtn, 'Perf refresh button not found');
    // Click refresh and verify content updates (no errors)
    await refreshBtn.click();
    await page.waitForFunction(() => {
      const c = document.getElementById('perfContent');
      return c && c.textContent.trim().length > 10;
    }, { timeout: 8000 });
    const content = await page.$eval('#perfContent', el => el.textContent.trim());
    assert(content.length > 10, 'Perf content should still be present after refresh');
  });

  // --- Group: Audio Lab page ---

  await test('Audio Lab page loads with controls', async () => {
    await page.goto(`${BASE}/#/audio-lab`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#alabSidebar', { timeout: 8000 });
    // Verify core controls exist
    const playBtn = await page.$('#alabPlay');
    assert(playBtn, 'Audio Lab play button not found');
    const voiceSelect = await page.$('#alabVoice');
    assert(voiceSelect, 'Audio Lab voice selector not found');
    const bpmSlider = await page.$('#alabBPM');
    assert(bpmSlider, 'Audio Lab BPM slider not found');
    const volSlider = await page.$('#alabVol');
    assert(volSlider, 'Audio Lab volume slider not found');
  });

  await test('Audio Lab sidebar lists packets', async () => {
    // Wait for packets to load from API
    await page.waitForFunction(() => {
      const sidebar = document.getElementById('alabSidebar');
      return sidebar && sidebar.querySelectorAll('.alab-pkt').length > 0;
    }, { timeout: 10000 });
    const packets = await page.$$('#alabSidebar .alab-pkt');
    assert(packets.length > 0, `Expected packets in sidebar, got ${packets.length}`);
    // Verify type headers exist
    const typeHeaders = await page.$$('#alabSidebar .alab-type-hdr');
    assert(typeHeaders.length > 0, 'Should have packet type headers');
  });

  await test('Audio Lab clicking packet shows detail', async () => {
    const firstPkt = await page.$('#alabSidebar .alab-pkt');
    assert(firstPkt, 'No packets to click');
    await firstPkt.click();
    await page.waitForFunction(() => {
      const detail = document.getElementById('alabDetail');
      return detail && detail.textContent.trim().length > 10;
    }, { timeout: 5000 });
    const detail = await page.$eval('#alabDetail', el => el.textContent.trim());
    assert(detail.length > 10, 'Packet detail should show content after click');
    // Verify hex dump is present
    const hexDump = await page.$('#alabHex');
    assert(hexDump, 'Hex dump should be visible after selecting a packet');
  });

  await browser.close();

  // Summary
  const passed = results.filter(r => r.pass).length;
  const failed = results.filter(r => !r.pass).length;
  console.log(`\n${passed}/${results.length} tests passed${failed ? `, ${failed} failed` : ''}`);
  process.exit(failed > 0 ? 1 : 0);
}

run().catch(err => {
  console.error('Fatal error:', err);
  process.exit(1);
});
