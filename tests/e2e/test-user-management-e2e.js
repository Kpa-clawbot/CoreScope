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
  await page.waitForSelector('#accountMsg.ok');
  const { to, token } = await lastMailToken(page);
  assert(to === email, 'activation mail went to ' + to);
  await page.goto(BASE + '/#/account/activate?token=' + encodeURIComponent(token));
  await page.waitForSelector('#activateForm');
  await page.fill('#actPassword', PW);
  await page.click('#activateForm button[type="submit"]');
  await page.waitForSelector('#accountToggle .nav-account-label:has-text("' + name + '")');
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
  await step('config admin registers, activates with a password, sees the Users entry', async () => {
    await registerAndActivate(admin, 'admin@e2e.test', 'E2E Admin');
    await admin.click('#accountToggle');
    assert(await admin.locator('#accountMenu a[href="#/admin/users"]').isVisible(), 'no Users menu entry');
  });

  const user = await (await browser.newContext()).newPage();
  user.setDefaultTimeout(8000);
  user.on('pageerror', (e) => console.error('[pageerror user]', e.message));
  await step('second user registers and activates; no Users entry for a non-admin', async () => {
    await registerAndActivate(user, 'user@e2e.test', 'E2E User');
    await user.click('#accountToggle');
    assert(await user.locator('#accountMenu').isVisible(), 'account menu did not open');
    assert(await user.locator('#accountMenu a[href="#/admin/users"]').count() === 0, 'non-admin sees Users');
  });

  await step('user logs out from the account page (phone width) and logs in again', async () => {
    // At phone width the header control is hidden: the page button is the only way out.
    await user.setViewportSize({ width: 375, height: 800 });
    await user.goto(BASE + '/#/account');
    await user.waitForSelector('#profileForm');
    await user.click('#accountPageLogout');
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

  await step('admin disables the user; the live session is logged out without a reload', async () => {
    await admin.goto(BASE + '/#/admin/users');
    const row = admin.locator('tr[data-email="user@e2e.test"]');
    await row.waitFor();
    await row.locator('button[data-act="disable"]').click();
    await admin.waitForSelector('tr[data-email="user@e2e.test"] .um-status-disabled');
    // No reload: leave and re-enter the account page; its sessions call answers 401.
    await user.goto(BASE + '/#/home');
    await user.goto(BASE + '/#/account');
    await user.waitForSelector('.cs-auth-toast.visible:has-text("You were logged out.")');
    await user.waitForSelector('#accountToggle .nav-account-label:has-text("Log in")');
  });

  await step('axe: no serious or critical violations on the new views', async () => {
    for (const [pg, route, sel] of [[user, '/#/account/login', '#loginForm'], [admin, '/#/admin/users', '.um-table']]) {
      await pg.goto(BASE + route);
      await pg.waitForSelector(sel);
      await authReady(pg);
      await pg.waitForTimeout(1500);
      const res = await new AxeBuilder({ page: pg }).include('#app').withTags(['wcag2a', 'wcag2aa']).analyze();
      const bad = res.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
      assert(bad.length === 0, route + ': ' + bad.map((v) => v.id + ' ' + v.nodes.map((n) => n.target.join(' ') + ' ' + ((n.any[0] || {}).message || '')).join(' | ')).join(', '));
    }
  });

  await browser.close();
  console.log('\n' + passed + '/' + (passed + failed) + ' tests passed');
  process.exit(failed > 0 ? 1 : 0);
})();
