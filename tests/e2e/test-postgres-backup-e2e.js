/**
 * Native SQLite/PostgreSQL backup and restored-browser-session proof.
 * Run test-user-management-e2e.js first to create its synthetic accounts.
 *
 * BASE_URL=http://localhost:13582 E2E_BACKUP_DIR=<private absolute directory>
 *   node tests/e2e/test-postgres-backup-e2e.js capture
 * Restore telemetry.dump and accounts.dump into fresh PostgreSQL databases,
 * grant the restricted runtime roles, and restart the fixture server against
 * those databases. Then use the SAME directory and local browser origin:
 *   node tests/e2e/test-postgres-backup-e2e.js verify
 *
 * The directory contains account snapshots and session cookies. Keep it private
 * and outside the checkout; never publish it as a CI artifact.
 */
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { isDeepStrictEqual } = require('node:util');
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13582';
const DIR = process.env.E2E_BACKUP_DIR || '';
const MODE = process.argv[2];
const BACKEND = process.env.CORESCOPE_TEST_BACKEND || 'postgres';
assert(['sqlite','postgres'].includes(BACKEND), 'explicit supported backup backend required');
const EXTENSION = BACKEND === 'sqlite' ? 'db' : 'dump';
const HASH = 'fae0c9e6d357a814';
const ACCOUNTS = ['admin@e2e.test', 'sync@e2e.test'];
let passed = 0;

function pass(name) { passed++; console.log('  ✓ ' + name); }
function save(name, value) {
  fs.writeFileSync(path.join(DIR, name), JSON.stringify(value), { mode: 0o600, flag: 'wx' });
}
async function json(page, route) {
  const response = await page.request.get(BASE + route);
  assert.equal(response.status(), 200, route + ' status');
  return response.json();
}
async function telemetry(page) {
  const stats = await json(page, '/api/stats');
  const counts = {};
  for (const key of ['totalTransmissions', 'totalObservations', 'totalNodes', 'totalObservers']) {
    assert(Number.isInteger(stats[key]) && stats[key] > 0, key + ' must contain fixture data');
    counts[key] = stats[key];
  }
  const detail = await json(page, '/api/packets/' + HASH);
  assert.equal(detail.packet.hash, HASH);
  assert.equal(detail.observations.length, 3, 'seeded observation count');
  return { counts, hash: detail.packet.hash, observations: detail.observations.map(o => ({
    id: o.id, raw_hex: o.raw_hex, path_json: o.path_json,
  })).sort((a, b) => a.id - b.id) };
}

(async () => {
  assert(['capture', 'verify'].includes(MODE), 'use capture or verify');
  assert(['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname), 'local fixture server required');
  assert(path.isAbsolute(DIR), 'E2E_BACKUP_DIR must be a private absolute directory');
  const repo = path.resolve(__dirname, '../..');
  const relative = path.relative(repo, DIR);
  assert(relative.startsWith('..' + path.sep) || path.isAbsolute(relative), 'backup directory must be outside the checkout');
  fs.mkdirSync(DIR, { recursive: true, mode: 0o700 });
  const expected = MODE === 'verify' ? JSON.parse(fs.readFileSync(path.join(DIR, 'expected.json'), 'utf8')) : { accounts: [] };
  const browser = await chromium.launch({ headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'] });
  try {
    const pages = [];
    for (let i = 0; i < ACCOUNTS.length; i++) {
      const statePath = path.join(DIR, 'browser-' + i + '.json');
      const context = await browser.newContext(MODE === 'verify' ? { storageState: statePath } : {});
      const page = await context.newPage();
      page.setDefaultTimeout(10000);
      if (MODE === 'capture') {
        await page.goto(BASE + '/#/account/login', { waitUntil: 'domcontentloaded' });
        await page.fill('#loginEmail', ACCOUNTS[i]);
        await page.fill('#loginPassword', 'correct horse battery');
        await page.click('#loginForm button[type="submit"]');
      } else {
        // No login request: the pre-backup cookie must resolve the restored session.
        await page.goto(BASE + '/#/account', { waitUntil: 'domcontentloaded' });
      }
      await page.waitForSelector('#profileForm');
      await page.evaluate(() => window.CSAuth.ready());
      if (MODE === 'capture') await page.evaluate(() => window.CSSettingsSync.syncNow());
      const identity = await json(page, '/api/auth/me');
      assert.equal(identity.email, ACCOUNTS[i]);
      const settings = (await json(page, '/api/account/settings')).doc;
      if (MODE === 'capture') {
        expected.accounts.push({ identity, settings });
        save('browser-' + i + '.json', await context.storageState());
      } else {
        // Keep session tokens and account contents out of failure output.
        assert(isDeepStrictEqual(identity, expected.accounts[i].identity), 'restored identity and CSRF token');
        assert(isDeepStrictEqual(settings, expected.accounts[i].settings), 'restored account settings');
      }
      pages.push(page);
      pass(MODE + ': ' + (i === 0 ? 'admin' : 'user') + ' session and settings');
    }
    assert.equal(expected.accounts[1].settings.keys['meshcore-time-window'], '180', 'synced fixture setting');
    const actualTelemetry = await telemetry(pages[0]);
    if (MODE === 'capture') expected.telemetry = actualTelemetry;
    else assert(isDeepStrictEqual(actualTelemetry, expected.telemetry), 'restored telemetry API identity and observation bytes');
    pass(MODE + ': telemetry counts and distinct observation bytes');

    const anonymous = await (await browser.newContext()).newPage();
    // Telemetry falls back to the API-key gate; account backups use withAdmin.
    for (const [route, filename, anonymousStatus] of [['/api/backup', 'telemetry.' + EXTENSION, 403], ['/api/admin/users-backup', 'accounts.' + EXTENSION, 401]]) {
      assert.equal((await anonymous.request.get(BASE + route)).status(), anonymousStatus, route + ' anonymous denial');
      assert.equal((await pages[1].request.get(BASE + route)).status(), 403, route + ' non-admin denial');
      const response = await pages[0].request.get(BASE + route);
      assert.equal(response.status(), 200, route + ' admin download');
      assert.match(response.headers()['content-disposition'], new RegExp('^attachment; filename="corescope-[^"]+\\.' + EXTENSION + '"$'));
      assert.equal(response.headers()['content-type'], 'application/octet-stream');
      assert.equal(response.headers()['cache-control'], 'no-store');
      const body = await response.body();
      const magic = BACKEND === 'sqlite' ? 'SQLite format 3\0' : 'PGDMP';
      assert.equal(body.subarray(0, Buffer.byteLength(magic)).toString(), magic, route + ' native archive magic');
      if (MODE === 'capture') fs.writeFileSync(path.join(DIR, filename), body, { mode: 0o600, flag: 'wx' });
      pass(MODE + ': protected native ' + filename + ' download');
    }
    if (MODE === 'capture') save('expected.json', expected);
  } finally {
    await browser.close();
  }
  console.log('\n' + passed + '/' + passed + ' tests passed');
})().catch(err => { console.error('PostgreSQL backup E2E failed: ' + err.message); process.exitCode = 1; });
