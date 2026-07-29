// test-network-digest-tool.js — vm.createContext sandbox tests for
// public/network-digest.js (Tools > Network Digest page).
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
async function test(name, fn) {
  try {
    await fn();
    passed++;
    console.log(`  ✅ ${name}`);
  } catch (e) {
    failed++;
    console.log(`  ❌ ${name}: ${e.message}`);
  }
}

function makeDigest(overrides) {
  return Object.assign({
    window: '7d',
    since: '2026-07-22T10:00:00Z',
    newNodes: 3,
    roleChanges: 1,
    nameChanges: 0,
    positionMoves: 2,
    resurrections: 1,
    areaBreakdown: [{ label: 'Area A', count: 2 }],
  }, overrides);
}

function createSandbox(digestFixture) {
  const docStore = {};
  const apiCalls = [];
  function fakeEl(id) {
    if (!docStore[id]) {
      docStore[id] = {
        id: id,
        innerHTML: '',
        textContent: '',
        value: '',
        dataset: {},
        _listeners: {},
        addEventListener: function (evt, fn) { this._listeners[evt] = fn; },
        querySelectorAll: function () { return []; },
        querySelector: function () { return null; },
      };
    }
    return docStore[id];
  }

  const sandbox = {
    window: {},
    document: {
      getElementById: (id) => fakeEl(id),
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    location: { hash: '#/tools/network-digest' },
    api: (path) => {
      apiCalls.push(path);
      return Promise.resolve(typeof digestFixture === 'function' ? digestFixture(path) : digestFixture);
    },
    URLSearchParams: URLSearchParams,
    registerPage: function () {},
    timeAgo: (iso) => 'TIME_AGO(' + iso + ')',
    encodeURIComponent: encodeURIComponent,
    console: console,
    __docStore: docStore,
    __apiCalls: apiCalls,
  };
  sandbox.self = sandbox;
  sandbox.globalThis = sandbox;
  const ctx = vm.createContext(sandbox);
  const src = fs.readFileSync(__dirname + '/public/network-digest.js', 'utf8');
  vm.runInContext(src, ctx);
  return sandbox;
}

function initWith(digestFixture) {
  const sb = createSandbox(digestFixture);
  const container = { innerHTML: '' };
  sb.window.NetworkDigestTool.init(container);
  sb.__container = container;
  return sb;
}

function waitForLoad() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

(async () => {
  console.log('\n=== network-digest.js: Network Digest tool page ===');

  await test('window.NetworkDigestTool exists with init/destroy', () => {
    const sb = createSandbox(makeDigest());
    assert.strictEqual(typeof sb.window.NetworkDigestTool.init, 'function');
    assert.strictEqual(typeof sb.window.NetworkDigestTool.destroy, 'function');
  });

  await test('init renders window tabs with 7d active by default and requests the 7d window', async () => {
    const sb = initWith(makeDigest());
    await waitForLoad();
    const html = sb.__container.innerHTML;
    assert.ok(html.includes('data-window="7d"'), `expected a 7d tab, got: ${html}`);
    assert.ok(/class="tab-btn active"[^>]*data-window="7d"|data-window="7d"[^>]*class="tab-btn active"/.test(html),
      `expected the 7d tab to be active by default, got: ${html}`);
    assert.ok(sb.__apiCalls[0].includes('window=7d'), `expected the initial load to request window=7d, got: ${sb.__apiCalls[0]}`);
  });

  await test('clicking a different window tab reloads data for that window', async () => {
    const sb = initWith(makeDigest());
    await waitForLoad();
    const tabs = sb.__docStore['network-digest-window-tabs'];
    tabs._listeners.click({ target: { closest: () => ({ dataset: { window: '30d' } }) } });
    await waitForLoad();
    assert.ok(sb.__apiCalls[sb.__apiCalls.length - 1].includes('window=30d'), `expected a reload for window=30d, got: ${sb.__apiCalls}`);
    assert.ok(tabs.innerHTML.includes('data-window="30d"'), 'expected the tabs to re-render');
    assert.ok(/class="tab-btn active"[^>]*data-window="30d"|data-window="30d"[^>]*class="tab-btn active"/.test(tabs.innerHTML),
      `expected the 30d tab to become active, got: ${tabs.innerHTML}`);
  });

  await test('clicking the already-active tab does not trigger a reload', async () => {
    const sb = initWith(makeDigest());
    await waitForLoad();
    const callsBefore = sb.__apiCalls.length;
    const tabs = sb.__docStore['network-digest-window-tabs'];
    tabs._listeners.click({ target: { closest: () => ({ dataset: { window: '7d' } }) } });
    await waitForLoad();
    assert.strictEqual(sb.__apiCalls.length, callsBefore, 'expected no additional api call for a no-op tab click');
  });

  await test('init renders origin tabs with All active by default and requests origin=all', async () => {
    const sb = initWith(makeDigest());
    await waitForLoad();
    const html = sb.__container.innerHTML;
    assert.ok(html.includes('data-origin="all"'), `expected an All tab, got: ${html}`);
    assert.ok(/class="tab-btn active"[^>]*data-origin="all"|data-origin="all"[^>]*class="tab-btn active"/.test(html),
      `expected the All tab to be active by default, got: ${html}`);
    assert.ok(sb.__apiCalls[0].includes('origin=all'), `expected the initial load to request origin=all, got: ${sb.__apiCalls[0]}`);
  });

  await test('clicking a different origin tab reloads data for that origin', async () => {
    const sb = initWith(makeDigest());
    await waitForLoad();
    const tabs = sb.__docStore['network-digest-origin-tabs'];
    tabs._listeners.click({ target: { closest: () => ({ dataset: { origin: 'foreign' } }) } });
    await waitForLoad();
    assert.ok(sb.__apiCalls[sb.__apiCalls.length - 1].includes('origin=foreign'), `expected a reload for origin=foreign, got: ${sb.__apiCalls}`);
    assert.ok(tabs.innerHTML.includes('data-origin="foreign"'), 'expected the tabs to re-render');
    assert.ok(/class="tab-btn active"[^>]*data-origin="foreign"|data-origin="foreign"[^>]*class="tab-btn active"/.test(tabs.innerHTML),
      `expected the Foreign tab to become active, got: ${tabs.innerHTML}`);
  });

  await test('clicking the already-active origin tab does not trigger a reload', async () => {
    const sb = initWith(makeDigest());
    await waitForLoad();
    const callsBefore = sb.__apiCalls.length;
    const tabs = sb.__docStore['network-digest-origin-tabs'];
    tabs._listeners.click({ target: { closest: () => ({ dataset: { origin: 'all' } }) } });
    await waitForLoad();
    assert.strictEqual(sb.__apiCalls.length, callsBefore, 'expected no additional api call for a no-op origin tab click');
  });

  await test('window and origin filters are independent -- switching one keeps the other', async () => {
    const sb = initWith(makeDigest());
    await waitForLoad();
    const windowTabs = sb.__docStore['network-digest-window-tabs'];
    const originTabs = sb.__docStore['network-digest-origin-tabs'];
    originTabs._listeners.click({ target: { closest: () => ({ dataset: { origin: 'domestic' } }) } });
    await waitForLoad();
    windowTabs._listeners.click({ target: { closest: () => ({ dataset: { window: '24h' } }) } });
    await waitForLoad();
    const lastCall = sb.__apiCalls[sb.__apiCalls.length - 1];
    assert.ok(lastCall.includes('window=24h') && lastCall.includes('origin=domestic'),
      `expected the last request to carry both filters, got: ${lastCall}`);
  });

  await test('renders a stat tile for each of the five counts, linking to the right drill-down tool', async () => {
    const sb = initWith(makeDigest());
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(content.innerHTML.includes('href="#/tools/new-nodes"'), 'expected a link to New Nodes');
    assert.ok(content.innerHTML.includes('href="#/tools/node-changes"'), 'expected links to Node Changes');
    assert.ok(content.innerHTML.includes('New Nodes'), 'expected the New Nodes label');
    assert.ok(content.innerHTML.includes('Role Changes'), 'expected the Role Changes label');
    assert.ok(content.innerHTML.includes('Name Changes'), 'expected the Name Changes label');
    assert.ok(content.innerHTML.includes('Position Moves'), 'expected the Position Moves label');
    assert.ok(content.innerHTML.includes('Returned'), 'expected the Returned (resurrections) label');
    assert.ok(content.innerHTML.includes('>3<'), `expected the newNodes value 3 to render, got: ${content.innerHTML}`);
  });

  await test('renders the area breakdown card with the area label and count', async () => {
    const sb = initWith(makeDigest({ areaBreakdown: [{ label: 'Area A', count: 2 }] }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(content.innerHTML.includes('Area A'), `expected the area label, got: ${content.innerHTML}`);
    assert.ok(content.innerHTML.includes('2 new nodes'), `expected the pluralized count, got: ${content.innerHTML}`);
  });

  await test('singular "new node" wording when an area count is 1', async () => {
    const sb = initWith(makeDigest({ areaBreakdown: [{ label: 'Area B', count: 1 }] }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(content.innerHTML.includes('1 new node') && !content.innerHTML.includes('1 new nodes'),
      `expected singular wording, got: ${content.innerHTML}`);
  });

  await test('renders EVERY area in the breakdown, not just the top one', async () => {
    const sb = initWith(makeDigest({ areaBreakdown: [{ label: 'Area A', count: 5 }, { label: 'Area B', count: 3 }, { label: 'Area C', count: 1 }] }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(content.innerHTML.includes('Area A'), 'expected Area A to render');
    assert.ok(content.innerHTML.includes('Area B'), 'expected Area B to render too, not just the winner');
    assert.ok(content.innerHTML.includes('Area C'), 'expected Area C to render too');
  });

  await test('collapses to 10 areas with a "Show all" toggle when there are more than 10', async () => {
    const many = Array.from({ length: 15 }, (_, i) => ({ label: 'Area ' + i, count: 15 - i }));
    const sb = initWith(makeDigest({ areaBreakdown: many }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(content.innerHTML.includes('Area 0'), 'expected the top area to render');
    assert.ok(content.innerHTML.includes('Area 9'), 'expected the 10th area (index 9) to render');
    assert.ok(!content.innerHTML.includes('Area 10'), 'expected the 11th area to be collapsed away');
    assert.ok(content.innerHTML.includes('Show all 15 areas'), `expected a "Show all 15 areas" toggle, got: ${content.innerHTML}`);
  });

  await test('no "Show all" toggle when there are 10 or fewer areas', async () => {
    const ten = Array.from({ length: 10 }, (_, i) => ({ label: 'Area ' + i, count: 10 - i }));
    const sb = initWith(makeDigest({ areaBreakdown: ten }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(!content.innerHTML.includes('Show all'), `expected no toggle button for exactly 10 areas, got: ${content.innerHTML}`);
  });

  // rerenderAreaBreakdown() (fired by the click handlers below) writes
  // directly into the #network-digest-area-breakdown-list element, not
  // into #network-digest-content's innerHTML -- this sandbox has no real
  // DOM tree, so document.getElementById returns an independent fakeEl
  // per id rather than something nested inside content's own markup.
  // Assert against that element specifically for click-driven updates.

  await test('clicking "Show all" via the delegated listener expands the collapsed areas', async () => {
    const many = Array.from({ length: 12 }, (_, i) => ({ label: 'Area ' + i, count: 12 - i }));
    const sb = initWith(makeDigest({ areaBreakdown: many }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(!content.innerHTML.includes('Area 11'), 'expected Area 11 to be collapsed away initially');
    content._listeners.click({ target: { closest: (sel) => (sel === '[data-area-toggle]' ? { dataset: {} } : null) } });
    const list = sb.__docStore['network-digest-area-breakdown-list'];
    assert.ok(list.innerHTML.includes('Area 11'), `expected Area 11 to appear after expanding, got: ${list.innerHTML}`);
    assert.ok(list.innerHTML.includes('Show fewer'), `expected the toggle label to flip to "Show fewer", got: ${list.innerHTML}`);
  });

  await test('clicking an area row reveals the node list with names and node links', async () => {
    const sb = initWith(makeDigest({
      areaBreakdown: [{
        label: 'Area A',
        count: 2,
        nodes: [
          { publicKey: 'aa'.repeat(32), name: 'FirstNode', firstSeen: '2026-07-22T09:00:00Z' },
          { publicKey: 'bb'.repeat(32), name: 'SecondNode', firstSeen: '2026-07-22T08:00:00Z' },
        ],
      }],
    }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(!content.innerHTML.includes('FirstNode'), 'expected the node list to be collapsed initially');
    content._listeners.click({ target: { closest: (sel) => (sel === '[data-area-label]' ? { dataset: { areaLabel: 'Area A' } } : null) } });
    const list = sb.__docStore['network-digest-area-breakdown-list'];
    assert.ok(list.innerHTML.includes('FirstNode'), `expected FirstNode to appear after clicking the area, got: ${list.innerHTML}`);
    assert.ok(list.innerHTML.includes('SecondNode'), 'expected SecondNode to appear too');
    assert.ok(list.innerHTML.includes('href="#/nodes/' + 'aa'.repeat(32) + '"'), 'expected a link to the node detail page');
  });

  await test('clicking the same area row again collapses the node list', async () => {
    const sb = initWith(makeDigest({
      areaBreakdown: [{ label: 'Area A', count: 1, nodes: [{ publicKey: 'cc'.repeat(32), name: 'ToggleNode', firstSeen: '2026-07-22T09:00:00Z' }] }],
    }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    const clickRow = () => content._listeners.click({ target: { closest: (sel) => (sel === '[data-area-label]' ? { dataset: { areaLabel: 'Area A' } } : null) } });
    clickRow();
    const list = sb.__docStore['network-digest-area-breakdown-list'];
    assert.ok(list.innerHTML.includes('ToggleNode'), 'expected the node list open after the first click');
    clickRow();
    assert.ok(!list.innerHTML.includes('ToggleNode'), `expected the node list closed after a second click, got: ${list.innerHTML}`);
  });

  await test('a node with no known name falls back to a truncated pubkey', async () => {
    const sb = initWith(makeDigest({
      areaBreakdown: [{ label: 'Area A', count: 1, nodes: [{ publicKey: 'deadbeefcafebabe0000000000000000000000000000000000000000000000', name: '', firstSeen: '2026-07-22T09:00:00Z' }] }],
    }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    content._listeners.click({ target: { closest: (sel) => (sel === '[data-area-label]' ? { dataset: { areaLabel: 'Area A' } } : null) } });
    const list = sb.__docStore['network-digest-area-breakdown-list'];
    assert.ok(list.innerHTML.includes('deadbeef'), `expected a truncated-pubkey fallback, got: ${list.innerHTML}`);
  });

  await test('expand state resets on a fresh load (window/origin switch)', async () => {
    const sb = initWith(makeDigest({
      areaBreakdown: [{ label: 'Area A', count: 1, nodes: [{ publicKey: 'ee'.repeat(32), name: 'ResetNode', firstSeen: '2026-07-22T09:00:00Z' }] }],
    }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    content._listeners.click({ target: { closest: (sel) => (sel === '[data-area-label]' ? { dataset: { areaLabel: 'Area A' } } : null) } });
    const list = sb.__docStore['network-digest-area-breakdown-list'];
    assert.ok(list.innerHTML.includes('ResetNode'), 'expected the node list open before switching windows');

    const windowTabs = sb.__docStore['network-digest-window-tabs'];
    windowTabs._listeners.click({ target: { closest: () => ({ dataset: { window: '30d' } }) } });
    await waitForLoad();
    assert.ok(!content.innerHTML.includes('ResetNode'), `expected the expand state to reset on a fresh load, got: ${content.innerHTML}`);
  });

  await test('omits the area breakdown card entirely when absent', async () => {
    const sb = initWith(makeDigest({ areaBreakdown: null }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(!content.innerHTML.includes('Area Breakdown'), `expected no area breakdown card, got: ${content.innerHTML}`);
  });

  await test('shows a neutral "nothing to report" message when every count is zero', async () => {
    const sb = initWith(makeDigest({ newNodes: 0, roleChanges: 0, nameChanges: 0, positionMoves: 0, resurrections: 0, areaBreakdown: null }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(content.innerHTML.includes('Nothing to report'), `expected the all-zero message, got: ${content.innerHTML}`);
    assert.ok(!content.innerHTML.includes('stats-grid'), 'should not render the stat tiles when everything is zero');
  });

  await test('appends "+" to the New Nodes tile when newNodesCapped is true', async () => {
    const sb = initWith(makeDigest({ newNodes: 500, newNodesCapped: true }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(content.innerHTML.includes('>500+<'), `expected a "500+" value, got: ${content.innerHTML}`);
  });

  await test('does not append "+" when newNodesCapped is false', async () => {
    const sb = initWith(makeDigest({ newNodes: 39, newNodesCapped: false }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(content.innerHTML.includes('>39<'), `expected a plain "39" value, got: ${content.innerHTML}`);
    assert.ok(!content.innerHTML.includes('39+'), 'should not append + when not capped');
  });

  await test('appends "+" to all four change tiles when changesCapped is true', async () => {
    const sb = initWith(makeDigest({ roleChanges: 500, nameChanges: 500, positionMoves: 500, resurrections: 500, changesCapped: true }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    const plusCount = (content.innerHTML.match(/500\+/g) || []).length;
    assert.strictEqual(plusCount, 4, `expected all 4 change tiles to show "500+", got ${plusCount} in: ${content.innerHTML}`);
  });

  await test('status line notes possible undercounts when a cap was hit', async () => {
    const sb = initWith(makeDigest({ newNodesCapped: true }));
    await waitForLoad();
    const status = sb.__docStore['network-digest-status'];
    assert.ok(status.textContent.includes('may be higher'), `expected a caveat note, got: ${status.textContent}`);
  });

  await test('status line has no caveat note when nothing is capped', async () => {
    const sb = initWith(makeDigest({ newNodesCapped: false, changesCapped: false }));
    await waitForLoad();
    const status = sb.__docStore['network-digest-status'];
    assert.ok(!status.textContent.includes('may be higher'), `expected no caveat note, got: ${status.textContent}`);
  });

  await test('status line reports the since timestamp', async () => {
    const sb = initWith(makeDigest({ since: '2026-07-22T10:00:00Z' }));
    await waitForLoad();
    const status = sb.__docStore['network-digest-status'];
    assert.ok(status.textContent.includes('2026-07-22T10:00:00Z'), `expected the since timestamp, got: ${status.textContent}`);
    assert.ok(status.textContent.includes('TIME_AGO(2026-07-22T10:00:00Z)'), `expected timeAgo formatting, got: ${status.textContent}`);
  });

  await test('a failed load shows an error message, not a stuck loading state', async () => {
    const sb = initWith(() => Promise.reject(new Error('boom')));
    await waitForLoad();
    const status = sb.__docStore['network-digest-status'];
    const content = sb.__docStore['network-digest-content'];
    assert.ok(status.textContent.includes('Failed to load'), `expected an error message, got: ${status.textContent}`);
    assert.ok(!content.innerHTML.includes('Loading'), 'should not still show the loading placeholder after failure');
  });

  console.log('\n════════════════════════════════════════');
  console.log(`  Network Digest tool: ${passed} passed, ${failed} failed`);
  console.log('════════════════════════════════════════');
  process.exit(failed === 0 ? 0 : 1);
})();
