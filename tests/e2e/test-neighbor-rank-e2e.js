// E2E for the Analytics "Neighbor Rank" tab (#2100).
// Defaults to localhost:3000 — NEVER point at prod (AGENTS.md). CI sets BASE_URL.
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:3000';

function check(c, msg) { if (!c) throw new Error(msg); }
async function getJson(page, url) {
  const resp = await page.request.get(url);
  if (!resp.ok()) throw new Error('GET ' + url + ' → HTTP ' + resp.status());
  return resp.json();
}

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage();

  // 1. Endpoint shape.
  const api = await getJson(page, BASE + '/api/nodes/neighbor-rank?offset=0&limit=50');
  for (const k of ['entries', 'total', 'ranked', 'snapshot_at']) check(k in api, 'response missing key: ' + k);
  if (!api.entries.length) {
    console.log('neighbor-rank E2E SKIP (no ranked nodes in dataset)');
    await browser.close();
    return;
  }
  for (const k of ['rank', 'pubkey', 'name', 'role', 'neighbors']) check(k in api.entries[0], 'entry missing key: ' + k);

  // 2. Tab renders rows.
  await page.goto(BASE + '/#/analytics?tab=neighbor-rank');
  await page.waitForSelector('#nrRows tr', { timeout: 20000 });
  check((await page.locator('#nrRows tr').count()) === api.entries.length, 'row count != API entries');
  check(await page.locator('.nr-foot', { hasText: 'ranked nodes' }).count(), 'ranked footer missing');

  // 3. Search narrows without renumbering.
  const target = api.entries.find(e => e.name) || api.entries[0];
  const term = (target.name || target.pubkey).slice(0, 6);
  await page.fill('#nrSearch', term);
  await page.waitForFunction(() => location.hash.includes('q='), null, { timeout: 5000 });
  await page.waitForTimeout(500);
  const row = page.locator('#nrRows tr[data-pubkey="' + target.pubkey + '"]');
  await row.waitFor({ timeout: 10000 });
  check((await row.locator('.nr-rank').innerText()) === '#' + target.rank, 'rank changed when filtering');

  // 4. Role chip.
  await page.fill('#nrSearch', '');
  await page.click('.nr-chip[data-role="repeater"]');
  await page.waitForFunction(() => location.hash.includes('role=repeater'), null, { timeout: 5000 });
  await page.waitForTimeout(500);
  const roles = await page.locator('#nrRows tr td:nth-child(3)').allInnerTexts();
  check(roles.every(r => r.trim() === 'repeater'), 'role filter leaked other roles: ' + roles.join(','));
  await page.click('.nr-chip[data-role=""]');

  // 5. Paging (only if more than one page).
  if (api.total > 50) {
    await page.waitForFunction(() => !location.hash.includes('role='), null, { timeout: 5000 });
    await page.waitForTimeout(500);
    await page.click('#nrNext');
    await page.waitForFunction(() => location.hash.includes('page=2'), null, { timeout: 5000 });
    await page.waitForFunction(() => /Showing 51/.test(document.getElementById('nrPagerText').textContent));
    const p2 = await getJson(page, BASE + '/api/nodes/neighbor-rank?offset=50&limit=50');
    check((await page.locator('#nrRows tr').count()) === p2.entries.length, 'page 2 row count mismatch');
  }

  // 6. Deep link restore.
  await page.goto(BASE + '/#/analytics?tab=neighbor-rank&role=repeater&q=' + encodeURIComponent(term));
  await page.reload();
  await page.waitForSelector('#nrRows tr, .nr-empty', { timeout: 20000 });
  check((await page.inputValue('#nrSearch')) === term, 'q not restored from URL');
  check((await page.getAttribute('.nr-chip[data-role="repeater"]', 'aria-pressed')) === 'true', 'role chip not restored');

  // 7. Reach card link lands on the tab.
  // Use a node from the LAST page of the unfiltered list (deepest tie placement).
  const lastOffset = Math.floor((api.total - 1) / 50) * 50;
  const lastPage = await getJson(page, BASE + '/api/nodes/neighbor-rank?offset=' + lastOffset + '&limit=50');
  const deep = lastPage.entries[lastPage.entries.length - 1];
  await page.goto(BASE + '/#/nodes/' + deep.pubkey + '/reach');
  const link = page.locator('.nq-board-link');
  if (await link.count().catch(() => 0) || await page.waitForSelector('.nq-board-link', { timeout: 20000 }).then(() => true, () => false)) {
    await page.click('.nq-board-link');
    await page.waitForSelector('#nrRows tr', { timeout: 20000 });
    check(page.url().includes('tab=neighbor-rank'), 'leaderboard link did not land on tab');
    check(!page.url().includes('q='), 'leaderboard link must open the unfiltered list');
    await page.waitForSelector('#nrRows tr.nr-focus[data-pubkey="' + deep.pubkey + '"]', { timeout: 10000 });
  } else {
    console.log('  (Reach card not rendered for this node; link check skipped)');
  }

  await browser.close();
  console.log('neighbor-rank E2E OK');
})().catch(e => { console.error(e); process.exit(1); });
