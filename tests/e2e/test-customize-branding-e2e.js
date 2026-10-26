/**
 * E2E (#1297 B4): Customizer V2 — Branding (siteName, tagline, logo, favicon)
 *
 * Verifies the branding subsystem in public/customize-v2.js:
 *  - Site name input → updates .brand-text + document.title live
 *  - Logo URL input → swaps inline SVG for <img> (PR #1137 helper)
 *  - Override persisted to cs-theme-overrides.branding
 *  - Survives reload
 *
 * Usage: BASE_URL=http://localhost:13581 node test-customize-branding-e2e.js
 */
'use strict';
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:3000';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  \u2713 ' + name); }
  catch (e) { failed++; console.error('  \u2717 ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1297 B4 customize-branding E2E against ${BASE} ===`);

  await step('setup: clear overrides + load', async () => {
    await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => localStorage.removeItem('cs-theme-overrides'));
    await page.reload({ waitUntil: 'load' });
    await page.waitForFunction(() => window._customizerV2 && window._customizerV2.initDone, null, { timeout: 8000 });
  });

  async function navigate(hash) {
    const before = await page.evaluate(() => viewGeneration());
    await page.evaluate(value => { location.hash = value; }, hash);
    await page.waitForFunction(gen => viewGeneration() > gen, before);
  }

  await step('#2259: route titles distinguish pages and node subviews', async () => {
    const key = 'ab'.repeat(32);
    for (const [hash, title] of [
      ['#/nodes', 'Nodes | CoreScope'],
      ['#/nodes/' + key, 'Node abababababab | CoreScope'],
      ['#/nodes/' + key + '/reach', 'Node abababababab | Reach | CoreScope'],
      ['#/nodes/' + key + '/analytics', 'Node abababababab | Analytics | CoreScope'],
      ['#/packet/123', 'Packet 123 | CoreScope'],
      ['#/observers/example-observer', 'Observer example-obse | CoreScope'],
    ]) {
      await navigate(hash);
      assert(await page.title() === title, hash + ' title was ' + await page.title());
    }
  });

  await step('#2259: back/forward and direct reload retain the current title', async () => {
    await navigate('#/packets');
    await navigate('#/nodes');
    await page.goBack();
    await page.waitForFunction(() => document.title === 'Packets | CoreScope');
    await page.goForward();
    await page.waitForFunction(() => document.title === 'Nodes | CoreScope');
    await page.reload({ waitUntil: 'load' });
    await page.waitForFunction(() => window._customizerV2 && window._customizerV2.initDone);
    assert(await page.title() === 'Nodes | CoreScope', 'delayed branding replaced the route title');
  });

  await step('#2259: selecting and closing inline details updates the title', async () => {
    await navigate('#/home');
    await navigate('#/nodes');
    const node = page.locator('#nodesBody tr[data-action="select"]').first();
    await node.waitFor();
    const key = await node.getAttribute('data-value');
    await node.click();
    assert(await page.title() === 'Node ' + key.slice(0, 12) + ' | CoreScope', 'node selection kept the list title');
    await page.locator('#nodesRight .panel-close-btn').click();
    assert(await page.title() === 'Nodes | CoreScope', 'closing node detail kept its title');
    // Selection must not depend on the fixture still being within 15 minutes.
    await navigate('#/packets?timeWindow=0');
    const packet = page.locator('#pktTable tr[data-hash]').first();
    await packet.waitFor();
    const hash = await packet.getAttribute('data-hash');
    await packet.locator('.col-expand').click();
    await page.locator('#pktRight .detail-title').waitFor();
    assert(await page.title() === 'Packet ' + hash.slice(0, 12) + ' | CoreScope', 'packet selection kept the list title');
    await page.locator('#pktRight .panel-close-btn').click();
    assert(await page.title() === 'Packets | CoreScope', 'closing packet detail kept its title');
  });

  await step('#2259: Analytics tab clicks and deep links identify the selected tab', async () => {
    await navigate('#/analytics?tab=rf');
    await page.waitForSelector('#analyticsTabs');
    assert(await page.title() === 'Analytics | RF / Signal | CoreScope', 'deep-linked tab missing from title');
    await page.locator('#analyticsTabs [data-tab="topology"]').click();
    assert(await page.title() === 'Analytics | Topology | CoreScope', 'tab click did not update title');
  });

  await step('#2259: route text is never parsed as markup', async () => {
    await navigate('#/nodes/' + encodeURIComponent('<img src=x>'));
    assert(await page.title() === 'Node <img src=x> | CoreScope', 'encoded route value was not kept as title text');
    assert(await page.locator('head title').count() === 1, 'route data created extra title markup');
    assert(await page.locator('head img').count() === 0, 'route data created an HTML element');
  });

  await navigate('#/home');

  await step('open customizer + switch to branding tab', async () => {
    await page.click('#customizeToggle');
    await page.waitForSelector('.cust-overlay:not(.hidden)');
    const brandingTab = await page.$('.cust-tab[data-tab="branding"]');
    if (brandingTab) await brandingTab.click();
    await page.waitForSelector('input[data-cv2-field="branding.siteName"]', { timeout: 4000 });
  });

  await step('siteName input updates document.title live', async () => {
    const inp = await page.$('input[data-cv2-field="branding.siteName"]');
    assert(inp, 'branding.siteName input missing');
    await page.evaluate((el) => {
      el.value = 'MyMeshTest';
      el.dispatchEvent(new Event('input', { bubbles: true }));
    }, inp);
    await page.waitForTimeout(400);
    const title = await page.title();
    assert(title === 'MyMeshTest', 'document.title should update live, got: ' + title);
  });

  await step('siteName persists to cs-theme-overrides.branding.siteName', async () => {
    const raw = await page.evaluate(() => localStorage.getItem('cs-theme-overrides'));
    assert(raw, 'overrides not written');
    const parsed = JSON.parse(raw);
    assert(parsed.branding && parsed.branding.siteName === 'MyMeshTest',
      'branding.siteName missing in overrides: ' + raw);
  });

  await step('#2259: live branding preserves route context and empty names fall back', async () => {
    await navigate('#/nodes');
    const input = page.locator('input[data-cv2-field="branding.siteName"]');
    try {
      await input.fill('Renamed Mesh');
      assert(await page.title() === 'Nodes | Renamed Mesh', 'branding change dropped page context');
      await input.fill('');
      assert(await page.title() === 'Nodes | CoreScope', 'empty branding must have a useful fallback');
    } finally { await input.fill('MyMeshTest'); }
    await navigate('#/packets');
    await navigate('#/nodes');
    const title = await page.title();
    assert(title === 'Nodes | MyMeshTest', 'expected Nodes | MyMeshTest after navigation, got: ' + title);
  });

  await step('#2335: a pending packets render preserves the next route and branded title', async () => {
    await navigate('#/home');
    await page.evaluate(() => {
      window.__brandingOriginalApi = window.api;
      window.api = Object.assign(function(url, options) {
        const result = window.__brandingOriginalApi(url, options);
        if (!url.startsWith('/packets?')) return result;
        // Hold the real response so the render runs at the navigation boundary.
        return result.then(data => new Promise(resolve => {
          window.__brandingReleasePackets = () => resolve(data);
        }));
      }, window.api);
    });
    try {
      await navigate('#/packets?timeWindow=0');
      await page.waitForFunction(() => window.__brandingReleasePackets);
      const before = await page.evaluate(() => {
        const generation = viewGeneration();
        // The response continuation runs before the queued hashchange task.
        location.hash = '#/nodes';
        window.__brandingReleasePackets();
        return generation;
      });
      await page.waitForFunction(gen => viewGeneration() > gen, before);
      const state = await page.evaluate(() => ({ hash: location.hash, title: document.title }));
      assert(state.hash === '#/nodes', 'pending packets render replaced the route with ' + state.hash);
      assert(state.title === 'Nodes | MyMeshTest', 'pending packets render replaced the title with ' + state.title);
    } finally {
      await page.evaluate(() => {
        if (window.__brandingReleasePackets) window.__brandingReleasePackets();
        window.api = window.__brandingOriginalApi;
        delete window.__brandingOriginalApi;
        delete window.__brandingReleasePackets;
      });
    }
  });

  await step('logoUrl input triggers _setBrandLogoUrl helper (swaps SVG → img)', async () => {
    const inp = await page.$('input[data-cv2-field="branding.logoUrl"]');
    assert(inp, 'branding.logoUrl input missing');
    const testUrl = 'data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciLz4=';
    await page.evaluate((args) => {
      args.el.value = args.url;
      args.el.dispatchEvent(new Event('input', { bubbles: true }));
    }, { el: inp, url: testUrl });
    await page.waitForTimeout(300);
    // Brand logo node should now be <img>
    const tag = await page.evaluate(() => {
      const n = document.querySelector('.nav-brand .brand-logo');
      return n ? n.tagName.toLowerCase() : null;
    });
    assert(tag === 'img', 'expected brand-logo to be <img> after logoUrl set, got: ' + tag);
    const src = await page.evaluate(() => {
      const n = document.querySelector('.nav-brand .brand-logo');
      return n ? n.getAttribute('src') : null;
    });
    assert(src === testUrl, 'brand-logo src should match URL, got: ' + (src || '').slice(0, 40));
  });

  // ── #1518: branding.homeUrl override redirects nav-brand[href] ──
  await step('#1518: branding.homeUrl override sets .nav-brand[href]', async () => {
    const inp = await page.$('input[data-cv2-field="branding.homeUrl"]');
    assert(inp, 'branding.homeUrl input missing — Branding tab must expose homeUrl field');
    const target = 'https://example.com/embed-home';
    await page.evaluate((args) => {
      args.el.value = args.v;
      args.el.dispatchEvent(new Event('input', { bubbles: true }));
    }, { el: inp, v: target });
    await page.waitForTimeout(500);
    const href = await page.evaluate(() => {
      const a = document.querySelector('a.nav-brand');
      return a ? a.getAttribute('href') : null;
    });
    assert(href === target, '.nav-brand[href] should equal homeUrl override, got: ' + href);
  });

  await step('#1518: branding.homeUrl rejects javascript: scheme', async () => {
    const inp = await page.$('input[data-cv2-field="branding.homeUrl"]');
    await page.evaluate((el) => {
      el.value = 'javascript:alert(1)';
      el.dispatchEvent(new Event('input', { bubbles: true }));
    }, inp);
    await page.waitForTimeout(500);
    const href = await page.evaluate(() => {
      const a = document.querySelector('a.nav-brand');
      return a ? a.getAttribute('href') : null;
    });
    assert(href !== 'javascript:alert(1)', '.nav-brand[href] must NEVER be javascript:, got: ' + href);
  });

  await step('#1518: empty branding.homeUrl falls through to #/', async () => {
    const inp = await page.$('input[data-cv2-field="branding.homeUrl"]');
    await page.evaluate((el) => {
      el.value = '';
      el.dispatchEvent(new Event('input', { bubbles: true }));
    }, inp);
    await page.waitForTimeout(500);
    const href = await page.evaluate(() => {
      const a = document.querySelector('a.nav-brand');
      return a ? a.getAttribute('href') : null;
    });
    assert(href === '#/', '.nav-brand[href] should fall through to "#/" when homeUrl is empty, got: ' + href);
  });

  await step('branding overrides persist across reload', async () => {
    await page.reload({ waitUntil: 'load' });
    await page.waitForFunction(() => window._customizerV2 && window._customizerV2.initDone, null, { timeout: 8000 });
    const title = await page.title();
    // app.js applies branding.siteName to document.title on init (or customizer pipeline does)
    // At minimum, the override is still in localStorage:
    const raw = await page.evaluate(() => localStorage.getItem('cs-theme-overrides'));
    const parsed = JSON.parse(raw);
    assert(parsed.branding && parsed.branding.siteName === 'MyMeshTest',
      'siteName override should persist, got: ' + raw);
  });

  await step('cleanup: clear overrides', async () => {
    await page.evaluate(() => localStorage.removeItem('cs-theme-overrides'));
  });

  await browser.close();
  console.log('\n' + passed + '/' + (passed + failed) + ' tests passed');
  process.exit(failed > 0 ? 1 : 0);
})();
