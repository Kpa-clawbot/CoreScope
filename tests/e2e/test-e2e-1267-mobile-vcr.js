/**
 * #1267 — VCR bar invisible on mobile /live (iOS Safari ~375x812).
 *
 * RED first: at a 375x812 mobile viewport, the `.vcr-bar` must be visible
 * between the map and bottom-nav. Asserts measured height > 0, display !==
 * 'none', visibility !== 'hidden', and that its top edge is within the
 * viewport (not pushed below the visible area).
 *
 * Usage: BASE_URL=http://localhost:13581 node test-e2e-1267-mobile-vcr.js
 */
const { chromium, devices } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || '/usr/bin/chromium',
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage']
  });
  const context = await browser.newContext({
    viewport: { width: 375, height: 812 },
    deviceScaleFactor: 2,
    isMobile: true,
    hasTouch: true,
    userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1',
  });
  const page = await context.newPage();
  page.setDefaultTimeout(15000);

  let failed = false;
  function fail(msg) { failed = true; console.log('  \u274c', msg); }
  function pass(msg) { console.log('  \u2705', msg); }

  console.log(`\n#1267 mobile VCR-bar visibility against ${BASE} (375x812)\n`);

  await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
  // Allow live page mount + initial render + VCR bar ResizeObserver publish.
  await page.waitForSelector('.live-page', { timeout: 15000 });
  await page.waitForSelector('#vcrBar', { timeout: 15000 });
  // Wait until markers populate — #1267 only manifests after marker render.
  // We poll for >=1 leaflet-marker-icon to appear in the DOM, then settle.
  await page.waitForFunction(
    () => document.querySelectorAll('#liveMap .leaflet-marker-icon, #liveMap .leaflet-marker-pane > *').length > 0,
    null,
    { timeout: 15000 }
  ).catch(() => {});
  await page.waitForTimeout(4000);

  const info = await page.evaluate(() => {
    const bar = document.getElementById('vcrBar');
    if (!bar) return { missing: true };
    const r = bar.getBoundingClientRect();
    const cs = getComputedStyle(bar);
    const page = document.querySelector('.live-page');
    const pageR = page ? page.getBoundingClientRect() : null;
    const bn = document.querySelector('.bottom-nav');
    const bnR = bn ? bn.getBoundingClientRect() : null;
    const bnCs = bn ? getComputedStyle(bn) : null;
    const rootCs = getComputedStyle(document.documentElement);
    return {
      rect: { top: r.top, bottom: r.bottom, height: r.height, width: r.width, left: r.left, right: r.right },
      display: cs.display,
      visibility: cs.visibility,
      opacity: cs.opacity,
      position: cs.position,
      zIndex: cs.zIndex,
      viewportH: window.innerHeight,
      viewportW: window.innerWidth,
      pageRect: pageR ? { top: pageR.top, bottom: pageR.bottom, height: pageR.height } : null,
      bottomNav: bnR ? { top: bnR.top, bottom: bnR.bottom, height: bnR.height, display: bnCs.display } : null,
      bottomNavReserve: rootCs.getPropertyValue('--bottom-nav-reserve'),
      vcrBarHeightVar: getComputedStyle(page || document.body).getPropertyValue('--vcr-bar-height'),
    };
  });

  console.log('VCR bar measurement:', JSON.stringify(info, null, 2));

  if (info.missing) {
    fail('#vcrBar element not in DOM');
  } else {
    if (info.display === 'none') fail(`display:none on .vcr-bar`);
    else pass(`display is ${info.display}`);

    if (info.visibility === 'hidden') fail(`visibility:hidden on .vcr-bar`);
    else pass(`visibility is ${info.visibility}`);

    if (info.rect.height <= 0) fail(`getBoundingClientRect().height = ${info.rect.height} (expected > 0)`);
    else pass(`height ${info.rect.height}px > 0`);

    if (info.rect.top >= info.viewportH) fail(`bar top ${info.rect.top} >= viewport height ${info.viewportH} (pushed off-screen)`);
    else pass(`bar top ${info.rect.top} < viewport height ${info.viewportH}`);

    // #1267 root assertion: the VCR bar must not be occluded by the
    // fixed bottom-nav (z=1200 > vcr-bar z=1000). The bar's bottom edge
    // must sit AT OR ABOVE the bottom-nav's top edge.
    if (info.bottomNav && info.bottomNav.display !== 'none') {
      if (info.rect.bottom > info.bottomNav.top + 0.5) {
        fail(`VCR bar bottom ${info.rect.bottom} overlaps bottom-nav top ${info.bottomNav.top} (hidden behind bottom-nav — #1267)`);
      } else {
        pass(`VCR bar bottom ${info.rect.bottom} ≤ bottom-nav top ${info.bottomNav.top}`);
      }
    }

    // Sanity: the bar should occupy width across most of the viewport.
    if (info.rect.width < info.viewportW * 0.5) fail(`bar width ${info.rect.width} < 50% of viewport ${info.viewportW}`);
    else pass(`bar width ${info.rect.width} spans >50% viewport`);
  }

  await browser.close();
  process.exit(failed ? 1 : 0);
})().catch(err => { console.error(err); process.exit(2); });
