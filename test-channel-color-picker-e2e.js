/**
 * #1297 B2 — Coverage E2E for public/channel-color-picker.js
 *
 * Exercises the picker popover by driving its public API
 * (window.ChannelColorPicker.show / .hide) on the /#/channels page and
 * asserting:
 *   - palette renders all 8 swatches
 *   - clicking a swatch writes ChannelColors.set + updates .ch-color-dot
 *   - Escape closes popover
 *   - keyboard ArrowRight cycles focus across swatches
 *   - Clear button removes the assignment (when one exists)
 *   - active-class highlights the currently assigned color
 *
 * Usage: BASE_URL=http://localhost:13581 node test-channel-color-picker-e2e.js
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

  console.log('\n=== #1297 B2 channel-color-picker E2E against ' + BASE + ' ===');

  // Bootstrap: load page, clear storage
  await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#chList', { timeout: 8000 });
  await page.evaluate(() => {
    try { localStorage.removeItem('live-channel-colors'); } catch (e) {}
  });

  await step('window.ChannelColorPicker is loaded with PALETTE', async () => {
    const palette = await page.evaluate(() =>
      window.ChannelColorPicker && window.ChannelColorPicker.PALETTE);
    assert(Array.isArray(palette), 'PALETTE missing');
    assert(palette.length === 8, 'expected 8 colors, got ' + palette.length);
    assert(palette[0] === '#ef4444', 'first color should be #ef4444, got ' + palette[0]);
  });

  await step('show() opens popover with 8 swatches', async () => {
    await page.evaluate(() =>
      window.ChannelColorPicker.show('#testchan', 100, 100));
    await page.waitForSelector('.cc-picker-popover', { timeout: 3000 });
    const swatchCount = await page.$$eval('.cc-swatch', els => els.length);
    assert(swatchCount === 8, 'expected 8 swatches, got ' + swatchCount);
    const visible = await page.evaluate(() => {
      const el = document.querySelector('.cc-picker-popover');
      return el && el.style.display !== 'none';
    });
    assert(visible, 'popover should be visible after show()');
  });

  await step('Escape closes popover', async () => {
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => {
      const el = document.querySelector('.cc-picker-popover');
      return el && el.style.display === 'none';
    }, { timeout: 3000 });
  });

  await step('clicking a swatch writes ChannelColors + closes popover', async () => {
    await page.evaluate(() =>
      window.ChannelColorPicker.show('#myroom', 100, 100));
    await page.waitForSelector('.cc-picker-popover');
    // Click the green swatch (#22c55e)
    await page.click('.cc-swatch[data-color="#22c55e"]');
    // Popover should hide
    await page.waitForFunction(() => {
      const el = document.querySelector('.cc-picker-popover');
      return el && el.style.display === 'none';
    }, { timeout: 3000 });
    const stored = await page.evaluate(() =>
      window.ChannelColors && window.ChannelColors.get('#myroom'));
    assert(stored === '#22c55e', 'expected stored color #22c55e, got ' + stored);
    const raw = await page.evaluate(() =>
      localStorage.getItem('live-channel-colors'));
    assert(raw && raw.indexOf('#22c55e') >= 0,
      'expected localStorage live-channel-colors to contain #22c55e, got: ' + raw);
  });

  await step('reopening picker for assigned channel marks active swatch + shows Clear', async () => {
    await page.evaluate(() =>
      window.ChannelColorPicker.show('#myroom', 100, 100));
    await page.waitForSelector('.cc-picker-popover');
    const activeColor = await page.$eval('.cc-swatch.cc-swatch-active',
      el => el.getAttribute('data-color'));
    assert(activeColor === '#22c55e',
      'expected active swatch #22c55e, got ' + activeColor);
    const clearVisible = await page.evaluate(() => {
      const b = document.querySelector('.cc-picker-clear');
      return b && b.style.display !== 'none';
    });
    assert(clearVisible, 'Clear button should be visible when color is assigned');
  });

  await step('Clear button removes the channel color', async () => {
    await page.click('.cc-picker-clear');
    await page.waitForFunction(() => {
      const el = document.querySelector('.cc-picker-popover');
      return el && el.style.display === 'none';
    }, { timeout: 3000 });
    const stored = await page.evaluate(() =>
      window.ChannelColors && window.ChannelColors.get('#myroom'));
    assert(stored == null,
      'expected color cleared, got ' + JSON.stringify(stored));
  });

  await step('Clear button is hidden when no color assigned', async () => {
    await page.evaluate(() =>
      window.ChannelColorPicker.show('#freshchan', 100, 100));
    await page.waitForSelector('.cc-picker-popover');
    const clearHidden = await page.evaluate(() => {
      const b = document.querySelector('.cc-picker-clear');
      return b && b.style.display === 'none';
    });
    assert(clearHidden, 'Clear button should be hidden when no color assigned');
    await page.keyboard.press('Escape');
  });

  await step('ArrowRight cycles focus across swatches', async () => {
    await page.evaluate(() =>
      window.ChannelColorPicker.show('#navchan', 100, 100));
    await page.waitForSelector('.cc-picker-popover');
    // Wait a tick for setTimeout(0) focus
    await page.waitForFunction(() => {
      const el = document.activeElement;
      return el && el.classList && el.classList.contains('cc-swatch');
    }, { timeout: 2000 });
    const firstColor = await page.evaluate(() =>
      document.activeElement.getAttribute('data-color'));
    await page.keyboard.press('ArrowRight');
    const nextColor = await page.evaluate(() =>
      document.activeElement.getAttribute('data-color'));
    assert(nextColor && nextColor !== firstColor,
      'ArrowRight should move focus to next swatch (was ' + firstColor + ', now ' + nextColor + ')');
    // Enter to assign + close
    await page.keyboard.press('Enter');
    await page.waitForFunction(() => {
      const el = document.querySelector('.cc-picker-popover');
      return el && el.style.display === 'none';
    }, { timeout: 3000 });
    const stored = await page.evaluate(() =>
      window.ChannelColors && window.ChannelColors.get('#navchan'));
    assert(stored === nextColor,
      'Enter should assign focused color (' + nextColor + '), got ' + stored);
  });

  await step('outside click closes popover', async () => {
    await page.evaluate(() =>
      window.ChannelColorPicker.show('#outsidechan', 100, 100));
    await page.waitForSelector('.cc-picker-popover');
    // Click body far away
    await page.evaluate(() => {
      document.body.click();
    });
    await page.waitForFunction(() => {
      const el = document.querySelector('.cc-picker-popover');
      return el && el.style.display === 'none';
    }, { timeout: 3000 });
  });

  // Cleanup
  await page.evaluate(() => {
    try { localStorage.removeItem('live-channel-colors'); } catch (e) {}
  });

  console.log('\n=== Results: passed ' + passed + ' failed ' + failed + ' ===');
  await browser.close();
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error('FATAL:', e); process.exit(1); });
