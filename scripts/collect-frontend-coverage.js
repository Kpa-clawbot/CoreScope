// After Playwright tests, this script:
// 1. Connects to the running test server
// 2. Extracts window.__coverage__ from the browser
// 3. Writes it to .nyc_output/ for merging

const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');

async function collectCoverage() {
  const browser = await chromium.launch({
    executablePath: process.env.CHROMIUM_PATH || '/usr/bin/chromium',
    args: ['--no-sandbox'],
    headless: true
  });
  const page = await browser.newPage();
  const BASE = process.env.BASE_URL || 'http://localhost:13581';

  // Visit every major page to exercise the code
  const pages = ['#/home', '#/nodes', '#/map', '#/packets', '#/channels', '#/analytics', '#/live', '#/traces', '#/observers'];
  for (const hash of pages) {
    await page.goto(`${BASE}/${hash}`, { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
    await page.waitForTimeout(2000);
  }

  // Exercise some interactions
  try {
    await page.click('#customizeToggle');
    await page.waitForTimeout(1000);
  } catch {}

  // Extract coverage
  const coverage = await page.evaluate(() => window.__coverage__);
  await browser.close();

  if (coverage) {
    const outDir = path.join(__dirname, '..', '.nyc_output');
    if (!fs.existsSync(outDir)) fs.mkdirSync(outDir, { recursive: true });
    fs.writeFileSync(path.join(outDir, 'frontend-coverage.json'), JSON.stringify(coverage));
    console.log('Frontend coverage collected: ' + Object.keys(coverage).length + ' files');
  } else {
    console.log('WARNING: No __coverage__ object found — instrumentation may have failed');
  }
}

collectCoverage().catch(e => { console.error(e); process.exit(1); });
