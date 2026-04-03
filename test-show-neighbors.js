/**
 * Show Neighbors E2E tests (#484 fix)
 * Tests that selectReferenceNode() uses the affinity API instead of client-side path walking.
 * Usage: CHROMIUM_PATH=/usr/bin/chromium-browser BASE_URL=http://localhost:13590 node test-show-neighbors.js
 */
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';
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
  const launchOpts = { headless: true, args: ['--no-sandbox', '--disable-gpu'] };
  if (process.env.CHROMIUM_PATH) launchOpts.executablePath = process.env.CHROMIUM_PATH;
  const browser = await chromium.launch(launchOpts);
  const page = await browser.newPage();

  console.log(`\nRunning Show Neighbors tests against ${BASE}\n`);

  await test('Show Neighbors calls affinity API and populates neighborPubkeys', async () => {
    const testPubkey = 'aabbccdd11223344556677889900aabbccddeeff00112233445566778899001122';
    const neighborPubkey1 = '1111111111111111111111111111111111111111111111111111111111111111';
    const neighborPubkey2 = '2222222222222222222222222222222222222222222222222222222222222222';

    let apiCalled = false;
    await page.route(`**/api/nodes/${testPubkey}/neighbors*`, route => {
      apiCalled = true;
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          node: testPubkey,
          neighbors: [
            { pubkey: neighborPubkey1, prefix: '11', name: 'Neighbor-1', role: 'repeater', count: 50, score: 0.9, ambiguous: false },
            { pubkey: neighborPubkey2, prefix: '22', name: 'Neighbor-2', role: 'companion', count: 20, score: 0.7, ambiguous: false }
          ],
          total_observations: 70
        })
      });
    });

    await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(2000);

    const result = await page.evaluate(async (args) => {
      if (typeof window._mapSelectRefNode !== 'function') return { error: 'no _mapSelectRefNode function' };
      if (typeof window._mapGetNeighborPubkeys !== 'function') return { error: 'no _mapGetNeighborPubkeys function' };
      await window._mapSelectRefNode(args.pk, 'TestNode');
      return { neighbors: window._mapGetNeighborPubkeys() };
    }, { pk: testPubkey });

    assert(!result.error, result.error || '');
    assert(apiCalled, 'The /neighbors API should have been called');
    assert(result.neighbors.includes(neighborPubkey1), `Should contain neighbor1, got: ${JSON.stringify(result.neighbors)}`);
    assert(result.neighbors.includes(neighborPubkey2), `Should contain neighbor2, got: ${JSON.stringify(result.neighbors)}`);
    assert(result.neighbors.length === 2, `Should have exactly 2 neighbors, got ${result.neighbors.length}`);
    await page.unroute(`**/api/nodes/${testPubkey}/neighbors*`);
  });

  await test('Show Neighbors resolves correct node on hash collision via affinity API', async () => {
    const nodeA = 'c0dedad4208acb6cbe44b848943fc6d3c5d43cf38a21e48b43826a70862980e4';
    const nodeB = 'c0f1a2b3000000000000000000000000000000000000000000000000000000ff';
    const neighborR1 = 'r1aaaaaa000000000000000000000000000000000000000000000000000000aa';
    const neighborR2 = 'r2bbbbbb000000000000000000000000000000000000000000000000000000bb';
    const neighborR4 = 'r4dddddd000000000000000000000000000000000000000000000000000000dd';

    await page.route(`**/api/nodes/${nodeA}/neighbors*`, route => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          node: nodeA,
          neighbors: [
            { pubkey: neighborR1, prefix: 'R1', name: 'Repeater-R1', role: 'repeater', count: 100, score: 0.95, ambiguous: false },
            { pubkey: neighborR2, prefix: 'R2', name: 'Repeater-R2', role: 'repeater', count: 80, score: 0.85, ambiguous: false }
          ],
          total_observations: 180
        })
      });
    });

    await page.route(`**/api/nodes/${nodeB}/neighbors*`, route => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          node: nodeB,
          neighbors: [
            { pubkey: neighborR4, prefix: 'R4', name: 'Repeater-R4', role: 'repeater', count: 60, score: 0.75, ambiguous: false }
          ],
          total_observations: 60
        })
      });
    });

    await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(2000);

    // Select Node A — should get R1, R2 but NOT R4
    const resultA = await page.evaluate(async (pk) => {
      await window._mapSelectRefNode(pk, 'NodeA');
      return window._mapGetNeighborPubkeys();
    }, nodeA);
    assert(resultA.includes(neighborR1), 'Node A should have R1 as neighbor');
    assert(resultA.includes(neighborR2), 'Node A should have R2 as neighbor');
    assert(!resultA.includes(neighborR4), 'Node A should NOT have R4 (that belongs to Node B)');

    // Select Node B — should get R4 but NOT R1, R2
    const resultB = await page.evaluate(async (pk) => {
      await window._mapSelectRefNode(pk, 'NodeB');
      return window._mapGetNeighborPubkeys();
    }, nodeB);
    assert(resultB.includes(neighborR4), 'Node B should have R4 as neighbor');
    assert(!resultB.includes(neighborR1), 'Node B should NOT have R1 (that belongs to Node A)');
    assert(!resultB.includes(neighborR2), 'Node B should NOT have R2 (that belongs to Node A)');

    await page.unroute(`**/api/nodes/${nodeA}/neighbors*`);
    await page.unroute(`**/api/nodes/${nodeB}/neighbors*`);
  });

  await test('Show Neighbors falls back to path walking when affinity API returns empty', async () => {
    const testPubkey = 'fallbacktest0000000000000000000000000000000000000000000000000000';
    const hopBefore = 'aaaa000000000000000000000000000000000000000000000000000000000000';
    const hopAfter = 'bbbb000000000000000000000000000000000000000000000000000000000000';

    let neighborApiCalled = false;
    let pathsApiCalled = false;

    await page.route(`**/api/nodes/${testPubkey}/neighbors*`, route => {
      neighborApiCalled = true;
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ node: testPubkey, neighbors: [], total_observations: 0 })
      });
    });

    await page.route(`**/api/nodes/${testPubkey}/paths*`, route => {
      pathsApiCalled = true;
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          paths: [{
            hops: [
              { pubkey: hopBefore, name: 'HopBefore' },
              { pubkey: testPubkey, name: 'Self' },
              { pubkey: hopAfter, name: 'HopAfter' }
            ]
          }]
        })
      });
    });

    await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(2000);

    const result = await page.evaluate(async (pk) => {
      if (typeof window._mapSelectRefNode !== 'function') return { error: 'no-function' };
      await window._mapSelectRefNode(pk, 'FallbackNode');
      return { neighbors: window._mapGetNeighborPubkeys() };
    }, testPubkey);

    assert(!result.error, result.error || '');
    assert(neighborApiCalled, 'Should try neighbor API first');
    assert(pathsApiCalled, 'Should fall back to paths API when neighbors empty');
    assert(result.neighbors.includes(hopBefore), 'Fallback should find hopBefore as neighbor');
    assert(result.neighbors.includes(hopAfter), 'Fallback should find hopAfter as neighbor');
    assert(result.neighbors.length === 2, `Fallback should find exactly 2 neighbors, got ${result.neighbors.length}`);
    await page.unroute(`**/api/nodes/${testPubkey}/neighbors*`);
    await page.unroute(`**/api/nodes/${testPubkey}/paths*`);
  });

  await test('Show Neighbors includes ambiguous candidates in neighborPubkeys', async () => {
    const testPubkey = 'ambigtest000000000000000000000000000000000000000000000000000000';
    const candidate1 = 'a3b4c500000000000000000000000000000000000000000000000000000000';
    const candidate2 = 'a3f0e100000000000000000000000000000000000000000000000000000000';
    const knownNeighbor = 'b7e8f9a000000000000000000000000000000000000000000000000000000000';

    await page.route(`**/api/nodes/${testPubkey}/neighbors*`, route => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          node: testPubkey,
          neighbors: [
            { pubkey: knownNeighbor, prefix: 'B7', name: 'Known-Neighbor', role: 'repeater', count: 100, score: 0.95, ambiguous: false },
            { pubkey: null, prefix: 'A3', name: null, role: null, count: 12, score: 0.08, ambiguous: true,
              candidates: [
                { pubkey: candidate1, name: 'Node-Alpha', role: 'companion' },
                { pubkey: candidate2, name: 'Node-Beta', role: 'companion' }
              ]
            }
          ],
          total_observations: 112
        })
      });
    });

    await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(2000);

    const result = await page.evaluate(async (pk) => {
      await window._mapSelectRefNode(pk, 'AmbigNode');
      return window._mapGetNeighborPubkeys();
    }, testPubkey);

    // Should include the known neighbor AND both ambiguous candidates
    assert(result.includes(knownNeighbor), 'Should include known neighbor');
    assert(result.includes(candidate1), 'Should include ambiguous candidate 1');
    assert(result.includes(candidate2), 'Should include ambiguous candidate 2');
    assert(result.length === 3, `Should have 3 neighbors (1 known + 2 candidates), got ${result.length}`);
    await page.unroute(`**/api/nodes/${testPubkey}/neighbors*`);
  });

  await browser.close();

  const passed = results.filter(r => r.pass).length;
  const failed = results.filter(r => !r.pass).length;
  console.log(`\n${passed}/${results.length} tests passed${failed ? `, ${failed} failed` : ''}`);
  process.exit(failed > 0 ? 1 : 0);
}

run().catch(err => {
  console.error('Fatal error:', err);
  process.exit(1);
});
