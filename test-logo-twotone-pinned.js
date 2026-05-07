// E2E: assert logo wordmark renders in pinned brand duotone (sage/teal)
// regardless of customizer accent settings.
const { chromium } = require('playwright');
const assert = require('assert');

(async () => {
  const browser = await chromium.launch();
  try {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    const page = await ctx.newPage();
    await page.goto('http://localhost:8080/', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.brand-logo', { timeout: 5000 });

    // Override accent vars to RED to prove the logo doesn't follow customizer
    await page.evaluate(() => {
      document.documentElement.style.setProperty('--accent', '#ff0000');
      document.documentElement.style.setProperty('--accent-hover', '#aa0000');
    });
    await page.waitForTimeout(100);

    const fills = await page.evaluate(() => {
      const texts = document.querySelectorAll('.brand-logo text');
      const arcs = document.querySelectorAll('.brand-logo path');
      return {
        coreFill: window.getComputedStyle(texts[0]).fill,
        scopeFill: window.getComputedStyle(texts[1]).fill,
        leftArcStroke: window.getComputedStyle(arcs[0]).stroke,
        rightArcStroke: window.getComputedStyle(arcs[4]).stroke,
      };
    });
    console.log('Computed fills after --accent=red override:', JSON.stringify(fills));
    // Expected: sage-ish (#cfd9c9 = rgb(207,217,201)) and teal (#2c8c8c = rgb(44,140,140))
    assert.strictEqual(fills.coreFill, 'rgb(207, 217, 201)', `CORE expected sage rgb(207,217,201), got ${fills.coreFill}`);
    assert.strictEqual(fills.scopeFill, 'rgb(44, 140, 140)', `SCOPE expected teal rgb(44,140,140), got ${fills.scopeFill}`);
    assert.strictEqual(fills.leftArcStroke, 'rgb(207, 217, 201)', `Left arc expected sage, got ${fills.leftArcStroke}`);
    assert.strictEqual(fills.rightArcStroke, 'rgb(44, 140, 140)', `Right arc expected teal, got ${fills.rightArcStroke}`);
    console.log('OK — duotone pinned regardless of customizer accent');
  } finally {
    await browser.close();
  }
})().catch(e => { console.error(e.message); process.exit(1); });
