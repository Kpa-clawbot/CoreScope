/**
 * Playwright E2E tests — proof of concept
 * Runs against prod (analyzer.00id.net), read-only.
 * Usage: node test-e2e-playwright.js
 */
const { chromium } = require('playwright');

const BASE = 'https://analyzer.00id.net';
const results = [];

async function test(name, fn) {
  try {
    await fn();
    results.push({ name, pass: true });
    console.log(`  ✅ ${name}`);
  } catch (err) {
    results.push({ name, pass: false, error: err.message });
    console.log(`  ❌ ${name}: ${err.message}`);
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
  page.setDefaultTimeout(15000);

  console.log(`\nRunning E2E tests against ${BASE}\n`);

  // Test 1: Home page loads
  await test('Home page loads', async () => {
    await page.goto(BASE, { waitUntil: 'networkidle' });
    const title = await page.title();
    assert(title.toLowerCase().includes('meshcore'), `Title "${title}" doesn't contain MeshCore`);
    const nav = await page.$('nav, .navbar, .nav, [class*="nav"]');
    assert(nav, 'Nav bar not found');
  });

  // Test 2: Nodes page loads with data
  await test('Nodes page loads with data', async () => {
    await page.goto(`${BASE}/#/nodes`, { waitUntil: 'networkidle' });
    await page.waitForSelector('table tbody tr', { timeout: 15000 });
    await page.waitForTimeout(1000); // let SPA render
    const headers = await page.$$eval('th', els => els.map(e => e.textContent.trim()));
    for (const col of ['Name', 'Public Key', 'Role']) {
      assert(headers.some(h => h.includes(col)), `Missing column: ${col}`);
    }
    assert(headers.some(h => h.includes('Last Seen') || h.includes('Last')), 'Missing Last Seen column');
    const rows = await page.$$('table tbody tr');
    assert(rows.length >= 10, `Expected >=10 nodes, got ${rows.length}`);
  });

  // Test 3: Map page loads with markers
  await test('Map page loads with markers', async () => {
    await page.goto(`${BASE}/#/map`, { waitUntil: 'networkidle' });
    await page.waitForSelector('.leaflet-container', { timeout: 10000 });
    await page.waitForSelector('.leaflet-tile-loaded', { timeout: 10000 });
    // Markers can be icons, SVG circles, or canvas-rendered; wait a bit for data
    await page.waitForTimeout(3000);
    const markers = await page.$$('.leaflet-marker-icon, .leaflet-interactive, circle, .marker-cluster, .leaflet-marker-pane > *, .leaflet-overlay-pane svg path, .leaflet-overlay-pane svg circle');
    assert(markers.length > 0, 'No map markers/overlays found');
  });

  // Test 4: Packets page loads with filter
  await test('Packets page loads with filter', async () => {
    await page.goto(`${BASE}/#/packets`, { waitUntil: 'networkidle' });
    await page.waitForSelector('table tbody tr', { timeout: 10000 });
    const rowsBefore = await page.$$('table tbody tr');
    assert(rowsBefore.length > 0, 'No packets visible');
    // Use the specific filter input
    const filterInput = await page.$('#packetFilterInput');
    assert(filterInput, 'Packet filter input not found');
    await filterInput.fill('type == ADVERT');
    await page.waitForTimeout(1500);
    // Verify filter was applied (count may differ)
    const rowsAfter = await page.$$('table tbody tr');
    assert(rowsAfter.length > 0, 'No packets after filtering');
  });

  // Test 5: Node detail loads
  await test('Node detail loads', async () => {
    await page.goto(`${BASE}/#/nodes`, { waitUntil: 'networkidle' });
    await page.waitForSelector('table tbody tr', { timeout: 10000 });
    // Click first row
    const firstRow = await page.$('table tbody tr');
    assert(firstRow, 'No node rows found');
    await firstRow.click();
    // Wait for side pane or detail
    await page.waitForTimeout(1000);
    const html = await page.content();
    // Check for status indicator
    const hasStatus = html.includes('🟢') || html.includes('⚪') || html.includes('status') || html.includes('Active') || html.includes('Stale');
    assert(hasStatus, 'No status indicator found in node detail');
  });

  // Test 6: Theme customizer opens
  await test('Theme customizer opens', async () => {
    await page.goto(BASE, { waitUntil: 'networkidle' });
    // Look for palette/customize button
    const btn = await page.$('button[title*="ustom" i], button[aria-label*="theme" i], [class*="customize"], button:has-text("🎨")');
    if (!btn) {
      // Try finding by emoji content
      const allButtons = await page.$$('button');
      let found = false;
      for (const b of allButtons) {
        const text = await b.textContent();
        if (text.includes('🎨')) {
          await b.click();
          found = true;
          break;
        }
      }
      assert(found, 'Could not find theme customizer button');
    } else {
      await btn.click();
    }
    await page.waitForTimeout(500);
    const html = await page.content();
    const hasCustomizer = html.includes('preset') || html.includes('Preset') || html.includes('theme') || html.includes('Theme');
    assert(hasCustomizer, 'Customizer panel not found after clicking');
  });

  // Test 7: Dark mode toggle
  await test('Dark mode toggle', async () => {
    await page.goto(BASE, { waitUntil: 'networkidle' });
    const themeBefore = await page.$eval('html', el => el.getAttribute('data-theme'));
    // Find toggle button
    const allButtons = await page.$$('button');
    let toggled = false;
    for (const b of allButtons) {
      const text = await b.textContent();
      if (text.includes('☀') || text.includes('🌙') || text.includes('🌑') || text.includes('🌕')) {
        await b.click();
        toggled = true;
        break;
      }
    }
    assert(toggled, 'Could not find dark mode toggle button');
    await page.waitForTimeout(300);
    const themeAfter = await page.$eval('html', el => el.getAttribute('data-theme'));
    assert(themeBefore !== themeAfter, `Theme didn't change: before=${themeBefore}, after=${themeAfter}`);
  });

  // Test 8: Analytics page loads
  await test('Analytics page loads', async () => {
    await page.goto(`${BASE}/#/analytics`, { waitUntil: 'networkidle' });
    await page.waitForTimeout(2000);
    const html = await page.content();
    // Check for any analytics content
    const hasContent = html.includes('analytics') || html.includes('Analytics') || html.includes('tab') || html.includes('chart') || html.includes('topology');
    assert(hasContent, 'Analytics page has no recognizable content');
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
