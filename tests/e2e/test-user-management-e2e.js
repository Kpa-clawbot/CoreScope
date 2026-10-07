/**
 * E2E: optional user management (docs/specs/2026-10-06-user-management-design.md).
 *   BASE_URL      server with userManagement on + fake mailer (-tags e2etest build)
 *   BASE_URL_OFF  the regular fixture server (feature off)
 *
 * Local run (never point the servers at the tracked fixture; migrate a copy):
 *   cp test-fixtures/e2e-fixture.db "$TMP/on.db"; cp test-fixtures/e2e-fixture.db "$TMP/off.db"
 *   corescope-migrate -db "$TMP/on.db"; corescope-migrate -db "$TMP/off.db"
 *   (cd cmd/server && go build -o ../../corescope-server . && go build -tags e2etest -o ../../corescope-server-e2e .)
 *   # config.json for the on-server (in $CFGDIR): port 13582, userManagement {enabled: true,
 *   #   dbPath "users.db", adminEmails ["admin@e2e.test"], publicBaseUrl "http://localhost:13582",
 *   #   mail {provider "fake", fromEmail "noreply@e2e.test"}}
 *   corescope-server -port 13581 -db "$TMP/off.db" -public public &
 *   (cd "$CFGDIR" && corescope-server-e2e -config-dir . -port 13582 -db "$TMP/on.db" -public <repo>/public) &
 *   BASE_URL=http://localhost:13582 BASE_URL_OFF=http://localhost:13581 node tests/e2e/test-user-management-e2e.js
 * Set CHROMIUM_PATH to a Chrome/Chromium binary if Playwright's own is not installed.
 */
'use strict';
const { chromium } = require('playwright');
const { AxeBuilder } = require('@axe-core/playwright');
const BASE = process.env.BASE_URL || 'http://localhost:13582';
const BASE_OFF = process.env.BASE_URL_OFF || 'http://localhost:13581';
const PW = 'correct horse battery';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

// Resolves once auth.js has finished its first /api/auth/me round trip.
async function authReady(page) {
  await page.waitForFunction(() => !!window.CSAuth);
  await page.evaluate(() => window.CSAuth.ready());
}

async function lastMailToken(page) {
  const r = await page.request.get(BASE + '/__e2e/last-mail');
  assert(r.ok(), 'last-mail HTTP ' + r.status());
  const m = await r.json();
  const sm = /token=([A-Za-z0-9_%-]+)/.exec(m.text);
  assert(sm, 'no token in mail text: ' + m.text);
  return { to: m.to, token: decodeURIComponent(sm[1]) };
}

async function registerAndActivate(page, email, name) {
  await page.goto(BASE + '/#/account/register', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#registerForm');
  await page.fill('#regEmail', email);
  await page.fill('#regName', name);
  await page.fill('#regPassword', PW);
  await page.click('#registerForm button[type="submit"]');
  await page.waitForSelector('#mailSentHeading');
  const { to, token } = await lastMailToken(page);
  assert(to === email, 'activation mail went to ' + to);
  await page.goto(BASE + '/#/account/activate?token=' + encodeURIComponent(token));
  await page.waitForSelector('#activateForm');
  await page.fill('#actPassword', PW);
  await page.click('#activateForm button[type="submit"]');
  await page.waitForSelector('#accountToggle .nav-account-label:has-text("' + name + '")');
}

// axeClean fails on serious or critical WCAG 2 A/AA violations inside sel.
async function axeClean(pg, sel) {
  const res = await new AxeBuilder({ page: pg }).include(sel).withTags(['wcag2a', 'wcag2aa']).analyze();
  const bad = res.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
  assert(bad.length === 0, sel + ': ' + bad.map((v) => v.id + ' ' + v.nodes.map((n) => n.target.join(' ') + ' ' + ((n.any[0] || {}).message || '')).join(' | ')).join(', '));
}

// The account's synced keys, read through the page's own session.
async function accountKeys(pg) {
  return pg.evaluate(() => window.CSAuth.request('GET', '/api/account/settings').then((r) => (r.data.doc && r.data.doc.keys) || {}));
}

async function until(fn, label) {
  const end = Date.now() + 8000;
  for (;;) {
    if (await fn()) return;
    if (Date.now() > end) throw new Error('timed out: ' + label);
    await new Promise((r) => setTimeout(r, 200));
  }
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  console.log(`\n=== user management E2E against ${BASE} (off: ${BASE_OFF}) ===`);

  const off = await (await browser.newContext()).newPage();
  off.setDefaultTimeout(8000);
  await step('feature off: no account control and no auth API', async () => {
    await off.goto(BASE_OFF + '/', { waitUntil: 'domcontentloaded' });
    await authReady(off);
    assert(await off.locator('#accountToggle').count() === 0, 'account control rendered while off');
    assert(await off.evaluate(() => !window.CSAuth.isEnabled()), 'CSAuth enabled while off');
    // Unknown /api paths fall through to the SPA page (200 HTML): only JSON with a csrfToken would be a leak.
    const r = await off.request.get(BASE_OFF + '/api/auth/me');
    let body = null;
    try { body = await r.json(); } catch (_) { /* HTML, expected */ }
    assert(!(body && body.csrfToken), '/api/auth/me answered a session while off');
  });

  const admin = await (await browser.newContext()).newPage();
  admin.setDefaultTimeout(8000);
  admin.on('dialog', (d) => d.accept());
  admin.on('pageerror', (e) => console.error('[pageerror admin]', e.message));
  await step('config admin registers, activates with a password, sees the Admin entry', async () => {
    await registerAndActivate(admin, 'admin@e2e.test', 'E2E Admin');
    await admin.click('#accountToggle');
    assert(await admin.locator('#accountMenu a[href="#/admin"]').isVisible(), 'no Admin menu entry');
  });

  const user = await (await browser.newContext()).newPage();
  user.setDefaultTimeout(8000);
  user.on('pageerror', (e) => console.error('[pageerror user]', e.message));
  await step('second user registers and activates; no Admin entry for a non-admin', async () => {
    await registerAndActivate(user, 'user@e2e.test', 'E2E User');
    await user.click('#accountToggle');
    assert(await user.locator('#accountMenu').isVisible(), 'account menu did not open');
    assert(await user.locator('#accountMenu a[href="#/admin"]').count() === 0, 'non-admin sees Admin');
  });

  await step('user logs out from the account page (phone width) and logs in again', async () => {
    // At phone width the header control is hidden: the page button is the only way out.
    await user.setViewportSize({ width: 375, height: 800 });
    await user.goto(BASE + '/#/account');
    await user.waitForSelector('#profileForm');
    await user.click('#accountPageLogout');
    // Settings sync is active for a logged-in user: logout asks keep or remove.
    await user.click('.cs-dialog [data-choice="keep"]');
    await user.waitForSelector('#loginForm');
    assert(await user.evaluate(() => location.hash) === '#/account/login', 'not on the login view after logout');
    assert(await user.evaluate(() => window.CS_USER === null), 'client still holds the user');
    await user.setViewportSize({ width: 1280, height: 720 });
    await user.waitForSelector('#accountToggle .nav-account-label:has-text("Log in")');
    await user.fill('#loginEmail', 'user@e2e.test');
    await user.fill('#loginPassword', PW);
    await user.click('#loginForm button[type="submit"]');
    await user.waitForSelector('#profileForm');
    await user.waitForSelector('#accountToggle .nav-account-label:has-text("E2E User")');
  });

  // Settings sync: two browser contexts are two devices on one account.
  const SYNC_FAV = 'e2e5e7c0000000000000000000000000000000000000000000000000000000a1';
  const d1 = await (await browser.newContext()).newPage();
  const d2 = await (await browser.newContext()).newPage();
  for (const [pg, tag] of [[d1, 'd1'], [d2, 'd2']]) {
    pg.setDefaultTimeout(8000);
    pg.on('pageerror', (e) => console.error('[pageerror ' + tag + ']', e.message));
  }

  await step('settings sync: device 1 saves a packet time window and a favorite to the account', async () => {
    await registerAndActivate(d1, 'sync@e2e.test', 'E2E Sync');
    await d1.goto(BASE + '/#/packets');
    await d1.waitForSelector('#fTimeWindow');
    await d1.selectOption('#fTimeWindow', '180');
    // No favorite star without node rows in view: write the key as nodes.js does.
    await d1.evaluate((pk) => localStorage.setItem('meshcore-favorites', JSON.stringify([pk])), SYNC_FAV);
    await until(async () => {
      const k = await accountKeys(d1);
      return k['meshcore-time-window'] === '180' && (k['meshcore-favorites'] || '').includes(SYNC_FAV);
    }, 'account holds the time window and the favorite');
  });

  await step('settings sync: device 2 logs in and gets both; its channel key stays local', async () => {
    await d2.goto(BASE + '/#/account/login', { waitUntil: 'domcontentloaded' });
    await d2.waitForSelector('#loginForm');
    await d2.evaluate(() => localStorage.setItem('corescope_channel_keys', JSON.stringify({ '#e2e': '00112233445566778899aabbccddeeff' })));
    await d2.fill('#loginEmail', 'sync@e2e.test');
    await d2.fill('#loginPassword', PW);
    await d2.click('#loginForm button[type="submit"]');
    await d2.waitForSelector('#profileForm');
    await d2.waitForFunction((pk) => (localStorage.getItem('meshcore-favorites') || '').includes(pk) &&
      localStorage.getItem('meshcore-time-window') === '180', SYNC_FAV);
    // Check after a full round trip from device 2 (pull, merge, push), not
    // just after its first pull.
    await d2.evaluate(() => window.CSSettingsSync.syncNow());
    const k = await accountKeys(d2);
    assert(!('corescope_channel_keys' in k), 'channel key reached the account');
  });

  await step('settings sync: a favorite removed on device 1 is gone on device 2', async () => {
    await d1.evaluate(() => localStorage.setItem('meshcore-favorites', '[]'));
    await until(async () => !((await accountKeys(d1))['meshcore-favorites'] || '').includes(SYNC_FAV), 'removal reached the account');
    await d2.evaluate(() => window.CSSettingsSync.syncNow());
    await d2.waitForFunction((pk) => !(localStorage.getItem('meshcore-favorites') || '').includes(pk), SYNC_FAV);
  });

  await step('settings sync: axe on the section and the logout dialog; Remove keeps the channel key', async () => {
    await d2.waitForSelector('#syncStatus');
    await axeClean(d2, '#syncSection');
    await d2.click('#accountPageLogout');
    await d2.waitForSelector('.cs-dialog');
    assert(await d2.evaluate(() => document.activeElement && document.activeElement.getAttribute('data-choice') === 'keep'), 'Keep is not focused');
    await axeClean(d2, '.cs-dialog');
    await d2.click('.cs-dialog [data-choice="remove"]');
    await d2.waitForSelector('#loginForm');
    const left = await d2.evaluate(() => ({
      fav: localStorage.getItem('meshcore-favorites'), tw: localStorage.getItem('meshcore-time-window'),
      base: localStorage.getItem('cs-settings-sync-base'), ch: localStorage.getItem('corescope_channel_keys'),
    }));
    assert(left.fav === null && left.tw === null && left.base === null, 'synced keys left: ' + JSON.stringify(left));
    assert(left.ch && left.ch.includes('#e2e'), 'channel key removed');
  });

  await step('admin disables the user; the live session is logged out without a reload', async () => {
    await admin.goto(BASE + '/#/admin/users');
    const row = admin.locator('tr[data-email="user@e2e.test"]');
    await row.waitFor();
    await row.locator('button[data-act="disable"]').click();
    await admin.waitForSelector('tr[data-email="user@e2e.test"] .um-status-disabled');
    // No reload: leave and re-enter the account page; its sessions call answers 401.
    await user.goto(BASE + '/#/home');
    await user.goto(BASE + '/#/account');
    // The 60 s settings pull may log the user out first, so assert the end state only.
    await user.waitForSelector('#accountToggle .nav-account-label:has-text("Log in")');
    assert(await user.evaluate(() => window.CS_USER === null), 'CS_USER is not null');
  });

  await step('axe: no serious or critical violations on the new views', async () => {
    for (const [pg, route, sel] of [[user, '/#/account/login', '#loginForm'], [admin, '/#/admin/users', '.um-table']]) {
      await pg.goto(BASE + route);
      await pg.waitForSelector(sel);
      await authReady(pg);
      await pg.waitForTimeout(1500);
      await axeClean(pg, '#app');
    }
  });

  await browser.close();
  console.log('\n' + passed + '/' + (passed + failed) + ' tests passed');
  process.exit(failed > 0 ? 1 : 0);
})();
